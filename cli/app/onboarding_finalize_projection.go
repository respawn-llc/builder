package app

import (
	"errors"
	"fmt"
	"sort"

	"core/shared/config"
	onboardingpb "core/shared/protoapi/gen/kent/api/onboarding"
	"core/shared/toolspec"
)

func onboardingFinalizeRequest(state onboardingFlowState, defaults bool) (*onboardingpb.FinalizeRequest, error) {
	if err := state.validateInvariant("finalize_projection", "review"); err != nil {
		return nil, err
	}
	theme := onboardingThemeToProto(state.selections.theme.kind)
	req := &onboardingpb.FinalizeRequest{
		Theme:          &theme,
		CommandsImport: onboardingNoneImportSelection(),
	}
	if !defaults {
		if state.selections.pendingPrimaryThinking.pending() ||
			state.selections.pendingReviewerThinking.pending() {
			return nil, errors.New("custom thinking input must be committed before finishing setup")
		}
		mainProvider := onboardingMainProviderChoice(state.selections.preserved)
		model := onboardingModelChoice(state.selections.model)
		contextWindow := onboardingContextWindowChoice(state.selections.contextWindow)
		thinking := onboardingThinkingChoice(state.selections.thinking)
		supervisor := onboardingSupervisorChoice(state.selections.supervisor)
		compaction := onboardingCompactionToProto(state.selections.compactionValue())
		skillsImport, err := onboardingImportSelectionRequest(state.selections.skillImport)
		if err != nil {
			return nil, err
		}
		req.Model = &model
		req.MainProvider = mainProvider
		req.ContextWindow = &contextWindow
		req.Thinking = &thinking
		req.Supervisor = &supervisor
		req.Compaction = &compaction
		req.SkillsImport = skillsImport
		commandsImport, err := onboardingImportSelectionRequest(state.selections.commandImport)
		if err != nil {
			return nil, err
		}
		req.CommandsImport = commandsImport
		askQuestion := state.selections.askQuestion
		req.AskQuestion = &askQuestion
		req.ToolOverrides = onboardingToolOverrides(state.selections.preserved.enabledTools)
		if state.selections.preserved.modelTimeoutSeconds != nil {
			timeout := uint32(*state.selections.preserved.modelTimeoutSeconds)
			req.ModelTimeoutSeconds = &timeout
		}
		if state.selections.verbosity.kind == onboardingVerbosityLevel {
			verbosity := onboardingVerbosityToProto(state.selections.verbosity.value)
			req.Verbosity = &verbosity
		}
		req.DisabledSkillNames = disabledOnboardingSkillNames(state)
	}
	return req, nil
}

func onboardingMainProviderChoice(preserved onboardingPreservedInputs) *onboardingpb.ProviderChoice {
	if preserved.providerOverride == nil && preserved.openAIBaseURL == nil {
		return nil
	}
	choice := onboardingpb.ProviderChoice{}
	if preserved.providerOverride != nil {
		providerOverride := *preserved.providerOverride
		choice.ProviderOverride = &providerOverride
	}
	if preserved.openAIBaseURL != nil {
		openAIBaseURL := *preserved.openAIBaseURL
		choice.OpenaiBaseUrl = &openAIBaseURL
	}
	return &choice
}

func onboardingToolOverrides(enabledTools map[toolspec.ID]bool) []*onboardingpb.ToolOverride {
	defaults := config.DefaultOnboardingSettings().EnabledTools
	overrides := make([]*onboardingpb.ToolOverride, 0)
	for _, id := range toolspec.CatalogIDs() {
		if id == toolspec.ToolAskQuestion {
			continue
		}
		enabled := enabledTools[id]
		if enabled != defaults[id] {
			overrides = append(overrides, &onboardingpb.ToolOverride{Id: onboardingToolIDToProto(id), Enabled: enabled})
		}
	}
	if len(overrides) == 0 {
		return nil
	}
	return overrides
}

func onboardingModelChoice(selection onboardingModelSelection) onboardingpb.ModelChoice {
	if selection.kind == onboardingModelKnown {
		return onboardingpb.ModelChoice{Kind: onboardingpb.ModelKind_MODEL_KIND_KNOWN, ModelId: &selection.value}
	}
	return onboardingpb.ModelChoice{Kind: onboardingpb.ModelKind_MODEL_KIND_CUSTOM, Alias: &selection.value}
}

func onboardingContextWindowChoice(selection onboardingContextSelection) onboardingpb.ContextWindowChoice {
	switch selection.kind {
	case onboardingContextLarge:
		return onboardingpb.ContextWindowChoice{Kind: onboardingpb.ContextWindowKind_CONTEXT_WINDOW_KIND_LARGE}
	case onboardingContextCustom:
		tokens := uint32(selection.tokens)
		return onboardingpb.ContextWindowChoice{Kind: onboardingpb.ContextWindowKind_CONTEXT_WINDOW_KIND_CUSTOM, Tokens: &tokens}
	default:
		return onboardingpb.ContextWindowChoice{Kind: onboardingpb.ContextWindowKind_CONTEXT_WINDOW_KIND_DEFAULT}
	}
}

