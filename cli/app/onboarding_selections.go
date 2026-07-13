package app

import (
	"fmt"
	"maps"
	"runtime/debug"
	"slices"
	"strings"

	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/theme"
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

type onboardingSelectionConversionError struct {
	Field  string
	Value  any
	Reason string
}

func (e *onboardingSelectionConversionError) Error() string {
	return fmt.Sprintf("convert onboarding selection %s (%v): %s", e.Field, e.Value, e.Reason)
}

type onboardingInvariantDiagnostic struct {
	Operation           string
	StepID              string
	ModelIdentity       string
	VariantType         string
	VariantTag          string
	PendingPrimaryEdit  onboardingThinkingEditKind
	PendingReviewerEdit onboardingThinkingEditKind
	PendingAction       onboardingPendingAction
	Stack               string
}

type onboardingInvariantViolation struct {
	VariantType string
	VariantTag  string
}

type onboardingInternalStateError struct {
	Diagnostic onboardingInvariantDiagnostic
}

func (e *onboardingInternalStateError) Error() string {
	return fmt.Sprintf(
		"onboarding cannot continue because its selection state is invalid during %s at step %s (%s=%s)",
		e.Diagnostic.Operation,
		e.Diagnostic.StepID,
		e.Diagnostic.VariantType,
		e.Diagnostic.VariantTag,
	)
}

func newOnboardingFlowState(cfg config.App, facts serverapi.CapabilityFactsResponse) (onboardingFlowState, error) {
	selections, err := onboardingSelectionsFromConfig(cfg, facts)
	if err != nil {
		return onboardingFlowState{}, err
	}
	state := onboardingFlowState{
		selections:    selections,
		facts:         facts,
		pendingAction: onboardingPendingActionNone,
		imports:       onboardingImportDiscoveryFromFacts(facts.Imports),
		debug:         cfg.Settings.Debug,
	}
	state.selections.skillEnablement = initialSkillEnablement(&state)
	if err := state.validateInvariant("construction", "theme"); err != nil {
		return onboardingFlowState{}, err
	}
	return state, nil
}

func onboardingSelectionsFromConfig(cfg config.App, facts serverapi.CapabilityFactsResponse) (onboardingSelections, error) {
	if err := validateOnboardingFacts(facts); err != nil {
		return onboardingSelections{}, err
	}
	settings := cfg.Settings
	model, err := onboardingModelSelectionFromValue(settings.Model, facts)
	if err != nil {
		return onboardingSelections{}, err
	}
	themeSelection, err := seedThemeSelection(settings.Theme)
	if err != nil {
		return onboardingSelections{}, err
	}
	contextWindow, err := seedContextSelection(settings.ModelContextWindow, model.value, facts)
	if err != nil {
		return onboardingSelections{}, err
	}
	thinkingSource, err := requiredOnboardingSource(cfg.Source.Sources, "thinking_level")
	if err != nil {
		return onboardingSelections{}, err
	}
	if thinkingSource == "default" && strings.TrimSpace(settings.ThinkingLevel) != strings.TrimSpace(config.DefaultOnboardingSettings().ThinkingLevel) {
		return onboardingSelections{}, conversionError("thinking_level", settings.ThinkingLevel, "resolved value does not match default provenance")
	}
	thinking, err := seedThinkingSelection(
		settings.ThinkingLevel,
		thinkingSource == "default",
		modelFactForFacts(facts, model.value).SupportedThinkingLevels,
	)
	if err != nil {
		return onboardingSelections{}, err
	}
	verbosity, err := seedVerbositySelection(settings.ModelVerbosity)
	if err != nil {
		return onboardingSelections{}, err
	}
	supervisor, err := seedSupervisorSelection(settings, cfg.Source.Sources, facts)
	if err != nil {
		return onboardingSelections{}, err
	}
	compaction, err := seedCompactionSelection(settings.CompactionMode)
	if err != nil {
		return onboardingSelections{}, err
	}
	if settings.Timeouts.ModelRequestSeconds <= 0 {
		return onboardingSelections{}, conversionError("timeouts.model_request_seconds", settings.Timeouts.ModelRequestSeconds, "must be positive")
	}
	var modelTimeoutSeconds *int
	if settings.Timeouts.ModelRequestSeconds != config.DefaultOnboardingSettings().Timeouts.ModelRequestSeconds {
		value := settings.Timeouts.ModelRequestSeconds
		modelTimeoutSeconds = &value
	}
	var baselineModelContextWindow *int
	if settings.ModelContextWindow > 0 {
		value := settings.ModelContextWindow
		baselineModelContextWindow = &value
	}
	enabledTools := resolvedOnboardingTools(settings.EnabledTools)
	return onboardingSelections{
		theme:                   themeSelection,
		model:                   model,
		contextWindow:           contextWindow,
		thinking:                thinking,
		verbosity:               verbosity,
		askQuestion:             enabledTools[toolspec.ToolAskQuestion],
		supervisor:              supervisor,
		compaction:              compaction,
		skillImport:             onboardingImportSelection{Mode: onboardingImportModeNone},
		skillEnablement:         map[string]bool{},
		pendingPrimaryThinking:  onboardingThinkingEdit{kind: onboardingThinkingEditNone},
		pendingReviewerThinking: onboardingThinkingEdit{kind: onboardingThinkingEditNone},
		preserved: onboardingPreservedInputs{
			providerOverride:           optionalNonBlankString(settings.ProviderOverride),
			openAIBaseURL:              optionalNonBlankString(settings.OpenAIBaseURL),
			modelTimeoutSeconds:        modelTimeoutSeconds,
			enabledTools:               enabledTools,
			baselineModelContextWindow: baselineModelContextWindow,
		},
	}, nil
}

func seedThemeSelection(value string) (onboardingThemeSelection, error) {
	switch theme.Normalize(value) {
	case theme.Auto:
		return onboardingThemeSelection{kind: onboardingThemeAuto}, nil
	case theme.Light:
		return onboardingThemeSelection{kind: onboardingThemeLight}, nil
	case theme.Dark:
		return onboardingThemeSelection{kind: onboardingThemeDark}, nil
	default:
		return onboardingThemeSelection{}, conversionError("theme", value, "unsupported theme")
	}
}

func onboardingModelSelectionFromValue(value string, facts serverapi.CapabilityFactsResponse) (onboardingModelSelection, error) {
	model := strings.TrimSpace(value)
	if model == "" {
		return onboardingModelSelection{}, conversionError("model", value, "must not be blank")
	}
	kind := onboardingModelCustom
	if modelFactForFacts(facts, model).Known {
		kind = onboardingModelKnown
	}
	return onboardingModelSelection{kind: kind, value: model}, nil
}

func seedContextSelection(tokens int, model string, facts serverapi.CapabilityFactsResponse) (onboardingContextSelection, error) {
	fact := modelFactForFacts(facts, model)
	switch {
	case tokens == 0:
		return onboardingContextSelection{kind: onboardingContextDefault}, nil
	case fact.ContextWindowTokens != nil && tokens == *fact.ContextWindowTokens:
		return onboardingContextSelection{kind: onboardingContextDefault}, nil
	case fact.LargeWindow != nil && tokens == fact.LargeWindow.Tokens:
		return onboardingContextSelection{kind: onboardingContextLarge}, nil
	case tokens > 0:
		return onboardingContextSelection{kind: onboardingContextCustom, tokens: tokens}, nil
	default:
		return onboardingContextSelection{}, conversionError("model_context_window", tokens, "custom context window must be positive")
	}
}

func seedThinkingSelection(value string, useDefault bool, supportedLevels []string) (onboardingThinkingSelection, error) {
	trimmed := strings.TrimSpace(value)
	if useDefault {
		if trimmed == "" {
			return onboardingThinkingSelection{}, conversionError("thinking_level", value, "default thinking value must not be blank")
		}
		return onboardingThinkingSelection{kind: onboardingThinkingDefault}, nil
	}
	if trimmed == "" {
		return onboardingThinkingSelection{kind: onboardingThinkingDisabled}, nil
	}
	if slices.Contains(supportedLevels, trimmed) {
		return onboardingThinkingSelection{kind: onboardingThinkingLevel, value: trimmed}, nil
	}
	return onboardingThinkingSelection{kind: onboardingThinkingCustom, value: trimmed}, nil
}

func seedVerbositySelection(value config.ModelVerbosity) (onboardingVerbositySelection, error) {
	switch value {
	case "":
		return onboardingVerbositySelection{kind: onboardingVerbosityOmitted}, nil
	case config.ModelVerbosityLow, config.ModelVerbosityMedium, config.ModelVerbosityHigh:
		return onboardingVerbositySelection{kind: onboardingVerbosityLevel, value: string(value)}, nil
	default:
		return onboardingVerbositySelection{}, conversionError("model_verbosity", value, "unsupported verbosity")
	}
}

func seedSupervisorSelection(settings config.Settings, sources map[string]string, facts serverapi.CapabilityFactsResponse) (onboardingSupervisorSelection, error) {
	var frequency onboardingSupervisorFrequency
	switch strings.TrimSpace(settings.Reviewer.Frequency) {
	case "", "off":
		frequency = onboardingSupervisorOff
	case "edits":
		frequency = onboardingSupervisorEdits
	case "all":
		frequency = onboardingSupervisorAll
	default:
		return onboardingSupervisorSelection{}, conversionError("reviewer.frequency", settings.Reviewer.Frequency, "unsupported frequency")
	}
	modelSource, err := requiredOnboardingSource(sources, "reviewer.model")
	if err != nil {
		return onboardingSupervisorSelection{}, err
	}
	thinkingSource, err := requiredOnboardingSource(sources, "reviewer.thinking_level")
	if err != nil {
		return onboardingSupervisorSelection{}, err
	}
	reviewerModel := onboardingReviewerModelSelection{kind: onboardingReviewerModelInherited}
	if modelSource == "default" {
		if strings.TrimSpace(settings.Reviewer.Model) != strings.TrimSpace(settings.Model) {
			return onboardingSupervisorSelection{}, conversionError("reviewer.model", settings.Reviewer.Model, "resolved value does not match inherited provenance")
		}
	} else {
		override, err := onboardingModelSelectionFromValue(settings.Reviewer.Model, facts)
		if err != nil {
			return onboardingSupervisorSelection{}, conversionError("reviewer.model", settings.Reviewer.Model, err.Error())
		}
		reviewerModel = onboardingReviewerModelSelection{kind: onboardingReviewerModelOverridden, override: override}
	}
	reviewerThinking := onboardingReviewerThinkingSelection{kind: onboardingReviewerThinkingInherited}
	if thinkingSource == "default" {
		if strings.TrimSpace(settings.Reviewer.ThinkingLevel) != strings.TrimSpace(settings.ThinkingLevel) {
			return onboardingSupervisorSelection{}, conversionError("reviewer.thinking_level", settings.Reviewer.ThinkingLevel, "resolved value does not match inherited provenance")
		}
	} else {
		override, err := seedThinkingSelection(
			settings.Reviewer.ThinkingLevel,
			false,
			modelFactForFacts(facts, settings.Reviewer.Model).SupportedThinkingLevels,
		)
		if err != nil {
			return onboardingSupervisorSelection{}, conversionError("reviewer.thinking_level", settings.Reviewer.ThinkingLevel, err.Error())
		}
		reviewerThinking = onboardingReviewerThinkingSelection{kind: onboardingReviewerThinkingOverridden, override: override}
	}
	return onboardingSupervisorSelection{
		frequency: frequency,
		model:     reviewerModel,
		thinking:  reviewerThinking,
	}, nil
}

func seedCompactionSelection(value config.CompactionMode) (onboardingCompactionSelection, error) {
	switch value {
	case config.CompactionModeLocal:
		return onboardingCompactionLocal, nil
	case config.CompactionModeNative:
		return onboardingCompactionNative, nil
	case config.CompactionModeNone:
		return onboardingCompactionManualOnly, nil
	default:
		return "", conversionError("compaction_mode", value, "unsupported compaction mode")
	}
}

func requiredOnboardingSource(sources map[string]string, key string) (string, error) {
	source, ok := sources[key]
	if !ok {
		return "", conversionError(key, nil, "source provenance is missing")
	}
	switch source {
	case "default", "file", "env", "cli", "subagent":
		return source, nil
	default:
		return "", conversionError(key, source, "unknown source provenance")
	}
}

func validateOnboardingFacts(facts serverapi.CapabilityFactsResponse) error {
	for index, fact := range facts.Models.KnownModels {
		if fact.ModelID == nil || strings.TrimSpace(*fact.ModelID) == "" {
			return conversionError(fmt.Sprintf("facts.models.known_models[%d].model_id", index), fact.ModelID, "known model id must be present")
		}
		if !fact.Known {
			return conversionError(fmt.Sprintf("facts.models.known_models[%d].known", index), fact.Known, "catalog model must be marked known")
		}
		if err := validateModelFactDimensions(fmt.Sprintf("facts.models.known_models[%d]", index), fact); err != nil {
			return err
		}
	}
	if err := validateModelFactDimensions("facts.models.unknown_fallback", facts.Models.UnknownFallback); err != nil {
		return err
	}
	return validateOnboardingImportFacts(facts.Imports)
}

func validateModelFactDimensions(field string, fact serverapi.ModelCapabilityFact) error {
	if fact.ContextWindowTokens != nil && *fact.ContextWindowTokens <= 0 {
		return conversionError(field+".context_window_tokens", *fact.ContextWindowTokens, "must be positive")
	}
	if fact.LargeWindow != nil && fact.LargeWindow.Tokens <= 0 {
		return conversionError(field+".large_window.tokens", fact.LargeWindow.Tokens, "must be positive")
	}
	for index, level := range fact.SupportedThinkingLevels {
		if strings.TrimSpace(level) == "" {
			return conversionError(fmt.Sprintf("%s.supported_thinking_levels[%d]", field, index), level, "must not be blank")
		}
	}
	for index, level := range fact.Verbosity.Levels {
		switch strings.TrimSpace(level) {
		case string(config.ModelVerbosityLow), string(config.ModelVerbosityMedium), string(config.ModelVerbosityHigh):
		default:
			return conversionError(fmt.Sprintf("%s.verbosity.levels[%d]", field, index), level, "unsupported verbosity")
		}
	}
	return nil
}

func validateOnboardingImportFacts(facts serverapi.ImportCapabilityFacts) error {
	validateRef := func(field string, ref serverapi.ImportChoiceRef) error {
		switch onboardingImportMode(ref.Mode) {
		case onboardingImportModeNone:
			return nil
		case onboardingImportModeSymlinkSource:
			if ref.ImportProviderID == nil || strings.TrimSpace(*ref.ImportProviderID) == "" {
				return conversionError(field+".import_provider_id", ref.ImportProviderID, "must be present and non-blank")
			}
			if ref.SourceRootPath == nil || strings.TrimSpace(*ref.SourceRootPath) == "" {
				return conversionError(field+".source_root_path", ref.SourceRootPath, "must be present and non-blank")
			}
			return nil
		default:
			return conversionError(field+".mode", ref.Mode, "unsupported import mode")
		}
	}
	for index, choice := range facts.Skills.Choices {
		field := fmt.Sprintf("facts.imports.skills.choices[%d]", index)
		if choice.ItemCount < 0 {
			return conversionError(field+".item_count", choice.ItemCount, "must not be negative")
		}
		if err := validateRef(field+".ref", choice.Ref); err != nil {
			return err
		}
	}
	for index, projection := range facts.SkillEnablement {
		field := fmt.Sprintf("facts.imports.skill_enablement[%d]", index)
		if err := validateRef(field+".choice_ref", projection.ChoiceRef); err != nil {
			return err
		}
		for candidateIndex, candidate := range projection.Candidates {
			if strings.TrimSpace(candidate.Ref.TargetName) == "" {
				return conversionError(
					fmt.Sprintf("%s.candidates[%d].ref.target_name", field, candidateIndex),
					candidate.Ref.TargetName,
					"must not be blank",
				)
			}
		}
	}
	return nil
}

func conversionError(field string, value any, reason string) error {
	return &onboardingSelectionConversionError{Field: field, Value: value, Reason: reason}
}

func optionalNonBlankString(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func resolvedOnboardingTools(source map[toolspec.ID]bool) map[toolspec.ID]bool {
	defaults := config.DefaultOnboardingSettings().EnabledTools
	resolved := make(map[toolspec.ID]bool, len(toolspec.CatalogIDs()))
	for _, id := range toolspec.CatalogIDs() {
		value, ok := source[id]
		if !ok {
			value = defaults[id]
		}
		resolved[id] = value
	}
	return resolved
}

func (state *onboardingFlowState) updateSelections(
	operation string,
	stepID onboardingStepID,
	update func(*onboardingSelections) error,
) error {
	next := state.selections.clone()
	if err := update(&next); err != nil {
		return err
	}
	candidate := *state
	candidate.selections = next
	if err := candidate.validateInvariant(operation, stepID); err != nil {
		return err
	}
	state.selections = next
	return nil
}

func (state *onboardingFlowState) chooseTheme(choiceID string) error {
	return state.updateSelections("apply_choice", onboardingStepTheme, func(selections *onboardingSelections) error {
		return selections.chooseTheme(choiceID)
	})
}

func (state *onboardingFlowState) submitPrimaryModel(value string) error {
	return state.updateSelections("apply_model", onboardingStepModel, func(selections *onboardingSelections) error {
		return selections.submitPrimaryModel(value, state.facts)
	})
}

func (state *onboardingFlowState) chooseContextWindow(choiceID string) error {
	return state.updateSelections("apply_choice", onboardingStepContextWindow, func(selections *onboardingSelections) error {
		return selections.chooseContextWindow(choiceID, state.facts)
	})
}

func (state *onboardingFlowState) choosePrimaryThinking(choiceID string) error {
	return state.updateSelections("apply_choice", onboardingStepThinking, func(selections *onboardingSelections) error {
		return selections.choosePrimaryThinking(choiceID, state.facts)
	})
}

func (state *onboardingFlowState) commitPrimaryCustomThinking(value string) error {
	return state.updateSelections("apply_input", onboardingStepThinkingCustom, func(selections *onboardingSelections) error {
		return selections.commitPrimaryCustomThinking(value, state.facts)
	})
}

func (state *onboardingFlowState) chooseVerbosity(choiceID string) error {
	return state.updateSelections("apply_choice", onboardingStepVerbosity, func(selections *onboardingSelections) error {
		return selections.chooseVerbosity(choiceID, state.facts)
	})
}

func (state *onboardingFlowState) chooseAskQuestion(choiceID string) error {
	return state.updateSelections("apply_choice", onboardingStepAskQuestion, func(selections *onboardingSelections) error {
		return selections.chooseAskQuestion(choiceID)
	})
}

func (state *onboardingFlowState) chooseSupervisorFrequency(choiceID string) error {
	return state.updateSelections("apply_choice", onboardingStepReviewer, func(selections *onboardingSelections) error {
		return selections.chooseSupervisorFrequency(choiceID, state.facts)
	})
}

func (state *onboardingFlowState) submitReviewerModel(value string) error {
	return state.updateSelections("apply_input", onboardingStepReviewerModel, func(selections *onboardingSelections) error {
		return selections.submitReviewerModel(value, state.facts)
	})
}

func (state *onboardingFlowState) chooseReviewerThinking(choiceID string) error {
	return state.updateSelections("apply_choice", onboardingStepReviewerThinking, func(selections *onboardingSelections) error {
		return selections.chooseReviewerThinking(choiceID, state.facts)
	})
}

func (state *onboardingFlowState) commitReviewerCustomThinking(value string) error {
	return state.updateSelections("apply_input", onboardingStepReviewerThinkingCustom, func(selections *onboardingSelections) error {
		return selections.commitReviewerCustomThinking(value)
	})
}

func (state *onboardingFlowState) chooseCompaction(choiceID string) error {
	return state.updateSelections("apply_choice", onboardingStepCompaction, func(selections *onboardingSelections) error {
		return selections.chooseCompaction(choiceID)
	})
}

func (state *onboardingFlowState) chooseSkillImport(choiceID string) error {
	return state.updateSelections("apply_choice", onboardingStepSkillsImport, func(selections *onboardingSelections) error {
		if err := applyImportChoice(&selections.skillImport, choiceID, state.imports.skillChoices); err != nil {
			return err
		}
		candidate := *state
		candidate.selections = *selections
		selections.skillEnablement = initialSkillEnablement(&candidate)
		return nil
	})
}

func (state *onboardingFlowState) chooseSkillEnablement(selection map[string]bool) error {
	candidates := skillSelectionCandidates(state)
	return state.updateSelections("apply_multi_select", onboardingStepSkillsEnabled, func(selections *onboardingSelections) error {
		return selections.chooseSkillEnablement(selection, candidates)
	})
}

func (state *onboardingFlowState) refreshSkillSelectionsAfterDiscovery() error {
	return state.updateSelections("import_discovery", onboardingStepSkillsImport, func(selections *onboardingSelections) error {
		if state.imports.skipSkills {
			selections.skillImport = onboardingImportSelection{Mode: onboardingImportModeNone}
		}
		candidate := *state
		candidate.selections = *selections
		selections.skillEnablement = initialSkillEnablement(&candidate)
		return nil
	})
}

func (selections onboardingSelections) clone() onboardingSelections {
	selections.skillEnablement = maps.Clone(selections.skillEnablement)
	selections.preserved.enabledTools = maps.Clone(selections.preserved.enabledTools)
	return selections
}

func (selections *onboardingSelections) chooseTheme(choiceID string) error {
	normalizedChoice := theme.Normalize(choiceID)
	if selections.theme.kind == onboardingThemeAuto && normalizedChoice == theme.Resolve(theme.Auto) {
		return nil
	}
	switch normalizedChoice {
	case theme.Light:
		selections.theme = onboardingThemeSelection{kind: onboardingThemeLight}
	case theme.Dark:
		selections.theme = onboardingThemeSelection{kind: onboardingThemeDark}
	default:
		return conversionError("theme", choiceID, "unsupported theme choice")
	}
	return nil
}

func (selections *onboardingSelections) submitPrimaryModel(value string, facts serverapi.CapabilityFactsResponse) error {
	model, err := onboardingModelSelectionFromValue(value, facts)
	if err != nil {
		return err
	}
	selections.model = model
	fact := modelFactForFacts(facts, model.value)
	if !fact.Verbosity.Supported {
		selections.verbosity = onboardingVerbositySelection{kind: onboardingVerbosityOmitted}
	} else if selections.verbosity.kind == onboardingVerbosityOmitted {
		selections.verbosity = onboardingVerbositySelection{kind: onboardingVerbosityLevel, value: string(config.ModelVerbosityLow)}
	}
	if !fact.SupportsThinking {
		selections.thinking = onboardingThinkingSelection{kind: onboardingThinkingDisabled}
		selections.pendingPrimaryThinking = onboardingThinkingEdit{kind: onboardingThinkingEditNone}
	} else if selections.thinking.kind == onboardingThinkingLevel &&
		!slices.Contains(fact.SupportedThinkingLevels, selections.thinking.value) {
		selections.thinking.kind = onboardingThinkingCustom
	}
	if err := selections.chooseContextWindow("default", facts); err != nil {
		return err
	}
	selections.normalizeReviewerThinking(facts)
	return nil
}

func (selections *onboardingSelections) chooseContextWindow(choiceID string, facts serverapi.CapabilityFactsResponse) error {
	fact := modelFactForFacts(facts, selections.model.value)
	switch choiceID {
	case "large":
		if fact.LargeWindow == nil || fact.LargeWindow.Tokens <= 0 {
			return conversionError("context_window", choiceID, "large context window is unavailable")
		}
		selections.contextWindow = onboardingContextSelection{kind: onboardingContextLarge}
	case "default":
		switch {
		case fact.ContextWindowTokens != nil && *fact.ContextWindowTokens > 0:
			selections.contextWindow = onboardingContextSelection{kind: onboardingContextDefault}
		case selections.preserved.baselineModelContextWindow != nil:
			selections.contextWindow = onboardingContextSelection{
				kind:   onboardingContextCustom,
				tokens: *selections.preserved.baselineModelContextWindow,
			}
		default:
			selections.contextWindow = onboardingContextSelection{kind: onboardingContextDefault}
		}
	default:
		return conversionError("context_window", choiceID, "unsupported context window choice")
	}
	return nil
}

func (selections *onboardingSelections) choosePrimaryThinking(choiceID string, facts serverapi.CapabilityFactsResponse) error {
	switch choiceID {
	case "disable":
		selections.thinking = onboardingThinkingSelection{kind: onboardingThinkingDisabled}
		selections.pendingPrimaryThinking = onboardingThinkingEdit{kind: onboardingThinkingEditNone}
	case "custom":
		editKind := onboardingThinkingEditPending
		if selections.thinking.kind == onboardingThinkingCustom {
			editKind = onboardingThinkingEditRevisiting
		}
		selections.pendingPrimaryThinking = onboardingThinkingEdit{kind: editKind}
	default:
		fact := modelFactForFacts(facts, selections.model.value)
		if !fact.SupportsThinking || !slices.Contains(fact.SupportedThinkingLevels, choiceID) {
			return conversionError("thinking", choiceID, "unsupported thinking choice")
		}
		selections.thinking = onboardingThinkingSelection{kind: onboardingThinkingLevel, value: choiceID}
		selections.pendingPrimaryThinking = onboardingThinkingEdit{kind: onboardingThinkingEditNone}
	}
	selections.normalizeReviewerThinking(facts)
	return nil
}

func (selections *onboardingSelections) commitPrimaryCustomThinking(value string, facts serverapi.CapabilityFactsResponse) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return conversionError("thinking_custom", value, "must not be blank")
	}
	selections.thinking = onboardingThinkingSelection{kind: onboardingThinkingCustom, value: trimmed}
	selections.pendingPrimaryThinking = onboardingThinkingEdit{kind: onboardingThinkingEditNone}
	selections.normalizeReviewerThinking(facts)
	return nil
}

