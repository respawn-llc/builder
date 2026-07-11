package llm

import (
	"errors"
	"testing"

	"core/server/auth"
	"core/server/session"
	"core/shared/config"
)

func TestInferProviderCapabilities_UsesRegistryContracts(t *testing.T) {
	openai, err := InferProviderCapabilities("openai")
	if err != nil {
		t.Fatalf("infer openai capabilities: %v", err)
	}
	if !openai.SupportsResponsesCompact || !openai.IsOpenAIFirstParty || !openai.SupportsNativeWebSearch {
		t.Fatalf("expected first-party openai compact support, got %+v", openai)
	}
	if !openai.SupportsPromptCacheKey {
		t.Fatalf("expected openai prompt cache key support, got %+v", openai)
	}

	oauth, err := InferProviderCapabilities("chatgpt-codex")
	if err != nil {
		t.Fatalf("infer codex capabilities: %v", err)
	}
	if oauth.ProviderID != "chatgpt-codex" || !oauth.SupportsResponsesCompact || !oauth.IsOpenAIFirstParty || !oauth.SupportsNativeWebSearch {
		t.Fatalf("unexpected oauth capabilities: %+v", oauth)
	}
	if !oauth.SupportsPromptCacheKey {
		t.Fatalf("expected chatgpt-codex prompt cache key support, got %+v", oauth)
	}
}

func TestInferProviderCapabilities_UnknownProviderFailsExplicitly(t *testing.T) {
	_, err := InferProviderCapabilities("custom-provider")
	if !errors.Is(err, ErrUnsupportedProvider) {
		t.Fatalf("expected unsupported provider error, got %v", err)
	}
}

func TestResolveRuntimeProviderCapabilities(t *testing.T) {
	tests := []struct {
		name     string
		auth     auth.State
		settings config.Settings
		wantID   string
	}{
		{
			name:     "explicit capability override wins",
			settings: config.Settings{ProviderCapabilities: config.ProviderCapabilitiesOverride{ProviderID: "openai-compatible", SupportsResponsesAPI: true}},
			wantID:   "openai-compatible",
		},
		{
			name:     "anthropic provider override uses catalog variant",
			settings: config.Settings{ProviderOverride: "anthropic"},
			wantID:   "anthropic",
		},
		{
			name:     "openai provider override still resolves remote compatible transport variant",
			auth:     auth.State{Method: auth.Method{Type: auth.MethodAPIKey}},
			settings: config.Settings{ProviderOverride: "openai", OpenAIBaseURL: "https://example.test/v1"},
			wantID:   "openai-compatible",
		},
		{
			name:     "oauth defaults to chatgpt codex",
			auth:     auth.State{Method: auth.Method{Type: auth.MethodOAuth}},
			settings: config.Settings{Model: "gpt-5.5"},
			wantID:   "chatgpt-codex",
		},
		{
			name:     "default provider is inferred from non openai model",
			auth:     auth.State{Method: auth.Method{Type: auth.MethodNone}},
			settings: config.Settings{Model: "claude-3-7-sonnet"},
			wantID:   "anthropic",
		},
		{
			name:     "api key with first party base url stays openai",
			auth:     auth.State{Method: auth.Method{Type: auth.MethodAPIKey}},
			settings: config.Settings{OpenAIBaseURL: "https://api.openai.com/v1"},
			wantID:   "openai",
		},
		{
			name:     "api key with compatible base url uses openai compatible",
			auth:     auth.State{Method: auth.Method{Type: auth.MethodAPIKey}},
			settings: config.Settings{OpenAIBaseURL: "https://example.test/v1"},
			wantID:   "openai-compatible",
		},
		{
			name:     "none auth with no override falls back to openai",
			auth:     auth.State{Method: auth.Method{Type: auth.MethodNone}},
			settings: config.Settings{Model: "gpt-5.5"},
			wantID:   "openai",
		},
		{
			name:     "custom model alias with no override falls back to openai",
			auth:     auth.State{Method: auth.Method{Type: auth.MethodNone}},
			settings: config.Settings{Model: "operator-alias"},
			wantID:   "openai",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveRuntimeProviderCapabilities(tt.auth, tt.settings)
			if err != nil {
				t.Fatalf("ResolveRuntimeProviderCapabilities: %v", err)
			}
			want, err := InferProviderCapabilities(tt.wantID)
			if err != nil {
				t.Fatalf("InferProviderCapabilities(%q): %v", tt.wantID, err)
			}
			if got != want {
				t.Fatalf("capabilities = %+v, want %+v", got, want)
			}
		})
	}
}