func onboardingThinkingChoice(selection onboardingThinkingSelection) onboardingpb.ThinkingChoice {
	switch selection.kind {
	case onboardingThinkingDefault:
		return onboardingpb.ThinkingChoice{Kind: onboardingpb.ThinkingKind_THINKING_KIND_DEFAULT}
	case onboardingThinkingDisabled:
		return onboardingpb.ThinkingChoice{Kind: onboardingpb.ThinkingKind_THINKING_KIND_DISABLED}
	case onboardingThinkingCustom:
		return onboardingpb.ThinkingChoice{Kind: onboardingpb.ThinkingKind_THINKING_KIND_CUSTOM, Value: &selection.value}
	default:
		return onboardingpb.ThinkingChoice{Kind: onboardingpb.ThinkingKind_THINKING_KIND_LEVEL, Level: &selection.value}
	}
}

func onboardingSupervisorChoice(selection onboardingSupervisorSelection) onboardingpb.SupervisorChoice {
	result := onboardingpb.SupervisorChoice{Frequency: onboardingSupervisorFrequencyToProto(selection.frequency)}
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

func onboardingNoneImportSelection() *onboardingpb.ImportSelection {
	return &onboardingpb.ImportSelection{Mode: onboardingpb.ImportMode_IMPORT_MODE_NONE}
}

func onboardingImportSelectionRequest(selection onboardingImportSelection) (*onboardingpb.ImportSelection, error) {
	switch selection.Mode {
	case onboardingImportModeNone:
		return onboardingNoneImportSelection(), nil
	case onboardingImportModeSymlinkSource:
		ref := selection.ChoiceRef
		if ref.ImportProviderId == nil || ref.SourceRootPath == nil {
			return nil, errors.New("selected import is missing its server choice reference")
		}
		return &onboardingpb.ImportSelection{
			Mode:             onboardingpb.ImportMode_IMPORT_MODE_SYMLINK_SOURCE,
			ImportProviderId: ref.ImportProviderId,
			SourceRootPath:   ref.SourceRootPath,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported skill import mode %q", selection.Mode)
	}
}

func onboardingThemeToProto(theme onboardingThemeKind) onboardingpb.Theme {
	switch theme {
	case onboardingThemeAuto:
		return onboardingpb.Theme_THEME_AUTO
	case onboardingThemeLight:
		return onboardingpb.Theme_THEME_LIGHT
	default:
		return onboardingpb.Theme_THEME_DARK
	}
}

func onboardingCompactionToProto(mode config.CompactionMode) onboardingpb.CompactionMode {
	switch mode {
	case config.CompactionModeNative:
		return onboardingpb.CompactionMode_COMPACTION_MODE_NATIVE
	case config.CompactionModeNone:
		return onboardingpb.CompactionMode_COMPACTION_MODE_NONE
	default:
		return onboardingpb.CompactionMode_COMPACTION_MODE_LOCAL
	}
}

func onboardingVerbosityToProto(value string) onboardingpb.Verbosity {
	switch value {
	case string(config.ModelVerbosityLow):
		return onboardingpb.Verbosity_VERBOSITY_LOW
	case string(config.ModelVerbosityHigh):
		return onboardingpb.Verbosity_VERBOSITY_HIGH
	default:
		return onboardingpb.Verbosity_VERBOSITY_MEDIUM
	}
}

func onboardingSupervisorFrequencyToProto(value onboardingSupervisorFrequency) onboardingpb.SupervisorFrequency {
	switch value {
	case onboardingSupervisorOff:
		return onboardingpb.SupervisorFrequency_SUPERVISOR_FREQUENCY_OFF
	case onboardingSupervisorAll:
		return onboardingpb.SupervisorFrequency_SUPERVISOR_FREQUENCY_ALL
	default:
		return onboardingpb.SupervisorFrequency_SUPERVISOR_FREQUENCY_EDITS
	}
}

func onboardingToolIDToProto(id toolspec.ID) onboardingpb.ToolID {
	switch id {
	case toolspec.ToolExecCommand:
		return onboardingpb.ToolID_TOOL_ID_EXEC_COMMAND
	case toolspec.ToolWriteStdin:
		return onboardingpb.ToolID_TOOL_ID_WRITE_STDIN
	case toolspec.ToolViewImage:
		return onboardingpb.ToolID_TOOL_ID_VIEW_IMAGE
	case toolspec.ToolPatch:
		return onboardingpb.ToolID_TOOL_ID_PATCH
	case toolspec.ToolEdit:
		return onboardingpb.ToolID_TOOL_ID_EDIT
	case toolspec.ToolCompleteNode:
		return onboardingpb.ToolID_TOOL_ID_COMPLETE_NODE
	case toolspec.ToolTriggerHandoff:
		return onboardingpb.ToolID_TOOL_ID_TRIGGER_HANDOFF
	case toolspec.ToolWebSearch:
		return onboardingpb.ToolID_TOOL_ID_WEB_SEARCH
	default:
		return onboardingpb.ToolID_TOOL_ID_UNSPECIFIED
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
