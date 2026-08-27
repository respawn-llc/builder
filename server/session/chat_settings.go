package session

import (
	"errors"
	"fmt"
	"strings"

	"core/shared/config"
	"core/shared/protocol"
	"core/shared/sessioncontract"
	"core/shared/textutil"
)

var ErrChatAgentLocked = errors.New("Chat Agent is locked")

type ChatSettingsOverrides struct {
	Supervisor     *string `json:"supervisor,omitempty"`
	Thinking       *string `json:"thinking,omitempty"`
	Fast           *bool   `json:"fast,omitempty"`
	Questions      *bool   `json:"questions,omitempty"`
	AutoCompaction *bool   `json:"auto_compaction,omitempty"`
}

func (o *ChatSettingsOverrides) UnmarshalJSON(data []byte) error {
	type wire ChatSettingsOverrides
	var decoded wire
	if err := protocol.DecodeStrictJSON(data, &decoded); err != nil {
		return err
	}
	normalized, err := NormalizeChatSettingsOverrides((*ChatSettingsOverrides)(&decoded))
	if err != nil {
		return err
	}
	if normalized == nil {
		*o = ChatSettingsOverrides{}
		return nil
	}
	*o = *normalized
	return nil
}

type ChatSettings struct {
	Supervisor     string
	Thinking       string
	Fast           bool
	Questions      bool
	AutoCompaction bool
}

type ChatDraftState struct {
	Message  string
	Agent    string
	Settings *ChatSettingsOverrides
}

type ChatSettingsState struct {
	Agent    string
	Settings *ChatSettingsOverrides
}

func ChatSettingsStateFromCompleteSettings(agent string, settings ChatSettings) (ChatSettingsState, error) {
	normalizedAgent, err := normalizeChatAgent(agent)
	if err != nil {
		return ChatSettingsState{}, err
	}
	normalizedSettings, err := normalizeCompleteChatSettings(settings)
	if err != nil {
		return ChatSettingsState{}, err
	}
	return ChatSettingsState{
		Agent: normalizedAgent,
		Settings: &ChatSettingsOverrides{
			Supervisor:     textutil.Value(normalizedSettings.Supervisor),
			Thinking:       textutil.Value(normalizedSettings.Thinking),
			Fast:           textutil.Value(normalizedSettings.Fast),
			Questions:      textutil.Value(normalizedSettings.Questions),
			AutoCompaction: textutil.Value(normalizedSettings.AutoCompaction),
		},
	}, nil
}

// ChatSettingsCommitResult reports the result of committing a complete Chat
// settings state through the Session metadata authority.
type ChatSettingsCommitResult struct {
	CommitReceipt
	Changed bool
	State   ChatSettingsState
}

type ChatAgentSelection struct {
	Agent    string
	Baseline ChatSettings
}

type ChatSettingsMutation struct {
	Agent          *ChatAgentSelection
	Supervisor     *string
	Thinking       *string
	Fast           *bool
	Questions      *bool
	AutoCompaction *bool
}

type ChatSettingsMutationResult struct {
	CommitReceipt
	Changed bool
	State   ChatSettingsState
}

// ProjectChatSettingsState applies a complete Chat settings target to Meta
// without persisting it. The target is intentionally complete so every writer
// uses the same validation, continuation, and Agent-change behavior.
func ProjectChatSettingsState(meta Meta, target ChatSettingsState) (Meta, bool, error) {
	currentMeta := cloneMeta(meta)
	if err := normalizeMetaContinuation(&currentMeta); err != nil {
		return Meta{}, false, err
	}
	if err := normalizeMetaChatSettings(&currentMeta); err != nil {
		return Meta{}, false, err
	}
	agent, err := normalizeChatAgent(target.Agent)
	if err != nil {
		return Meta{}, false, err
	}
	settings, err := NormalizeChatSettingsOverrides(target.Settings)
	if err != nil {
		return Meta{}, false, err
	}
	if err := requireCompleteChatSettingsOverrides(settings); err != nil {
		return Meta{}, false, err
	}
	current := chatSettingsStateFromNormalizedMeta(currentMeta)
	next := ChatSettingsState{
		Agent:    agent,
		Settings: cloneChatSettingsOverrides(settings),
	}
	changed := !chatSettingsStatesEqual(current, next)
	if !changed {
		return currentMeta, false, nil
	}
	if currentMeta.Locked != nil && current.Agent != next.Agent {
		return Meta{}, false, ErrChatAgentLocked
	}

	currentMeta.ChatSettings = cloneChatSettingsOverrides(next.Settings)
	continuation := cloneContinuationContext(currentMeta.Continuation)
	if continuation == nil {
		continuation = &ContinuationContext{}
	}
	agentChanged := current.Agent != next.Agent
	if next.Agent == config.DefaultSubagentRole {
		continuation.AgentRole = nil
	} else {
		continuation.AgentRole = textutil.Value(next.Agent)
	}
	if agentChanged {
		continuation.OpenAIBaseURL = nil
	}
	currentMeta.Continuation, err = NormalizeContinuationContext(*continuation)
	if err != nil {
		return Meta{}, false, err
	}
	return currentMeta, true, nil
}

