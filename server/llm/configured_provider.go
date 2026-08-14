package llm

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"core/server/auth"
	"core/server/session"
	"core/shared/config"
)

type EffectiveAuthStateReader interface {
	Load(context.Context) (auth.State, error)
}

type EffectiveProviderResolution struct {
	AuthState    auth.State
	Capabilities ProviderCapabilities
}

// ResolveEffectiveProviderCapabilities is the sole authority for choosing
// locked, explicitly configured, or effective-auth-derived provider
// capabilities. A missing reader represents an effective no-auth state.
func ResolveEffectiveProviderCapabilities(
	ctx context.Context,
	locked *session.LockedContract,
	settings config.Settings,
	authStates EffectiveAuthStateReader,
) (EffectiveProviderResolution, error) {
	if capabilities, configured := ProviderCapabilitiesFromLockedOrOverride(
		locked,
		settings.ProviderCapabilities,
	); configured {
		return EffectiveProviderResolution{
			AuthState:    auth.EmptyState(),
			Capabilities: capabilities,
		}, nil
	}
	authState := auth.EmptyState()
	if authStates != nil {
		var err error
		authState, err = authStates.Load(ctx)
		if err != nil {
			return EffectiveProviderResolution{}, err
		}
	}
	capabilities, err := ProviderCapabilitiesForSettings(authState, settings)
	if err != nil {
		return EffectiveProviderResolution{}, err
	}
	return EffectiveProviderResolution{
		AuthState:    authState,
		Capabilities: capabilities,
	}, nil
}

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
	endpoint, err := newProviderTransportEndpoint(settings.OpenAIBaseURL, strings.TrimSpace(settings.OpenAIBaseURL) != "")
	if err != nil {
		return ProviderCapabilities{}, err
	}
	variant, err := resolveRuntimeTransportVariant(provider, endpoint, mode)
	if err != nil {
		return ProviderCapabilities{}, err
	}
	return variant.Capabilities, nil
}

func newProviderTransportEndpoint(rawURL string, explicit bool) (ProviderTransportEndpoint, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		if explicit {
			return ProviderTransportEndpoint{}, fmt.Errorf("explicit provider endpoint URL is empty")
		}
		return ProviderTransportEndpoint{Explicit: false}, nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return ProviderTransportEndpoint{}, fmt.Errorf("parse provider endpoint URL: %w", err)
	}
	return ProviderTransportEndpoint{URL: parsed, Explicit: explicit}, nil
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

func resolveRuntimeTransportVariant(provider Provider, endpoint ProviderTransportEndpoint, mode OpenAIAuthMode) (ProviderVariantContract, error) {
	if variant, err := resolveProviderTransportVariant(provider, endpoint, mode); err == nil {
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