func (selections *onboardingSelections) chooseVerbosity(choiceID string, facts serverapi.CapabilityFactsResponse) error {
	fact := modelFactForFacts(facts, selections.model.value)
	if !fact.Verbosity.Supported || !slices.Contains(fact.Verbosity.Levels, choiceID) {
		return conversionError("verbosity", choiceID, "unsupported verbosity choice")
	}
	selections.verbosity = onboardingVerbositySelection{kind: onboardingVerbosityLevel, value: choiceID}
	return nil
}

func (selections *onboardingSelections) chooseAskQuestion(choiceID string) error {
	switch choiceID {
	case "yes":
		selections.askQuestion = true
	case "no":
		selections.askQuestion = false
	default:
		return conversionError("ask_question", choiceID, "unsupported choice")
	}
	return nil
}

func (selections *onboardingSelections) chooseSupervisorFrequency(choiceID string, facts serverapi.CapabilityFactsResponse) error {
	switch choiceID {
	case string(onboardingSupervisorOff):
		selections.supervisor.frequency = onboardingSupervisorOff
	case string(onboardingSupervisorEdits):
		selections.supervisor.frequency = onboardingSupervisorEdits
	case string(onboardingSupervisorAll):
		selections.supervisor.frequency = onboardingSupervisorAll
	default:
		return conversionError("reviewer.frequency", choiceID, "unsupported frequency")
	}
	selections.normalizeReviewerThinking(facts)
	return nil
}

