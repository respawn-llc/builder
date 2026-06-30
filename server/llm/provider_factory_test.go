package llm

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"core/server/auth"
)

func openAIClientFromProvider(t *testing.T, client Client) *OpenAIClient {
	t.Helper()
	if watchdog, ok := client.(*idleWatchdogClient); ok {
		client = watchdog.streamingModelClient
	}
	openAIClient, ok := client.(*OpenAIClient)
	if !ok {
		t.Fatalf("expected *OpenAIClient, got %T", client)
	}
	return openAIClient
}

type providerTestAuth struct{}

func (providerTestAuth) AuthorizationHeader(context.Context) (string, error) {
	return "Bearer test", nil
}

type providerTestMissingAuth struct{}

func (providerTestMissingAuth) AuthorizationHeader(context.Context) (string, error) {
	return "", auth.ErrAuthNotConfigured
}

func TestInferProviderFromModel(t *testing.T) {
	got, err := InferProviderFromModel("gpt-5")
	if err != nil {
		t.Fatalf("infer openai provider: %v", err)
	}
	if got != ProviderOpenAI {
		t.Fatalf("expected openai provider, got %q", got)
	}
	got, err = InferProviderFromModel("claude-3-7-sonnet")
	if err != nil {
		t.Fatalf("infer anthropic provider: %v", err)
	}
	if got != ProviderAnthropic {
		t.Fatalf("expected anthropic provider, got %q", got)
	}
	if _, err := InferProviderFromModel("custom-model"); !errors.Is(err, ErrUnsupportedProvider) {
		t.Fatalf("expected unsupported provider inference for unknown model family, got %v", err)
	}
}

func TestNewProviderClient_OpenAI(t *testing.T) {
	httpClient := &http.Client{Timeout: 7 * time.Second}
	client, err := NewProviderClient(ProviderClientOptions{
		Model:          "gpt-5.3-codex",
		Auth:           providerTestAuth{},
		HTTPClient:     httpClient,
		ModelVerbosity: "HIGH",
	})
	if err != nil {
		t.Fatalf("new provider client: %v", err)
	}
	openAIClient := openAIClientFromProvider(t, client)
	transport, ok := openAIClient.transport.(*HTTPTransport)
	if !ok {
		t.Fatalf("expected *HTTPTransport, got %T", openAIClient.transport)
	}
	if transport.Client != httpClient {
		t.Fatal("expected provider HTTP client override to be used")
	}
	if transport.ContextWindowTokens != 400_000 {
		t.Fatalf("expected context window from model metadata, got %d", transport.ContextWindowTokens)
	}
	if transport.ModelVerbosity != "high" {
		t.Fatalf("expected normalized model verbosity, got %q", transport.ModelVerbosity)
	}
}

func TestNewProviderClient_CodexSparkUsesSparkMetadata(t *testing.T) {
	client, err := NewProviderClient(ProviderClientOptions{
		Model: "gpt-5.3-codex-spark",
		Auth:  providerTestAuth{},
	})
	if err != nil {
		t.Fatalf("new provider client: %v", err)
	}
	openAIClient := openAIClientFromProvider(t, client)
	transport, ok := openAIClient.transport.(*HTTPTransport)
	if !ok {
		t.Fatalf("expected *HTTPTransport, got %T", openAIClient.transport)
	}
	if transport.ContextWindowTokens != 128_000 {
		t.Fatalf("expected spark context window from model metadata, got %d", transport.ContextWindowTokens)
	}
}

func TestNewProviderClient_AnthropicNotImplemented(t *testing.T) {
	_, err := NewProviderClient(ProviderClientOptions{
		Model: "claude-3-7-sonnet",
		Auth:  providerTestAuth{},
	})
	if !errors.Is(err, ErrUnsupportedProvider) {
		t.Fatalf("expected unsupported provider error, got %v", err)
	}
}

func TestNewProviderClient_ExplicitProviderOverrideAllowsCustomModelAlias(t *testing.T) {
	client, err := NewProviderClient(ProviderClientOptions{
		Provider: ProviderOpenAI,
		Model:    "my-team-alias",
		Auth:     providerTestAuth{},
	})
	if err != nil {
		t.Fatalf("new provider client: %v", err)
	}

	openAIClient := openAIClientFromProvider(t, client)
	providerCaps, err := openAIClient.ProviderCapabilities(context.Background())
	if err != nil {
		t.Fatalf("provider capabilities: %v", err)
	}
	if providerCaps.ProviderID != "openai" || !providerCaps.IsOpenAIFirstParty {
		t.Fatalf("expected explicit provider override to bypass model inference, got %+v", providerCaps)
	}
}

