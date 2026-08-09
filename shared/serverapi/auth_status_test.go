package serverapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAuthStatusResponseValidatesKnownAndUnavailableResolution(t *testing.T) {
	known := AuthStatusResponse{
		Resolution: KnownAuthStatusResolution(AuthStatusFacts{
			Method:        AuthStatusMethodOAuth,
			Provider:      OpenAIAuthProviderFacts(),
			EnvPreference: AuthStatusEnvPreferencePreferSaved,
			OAuth:         &AuthOAuthFacts{},
		}, nil),
	}
	if err := known.Validate(); err != nil {
		t.Fatalf("known response: %v", err)
	}

	unavailable := AuthStatusResponse{
		Resolution: UnavailableAuthStatusResolution(AuthStatusFailure{Cause: "permission denied"}),
	}
	if err := unavailable.Validate(); err != nil {
		t.Fatalf("unavailable response: %v", err)
	}

	invalid := unavailable
	invalid.Resolution.Facts = known.Resolution.Facts
	if err := invalid.Validate(); err == nil {
		t.Fatal("unavailable resolution accepted known facts")
	}
}

func TestAuthStatusResponseRejectsMethodPayloadMismatch(t *testing.T) {
	tests := []AuthStatusFacts{
		{Method: AuthStatusMethodNone, Provider: OpenAIAuthProviderFacts(), EnvPreference: AuthStatusEnvPreferenceUnspecified, OAuth: &AuthOAuthFacts{}},
		{Method: AuthStatusMethodOAuth, Provider: OpenAIAuthProviderFacts(), EnvPreference: AuthStatusEnvPreferenceUnspecified},
		{Method: AuthStatusMethodAPIKey, Provider: OpenAIAuthProviderFacts(), EnvPreference: AuthStatusEnvPreferenceUnspecified, APIKey: &AuthAPIKeyFacts{Suffix: authStatusStringPointer("abc")}},
	}
	for _, facts := range tests {
		response := AuthStatusResponse{Resolution: KnownAuthStatusResolution(facts, nil)}
		if err := response.Validate(); err == nil {
			t.Fatalf("invalid method payload accepted: %+v", facts)
		}
	}
}

func TestAuthStatusResponseCarriesOnlyRedactedAPIKeyFacts(t *testing.T) {
	suffix := "1234"
	response := AuthStatusResponse{
		Resolution: KnownAuthStatusResolution(AuthStatusFacts{
			Method:        AuthStatusMethodAPIKey,
			Provider:      OpenAIAuthProviderFacts(),
			EnvPreference: AuthStatusEnvPreferencePreferEnv,
			APIKey:        &AuthAPIKeyFacts{Suffix: &suffix},
		}, nil),
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("response: %v", err)
	}
	body, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if strings.Contains(string(body), "secret") || !strings.Contains(string(body), `"suffix":"1234"`) {
		t.Fatalf("unexpected API-key wire facts: %s", body)
	}
}

func TestAuthStatusProviderDisplayOriginIsStructural(t *testing.T) {
	port := "8443"
	response := AuthStatusResponse{
		Resolution: KnownAuthStatusResolution(AuthStatusFacts{
			Method: AuthStatusMethodNone,
			Provider: AuthProviderFacts{
				Kind:       AuthProviderKindOpenAICompatible,
				Identifier: "openai-compatible",
				DisplayOrigin: &AuthProviderDisplayOrigin{
					Scheme:   "https",
					Hostname: "example.com",
					Port:     &port,
				},
			},
			EnvPreference: AuthStatusEnvPreferenceUnspecified,
		}, nil),
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("response: %v", err)
	}
	body, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	for _, secretComponent := range []string{"userinfo", "secret", "query", "fragment", "/v1"} {
		if strings.Contains(string(body), secretComponent) {
			t.Fatalf("display origin leaked %q: %s", secretComponent, body)
		}
	}
}

func TestAuthStatusProviderDisplayOriginRejectsEmbeddedURLSyntax(t *testing.T) {
	for _, hostname := range []string{
		"user@example.com",
		"example.com:8443",
		"example.com/path",
		"example.com?token=secret",
		"example.com#fragment",
		"example.com\nattacker",
		"fe80::1%eth0/path",
		"fe80::1%eth0?token=secret",
		"fe80::1%user@example.com",
	} {
		response := authStatusResponseWithDisplayHostname(hostname)
		if err := response.Validate(); err == nil {
			t.Fatalf("display origin hostname %q was accepted", hostname)
		}
	}
}

func TestAuthStatusProviderDisplayOriginAcceptsIPHostnames(t *testing.T) {
	for _, hostname := range []string{"127.0.0.1", "2001:db8::1", "fe80::1%en0"} {
		if err := authStatusResponseWithDisplayHostname(hostname).Validate(); err != nil {
			t.Fatalf("display origin hostname %q: %v", hostname, err)
		}
	}
}

func TestAuthStatusSubscriptionFactsValidateWindowShape(t *testing.T) {
	resetAt := time.Date(2026, time.August, 9, 5, 0, 0, 0, time.UTC)
	limitName := "vision"
	feature := "images"
	response := AuthStatusResponse{
		Resolution: KnownAuthStatusResolution(AuthStatusFacts{
			Method:        AuthStatusMethodOAuth,
			Provider:      OpenAIAuthProviderFacts(),
			EnvPreference: AuthStatusEnvPreferencePreferSaved,
			OAuth:         &AuthOAuthFacts{},
		}, nil),
		Subscription: AuthSubscriptionFacts{
			Applicable: true,
			Windows: []AuthSubscriptionWindowFacts{{
				Bucket:         AuthSubscriptionWindowBucketAdditional,
				DurationSecs:   5 * 60 * 60,
				UsedPercent:    12.5,
				ResetAt:        &resetAt,
				LimitName:      &limitName,
				MeteredFeature: &feature,
			}},
		},
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("response: %v", err)
	}

	response.Subscription.Applicable = false
	if err := response.Validate(); err == nil {
		t.Fatal("non-applicable subscription accepted windows")
	}
}

func authStatusStringPointer(value string) *string {
	return &value
}

func authStatusResponseWithDisplayHostname(hostname string) AuthStatusResponse {
	return AuthStatusResponse{
		Resolution: KnownAuthStatusResolution(AuthStatusFacts{
			Method: AuthStatusMethodNone,
			Provider: AuthProviderFacts{
				Kind:       AuthProviderKindOpenAICompatible,
				Identifier: "openai-compatible",
				DisplayOrigin: &AuthProviderDisplayOrigin{
					Scheme:   "https",
					Hostname: hostname,
				},
			},
			EnvPreference: AuthStatusEnvPreferenceUnspecified,
		}, nil),
	}
}
