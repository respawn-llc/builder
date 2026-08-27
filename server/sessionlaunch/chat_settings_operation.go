package sessionlaunch

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"core/server/launch"
	"core/server/session"
	"core/shared/config"
	"core/shared/serverapi"
)

type PreparedChatSettingsOperationInput struct {
	Raw                session.ChatSettingsState
	Effective          session.ChatSettings
	PersistedQuestions bool
	PersistedThinking  string
	Catalog            launch.PreparedChatAgentCatalog
	Locked             *session.LockedContract
	WorkflowLocked     bool
	CompactionMode     config.CompactionMode
}

type PreparedChatSettingsOperationResult struct {
	State     session.ChatSettingsState
	Effective session.ChatSettings
	Changed   bool
	Rejection *serverapi.ChatSettingsMutationRejectedResult
}

func ProjectPreparedChatSettingsOperation(
	input PreparedChatSettingsOperationInput,
	operation serverapi.ChatSettingsMutationOperation,
) (PreparedChatSettingsOperationResult, error) {
	operation, err := normalizeChatSettingsOperation(operation)
	if err != nil {
		return PreparedChatSettingsOperationResult{}, err
	}
	rawAgent, ok := session.NormalizeChatAgent(input.Raw.Agent)
	if !ok {
		return PreparedChatSettingsOperationResult{}, fmt.Errorf("Chat Agent %q is invalid", input.Raw.Agent)
	}
	input.Raw.Agent = rawAgent

	defaultEntry, ok := input.Catalog.Lookup(config.DefaultSubagentRole)
	if !ok {
		return PreparedChatSettingsOperationResult{}, errors.New("default Chat Agent baseline is missing")
	}
	selectedEntry, selectedAvailable := input.Catalog.Lookup(rawAgent)
	if !selectedAvailable {
		selectedEntry = defaultEntry
	}
	if input.Locked != nil {
		selectedSettings, err := lockedPreparedChatSettings(
			*input.Locked,
			selectedEntry.Settings,
			input.Effective,
		)
		if err != nil {
			return PreparedChatSettingsOperationResult{}, err
		}
		selectedEntry.Settings = selectedSettings
	}

	baseAgent := rawAgent
	baseSettings := input.Effective
	if !selectedAvailable && input.Locked == nil {
		baseAgent = config.DefaultSubagentRole
		baseSettings = defaultEntry.Settings.Baseline
		baseSettings.Questions = input.PersistedQuestions
		baseSettings.Thinking = defaultEntry.Settings.Baseline.Thinking
	} else if selectedAvailable {
		baseSettings.Questions = input.PersistedQuestions
		if strings.TrimSpace(input.PersistedThinking) != "" &&
			slices.Contains(selectedEntry.Settings.SupportedThinkingValues, strings.TrimSpace(input.PersistedThinking)) {
			baseSettings.Thinking = strings.TrimSpace(input.PersistedThinking)
		}
	}
	base := session.ChatSettingsState{
		Agent:    baseAgent,
		Settings: completeChatSettingsOverrides(baseSettings),
	}

	target := base
	effectiveEntry := selectedEntry
	switch operation.Kind {
	case serverapi.ChatSettingsMutationAgent:
		entry, available := input.Catalog.Lookup(*operation.Role)
		if !available {
			return rejectedChatSettingsOperation(input, serverapi.ChatSettingsMutationAgentUnavailable), nil
		}
		if (input.Locked != nil || input.WorkflowLocked) && *operation.Role != rawAgent {
			return rejectedChatSettingsOperation(input, serverapi.ChatSettingsMutationAgentLocked), nil
		}
		if *operation.Role != rawAgent || !selectedAvailable {
			target = session.ChatSettingsState{
				Agent:    entry.Choice.Role,
				Settings: completeChatSettingsOverrides(entry.Settings.Baseline),
			}
		}
		effectiveEntry = entry
	case serverapi.ChatSettingsMutationSupervisor:
		target.Settings.Supervisor = operation.Value
	case serverapi.ChatSettingsMutationThinking:
		if !slices.Contains(selectedEntry.Settings.SupportedThinkingValues, *operation.Value) {
			return rejectedChatSettingsOperation(input, serverapi.ChatSettingsMutationThinkingUnavailable), nil
		}
		target.Settings.Thinking = operation.Value
	case serverapi.ChatSettingsMutationFast:
		if !selectedEntry.Settings.FastAvailable {
			return rejectedChatSettingsOperation(input, serverapi.ChatSettingsMutationFastUnavailable), nil
		}
		target.Settings.Fast = operation.Enabled
	case serverapi.ChatSettingsMutationQuestions:
		target.Settings.Questions = operation.Enabled
	case serverapi.ChatSettingsMutationAutoCompaction:
		if input.WorkflowLocked || input.CompactionMode == config.CompactionModeNone {
			return rejectedChatSettingsOperation(input, serverapi.ChatSettingsMutationAutoCompactionPolicyLock), nil
		}
		target.Settings.AutoCompaction = operation.Enabled
	default:
		return PreparedChatSettingsOperationResult{}, fmt.Errorf("Chat settings operation kind %q is invalid", operation.Kind)
	}

	normalized, err := session.NormalizeChatSettingsOverrides(target.Settings)
	if err != nil {
		return PreparedChatSettingsOperationResult{}, err
	}
	target.Settings = normalized
	effective, err := session.ResolveEffectiveChatSettings(target.Settings, nil, effectiveEntry.Settings.Baseline)
	if err != nil {
		return PreparedChatSettingsOperationResult{}, err
	}
	effective = normalizeProjectedChatSettings(effective, effectiveEntry.Settings)
	changed := !session.ChatSettingsStatesEqual(input.Raw, target)
	return PreparedChatSettingsOperationResult{
		State:     target,
		Effective: effective,
		Changed:   changed,
	}, nil
}

