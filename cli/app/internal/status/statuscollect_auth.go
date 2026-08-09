package status

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"

	"core/shared/serverapi"
)

func AuthStageFromResponse(response serverapi.AuthStatusResponse) AuthStageResult {
	if err := response.Validate(); err != nil {
		return UnavailableAuthStage(err)
	}
	resolution := response.Resolution
	if resolution.Kind == serverapi.AuthStatusResolutionUnavailable {
		return UnavailableAuthStage(fmt.Errorf("%s", resolution.Failure.Cause))
	}
	facts := *resolution.Facts
	result := AuthStageResult{
		Auth:         authInfoFromFacts(facts, resolution.Failure),
		Subscription: subscriptionInfoFromFacts(response.Subscription),
	}
	if resolution.Failure != nil {
		result.Warning = "auth: " + strings.TrimSpace(resolution.Failure.Cause)
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

func authInfoFromFacts(facts serverapi.AuthStatusFacts, failure *serverapi.AuthStatusFailure) AuthInfo {
	provider := authProviderStatusLabel(facts.Provider)
	details := make([]string, 0, 4)
	if facts.Provider.Kind == serverapi.AuthProviderKindOpenAICompatible {
		if origin := authProviderDisplayOrigin(facts.Provider.DisplayOrigin); origin != "" {
			details = append(details, origin)
		}
	}
	info := AuthInfo{
		Visible:  true,
		Method:   facts.Method,
		Provider: provider,
	}
	switch facts.Method {
	case serverapi.AuthStatusMethodOAuth:
		info.Summary = "Subscription"
		if facts.OAuth != nil && facts.OAuth.Email != nil {
			info.Summary = *facts.OAuth.Email
		}
	case serverapi.AuthStatusMethodAPIKey:
		info.Summary = "API Key"
		if facts.APIKey != nil && facts.APIKey.Suffix != nil {
			info.Summary += " ..." + *facts.APIKey.Suffix
		}
		if provider != "" {
			details = append(details, authProviderDetailLabel(facts.Provider))
		}
		if preference := authEnvPreferenceLabel(facts.EnvPreference); preference != "" {
			details = append(details, preference)
		}
	default:
		info.Summary = "No Auth"
	}
	if failure != nil {
		details = append(details, strings.TrimSpace(failure.Cause))
	}
	info.Details = details
	return info
}

func subscriptionInfoFromFacts(facts serverapi.AuthSubscriptionFacts) SubscriptionInfo {
	if !facts.Applicable {
		return SubscriptionInfo{}
	}
	if facts.Failure != nil {
		cause := strings.TrimSpace(facts.Failure.Cause)
		return SubscriptionInfo{
			Applicable: true,
			Summary:    "Subscription unavailable: " + cause,
			Error:      cause,
		}
	}
	return SubscriptionInfo{
		Applicable: true,
		Summary:    subscriptionPlanSummary(facts.Plan),
		Windows:    subscriptionWindowsFromFacts(facts.Windows),
	}
}

func subscriptionWindowsFromFacts(facts []serverapi.AuthSubscriptionWindowFacts) []SubscriptionWindow {
	if len(facts) == 0 {
		return nil
	}
	qualifierCounts := map[string]int{}
	windows := make([]SubscriptionWindow, 0, len(facts))
	for _, fact := range facts {
		window := SubscriptionWindow{
			Label:       subscriptionWindowDuration(fact.DurationSecs / 60),
			UsedPercent: fact.UsedPercent,
		}
		if fact.ResetAt != nil {
			window.ResetAt = *fact.ResetAt
		}
		if fact.Bucket == serverapi.AuthSubscriptionWindowBucketAdditional {
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
	window serverapi.AuthSubscriptionWindowFacts,
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
	case serverapi.AuthStatusMethodNone:
		return "No auth"
	case serverapi.AuthStatusMethodAPIKey:
		return authDisplayProviderLabel(info.Provider) + " API Key"
	case serverapi.AuthStatusMethodOAuth:
		return authDisplayProviderLabel(info.Provider) + " Subscription"
	default:
		return ""
	}
}

func authProviderStatusLabel(provider serverapi.AuthProviderFacts) string {
	switch provider.Kind {
	case serverapi.AuthProviderKindOpenAI:
		return "openai"
	case serverapi.AuthProviderKindOpenAICompatible:
		if origin := authProviderDisplayOrigin(provider.DisplayOrigin); origin != "" {
			return origin
		}
		return "openai-compatible"
	default:
		return strings.TrimSpace(provider.Identifier)
	}
}

func authProviderDetailLabel(provider serverapi.AuthProviderFacts) string {
	if provider.Kind == serverapi.AuthProviderKindOpenAI {
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

func authProviderDisplayOrigin(origin *serverapi.AuthProviderDisplayOrigin) string {
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

func authEnvPreferenceLabel(preference serverapi.AuthStatusEnvPreference) string {
	switch preference {
	case serverapi.AuthStatusEnvPreferencePreferSaved:
		return "saved auth preferred"
	case serverapi.AuthStatusEnvPreferencePreferEnv:
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