func TestNewProviderClient_CustomModelInferenceErrorMentionsProviderOverride(t *testing.T) {
	_, err := NewProviderClient(ProviderClientOptions{
		Model: "my-team-alias",
		Auth:  providerTestAuth{},
	})
	if !errors.Is(err, ErrUnsupportedProvider) {
		t.Fatalf("expected unsupported provider error, got %v", err)
	}
	var providerSelectionErr *ProviderSelectionError
	if !errors.As(err, &providerSelectionErr) {
		t.Fatalf("expected provider selection error, got %T", err)
	}
}

func TestNewProviderClient_RemoteOpenAICompatibleBaseURLAllowsCustomModelFamily(t *testing.T) {
	client, err := NewProviderClient(ProviderClientOptions{
		Model:          "vendor-custom-model",
		Auth:           providerTestAuth{},
		OpenAIBaseURL:  "https://example.openrouter.ai/api/v1",
		ModelVerbosity: "MEDIUM",
	})
	if err != nil {
		t.Fatalf("new provider client: %v", err)
	}

	openAIClient := openAIClientFromProvider(t, client)

	transport, ok := openAIClient.transport.(*HTTPTransport)
	if !ok {
		t.Fatalf("expected *HTTPTransport, got %T", openAIClient.transport)
	}
	if transport.Provider != ProviderOpenAI {
		t.Fatalf("expected explicit openai-compatible base URL to select openai transport family, got %q", transport.Provider)
	}
	if transport.BaseURL != "https://example.openrouter.ai/api/v1" {
		t.Fatalf("expected custom base url to be preserved, got %q", transport.BaseURL)
	}

	providerCaps, err := openAIClient.ProviderCapabilities(context.Background())
	if err != nil {
		t.Fatalf("provider capabilities: %v", err)
	}
	if providerCaps.ProviderID != "openai-compatible" {
		t.Fatalf("expected remote base url to resolve openai-compatible provider id, got %+v", providerCaps)
	}
	if !providerCaps.SupportsResponsesAPI {
		t.Fatalf("expected responses api support, got %+v", providerCaps)
	}
	if providerCaps.SupportsResponsesCompact || providerCaps.SupportsNativeWebSearch || providerCaps.IsOpenAIFirstParty {
		t.Fatalf("expected conservative remote provider capabilities, got %+v", providerCaps)
	}
	if providerCaps.SupportsRequestInputTokenCount {
		t.Fatalf("expected generic openai-compatible provider to disable exact input-token counting, got %+v", providerCaps)
	}
}

func TestNewProviderClient_RemoteOpenAICompatibleBaseURLAllowsAnonymousCapabilitiesResolution(t *testing.T) {
	client, err := NewProviderClient(ProviderClientOptions{
		Model:         "vendor-custom-model",
		Auth:          providerTestMissingAuth{},
		OpenAIBaseURL: "https://example.openrouter.ai/api/v1",
	})
	if err != nil {
		t.Fatalf("new provider client: %v", err)
	}

	openAIClient := openAIClientFromProvider(t, client)

	providerCaps, err := openAIClient.ProviderCapabilities(context.Background())
	if err != nil {
		t.Fatalf("provider capabilities: %v", err)
	}
	if providerCaps.ProviderID != "openai-compatible" {
		t.Fatalf("expected remote base url to resolve openai-compatible provider id, got %+v", providerCaps)
	}
}

func TestNewProviderClient_DefaultOpenAIBaseURLDoesNotStayExplicit(t *testing.T) {
	client, err := NewProviderClient(ProviderClientOptions{
		Model:         "gpt-5",
		Auth:          providerTestMissingAuth{},
		OpenAIBaseURL: "https://api.openai.com",
	})
	if err != nil {
		t.Fatalf("new provider client: %v", err)
	}
	openAIClient := openAIClientFromProvider(t, client)
	transport, ok := openAIClient.transport.(*HTTPTransport)
	if !ok {
		t.Fatalf("expected *HTTPTransport, got %T", openAIClient.transport)
	}
	if transport.BaseURLExplicit {
		t.Fatal("expected canonical default OpenAI URL to avoid explicit anonymous-mode transport")
	}
}

func TestProviderErrorReducerForUnknownIDFailsFast(t *testing.T) {
	_, err := providerErrorReducerForID("custom-provider-id")
	if err == nil {
		t.Fatal("expected missing provider reducer error")
	}
}
