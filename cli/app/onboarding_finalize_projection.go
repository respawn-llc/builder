package app

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/toolspec"
)

func onboardingFinalizeRequest(state onboardingFlowState, defaults bool) (serverapi.OnboardingFinalizeRequest, error) {
	theme, err := onboardingTheme(state.settings.Theme)
	if err != nil {
		return serverapi.OnboardingFinalizeRequest{}, err
	}
	req := serverapi.OnboardingFinalizeRequest{
		Theme:          &theme,
		CommandsImport: onboardingNoneImportSelection(),
	}
	if !defaults {
		mainProvider := onboardingMainProviderChoice(state.settings)
		model, err := onboardingModelChoice(state, state.settings.Model)
		if err != nil {
			return serverapi.OnboardingFinalizeRequest{}, err
		}
		contextWindow, err := onboardingContextWindowChoice(state)
		if err != nil {
			return serverapi.OnboardingFinalizeRequest{}, err
		}
		thinking, err := onboardingPrimaryThinkingChoice(state)
		if err != nil {
			return serverapi.OnboardingFinalizeRequest{}, err
		}
		supervisor, err := onboardingSupervisorChoice(state)
		if err != nil {
			return serverapi.OnboardingFinalizeRequest{}, err
		}
		compaction, err := onboardingCompaction(state.settings.CompactionMode)
		if err != nil {
			return serverapi.OnboardingFinalizeRequest{}, err
		}
		skillsImport, err := onboardingImportSelectionRequest(state.skillImport)
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
		askQuestion := state.settings.EnabledTools[toolspec.ToolAskQuestion]
		req.AskQuestion = &askQuestion
		req.ToolOverrides = onboardingToolOverrides(state.settings.EnabledTools)
		if state.settings.ModelVerbosity != "" {
			verbosity := serverapi.OnboardingVerbosity(state.settings.ModelVerbosity)
			req.Verbosity = &verbosity
		}
		req.DisabledSkillNames = disabledOnboardingSkillNames(state)
	}
	if err := serverapi.ValidateOnboardingFinalizeRequest(req); err != nil {
		return serverapi.OnboardingFinalizeRequest{}, err
	}
	return req, nil
}

func onboardingMainProviderChoice(settings config.Settings) *serverapi.OnboardingProviderChoice {
	providerOverride := strings.TrimSpace(settings.ProviderOverride)
	openAIBaseURL := strings.TrimSpace(settings.OpenAIBaseURL)
	if providerOverride == "" && openAIBaseURL == "" {
		return nil
	}
	choice := serverapi.OnboardingProviderChoice{}
	if providerOverride != "" {
		choice.ProviderOverride = &providerOverride
	}
	if openAIBaseURL != "" {
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
		enabled, configured := enabledTools[id]
		if !configured {
			enabled = defaults[id]
		}
		if enabled != defaults[id] {
			overrides = append(overrides, serverapi.OnboardingToolOverride{ID: id, Enabled: enabled})
		}
	}
	if len(overrides) == 0 {
		return nil
	}
	return overrides
}

func onboardingTheme(value string) (serverapi.OnboardingTheme, error) {
	switch strings.TrimSpace(value) {
	case "", "auto":
		return serverapi.OnboardingThemeAuto, nil
	case "light":
		return serverapi.OnboardingThemeLight, nil
	case "dark":
		return serverapi.OnboardingThemeDark, nil
	default:
		return "", fmt.Errorf("unsupported onboarding theme %q", value)
	}
}

func onboardingModelChoice(state onboardingFlowState, value string) (serverapi.OnboardingModelChoice, error) {
	model := strings.TrimSpace(value)
	if model == "" {
		return serverapi.OnboardingModelChoice{}, fmt.Errorf("onboarding model is required")
	}
	fact := modelFactFor(&state, model)
	if fact.Known {
		return serverapi.OnboardingModelChoice{Kind: serverapi.OnboardingModelKnown, ModelID: model}, nil
	}
	return serverapi.OnboardingModelChoice{Kind: serverapi.OnboardingModelCustom, Alias: model}, nil
}

