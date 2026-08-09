package testsetup

import (
	"time"

	"core/shared/serverapi"
)

type AuthStatusTransportCase struct {
	Name     string
	Response serverapi.AuthStatusResponse
}

func AuthStatusTransportCases() []AuthStatusTransportCase {
	resetAt := time.Date(2026, time.August, 9, 5, 0, 0, 0, time.UTC)
	stringPointer := func(value string) *string { return &value }
	oauthFacts := serverapi.AuthStatusFacts{
		Method: serverapi.AuthStatusMethodOAuth,
		Provider: serverapi.AuthProviderFacts{
			Kind:       serverapi.AuthProviderKindOpenAICompatible,
			Identifier: "openai-compatible",
			DisplayOrigin: &serverapi.AuthProviderDisplayOrigin{
				Scheme: "https", Hostname: "example.com", Port: stringPointer("8443"),
			},
		},
		EnvPreference: serverapi.AuthStatusEnvPreferencePreferEnv,
		OAuth: &serverapi.AuthOAuthFacts{
			AccountID: stringPointer("acct-1"), Email: stringPointer("user@example.com"),
		},
	}
	return []AuthStatusTransportCase{
		{
			Name: "oauth subscription facts",
			Response: serverapi.AuthStatusResponse{
				Resolution: serverapi.KnownAuthStatusResolution(oauthFacts, nil),
				Subscription: serverapi.AuthSubscriptionFacts{
					Applicable: true,
					Plan:       stringPointer("pro"),
					Windows: []serverapi.AuthSubscriptionWindowFacts{
						{
							Bucket: serverapi.AuthSubscriptionWindowBucketDefault, DurationSecs: 5 * 60 * 60,
							UsedPercent: 12.5, ResetAt: &resetAt,
						},
						{
							Bucket: serverapi.AuthSubscriptionWindowBucketAdditional, DurationSecs: 5 * 60 * 60,
							UsedPercent: 37.5, ResetAt: &resetAt, LimitName: stringPointer("vision"),
							MeteredFeature: stringPointer("images"),
						},
					},
				},
			},
		},
		{
			Name: "auth and subscription failures",
			Response: serverapi.AuthStatusResponse{
				Resolution: serverapi.KnownAuthStatusResolution(
					oauthFacts,
					&serverapi.AuthStatusFailure{Cause: "oauth refresh failed"},
				),
				Subscription: serverapi.AuthSubscriptionFacts{
					Applicable: true,
					Failure:    &serverapi.AuthStatusFailure{Cause: "subscription usage unavailable"},
				},
			},
		},
		{
			Name: "api key redacted suffix and environment preference",
			Response: serverapi.AuthStatusResponse{
				Resolution: serverapi.KnownAuthStatusResolution(serverapi.AuthStatusFacts{
					Method:        serverapi.AuthStatusMethodAPIKey,
					Provider:      serverapi.OpenAIAuthProviderFacts(),
					EnvPreference: serverapi.AuthStatusEnvPreferencePreferEnv,
					APIKey:        &serverapi.AuthAPIKeyFacts{Suffix: stringPointer("1234")},
				}, nil),
			},
		},
		{
			Name: "authoritative known no auth",
			Response: serverapi.AuthStatusResponse{
				Resolution: serverapi.KnownAuthStatusResolution(serverapi.AuthStatusFacts{
					Method:        serverapi.AuthStatusMethodNone,
					Provider:      serverapi.OpenAIAuthProviderFacts(),
					EnvPreference: serverapi.AuthStatusEnvPreferenceUnspecified,
				}, nil),
			},
		},
		{
			Name: "unavailable initial resolution",
			Response: serverapi.AuthStatusResponse{
				Resolution: serverapi.UnavailableAuthStatusResolution(
					serverapi.AuthStatusFailure{Cause: "initial auth resolution failed"},
				),
			},
		},
	}
}
