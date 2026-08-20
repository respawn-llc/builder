package authstatus

import (
	"reflect"
	"testing"

	"core/shared/config"
	authpb "core/shared/protoapi/gen/kent/api/auth"
)

func TestProviderSelectionRoundTripsCanonicalInputs(t *testing.T) {
	settings := config.Settings{
		Model:            "gpt-5.6-sol",
		ProviderOverride: "openai",
		OpenAIBaseURL:    "https://api.openai.com/v1",
		ProviderCapabilities: config.ProviderCapabilitiesOverride{
			ProviderID:         "internal-openai",
			IsOpenAIFirstParty: true,
		},
	}
	selection := ProviderSelection(settings)
	roundTripped := ProviderSettings(selection)
	if roundTripped.Model != settings.Model ||
		roundTripped.ProviderOverride != settings.ProviderOverride ||
		roundTripped.OpenAIBaseURL != settings.OpenAIBaseURL ||
		roundTripped.ProviderCapabilities.ProviderID != settings.ProviderCapabilities.ProviderID ||
		roundTripped.ProviderCapabilities.IsOpenAIFirstParty != settings.ProviderCapabilities.IsOpenAIFirstParty {
		t.Fatalf("provider settings round trip = %+v, want canonical inputs from %+v", roundTripped, settings)
	}
}

func TestProviderFactsDropsCredentialBearingURLComponents(t *testing.T) {
	facts := ProviderFacts("openai-compatible", false, config.Settings{
		OpenAIBaseURL: "https://user:secret@example.com:8443/v1/key?token=secret#fragment",
	})
	want := &authpb.ProviderDisplayOrigin{
		Scheme:   "https",
		Hostname: "example.com",
		Port:     testString("8443"),
	}
	if facts.Kind != authpb.ProviderKind_PROVIDER_KIND_OPENAI_COMPATIBLE ||
		!reflect.DeepEqual(facts.DisplayOrigin, want) {
		t.Fatalf("provider facts = %+v, want origin %+v", facts, want)
	}
	for _, raw := range []string{
		"relative/path",
		"mailto:user@example.com",
		"://invalid",
		"https://example.com:0",
		"https://example.com:65536",
	} {
		if got := ProviderFacts("openai-compatible", false, config.Settings{OpenAIBaseURL: raw}).DisplayOrigin; got != nil {
			t.Fatalf("display origin for %q = %+v, want nil", raw, got)
		}
	}
}

func TestProviderFactsProjectsCanonicalRuntimeCapabilities(t *testing.T) {
	tests := []struct {
		name               string
		providerID         string
		isOpenAIFirstParty bool
		wantKind           authpb.ProviderKind
		wantIdentifier     string
	}{
		{name: "OpenAI", providerID: "openai", isOpenAIFirstParty: true, wantKind: authpb.ProviderKind_PROVIDER_KIND_OPENAI, wantIdentifier: "openai"},
		{name: "ChatGPT", providerID: "chatgpt-codex", isOpenAIFirstParty: true, wantKind: authpb.ProviderKind_PROVIDER_KIND_OPENAI, wantIdentifier: "openai"},
		{name: "configured provider", providerID: "anthropic", wantKind: authpb.ProviderKind_PROVIDER_KIND_CONFIGURED_PROVIDER, wantIdentifier: "anthropic"},
		{name: "compatible endpoint", providerID: "openai-compatible", wantKind: authpb.ProviderKind_PROVIDER_KIND_OPENAI_COMPATIBLE, wantIdentifier: "openai-compatible"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ProviderFacts(test.providerID, test.isOpenAIFirstParty, config.Settings{})
			if got.Kind != test.wantKind || got.Identifier != test.wantIdentifier {
				t.Fatalf("ProviderFacts = %+v, want kind %q identifier %q", got, test.wantKind, test.wantIdentifier)
			}
		})
	}
}

func TestSupportsSubscriptionUsageRequiresCanonicalFirstPartyAndOfficialEndpoint(t *testing.T) {
	tests := []struct {
		name               string
		settings           config.Settings
		isOpenAIFirstParty bool
		want               bool
	}{
		{name: "default first party", isOpenAIFirstParty: true, want: true},
		{name: "ChatGPT", settings: config.Settings{OpenAIBaseURL: "https://chatgpt.com/backend-api"}, isOpenAIFirstParty: true, want: true},
		{name: "OpenAI API", settings: config.Settings{OpenAIBaseURL: "https://api.openai.com/v1"}, isOpenAIFirstParty: true, want: true},
		{name: "canonical custom provider", isOpenAIFirstParty: false},
		{name: "compatible endpoint", settings: config.Settings{OpenAIBaseURL: "https://example.com/v1"}, isOpenAIFirstParty: true},
		{name: "insecure ChatGPT transport", settings: config.Settings{OpenAIBaseURL: "http://chatgpt.com/backend-api"}, isOpenAIFirstParty: true},
		{name: "custom ChatGPT port", settings: config.Settings{OpenAIBaseURL: "https://chatgpt.com:8443/backend-api"}, isOpenAIFirstParty: true},
		{name: "custom ChatGPT path", settings: config.Settings{OpenAIBaseURL: "https://chatgpt.com/v1"}, isOpenAIFirstParty: true},
		{name: "credential-bearing ChatGPT URL", settings: config.Settings{OpenAIBaseURL: "https://user@chatgpt.com/backend-api"}, isOpenAIFirstParty: true},
		{name: "query-bearing ChatGPT URL", settings: config.Settings{OpenAIBaseURL: "https://chatgpt.com/backend-api?token=secret"}, isOpenAIFirstParty: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := SupportsSubscriptionUsage(test.settings, test.isOpenAIFirstParty); got != test.want {
				t.Fatalf("SupportsSubscriptionUsage = %v, want %v", got, test.want)
			}
		})
	}
}

func testString(value string) *string {
	return &value
}
