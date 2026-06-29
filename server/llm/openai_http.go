package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"core/server/auth"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
)

const (
	defaultOpenAIBaseURL   = "https://api.openai.com/v1"
	codexResponsesEndpoint = "https://chatgpt.com/backend-api/codex/responses"
	defaultOriginator      = "kent"
	defaultUserAgent       = "kent/dev"
	reasoningRoleSummary   = "reasoning"
)

type AuthHeaderProvider interface {
	AuthorizationHeader(ctx context.Context) (string, error)
}

type OpenAIAuthMetadataProvider interface {
	OpenAIAuthMetadata(ctx context.Context) (method string, accountID string, err error)
}

type openAIAuthMode struct {
	IsOAuth   bool
	AccountID string
}

type HTTPTransport struct {
	BaseURL                      string
	BaseURLExplicit              bool
	Client                       *http.Client
	Auth                         AuthHeaderProvider
	Provider                     Provider
	Store                        bool
	ModelVerbosity               string
	ContextWindowTokens          int
	ProviderCapabilitiesOverride *ProviderCapabilities

	mu                  sync.RWMutex
	modelContextWindows map[string]int
}

func NewHTTPTransport(auth AuthHeaderProvider) *HTTPTransport {
	window := 200000
	if raw := strings.TrimSpace(os.Getenv("KENT_CONTEXT_WINDOW")); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value > 0 {
			window = value
		}
	}
	return &HTTPTransport{
		BaseURL:             defaultOpenAIBaseURL,
		Client:              NewHTTPClient(120 * time.Second),
		Auth:                auth,
		Provider:            ProviderOpenAI,
		ContextWindowTokens: window,
		modelContextWindows: make(map[string]int),
	}
}

func (t *HTTPTransport) Generate(ctx context.Context, request OpenAIRequest) (OpenAIResponse, error) {
	if t.Client == nil {
		t.Client = NewHTTPClient(120 * time.Second)
	}
	windowTokens := t.resolveContextWindowFallback(ctx, request.Model)

	authHeader, mode, err := t.resolveAuth(ctx)
	if err != nil {
		return OpenAIResponse{}, err
	}
	providerCaps, err := t.providerCapabilitiesForMode(mode)
	if err != nil {
		return OpenAIResponse{}, err
	}

	payload, err := t.buildPayload(request, mode, providerCaps)
	if err != nil {
		return OpenAIResponse{}, err
	}

	service := responses.NewResponseService(
		option.WithBaseURL(t.serviceBaseURL(mode)),
		option.WithHTTPClient(t.Client),
		option.WithMaxRetries(0),
	)
	reqOpts := t.buildRequestOptions(authHeader, mode, request.SessionID)
	var rawResp *http.Response
	reqOpts = append(reqOpts, option.WithResponseInto(&rawResp))

	decoded, err := service.New(ctx, payload, reqOpts...)
	if err != nil {
		return OpenAIResponse{}, newOpenAIRequestErrorMapper(providerCaps.ProviderID).Map(err, rawResp, "openai responses request failed")
	}
	if decoded == nil {
		return OpenAIResponse{}, fmt.Errorf("openai responses request failed: empty response")
	}

	outputItems, assistantText, assistantPhase, toolCalls, reasoning, reasoningItems := parseOutputItems(decoded.Output)
	return OpenAIResponse{
		AssistantText:  assistantText,
		AssistantPhase: assistantPhase,
		ToolCalls:      toolCalls,
		Reasoning:      normalizeReasoningEntries(reasoning),
		ReasoningItems: reasoningItems,
		OutputItems:    outputItems,
		Usage:          usageFromSDK(decoded.Usage, windowTokens),
	}, nil
}

func (t *HTTPTransport) GenerateStream(ctx context.Context, request OpenAIRequest, onDelta func(text string)) (OpenAIResponse, error) {
	var callback func(AssistantDelta)
	if onDelta != nil {
		callback = func(delta AssistantDelta) {
			onDelta(delta.Text)
		}
	}
	return t.GenerateStreamWithEvents(ctx, request, StreamCallbacks{OnAssistantDelta: callback})
}

