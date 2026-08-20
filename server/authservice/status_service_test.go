package authservice

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"core/server/auth"
	"core/shared/authstatus"
	"core/shared/config"
	authpb "core/shared/protoapi/gen/kent/api/auth"
)

type failingAuthStatusStore struct {
	err error
}

func (s failingAuthStatusStore) Load(context.Context) (auth.State, error) {
	return auth.State{}, s.err
}

func (failingAuthStatusStore) Save(context.Context, auth.State) error {
	return nil
}

type countingAuthStatusStore struct {
	state auth.State
	loads int
}

func (s *countingAuthStatusStore) Load(context.Context) (auth.State, error) {
	s.loads++
	return s.state, nil
}

func (*countingAuthStatusStore) Save(context.Context, auth.State) error {
	return nil
}

func TestStatusServiceLoadsAuthStateOnce(t *testing.T) {
	store := &countingAuthStatusStore{state: auth.State{
		Method: auth.Method{Type: auth.MethodAPIKey, APIKey: &auth.APIKeyMethod{Key: "sk-secret-1234"}},
	}}
	service := NewStatusService(auth.NewManager(store, nil, time.Now), config.Settings{})

	if _, err := service.GetStatus(context.Background(), &authpb.GetStatusRequest{}); err != nil {
		t.Fatalf("GetAuthStatus: %v", err)
	}
	if store.loads != 1 {
		t.Fatalf("auth state loads = %d, want 1", store.loads)
	}
}

func TestFetchUsagePayloadUsesTypedOAuthHeaders(t *testing.T) {
	var authorization, accountHeader, acceptEncoding string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		accountHeader = r.Header.Get("ChatGPT-Account-Id")
		acceptEncoding = r.Header.Get("Accept-Encoding")
		payload, _ := json.Marshal(usagePayload{PlanType: "pro"})
		var compressed bytes.Buffer
		writer := gzip.NewWriter(&compressed)
		_, _ = writer.Write(payload)
		_ = writer.Close()
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(compressed.Bytes())
	}))
	defer server.Close()

	_, err := fetchUsagePayload(context.Background(), server.URL, auth.State{
		Method: auth.Method{Type: auth.MethodOAuth, OAuth: &auth.OAuthMethod{
			AccessToken: "access-token",
			TokenType:   "Bearer",
			AccountID:   "acct-1",
		}},
	})
	if err != nil {
		t.Fatalf("fetchUsagePayload: %v", err)
	}
	if authorization != "Bearer access-token" || accountHeader != "acct-1" {
		t.Fatalf("headers = authorization %q account %q", authorization, accountHeader)
	}
	if acceptEncoding != "zstd,gzip" {
		t.Fatalf("Accept-Encoding = %q, want zstd,gzip", acceptEncoding)
	}
}

func TestStatusServicePublishesUnavailableWhenInitialLoadFails(t *testing.T) {
	wantErr := errors.New("permission denied")
	service := NewStatusService(
		auth.NewManager(failingAuthStatusStore{err: wantErr}, nil, time.Now),
		config.Settings{},
	)

	response, err := service.GetStatus(context.Background(), &authpb.GetStatusRequest{})
	if err != nil {
		t.Fatalf("GetAuthStatus: %v", err)
	}
	if response.Resolution.GetUnavailable() == nil ||
		response.Resolution.GetUnavailable().Cause != wantErr.Error() {
		t.Fatalf("resolution = %+v", response.Resolution)
	}
	if response.Subscription.Applicable {
		t.Fatalf("subscription = %+v, want not applicable", response.Subscription)
	}
}

