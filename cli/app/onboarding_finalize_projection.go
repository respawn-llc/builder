package app

import (
	"errors"
	"fmt"
	"sort"

	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/toolspec"
)

func onboardingFinalizeRequest(state onboardingFlowState, defaults bool) (serverapi.OnboardingFinalizeRequest, error) {
	if err := state.validateInvariant("finalize_projection", "review"); err != nil {
		return serverapi.OnboardingFinalizeRequest{}, err
	}
	theme := serverapi.OnboardingTheme(state.selections.theme.kind)
	req := serverapi.OnboardingFinalizeRequest{
		Theme:          &theme,
		CommandsImport: onboardingNoneImportSelection(),
	}
	if !defaults {
		if state.selections.pendingPrimaryThinking.pending() ||
			state.selections.pendingReviewerThinking.pending() {
			return serverapi.OnboardingFinalizeRequest{}, errors.New("custom thinking input must be committed before finishing setup")
		}
		mainProvider := onboardingMainProviderChoice(state.selections.preserved)
		model := onboardingModelChoice(state.selections.model)
		contextWindow := onboardingContextWindowChoice(state.selections.contextWindow)
		thinking := onboardingThinkingChoice(state.selections.thinking)
		supervisor := onboardingSupervisorChoice(state.selections.supervisor)
		compaction := serverapi.OnboardingCompactionMode(state.selections.compactionValue())
		skillsImport, err := onboardingImportSelectionRequest(state.selections.skillImport)
		if err != nil {
			return serverapi.OnboardingFinalizeRequest{}, err
		}
		req.Model = &model
		req.MainProvider = mainProvider
		req.ContextWindow = &contextWindow
		req.Thinking = &thinking
		req.Supervisor = &supervisor
		req.Compaction = &compaction
		req.SkillsImport = skillsImport
		askQuestion := state.selections.askQuestion
		req.AskQuestion = &askQuestion
		req.ToolOverrides = onboardingToolOverrides(state.selections.preserved.enabledTools)
		req.ModelTimeoutSeconds = state.selections.preserved.modelTimeoutSeconds
		if state.selections.verbosity.kind == onboardingVerbosityLevel {
			verbosity := serverapi.OnboardingVerbosity(state.selections.verbosity.value)
			req.Verbosity = &verbosity
		}
		req.DisabledSkillNames = disabledOnboardingSkillNames(state)
	}
	if err := serverapi.ValidateOnboardingFinalizeRequest(req); err != nil {
		return serverapi.OnboardingFinalizeRequest{}, err
	}
	return req, nil
}

func onboardingMainProviderChoice(preserved onboardingPreservedInputs) *serverapi.OnboardingProviderChoice {
	if preserved.providerOverride == nil && preserved.openAIBaseURL == nil {
		return nil
	}
	choice := serverapi.OnboardingProviderChoice{}
	if preserved.providerOverride != nil {
		providerOverride := *preserved.providerOverride
		choice.ProviderOverride = &providerOverride
	}
	if preserved.openAIBaseURL != nil {
		openAIBaseURL := *preserved.openAIBaseURL
		choice.OpenAIBaseURL = &openAIBaseURL
	}
	return &choice
}

func onboardingToolOverrides(enabledTools map[toolspec.ID]bool) []serverapi.OnboardingToolOverride {
	defaults := config.DefaultOnboardingSettings().EnabledTools
	overrides := make([]serverapi.OnboardingToolOverride, 0)
	for _, id := range toolspec.CatalogIDs() {
		if id == toolspec.ToolAskQuestion {
			continue
		}
		enabled := enabledTools[id]
		if enabled != defaults[id] {
			overrides = append(overrides, serverapi.OnboardingToolOverride{ID: id, Enabled: enabled})
		}
	}
	if len(overrides) == 0 {
		return nil
	}
	return overrides
}

