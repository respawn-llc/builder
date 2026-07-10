package llm

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

var ErrUnsupportedProvider = errors.New("unsupported llm provider")

type Provider string

const (
	ProviderOpenAI    Provider = "openai"
	ProviderAnthropic Provider = "anthropic"
)

type ProviderClientOptions struct {
	Provider Provider
	Model    string

	Auth                         AuthHeaderProvider
	HTTPClient                   *http.Client
	OpenAIBaseURL                string
	ModelVerbosity               string
	Store                        bool
	ContextWindowTokens          int
	ProviderCapabilitiesOverride *ProviderCapabilities
}

type ProviderClientFactory func(opts ProviderClientOptions) (Client, error)

type ProviderErrorReducerFactory func(providerID string) ProviderErrorReducer

type ProviderModelMatcher func(model string) bool

type ProviderTransportVariantResolver func(baseURL string, mode openAIAuthMode) (string, error)

type ProviderVariantContract struct {
	ProviderID      string
	Capabilities    ProviderCapabilities
	NewErrorReducer ProviderErrorReducerFactory
}

type ProviderContract struct {
	Provider                Provider
	MatchModel              ProviderModelMatcher
	ResolveTransportVariant ProviderTransportVariantResolver
	NewClient               ProviderClientFactory
	ProviderVariants        []ProviderVariantContract
	ModelContracts          []ModelCapabilityContract
}

type providerVariantRegistration struct {
	Provider Provider
	Variant  ProviderVariantContract
}

type modelCapabilityRegistration struct {
	Provider Provider
	Contract ModelCapabilityContract
}

type providerRegistry struct {
	contractsByProvider  map[Provider]ProviderContract
	providerVariantsByID map[string]providerVariantRegistration
	modelContractsByName map[string]modelCapabilityRegistration
	modelContracts       []ModelCapabilityContract
	modelMatchers        []ProviderContract
}

var globalProviderRegistry = mustBuildProviderRegistry(providerContracts())

