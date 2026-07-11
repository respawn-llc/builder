package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"core/shared/llmerrors"

	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
)

func (t *HTTPTransport) serviceBaseURL(mode OpenAIAuthMode) string {
	if mode.IsOAuth && !t.BaseURLExplicit {
		return strings.TrimSuffix(codexResponsesEndpoint, "/responses")
	}
	base := strings.TrimSuffix(t.BaseURL, "/")
	if base == "" {
		base = defaultOpenAIBaseURL
	}
	return base
}

func (t *HTTPTransport) buildRequestOptions(authHeader string, mode OpenAIAuthMode, sessionID string) []option.RequestOption {
	opts := []option.RequestOption{
		option.WithHeader("originator", defaultOriginator),
		option.WithHeader("User-Agent", defaultUserAgent),
	}
	if strings.TrimSpace(authHeader) != "" {
		opts = append([]option.RequestOption{option.WithHeader("Authorization", authHeader)}, opts...)
	}
	if strings.TrimSpace(sessionID) != "" {
		opts = append(opts, option.WithHeader("session_id", sessionID))
	}
	if mode.IsOAuth && mode.AccountID != "" {
		opts = append(opts, option.WithHeader("ChatGPT-Account-Id", mode.AccountID))
	}
	return opts
}

func (t *HTTPTransport) resolveContextWindowFallback(ctx context.Context, model string) int {
	if t.ContextWindowTokens > 0 {
		return t.ContextWindowTokens
	}
	resolved, err := t.ResolveModelContextWindow(ctx, model)
	if err == nil && resolved > 0 {
		return resolved
	}
	if fallbackMeta, ok := LookupModelMetadata(model); ok && fallbackMeta.ContextWindowTokens > 0 {
		return fallbackMeta.ContextWindowTokens
	}
	return 0
}

func (t *HTTPTransport) providerVariantForMode(mode OpenAIAuthMode) (ProviderVariantContract, error) {
	provider := t.Provider
	if provider == "" {
		provider = ProviderOpenAI
	}
	variant, err := resolveProviderTransportVariant(provider, t.BaseURL, mode)
	if err != nil {
		providerID := strings.TrimSpace(string(provider))
		if providerID == "" {
			providerID = "unknown-provider"
		}
		return ProviderVariantContract{}, llmerrors.NewProviderContractError(providerID, 0, err)
	}
	return variant, nil
}

func (t *HTTPTransport) providerCapabilitiesForMode(mode OpenAIAuthMode) (ProviderCapabilities, error) {
	if t.ProviderCapabilitiesOverride != nil {
		return *t.ProviderCapabilitiesOverride, nil
	}
	variant, err := t.providerVariantForMode(mode)
	if err != nil {
		return ProviderCapabilities{}, err
	}
	return variant.Capabilities, nil
}

func (t *HTTPTransport) cacheModelContextWindow(model string, tokens int) {
	if tokens <= 0 {
		return
	}
	normalizedModel := strings.ToLower(strings.TrimSpace(model))
	if normalizedModel == "" {
		return
	}
	t.mu.Lock()
	t.modelContextWindows[normalizedModel] = tokens
	t.mu.Unlock()
}

func parseContextWindowTokens(rawJSON string) int {
	trimmed := strings.TrimSpace(rawJSON)
	if trimmed == "" {
		return 0
	}
	var decoded any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return 0
	}
	return findPositiveIntByPreferredKeys(decoded, []string{"context_window", "model_context_window", "input_token_limit", "max_input_tokens", "context_length"})
}

func parseContextWindowTokensFromHeaders(rawResp *http.Response) int {
	if rawResp == nil {
		return 0
	}
	for _, headerName := range []string{
		"x-openai-model-context-window",
		"openai-model-context-window",
		"x-model-context-window",
		"model-context-window",
		"x-context-window",
		"context-window",
	} {
		if parsed := parsePositiveInt(rawResp.Header.Get(headerName)); parsed > 0 {
			return parsed
		}
	}
	return 0
}

func findPositiveIntByPreferredKeys(node any, keys []string) int {
	switch typed := node.(type) {
	case map[string]any:
		for _, key := range keys {
			if value, ok := typed[key]; ok {
				if parsed := parsePositiveInt(value); parsed > 0 {
					return parsed
				}
			}
		}
		for _, value := range typed {
			if parsed := findPositiveIntByPreferredKeys(value, keys); parsed > 0 {
				return parsed
			}
		}
	case []any:
		for _, value := range typed {
			if parsed := findPositiveIntByPreferredKeys(value, keys); parsed > 0 {
				return parsed
			}
		}
	}
	return 0
}

func parsePositiveInt(value any) int {
	switch typed := value.(type) {
	case float64:
		parsed := int(typed)
		if parsed > 0 {
			return parsed
		}
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil && parsed > 0 {
			return int(parsed)
		}
	case int:
		if typed > 0 {
			return typed
		}
	case int64:
		if typed > 0 {
			return int(typed)
		}
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err == nil && parsed > 0 {
			return parsed
		}
	}
	return 0
}

func usageFromSDK(usage responses.ResponseUsage, window int) Usage {
	out := Usage{InputTokens: int(usage.InputTokens), OutputTokens: int(usage.OutputTokens), WindowTokens: window}
	if usage.JSON.InputTokensDetails.Valid() && usage.InputTokensDetails.JSON.CachedTokens.Valid() {
		out.CachedInputTokens = int(usage.InputTokensDetails.CachedTokens)
		out.HasCachedInputTokens = true
	}
	return out
}