func (t *HTTPTransport) GenerateStreamWithEvents(ctx context.Context, request OpenAIRequest, callbacks StreamCallbacks) (OpenAIResponse, error) {
	if t.Client == nil {
		t.Client = NewHTTPClient(120 * time.Second)
	}
	windowTokens := t.resolveContextWindowFallback(ctx, request.Model)

	authHeader, mode, err := t.resolveAuth(ctx)
	if err != nil {
		return OpenAIResponse{}, err
	}
	providerCaps, err := t.providerCapabilitiesForMode(mode)
	if err != nil {
		return OpenAIResponse{}, err
	}

	payload, err := t.buildPayload(request, mode, providerCaps)
	if err != nil {
		return OpenAIResponse{}, err
	}

	service := responses.NewResponseService(
		option.WithBaseURL(t.serviceBaseURL(mode)),
		option.WithHTTPClient(t.streamingHTTPClient()),
		option.WithMaxRetries(0),
	)
	reqOpts := t.buildRequestOptions(authHeader, mode, request.SessionID)
	var rawResp *http.Response
	reqOpts = append(reqOpts, option.WithResponseInto(&rawResp))

	stream := service.NewStreaming(ctx, payload, reqOpts...)
	defer stream.Close()

	accumulator := newResponseStreamAccumulator(callbacks, windowTokens)
	for stream.Next() {
		if callbacks.OnStreamActivity != nil {
			callbacks.OnStreamActivity()
		}
		accumulator.Consume(stream.Current())
		if err := accumulator.Err(providerCaps.ProviderID); err != nil {
			return OpenAIResponse{}, newOpenAIRequestErrorMapper(providerCaps.ProviderID).Map(err, rawResp, "read responses stream events")
		}
	}
	if err := stream.Err(); err != nil {
		if accumulator.hasCompleted() {
			return accumulator.Response(), nil
		}
		return OpenAIResponse{}, newOpenAIRequestErrorMapper(providerCaps.ProviderID).Map(err, rawResp, "read responses stream events")
	}
	return accumulator.Response(), nil
}

func (t *HTTPTransport) streamingHTTPClient() *http.Client {
	transport := t.Client.Transport
	if transport == nil {
		transport = sharedHTTPTransport
	}
	return &http.Client{Transport: transport}
}

func (t *HTTPTransport) Compact(ctx context.Context, request OpenAICompactionRequest) (OpenAICompactionResponse, error) {
	if t.Client == nil {
		t.Client = NewHTTPClient(120 * time.Second)
	}
	windowTokens := t.resolveContextWindowFallback(ctx, request.Model)

	authHeader, mode, err := t.resolveAuth(ctx)
	if err != nil {
		return OpenAICompactionResponse{}, err
	}
	providerCaps, err := t.providerCapabilitiesForMode(mode)
	if err != nil {
		return OpenAICompactionResponse{}, err
	}

	payload, err := newOpenAIRequestPayloadBuilder(t.Store, t.ModelVerbosity, ProviderCapabilities{}).BuildCompact(request)
	if err != nil {
		return OpenAICompactionResponse{}, err
	}

	service := responses.NewResponseService(
		option.WithBaseURL(t.serviceBaseURL(mode)),
		option.WithHTTPClient(t.Client),
		option.WithMaxRetries(0),
	)
	reqOpts := t.buildRequestOptions(authHeader, mode, request.SessionID)
	var rawResp *http.Response
	var rawBody []byte
	reqOpts = append(reqOpts,
		option.WithResponseInto(&rawResp),
		option.WithResponseBodyInto(&rawBody),
	)

	decoded, err := service.Compact(ctx, payload, reqOpts...)
	if err != nil {
		return OpenAICompactionResponse{}, newOpenAIRequestErrorMapper(providerCaps.ProviderID).Map(err, rawResp, "openai responses compact request failed")
	}
	if len(bytes.TrimSpace(rawBody)) > 0 {
		var parsed responses.CompactedResponse
		if err := json.Unmarshal(rawBody, &parsed); err != nil {
			return OpenAICompactionResponse{}, fmt.Errorf("openai responses compact request failed: invalid compact response body: %w", err)
		}
		decoded = &parsed
	}
	if decoded == nil {
		return OpenAICompactionResponse{}, fmt.Errorf("openai responses compact request failed: empty response")
	}

	outputItems, _, _, _, _, _ := parseOutputItems(decoded.Output)
	return OpenAICompactionResponse{
		OutputItems:       outputItems,
		Usage:             usageFromSDK(decoded.Usage, windowTokens),
		TrimmedItemsCount: 0,
	}, nil
}