// CommitChatSettingsState commits one complete Chat settings target through
// the existing metadata mutation lock and commit-receipt path.
func (s *Store) CommitChatSettingsState(target ChatSettingsState) (ChatSettingsCommitResult, error) {
	if s == nil {
		return ChatSettingsCommitResult{}, errors.New("Session store is required")
	}

	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	s.mu.Lock()

	projected, changed, err := ProjectChatSettingsState(s.meta, target)
	if err != nil || !changed {
		state, stateErr := ChatSettingsStateFromMeta(s.meta)
		s.mu.Unlock()
		if err != nil {
			return ChatSettingsCommitResult{State: state}, err
		}
		if stateErr != nil {
			return ChatSettingsCommitResult{}, stateErr
		}
		return ChatSettingsCommitResult{State: state}, nil
	}
	if err := s.requireMetadataPersistenceLocked(); err != nil {
		s.mu.Unlock()
		return ChatSettingsCommitResult{State: target}, err
	}

	checkpoint := s.metadataMutationCheckpointLocked()
	s.meta.ChatSettings = projected.ChatSettings
	s.meta.Continuation = projected.Continuation
	s.meta.UpdatedAt = storeTimestamp(s.options)

	receipt, err := s.persistMetadataMutationWithCommitReceiptLocked(checkpoint)
	if !receipt.Committed {
		changed = false
	}
	state, stateErr := ChatSettingsStateFromMeta(projected)
	if stateErr != nil {
		return ChatSettingsCommitResult{
			CommitReceipt: receipt,
			Changed:       changed,
		}, errors.Join(err, stateErr)
	}
	return ChatSettingsCommitResult{
		CommitReceipt: receipt,
		Changed:       changed,
		State:         state,
	}, errors.Join(err, stateErr)
}

func NormalizeChatSettingsOverrides(overrides *ChatSettingsOverrides) (*ChatSettingsOverrides, error) {
	if overrides == nil {
		return nil, nil
	}
	normalized := &ChatSettingsOverrides{
		Supervisor:     textutil.Pointer(overrides.Supervisor),
		Thinking:       textutil.Pointer(overrides.Thinking),
		Fast:           textutil.Pointer(overrides.Fast),
		Questions:      textutil.Pointer(overrides.Questions),
		AutoCompaction: textutil.Pointer(overrides.AutoCompaction),
	}
	if normalized.Supervisor != nil {
		value, err := normalizeChatSupervisor(*normalized.Supervisor)
		if err != nil {
			return nil, err
		}
		normalized.Supervisor = &value
	}
	if normalized.Thinking != nil {
		value := strings.TrimSpace(*normalized.Thinking)
		if value == "" {
			return nil, errors.New("Chat settings Thinking is required when present")
		}
		normalized.Thinking = &value
	}
	if normalized.Supervisor == nil &&
		normalized.Thinking == nil &&
		normalized.Fast == nil &&
		normalized.Questions == nil &&
		normalized.AutoCompaction == nil {
		return nil, nil
	}
	return normalized, nil
}

func ResolveEffectiveChatSettings(
	overrides *ChatSettingsOverrides,
	current *ChatSettingsOverrides,
	defaults ChatSettings,
) (ChatSettings, error) {
	normalizedDefaults, err := normalizeCompleteChatSettings(defaults)
	if err != nil {
		return ChatSettings{}, fmt.Errorf("validate default Chat settings: %w", err)
	}
	normalizedCurrent, err := NormalizeChatSettingsOverrides(current)
	if err != nil {
		return ChatSettings{}, fmt.Errorf("validate current Chat settings: %w", err)
	}
	normalizedOverrides, err := NormalizeChatSettingsOverrides(overrides)
	if err != nil {
		return ChatSettings{}, fmt.Errorf("validate Session Chat settings: %w", err)
	}
	result := normalizedDefaults
	applyChatSettingsOverrides(&result, normalizedCurrent)
	applyChatSettingsOverrides(&result, normalizedOverrides)
	return result, nil
}

