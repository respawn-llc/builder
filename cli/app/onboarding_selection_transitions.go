package app

import (
	"maps"
	"slices"
	"strings"

	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/theme"
)

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
		state.recomputeSkillEnablement(selections)
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
		state.recomputeSkillEnablement(selections)
		return nil
	})
}

func (state *onboardingFlowState) recomputeSkillEnablement(selections *onboardingSelections) {
	candidate := *state
	candidate.selections = *selections
	selections.skillEnablement = initialSkillEnablement(&candidate)
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
