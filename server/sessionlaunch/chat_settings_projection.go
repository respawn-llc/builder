package sessionlaunch

import (
	"errors"
	"slices"
	"strings"

	"core/server/launch"
	"core/server/llm"
	"core/server/session"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/toolspec"
)

type ChatSettingsProjectionInput struct {
	Catalog        launch.PreparedChatAgentCatalog
	Agent          string
	Settings       session.ChatSettings
	WorkflowLocked bool
	CompactionMode config.CompactionMode
	Locked         *session.LockedContract
}

func ProjectChatSettings(input ChatSettingsProjectionInput) (serverapi.ChatSettings, error) {
	cachingLocked := input.Locked != nil
	defaultEntry, _ := input.Catalog.Lookup(config.DefaultSubagentRole)
	selected, ok := input.Catalog.Lookup(input.Agent)
	if !ok {
		if cachingLocked {
			selected = defaultEntry
		} else {
			selected = defaultEntry
			input.Agent = config.DefaultSubagentRole
			input.Settings = defaultEntry.Settings.Baseline
		}
	}
	effective := input.Settings
	selectedRole := selected.Choice.Role
	selectedModel := selected.Choice.Model
	selectedSettings := selected.Settings
	if cachingLocked {
		selectedRole = normalizeWorkspaceChatDraftAgent(input.Agent)
		selectedModel = input.Locked.Model
		lockedSettings, err := lockedPreparedChatSettings(
			*input.Locked,
			selected.Settings,
			effective,
		)
		if err != nil {
			return serverapi.ChatSettings{}, err
		}
		selectedSettings = lockedSettings
	}
	effective = normalizeProjectedChatSettings(effective, selectedSettings)

	agentEditability := serverapi.ChatSettingsEditable
	if input.WorkflowLocked {
		agentEditability = serverapi.ChatSettingsWorkflowLock
	} else if cachingLocked {
		agentEditability = serverapi.ChatSettingsCachingLock
	}
	thinking := projectChatThinking(effective.Thinking, selectedSettings)
	var fast *serverapi.ChatSettingsFast
	if selectedSettings.FastAvailable {
		fast = &serverapi.ChatSettingsFast{
			Value:       effective.Fast,
			Editability: serverapi.ChatSettingsEditable,
		}
	}
	autoCompaction := projectChatAutoCompaction(
		input.CompactionMode,
		effective.AutoCompaction,
		input.WorkflowLocked,
	)
	return serverapi.ChatSettings{
		SelectedAgent: serverapi.ChatSettingsAgentSummary{
			Role:     selectedRole,
			Model:    selectedModel,
			Thinking: effective.Thinking,
		},
		AgentChoices:     input.Catalog.Choices(),
		AgentEditability: agentEditability,
		Supervisor: serverapi.ChatSettingsSupervisor{
			Value:       serverapi.ChatSettingsSupervisorValue(effective.Supervisor),
			Editability: serverapi.ChatSettingsEditable,
		},
		Thinking: thinking,
		Fast:     fast,
		Questions: serverapi.ChatSettingsQuestions{
			Capable:     selectedSettings.QuestionsAvailable,
			Enabled:     effective.Questions,
			Editability: serverapi.ChatSettingsEditable,
		},
		AutoCompaction: autoCompaction,
		AgentLocked:    input.WorkflowLocked || cachingLocked,
		WorkflowLocked: input.WorkflowLocked,
		CachingLocked:  cachingLocked,
	}, nil
}

func lockedPreparedChatSettings(
	locked session.LockedContract,
	fallback launch.PreparedChatSettings,
	effective session.ChatSettings,
) (launch.PreparedChatSettings, error) {
	capabilities, ok := llm.ProviderCapabilitiesFromLocked(&locked)
	if !ok {
		return launch.PreparedChatSettings{}, errors.New(
			"caching-locked Chat provider contract is required",
		)
	}
	fallback.SupportedThinkingValues = nil
	if llm.LockedContractSupportsReasoningEffort(&locked, locked.Model) {
		fallback.SupportedThinkingValues = launch.SupportedChatThinkingValues(
			locked.Model,
			effective.Thinking,
		)
	}
	fallback.Baseline.Thinking = effective.Thinking
	fallback.Baseline.Fast = effective.Fast
	fallback.Baseline.Questions = effective.Questions
	fallback.FastAvailable = llm.SupportsFastModeProvider(capabilities)
	fallback.QuestionsAvailable = slices.Contains(
		locked.EnabledTools,
		string(toolspec.ToolAskQuestion),
	)
	return fallback, nil
}

func normalizeProjectedChatSettings(
	current session.ChatSettings,
	prepared launch.PreparedChatSettings,
) session.ChatSettings {
	current.Thinking = strings.TrimSpace(current.Thinking)
	if current.Thinking == "" {
		current.Thinking = prepared.Baseline.Thinking
	}
	if current.Fast && !prepared.FastAvailable {
		current.Fast = prepared.Baseline.Fast
	}
	return current
}

func projectChatThinking(
	current string,
	prepared launch.PreparedChatSettings,
) *serverapi.ChatSettingsThinking {
	if len(prepared.SupportedThinkingValues) == 0 {
		return nil
	}
	kind := serverapi.ChatSettingsThinkingEnumerated
	values := append([]string(nil), prepared.SupportedThinkingValues...)
	if !slices.Contains(values, current) ||
		!slices.Contains(values, prepared.Baseline.Thinking) {
		kind = serverapi.ChatSettingsThinkingCustom
		values = nil
	}
	return &serverapi.ChatSettingsThinking{
		Kind:          kind,
		Value:         current,
		BaselineValue: prepared.Baseline.Thinking,
		Values:        values,
		Editability:   serverapi.ChatSettingsEditable,
	}
}

func projectChatAutoCompaction(
	mode config.CompactionMode,
	stored bool,
	workflowLocked bool,
) serverapi.ChatSettingsAutoCompaction {
	policy := serverapi.ChatSettingsAutoCompactionOptional
	if mode == config.CompactionModeNone {
		policy = serverapi.ChatSettingsAutoCompactionDisabled
	} else if workflowLocked {
		policy = serverapi.ChatSettingsAutoCompactionRequired
	}
	projected := serverapi.ChatSettingsAutoCompaction{Policy: policy, Stored: stored}
	switch {
	case mode == config.CompactionModeNone:
		projected.Editability = serverapi.ChatSettingsPolicyDisabled
	case workflowLocked:
		projected.Effective = true
		projected.Editability = serverapi.ChatSettingsWorkflowLock
	default:
		projected.Effective = stored
		projected.Editability = serverapi.ChatSettingsEditable
	}
	return projected
}
