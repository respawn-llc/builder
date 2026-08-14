package authservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"core/server/auth"
	"core/server/httpcompression"
	"core/server/llm"
	servicecontract "core/shared/apicontract"
	"core/shared/authstatus"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/textutil"
)

const usageBaseURL = "https://chatgpt.com/backend-api"

type StatusService struct {
	manager  *auth.Manager
	settings config.Settings
}

func NewStatusService(manager *auth.Manager, settings config.Settings) *StatusService {
	return &StatusService{manager: manager, settings: settings}
}

func (s *StatusService) GetAuthStatus(ctx context.Context, req serverapi.AuthStatusRequest) (serverapi.AuthStatusResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.AuthStatusResponse{}, err
	}
	state := auth.EmptyState()
	if s != nil && s.manager != nil {
		loaded, err := s.manager.Load(ctx)
		if err != nil {
			return validatedAuthStatusResponse(serverapi.AuthStatusResponse{
				Resolution: serverapi.UnavailableAuthStatusResolution(authStatusFailure(err)),
			})
		}
		state = loaded
	}
	provider, subscriptionUsageSupported, err := s.resolveProvider(state, req.Provider)
	if err != nil {
		return serverapi.AuthStatusResponse{}, err
	}
	subscriptionUsageSupported = subscriptionUsageSupported && !req.SkipSubscriptionUsage
	return validatedAuthStatusResponse(serverapi.AuthStatusResponse{
		Resolution:   serverapi.KnownAuthStatusResolution(authFacts(state, provider), nil),
		Subscription: subscriptionStatus(ctx, state, nil, subscriptionUsageSupported),
	})
}

func (s *StatusService) resolveProvider(
	state auth.State,
	requested *serverapi.AuthProviderSelection,
) (serverapi.AuthProviderFacts, bool, error) {
	settings := config.Settings{}
	if s != nil {
		settings = s.settings
	}
	requestedSelection := requested != nil
	if requested != nil {
		settings = authstatus.ProviderSettings(*requested)
	}
	capabilities, err := llm.ResolveRuntimeProviderCapabilities(state, settings)
	if err != nil {
		return serverapi.AuthProviderFacts{}, false, fmt.Errorf("resolve auth status provider: %w", err)
	}
	provider := authstatus.ProviderFacts(capabilities.ProviderID, capabilities.IsOpenAIFirstParty, settings)
	if requestedSelection {
		return provider, capabilities.IsOpenAIFirstParty, nil
	}
	return provider, authstatus.SupportsSubscriptionUsage(settings, capabilities.IsOpenAIFirstParty), nil
}

func validatedAuthStatusResponse(response serverapi.AuthStatusResponse) (serverapi.AuthStatusResponse, error) {
	if err := response.Validate(); err != nil {
		return serverapi.AuthStatusResponse{}, fmt.Errorf("validate auth status response: %w", err)
	}
	return response, nil
}

func authFacts(state auth.State, provider serverapi.AuthProviderFacts) serverapi.AuthStatusFacts {
	facts := serverapi.AuthStatusFacts{
		Method:        authStatusMethod(state.Method.Type),
		Provider:      provider,
		EnvPreference: authStatusEnvPreference(state.EnvAPIKeyPreference),
	}
	switch state.Method.Type {
	case auth.MethodOAuth:
		facts.OAuth = &serverapi.AuthOAuthFacts{}
		if state.Method.OAuth != nil {
			facts.OAuth.AccountID = textutil.OptionalTrimmedString(state.Method.OAuth.AccountID)
			facts.OAuth.Email = textutil.OptionalTrimmedString(state.Method.OAuth.Email)
		}
	case auth.MethodAPIKey:
		facts.APIKey = apiKeyFacts(state.Method.APIKey)
	}
	return facts
}

func authStatusMethod(method auth.MethodType) serverapi.AuthStatusMethod {
	switch method {
	case auth.MethodOAuth:
		return serverapi.AuthStatusMethodOAuth
	case auth.MethodAPIKey:
		return serverapi.AuthStatusMethodAPIKey
	default:
		return serverapi.AuthStatusMethodNone
	}
}

func authStatusEnvPreference(preference auth.EnvAPIKeyPreference) serverapi.AuthStatusEnvPreference {
	switch preference {
	case auth.EnvAPIKeyPreferencePreferSaved:
		return serverapi.AuthStatusEnvPreferencePreferSaved
	case auth.EnvAPIKeyPreferencePreferEnv:
		return serverapi.AuthStatusEnvPreferencePreferEnv
	default:
		return serverapi.AuthStatusEnvPreferenceUnspecified
	}
}

func apiKeyFacts(method *auth.APIKeyMethod) *serverapi.AuthAPIKeyFacts {
	facts := &serverapi.AuthAPIKeyFacts{}
	if method == nil {
		return facts
	}
	runes := []rune(strings.TrimSpace(method.Key))
	if len(runes) <= 4 {
		return facts
	}
	suffix := string(runes[len(runes)-4:])
	facts.Suffix = &suffix
	return facts
}

func subscriptionStatus(
	ctx context.Context,
	state auth.State,
	authStateErr error,
	subscriptionUsageSupported bool,
) serverapi.AuthSubscriptionFacts {
	if !shouldFetchSubscriptionUsage(state, subscriptionUsageSupported) {
		return serverapi.AuthSubscriptionFacts{}
	}
	if authStateErr != nil {
		failure := authStatusFailure(authStateErr)
		return serverapi.AuthSubscriptionFacts{Applicable: true, Failure: &failure}
	}
	payload, err := fetchUsagePayload(ctx, usageBaseURL, state)
	if err != nil {
		failure := authStatusFailure(err)
		return serverapi.AuthSubscriptionFacts{Applicable: true, Failure: &failure}
	}
	windows, err := usageWindowFacts(payload)
	if err != nil {
		failure := authStatusFailure(err)
		return serverapi.AuthSubscriptionFacts{Applicable: true, Failure: &failure}
	}
	return serverapi.AuthSubscriptionFacts{
		Applicable: true,
		Plan:       textutil.OptionalTrimmedString(payload.PlanType),
		Windows:    windows,
	}
}