func InitializeChatDraft(store *Store, state ChatDraftState) error {
	if store == nil {
		return errors.New("Session store is required")
	}
	agent, err := normalizeChatAgent(state.Agent)
	if err != nil {
		return err
	}
	settings, err := NormalizeChatSettingsOverrides(state.Settings)
	if err != nil {
		return err
	}
	if err := requireCompleteChatSettingsOverrides(settings); err != nil {
		return err
	}

	store.mutationMu.Lock()
	defer store.mutationMu.Unlock()
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.persisted {
		return errors.New("Chat draft initialization requires a non-durable Session")
	}
	if store.meta.Category == nil || *store.meta.Category != sessioncontract.SessionCategoryMain {
		return errors.New("Chat draft initialization requires a main Session")
	}
	if store.meta.PreviousSessionID != nil || store.meta.ParentAgentSessionID != nil {
		return errors.New("Chat draft initialization requires an independent Session")
	}
	if store.meta.Name != "" ||
		store.meta.FirstPromptPreview != "" ||
		store.meta.InputDraft != "" ||
		store.meta.Continuation != nil ||
		store.meta.ChatSettings != nil ||
		store.meta.LastSequence != 0 ||
		store.meta.ConversationEstablished ||
		store.meta.ModelRequestCount != 0 ||
		store.meta.Locked != nil {
		return errors.New("Chat draft initialization requires a fresh Session")
	}

	store.meta.InputDraft = state.Message
	store.meta.ChatSettings = settings
	if agent == config.DefaultSubagentRole {
		store.meta.Continuation = nil
	} else {
		store.meta.Continuation = &ContinuationContext{AgentRole: &agent}
	}
	store.meta.UpdatedAt = storeTimestamp(store.options)
	return nil
}

func ChatDraftStateFromMeta(meta Meta) (ChatDraftState, error) {
	settings, err := ChatSettingsStateFromMeta(meta)
	if err != nil {
		return ChatDraftState{}, err
	}
	return ChatDraftState{
		Message:  meta.InputDraft,
		Agent:    settings.Agent,
		Settings: settings.Settings,
	}, nil
}

func ChatSettingsStateFromMeta(meta Meta) (ChatSettingsState, error) {
	if err := normalizeMetaContinuation(&meta); err != nil {
		return ChatSettingsState{}, err
	}
	if err := normalizeMetaChatSettings(&meta); err != nil {
		return ChatSettingsState{}, err
	}
	return chatSettingsStateFromNormalizedMeta(meta), nil
}

// ProjectChatSettingsMutation applies Chat settings domain rules to an
// immutable Meta value without persisting it.
func ProjectChatSettingsMutation(meta Meta, mutation ChatSettingsMutation) (Meta, ChatSettingsMutationResult, error) {
	prepared, err := prepareChatSettingsMutation(mutation)
	if err != nil {
		return Meta{}, ChatSettingsMutationResult{}, err
	}
	currentMeta := cloneMeta(meta)
	if err := normalizeMetaContinuation(&currentMeta); err != nil {
		return Meta{}, ChatSettingsMutationResult{}, err
	}
	if err := normalizeMetaChatSettings(&currentMeta); err != nil {
		return Meta{}, ChatSettingsMutationResult{}, err
	}
	current := chatSettingsStateFromNormalizedMeta(currentMeta)
	next, err := applyChatSettingsMutation(current, prepared, currentMeta.Locked != nil)
	if err != nil {
		return Meta{}, ChatSettingsMutationResult{State: current}, err
	}
	if chatSettingsStatesEqual(current, next) {
		return currentMeta, ChatSettingsMutationResult{State: current}, nil
	}
	currentMeta.ChatSettings = cloneChatSettingsOverrides(next.Settings)
	continuation := cloneContinuationContext(currentMeta.Continuation)
	if continuation == nil {
		continuation = &ContinuationContext{}
	}
	if next.Agent == config.DefaultSubagentRole {
		continuation.AgentRole = nil
	} else {
		agent := next.Agent
		continuation.AgentRole = &agent
	}
	if prepared.agent != nil {
		continuation.OpenAIBaseURL = nil
	}
	currentMeta.Continuation, err = NormalizeContinuationContext(*continuation)
	if err != nil {
		return Meta{}, ChatSettingsMutationResult{State: current}, err
	}
	return currentMeta, ChatSettingsMutationResult{Changed: true, State: next}, nil
}

