package sessionlaunch

import (
	"errors"
	"fmt"
	"slices"

	"core/server/launch"
	"core/server/llm"
	"core/server/session"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/toolspec"
)

type ChatSettingsProjectionInput struct {
	Catalog                  launch.PreparedChatAgentCatalog
	Agent                    string
	Settings                 session.ChatSettings
	PersistedQuestionsPolicy bool
	WorkflowLocked           bool
	CachingLocked            bool
	CompactionPolicy         serverapi.ChatSettingsAutoCompactionPolicy
	Locked                   *session.LockedContract
}

func ProjectChatSettings(input ChatSettingsProjectionInput) (serverapi.ChatSettings, error) {
	defaultEntry, ok := input.Catalog.Lookup(config.DefaultSubagentRole)
	if !ok {
		return serverapi.ChatSettings{}, errors.New("prepared Chat Agent catalog is missing default")
	}
	selected, ok := input.Catalog.Lookup(input.Agent)
	if !ok {
		if input.CachingLocked {
			selected = defaultEntry
		} else {
			selected = defaultEntry
			input.Agent = config.DefaultSubagentRole
			input.Settings = defaultEntry.Settings.Baseline
			input.PersistedQuestionsPolicy = defaultEntry.Settings.Baseline.Questions
		}
	}
	effective := input.Settings
	if !input.CachingLocked {
		effective = normalizeProjectedChatSettings(effective, selected.Settings)
	}
	effective.Questions = input.PersistedQuestionsPolicy
	selectedRole := selected.Choice.Role
	selectedModel := selected.Choice.Model
	selectedSettings := selected.Settings
	if input.CachingLocked {
		if input.Locked == nil {
			return serverapi.ChatSettings{}, errors.New("caching-locked Chat settings require locked contract")
		}
		selectedRole = normalizeWorkspaceChatDraftAgent(input.Agent)
		if selectedRole == "" {
			return serverapi.ChatSettings{}, errors.New("caching-locked Chat Agent is required")
		}
		selectedModel = input.Locked.Model
		if selectedModel == "" {
			return serverapi.ChatSettings{}, errors.New("caching-locked Chat model is required")
		}
		lockedSettings, err := lockedPreparedChatSettings(
			*input.Locked,
			selected.Settings,
			effective,
		)
		if err != nil {
			return serverapi.ChatSettings{}, err
		}
		selectedSettings = lockedSettings
		effective = normalizeProjectedChatSettings(effective, selectedSettings)
		effective.Questions = input.PersistedQuestionsPolicy
	}

	agentEditability := serverapi.ChatSettingsEditable
	if input.WorkflowLocked {
		agentEditability = serverapi.ChatSettingsWorkflowLock
	} else if input.CachingLocked {
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
	autoCompaction, err := projectChatAutoCompaction(
		input.CompactionPolicy,
		effective.AutoCompaction,
		input.WorkflowLocked,
	)
	if err != nil {
		return serverapi.ChatSettings{}, err
	}
	projected := serverapi.ChatSettings{
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
		AgentLocked:    input.WorkflowLocked || input.CachingLocked,
		WorkflowLocked: input.WorkflowLocked,
		CachingLocked:  input.CachingLocked,
	}
	if err := projected.Validate(); err != nil {
		return serverapi.ChatSettings{}, fmt.Errorf("validate projected Chat settings: %w", err)
	}
	return projected, nil
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
	thinkingValues := []string(nil)
	if llm.LockedContractSupportsReasoningEffort(&locked, locked.Model) {
		thinkingValues = launch.SupportedChatThinkingValues(locked.Model, effective.Thinking)
	}
	tools := lockedChatToolIDs(locked.EnabledTools)
	fallback.Baseline.Thinking = effective.Thinking
	fallback.Baseline.Fast = effective.Fast
	fallback.Baseline.Questions = effective.Questions
	fallback.SupportedThinkingValues = thinkingValues
	fallback.FastAvailable = llm.SupportsFastModeProvider(capabilities)
	fallback.QuestionsAvailable = slices.Contains(tools, toolspec.ToolAskQuestion)
	return fallback, nil
}

func lockedChatToolIDs(raw []string) []toolspec.ID {
	enabled := make(map[toolspec.ID]struct{}, len(raw))
	for _, value := range raw {
		if id, ok := toolspec.ParseID(value); ok {
			enabled[id] = struct{}{}
		}
	}
	result := make([]toolspec.ID, 0, len(enabled))
	for _, id := range toolspec.CatalogIDs() {
		if _, ok := enabled[id]; ok {
			result = append(result, id)
		}
	}
	return result
}

func normalizeProjectedChatSettings(
	current session.ChatSettings,
	prepared launch.PreparedChatSettings,
) session.ChatSettings {
	if !slices.Contains(prepared.SupportedThinkingValues, current.Thinking) {
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
	policy serverapi.ChatSettingsAutoCompactionPolicy,
	stored bool,
	workflowLocked bool,
) (serverapi.ChatSettingsAutoCompaction, error) {
	projected := serverapi.ChatSettingsAutoCompaction{
		Policy: policy,
		Stored: stored,
	}
	switch policy {
	case serverapi.ChatSettingsAutoCompactionOptional:
		projected.Effective = stored
		projected.Editability = serverapi.ChatSettingsEditable
	case serverapi.ChatSettingsAutoCompactionRequired:
		projected.Effective = true
		projected.Editability = serverapi.ChatSettingsEditable
		if workflowLocked {
			projected.Editability = serverapi.ChatSettingsWorkflowLock
		}
	case serverapi.ChatSettingsAutoCompactionDisabled:
		projected.Effective = false
		projected.Editability = serverapi.ChatSettingsPolicyDisabled
	default:
		return serverapi.ChatSettingsAutoCompaction{}, errors.New(
			"Chat Auto-compaction policy is invalid",
		)
	}
	return projected, nil
}
