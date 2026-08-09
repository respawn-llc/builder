package app

import (
	"testing"

	"core/shared/config"
	"core/shared/serverapi"
)

func TestAuthProviderFallbackFactsUseCanonicalRuntimeProviderResolution(t *testing.T) {
	tests := []struct {
		name     string
		settings config.Settings
		wantKind serverapi.AuthProviderKind
		wantID   string
	}{
		{
			name:     "explicit OpenAI provider",
			settings: config.Settings{ProviderOverride: "openai"},
			wantKind: serverapi.AuthProviderKindOpenAI,
			wantID:   "openai",
		},
		{
			name:     "model-inferred provider",
			settings: config.Settings{Model: "claude-3-7-sonnet"},
			wantKind: serverapi.AuthProviderKindConfiguredProvider,
			wantID:   "anthropic",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider, err := authProviderFallbackFactsForSettings(test.settings)
			if err != nil {
				t.Fatalf("authProviderFallbackFactsForSettings: %v", err)
			}
			if provider.Kind != test.wantKind || provider.Identifier != test.wantID {
				t.Fatalf("provider = %+v, want kind %q identifier %q", provider, test.wantKind, test.wantID)
			}
		})
	}
}