func (s *Store) MutateChatSettings(mutation ChatSettingsMutation) (ChatSettingsMutationResult, error) {
	if s == nil {
		return ChatSettingsMutationResult{}, errors.New("Session store is required")
	}

	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	s.mu.Lock()

	projected, result, err := ProjectChatSettingsMutation(s.meta, mutation)
	if err != nil || !result.Changed {
		s.mu.Unlock()
		return result, err
	}
	if err := s.requireMetadataPersistenceLocked(); err != nil {
		s.mu.Unlock()
		return ChatSettingsMutationResult{State: result.State}, err
	}

	checkpoint := s.metadataMutationCheckpointLocked()
	s.meta.ChatSettings = projected.ChatSettings
	s.meta.Continuation = projected.Continuation
	s.meta.UpdatedAt = storeTimestamp(s.options)

	receipt, err := s.persistMetadataMutationWithCommitReceiptLocked(checkpoint)
	if !receipt.Committed {
		result.Changed = false
	}
	result.CommitReceipt = receipt
	return result, err
}

func normalizeMetaChatSettings(meta *Meta) error {
	if meta == nil {
		return nil
	}
	normalized, err := NormalizeChatSettingsOverrides(meta.ChatSettings)
	if err != nil {
		return err
	}
	meta.ChatSettings = normalized
	return nil
}

type preparedChatSettingsMutation struct {
	agent          *ChatAgentSelection
	supervisor     *string
	thinking       *string
	fast           *bool
	questions      *bool
	autoCompaction *bool
}

func prepareChatSettingsMutation(mutation ChatSettingsMutation) (preparedChatSettingsMutation, error) {
	if mutation.Agent != nil &&
		(mutation.Supervisor != nil ||
			mutation.Thinking != nil ||
			mutation.Fast != nil ||
			mutation.Questions != nil ||
			mutation.AutoCompaction != nil) {
		return preparedChatSettingsMutation{}, errors.New("Agent selection cannot be combined with individual Chat setting changes")
	}
	prepared := preparedChatSettingsMutation{}
	if mutation.Agent != nil {
		agent, err := normalizeChatAgent(mutation.Agent.Agent)
		if err != nil {
			return preparedChatSettingsMutation{}, err
		}
		baseline, err := normalizeCompleteChatSettings(mutation.Agent.Baseline)
		if err != nil {
			return preparedChatSettingsMutation{}, fmt.Errorf("validate Agent Chat settings baseline: %w", err)
		}
		prepared.agent = &ChatAgentSelection{Agent: agent, Baseline: baseline}
		return prepared, nil
	}
	normalized, err := NormalizeChatSettingsOverrides(&ChatSettingsOverrides{
		Supervisor:     mutation.Supervisor,
		Thinking:       mutation.Thinking,
		Fast:           mutation.Fast,
		Questions:      mutation.Questions,
		AutoCompaction: mutation.AutoCompaction,
	})
	if err != nil {
		return preparedChatSettingsMutation{}, err
	}
	if normalized == nil {
		return prepared, nil
	}
	prepared.supervisor = normalized.Supervisor
	prepared.thinking = normalized.Thinking
	prepared.fast = normalized.Fast
	prepared.questions = normalized.Questions
	prepared.autoCompaction = normalized.AutoCompaction
	return prepared, nil
}

func applyChatSettingsMutation(
	current ChatSettingsState,
	mutation preparedChatSettingsMutation,
	agentLocked bool,
) (ChatSettingsState, error) {
	if mutation.agent != nil {
		if mutation.agent.Agent == current.Agent {
			return current, nil
		}
		if agentLocked {
			return current, ErrChatAgentLocked
		}
		return ChatSettingsState{
			Agent: mutation.agent.Agent,
			Settings: &ChatSettingsOverrides{
				Supervisor:     textutil.Value(mutation.agent.Baseline.Supervisor),
				Thinking:       textutil.Value(mutation.agent.Baseline.Thinking),
				Fast:           textutil.Value(mutation.agent.Baseline.Fast),
				Questions:      textutil.Value(mutation.agent.Baseline.Questions),
				AutoCompaction: textutil.Value(mutation.agent.Baseline.AutoCompaction),
			},
		}, nil
	}
	next := ChatSettingsState{
		Agent:    current.Agent,
		Settings: cloneChatSettingsOverrides(current.Settings),
	}
	if next.Settings == nil {
		next.Settings = &ChatSettingsOverrides{}
	}
	if mutation.supervisor != nil {
		next.Settings.Supervisor = textutil.Pointer(mutation.supervisor)
	}
	if mutation.thinking != nil {
		next.Settings.Thinking = textutil.Pointer(mutation.thinking)
	}
	if mutation.fast != nil {
		next.Settings.Fast = textutil.Pointer(mutation.fast)
	}
	if mutation.questions != nil {
		next.Settings.Questions = textutil.Pointer(mutation.questions)
	}
	if mutation.autoCompaction != nil {
		next.Settings.AutoCompaction = textutil.Pointer(mutation.autoCompaction)
	}
	if normalized, err := NormalizeChatSettingsOverrides(next.Settings); err != nil {
		return current, err
	} else {
		next.Settings = normalized
	}
	return next, nil
}

