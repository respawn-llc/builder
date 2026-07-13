package llm

import (
	"context"
	"fmt"

	"core/shared/textutil"
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
	SessionID               string
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
		Items:                   CloneResponseItems(request.Items),
		Tools:                   append([]Tool(nil), request.Tools...),
		ToolChoiceMode:          request.ToolChoiceMode,
		StructuredOutput:        request.StructuredOutput,
	}
}

type OpenAIResponse struct {
	AssistantText  string
	AssistantPhase MessagePhase
	ToolCalls      []ToolCall
	Reasoning      []ReasoningEntry
	ReasoningItems []ReasoningItem
	OutputItems    []ResponseItem
	Usage          Usage
}

type OpenAICompactionRequest struct {
	Model        string
	Instructions string
	SessionID    string
	InputItems   []ResponseItem
}

type OpenAICompactionResponse struct {
	OutputItems       []ResponseItem
	Usage             Usage
	TrimmedItemsCount *int
}

type OpenAITransport interface {
	Generate(ctx context.Context, request OpenAIRequest) (OpenAIResponse, error)
	Compact(ctx context.Context, request OpenAICompactionRequest) (OpenAICompactionResponse, error)
}

type OpenAIInputTokenCountTransport interface {
	CountRequestInputTokens(ctx context.Context, request OpenAIRequest) (int, error)
}

type OpenAIInputTokenCountSupportTransport interface {
	SupportsRequestInputTokenCount(ctx context.Context) (bool, error)
}

type OpenAIModelContextWindowTransport interface {
	ResolveModelContextWindow(ctx context.Context, model string) (int, error)
}

type OpenAIStreamingTransport interface {
	GenerateStream(ctx context.Context, request OpenAIRequest, onDelta func(text string)) (OpenAIResponse, error)
}

