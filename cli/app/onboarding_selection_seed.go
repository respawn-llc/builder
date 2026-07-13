package app

import (
	"fmt"
	"slices"
	"strings"

	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/theme"
	"core/shared/toolspec"
)

type onboardingSelectionConversionError struct {
	Field  string
	Value  any
	Reason string
}

func (e *onboardingSelectionConversionError) Error() string {
	return fmt.Sprintf("convert onboarding selection %s (%v): %s", e.Field, e.Value, e.Reason)
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
	state.recomputeSkillEnablement(&state.selections)
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
