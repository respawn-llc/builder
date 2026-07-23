package status

import (
	"context"
	"core/server/auth"
	"core/shared/config"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLimitDurationMatchesCodexBuckets(t *testing.T) {
	if got := LimitDuration(300); got != "5h" {
		t.Fatalf("5h window label = %q, want %q", got, "5h")
	}
	if got := LimitDuration(60 * 24 * 7); got != "weekly" {
		t.Fatalf("weekly window label = %q, want %q", got, "weekly")
	}
}

func TestUsageWindowsByLabelKeepsNonWhitelistedHourDurations(t *testing.T) {
	windows := UsageWindowsByLabel(UsagePayload{
		RateLimit: &UsageRateLimit{
			PrimaryWindow:   &UsageWindow{UsedPercent: 10, LimitWindowSeconds: 3600},
			SecondaryWindow: &UsageWindow{UsedPercent: 20, LimitWindowSeconds: 3 * 3600},
		},
		AdditionalRateLimits: []UsageExtraBucket{{
			RateLimit: &UsageRateLimit{
				PrimaryWindow: &UsageWindow{UsedPercent: 30, LimitWindowSeconds: 24 * 3600},
			},
		}},
	})
	got := make([]string, 0, len(windows))
	for _, window := range windows {
		got = append(got, window.Label)
	}
	want := []string{"1h", "3h", "24h"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("window labels = %v, want %v", got, want)
	}
}

func TestUsageWindowsByLabelKeepsDuplicateDurationBuckets(t *testing.T) {
	resetAt := time.Date(2026, time.March, 25, 2, 0, 0, 0, time.UTC).Unix()
	windows := UsageWindowsByLabel(UsagePayload{
		RateLimit: &UsageRateLimit{
			PrimaryWindow: &UsageWindow{UsedPercent: 10, LimitWindowSeconds: 5 * 3600, ResetAt: resetAt},
		},
		AdditionalRateLimits: []UsageExtraBucket{{
			MeteredFeature: "images",
			LimitName:      "vision",
			RateLimit: &UsageRateLimit{
				PrimaryWindow: &UsageWindow{UsedPercent: 30, LimitWindowSeconds: 5 * 3600, ResetAt: resetAt},
			},
		}},
	})

	if len(windows) != 2 {
		t.Fatalf("windows len = %d, want 2", len(windows))
	}
	if windows[0].Label != "5h" || windows[1].Label != "5h" {
		t.Fatalf("window labels = %#v", windows)
	}
	if windows[0].Qualifier != "" {
		t.Fatalf("first qualifier = %q, want empty", windows[0].Qualifier)
	}
	if windows[1].Qualifier != "vision / images" {
		t.Fatalf("second qualifier = %q, want %q", windows[1].Qualifier, "vision / images")
	}
}

func TestUsageWindowsByLabelDisambiguatesDuplicateExtraBucketsWithoutUniqueQualifier(t *testing.T) {
	resetAt := time.Date(2026, time.March, 25, 2, 0, 0, 0, time.UTC).Unix()
	windows := UsageWindowsByLabel(UsagePayload{
		AdditionalRateLimits: []UsageExtraBucket{
			{
				RateLimit: &UsageRateLimit{
					PrimaryWindow: &UsageWindow{UsedPercent: 10, LimitWindowSeconds: 5 * 3600, ResetAt: resetAt},
				},
			},
			{
				RateLimit: &UsageRateLimit{
					PrimaryWindow: &UsageWindow{UsedPercent: 20, LimitWindowSeconds: 5 * 3600, ResetAt: resetAt},
				},
			},
			{
				MeteredFeature: "images",
				LimitName:      "images",
				RateLimit: &UsageRateLimit{
					PrimaryWindow: &UsageWindow{UsedPercent: 30, LimitWindowSeconds: 5 * 3600, ResetAt: resetAt},
				},
			},
			{
				MeteredFeature: "images",
				LimitName:      "images",
				RateLimit: &UsageRateLimit{
					PrimaryWindow: &UsageWindow{UsedPercent: 40, LimitWindowSeconds: 5 * 3600, ResetAt: resetAt},
				},
			},
		},
	})
	got := make([]string, 0, len(windows))
	for _, window := range windows {
		got = append(got, window.Qualifier)
	}
	want := []string{"extra", "extra #2", "images", "images #2"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("qualifiers = %v, want %v", got, want)
	}
}

func TestShouldFetchSubscriptionUsageOnlyForDefaultOpenAIOAuth(t *testing.T) {
	oauthState := auth.State{Method: auth.Method{Type: auth.MethodOAuth, OAuth: &auth.OAuthMethod{AccessToken: "access-token", AccountID: "acct-123"}}}
	if !ShouldFetchSubscriptionUsage(config.Settings{}, oauthState) {
		t.Fatal("expected default OAuth configuration to allow subscription usage fetch")
	}
	for _, baseURL := range []string{
		"https://chatgpt.com",
		"https://chatgpt.com/backend-api",
		"https://chat.openai.com",
		"https://chat.openai.com/backend-api",
	} {
		if !ShouldFetchSubscriptionUsage(config.Settings{OpenAIBaseURL: baseURL}, oauthState) {
			t.Fatalf("expected official ChatGPT base URL %q to allow subscription usage fetch", baseURL)
		}
	}
	if ShouldFetchSubscriptionUsage(config.Settings{OpenAIBaseURL: "https://example.com/backend-api"}, oauthState) {
		t.Fatal("expected custom base URL override to disable subscription usage fetch")
	}
	if ShouldFetchSubscriptionUsage(config.Settings{ProviderOverride: "anthropic"}, oauthState) {
		t.Fatal("expected provider override to disable subscription usage fetch")
	}
	if ShouldFetchSubscriptionUsage(config.Settings{}, auth.State{}) {
		t.Fatal("expected non-OAuth auth state to disable subscription usage fetch")
	}
}

func TestCollectSubscriptionStatusDoesNotFetchForOverrides(t *testing.T) {
	called := false
	fetcher := func(_ context.Context, baseURL string, _ auth.State) (UsagePayload, error) {
		called = true
		if baseURL != DefaultUsageBaseURL {
			t.Fatalf("usage fetch base URL = %q, want %q", baseURL, DefaultUsageBaseURL)
		}
		return UsagePayload{PlanType: "pro"}, nil
	}
	state := auth.State{Method: auth.Method{Type: auth.MethodOAuth, OAuth: &auth.OAuthMethod{AccessToken: "access-token", AccountID: "acct-123"}}}

	status := CollectSubscriptionStatus(context.Background(), requestWithSettings(config.Settings{OpenAIBaseURL: "https://example.com/backend-api"}), state, nil, fetcher, DefaultUsageBaseURL)
	if status.Applicable {
		t.Fatalf("expected overridden base URL to disable subscription status, got %+v", status)
	}
	status = CollectSubscriptionStatus(context.Background(), requestWithSettings(config.Settings{ProviderOverride: "openai-compatible"}), state, nil, fetcher, DefaultUsageBaseURL)
	if status.Applicable {
		t.Fatalf("expected provider override to disable subscription status, got %+v", status)
	}
	if called {
		t.Fatal("did not expect subscription usage fetcher to be called for overrides")
	}

	status = CollectSubscriptionStatus(context.Background(), requestWithSettings(config.Settings{OpenAIBaseURL: "https://chatgpt.com/backend-api"}), state, nil, fetcher, DefaultUsageBaseURL)
	if !status.Applicable || status.Summary != "Pro subscription" {
		t.Fatalf("expected official ChatGPT base URL to preserve subscription status, got %+v", status)
	}
	if !called {
		t.Fatal("expected subscription usage fetcher to be called for official ChatGPT base URL")
	}
}

func TestCollectSubscriptionStatusUsesFixedUsageEndpointForOfficialChatGPTHost(t *testing.T) {
	called := false
	fetcher := func(_ context.Context, baseURL string, _ auth.State) (UsagePayload, error) {
		called = true
		if baseURL != DefaultUsageBaseURL {
			t.Fatalf("usage fetch base URL = %q, want %q", baseURL, DefaultUsageBaseURL)
		}
		return UsagePayload{
			PlanType: "pro",
			RateLimit: &UsageRateLimit{
				PrimaryWindow: &UsageWindow{UsedPercent: 12.5, LimitWindowSeconds: 5 * 3600, ResetAt: 1704069000},
			},
		}, nil
	}
	state := auth.State{Method: auth.Method{Type: auth.MethodOAuth, OAuth: &auth.OAuthMethod{AccessToken: "access-token", AccountID: "acct-123"}}}
	status := CollectSubscriptionStatus(context.Background(), requestWithSettings(config.Settings{OpenAIBaseURL: "https://chatgpt.com"}), state, nil, fetcher, DefaultUsageBaseURL)
	if !called {
		t.Fatal("expected subscription usage fetcher to be called")
	}
	if !status.Applicable || status.Summary != "Pro subscription" {
		t.Fatalf("expected official ChatGPT host to preserve subscription status, got %+v", status)
	}
	if len(status.Windows) != 1 || status.Windows[0].Label != "5h" {
		t.Fatalf("expected quota window preserved, got %+v", status.Windows)
	}
}

func TestFetchUsagePayloadFetchesWhamUsageWithOAuthHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/usage" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Fatalf("authorization header = %q", got)
		}
		if got := r.Header.Get("ChatGPT-Account-Id"); got != "acct-123" {
			t.Fatalf("ChatGPT-Account-Id header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"plan_type":"pro",
			"rate_limit":{
				"primary_window":{"used_percent":12.5,"limit_window_seconds":18000,"reset_at":1704069000},
				"secondary_window":{"used_percent":40.0,"limit_window_seconds":604800,"reset_at":1704074400}
			}
		}`))
	}))
	defer server.Close()

	status, err := FetchUsagePayload(context.Background(), server.URL+"/backend-api", auth.State{Method: auth.Method{Type: auth.MethodOAuth, OAuth: &auth.OAuthMethod{AccessToken: "access-token", AccountID: "acct-123"}}})
	if err != nil {
		t.Fatalf("fetch status usage payload: %v", err)
	}

	if status.PlanType != "pro" {
		t.Fatalf("plan type = %q", status.PlanType)
	}
	windows := UsageWindowsByLabel(status)
	if len(windows) != 2 {
		t.Fatalf("windows len = %d", len(windows))
	}
	if windows[0].Label != "5h" || windows[1].Label != "weekly" {
		t.Fatalf("windows = %#v", windows)
	}
}

func requestWithSettings(settings config.Settings) Request {
	return Request{Settings: settings}
}

func TestFetchUsagePayloadHandlesUsageErrors(t *testing.T) {
	t.Run("non-2xx", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusBadGateway)
		}))
		defer server.Close()

		_, err := FetchUsagePayload(context.Background(), server.URL+"/backend-api", auth.State{Method: auth.Method{Type: auth.MethodOAuth, OAuth: &auth.OAuthMethod{AccessToken: "access-token"}}})
		if err == nil || !errors.Is(err, ErrUsageRequestFailed) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("decode error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"plan_type":`))
		}))
		defer server.Close()

		_, err := FetchUsagePayload(context.Background(), server.URL+"/backend-api", auth.State{Method: auth.Method{Type: auth.MethodOAuth, OAuth: &auth.OAuthMethod{AccessToken: "access-token"}}})
		if err == nil || !errors.Is(err, ErrDecodeUsageResponse) {
			t.Fatalf("err = %v", err)
		}
	})
}
