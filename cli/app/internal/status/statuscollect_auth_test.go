package status

import (
	"errors"
	"net/url"
	"reflect"
	"testing"
	"time"

	"core/shared/serverapi"
)

func TestAuthStageFromResponseProjectsTypedMethods(t *testing.T) {
	shortKey := serverapi.AuthStatusResponse{
		Resolution: serverapi.KnownAuthStatusResolution(serverapi.AuthStatusFacts{
			Method:        serverapi.AuthStatusMethodAPIKey,
			Provider:      serverapi.OpenAIAuthProviderFacts(),
			EnvPreference: serverapi.AuthStatusEnvPreferencePreferEnv,
			APIKey:        &serverapi.AuthAPIKeyFacts{},
		}, nil),
	}
	longSuffix := "1234"
	longKey := shortKey
	longKey.Resolution = serverapi.KnownAuthStatusResolution(serverapi.AuthStatusFacts{
		Method:        serverapi.AuthStatusMethodAPIKey,
		Provider:      serverapi.OpenAIAuthProviderFacts(),
		EnvPreference: serverapi.AuthStatusEnvPreferencePreferSaved,
		APIKey:        &serverapi.AuthAPIKeyFacts{Suffix: &longSuffix},
	}, nil)

	short := AuthStageFromResponse(shortKey, nil)
	if short.Auth.Summary != "API Key" ||
		AuthDisplayLabel(short.Auth) != "OpenAI API Key" ||
		!reflect.DeepEqual(short.Auth.Details, []string{"OpenAI", "OPENAI_API_KEY preferred"}) {
		t.Fatalf("short key projection = %+v", short)
	}
	long := AuthStageFromResponse(longKey, nil)
	if long.Auth.Summary != "API Key ...1234" ||
		!reflect.DeepEqual(long.Auth.Details, []string{"OpenAI", "saved auth preferred"}) {
		t.Fatalf("long key projection = %+v", long)
	}
}

func TestAuthStageFromResponseProjectsUnavailableAndRetainedOAuth(t *testing.T) {
	unavailable := AuthStageFromResponse(serverapi.AuthStatusResponse{
		Resolution: serverapi.UnavailableAuthStatusResolution(serverapi.AuthStatusFailure{Cause: "permission denied"}),
	}, nil)
	if unavailable.Auth.Summary != "Auth unavailable" ||
		!unavailable.Auth.Unavailable ||
		unavailable.Warning != "auth: permission denied" {
		t.Fatalf("unavailable projection = %+v", unavailable)
	}

	email := "user@example.com"
	refreshFailure := serverapi.AuthStatusFailure{Cause: "refresh failed"}
	retained := AuthStageFromResponse(serverapi.AuthStatusResponse{
		Resolution: serverapi.KnownAuthStatusResolution(serverapi.AuthStatusFacts{
			Method:        serverapi.AuthStatusMethodOAuth,
			Provider:      serverapi.OpenAIAuthProviderFacts(),
			EnvPreference: serverapi.AuthStatusEnvPreferencePreferSaved,
			OAuth:         &serverapi.AuthOAuthFacts{Email: &email},
		}, &refreshFailure),
		Subscription: serverapi.AuthSubscriptionFacts{
			Applicable: true,
			Failure:    &refreshFailure,
		},
	}, nil)
	if retained.Auth.Summary != email ||
		AuthDisplayLabel(retained.Auth) != "OpenAI Subscription" ||
		!reflect.DeepEqual(retained.Auth.Details, []string{"refresh failed"}) ||
		retained.Subscription.Summary != "Subscription unavailable: refresh failed" ||
		retained.Warning != "auth: refresh failed" {
		t.Fatalf("retained OAuth projection = %+v", retained)
	}
}

func TestAuthStageFromResponseProjectsCredentialFreeProviderOrigin(t *testing.T) {
	port := "8443"
	response := serverapi.AuthStatusResponse{
		Resolution: serverapi.KnownAuthStatusResolution(serverapi.AuthStatusFacts{
			Method: serverapi.AuthStatusMethodAPIKey,
			Provider: serverapi.AuthProviderFacts{
				Kind:       serverapi.AuthProviderKindOpenAICompatible,
				Identifier: "openai-compatible",
				DisplayOrigin: &serverapi.AuthProviderDisplayOrigin{
					Scheme:   "https",
					Hostname: "example.com",
					Port:     &port,
				},
			},
			EnvPreference: serverapi.AuthStatusEnvPreferenceUnspecified,
			APIKey:        &serverapi.AuthAPIKeyFacts{},
		}, nil),
	}
	projected := AuthStageFromResponse(response, nil)
	origin := "https://example.com:8443"
	if projected.Auth.Provider != origin ||
		AuthDisplayLabel(projected.Auth) != origin+" API Key" ||
		!reflect.DeepEqual(projected.Auth.Details, []string{origin, origin}) {
		t.Fatalf("origin projection = %+v", projected.Auth)
	}
}

