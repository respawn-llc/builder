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
	"core/shared/authstatus"
	"core/shared/config"
	authpb "core/shared/protoapi/gen/kent/api/auth"
	"core/shared/textutil"

	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const usageBaseURL = "https://chatgpt.com/backend-api"

type StatusService struct {
	manager  *auth.Manager
	settings config.Settings
}

func NewStatusService(manager *auth.Manager, settings config.Settings) *StatusService {
	return &StatusService{manager: manager, settings: settings}
}

func (s *StatusService) GetStatus(ctx context.Context, req *authpb.GetStatusRequest) (*authpb.Status, error) {
	if req == nil {
		return nil, errors.New("auth status request is required")
	}
	state := auth.EmptyState()
	if s != nil && s.manager != nil {
		loaded, err := s.manager.Load(ctx)
		if err != nil {
			return &authpb.Status{
				Resolution: &authpb.StatusResolution{
					Resolution: &authpb.StatusResolution_Unavailable{Unavailable: authStatusFailure(err)},
				},
				Subscription: &authpb.SubscriptionFacts{},
			}, nil
		}
		state = loaded
	}
	provider, subscriptionUsageSupported, err := s.resolveProvider(state, req.Provider)
	if err != nil {
		return nil, err
	}
	subscriptionUsageSupported = subscriptionUsageSupported && !req.SkipSubscriptionUsage
	return &authpb.Status{
		Resolution: &authpb.StatusResolution{
			Resolution: &authpb.StatusResolution_Known{Known: authFacts(state, provider)},
		},
		Subscription: subscriptionStatus(ctx, state, nil, subscriptionUsageSupported),
	}, nil
}

func (s *StatusService) resolveProvider(
	state auth.State,
	requested *authpb.ProviderSelection,
) (*authpb.ProviderFacts, bool, error) {
	settings := config.Settings{}
	if s != nil {
		settings = s.settings
	}
	requestedSelection := requested != nil
	if requested != nil {
		settings = authstatus.ProviderSettings(requested)
	}
	capabilities, err := llm.ResolveRuntimeProviderCapabilities(state, settings)
	if err != nil {
		return nil, false, fmt.Errorf("resolve auth status provider: %w", err)
	}
	provider := authstatus.ProviderFacts(capabilities.ProviderID, capabilities.IsOpenAIFirstParty, settings)
	if requestedSelection {
		return provider, capabilities.IsOpenAIFirstParty, nil
	}
	return provider, authstatus.SupportsSubscriptionUsage(settings, capabilities.IsOpenAIFirstParty), nil
}

func authFacts(state auth.State, provider *authpb.ProviderFacts) *authpb.StatusFacts {
	facts := &authpb.StatusFacts{
		Method:        authStatusMethod(state.Method.Type),
		Provider:      provider,
		EnvPreference: authStatusEnvPreference(state.EnvAPIKeyPreference),
	}
	switch state.Method.Type {
	case auth.MethodOAuth:
		oauthFacts := &authpb.OAuthFacts{}
		if state.Method.OAuth != nil {
			oauthFacts.AccountId = textutil.OptionalTrimmedString(state.Method.OAuth.AccountID)
			oauthFacts.Email = textutil.OptionalTrimmedString(state.Method.OAuth.Email)
		}
		facts.MethodFacts = &authpb.StatusFacts_Oauth{Oauth: oauthFacts}
	case auth.MethodAPIKey:
		facts.MethodFacts = &authpb.StatusFacts_ApiKey{ApiKey: apiKeyFacts(state.Method.APIKey)}
	default:
		facts.MethodFacts = &authpb.StatusFacts_NoAuth{NoAuth: &emptypb.Empty{}}
	}
	return facts
}

func authStatusMethod(method auth.MethodType) authpb.AuthMethod {
	switch method {
	case auth.MethodOAuth:
		return authpb.AuthMethod_AUTH_METHOD_OAUTH
	case auth.MethodAPIKey:
		return authpb.AuthMethod_AUTH_METHOD_API_KEY
	default:
		return authpb.AuthMethod_AUTH_METHOD_NONE
	}
}

func authStatusEnvPreference(preference auth.EnvAPIKeyPreference) authpb.EnvironmentPreference {
	switch preference {
	case auth.EnvAPIKeyPreferencePreferSaved:
		return authpb.EnvironmentPreference_ENVIRONMENT_PREFERENCE_PREFER_SAVED_AUTH
	case auth.EnvAPIKeyPreferencePreferEnv:
		return authpb.EnvironmentPreference_ENVIRONMENT_PREFERENCE_PREFER_ENV_API_KEY
	default:
		return authpb.EnvironmentPreference_ENVIRONMENT_PREFERENCE_UNSPECIFIED
	}
}

