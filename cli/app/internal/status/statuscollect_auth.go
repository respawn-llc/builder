package status

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"

	authpb "core/shared/protoapi/gen/kent/api/auth"
)

func AuthStageFromResponse(response *authpb.Status) AuthStageResult {
	resolution := response.GetResolution()
	if unavailable := resolution.GetUnavailable(); unavailable != nil {
		return UnavailableAuthStage(fmt.Errorf("%s", unavailable.GetCause()))
	}
	facts := resolution.GetKnown()
	result := AuthStageResult{
		Auth:         authInfoFromFacts(facts, resolution.GetPartialFailure()),
		Subscription: subscriptionInfoFromFacts(response.GetSubscription()),
	}
	if resolution.GetPartialFailure() != nil {
		result.Warning = "auth: " + strings.TrimSpace(resolution.GetPartialFailure().GetCause())
	}
	return result
}

func UnavailableAuthStage(err error) AuthStageResult {
	cause := strings.TrimSpace(err.Error())
	result := AuthStageResult{
		Auth: AuthInfo{
			Summary:     "Auth unavailable",
			Details:     []string{cause},
			Visible:     true,
			Unavailable: true,
		},
		Warning: "auth: " + cause,
	}
	return result
}

func authInfoFromFacts(facts *authpb.StatusFacts, failure *authpb.StatusFailure) AuthInfo {
	provider := authProviderStatusLabel(facts.GetProvider())
	details := make([]string, 0, 4)
	if facts.GetProvider().GetKind() == authpb.ProviderKind_PROVIDER_KIND_OPENAI_COMPATIBLE {
		if origin := authProviderDisplayOrigin(facts.GetProvider().GetDisplayOrigin()); origin != "" {
			details = append(details, origin)
		}
	}
	info := AuthInfo{
		Visible:  true,
		Method:   facts.GetMethod(),
		Provider: provider,
	}
	switch facts.GetMethod() {
	case authpb.AuthMethod_AUTH_METHOD_OAUTH:
		info.Summary = "Subscription"
		if facts.GetOauth() != nil && facts.GetOauth().Email != nil {
			info.Summary = facts.GetOauth().GetEmail()
		}
	case authpb.AuthMethod_AUTH_METHOD_API_KEY:
		info.Summary = "API Key"
		if facts.GetApiKey() != nil && facts.GetApiKey().Suffix != nil {
			info.Summary += " ..." + facts.GetApiKey().GetSuffix()
		}
		if provider != "" {
			details = append(details, authProviderDetailLabel(facts.GetProvider()))
		}
		if preference := authEnvPreferenceLabel(facts.GetEnvPreference()); preference != "" {
			details = append(details, preference)
		}
	default:
		info.Summary = "No Auth"
	}
	if failure != nil {
		details = append(details, strings.TrimSpace(failure.GetCause()))
	}
	info.Details = details
	return info
}

func subscriptionInfoFromFacts(facts *authpb.SubscriptionFacts) SubscriptionInfo {
	if !facts.GetApplicable() {
		return SubscriptionInfo{}
	}
	if facts.GetFailure() != nil {
		cause := strings.TrimSpace(facts.GetFailure().GetCause())
		return SubscriptionInfo{
			Applicable: true,
			Summary:    "Subscription unavailable: " + cause,
			Error:      cause,
		}
	}
	return SubscriptionInfo{
		Applicable: true,
		Summary:    subscriptionPlanSummary(facts.Plan),
		Windows:    subscriptionWindowsFromFacts(facts.GetWindows()),
	}
}

func subscriptionWindowsFromFacts(facts []*authpb.SubscriptionWindowFacts) []SubscriptionWindow {
	if len(facts) == 0 {
		return nil
	}
	qualifierCounts := map[string]int{}
	windows := make([]SubscriptionWindow, 0, len(facts))
	for _, fact := range facts {
		window := SubscriptionWindow{
			Label:       subscriptionWindowDuration(int(fact.GetDurationSeconds() / 60)),
			UsedPercent: fact.GetUsedPercent(),
		}
		if fact.GetResetAt() != nil {
			window.ResetAt = fact.GetResetAt().AsTime()
		}
		if fact.GetBucket() == authpb.SubscriptionWindowBucket_SUBSCRIPTION_WINDOW_BUCKET_ADDITIONAL {
			window.Qualifier = subscriptionWindowQualifier(fact, qualifierCounts)
		}
		windows = append(windows, window)
	}
	return windows
}

