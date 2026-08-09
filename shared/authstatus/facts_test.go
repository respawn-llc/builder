package authstatus

import (
	"reflect"
	"testing"

	"core/shared/config"
	"core/shared/serverapi"
)

func TestProviderFactsDropsCredentialBearingURLComponents(t *testing.T) {
	facts := ProviderFacts(config.Settings{
		OpenAIBaseURL: "https://user:secret@example.com:8443/v1/key?token=secret#fragment",
	})
	want := &serverapi.AuthProviderDisplayOrigin{
		Scheme:   "https",
		Hostname: "example.com",
		Port:     testString("8443"),
	}
	if facts.Kind != serverapi.AuthProviderKindOpenAICompatible ||
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
		if got := ProviderFacts(config.Settings{OpenAIBaseURL: raw}).DisplayOrigin; got != nil {
			t.Fatalf("display origin for %q = %+v, want nil", raw, got)
		}
	}
}

func TestSupportsSubscriptionUsageUsesEffectiveProviderSettings(t *testing.T) {
	tests := []struct {
		name     string
		settings config.Settings
		want     bool
	}{
		{name: "default", want: true},
		{name: "ChatGPT", settings: config.Settings{OpenAIBaseURL: "https://chatgpt.com/backend-api"}, want: true},
		{name: "ChatGPT root", settings: config.Settings{OpenAIBaseURL: "https://chat.openai.com/"}, want: true},
		{name: "configured provider", settings: config.Settings{ProviderOverride: "anthropic"}},
		{name: "compatible endpoint", settings: config.Settings{OpenAIBaseURL: "https://example.com/v1"}},
		{name: "insecure ChatGPT transport", settings: config.Settings{OpenAIBaseURL: "http://chatgpt.com/backend-api"}},
		{name: "custom ChatGPT port", settings: config.Settings{OpenAIBaseURL: "https://chatgpt.com:8443/backend-api"}},
		{name: "custom ChatGPT path", settings: config.Settings{OpenAIBaseURL: "https://chatgpt.com/v1"}},
		{name: "credential-bearing ChatGPT URL", settings: config.Settings{OpenAIBaseURL: "https://user@chatgpt.com/backend-api"}},
		{name: "query-bearing ChatGPT URL", settings: config.Settings{OpenAIBaseURL: "https://chatgpt.com/backend-api?token=secret"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := SupportsSubscriptionUsage(test.settings); got != test.want {
				t.Fatalf("SupportsSubscriptionUsage = %v, want %v", got, test.want)
			}
		})
	}
}

func testString(value string) *string {
	return &value
}
