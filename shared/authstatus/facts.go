package authstatus

import (
	"net/url"
	"strings"

	"core/shared/config"
	"core/shared/serverapi"
)

func ProviderFacts(settings config.Settings) serverapi.AuthProviderFacts {
	if identifier := strings.TrimSpace(settings.ProviderOverride); identifier != "" {
		return serverapi.AuthProviderFacts{
			Kind:       serverapi.AuthProviderKindConfiguredProvider,
			Identifier: identifier,
		}
	}
	baseURL := strings.TrimSpace(settings.OpenAIBaseURL)
	if baseURL == "" || isOfficialChatGPTBaseURL(baseURL) {
		return serverapi.OpenAIAuthProviderFacts()
	}
	return serverapi.AuthProviderFacts{
		Kind:          serverapi.AuthProviderKindOpenAICompatible,
		Identifier:    "openai-compatible",
		DisplayOrigin: providerDisplayOrigin(baseURL),
	}
}

func SupportsSubscriptionUsage(settings config.Settings) bool {
	if strings.TrimSpace(settings.ProviderOverride) != "" {
		return false
	}
	baseURL := strings.TrimSpace(settings.OpenAIBaseURL)
	return baseURL == "" || isOfficialChatGPTBaseURL(baseURL)
}

func SupportsSubscriptionUsageForProvider(provider serverapi.AuthProviderFacts) bool {
	return provider.Kind == serverapi.AuthProviderKindOpenAI
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

func isOfficialChatGPTBaseURL(raw string) bool {
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
	hostname := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if hostname != "chatgpt.com" && hostname != "chat.openai.com" {
		return false
	}
	return parsed.Path == "" || parsed.Path == "/" || parsed.Path == "/backend-api"
}
