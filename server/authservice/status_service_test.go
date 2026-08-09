package authservice

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"core/server/auth"
	"core/shared/config"
	"core/shared/serverapi"
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

	if _, err := service.GetAuthStatus(context.Background(), serverapi.AuthStatusRequest{}); err != nil {
		t.Fatalf("GetAuthStatus: %v", err)
	}
	if store.loads != 1 {
		t.Fatalf("auth state loads = %d, want 1", store.loads)
	}
}

func TestFetchUsagePayloadUsesTypedOAuthHeaders(t *testing.T) {
	var authorization, accountHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		accountHeader = r.Header.Get("ChatGPT-Account-Id")
		_ = json.NewEncoder(w).Encode(usagePayload{PlanType: "pro"})
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
}

func TestStatusServicePublishesUnavailableWhenInitialLoadFails(t *testing.T) {
	wantErr := errors.New("permission denied")
	service := NewStatusService(
		auth.NewManager(failingAuthStatusStore{err: wantErr}, nil, time.Now),
		config.Settings{},
	)

	response, err := service.GetAuthStatus(context.Background(), serverapi.AuthStatusRequest{})
	if err != nil {
		t.Fatalf("GetAuthStatus: %v", err)
	}
	if response.Resolution.Kind != serverapi.AuthStatusResolutionUnavailable ||
		response.Resolution.Facts != nil ||
		response.Resolution.Failure == nil ||
		response.Resolution.Failure.Cause != wantErr.Error() {
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

	response, err := service.GetAuthStatus(context.Background(), serverapi.AuthStatusRequest{})
	if err != nil {
		t.Fatalf("GetAuthStatus: %v", err)
	}
	facts := response.Resolution.Facts
	if response.Resolution.Kind != serverapi.AuthStatusResolutionKnown ||
		response.Resolution.Failure == nil ||
		response.Resolution.Failure.Cause != refreshErr.Error() ||
		facts == nil ||
		facts.Method != serverapi.AuthStatusMethodOAuth ||
		facts.OAuth == nil ||
		facts.OAuth.Email == nil ||
		*facts.OAuth.Email != "user@example.com" {
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
			}), nil, time.Now), config.Settings{ProviderOverride: "internal-openai"})

			response, err := service.GetAuthStatus(context.Background(), serverapi.AuthStatusRequest{})
			if err != nil {
				t.Fatalf("GetAuthStatus: %v", err)
			}
			facts := response.Resolution.Facts
			if facts == nil || facts.APIKey == nil || facts.Provider.Identifier != "internal-openai" {
				t.Fatalf("facts = %+v", facts)
			}
			if !reflect.DeepEqual(facts.APIKey.Suffix, test.wantSuffix) {
				t.Fatalf("suffix = %v, want %v", facts.APIKey.Suffix, test.wantSuffix)
			}
		})
	}
}

func TestAuthProviderDisplayOriginDropsCredentialBearingURLComponents(t *testing.T) {
	facts := authProviderFacts(config.Settings{
		OpenAIBaseURL: "https://user:secret@example.com:8443/v1/key?token=secret#fragment",
	})
	want := &serverapi.AuthProviderDisplayOrigin{
		Scheme:   "https",
		Hostname: "example.com",
		Port:     authStatusTestString("8443"),
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
		if got := authProviderDisplayOrigin(raw); got != nil {
			t.Fatalf("display origin for %q = %+v, want nil", raw, got)
		}
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
		windows[0].Bucket != serverapi.AuthSubscriptionWindowBucketDefault ||
		windows[1].Bucket != serverapi.AuthSubscriptionWindowBucketAdditional ||
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
