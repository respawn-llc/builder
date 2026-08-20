package status

import (
	"errors"
	"net/url"
	"reflect"
	"testing"
	"time"

	authpb "core/shared/protoapi/gen/kent/api/auth"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestAuthStageFromResponseProjectsTypedMethods(t *testing.T) {
	shortKey := &authpb.Status{
		Resolution: &authpb.StatusResolution{
			Resolution: &authpb.StatusResolution_Known{Known: &authpb.StatusFacts{
				Method: authpb.AuthMethod_AUTH_METHOD_API_KEY,
				Provider: &authpb.ProviderFacts{
					Kind:       authpb.ProviderKind_PROVIDER_KIND_OPENAI,
					Identifier: "openai",
				},
				EnvPreference: authpb.EnvironmentPreference_ENVIRONMENT_PREFERENCE_PREFER_ENV_API_KEY,
				MethodFacts:   &authpb.StatusFacts_ApiKey{ApiKey: &authpb.APIKeyFacts{}},
			}},
		},
		Subscription: &authpb.SubscriptionFacts{},
	}
	longSuffix := "1234"
	longKey := &authpb.Status{
		Resolution: &authpb.StatusResolution{
			Resolution: &authpb.StatusResolution_Known{Known: &authpb.StatusFacts{
				Method: authpb.AuthMethod_AUTH_METHOD_API_KEY,
				Provider: &authpb.ProviderFacts{
					Kind:       authpb.ProviderKind_PROVIDER_KIND_OPENAI,
					Identifier: "openai",
				},
				EnvPreference: authpb.EnvironmentPreference_ENVIRONMENT_PREFERENCE_PREFER_SAVED_AUTH,
				MethodFacts:   &authpb.StatusFacts_ApiKey{ApiKey: &authpb.APIKeyFacts{Suffix: &longSuffix}},
			}},
		},
		Subscription: &authpb.SubscriptionFacts{},
	}

	short := AuthStageFromResponse(shortKey)
	if short.Auth.Summary != "API Key" ||
		AuthDisplayLabel(short.Auth) != "OpenAI API Key" ||
		!reflect.DeepEqual(short.Auth.Details, []string{"OpenAI", "OPENAI_API_KEY preferred"}) {
		t.Fatalf("short key projection = %+v", short)
	}
	long := AuthStageFromResponse(longKey)
	if long.Auth.Summary != "API Key ...1234" ||
		!reflect.DeepEqual(long.Auth.Details, []string{"OpenAI", "saved auth preferred"}) {
		t.Fatalf("long key projection = %+v", long)
	}
}

func TestAuthStageFromResponseProjectsUnavailableAndRetainedOAuth(t *testing.T) {
	unavailable := AuthStageFromResponse(&authpb.Status{
		Resolution: &authpb.StatusResolution{
			Resolution: &authpb.StatusResolution_Unavailable{
				Unavailable: &authpb.StatusFailure{Cause: "permission denied"},
			},
		},
		Subscription: &authpb.SubscriptionFacts{},
	})
	if unavailable.Auth.Summary != "Auth unavailable" ||
		!unavailable.Auth.Unavailable ||
		unavailable.Warning != "auth: permission denied" {
		t.Fatalf("unavailable projection = %+v", unavailable)
	}

	email := "user@example.com"
	refreshFailure := &authpb.StatusFailure{Cause: "refresh failed"}
	retained := AuthStageFromResponse(&authpb.Status{
		Resolution: &authpb.StatusResolution{
			Resolution: &authpb.StatusResolution_Known{Known: &authpb.StatusFacts{
				Method: authpb.AuthMethod_AUTH_METHOD_OAUTH,
				Provider: &authpb.ProviderFacts{
					Kind:       authpb.ProviderKind_PROVIDER_KIND_OPENAI,
					Identifier: "openai",
				},
				EnvPreference: authpb.EnvironmentPreference_ENVIRONMENT_PREFERENCE_PREFER_SAVED_AUTH,
				MethodFacts:   &authpb.StatusFacts_Oauth{Oauth: &authpb.OAuthFacts{Email: &email}},
			}},
			PartialFailure: refreshFailure,
		},
		Subscription: &authpb.SubscriptionFacts{
			Applicable: true,
			Failure:    refreshFailure,
		},
	})
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
	response := &authpb.Status{
		Resolution: &authpb.StatusResolution{
			Resolution: &authpb.StatusResolution_Known{Known: &authpb.StatusFacts{
				Method: authpb.AuthMethod_AUTH_METHOD_API_KEY,
				Provider: &authpb.ProviderFacts{
					Kind:       authpb.ProviderKind_PROVIDER_KIND_OPENAI_COMPATIBLE,
					Identifier: "openai-compatible",
					DisplayOrigin: &authpb.ProviderDisplayOrigin{
						Scheme:   "https",
						Hostname: "example.com",
						Port:     &port,
					},
				},
				EnvPreference: authpb.EnvironmentPreference_ENVIRONMENT_PREFERENCE_UNSPECIFIED,
				MethodFacts:   &authpb.StatusFacts_ApiKey{ApiKey: &authpb.APIKeyFacts{}},
			}},
		},
		Subscription: &authpb.SubscriptionFacts{},
	}
	projected := AuthStageFromResponse(response)
	origin := "https://example.com:8443"
	if projected.Auth.Provider != origin ||
		AuthDisplayLabel(projected.Auth) != origin+" API Key" ||
		!reflect.DeepEqual(projected.Auth.Details, []string{origin, origin}) {
		t.Fatalf("origin projection = %+v", projected.Auth)
	}
}

func TestAuthProviderDisplayOriginFormatsScopedIPv6(t *testing.T) {
	hostname := "fe80::1%en0"
	rendered := authProviderDisplayOrigin(&authpb.ProviderDisplayOrigin{
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
	response := &authpb.Status{
		Resolution: &authpb.StatusResolution{
			Resolution: &authpb.StatusResolution_Known{Known: &authpb.StatusFacts{
				Method: authpb.AuthMethod_AUTH_METHOD_OAUTH,
				Provider: &authpb.ProviderFacts{
					Kind:       authpb.ProviderKind_PROVIDER_KIND_OPENAI,
					Identifier: "openai",
				},
				EnvPreference: authpb.EnvironmentPreference_ENVIRONMENT_PREFERENCE_PREFER_SAVED_AUTH,
				MethodFacts:   &authpb.StatusFacts_Oauth{Oauth: &authpb.OAuthFacts{}},
			}},
		},
		Subscription: &authpb.SubscriptionFacts{
			Applicable: true,
			Plan:       &plan,
			Windows: []*authpb.SubscriptionWindowFacts{
				{Bucket: authpb.SubscriptionWindowBucket_SUBSCRIPTION_WINDOW_BUCKET_DEFAULT, DurationSeconds: 5 * 3600, UsedPercent: 10, ResetAt: timestamppb.New(reset)},
				{Bucket: authpb.SubscriptionWindowBucket_SUBSCRIPTION_WINDOW_BUCKET_ADDITIONAL, DurationSeconds: 5 * 3600, UsedPercent: 20, LimitName: &vision, MeteredFeature: &images},
				{Bucket: authpb.SubscriptionWindowBucket_SUBSCRIPTION_WINDOW_BUCKET_ADDITIONAL, DurationSeconds: 5 * 3600, UsedPercent: 30, LimitName: &vision, MeteredFeature: &images},
				{Bucket: authpb.SubscriptionWindowBucket_SUBSCRIPTION_WINDOW_BUCKET_DEFAULT, DurationSeconds: 90 * 24 * 3600, UsedPercent: 40},
			},
		},
	}
	projected := AuthStageFromResponse(response).Subscription
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

func TestUnavailableAuthStageKeepsSubscriptionApplicabilityUnknown(t *testing.T) {
	result := UnavailableAuthStage(errors.New("connection lost"))
	if result.Subscription.Applicable || result.Subscription.Summary != "" {
		t.Fatalf("RPC failure invented subscription applicability: %+v", result)
	}
}