func (selections *onboardingSelections) submitReviewerModel(value string, facts serverapi.CapabilityFactsResponse) error {
	model, err := onboardingModelSelectionFromValue(value, facts)
	if err != nil {
		return conversionError("reviewer.model", value, err.Error())
	}
	currentValue := selections.reviewerModelValue()
	switch {
	case model.value == currentValue && selections.supervisor.model.kind == onboardingReviewerModelOverridden:
		// Ordinary traversal of a pre-populated override preserves its provenance.
	case model.value == selections.model.value:
		selections.supervisor.model = onboardingReviewerModelSelection{kind: onboardingReviewerModelInherited}
	default:
		selections.supervisor.model = onboardingReviewerModelSelection{
			kind:     onboardingReviewerModelOverridden,
			override: model,
		}
	}
	selections.normalizeReviewerThinking(facts)
	return nil
}

func (selections *onboardingSelections) chooseReviewerThinking(choiceID string, facts serverapi.CapabilityFactsResponse) error {
	switch choiceID {
	case "disable":
		selections.supervisor.thinking = onboardingReviewerThinkingSelection{
			kind:     onboardingReviewerThinkingOverridden,
			override: onboardingThinkingSelection{kind: onboardingThinkingDisabled},
		}
		selections.pendingReviewerThinking = onboardingThinkingEdit{kind: onboardingThinkingEditNone}
	case "custom":
		editKind := onboardingThinkingEditPending
		if selections.supervisor.thinking.kind == onboardingReviewerThinkingOverridden &&
			selections.supervisor.thinking.override.kind == onboardingThinkingCustom {
			editKind = onboardingThinkingEditRevisiting
		}
		selections.pendingReviewerThinking = onboardingThinkingEdit{kind: editKind}
	default:
		fact := modelFactForFacts(facts, selections.reviewerModelValue())
		if !fact.SupportsThinking || !slices.Contains(fact.SupportedThinkingLevels, choiceID) {
			return conversionError("reviewer.thinking", choiceID, "unsupported thinking choice")
		}
		currentValue := selections.reviewerThinkingValue()
		switch {
		case choiceID == currentValue && selections.supervisor.thinking.kind == onboardingReviewerThinkingOverridden:
			// Ordinary traversal of a pre-populated override preserves its provenance.
		case choiceID == selections.thinkingValue():
			selections.supervisor.thinking = onboardingReviewerThinkingSelection{kind: onboardingReviewerThinkingInherited}
		default:
			selections.supervisor.thinking = onboardingReviewerThinkingSelection{
				kind:     onboardingReviewerThinkingOverridden,
				override: onboardingThinkingSelection{kind: onboardingThinkingLevel, value: choiceID},
			}
		}
		selections.pendingReviewerThinking = onboardingThinkingEdit{kind: onboardingThinkingEditNone}
	}
	return nil
}