func providerContracts() []ProviderContract {
	return []ProviderContract{
		{
			Provider: ProviderAnthropic,
			MatchModel: func(model string) bool {
				return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "claude")
			},
			NewClient: newUnsupportedProviderClientFactory(ProviderAnthropic),
			ProviderVariants: []ProviderVariantContract{
				{
					ProviderID: "anthropic",
					Capabilities: ProviderCapabilities{
						ProviderID:                     "anthropic",
						SupportsResponsesAPI:           false,
						SupportsResponsesCompact:       false,
						SupportsRequestInputTokenCount: false,
						SupportsNativeWebSearch:        false,
						SupportsReasoningEncrypted:     false,
						SupportsServerSideContextEdit:  false,
						IsOpenAIFirstParty:             false,
					},
					NewErrorReducer: newOpaqueProviderErrorReducer,
				},
			},
		},
		{
			Provider:                ProviderOpenAI,
			MatchModel:              matchOpenAIModelFamily,
			ResolveTransportVariant: resolveOpenAITransportProviderVariant,
			NewClient:               newOpenAIProviderClient,
			ProviderVariants: []ProviderVariantContract{
				{
					ProviderID: "openai",
					Capabilities: ProviderCapabilities{
						ProviderID:                     "openai",
						SupportsResponsesAPI:           true,
						SupportsResponsesCompact:       true,
						SupportsRequestInputTokenCount: true,
						SupportsPromptCacheKey:         true,
						SupportsNativeWebSearch:        true,
						SupportsReasoningEncrypted:     true,
						SupportsServerSideContextEdit:  true,
						IsOpenAIFirstParty:             true,
					},
					NewErrorReducer: newOpenAICompatibleErrorReducer,
				},
				{
					ProviderID: "openai-compatible",
					Capabilities: ProviderCapabilities{
						ProviderID:                     "openai-compatible",
						SupportsResponsesAPI:           true,
						SupportsResponsesCompact:       false,
						SupportsRequestInputTokenCount: false,
						SupportsPromptCacheKey:         false,
						SupportsNativeWebSearch:        false,
						SupportsReasoningEncrypted:     false,
						SupportsServerSideContextEdit:  false,
						IsOpenAIFirstParty:             false,
					},
					NewErrorReducer: newOpenAICompatibleErrorReducer,
				},
				{
					ProviderID: "chatgpt-codex",
					Capabilities: ProviderCapabilities{
						ProviderID:                     "chatgpt-codex",
						SupportsResponsesAPI:           true,
						SupportsResponsesCompact:       true,
						SupportsRequestInputTokenCount: false,
						SupportsPromptCacheKey:         true,
						SupportsNativeWebSearch:        true,
						SupportsReasoningEncrypted:     true,
						SupportsServerSideContextEdit:  true,
						IsOpenAIFirstParty:             true,
					},
					NewErrorReducer: newOpenAICompatibleErrorReducer,
				},
			},
			ModelContracts: []ModelCapabilityContract{
				{Model: "gpt-5", SupportsReasoningEffort: true, SupportedReasoningEfforts: []string{"low", "medium", "high"}, SupportsReasoningSummary: true, SupportsVerbosity: true, SupportedVerbosityLevels: []string{"low", "medium", "high"}, SupportsVisionInputs: true},
				{Model: "gpt-5.6-sol", ContextWindowTokens: 372_000, LargeContextWindowTokens: 372_000, SupportsReasoningEffort: true, SupportedReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max", "ultra"}, SupportsReasoningSummary: true, SupportsVerbosity: true, SupportedVerbosityLevels: []string{"low", "medium", "high"}, SupportsVisionInputs: true},
				{Model: "gpt-5.6-terra", ContextWindowTokens: 372_000, LargeContextWindowTokens: 372_000, SupportsReasoningEffort: true, SupportedReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max", "ultra"}, SupportsReasoningSummary: true, SupportsVerbosity: true, SupportedVerbosityLevels: []string{"low", "medium", "high"}, SupportsVisionInputs: true},
				{Model: "gpt-5.6-luna", ContextWindowTokens: 372_000, LargeContextWindowTokens: 372_000, SupportsReasoningEffort: true, SupportedReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max"}, SupportsReasoningSummary: true, SupportsVerbosity: true, SupportedVerbosityLevels: []string{"low", "medium", "high"}, SupportsVisionInputs: true},
				{Model: "gpt-5.4", ContextWindowTokens: 272_000, LargeContextWindowTokens: 1_000_000, SupportsReasoningEffort: true, SupportedReasoningEfforts: []string{"low", "medium", "high", "xhigh"}, SupportsReasoningSummary: true, SupportsVerbosity: true, SupportedVerbosityLevels: []string{"low", "medium", "high"}, SupportsVisionInputs: true},
				{Model: "gpt-5.4-mini", ContextWindowTokens: 272_000, LargeContextWindowTokens: 400_000, SupportsReasoningEffort: true, SupportedReasoningEfforts: []string{"low", "medium", "high", "xhigh"}, SupportsReasoningSummary: true, SupportsVerbosity: true, SupportedVerbosityLevels: []string{"low", "medium", "high"}, SupportsVisionInputs: true},
				{Model: "gpt-5.4-nano", ContextWindowTokens: 272_000, LargeContextWindowTokens: 400_000, SupportsReasoningEffort: true, SupportedReasoningEfforts: []string{"low", "medium", "high", "xhigh"}, SupportsReasoningSummary: true, SupportsVerbosity: true, SupportedVerbosityLevels: []string{"low", "medium", "high"}, SupportsVisionInputs: false},
				{Model: "gpt-5.3-codex", ContextWindowTokens: 400_000, SupportsReasoningEffort: true, SupportedReasoningEfforts: []string{"low", "medium", "high"}, SupportsReasoningSummary: true, SupportsVerbosity: true, SupportedVerbosityLevels: []string{"low", "medium", "high"}, SupportsVisionInputs: true},
				{Model: "gpt-5.3-codex-spark", ContextWindowTokens: 128_000, SupportsReasoningEffort: true, SupportedReasoningEfforts: []string{"low", "medium", "high"}, SupportsVerbosity: true, SupportedVerbosityLevels: []string{"low", "medium", "high"}, SupportsVisionInputs: false},
				{Model: "gpt-4.1", SupportsReasoningEffort: true, SupportedReasoningEfforts: []string{"low", "medium", "high"}, SupportsReasoningSummary: true, SupportsVisionInputs: true},
			},
		},
	}
}

func mustBuildProviderRegistry(contracts []ProviderContract) providerRegistry {
	registry := providerRegistry{
		contractsByProvider:  make(map[Provider]ProviderContract, len(contracts)),
		providerVariantsByID: make(map[string]providerVariantRegistration),
		modelContractsByName: make(map[string]modelCapabilityRegistration),
		modelMatchers:        make([]ProviderContract, 0, len(contracts)),
	}

	for _, contract := range contracts {
		if contract.Provider == "" {
			panic("provider contract missing provider key")
		}
		if contract.MatchModel == nil {
			panic(fmt.Sprintf("provider %q missing model matcher", contract.Provider))
		}
		if contract.NewClient == nil {
			panic(fmt.Sprintf("provider %q missing client factory", contract.Provider))
		}
		if len(contract.ProviderVariants) == 0 {
			panic(fmt.Sprintf("provider %q missing provider variants", contract.Provider))
		}
		if _, exists := registry.contractsByProvider[contract.Provider]; exists {
			panic(fmt.Sprintf("duplicate provider contract for %q", contract.Provider))
		}
		registry.contractsByProvider[contract.Provider] = contract
		registry.modelMatchers = append(registry.modelMatchers, contract)

		for _, variant := range contract.ProviderVariants {
			normalizedID := strings.ToLower(strings.TrimSpace(variant.ProviderID))
			if normalizedID == "" {
				panic(fmt.Sprintf("provider %q has empty provider_id variant", contract.Provider))
			}
			if variant.NewErrorReducer == nil {
				panic(fmt.Sprintf("provider %q missing reducer factory for provider_id %q", contract.Provider, normalizedID))
			}
			if strings.TrimSpace(variant.Capabilities.ProviderID) == "" {
				variant.Capabilities.ProviderID = normalizedID
			}
			if strings.ToLower(strings.TrimSpace(variant.Capabilities.ProviderID)) != normalizedID {
				panic(fmt.Sprintf("provider %q capabilities provider_id %q does not match variant key %q", contract.Provider, variant.Capabilities.ProviderID, normalizedID))
			}
			if _, exists := registry.providerVariantsByID[normalizedID]; exists {
				panic(fmt.Sprintf("duplicate provider variant registration for provider_id %q", normalizedID))
			}
			registry.providerVariantsByID[normalizedID] = providerVariantRegistration{Provider: contract.Provider, Variant: variant}
		}

		for _, modelContract := range contract.ModelContracts {
			normalizedModel := strings.ToLower(strings.TrimSpace(modelContract.Model))
			if normalizedModel == "" {
				panic(fmt.Sprintf("provider %q has empty model contract key", contract.Provider))
			}
			if _, exists := registry.modelContractsByName[normalizedModel]; exists {
				panic(fmt.Sprintf("duplicate model contract registration for %q", normalizedModel))
			}
			registry.modelContractsByName[normalizedModel] = modelCapabilityRegistration{Provider: contract.Provider, Contract: modelContract}
			registry.modelContracts = append(registry.modelContracts, modelContract)
		}
	}

	return registry
}

func newUnsupportedProviderClientFactory(provider Provider) ProviderClientFactory {
	return func(_ ProviderClientOptions) (Client, error) {
		return nil, fmt.Errorf("%w: %s (not implemented)", ErrUnsupportedProvider, provider)
	}
}

func newOpenAIProviderClient(opts ProviderClientOptions) (Client, error) {
	if opts.Auth == nil && !allowsAnonymousOpenAIBaseURL(opts.OpenAIBaseURL) {
		return nil, fmt.Errorf("openai auth provider is required")
	}
	transport := NewHTTPTransport(opts.Auth)
	if opts.Provider != "" {
		transport.Provider = opts.Provider
	}
	if opts.HTTPClient != nil {
		transport.Client = opts.HTTPClient
	}
	if v := strings.TrimSpace(opts.OpenAIBaseURL); v != "" {
		normalizedBaseURL := normalizeOpenAIBaseURL(v)
		transport.BaseURL = normalizedBaseURL
		transport.BaseURLExplicit = !IsOpenAIFirstPartyBaseURL(normalizedBaseURL)
	}
	transport.ModelVerbosity = strings.ToLower(strings.TrimSpace(opts.ModelVerbosity))
	if opts.ContextWindowTokens > 0 {
		transport.ContextWindowTokens = opts.ContextWindowTokens
	}
	if opts.ProviderCapabilitiesOverride != nil {
		caps := *opts.ProviderCapabilitiesOverride
		transport.ProviderCapabilitiesOverride = &caps
	}
	transport.Store = opts.Store
	return newIdleWatchdogClient(NewOpenAIClient(transport), transport.Client.Timeout), nil
}

func allowsAnonymousOpenAIBaseURL(baseURL string) bool {
	trimmed := strings.TrimSpace(baseURL)
	return trimmed != "" && !IsOpenAIFirstPartyBaseURL(trimmed)
}

func NewProviderClient(opts ProviderClientOptions) (Client, error) {
	provider := opts.Provider
	if provider == "" {
		if strings.TrimSpace(opts.OpenAIBaseURL) != "" {
			provider = ProviderOpenAI
		} else {
			inferredProvider, err := InferProviderFromModel(opts.Model)
			if err != nil {
				return nil, &ProviderSelectionError{Model: strings.TrimSpace(opts.Model), Err: err}
			}
			provider = inferredProvider
		}
	}
	opts.Provider = provider
	if opts.ContextWindowTokens <= 0 {
		if meta, ok := LookupModelMetadata(opts.Model); ok && meta.ContextWindowTokens > 0 {
			opts.ContextWindowTokens = meta.ContextWindowTokens
		}
	}
	contract, ok := globalProviderRegistry.contractsByProvider[provider]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedProvider, provider)
	}
	return contract.NewClient(opts)
}

func InferProviderFromModel(model string) (Provider, error) {
	normalizedModel := strings.TrimSpace(model)
	if normalizedModel == "" {
		return "", fmt.Errorf("%w: model is required to infer provider", ErrUnsupportedProvider)
	}
	for _, contract := range globalProviderRegistry.modelMatchers {
		if contract.MatchModel(normalizedModel) {
			return contract.Provider, nil
		}
	}
	return "", fmt.Errorf("%w: no provider contract matches model %q", ErrUnsupportedProvider, normalizedModel)
}

func matchOpenAIModelFamily(model string) bool {
	normalizedModel := strings.ToLower(strings.TrimSpace(model))
	if normalizedModel == "" {
		return false
	}
	if strings.HasPrefix(normalizedModel, "gpt-") {
		return true
	}
	return false
}