func shouldFetchSubscriptionUsage(state auth.State, subscriptionUsageSupported bool) bool {
	if state.Method.Type != auth.MethodOAuth ||
		state.Method.OAuth == nil ||
		!subscriptionUsageSupported {
		return false
	}
	return true
}

type usagePayload struct {
	PlanType             string             `json:"plan_type"`
	RateLimit            *usageRateLimit    `json:"rate_limit"`
	AdditionalRateLimits []usageExtraBucket `json:"additional_rate_limits"`
}

type usageExtraBucket struct {
	MeteredFeature string          `json:"metered_feature"`
	LimitName      string          `json:"limit_name"`
	RateLimit      *usageRateLimit `json:"rate_limit"`
}

type usageRateLimit struct {
	PrimaryWindow   *usageWindow `json:"primary_window"`
	SecondaryWindow *usageWindow `json:"secondary_window"`
}

type usageWindow struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int     `json:"limit_window_seconds"`
	ResetAt            int64   `json:"reset_at"`
}

func fetchUsagePayload(ctx context.Context, baseURL string, state auth.State) (usagePayload, error) {
	authorization, err := state.Method.AuthHeaderValue()
	if err != nil {
		return usagePayload{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/wham/usage", nil)
	if err != nil {
		return usagePayload{}, err
	}
	request.Header.Set("Authorization", authorization)
	request.Header.Set("User-Agent", "kent/dev")
	if state.Method.OAuth != nil {
		if accountID := strings.TrimSpace(state.Method.OAuth.AccountID); accountID != "" {
			request.Header.Set("ChatGPT-Account-Id", accountID)
		}
	}
	response, err := httpcompression.NewClient(&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		return usagePayload{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		statusErr := fmt.Errorf("usage request failed: %s", response.Status)
		return usagePayload{}, errors.Join(statusErr, response.Body.Close())
	}
	var payload usagePayload
	decodeErr := json.NewDecoder(response.Body).Decode(&payload)
	closeErr := response.Body.Close()
	if decodeErr != nil {
		return usagePayload{}, errors.Join(fmt.Errorf("decode usage response: %w", decodeErr), closeErr)
	}
	if closeErr != nil {
		return usagePayload{}, fmt.Errorf("close usage response: %w", closeErr)
	}
	return payload, nil
}

func usageWindowFacts(payload usagePayload) ([]serverapi.AuthSubscriptionWindowFacts, error) {
	type orderedWindow struct {
		facts         serverapi.AuthSubscriptionWindowFacts
		discoveryRank int
	}
	ordered := make([]orderedWindow, 0, 2+len(payload.AdditionalRateLimits)*2)
	discoveryRank := 0
	addWindow := func(window *usageWindow, bucket serverapi.AuthSubscriptionWindowBucket, limitName, feature string) error {
		if window == nil {
			return nil
		}
		durationSecs := window.LimitWindowSeconds
		if durationSecs <= 0 {
			return fmt.Errorf("usage window duration must be positive: %d", durationSecs)
		}
		facts := serverapi.AuthSubscriptionWindowFacts{
			Bucket:       bucket,
			DurationSecs: durationSecs,
			UsedPercent:  window.UsedPercent,
		}
		if window.ResetAt > 0 {
			resetAt := time.Unix(window.ResetAt, 0).UTC()
			facts.ResetAt = &resetAt
		}
		if bucket == serverapi.AuthSubscriptionWindowBucketAdditional {
			facts.LimitName = textutil.OptionalTrimmedString(limitName)
			facts.MeteredFeature = textutil.OptionalTrimmedString(feature)
		}
		ordered = append(ordered, orderedWindow{facts: facts, discoveryRank: discoveryRank})
		discoveryRank++
		return nil
	}
	if payload.RateLimit != nil {
		if err := addWindow(payload.RateLimit.PrimaryWindow, serverapi.AuthSubscriptionWindowBucketDefault, "", ""); err != nil {
			return nil, err
		}
		if err := addWindow(payload.RateLimit.SecondaryWindow, serverapi.AuthSubscriptionWindowBucketDefault, "", ""); err != nil {
			return nil, err
		}
	}
	for _, extra := range payload.AdditionalRateLimits {
		if extra.RateLimit == nil {
			continue
		}
		if err := addWindow(extra.RateLimit.PrimaryWindow, serverapi.AuthSubscriptionWindowBucketAdditional, extra.LimitName, extra.MeteredFeature); err != nil {
			return nil, err
		}
		if err := addWindow(extra.RateLimit.SecondaryWindow, serverapi.AuthSubscriptionWindowBucketAdditional, extra.LimitName, extra.MeteredFeature); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].facts.DurationSecs != ordered[j].facts.DurationSecs {
			return ordered[i].facts.DurationSecs < ordered[j].facts.DurationSecs
		}
		return ordered[i].discoveryRank < ordered[j].discoveryRank
	})
	windows := make([]serverapi.AuthSubscriptionWindowFacts, 0, len(ordered))
	for _, window := range ordered {
		windows = append(windows, window.facts)
	}
	return windows, nil
}

func authStatusFailure(err error) serverapi.AuthStatusFailure {
	return serverapi.AuthStatusFailure{Cause: strings.TrimSpace(err.Error())}
}

var _ servicecontract.AuthStatusService = (*StatusService)(nil)
