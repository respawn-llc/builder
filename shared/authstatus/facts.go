package authstatus

import (
	"net/url"
	"strconv"
	"strings"

	"core/shared/config"
	authpb "core/shared/protoapi/gen/kent/api/auth"
	"core/shared/textutil"
)

func ProviderFacts(providerID string, isOpenAIFirstParty bool, settings config.Settings) *authpb.ProviderFacts {
	providerID = strings.TrimSpace(providerID)
	if isOpenAIFirstParty {
		return &authpb.ProviderFacts{
			Kind:       authpb.ProviderKind_PROVIDER_KIND_OPENAI,
			Identifier: "openai",
		}
	}
	if providerID != "openai-compatible" {
		return &authpb.ProviderFacts{
			Kind:       authpb.ProviderKind_PROVIDER_KIND_CONFIGURED_PROVIDER,
			Identifier: providerID,
		}
	}
	return &authpb.ProviderFacts{
		Kind:          authpb.ProviderKind_PROVIDER_KIND_OPENAI_COMPATIBLE,
		Identifier:    "openai-compatible",
		DisplayOrigin: providerDisplayOrigin(settings.OpenAIBaseURL),
	}
}

func ProviderSelection(settings config.Settings) *authpb.ProviderSelection {
	selection := &authpb.ProviderSelection{
		Model:            textutil.OptionalTrimmedString(settings.Model),
		ProviderOverride: textutil.OptionalTrimmedString(settings.ProviderOverride),
		OpenaiBaseUrl:    textutil.OptionalTrimmedString(settings.OpenAIBaseURL),
	}
	if providerID := strings.TrimSpace(settings.ProviderCapabilities.ProviderID); providerID != "" {
		selection.ProviderCapabilities = &authpb.ProviderCapabilitySelection{
			ProviderId:         providerID,
			IsOpenaiFirstParty: settings.ProviderCapabilities.IsOpenAIFirstParty,
		}
	}
	return selection
}

func ProviderSettings(selection *authpb.ProviderSelection) config.Settings {
	settings := config.Settings{}
	if selection == nil {
		return settings
	}
	if selection.Model != nil {
		settings.Model = *selection.Model
	}
	if selection.ProviderOverride != nil {
		settings.ProviderOverride = *selection.ProviderOverride
	}
	if selection.OpenaiBaseUrl != nil {
		settings.OpenAIBaseURL = *selection.OpenaiBaseUrl
	}
	if selection.ProviderCapabilities != nil {
		settings.ProviderCapabilities.ProviderID = selection.ProviderCapabilities.ProviderId
		settings.ProviderCapabilities.IsOpenAIFirstParty = selection.ProviderCapabilities.IsOpenaiFirstParty
	}
	return settings
}

func SupportsSubscriptionUsage(settings config.Settings, isOpenAIFirstParty bool) bool {
	if !isOpenAIFirstParty {
		return false
	}
	baseURL := strings.TrimSpace(settings.OpenAIBaseURL)
	return baseURL == "" || isOfficialSubscriptionBaseURL(baseURL)
}

func providerDisplayOrigin(raw string) *authpb.ProviderDisplayOrigin {
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
	origin := &authpb.ProviderDisplayOrigin{Scheme: scheme, Hostname: hostname}
	if port := strings.TrimSpace(parsed.Port()); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return nil
		}
		origin.Port = &port
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