func onboardingContextWindowChoice(state onboardingFlowState) (serverapi.OnboardingContextWindowChoice, error) {
	fact := modelFactFor(&state, state.settings.Model)
	selected := state.settings.ModelContextWindow
	switch {
	case fact.ContextWindowTokens != nil && selected == *fact.ContextWindowTokens:
		return serverapi.OnboardingContextWindowChoice{Kind: serverapi.OnboardingContextWindowDefault}, nil
	case fact.LargeWindow != nil && selected == fact.LargeWindow.Tokens:
		return serverapi.OnboardingContextWindowChoice{Kind: serverapi.OnboardingContextWindowLarge}, nil
	case selected > 0:
		return serverapi.OnboardingContextWindowChoice{Kind: serverapi.OnboardingContextWindowCustom, Tokens: selected}, nil
	default:
		return serverapi.OnboardingContextWindowChoice{Kind: serverapi.OnboardingContextWindowDefault}, nil
	}
}

func onboardingThinkingChoice(value string, custom bool) (serverapi.OnboardingThinkingChoice, error) {
	thinking := strings.TrimSpace(value)
	switch {
	case thinking == "":
		return serverapi.OnboardingThinkingChoice{Kind: serverapi.OnboardingThinkingDisabled}, nil
	case custom:
		return serverapi.OnboardingThinkingChoice{Kind: serverapi.OnboardingThinkingCustom, Value: thinking}, nil
	default:
		return serverapi.OnboardingThinkingChoice{Kind: serverapi.OnboardingThinkingLevel, Level: thinking}, nil
	}
}

func onboardingPrimaryThinkingChoice(state onboardingFlowState) (serverapi.OnboardingThinkingChoice, error) {
	if !state.customThinking && strings.TrimSpace(state.settings.ThinkingLevel) == strings.TrimSpace(config.DefaultOnboardingSettings().ThinkingLevel) {
		return serverapi.OnboardingThinkingChoice{Kind: serverapi.OnboardingThinkingDefault}, nil
	}
	return onboardingThinkingChoice(state.settings.ThinkingLevel, state.customThinking)
}

func onboardingSupervisorChoice(state onboardingFlowState) (serverapi.OnboardingSupervisorChoice, error) {
	frequency := strings.TrimSpace(state.settings.Reviewer.Frequency)
	switch frequency {
	case "", "off":
		return serverapi.OnboardingSupervisorChoice{Frequency: serverapi.OnboardingSupervisorOff}, nil
	case "edits", "all":
	default:
		return serverapi.OnboardingSupervisorChoice{}, fmt.Errorf("unsupported reviewer frequency %q", frequency)
	}
	result := serverapi.OnboardingSupervisorChoice{Frequency: serverapi.OnboardingSupervisorFrequency(frequency)}
	if state.reviewerCustomModel {
		model, err := onboardingModelChoice(state, state.settings.Reviewer.Model)
		if err != nil {
			return serverapi.OnboardingSupervisorChoice{}, err
		}
		result.Model = &model
	}
	if state.reviewerThinkingDisabled {
		thinking := serverapi.OnboardingThinkingChoice{Kind: serverapi.OnboardingThinkingDisabled}
		result.Thinking = &thinking
	} else if state.reviewerCustomThinking {
		thinking, err := onboardingThinkingChoice(state.settings.Reviewer.ThinkingLevel, state.reviewerCustomThinkingInput)
		if err != nil {
			return serverapi.OnboardingSupervisorChoice{}, err
		}
		result.Thinking = &thinking
	}
	return result, nil
}

func onboardingCompaction(value config.CompactionMode) (serverapi.OnboardingCompactionMode, error) {
	switch value {
	case config.CompactionModeLocal:
		return serverapi.OnboardingCompactionLocal, nil
	case config.CompactionModeNative:
		return serverapi.OnboardingCompactionNative, nil
	case config.CompactionModeNone:
		return serverapi.OnboardingCompactionNone, nil
	default:
		return "", fmt.Errorf("unsupported compaction mode %q", value)
	}
}

func onboardingNoneImportSelection() *serverapi.OnboardingImportSelection {
	return &serverapi.OnboardingImportSelection{Mode: serverapi.OnboardingImportModeNone}
}

func onboardingImportSelectionRequest(selection onboardingImportSelection) (*serverapi.OnboardingImportSelection, error) {
	selection = normalizeImportSelection(selection)
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
	for _, item := range skillSelectionCandidates(&state) {
		if effectiveSkillSelection(&state)[item.ID] {
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
