package sessionlaunch

import (
	"core/server/launch"
	"core/server/session"
	"core/shared/config"
	"core/shared/serverapi"
	"errors"
	"fmt"
	"slices"
	"strings"
)

type PreparedChatSettingsOperationInput struct {
	Raw                session.ChatSettingsState
	Effective          session.ChatSettings
	PersistedQuestions bool
	PersistedThinking  *string
	Catalog            launch.PreparedChatAgentCatalog
	Locked             *session.LockedContract
	WorkflowLocked     bool
	CompactionMode     config.CompactionMode
}
type PreparedChatSettingsOperationResult struct {
	State     session.ChatSettingsState
	Effective session.ChatSettings
	Rejection *serverapi.ChatSettingsMutationRejectedResult
}

func ProjectPreparedChatSettingsOperation(input PreparedChatSettingsOperationInput, operation serverapi.ChatSettingsMutationOperation) (PreparedChatSettingsOperationResult, error) {
	rawAgent := input.Raw.Agent
	defaultEntry, ok := input.Catalog.Lookup(config.DefaultSubagentRole)
	if !ok {
		return PreparedChatSettingsOperationResult{}, errors.New("default Chat Agent baseline is missing")
	}
	selectedEntry, selectedAvailable := input.Catalog.Lookup(rawAgent)
	if !selectedAvailable {
		selectedEntry = defaultEntry
	}
	if input.Locked != nil {
		selectedSettings, err := lockedPreparedChatSettings(*input.Locked, selectedEntry.Settings, input.Effective)
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
	} else if selectedAvailable {
		baseSettings.Questions = input.PersistedQuestions
		if input.PersistedThinking != nil {
			persistedThinking := strings.TrimSpace(*input.PersistedThinking)
			if persistedThinking == "" {
				return PreparedChatSettingsOperationResult{}, errors.New("persisted Chat settings Thinking is required when present")
			}
			if slices.Contains(selectedEntry.Settings.SupportedThinkingValues, persistedThinking) {
				baseSettings.Thinking = persistedThinking
			}
		}
	}
	base, err := session.ChatSettingsStateFromCompleteSettings(baseAgent, baseSettings)
	if err != nil {
		return PreparedChatSettingsOperationResult{}, err
	}
	target := base
	switch operation.Kind {
	case serverapi.ChatSettingsMutationAgent:
		agent, ok := session.NormalizeChatAgent(*operation.Role)
		if !ok {
			return PreparedChatSettingsOperationResult{}, fmt.Errorf("Chat Agent %q is invalid", *operation.Role)
		}
		entry, available := input.Catalog.Lookup(agent)
		if !available {
			return rejectedChatSettingsOperation(input, serverapi.ChatSettingsMutationAgentUnavailable), nil
		}
		if (input.Locked != nil || input.WorkflowLocked) && agent != rawAgent {
			return rejectedChatSettingsOperation(input, serverapi.ChatSettingsMutationAgentLocked), nil
		}
		if agent != rawAgent || !selectedAvailable {
			target, err = session.ChatSettingsStateFromCompleteSettings(entry.Choice.Role, entry.Settings.Baseline)
			if err != nil {
				return PreparedChatSettingsOperationResult{}, err
			}
		}
		selectedEntry = entry
	case serverapi.ChatSettingsMutationSupervisor:
		target.Settings.Supervisor = operation.Value
	case serverapi.ChatSettingsMutationThinking:
		thinking := strings.TrimSpace(*operation.Value)
		thinkingProjection := projectChatThinking(input.Effective.Thinking, selectedEntry.Settings)
		if thinkingProjection == nil ||
			(thinkingProjection.Kind == serverapi.ChatSettingsThinkingEnumerated &&
				!slices.Contains(thinkingProjection.Values, thinking)) {
			return rejectedChatSettingsOperation(input, serverapi.ChatSettingsMutationThinkingUnavailable), nil
		}
		target.Settings.Thinking = &thinking
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
	}
	normalized, err := session.NormalizeChatSettingsOverrides(target.Settings)
	if err != nil {
		return PreparedChatSettingsOperationResult{}, err
	}
	target.Settings = normalized
	effective, err := session.ResolveEffectiveChatSettings(target.Settings, nil, selectedEntry.Settings.Baseline)
	if err != nil {
		return PreparedChatSettingsOperationResult{}, err
	}
	effective = normalizeProjectedChatSettings(effective, selectedEntry.Settings)
	return PreparedChatSettingsOperationResult{State: target, Effective: effective}, nil
}
func rejectedChatSettingsOperation(
	input PreparedChatSettingsOperationInput,
	reason serverapi.ChatSettingsMutationRejectionReason,
) PreparedChatSettingsOperationResult {
	return PreparedChatSettingsOperationResult{State: input.Raw, Effective: input.Effective, Rejection: &serverapi.ChatSettingsMutationRejectedResult{Reason: reason}}
}