func TestProviderCapabilitiesForSettingsRejectsUnsupportedProviderOverride(t *testing.T) {
	_, err := ProviderCapabilitiesForSettings(auth.EmptyState(), config.Settings{ProviderOverride: "custom-provider"})
	if !errors.Is(err, ErrUnsupportedProvider) {
		t.Fatalf("expected unsupported provider error, got %v", err)
	}
}

func TestResolveOpenAITransportProviderVariant_DefaultLoopbackAndRemoteCompatibleBaseURL(t *testing.T) {
	if got, err := resolveOpenAITransportProviderVariant("", OpenAIAuthMode{}); err != nil || got != "openai" {
		t.Fatalf("expected default base url to resolve openai variant, got variant=%q err=%v", got, err)
	}
	if got, err := resolveOpenAITransportProviderVariant("https://api.openai.com/v1/", OpenAIAuthMode{}); err != nil || got != "openai" {
		t.Fatalf("expected normalized default base url to resolve openai variant, got variant=%q err=%v", got, err)
	}
	if got, err := resolveOpenAITransportProviderVariant("https://api.openai.com", OpenAIAuthMode{}); err != nil || got != "openai" {
		t.Fatalf("expected bare api.openai.com base url to resolve openai variant, got variant=%q err=%v", got, err)
	}
	if got, err := resolveOpenAITransportProviderVariant("http://127.0.0.1:8080/v1", OpenAIAuthMode{}); err != nil || got != "openai" {
		t.Fatalf("expected loopback base url to resolve openai variant, got variant=%q err=%v", got, err)
	}
	if got, err := resolveOpenAITransportProviderVariant("https://example.openai.azure.com/openai/v1", OpenAIAuthMode{}); err != nil || got != "openai-compatible" {
		t.Fatalf("expected remote compatible base url to resolve openai-compatible variant, got variant=%q err=%v", got, err)
	}
	if got, err := resolveOpenAITransportProviderVariant("https://ignored.example/v1", OpenAIAuthMode{IsOAuth: true}); err != nil || got != "chatgpt-codex" {
		t.Fatalf("expected oauth mode to resolve chatgpt-codex variant, got variant=%q err=%v", got, err)
	}
}

func TestIsOpenAIFirstPartyBaseURL(t *testing.T) {
	if !IsOpenAIFirstPartyBaseURL("https://api.openai.com") {
		t.Fatal("expected bare api.openai.com to be treated as first-party OpenAI")
	}
	if !IsOpenAIFirstPartyBaseURL("https://api.openai.com/v1") {
		t.Fatal("expected default OpenAI /v1 endpoint to be treated as first-party OpenAI")
	}
	if IsOpenAIFirstPartyBaseURL("https://example.test/v1") {
		t.Fatal("did not expect non-OpenAI endpoint to be treated as first-party OpenAI")
	}
}

func TestKnownNonFirstPartyProviderContractsRemainLocalCompactionOnly(t *testing.T) {
	for _, providerID := range []string{"anthropic", "openai-compatible"} {
		caps, err := InferProviderCapabilities(providerID)
		if err != nil {
			t.Fatalf("infer %s capabilities: %v", providerID, err)
		}
		if caps.SupportsResponsesCompact {
			t.Fatalf("expected compact unsupported for %s, got %+v", providerID, caps)
		}
		if caps.IsOpenAIFirstParty {
			t.Fatalf("expected third-party classification for %s, got %+v", providerID, caps)
		}
		if caps.SupportsPromptCacheKey {
			t.Fatalf("expected prompt cache key unsupported for %s, got %+v", providerID, caps)
		}
		if caps.SupportsRequestInputTokenCount {
			t.Fatalf("expected exact input-token counting unsupported for %s, got %+v", providerID, caps)
		}
		if caps.SupportsNativeWebSearch {
			t.Fatalf("expected native web search unsupported for %s, got %+v", providerID, caps)
		}
	}
}