func onboardingModelChoice(selection onboardingModelSelection) serverapi.OnboardingModelChoice {
	if selection.kind == onboardingModelKnown {
		return serverapi.OnboardingModelChoice{Kind: serverapi.OnboardingModelKnown, ModelID: selection.value}
	}
	return serverapi.OnboardingModelChoice{Kind: serverapi.OnboardingModelCustom, Alias: selection.value}
}

func onboardingContextWindowChoice(selection onboardingContextSelection) serverapi.OnboardingContextWindowChoice {
	switch selection.kind {
	case onboardingContextLarge:
		return serverapi.OnboardingContextWindowChoice{Kind: serverapi.OnboardingContextWindowLarge}
	case onboardingContextCustom:
		return serverapi.OnboardingContextWindowChoice{Kind: serverapi.OnboardingContextWindowCustom, Tokens: selection.tokens}
	default:
		return serverapi.OnboardingContextWindowChoice{Kind: serverapi.OnboardingContextWindowDefault}
	}
}

func onboardingThinkingChoice(selection onboardingThinkingSelection) serverapi.OnboardingThinkingChoice {
	switch selection.kind {
	case onboardingThinkingDefault:
		return serverapi.OnboardingThinkingChoice{Kind: serverapi.OnboardingThinkingDefault}
	case onboardingThinkingDisabled:
		return serverapi.OnboardingThinkingChoice{Kind: serverapi.OnboardingThinkingDisabled}
	case onboardingThinkingCustom:
		return serverapi.OnboardingThinkingChoice{Kind: serverapi.OnboardingThinkingCustom, Value: selection.value}
	default:
		return serverapi.OnboardingThinkingChoice{Kind: serverapi.OnboardingThinkingLevel, Level: selection.value}
	}
}

func onboardingSupervisorChoice(selection onboardingSupervisorSelection) serverapi.OnboardingSupervisorChoice {
	result := serverapi.OnboardingSupervisorChoice{Frequency: serverapi.OnboardingSupervisorFrequency(selection.frequency)}
	if selection.frequency == onboardingSupervisorOff {
		return result
	}
	if selection.model.kind == onboardingReviewerModelOverridden {
		model := onboardingModelChoice(selection.model.override)
		result.Model = &model
	}
	if selection.thinking.kind == onboardingReviewerThinkingOverridden {
		thinking := onboardingThinkingChoice(selection.thinking.override)
		result.Thinking = &thinking
	} else if selection.thinking.kind == onboardingReviewerThinkingCapabilityDisabled {
		thinking := onboardingThinkingChoice(onboardingThinkingSelection{kind: onboardingThinkingDisabled})
		result.Thinking = &thinking
	}
	return result
}

func onboardingNoneImportSelection() *serverapi.OnboardingImportSelection {
	return &serverapi.OnboardingImportSelection{Mode: serverapi.OnboardingImportModeNone}
}

func onboardingImportSelectionRequest(selection onboardingImportSelection) (*serverapi.OnboardingImportSelection, error) {
	switch selection.Mode {
	case onboardingImportModeNone:
		return onboardingNoneImportSelection(), nil
	case onboardingImportModeSymlinkSource:
		ref := selection.ChoiceRef
		if ref.ImportProviderID == nil || ref.SourceRootPath == nil {
			return nil, errors.New("selected skill import is missing its server choice reference")
		}
		return &serverapi.OnboardingImportSelection{
			Mode:             serverapi.OnboardingImportModeSymlinkSource,
			ImportProviderID: ref.ImportProviderID,
			SourceRootPath:   ref.SourceRootPath,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported skill import mode %q", selection.Mode)
	}
}

func disabledOnboardingSkillNames(state onboardingFlowState) []string {
	disabled := map[string]struct{}{}
	selected := effectiveSkillSelection(&state)
	for _, item := range skillSelectionCandidates(&state) {
		if selected[item.ID] {
			continue
		}
		if name := config.NormalizeSkillName(item.SkillName); name != "" {
			disabled[name] = struct{}{}
		}
	}
	if len(disabled) == 0 {
		return nil
	}
	names := make([]string, 0, len(disabled))
	for name := range disabled {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