func TestAuthProviderDisplayOriginFormatsScopedIPv6(t *testing.T) {
	hostname := "fe80::1%en0"
	rendered := authProviderDisplayOrigin(&serverapi.AuthProviderDisplayOrigin{
		Scheme:   "https",
		Hostname: hostname,
	})
	parsed, err := url.Parse(rendered)
	if err != nil ||
		parsed.Scheme != "https" ||
		parsed.Hostname() != hostname ||
		parsed.Port() != "" {
		t.Fatalf("scoped IPv6 origin = %q, parsed=%+v, error=%v", rendered, parsed, err)
	}
}

func TestSubscriptionProjectionPreservesDurationAndDuplicateBuckets(t *testing.T) {
	reset := time.Date(2026, time.August, 9, 5, 0, 0, 0, time.UTC)
	vision := "vision"
	images := "images"
	plan := "pro"
	response := serverapi.AuthStatusResponse{
		Resolution: serverapi.KnownAuthStatusResolution(serverapi.AuthStatusFacts{
			Method:        serverapi.AuthStatusMethodOAuth,
			Provider:      serverapi.OpenAIAuthProviderFacts(),
			EnvPreference: serverapi.AuthStatusEnvPreferencePreferSaved,
			OAuth:         &serverapi.AuthOAuthFacts{},
		}, nil),
		Subscription: serverapi.AuthSubscriptionFacts{
			Applicable: true,
			Plan:       &plan,
			Windows: []serverapi.AuthSubscriptionWindowFacts{
				{Bucket: serverapi.AuthSubscriptionWindowBucketDefault, DurationSecs: 5 * 3600, UsedPercent: 10, ResetAt: &reset},
				{Bucket: serverapi.AuthSubscriptionWindowBucketAdditional, DurationSecs: 5 * 3600, UsedPercent: 20, LimitName: &vision, MeteredFeature: &images},
				{Bucket: serverapi.AuthSubscriptionWindowBucketAdditional, DurationSecs: 5 * 3600, UsedPercent: 30, LimitName: &vision, MeteredFeature: &images},
				{Bucket: serverapi.AuthSubscriptionWindowBucketDefault, DurationSecs: 90 * 24 * 3600, UsedPercent: 40},
			},
		},
	}
	projected := AuthStageFromResponse(response, nil).Subscription
	if projected.Summary != "Pro subscription" || len(projected.Windows) != 4 {
		t.Fatalf("subscription = %+v", projected)
	}
	got := []string{
		projected.Windows[0].Label + ":" + projected.Windows[0].Qualifier,
		projected.Windows[1].Label + ":" + projected.Windows[1].Qualifier,
		projected.Windows[2].Label + ":" + projected.Windows[2].Qualifier,
		projected.Windows[3].Label + ":" + projected.Windows[3].Qualifier,
	}
	want := []string{"5h:", "5h:vision / images", "5h:vision / images #2", "90d:"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("window projections = %v, want %v", got, want)
	}
}

func TestUnavailableAuthStageSurfacesRPCFailure(t *testing.T) {
	result := UnavailableAuthStage(errors.New("connection lost"), nil)
	if result.Subscription.Summary != "Subscription unavailable: connection lost" ||
		result.Subscription.Error != "connection lost" {
		t.Fatalf("RPC failure projection = %+v", result)
	}
}

func TestUnavailableAuthStageKeepsCustomProviderSubscriptionInapplicable(t *testing.T) {
	provider := serverapi.AuthProviderFacts{
		Kind:       serverapi.AuthProviderKindConfiguredProvider,
		Identifier: "anthropic",
	}
	result := UnavailableAuthStage(errors.New("connection lost"), &provider)
	if result.Subscription.Applicable || result.Subscription.Summary != "" {
		t.Fatalf("custom-provider RPC failure projection = %+v", result)
	}
}
