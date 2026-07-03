package llm

import (
	"strings"

	"core/shared/config"
	"core/shared/modelcontract"
)

type ModelMetadata = modelcontract.ModelMetadata

var defaultSupportedThinkingLevels = []string{"low", "medium", "high"}
var defaultSupportedVerbosityLevels = []string{"low", "medium", "high"}

type ModelVerbositySupportSource string

const (
	ModelVerbositySupportSourceModelCatalog    ModelVerbositySupportSource = "model_catalog"
	ModelVerbositySupportSourceProviderDefault ModelVerbositySupportSource = "provider_default"
)

type ModelVerbositySupport struct {
	Supported bool
	Source    ModelVerbositySupportSource
	Levels    []string
}

func ModelDisplayLabel(model string, thinkingLevel string) string {
	modelLabel := strings.TrimSpace(model)
	if modelLabel == "" {
		modelLabel = "gpt-5"
	}
	level := strings.TrimSpace(thinkingLevel)
	if level == "" {
		return modelLabel
	}
	if !SupportsReasoningEffortModel(modelLabel) {
		return modelLabel
	}
	return modelLabel + " " + level
}

// SupportsReasoningEffortModel reports whether reasoning effort is enabled for
// the given model identifier. Unknown non-empty models default to reasoning
// support so new model rollouts do not silently disable thinking.
func SupportsReasoningEffortModel(model string) bool {
	normalized := strings.TrimSpace(model)
	if normalized == "" {
		return false
	}
	contract, ok := LookupModelCapabilityContract(normalized)
	if !ok {
		return true
	}
	return contract.SupportsReasoningEffort
}

// SupportsReasoningSummaryModel reports whether the Responses API
// reasoning.summary field should be sent for the given model identifier.
// Unknown models default to false because unsupported summary fields can
// hard-fail requests.
func SupportsReasoningSummaryModel(model string) bool {
	contract, ok := LookupModelCapabilityContract(model)
	return ok && contract.SupportsReasoningSummary
}

// SupportsVisionInputsModel reports whether the explicit model capability
// contract allows multimodal image/file inputs for the Responses API.
func SupportsVisionInputsModel(model string) bool {
	contract, ok := LookupModelCapabilityContract(model)
	return ok && contract.SupportsVisionInputs
}

// SupportsVerbosityModel reports whether Responses API text verbosity should be
// sent for the given model identifier. Unknown models default to false because
// unsupported verbosity fields can hard-fail requests.
func SupportsVerbosityModel(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if normalized == "" {
		return false
	}
	contract, ok := LookupModelCapabilityContract(normalized)
	return ok && contract.SupportsVerbosity
}

func VerbositySupportForModelAndProvider(model string, providerCaps ProviderCapabilities) ModelVerbositySupport {
	contract, ok := LookupModelCapabilityContract(model)
	if ok {
		return ModelVerbositySupport{
			Supported: contract.SupportsVerbosity,
			Source:    ModelVerbositySupportSourceModelCatalog,
			Levels:    verbosityLevelsFromSupport(contract.SupportsVerbosity, contract.SupportedVerbosityLevels),
		}
	}
	supported := strings.TrimSpace(model) != "" && providerCaps.IsOpenAIFirstParty
	return ModelVerbositySupport{
		Supported: supported,
		Source:    ModelVerbositySupportSourceProviderDefault,
		Levels:    verbosityLevelsFromSupport(supported, nil),
	}
}

func LookupModelMetadata(model string) (ModelMetadata, bool) {
	contract, ok := LookupModelCapabilityContract(model)
	if !ok {
		return ModelMetadata{}, false
	}
	return ModelMetadata{
		ContextWindowTokens:      contract.ContextWindowTokens,
		LargeContextWindowTokens: contract.LargeContextWindowTokens,
	}, contract.ContextWindowTokens > 0 || contract.LargeContextWindowTokens > 0
}

func ApplyDerivedModelContextBudget(settings *config.Settings, model string, fallbackWindow, fallbackThreshold int) {
	if settings == nil {
		return
	}
	if meta, ok := LookupModelMetadata(model); ok && meta.ContextWindowTokens > 0 {
		settings.ModelContextWindow = meta.ContextWindowTokens
		settings.ContextCompactionThresholdTokens = meta.ContextWindowTokens * 95 / 100
		return
	}
	settings.ModelContextWindow = fallbackWindow
	settings.ContextCompactionThresholdTokens = fallbackThreshold
}

func SupportedThinkingLevelsModel(model string) []string {
	if !SupportsReasoningEffortModel(model) {
		return nil
	}
	contract, ok := LookupModelCapabilityContract(model)
	if ok && len(contract.SupportedReasoningEfforts) > 0 {
		return append([]string(nil), contract.SupportedReasoningEfforts...)
	}
	return append([]string(nil), defaultSupportedThinkingLevels...)
}

func SupportedVerbosityLevelsModel(model string) []string {
	if !SupportsVerbosityModel(model) {
		return nil
	}
	contract, ok := LookupModelCapabilityContract(model)
	if !ok {
		return nil
	}
	return verbosityLevelsFromSupport(contract.SupportsVerbosity, contract.SupportedVerbosityLevels)
}

func SupportsLargeContextWindowModel(model string) bool {
	contract, ok := LookupModelCapabilityContract(model)
	return ok && contract.LargeContextWindowTokens > 0
}

func verbosityLevelsFromSupport(supported bool, levels []string) []string {
	if !supported {
		return nil
	}
	if len(levels) > 0 {
		return append([]string(nil), levels...)
	}
	return append([]string(nil), defaultSupportedVerbosityLevels...)
}