func TestStatusServiceRetainsOAuthFactsWhenRefreshFails(t *testing.T) {
	now := time.Date(2026, time.August, 9, 3, 0, 0, 0, time.UTC)
	store := auth.NewMemoryStore(auth.State{
		Scope: auth.ScopeGlobal,
		Method: auth.Method{Type: auth.MethodOAuth, OAuth: &auth.OAuthMethod{
			AccessToken:  "stale",
			RefreshToken: "refresh",
			TokenType:    "Bearer",
			Expiry:       now.Add(-time.Minute),
			AccountID:    "acct-1",
			Email:        "user@example.com",
		}},
		EnvAPIKeyPreference: auth.EnvAPIKeyPreferencePreferSaved,
	})
	refreshErr := errors.New("refresh failed")
	refresher := auth.NewOAuthRefresher(
		func() time.Time { return now },
		30*time.Second,
		func(context.Context, auth.Method) (auth.Method, error) {
			return auth.Method{}, refreshErr
		},
	)
	service := NewStatusService(auth.NewManager(store, refresher, func() time.Time { return now }), config.Settings{})

	response, err := service.GetStatus(context.Background(), &authpb.GetStatusRequest{})
	if err != nil {
		t.Fatalf("GetAuthStatus: %v", err)
	}
	facts := response.Resolution.GetKnown()
	if response.Resolution.PartialFailure == nil ||
		response.Resolution.PartialFailure.Cause != refreshErr.Error() ||
		facts == nil ||
		facts.Method != authpb.AuthMethod_AUTH_METHOD_OAUTH ||
		facts.GetOauth() == nil ||
		facts.GetOauth().Email == nil ||
		*facts.GetOauth().Email != "user@example.com" {
		t.Fatalf("resolution = %+v", response.Resolution)
	}
	if !response.Subscription.Applicable ||
		response.Subscription.Failure == nil ||
		response.Subscription.Failure.Cause != refreshErr.Error() {
		t.Fatalf("subscription = %+v", response.Subscription)
	}
}

func TestStatusServicePublishesOnlySafeAPIKeyFacts(t *testing.T) {
	tests := []struct {
		name       string
		key        string
		wantSuffix *string
	}{
		{name: "short", key: "abc"},
		{name: "four runes", key: "abcd"},
		{name: "long", key: "sk-secret-1234", wantSuffix: authStatusTestString("1234")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := NewStatusService(auth.NewManager(auth.NewMemoryStore(auth.State{
				Method: auth.Method{Type: auth.MethodAPIKey, APIKey: &auth.APIKeyMethod{Key: test.key}},
			}), nil, time.Now), config.Settings{ProviderOverride: "anthropic"})

			response, err := service.GetStatus(context.Background(), &authpb.GetStatusRequest{})
			if err != nil {
				t.Fatalf("GetAuthStatus: %v", err)
			}
			facts := response.Resolution.GetKnown()
			if facts == nil || facts.GetApiKey() == nil || facts.Provider.Identifier != "anthropic" {
				t.Fatalf("facts = %+v", facts)
			}
			if !reflect.DeepEqual(facts.GetApiKey().Suffix, test.wantSuffix) {
				t.Fatalf("suffix = %v, want %v", facts.GetApiKey().Suffix, test.wantSuffix)
			}
		})
	}
}

func TestStatusServiceUsesRequestedEffectiveProviderForSubscription(t *testing.T) {
	now := time.Date(2026, time.August, 9, 9, 0, 0, 0, time.UTC)
	store := auth.NewMemoryStore(auth.State{
		Method: auth.Method{Type: auth.MethodOAuth, OAuth: &auth.OAuthMethod{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			TokenType:    "Bearer",
			Expiry:       now.Add(-time.Minute),
		}},
	})
	refreshErr := errors.New("refresh failed")
	refresher := auth.NewOAuthRefresher(
		func() time.Time { return now },
		30*time.Second,
		func(context.Context, auth.Method) (auth.Method, error) {
			return auth.Method{}, refreshErr
		},
	)
	service := NewStatusService(auth.NewManager(store, refresher, func() time.Time { return now }), config.Settings{
		OpenAIBaseURL: "https://daemon.example/v1",
	})

	global, err := service.GetStatus(context.Background(), &authpb.GetStatusRequest{})
	if err != nil {
		t.Fatalf("global GetAuthStatus: %v", err)
	}
	if global.Subscription.Applicable {
		t.Fatalf("global custom-provider subscription = %+v, want not applicable", global.Subscription)
	}

	selection := authstatus.ProviderSelection(config.Settings{
		OpenAIBaseURL: "https://session.example/v1",
	})
	effective, err := service.GetStatus(context.Background(), &authpb.GetStatusRequest{Provider: selection})
	if err != nil {
		t.Fatalf("effective GetAuthStatus: %v", err)
	}
	if effective.Resolution.GetKnown() == nil ||
		effective.Resolution.GetKnown().Provider.Identifier != "openai-compatible" ||
		effective.Resolution.GetKnown().Provider.Kind != authpb.ProviderKind_PROVIDER_KIND_OPENAI_COMPATIBLE ||
		effective.Subscription.Applicable {
		t.Fatalf("effective subscription response = %+v", effective)
	}
}