func (selections *onboardingSelections) commitReviewerCustomThinking(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return conversionError("reviewer.thinking_custom", value, "must not be blank")
	}
	currentValue := selections.reviewerThinkingValue()
	switch {
	case trimmed == currentValue &&
		selections.pendingReviewerThinking.kind == onboardingThinkingEditRevisiting &&
		selections.supervisor.thinking.kind == onboardingReviewerThinkingOverridden:
		// Re-submitting an existing custom override keeps it explicit.
	case trimmed == selections.thinkingValue():
		selections.supervisor.thinking = onboardingReviewerThinkingSelection{kind: onboardingReviewerThinkingInherited}
	default:
		selections.supervisor.thinking = onboardingReviewerThinkingSelection{
			kind:     onboardingReviewerThinkingOverridden,
			override: onboardingThinkingSelection{kind: onboardingThinkingCustom, value: trimmed},
		}
	}
	selections.pendingReviewerThinking = onboardingThinkingEdit{kind: onboardingThinkingEditNone}
	return nil
}

func (selections *onboardingSelections) chooseCompaction(choiceID string) error {
	switch config.CompactionMode(choiceID) {
	case config.CompactionModeLocal:
		selections.compaction = onboardingCompactionLocal
	case config.CompactionModeNative:
		selections.compaction = onboardingCompactionNative
	case config.CompactionModeNone:
		selections.compaction = onboardingCompactionManualOnly
	default:
		return conversionError("compaction", choiceID, "unsupported compaction choice")
	}
	return nil
}

