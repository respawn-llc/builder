package llm

import (
	"context"
	"fmt"
	"strings"

	"core/shared/textutil"
	"core/shared/transcript"
)

type OpenAIRequest struct {
	Model                   string
	Temperature             float64
	MaxTokens               int
	ReasoningEffort         string
	SupportsReasoningEffort bool
	FastMode                bool
	EnableNativeWebSearch   bool
	SystemPrompt            string
	PromptCacheKey          string
	SessionID               *string
	CodexDispatch           *CodexDispatchContext
	Items                   []ResponseItem
	Tools                   []Tool
	ToolChoiceMode          ToolChoiceMode
	StructuredOutput        *StructuredOutput
}

// RequestAsOpenAI projects the provider-agnostic Request into the OpenAI-family
// provider DTO. It is the single source of truth for that projection: the
// OpenAIClient request methods and the offline inspection seam both use it, so
// the wire shape stays identical between live generation and captured payloads.
func RequestAsOpenAI(request Request) OpenAIRequest {
	var structuredOutput *StructuredOutput
	if request.StructuredOutput != nil {
		cloned := *request.StructuredOutput
		structuredOutput = &cloned
	}
	return OpenAIRequest{
		Model:                   request.Model,
		Temperature:             request.Temperature,
		MaxTokens:               request.MaxTokens,
		ReasoningEffort:         request.ReasoningEffort,
		SupportsReasoningEffort: request.SupportsReasoningEffort,
		FastMode:                request.FastMode,
		EnableNativeWebSearch:   request.EnableNativeWebSearch,
		SystemPrompt:            request.SystemPrompt,
		PromptCacheKey:          request.PromptCacheKey,
		SessionID:               request.SessionID,
		CodexDispatch:           request.CodexDispatch,
		Items:                   CloneResponseItems(request.Items),
		Tools:                   append([]Tool(nil), request.Tools...),
		ToolChoiceMode:          request.ToolChoiceMode,
		StructuredOutput:        structuredOutput,
	}
}

type OpenAIResponse struct {
	AssistantText     *string
	ProviderPhase     *ProviderPhase
	ServedModel       *string
	ReasoningIncluded bool
	ToolCalls         []ToolCall
	Reasoning         []ReasoningEntry
	ReasoningItems    []ReasoningItem
	OutputItems       []ResponseItem
	Usage             Usage
}

type OpenAICompactionRequest struct {
	Model          string
	Instructions   string
	PromptCacheKey string
	SessionID      *string
	FastMode       bool
	CodexDispatch  *CodexDispatchContext
	InputItems     []ResponseItem
}

type OpenAICompactionResponse struct {
	Checkpoint ResponseItem
	Usage      Usage
}

type OpenAITransport interface {
	Generate(ctx context.Context, request OpenAIRequest, callbacks StreamCallbacks) (OpenAIResponse, error)
	Compact(ctx context.Context, request OpenAICompactionRequest) (OpenAICompactionResponse, error)
}

type OpenAIModelContextWindowTransport interface {
	ResolveModelContextWindow(ctx context.Context, model string) (int, error)
}

type OpenAIProviderCapabilitiesTransport interface {
	ProviderCapabilities(ctx context.Context) (ProviderCapabilities, error)
}

type OpenAIClient struct {
	transport OpenAITransport
}

func NewOpenAIClient(transport OpenAITransport) *OpenAIClient {
	return &OpenAIClient{transport: transport}
}

func (c *OpenAIClient) Generate(ctx context.Context, request Request, callbacks StreamCallbacks) (Response, error) {
	if c == nil || c.transport == nil {
		return Response{}, ErrMissingTransport
	}
	if err := request.Validate(); err != nil {
		return Response{}, err
	}

	providerReq := RequestAsOpenAI(request)

	providerResp, err := c.transport.Generate(ctx, providerReq, callbacks)
	if err != nil {
		return Response{}, fmt.Errorf("openai generate: %w", err)
	}

	return responseFromOpenAI(providerResp)
}