func TestSupportsFastModeProvider(t *testing.T) {
	if !SupportsFastModeProvider(ProviderCapabilities{ProviderID: "chatgpt-codex", SupportsResponsesAPI: true, IsOpenAIFirstParty: true}) {
		t.Fatal("expected chatgpt-codex to support fast mode")
	}
	if !SupportsFastModeProvider(ProviderCapabilities{ProviderID: "openai", SupportsResponsesAPI: true, IsOpenAIFirstParty: true}) {
		t.Fatal("expected openai provider to support fast mode")
	}
	if SupportsFastModeProvider(ProviderCapabilities{ProviderID: "azure-openai", SupportsResponsesAPI: true, IsOpenAIFirstParty: false}) {
		t.Fatal("did not expect non-first-party provider to support fast mode")
	}
}

func TestSupportsPromptCacheKeyProvider(t *testing.T) {
	if !SupportsPromptCacheKeyProvider(ProviderCapabilities{ProviderID: "openai-compatible", SupportsResponsesAPI: true, SupportsPromptCacheKey: true}) {
		t.Fatal("expected explicit prompt cache capability to enable support")
	}
	if SupportsPromptCacheKeyProvider(ProviderCapabilities{ProviderID: "openai-compatible", SupportsResponsesAPI: true, SupportsPromptCacheKey: false}) {
		t.Fatal("did not expect prompt cache support without explicit capability")
	}
}

func TestProviderCapabilitiesFromLockedHandlesExplicitAndLegacyCapabilities(t *testing.T) {
	tests := []struct {
		name                       string
		locked                     session.LockedProviderCapabilities
		wantRequestInputTokenCount bool
		wantPromptCacheKey         bool
	}{
		{
			name: "explicit request input token count false is preserved",
			locked: session.LockedProviderCapabilities{
				ProviderID:                        "openai-compatible",
				SupportsResponsesAPI:              true,
				SupportsRequestInputTokenCount:    false,
				HasSupportsRequestInputTokenCount: true,
			},
		},
		{
			name: "legacy request input token count inherits conservative compatible default",
			locked: session.LockedProviderCapabilities{
				ProviderID:                     "openai-compatible",
				SupportsResponsesAPI:           true,
				SupportsRequestInputTokenCount: false,
			},
		},
		{
			name: "explicit prompt cache false is preserved",
			locked: session.LockedProviderCapabilities{
				ProviderID:                "openai",
				SupportsResponsesAPI:      true,
				SupportsPromptCacheKey:    false,
				HasSupportsPromptCacheKey: true,
			},
			wantRequestInputTokenCount: true,
		},
		{
			name: "legacy prompt cache inherits openai support",
			locked: session.LockedProviderCapabilities{
				ProviderID:           "openai",
				SupportsResponsesAPI: true,
			},
			wantRequestInputTokenCount: true,
			wantPromptCacheKey:         true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			caps, ok := ProviderCapabilitiesFromLocked(&session.LockedContract{ProviderContract: tt.locked})
			if !ok {
				t.Fatal("expected locked provider capabilities")
			}
			if caps.SupportsRequestInputTokenCount != tt.wantRequestInputTokenCount {
				t.Fatalf("request input token count = %v, want %v, caps=%+v", caps.SupportsRequestInputTokenCount, tt.wantRequestInputTokenCount, caps)
			}
			if caps.SupportsPromptCacheKey != tt.wantPromptCacheKey {
				t.Fatalf("prompt cache key = %v, want %v, caps=%+v", caps.SupportsPromptCacheKey, tt.wantPromptCacheKey, caps)
			}
		})
	}
}