func normalizeChatSettingsOperation(operation serverapi.ChatSettingsMutationOperation) (serverapi.ChatSettingsMutationOperation, error) {
	if err := operation.Validate(); err != nil {
		return serverapi.ChatSettingsMutationOperation{}, err
	}
	switch operation.Kind {
	case serverapi.ChatSettingsMutationAgent:
		agent, ok := session.NormalizeChatAgent(*operation.Role)
		if !ok {
			return serverapi.ChatSettingsMutationOperation{}, fmt.Errorf("Chat Agent %q is invalid", *operation.Role)
		}
		operation.Role = &agent
	case serverapi.ChatSettingsMutationSupervisor:
		normalized, err := session.NormalizeChatSettingsOverrides(&session.ChatSettingsOverrides{
			Supervisor: operation.Value,
		})
		if err != nil {
			return serverapi.ChatSettingsMutationOperation{}, err
		}
		operation.Value = normalized.Supervisor
	case serverapi.ChatSettingsMutationThinking:
		value := strings.TrimSpace(*operation.Value)
		if value == "" {
			return serverapi.ChatSettingsMutationOperation{}, errors.New("Chat settings Thinking is required")
		}
		operation.Value = &value
	case serverapi.ChatSettingsMutationFast, serverapi.ChatSettingsMutationQuestions, serverapi.ChatSettingsMutationAutoCompaction:
	default:
		return serverapi.ChatSettingsMutationOperation{}, fmt.Errorf("Chat settings operation kind %q is invalid", operation.Kind)
	}
	return operation, nil
}

func rejectedChatSettingsOperation(
	input PreparedChatSettingsOperationInput,
	reason serverapi.ChatSettingsMutationRejectionReason,
) PreparedChatSettingsOperationResult {
	return PreparedChatSettingsOperationResult{
		State:     input.Raw,
		Effective: input.Effective,
		Rejection: &serverapi.ChatSettingsMutationRejectedResult{Reason: reason},
	}
}

func completeChatSettingsOverrides(settings session.ChatSettings) *session.ChatSettingsOverrides {
	return &session.ChatSettingsOverrides{
		Supervisor:     &settings.Supervisor,
		Thinking:       &settings.Thinking,
		Fast:           &settings.Fast,
		Questions:      &settings.Questions,
		AutoCompaction: &settings.AutoCompaction,
	}
}
