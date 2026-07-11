package llm

import (
	"fmt"
	"strings"

	"core/server/auth"
	"core/shared/config"
)

func ProviderCapabilitiesForSettings(authState auth.State, settings config.Settings) (ProviderCapabilities, error) {
	return ResolveRuntimeProviderCapabilities(authState, settings)
}

func ResolveRuntimeProviderCapabilities(authState auth.State, settings config.Settings) (ProviderCapabilities, error) {
	if caps, ok := ProviderCapabilitiesFromOverride(settings.ProviderCapabilities); ok {
		return caps, nil
	}
	provider := Provider(strings.TrimSpace(settings.ProviderOverride))
	if provider == "" {
		if strings.TrimSpace(settings.OpenAIBaseURL) != "" {
			provider = ProviderOpenAI
		} else {
			inferredProvider, err := InferProviderFromModel(settings.Model)
			if err != nil {
				provider = ProviderOpenAI
			} else {
				provider = inferredProvider
			}
		}
	}
	mode := OpenAIAuthModeForAuthState(authState)
	variant, err := resolveRuntimeTransportVariant(provider, settings.OpenAIBaseURL, mode)
	if err != nil {
		return ProviderCapabilities{}, err
	}
	return variant.Capabilities, nil
}

func OpenAIAuthModeForAuthState(authState auth.State) OpenAIAuthMode {
	if authState.Method.Type != auth.MethodOAuth {
		return OpenAIAuthMode{}
	}
	accountID := ""
	if authState.Method.OAuth != nil {
		accountID = strings.TrimSpace(authState.Method.OAuth.AccountID)
	}
	return OpenAIAuthMode{IsOAuth: true, AccountID: accountID}
}

func resolveRuntimeTransportVariant(provider Provider, baseURL string, mode OpenAIAuthMode) (ProviderVariantContract, error) {
	if variant, err := resolveProviderTransportVariant(provider, baseURL, mode); err == nil {
		return variant, nil
	} else if provider == ProviderOpenAI {
		return ProviderVariantContract{}, err
	}
	providerID := strings.TrimSpace(string(provider))
	registration, ok := lookupProviderVariantContract(providerID)
	if !ok {
		return ProviderVariantContract{}, fmt.Errorf("%w: %s", ErrUnsupportedProvider, providerID)
	}
	if registration.Provider != provider {
		return ProviderVariantContract{}, fmt.Errorf("provider %q maps to provider_id %q owned by %q", provider, providerID, registration.Provider)
	}
	return registration.Variant, nil
}