func (selections *onboardingSelections) chooseSkillEnablement(
	selection map[string]bool,
	candidates []onboardingSkillImportItem,
) error {
	next := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		enabled, ok := selection[candidate.ID]
		if !ok {
			return conversionError("skills_enabled", candidate.ID, "selection is missing a candidate")
		}
		next[candidate.ID] = enabled
	}
	selections.skillEnablement = next
	return nil
}

func (selections *onboardingSelections) normalizeReviewerThinking(facts serverapi.CapabilityFactsResponse) {
	reviewerModel := selections.reviewerModelValue()
	fact := modelFactForFacts(facts, reviewerModel)
	if !fact.SupportsThinking {
		selections.supervisor.thinking = onboardingReviewerThinkingSelection{
			kind:     onboardingReviewerThinkingOverridden,
			override: onboardingThinkingSelection{kind: onboardingThinkingDisabled},
		}
		selections.pendingReviewerThinking = onboardingThinkingEdit{kind: onboardingThinkingEditNone}
		return
	}
	if selections.supervisor.thinking.kind == onboardingReviewerThinkingOverridden &&
		selections.supervisor.thinking.override.kind == onboardingThinkingLevel &&
		!slices.Contains(fact.SupportedThinkingLevels, selections.supervisor.thinking.override.value) {
		selections.supervisor.thinking.override.kind = onboardingThinkingCustom
	}
}

