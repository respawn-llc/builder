package sessionlaunch

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"core/server/launch"
	"core/server/session"
	"core/shared/config"
)

type ChatSettingsOperationKind string

const (
	ChatSettingsOperationAgent          ChatSettingsOperationKind = "agent"
	ChatSettingsOperationSupervisor     ChatSettingsOperationKind = "supervisor"
	ChatSettingsOperationThinking       ChatSettingsOperationKind = "thinking"
	ChatSettingsOperationFast           ChatSettingsOperationKind = "fast"
	ChatSettingsOperationQuestions      ChatSettingsOperationKind = "questions"
	ChatSettingsOperationAutoCompaction ChatSettingsOperationKind = "auto_compaction"
)

// ChatSettingsOperation is one semantic settings action. Its kind determines
// which value is present; false remains a valid value for boolean operations.
type ChatSettingsOperation struct {
	Kind    ChatSettingsOperationKind
	Value   string
	Enabled bool
}

type ChatSettingsRejectionReason string

const (
	ChatSettingsAgentLocked              ChatSettingsRejectionReason = "agent_locked"
	ChatSettingsAgentUnavailable         ChatSettingsRejectionReason = "agent_unavailable"
	ChatSettingsThinkingUnavailable      ChatSettingsRejectionReason = "thinking_unavailable"
	ChatSettingsFastUnavailable          ChatSettingsRejectionReason = "fast_unavailable"
	ChatSettingsAutoCompactionPolicyLock ChatSettingsRejectionReason = "auto_compaction_policy_locked"
)

type ChatSettingsOperationRejection struct {
	Reason ChatSettingsRejectionReason
}

type PreparedChatSettingsOperationInput struct {
	Raw                session.ChatSettingsState
	Effective          session.ChatSettings
	PersistedQuestions bool
	PersistedThinking  string
	Catalog            launch.PreparedChatAgentCatalog
	AgentLocked        bool
	WorkflowLocked     bool
	CompactionMode     config.CompactionMode
}

type PreparedChatSettingsOperationResult struct {
	State     session.ChatSettingsState
	Effective session.ChatSettings
	Changed   bool
	Rejection *ChatSettingsOperationRejection
}

func ProjectPreparedChatSettingsOperation(
	input PreparedChatSettingsOperationInput,
	operation ChatSettingsOperation,
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

	baseAgent := rawAgent
	baseSettings := input.Effective
	if !selectedAvailable && !input.AgentLocked {
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
	case ChatSettingsOperationAgent:
		entry, available := input.Catalog.Lookup(operation.Value)
		if !available {
			return rejectedChatSettingsOperation(input, ChatSettingsAgentUnavailable), nil
		}
		if input.AgentLocked && operation.Value != rawAgent {
			return rejectedChatSettingsOperation(input, ChatSettingsAgentLocked), nil
		}
		if operation.Value != rawAgent || !selectedAvailable {
			target = session.ChatSettingsState{
				Agent:    entry.Choice.Role,
				Settings: completeChatSettingsOverrides(entry.Settings.Baseline),
			}
		}
		effectiveEntry = entry
	case ChatSettingsOperationSupervisor:
		target.Settings.Supervisor = &operation.Value
	case ChatSettingsOperationThinking:
		if !slices.Contains(selectedEntry.Settings.SupportedThinkingValues, operation.Value) {
			return rejectedChatSettingsOperation(input, ChatSettingsThinkingUnavailable), nil
		}
		target.Settings.Thinking = &operation.Value
	case ChatSettingsOperationFast:
		if !selectedEntry.Settings.FastAvailable {
			return rejectedChatSettingsOperation(input, ChatSettingsFastUnavailable), nil
		}
		target.Settings.Fast = &operation.Enabled
	case ChatSettingsOperationQuestions:
		target.Settings.Questions = &operation.Enabled
	case ChatSettingsOperationAutoCompaction:
		if input.WorkflowLocked || input.CompactionMode == config.CompactionModeNone {
			return rejectedChatSettingsOperation(input, ChatSettingsAutoCompactionPolicyLock), nil
		}
		target.Settings.AutoCompaction = &operation.Enabled
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

func normalizeChatSettingsOperation(operation ChatSettingsOperation) (ChatSettingsOperation, error) {
	switch operation.Kind {
	case ChatSettingsOperationAgent:
		agent, ok := session.NormalizeChatAgent(operation.Value)
		if !ok {
			return ChatSettingsOperation{}, fmt.Errorf("Chat Agent %q is invalid", operation.Value)
		}
		operation.Value = agent
	case ChatSettingsOperationSupervisor:
		normalized, err := session.NormalizeChatSettingsOverrides(&session.ChatSettingsOverrides{
			Supervisor: &operation.Value,
		})
		if err != nil {
			return ChatSettingsOperation{}, err
		}
		operation.Value = *normalized.Supervisor
	case ChatSettingsOperationThinking:
		operation.Value = strings.TrimSpace(operation.Value)
		if operation.Value == "" {
			return ChatSettingsOperation{}, errors.New("Chat settings Thinking is required")
		}
	case ChatSettingsOperationFast, ChatSettingsOperationQuestions, ChatSettingsOperationAutoCompaction:
	default:
		return ChatSettingsOperation{}, fmt.Errorf("Chat settings operation kind %q is invalid", operation.Kind)
	}
	return operation, nil
}

func rejectedChatSettingsOperation(
	input PreparedChatSettingsOperationInput,
	reason ChatSettingsRejectionReason,
) PreparedChatSettingsOperationResult {
	return PreparedChatSettingsOperationResult{
		State:     input.Raw,
		Effective: input.Effective,
		Rejection: &ChatSettingsOperationRejection{Reason: reason},
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