func apiKeyFacts(method *auth.APIKeyMethod) *authpb.APIKeyFacts {
	facts := &authpb.APIKeyFacts{}
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
) *authpb.SubscriptionFacts {
	if !shouldFetchSubscriptionUsage(state, subscriptionUsageSupported) {
		return &authpb.SubscriptionFacts{}
	}
	if authStateErr != nil {
		return &authpb.SubscriptionFacts{Applicable: true, Failure: authStatusFailure(authStateErr)}
	}
	payload, err := fetchUsagePayload(ctx, usageBaseURL, state)
	if err != nil {
		return &authpb.SubscriptionFacts{Applicable: true, Failure: authStatusFailure(err)}
	}
	windows, err := usageWindowFacts(payload)
	if err != nil {
		return &authpb.SubscriptionFacts{Applicable: true, Failure: authStatusFailure(err)}
	}
	return &authpb.SubscriptionFacts{
		Applicable: true,
		Plan:       textutil.OptionalTrimmedString(payload.PlanType),
		Windows:    windows,
	}
}

func shouldFetchSubscriptionUsage(state auth.State, subscriptionUsageSupported bool) bool {
	return state.Method.Type == auth.MethodOAuth &&
		state.Method.OAuth != nil &&
		subscriptionUsageSupported
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

func usageWindowFacts(payload usagePayload) ([]*authpb.SubscriptionWindowFacts, error) {
	type orderedWindow struct {
		facts         *authpb.SubscriptionWindowFacts
		discoveryRank int
	}
	ordered := make([]orderedWindow, 0, 2+len(payload.AdditionalRateLimits)*2)
	discoveryRank := 0
	addWindow := func(window *usageWindow, bucket authpb.SubscriptionWindowBucket, limitName, feature string) error {
		if window == nil {
			return nil
		}
		if window.LimitWindowSeconds <= 0 {
			return fmt.Errorf("usage window duration must be positive: %d", window.LimitWindowSeconds)
		}
		facts := &authpb.SubscriptionWindowFacts{
			Bucket:          bucket,
			DurationSeconds: uint32(window.LimitWindowSeconds),
			UsedPercent:     window.UsedPercent,
		}
		if window.ResetAt > 0 {
			facts.ResetAt = timestamppb.New(time.Unix(window.ResetAt, 0).UTC())
		}
		if bucket == authpb.SubscriptionWindowBucket_SUBSCRIPTION_WINDOW_BUCKET_ADDITIONAL {
			facts.LimitName = textutil.OptionalTrimmedString(limitName)
			facts.MeteredFeature = textutil.OptionalTrimmedString(feature)
		}
		ordered = append(ordered, orderedWindow{facts: facts, discoveryRank: discoveryRank})
		discoveryRank++
		return nil
	}
	if payload.RateLimit != nil {
		if err := addWindow(payload.RateLimit.PrimaryWindow, authpb.SubscriptionWindowBucket_SUBSCRIPTION_WINDOW_BUCKET_DEFAULT, "", ""); err != nil {
			return nil, err
		}
		if err := addWindow(payload.RateLimit.SecondaryWindow, authpb.SubscriptionWindowBucket_SUBSCRIPTION_WINDOW_BUCKET_DEFAULT, "", ""); err != nil {
			return nil, err
		}
	}
	for _, extra := range payload.AdditionalRateLimits {
		if extra.RateLimit == nil {
			continue
		}
		if err := addWindow(extra.RateLimit.PrimaryWindow, authpb.SubscriptionWindowBucket_SUBSCRIPTION_WINDOW_BUCKET_ADDITIONAL, extra.LimitName, extra.MeteredFeature); err != nil {
			return nil, err
		}
		if err := addWindow(extra.RateLimit.SecondaryWindow, authpb.SubscriptionWindowBucket_SUBSCRIPTION_WINDOW_BUCKET_ADDITIONAL, extra.LimitName, extra.MeteredFeature); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].facts.DurationSeconds != ordered[j].facts.DurationSeconds {
			return ordered[i].facts.DurationSeconds < ordered[j].facts.DurationSeconds
		}
		return ordered[i].discoveryRank < ordered[j].discoveryRank
	})
	windows := make([]*authpb.SubscriptionWindowFacts, 0, len(ordered))
	for _, window := range ordered {
		windows = append(windows, window.facts)
	}
	return windows, nil
}

func authStatusFailure(err error) *authpb.StatusFailure {
	return &authpb.StatusFailure{Cause: strings.TrimSpace(err.Error())}
}