func (edit onboardingThinkingEdit) pending() bool {
	return edit.kind == onboardingThinkingEditPending || edit.kind == onboardingThinkingEditRevisiting
}

func (selections onboardingSelections) primaryCustomInputValue() string {
	if selections.pendingPrimaryThinking.kind != onboardingThinkingEditPending &&
		selections.thinking.kind == onboardingThinkingCustom {
		return selections.thinking.value
	}
	return ""
}

func (selections onboardingSelections) reviewerCustomInputValue() string {
	if selections.pendingReviewerThinking.kind != onboardingThinkingEditPending &&
		selections.supervisor.thinking.kind == onboardingReviewerThinkingOverridden &&
		selections.supervisor.thinking.override.kind == onboardingThinkingCustom {
		return selections.supervisor.thinking.override.value
	}
	return ""
}

func initialSkillEnablement(state *onboardingFlowState) map[string]bool {
	items := skillSelectionCandidates(state)
	selection := make(map[string]bool, len(items))
	for _, item := range items {
		selection[item.ID] = item.DefaultEnabled
	}
	return selection
}

func (state *onboardingFlowState) validateInvariant(operation string, stepID onboardingStepID) error {
	if violation, ok := state.selections.invariantViolation(); ok {
		return state.handleInvariantViolation(operation, stepID, violation)
	}
	for _, item := range skillSelectionCandidates(state) {
		if _, ok := state.selections.skillEnablement[item.ID]; !ok {
			return state.handleInvariantViolation(operation, stepID, onboardingInvariantViolation{
				VariantType: "skill_enablement",
				VariantTag:  item.ID,
			})
		}
	}
	return nil
}