type OpenAIStreamingEventsTransport interface {
	GenerateStreamWithEvents(ctx context.Context, request OpenAIRequest, callbacks StreamCallbacks) (OpenAIResponse, error)
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

func (c *OpenAIClient) Generate(ctx context.Context, request Request) (Response, error) {
	if c == nil || c.transport == nil {
		return Response{}, ErrMissingTransport
	}
	if err := request.Validate(); err != nil {
		return Response{}, err
	}

	providerReq := RequestAsOpenAI(request)

	providerResp, err := c.transport.Generate(ctx, providerReq)
	if err != nil {
		return Response{}, fmt.Errorf("openai generate: %w", err)
	}

	return Response{
		Assistant: Message{
			Role:           RoleAssistant,
			Content:        providerResp.AssistantText,
			Phase:          providerResp.AssistantPhase,
			ToolCalls:      append([]ToolCall(nil), providerResp.ToolCalls...),
			ReasoningItems: append([]ReasoningItem(nil), providerResp.ReasoningItems...),
		},
		ToolCalls:      providerResp.ToolCalls,
		Reasoning:      append([]ReasoningEntry(nil), providerResp.Reasoning...),
		ReasoningItems: append([]ReasoningItem(nil), providerResp.ReasoningItems...),
		OutputItems:    CloneResponseItems(providerResp.OutputItems),
		Usage:          providerResp.Usage,
	}, nil
}

func (c *OpenAIClient) GenerateStream(ctx context.Context, request Request, onDelta func(text string)) (Response, error) {
	var callback func(AssistantDelta)
	if onDelta != nil {
		callback = func(delta AssistantDelta) {
			onDelta(delta.Text)
		}
	}
	return c.GenerateStreamWithEvents(ctx, request, StreamCallbacks{OnAssistantDelta: callback})
}

func (c *OpenAIClient) GenerateStreamWithEvents(ctx context.Context, request Request, callbacks StreamCallbacks) (Response, error) {
	if c == nil || c.transport == nil {
		return Response{}, ErrMissingTransport
	}
	if err := request.Validate(); err != nil {
		return Response{}, err
	}

	providerReq := RequestAsOpenAI(request)

	if streamTransport, ok := c.transport.(OpenAIStreamingEventsTransport); ok {
		providerResp, err := streamTransport.GenerateStreamWithEvents(ctx, providerReq, callbacks)
		if err != nil {
			return Response{}, fmt.Errorf("openai generate stream: %w", err)
		}
		return Response{
			Assistant: Message{
				Role:           RoleAssistant,
				Content:        providerResp.AssistantText,
				Phase:          providerResp.AssistantPhase,
				ToolCalls:      append([]ToolCall(nil), providerResp.ToolCalls...),
				ReasoningItems: append([]ReasoningItem(nil), providerResp.ReasoningItems...),
			},
			ToolCalls:      providerResp.ToolCalls,
			Reasoning:      append([]ReasoningEntry(nil), providerResp.Reasoning...),
			ReasoningItems: append([]ReasoningItem(nil), providerResp.ReasoningItems...),
			OutputItems:    CloneResponseItems(providerResp.OutputItems),
			Usage:          providerResp.Usage,
		}, nil
	}

	if streamTransport, ok := c.transport.(OpenAIStreamingTransport); ok {
		var onTextDelta func(string)
		if callbacks.OnAssistantDelta != nil {
			onTextDelta = func(text string) {
				callbacks.OnAssistantDelta(AssistantDelta{Text: text})
			}
		}
		providerResp, err := streamTransport.GenerateStream(ctx, providerReq, onTextDelta)
		if err != nil {
			return Response{}, fmt.Errorf("openai generate stream: %w", err)
		}
		return Response{
			Assistant: Message{
				Role:           RoleAssistant,
				Content:        providerResp.AssistantText,
				Phase:          providerResp.AssistantPhase,
				ToolCalls:      append([]ToolCall(nil), providerResp.ToolCalls...),
				ReasoningItems: append([]ReasoningItem(nil), providerResp.ReasoningItems...),
			},
			ToolCalls:      providerResp.ToolCalls,
			Reasoning:      append([]ReasoningEntry(nil), providerResp.Reasoning...),
			ReasoningItems: append([]ReasoningItem(nil), providerResp.ReasoningItems...),
			OutputItems:    CloneResponseItems(providerResp.OutputItems),
			Usage:          providerResp.Usage,
		}, nil
	}

	resp, err := c.Generate(ctx, request)
	if err != nil {
		return Response{}, err
	}
	if callbacks.OnAssistantDelta != nil && resp.Assistant.Content != "" {
		callbacks.OnAssistantDelta(AssistantDelta{Text: resp.Assistant.Content, Phase: resp.Assistant.Phase})
	}
	return resp, nil
}

func (c *OpenAIClient) Compact(ctx context.Context, request CompactionRequest) (CompactionResponse, error) {
	if c == nil || c.transport == nil {
		return CompactionResponse{}, ErrMissingTransport
	}
	if request.Model == "" {
		return CompactionResponse{}, fmt.Errorf("%w: compaction model is required", ErrInvalidRequest)
	}

	providerReq := OpenAICompactionRequest{
		Model:        request.Model,
		Instructions: request.Instructions,
		SessionID:    request.SessionID,
		InputItems:   CloneResponseItems(request.InputItems),
	}
	providerResp, err := c.transport.Compact(ctx, providerReq)
	if err != nil {
		return CompactionResponse{}, fmt.Errorf("openai compact: %w", err)
	}
	return CompactionResponse{
		OutputItems:       CloneResponseItems(providerResp.OutputItems),
		Usage:             providerResp.Usage,
		TrimmedItemsCount: textutil.CloneInt(providerResp.TrimmedItemsCount),
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

func (c *OpenAIClient) CountRequestInputTokens(ctx context.Context, request Request) (int, error) {
	if c == nil || c.transport == nil {
		return 0, ErrMissingTransport
	}
	if err := request.Validate(); err != nil {
		return 0, err
	}
	counter, ok := c.transport.(OpenAIInputTokenCountTransport)
	if !ok {
		return 0, fmt.Errorf("openai request token counting is not supported by transport")
	}

	providerReq := RequestAsOpenAI(request)

	count, err := counter.CountRequestInputTokens(ctx, providerReq)
	if err != nil {
		return 0, fmt.Errorf("openai request token counting failed: %w", err)
	}
	if count < 0 {
		return 0, nil
	}
	return count, nil
}

func (c *OpenAIClient) SupportsRequestInputTokenCount(ctx context.Context) (bool, error) {
	if c == nil || c.transport == nil {
		return false, ErrMissingTransport
	}
	support, ok := c.transport.(OpenAIInputTokenCountSupportTransport)
	if !ok {
		_, counterSupported := c.transport.(OpenAIInputTokenCountTransport)
		return counterSupported, nil
	}
	return support.SupportsRequestInputTokenCount(ctx)
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