func TestStatusServiceSkipsSubscriptionUsageWhenRequested(t *testing.T) {
	now := time.Date(2026, time.August, 9, 11, 0, 0, 0, time.UTC)
	service := NewStatusService(auth.NewManager(auth.NewMemoryStore(auth.State{
		Method: auth.Method{Type: auth.MethodOAuth, OAuth: &auth.OAuthMethod{
			AccessToken: "access-token",
			TokenType:   "Bearer",
			Expiry:      now.Add(time.Hour),
		}},
	}), nil, func() time.Time { return now }), config.Settings{})

	response, err := service.GetStatus(context.Background(), &authpb.GetStatusRequest{
		SkipSubscriptionUsage: true,
	})
	if err != nil {
		t.Fatalf("GetAuthStatus: %v", err)
	}
	if response.Resolution.GetKnown() == nil ||
		response.Resolution.GetKnown().Method != authpb.AuthMethod_AUTH_METHOD_OAUTH ||
		response.Subscription.Applicable {
		t.Fatalf("method-only auth status = %+v", response)
	}
}

func TestStatusServiceUsesCanonicalRuntimeProviderResolution(t *testing.T) {
	tests := []struct {
		name     string
		settings config.Settings
		wantKind authpb.ProviderKind
		wantID   string
	}{
		{
			name:     "explicit OpenAI provider",
			settings: config.Settings{ProviderOverride: "openai"},
			wantKind: authpb.ProviderKind_PROVIDER_KIND_OPENAI,
			wantID:   "openai",
		},
		{
			name:     "model-inferred provider",
			settings: config.Settings{Model: "claude-3-7-sonnet"},
			wantKind: authpb.ProviderKind_PROVIDER_KIND_CONFIGURED_PROVIDER,
			wantID:   "anthropic",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := NewStatusService(nil, test.settings)
			response, err := service.GetStatus(context.Background(), &authpb.GetStatusRequest{})
			if err != nil {
				t.Fatalf("GetAuthStatus: %v", err)
			}
			provider := response.Resolution.GetKnown().Provider
			if provider.Kind != test.wantKind || provider.Identifier != test.wantID {
				t.Fatalf("provider = %+v, want kind %q identifier %q", provider, test.wantKind, test.wantID)
			}
		})
	}
}

func TestUsageWindowFactsKeepStableDuplicateDurations(t *testing.T) {
	resetAt := time.Date(2026, time.August, 9, 5, 0, 0, 0, time.UTC).Unix()
	windows, err := usageWindowFacts(usagePayload{
		RateLimit: &usageRateLimit{
			PrimaryWindow: &usageWindow{UsedPercent: 10, LimitWindowSeconds: 5 * 3600, ResetAt: resetAt},
		},
		AdditionalRateLimits: []usageExtraBucket{{
			LimitName:      "vision",
			MeteredFeature: "images",
			RateLimit: &usageRateLimit{
				PrimaryWindow: &usageWindow{UsedPercent: 20, LimitWindowSeconds: 5 * 3600, ResetAt: resetAt},
			},
		}},
	})
	if err != nil {
		t.Fatalf("usageWindowFacts: %v", err)
	}
	if len(windows) != 2 ||
		windows[0].Bucket != authpb.SubscriptionWindowBucket_SUBSCRIPTION_WINDOW_BUCKET_DEFAULT ||
		windows[1].Bucket != authpb.SubscriptionWindowBucket_SUBSCRIPTION_WINDOW_BUCKET_ADDITIONAL ||
		windows[1].LimitName == nil || *windows[1].LimitName != "vision" ||
		windows[1].MeteredFeature == nil || *windows[1].MeteredFeature != "images" {
		t.Fatalf("windows = %+v", windows)
	}
}

func TestUsageWindowFactsRejectsNonPositiveDurations(t *testing.T) {
	_, err := usageWindowFacts(usagePayload{
		RateLimit: &usageRateLimit{
			PrimaryWindow: &usageWindow{UsedPercent: 10},
		},
	})
	if err == nil {
		t.Fatal("non-positive usage duration was accepted")
	}
}

func authStatusTestString(value string) *string {
	return &value
}