func (t *HTTPTransport) CountRequestInputTokens(ctx context.Context, request OpenAIRequest) (int, error) {
	if t.Client == nil {
		t.Client = NewHTTPClient(120 * time.Second)
	}

	authHeader, mode, err := t.resolveAuth(ctx)
	if err != nil {
		return 0, err
	}
	providerCaps, err := t.providerCapabilitiesForMode(mode)
	if err != nil {
		return 0, err
	}

	payload, err := t.buildInputTokenCountParams(request, providerCaps)
	if err != nil {
		return 0, err
	}

	service := responses.NewInputTokenService(
		option.WithBaseURL(t.serviceBaseURL(mode)),
		option.WithHTTPClient(t.Client),
		option.WithMaxRetries(0),
	)
	reqOpts := t.buildRequestOptions(authHeader, mode, request.SessionID)
	var rawResp *http.Response
	reqOpts = append(reqOpts, option.WithResponseInto(&rawResp))

	decoded, err := service.Count(ctx, payload, reqOpts...)
	if err != nil {
		return 0, newOpenAIRequestErrorMapper(providerCaps.ProviderID).Map(err, rawResp, "openai responses input_tokens request failed")
	}
	if decoded == nil {
		return 0, fmt.Errorf("openai responses input_tokens request failed: empty response")
	}
	resolvedWindow := parseContextWindowTokens(decoded.RawJSON())
	if resolvedWindow <= 0 {
		resolvedWindow = parseContextWindowTokensFromHeaders(rawResp)
	}
	t.cacheModelContextWindow(request.Model, resolvedWindow)
	if decoded.InputTokens < 0 {
		return 0, nil
	}
	return int(decoded.InputTokens), nil
}

func (t *HTTPTransport) SupportsRequestInputTokenCount(ctx context.Context) (bool, error) {
	_, mode, err := t.resolveAuth(ctx)
	if err != nil {
		return false, err
	}
	providerCaps, err := t.providerCapabilitiesForMode(mode)
	if err != nil {
		return false, err
	}
	return providerCaps.SupportsRequestInputTokenCount, nil
}

func (t *HTTPTransport) ResolveModelContextWindow(ctx context.Context, model string) (int, error) {
	if t.Client == nil {
		t.Client = NewHTTPClient(120 * time.Second)
	}
	if t.ContextWindowTokens > 0 {
		return t.ContextWindowTokens, nil
	}

	normalizedModel := strings.ToLower(strings.TrimSpace(model))
	if normalizedModel == "" {
		if t.ContextWindowTokens > 0 {
			return t.ContextWindowTokens, nil
		}
		return 0, nil
	}

	t.mu.RLock()
	if cached := t.modelContextWindows[normalizedModel]; cached > 0 {
		t.mu.RUnlock()
		return cached, nil
	}
	t.mu.RUnlock()

	resolved := 0
	authHeader, mode, err := t.resolveAuth(ctx)
	if err == nil {
		service := openai.NewModelService(
			option.WithBaseURL(t.serviceBaseURL(mode)),
			option.WithHTTPClient(t.Client),
			option.WithMaxRetries(0),
		)
		reqOpts := t.buildRequestOptions(authHeader, mode, "")
		var rawResp *http.Response
		reqOpts = append(reqOpts, option.WithResponseInto(&rawResp))
		modelResponse, modelErr := service.Get(ctx, strings.TrimSpace(model), reqOpts...)
		if modelErr == nil && modelResponse != nil {
			resolved = parseContextWindowTokens(modelResponse.RawJSON())
		}
		if resolved <= 0 {
			resolved = parseContextWindowTokensFromHeaders(rawResp)
		}
	}

	if resolved <= 0 {
		if fallbackMeta, ok := LookupModelMetadata(model); ok && fallbackMeta.ContextWindowTokens > 0 {
			resolved = fallbackMeta.ContextWindowTokens
		}
	}
	if resolved <= 0 {
		resolved = t.ContextWindowTokens
	}

	t.cacheModelContextWindow(model, resolved)
	return resolved, nil
}

func (t *HTTPTransport) ProviderCapabilities(ctx context.Context) (ProviderCapabilities, error) {
	_, mode, err := t.resolveAuth(ctx)
	if err != nil {
		return ProviderCapabilities{}, err
	}
	return t.providerCapabilitiesForMode(mode)
}

func (t *HTTPTransport) resolveAuth(ctx context.Context) (string, openAIAuthMode, error) {
	if t.Auth == nil {
		if t.BaseURLExplicit {
			return "", openAIAuthMode{}, nil
		}
		return "", openAIAuthMode{}, &AuthError{Err: auth.ErrAuthNotConfigured}
	}
	authHeader, err := t.Auth.AuthorizationHeader(ctx)
	if err != nil {
		if t.BaseURLExplicit && errors.Is(err, auth.ErrAuthNotConfigured) {
			return "", openAIAuthMode{}, nil
		}
		return "", openAIAuthMode{}, &AuthError{Err: err}
	}

	mode := openAIAuthMode{}
	if provider, ok := t.Auth.(OpenAIAuthMetadataProvider); ok {
		method, accountID, err := provider.OpenAIAuthMetadata(ctx)
		if err != nil {
			return "", openAIAuthMode{}, &AuthError{Err: err}
		}
		mode.IsOAuth = method == "oauth"
		mode.AccountID = strings.TrimSpace(accountID)
	}
	return authHeader, mode, nil
}