func (state *onboardingFlowState) handleInvariantViolation(
	operation string,
	stepID onboardingStepID,
	violation onboardingInvariantViolation,
) error {
	diagnostic := onboardingInvariantDiagnostic{
		Operation:           operation,
		StepID:              string(stepID),
		ModelIdentity:       state.selections.model.value,
		VariantType:         violation.VariantType,
		VariantTag:          violation.VariantTag,
		PendingPrimaryEdit:  state.selections.pendingPrimaryThinking.kind,
		PendingReviewerEdit: state.selections.pendingReviewerThinking.kind,
		PendingAction:       state.pendingAction,
		Stack:               string(debug.Stack()),
	}
	if state.debug {
		panic(diagnostic)
	}
	return &onboardingInternalStateError{Diagnostic: diagnostic}
}

func (selections onboardingSelections) invariantViolation() (onboardingInvariantViolation, bool) {
	valid := func(value string, allowed ...string) bool {
		for _, candidate := range allowed {
			if value == candidate {
				return true
			}
		}
		return false
	}
	checks := []struct {
		name    string
		value   string
		allowed []string
	}{
		{"theme", string(selections.theme.kind), []string{"auto", "light", "dark"}},
		{"model", string(selections.model.kind), []string{"known", "custom"}},
		{"context_window", string(selections.contextWindow.kind), []string{"default", "large", "custom"}},
		{"thinking", string(selections.thinking.kind), []string{"default", "disabled", "level", "custom"}},
		{"verbosity", string(selections.verbosity.kind), []string{"omitted", "level"}},
		{"supervisor.frequency", string(selections.supervisor.frequency), []string{"off", "edits", "all"}},
		{"supervisor.model", string(selections.supervisor.model.kind), []string{"inherited", "overridden"}},
		{"supervisor.thinking", string(selections.supervisor.thinking.kind), []string{"inherited", "overridden"}},
		{"compaction", string(selections.compaction), []string{"local", "native", "manual_only"}},
		{"skill_import", string(selections.skillImport.Mode), []string{"none", "symlink_source"}},
		{"pending_primary_thinking", string(selections.pendingPrimaryThinking.kind), []string{"none", "pending", "revisiting"}},
		{"pending_reviewer_thinking", string(selections.pendingReviewerThinking.kind), []string{"none", "pending", "revisiting"}},
	}
	for _, check := range checks {
		if !valid(check.value, check.allowed...) {
			return onboardingInvariantViolation{VariantType: check.name, VariantTag: check.value}, true
		}
	}
	if strings.TrimSpace(selections.model.value) == "" {
		return onboardingInvariantViolation{VariantType: "model.value", VariantTag: selections.model.value}, true
	}
	if selections.contextWindow.kind == onboardingContextCustom && selections.contextWindow.tokens <= 0 {
		return onboardingInvariantViolation{VariantType: "context_window.tokens", VariantTag: fmt.Sprint(selections.contextWindow.tokens)}, true
	}
	if selections.thinking.requiresValue() && strings.TrimSpace(selections.thinking.value) == "" {
		return onboardingInvariantViolation{VariantType: "thinking.value", VariantTag: selections.thinking.value}, true
	}
	if selections.verbosity.kind == onboardingVerbosityLevel && strings.TrimSpace(selections.verbosity.value) == "" {
		return onboardingInvariantViolation{VariantType: "verbosity.value", VariantTag: selections.verbosity.value}, true
	}
	if selections.supervisor.model.kind == onboardingReviewerModelOverridden {
		if violation, ok := selections.supervisor.model.override.invariantViolation("supervisor.model.override"); ok {
			return violation, true
		}
	}
	if selections.supervisor.thinking.kind == onboardingReviewerThinkingOverridden {
		if violation, ok := selections.supervisor.thinking.override.invariantViolation("supervisor.thinking.override"); ok {
			return violation, true
		}
	}
	if selections.skillImport.Mode == onboardingImportModeSymlinkSource {
		ref := selections.skillImport.ChoiceRef
		if onboardingImportMode(ref.Mode) != onboardingImportModeSymlinkSource {
			return onboardingInvariantViolation{VariantType: "skill_import.choice_ref.mode", VariantTag: ref.Mode}, true
		}
		if ref.ImportProviderID == nil || strings.TrimSpace(*ref.ImportProviderID) == "" {
			return onboardingInvariantViolation{VariantType: "skill_import.choice_ref.import_provider_id", VariantTag: fmt.Sprint(ref.ImportProviderID)}, true
		}
		if ref.SourceRootPath == nil || strings.TrimSpace(*ref.SourceRootPath) == "" {
			return onboardingInvariantViolation{VariantType: "skill_import.choice_ref.source_root_path", VariantTag: fmt.Sprint(ref.SourceRootPath)}, true
		}
	}
	if selections.preserved.providerOverride != nil && strings.TrimSpace(*selections.preserved.providerOverride) == "" {
		return onboardingInvariantViolation{VariantType: "preserved.provider_override", VariantTag: *selections.preserved.providerOverride}, true
	}
	if selections.preserved.openAIBaseURL != nil && strings.TrimSpace(*selections.preserved.openAIBaseURL) == "" {
		return onboardingInvariantViolation{VariantType: "preserved.openai_base_url", VariantTag: *selections.preserved.openAIBaseURL}, true
	}
	if selections.preserved.modelTimeoutSeconds != nil && *selections.preserved.modelTimeoutSeconds <= 0 {
		return onboardingInvariantViolation{VariantType: "preserved.model_timeout_seconds", VariantTag: fmt.Sprint(*selections.preserved.modelTimeoutSeconds)}, true
	}
	if selections.preserved.baselineModelContextWindow != nil && *selections.preserved.baselineModelContextWindow <= 0 {
		return onboardingInvariantViolation{VariantType: "preserved.baseline_model_context_window", VariantTag: fmt.Sprint(*selections.preserved.baselineModelContextWindow)}, true
	}
	for _, id := range toolspec.CatalogIDs() {
		if _, ok := selections.preserved.enabledTools[id]; !ok {
			return onboardingInvariantViolation{VariantType: "preserved.enabled_tools", VariantTag: string(id)}, true
		}
	}
	return onboardingInvariantViolation{}, false
}

