package serverapi

import (
	"encoding/json"
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
	wire := decodeAuthStatusWireProjection(t, body)
	if wire.Resolution.Facts == nil {
		t.Fatal("known resolution omitted facts")
	}
	apiKey := wire.Resolution.Facts.APIKey
	if len(apiKey) != 1 {
		t.Fatalf("API-key wire fields = %v, want only suffix", apiKey)
	}
	if got := decodeAuthStatusWireField[string](t, apiKey, "suffix"); got != suffix {
		t.Fatalf("API-key suffix = %q, want %q", got, suffix)
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
	wire := decodeAuthStatusWireProjection(t, body)
	if wire.Resolution.Facts == nil {
		t.Fatal("known resolution omitted facts")
	}
	displayOrigin := wire.Resolution.Facts.Provider.DisplayOrigin
	if len(displayOrigin) != 3 {
		t.Fatalf("display-origin wire fields = %v, want scheme, hostname, and port", displayOrigin)
	}
	if got := decodeAuthStatusWireField[string](t, displayOrigin, "scheme"); got != "https" {
		t.Fatalf("display-origin scheme = %q, want https", got)
	}
	if got := decodeAuthStatusWireField[string](t, displayOrigin, "hostname"); got != "example.com" {
		t.Fatalf("display-origin hostname = %q, want example.com", got)
	}
	if got := decodeAuthStatusWireField[string](t, displayOrigin, "port"); got != port {
		t.Fatalf("display-origin port = %q, want %q", got, port)
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

type authStatusWireProjection struct {
	Resolution struct {
		Facts *struct {
			APIKey   map[string]json.RawMessage `json:"api_key"`
			Provider struct {
				DisplayOrigin map[string]json.RawMessage `json:"display_origin"`
			} `json:"provider"`
		} `json:"facts"`
	} `json:"resolution"`
}

func decodeAuthStatusWireProjection(t *testing.T, body []byte) authStatusWireProjection {
	t.Helper()
	var wire authStatusWireProjection
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("decode auth-status JSON: %v", err)
	}
	return wire
}

func decodeAuthStatusWireField[T any](t *testing.T, object map[string]json.RawMessage, name string) T {
	t.Helper()
	raw, ok := object[name]
	if !ok {
		t.Fatalf("auth-status JSON omitted %q", name)
	}
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode auth-status JSON field %q: %v", name, err)
	}
	return value
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