func subscriptionPlanSummary(plan *string) string {
	if plan == nil {
		return "Subscription"
	}
	normalized := strings.ToLower(strings.TrimSpace(*plan))
	if normalized == "" {
		return "Subscription"
	}
	return strings.ToUpper(normalized[:1]) + normalized[1:] + " subscription"
}

func subscriptionWindowQualifier(
	window *authpb.SubscriptionWindowFacts,
	counts map[string]int,
) string {
	limitName := optionalAuthFactValue(window.LimitName)
	feature := optionalAuthFactValue(window.MeteredFeature)
	base := ""
	switch {
	case limitName == "" && feature == "":
		base = "extra"
	case limitName == "":
		base = feature
	case feature == "" || strings.EqualFold(limitName, feature):
		base = limitName
	default:
		base = limitName + " / " + feature
	}
	counts[base]++
	if counts[base] == 1 {
		return base
	}
	return fmt.Sprintf("%s #%d", base, counts[base])
}

func subscriptionWindowDuration(windowMinutes int) string {
	const minutesPerHour = 60
	const minutesPerDay = 24 * minutesPerHour
	const minutesPerWeek = 7 * minutesPerDay
	const minutesPerMonth = 30 * minutesPerDay
	const minutesPerYear = 365 * minutesPerDay
	const roundingBiasMinutes = 3

	if windowMinutes < 0 {
		windowMinutes = 0
	}
	if windowMinutes <= minutesPerDay+roundingBiasMinutes {
		hours := (windowMinutes + roundingBiasMinutes) / minutesPerHour
		if hours < 1 {
			hours = 1
		}
		return fmt.Sprintf("%dh", hours)
	}
	if windowMinutes <= minutesPerWeek+roundingBiasMinutes {
		return "weekly"
	}
	if windowMinutes <= minutesPerMonth+roundingBiasMinutes {
		return "monthly"
	}
	if windowMinutes < minutesPerYear-roundingBiasMinutes {
		days := (windowMinutes + minutesPerDay/2) / minutesPerDay
		if days < 31 {
			days = 31
		}
		return fmt.Sprintf("%dd", days)
	}
	return "annual"
}

func AuthDisplayLabel(info AuthInfo) string {
	if !info.Visible {
		return ""
	}
	if info.Unavailable {
		return "Auth unavailable"
	}
	switch info.Method {
	case authpb.AuthMethod_AUTH_METHOD_NONE:
		return "No auth"
	case authpb.AuthMethod_AUTH_METHOD_API_KEY:
		return authDisplayProviderLabel(info.Provider) + " API Key"
	case authpb.AuthMethod_AUTH_METHOD_OAUTH:
		return authDisplayProviderLabel(info.Provider) + " Subscription"
	default:
		return ""
	}
}

func authProviderStatusLabel(provider *authpb.ProviderFacts) string {
	switch provider.GetKind() {
	case authpb.ProviderKind_PROVIDER_KIND_OPENAI:
		return "openai"
	case authpb.ProviderKind_PROVIDER_KIND_OPENAI_COMPATIBLE:
		if origin := authProviderDisplayOrigin(provider.GetDisplayOrigin()); origin != "" {
			return origin
		}
		return "openai-compatible"
	default:
		return strings.TrimSpace(provider.GetIdentifier())
	}
}

func authProviderDetailLabel(provider *authpb.ProviderFacts) string {
	if provider.GetKind() == authpb.ProviderKind_PROVIDER_KIND_OPENAI {
		return "OpenAI"
	}
	return authProviderStatusLabel(provider)
}

func authDisplayProviderLabel(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "", "openai":
		return "OpenAI"
	case "openai-compatible":
		return "OpenAI-compatible"
	default:
		return strings.TrimSpace(provider)
	}
}

func authProviderDisplayOrigin(origin *authpb.ProviderDisplayOrigin) string {
	if origin == nil {
		return ""
	}
	host := origin.Hostname
	if origin.Port != nil {
		host = net.JoinHostPort(host, *origin.Port)
	} else if address, err := netip.ParseAddr(host); err == nil && address.Is6() {
		host = "[" + host + "]"
	}
	return (&url.URL{Scheme: origin.Scheme, Host: host}).String()
}

func authEnvPreferenceLabel(preference authpb.EnvironmentPreference) string {
	switch preference {
	case authpb.EnvironmentPreference_ENVIRONMENT_PREFERENCE_PREFER_SAVED_AUTH:
		return "saved auth preferred"
	case authpb.EnvironmentPreference_ENVIRONMENT_PREFERENCE_PREFER_ENV_API_KEY:
		return "OPENAI_API_KEY preferred"
	default:
		return ""
	}
}

func optionalAuthFactValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