func chatSettingsStateFromNormalizedMeta(meta Meta) ChatSettingsState {
	agent := config.DefaultSubagentRole
	if role := ContinuationAgentRole(meta); role != nil {
		agent = *role
	}
	return ChatSettingsState{
		Agent:    agent,
		Settings: cloneChatSettingsOverrides(meta.ChatSettings),
	}
}

func chatSettingsStatesEqual(left ChatSettingsState, right ChatSettingsState) bool {
	return left.Agent == right.Agent && chatSettingsOverridesEqual(left.Settings, right.Settings)
}

func ChatSettingsStatesEqual(left ChatSettingsState, right ChatSettingsState) bool {
	return chatSettingsStatesEqual(left, right)
}

func chatSettingsOverridesEqual(left *ChatSettingsOverrides, right *ChatSettingsOverrides) bool {
	if left == nil || right == nil {
		return left == right
	}
	return textutil.EqualOptional(left.Supervisor, right.Supervisor) &&
		textutil.EqualOptional(left.Thinking, right.Thinking) &&
		textutil.EqualOptional(left.Fast, right.Fast) &&
		textutil.EqualOptional(left.Questions, right.Questions) &&
		textutil.EqualOptional(left.AutoCompaction, right.AutoCompaction)
}

func normalizeChatSupervisor(value string) (string, error) {
	normalized, ok := NormalizeReviewerFrequency(value)
	if !ok {
		return "", fmt.Errorf("Chat settings Supervisor %q is invalid", value)
	}
	return normalized, nil
}

func normalizeCompleteChatSettings(settings ChatSettings) (ChatSettings, error) {
	supervisor, err := normalizeChatSupervisor(settings.Supervisor)
	if err != nil {
		return ChatSettings{}, err
	}
	thinking := strings.TrimSpace(settings.Thinking)
	if thinking == "" {
		return ChatSettings{}, errors.New("Chat settings Thinking is required")
	}
	settings.Supervisor = supervisor
	settings.Thinking = thinking
	return settings, nil
}

func normalizeChatAgent(value string) (string, error) {
	normalized, ok := NormalizeChatAgent(value)
	if !ok {
		return "", fmt.Errorf("Chat draft Agent %q is invalid", value)
	}
	return normalized, nil
}

func NormalizeChatAgent(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if strings.EqualFold(trimmed, config.DefaultSubagentRole) {
		return config.DefaultSubagentRole, true
	}
	normalized := config.NormalizeSubagentRole(trimmed)
	if normalized == "" {
		return "", false
	}
	return normalized, true
}

func requireCompleteChatSettingsOverrides(settings *ChatSettingsOverrides) error {
	if settings == nil ||
		settings.Supervisor == nil ||
		settings.Thinking == nil ||
		settings.Fast == nil ||
		settings.Questions == nil ||
		settings.AutoCompaction == nil {
		return errors.New("Chat draft settings must contain every transferred value")
	}
	return nil
}

func applyChatSettingsOverrides(settings *ChatSettings, overrides *ChatSettingsOverrides) {
	if overrides == nil {
		return
	}
	if overrides.Supervisor != nil {
		settings.Supervisor = *overrides.Supervisor
	}
	if overrides.Thinking != nil {
		settings.Thinking = *overrides.Thinking
	}
	if overrides.Fast != nil {
		settings.Fast = *overrides.Fast
	}
	if overrides.Questions != nil {
		settings.Questions = *overrides.Questions
	}
	if overrides.AutoCompaction != nil {
		settings.AutoCompaction = *overrides.AutoCompaction
	}
}

func cloneChatSettingsOverrides(overrides *ChatSettingsOverrides) *ChatSettingsOverrides {
	if overrides == nil {
		return nil
	}
	return &ChatSettingsOverrides{
		Supervisor:     textutil.Pointer(overrides.Supervisor),
		Thinking:       textutil.Pointer(overrides.Thinking),
		Fast:           textutil.Pointer(overrides.Fast),
		Questions:      textutil.Pointer(overrides.Questions),
		AutoCompaction: textutil.Pointer(overrides.AutoCompaction),
	}
}