func (selection onboardingModelSelection) invariantViolation(prefix string) (onboardingInvariantViolation, bool) {
	if selection.kind != onboardingModelKnown && selection.kind != onboardingModelCustom {
		return onboardingInvariantViolation{VariantType: prefix, VariantTag: string(selection.kind)}, true
	}
	if strings.TrimSpace(selection.value) == "" {
		return onboardingInvariantViolation{VariantType: prefix + ".value", VariantTag: selection.value}, true
	}
	return onboardingInvariantViolation{}, false
}

func (selection onboardingThinkingSelection) invariantViolation(prefix string) (onboardingInvariantViolation, bool) {
	switch selection.kind {
	case onboardingThinkingDefault, onboardingThinkingDisabled:
		return onboardingInvariantViolation{}, false
	case onboardingThinkingLevel, onboardingThinkingCustom:
		if strings.TrimSpace(selection.value) == "" {
			return onboardingInvariantViolation{VariantType: prefix + ".value", VariantTag: selection.value}, true
		}
		return onboardingInvariantViolation{}, false
	default:
		return onboardingInvariantViolation{VariantType: prefix, VariantTag: string(selection.kind)}, true
	}
}

func (selection onboardingThinkingSelection) requiresValue() bool {
	return selection.kind == onboardingThinkingLevel || selection.kind == onboardingThinkingCustom
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
