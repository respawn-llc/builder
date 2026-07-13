package app

import (
	"strings"

	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/toolspec"
)

type onboardingThemeKind string

const (
	onboardingThemeAuto  onboardingThemeKind = "auto"
	onboardingThemeLight onboardingThemeKind = "light"
	onboardingThemeDark  onboardingThemeKind = "dark"
)

type onboardingThemeSelection struct {
	kind onboardingThemeKind
}

type onboardingModelKind string

const (
	onboardingModelKnown  onboardingModelKind = "known"
	onboardingModelCustom onboardingModelKind = "custom"
)

type onboardingModelSelection struct {
	kind  onboardingModelKind
	value string
}

type onboardingContextKind string

const (
	onboardingContextDefault onboardingContextKind = "default"
	onboardingContextLarge   onboardingContextKind = "large"
	onboardingContextCustom  onboardingContextKind = "custom"
)

type onboardingContextSelection struct {
	kind   onboardingContextKind
	tokens int
}

type onboardingThinkingKind string

const (
	onboardingThinkingDefault  onboardingThinkingKind = "default"
	onboardingThinkingDisabled onboardingThinkingKind = "disabled"
	onboardingThinkingLevel    onboardingThinkingKind = "level"
	onboardingThinkingCustom   onboardingThinkingKind = "custom"
)

type onboardingThinkingSelection struct {
	kind  onboardingThinkingKind
	value string
}

type onboardingVerbosityKind string

const (
	onboardingVerbosityOmitted onboardingVerbosityKind = "omitted"
	onboardingVerbosityLevel   onboardingVerbosityKind = "level"
)

type onboardingVerbositySelection struct {
	kind  onboardingVerbosityKind
	value string
}

type onboardingSupervisorFrequency string

const (
	onboardingSupervisorOff   onboardingSupervisorFrequency = "off"
	onboardingSupervisorEdits onboardingSupervisorFrequency = "edits"
	onboardingSupervisorAll   onboardingSupervisorFrequency = "all"
)

type onboardingReviewerModelKind string

const (
	onboardingReviewerModelInherited  onboardingReviewerModelKind = "inherited"
	onboardingReviewerModelOverridden onboardingReviewerModelKind = "overridden"
)

type onboardingReviewerModelSelection struct {
	kind     onboardingReviewerModelKind
	override onboardingModelSelection
}

type onboardingReviewerThinkingKind string

const (
	onboardingReviewerThinkingInherited  onboardingReviewerThinkingKind = "inherited"
	onboardingReviewerThinkingOverridden onboardingReviewerThinkingKind = "overridden"
)

type onboardingReviewerThinkingSelection struct {
	kind     onboardingReviewerThinkingKind
	override onboardingThinkingSelection
}

type onboardingSupervisorSelection struct {
	frequency onboardingSupervisorFrequency
	model     onboardingReviewerModelSelection
	thinking  onboardingReviewerThinkingSelection
}

type onboardingCompactionSelection string

const (
	onboardingCompactionLocal      onboardingCompactionSelection = "local"
	onboardingCompactionNative     onboardingCompactionSelection = "native"
	onboardingCompactionManualOnly onboardingCompactionSelection = "manual_only"
)

type onboardingThinkingEditKind string

const (
	onboardingThinkingEditNone       onboardingThinkingEditKind = "none"
	onboardingThinkingEditPending    onboardingThinkingEditKind = "pending"
	onboardingThinkingEditRevisiting onboardingThinkingEditKind = "revisiting"
)

type onboardingThinkingEdit struct {
	kind onboardingThinkingEditKind
}

type onboardingPreservedInputs struct {
	providerOverride           *string
	openAIBaseURL              *string
	modelTimeoutSeconds        *int
	enabledTools               map[toolspec.ID]bool
	baselineModelContextWindow *int
}

type onboardingSelections struct {
	theme                   onboardingThemeSelection
	model                   onboardingModelSelection
	contextWindow           onboardingContextSelection
	thinking                onboardingThinkingSelection
	verbosity               onboardingVerbositySelection
	askQuestion             bool
	supervisor              onboardingSupervisorSelection
	compaction              onboardingCompactionSelection
	skillImport             onboardingImportSelection
	skillEnablement         map[string]bool
	pendingPrimaryThinking  onboardingThinkingEdit
	pendingReviewerThinking onboardingThinkingEdit
	preserved               onboardingPreservedInputs
}

func modelFactForFacts(facts serverapi.CapabilityFactsResponse, model string) serverapi.ModelCapabilityFact {
	trimmed := strings.TrimSpace(model)
	for _, fact := range facts.Models.KnownModels {
		if fact.ModelID != nil && strings.EqualFold(strings.TrimSpace(*fact.ModelID), trimmed) {
			return fact
		}
	}
	return facts.Models.UnknownFallback
}

func (selections onboardingSelections) themeValue() string {
	return string(selections.theme.kind)
}

func (selections onboardingSelections) thinkingValue() string {
	switch selections.thinking.kind {
	case onboardingThinkingDefault:
		return config.DefaultOnboardingSettings().ThinkingLevel
	case onboardingThinkingDisabled:
		return ""
	default:
		return selections.thinking.value
	}
}

func (selections onboardingSelections) reviewerModelValue() string {
	if selections.supervisor.model.kind == onboardingReviewerModelOverridden {
		return selections.supervisor.model.override.value
	}
	return selections.model.value
}

func (selections onboardingSelections) reviewerThinkingValue() string {
	if selections.supervisor.thinking.kind == onboardingReviewerThinkingOverridden {
		switch selections.supervisor.thinking.override.kind {
		case onboardingThinkingDisabled:
			return ""
		case onboardingThinkingDefault:
			return config.DefaultOnboardingSettings().ThinkingLevel
		default:
			return selections.supervisor.thinking.override.value
		}
	}
	return selections.thinkingValue()
}

func (selections onboardingSelections) contextWindowTokens(fact serverapi.ModelCapabilityFact) int {
	switch selections.contextWindow.kind {
	case onboardingContextLarge:
		if fact.LargeWindow != nil {
			return fact.LargeWindow.Tokens
		}
	case onboardingContextCustom:
		return selections.contextWindow.tokens
	default:
		if fact.ContextWindowTokens != nil {
			return *fact.ContextWindowTokens
		}
	}
	if selections.preserved.baselineModelContextWindow != nil {
		return *selections.preserved.baselineModelContextWindow
	}
	return 0
}

func (selections onboardingSelections) compactionValue() config.CompactionMode {
	switch selections.compaction {
	case onboardingCompactionNative:
		return config.CompactionModeNative
	case onboardingCompactionManualOnly:
		return config.CompactionModeNone
	default:
		return config.CompactionModeLocal
	}
}
