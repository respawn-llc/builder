package llm

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"core/server/session"
	"core/shared/config"
)

// capability_registry.go is the single source of truth for built-in provider
// contracts. Each provider contract owns its client wiring, transport variants,
// provider capability flags, and model metadata.

type ModelCapabilityContract struct {
	Model                     string
	ContextWindowTokens       int
	LargeContextWindowTokens  int
	SupportsReasoningEffort   bool
	SupportedReasoningEfforts []string
	SupportsReasoningSummary  bool
	SupportsVerbosity         bool
	SupportedVerbosityLevels  []string
	SupportsVisionInputs      bool
}

func lookupProviderVariantContract(providerID string) (providerVariantRegistration, bool) {
	key := strings.ToLower(strings.TrimSpace(providerID))
	if key == "" {
		return providerVariantRegistration{}, false
	}
	registration, ok := globalProviderRegistry.providerVariantsByID[key]
	return registration, ok
}

func LookupModelCapabilityContract(model string) (ModelCapabilityContract, bool) {
	key := strings.ToLower(strings.TrimSpace(model))
	if key == "" {
		return ModelCapabilityContract{}, false
	}
	registration, ok := globalProviderRegistry.modelContractsByName[key]
	if !ok {
		return ModelCapabilityContract{}, false
	}
	return registration.Contract, true
}

func KnownModelCapabilityContracts() []ModelCapabilityContract {
	return append([]ModelCapabilityContract(nil), globalProviderRegistry.modelContracts...)
}

func LookupProviderCapabilityContract(providerID string) (ProviderCapabilities, bool) {
	registration, ok := lookupProviderVariantContract(providerID)
	if !ok {
		return ProviderCapabilities{}, false
	}
	return registration.Variant.Capabilities, true
}

func resolveProviderTransportVariant(provider Provider, baseURL string, mode OpenAIAuthMode) (ProviderVariantContract, error) {
	contract, ok := globalProviderRegistry.contractsByProvider[provider]
	if !ok {
		return ProviderVariantContract{}, fmt.Errorf("%w: %s", ErrUnsupportedProvider, provider)
	}
	if contract.ResolveTransportVariant == nil {
		return ProviderVariantContract{}, fmt.Errorf("%w: transport provider resolution is not implemented for %s", ErrUnsupportedProvider, provider)
	}
	providerID, err := contract.ResolveTransportVariant(baseURL, mode)
	if err != nil {
		return ProviderVariantContract{}, err
	}
	registration, ok := lookupProviderVariantContract(providerID)
	if !ok {
		return ProviderVariantContract{}, fmt.Errorf("provider %q resolved unknown provider_id %q", provider, strings.TrimSpace(providerID))
	}
	if registration.Provider != provider {
		return ProviderVariantContract{}, fmt.Errorf("provider %q resolved provider_id %q owned by %q", provider, strings.TrimSpace(providerID), registration.Provider)
	}
	return registration.Variant, nil
}

func resolveOpenAITransportProviderVariant(baseURL string, mode OpenAIAuthMode) (string, error) {
	if mode.IsOAuth {
		return "chatgpt-codex", nil
	}
	normalizedBaseURL := normalizeOpenAIBaseURL(baseURL)
	if normalizedBaseURL == normalizeOpenAIBaseURL(defaultOpenAIBaseURL) || IsOpenAIFirstPartyBaseURL(normalizedBaseURL) || isLoopbackOpenAIBaseURL(normalizedBaseURL) {
		return "openai", nil
	}
	if strings.TrimSpace(baseURL) != "" {
		return "openai-compatible", nil
	}
	return "", fmt.Errorf("%w: openai base URL %q does not map to a registered provider contract", ErrUnsupportedProvider, strings.TrimSpace(baseURL))
}

func normalizeOpenAIBaseURL(baseURL string) string {
	trimmed := strings.TrimSpace(baseURL)
	trimmed = strings.TrimSuffix(trimmed, "/")
	if trimmed == "" {
		return strings.TrimSuffix(defaultOpenAIBaseURL, "/")
	}
	if IsOpenAIFirstPartyBaseURL(trimmed) {
		return strings.TrimSuffix(defaultOpenAIBaseURL, "/")
	}
	return trimmed
}

func IsOpenAIFirstPartyBaseURL(baseURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(parsed.Hostname()), "api.openai.com")
}

func isLoopbackOpenAIBaseURL(baseURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return false
	}
	hostname := strings.TrimSpace(parsed.Hostname())
	if hostname == "" {
		return false
	}
	if strings.EqualFold(hostname, "localhost") {
		return true
	}
	parsedIP := net.ParseIP(hostname)
	return parsedIP != nil && parsedIP.IsLoopback()
}

func LockedModelCapabilitiesForModel(model string) session.LockedModelCapabilities {
	contract, ok := LookupModelCapabilityContract(model)
	if !ok {
		return session.LockedModelCapabilities{}
	}
	return session.LockedModelCapabilities{
		SupportsReasoningEffort: contract.SupportsReasoningEffort,
		SupportsVisionInputs:    contract.SupportsVisionInputs,
	}
}