func responseFromOpenAI(providerResp OpenAIResponse) (Response, error) {
	if providerResp.ProviderPhase == nil {
		return Response{}, fmt.Errorf("openai response omitted authoritative provider phase fact")
	}
	assistantPhase := providerPhaseProjection(providerResp.ProviderPhase)
	var typedAssistantPhase *MessagePhase
	if assistantPhase != "" {
		typedAssistantPhase = textutil.Value(assistantPhase)
	}
	return Response{
		Assistant: Message{
			Role:           RoleAssistant,
			Content:        resolveAssistantContent(RoleAssistant, assistantPhase, providerResp.AssistantText),
			Phase:          typedAssistantPhase,
			ToolCalls:      append([]ToolCall(nil), providerResp.ToolCalls...),
			ReasoningItems: append([]ReasoningItem(nil), providerResp.ReasoningItems...),
		},
		ProviderPhase:     providerResp.ProviderPhase,
		ServedModel:       textutil.Pointer(providerResp.ServedModel),
		ReasoningIncluded: providerResp.ReasoningIncluded,
		ToolCalls:         providerResp.ToolCalls,
		Reasoning:         append([]ReasoningEntry(nil), providerResp.Reasoning...),
		ReasoningItems:    append([]ReasoningItem(nil), providerResp.ReasoningItems...),
		OutputItems:       CloneResponseItems(providerResp.OutputItems),
		Usage:             providerResp.Usage,
	}, nil
}

func resolveAssistantContent(role Role, phase MessagePhase, content *string) *string {
	if content == nil {
		return nil
	}
	if strings.TrimSpace(*content) == "" &&
		!transcript.IsBlankAssistantFinal(transcript.AssistantFinalCandidate{
			IsAssistant: role == RoleAssistant,
			IsFinal:     phase == MessagePhaseFinal,
			Content:     content,
		}) {
		return nil
	}
	return textutil.Pointer(content)
}

func (c *OpenAIClient) Compact(ctx context.Context, request CompactionRequest) (CompactionResponse, error) {
	if c == nil || c.transport == nil {
		return CompactionResponse{}, ErrMissingTransport
	}
	if request.Model == "" {
		return CompactionResponse{}, fmt.Errorf("%w: compaction model is required", ErrInvalidRequest)
	}
	if err := validateSessionDispatchPairing(request.SessionID, request.CodexDispatch); err != nil {
		return CompactionResponse{}, err
	}

	providerReq := OpenAICompactionRequest{
		Model:          request.Model,
		Instructions:   request.Instructions,
		PromptCacheKey: request.PromptCacheKey,
		SessionID:      request.SessionID,
		FastMode:       request.FastMode,
		CodexDispatch:  request.CodexDispatch,
		InputItems:     CloneResponseItems(request.InputItems),
	}
	providerResp, err := c.transport.Compact(ctx, providerReq)
	if err != nil {
		return CompactionResponse{}, fmt.Errorf("openai compact: %w", err)
	}
	return CompactionResponse{
		Checkpoint: CloneResponseItems([]ResponseItem{providerResp.Checkpoint})[0],
		Usage:      providerResp.Usage,
	}, nil
}

func (c *OpenAIClient) ProviderCapabilities(ctx context.Context) (ProviderCapabilities, error) {
	if c == nil || c.transport == nil {
		return ProviderCapabilities{}, ErrMissingTransport
	}
	if transport, ok := c.transport.(OpenAIProviderCapabilitiesTransport); ok {
		return transport.ProviderCapabilities(ctx)
	}
	return ProviderCapabilities{}, fmt.Errorf("openai provider capabilities are not supported by transport %T", c.transport)
}

func (c *OpenAIClient) ResolveModelContextWindow(ctx context.Context, model string) (int, error) {
	if c == nil || c.transport == nil {
		return 0, ErrMissingTransport
	}
	resolver, ok := c.transport.(OpenAIModelContextWindowTransport)
	if !ok {
		return 0, fmt.Errorf("openai model context window resolution is not supported by transport")
	}
	return resolver.ResolveModelContextWindow(ctx, model)
}
