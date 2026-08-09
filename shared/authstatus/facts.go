package authstatus

import (
	"net/url"
	"strings"

	"core/shared/config"
	"core/shared/serverapi"
)

func ProviderFacts(providerID string, isOpenAIFirstParty bool, settings config.Settings) serverapi.AuthProviderFacts {
	providerID = strings.TrimSpace(providerID)
	if isOpenAIFirstParty {
		return serverapi.OpenAIAuthProviderFacts()
	}
	if providerID != "openai-compatible" {
		return serverapi.AuthProviderFacts{
			Kind:       serverapi.AuthProviderKindConfiguredProvider,
			Identifier: providerID,
		}
	}
	return serverapi.AuthProviderFacts{
		Kind:          serverapi.AuthProviderKindOpenAICompatible,
		Identifier:    "openai-compatible",
		DisplayOrigin: providerDisplayOrigin(settings.OpenAIBaseURL),
	}
}

func ProviderSelection(settings config.Settings) serverapi.AuthProviderSelection {
	selection := serverapi.AuthProviderSelection{
		Model:            optionalTrimmedString(settings.Model),
		ProviderOverride: optionalTrimmedString(settings.ProviderOverride),
		OpenAIBaseURL:    optionalTrimmedString(settings.OpenAIBaseURL),
	}
	if providerID := strings.TrimSpace(settings.ProviderCapabilities.ProviderID); providerID != "" {
		selection.ProviderCapabilities = &serverapi.AuthProviderCapabilitySelection{
			ProviderID:         providerID,
			IsOpenAIFirstParty: settings.ProviderCapabilities.IsOpenAIFirstParty,
		}
	}
	return selection
}

func ProviderSettings(selection serverapi.AuthProviderSelection) config.Settings {
	settings := config.Settings{}
	if selection.Model != nil {
		settings.Model = *selection.Model
	}
	if selection.ProviderOverride != nil {
		settings.ProviderOverride = *selection.ProviderOverride
	}
	if selection.OpenAIBaseURL != nil {
		settings.OpenAIBaseURL = *selection.OpenAIBaseURL
	}
	if selection.ProviderCapabilities != nil {
		settings.ProviderCapabilities.ProviderID = selection.ProviderCapabilities.ProviderID
		settings.ProviderCapabilities.IsOpenAIFirstParty = selection.ProviderCapabilities.IsOpenAIFirstParty
	}
	return settings
}

func SupportsSubscriptionUsageForProvider(provider serverapi.AuthProviderFacts) bool {
	return provider.Kind == serverapi.AuthProviderKindOpenAI
}

func SupportsSubscriptionUsage(settings config.Settings, isOpenAIFirstParty bool) bool {
	if !isOpenAIFirstParty {
		return false
	}
	baseURL := strings.TrimSpace(settings.OpenAIBaseURL)
	return baseURL == "" || isOfficialSubscriptionBaseURL(baseURL)
}

func providerDisplayOrigin(raw string) *serverapi.AuthProviderDisplayOrigin {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !parsed.IsAbs() || parsed.Opaque != "" {
		return nil
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if scheme != "http" && scheme != "https" {
		return nil
	}
	hostname := strings.TrimSpace(parsed.Hostname())
	if hostname == "" {
		return nil
	}
	origin := &serverapi.AuthProviderDisplayOrigin{Scheme: scheme, Hostname: hostname}
	if port := strings.TrimSpace(parsed.Port()); port != "" {
		origin.Port = &port
	}
	if err := origin.Validate(); err != nil {
		return nil
	}
	return origin
}

func isOfficialSubscriptionBaseURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil ||
		!parsed.IsAbs() ||
		parsed.Opaque != "" ||
		parsed.Scheme != "https" ||
		parsed.User != nil ||
		parsed.Port() != "" ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(parsed.Hostname())) {
	case "chatgpt.com", "chat.openai.com":
		return parsed.Path == "" || parsed.Path == "/" || parsed.Path == "/backend-api"
	case "api.openai.com":
		return parsed.Path == "" || parsed.Path == "/" || parsed.Path == "/v1"
	default:
		return false
	}
}

func optionalTrimmedString(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}