func LockedModelCapabilitiesForConfig(model string, override config.ModelCapabilitiesOverride) session.LockedModelCapabilities {
	if override.SupportsReasoningEffort || override.SupportsVisionInputs {
		return session.LockedModelCapabilities{
			SupportsReasoningEffort: override.SupportsReasoningEffort,
			SupportsVisionInputs:    override.SupportsVisionInputs,
		}
	}
	return LockedModelCapabilitiesForModel(model)
}

func LockedProviderCapabilitiesFromContract(contract ProviderCapabilities) session.LockedProviderCapabilities {
	return session.LockedProviderCapabilities{
		ProviderID:                        strings.TrimSpace(contract.ProviderID),
		SupportsResponsesAPI:              contract.SupportsResponsesAPI,
		SupportsResponsesCompact:          contract.SupportsResponsesCompact,
		SupportsRequestInputTokenCount:    contract.SupportsRequestInputTokenCount,
		HasSupportsRequestInputTokenCount: true,
		SupportsPromptCacheKey:            contract.SupportsPromptCacheKey,
		HasSupportsPromptCacheKey:         true,
		SupportsNativeWebSearch:           contract.SupportsNativeWebSearch,
		SupportsReasoningEncrypted:        contract.SupportsReasoningEncrypted,
		SupportsServerSideContextEdit:     contract.SupportsServerSideContextEdit,
		IsOpenAIFirstParty:                contract.IsOpenAIFirstParty,
	}
}

func ProviderCapabilitiesFromOverride(override config.ProviderCapabilitiesOverride) (ProviderCapabilities, bool) {
	providerID := strings.TrimSpace(override.ProviderID)
	if providerID == "" {
		return ProviderCapabilities{}, false
	}
	return ProviderCapabilities{
		ProviderID:                     providerID,
		SupportsResponsesAPI:           override.SupportsResponsesAPI,
		SupportsResponsesCompact:       override.SupportsResponsesCompact,
		SupportsRequestInputTokenCount: override.SupportsRequestInputTokenCount,
		SupportsPromptCacheKey:         override.SupportsPromptCacheKey,
		SupportsNativeWebSearch:        override.SupportsNativeWebSearch,
		SupportsReasoningEncrypted:     override.SupportsReasoningEncrypted,
		SupportsServerSideContextEdit:  override.SupportsServerSideContextEdit,
		IsOpenAIFirstParty:             override.IsOpenAIFirstParty,
	}, true
}

func ProviderCapabilitiesFromLocked(locked *session.LockedContract) (ProviderCapabilities, bool) {
	if locked == nil {
		return ProviderCapabilities{}, false
	}
	providerID := strings.TrimSpace(locked.ProviderContract.ProviderID)
	if providerID == "" {
		return ProviderCapabilities{}, false
	}
	supportsRequestInputTokenCount := locked.ProviderContract.SupportsRequestInputTokenCount
	if !locked.ProviderContract.HasSupportsRequestInputTokenCount {
		if contract, ok := LookupProviderCapabilityContract(providerID); ok {
			supportsRequestInputTokenCount = contract.SupportsRequestInputTokenCount
		}
	}
	supportsPromptCacheKey := locked.ProviderContract.SupportsPromptCacheKey
	if !locked.ProviderContract.HasSupportsPromptCacheKey {
		switch strings.TrimSpace(locked.ProviderContract.ProviderID) {
		case "openai", "chatgpt-codex":
			supportsPromptCacheKey = locked.ProviderContract.SupportsResponsesAPI
		}
	}
	return ProviderCapabilities{
		ProviderID:                     providerID,
		SupportsResponsesAPI:           locked.ProviderContract.SupportsResponsesAPI,
		SupportsResponsesCompact:       locked.ProviderContract.SupportsResponsesCompact,
		SupportsRequestInputTokenCount: supportsRequestInputTokenCount,
		SupportsPromptCacheKey:         supportsPromptCacheKey,
		SupportsNativeWebSearch:        locked.ProviderContract.SupportsNativeWebSearch,
		SupportsReasoningEncrypted:     locked.ProviderContract.SupportsReasoningEncrypted,
		SupportsServerSideContextEdit:  locked.ProviderContract.SupportsServerSideContextEdit,
		IsOpenAIFirstParty:             locked.ProviderContract.IsOpenAIFirstParty,
	}, true
}

func LockedContractSupportsReasoningEffort(locked *session.LockedContract, model string) bool {
	if locked != nil && (locked.ModelCapabilities.SupportsReasoningEffort || locked.ModelCapabilities.SupportsVisionInputs) {
		return locked.ModelCapabilities.SupportsReasoningEffort
	}
	return SupportsReasoningEffortModel(model)
}

func LockedContractSupportsVisionInputs(locked *session.LockedContract, model string) bool {
	if locked != nil && (locked.ModelCapabilities.SupportsReasoningEffort || locked.ModelCapabilities.SupportsVisionInputs) {
		return locked.ModelCapabilities.SupportsVisionInputs
	}
	return SupportsVisionInputsModel(model)
}
