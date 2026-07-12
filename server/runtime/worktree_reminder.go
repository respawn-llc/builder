package runtime

import (
	"errors"
	"fmt"
	"strings"

	"core/server/llm"
	"core/server/session"
	"core/shared/clientui"
)

func (e *Engine) SteerWorktreeTransitionFailure(outcome clientui.WorktreeTransitionOutcome) error {
	if outcome.State != clientui.WorktreeTransitionFailed || outcome.Failure == nil {
		return errors.New("failed worktree transition outcome is required")
	}
	diagnostic := strings.TrimSpace(outcome.Failure.Diagnostic)
	if diagnostic == "" {
		return errors.New("worktree transition failure diagnostic is required")
	}
	return e.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{
		Role:        llm.RoleDeveloper,
		MessageType: llm.MessageTypeErrorFeedback,
		Content: fmt.Sprintf(
			"Scheduled worktree %s transition %s failed: %s",
			outcome.Transition,
			outcome.OperationID.String(),
			diagnostic,
		),
	}}))
}

func (e *Engine) materializePendingWorktreeReminder(stepID string) error {
	return e.materializePendingWorktreeReminderWithOptions(stepID, worktreeReminderMaterializationOptions{})
}

func (e *Engine) materializePendingWorktreeReminderAfterCompaction(stepID string, previousCompactionCount int) error {
	if e.compactionRuntimeState().Count() == previousCompactionCount {
		return nil
	}
	return e.materializePendingWorktreeReminderWithOptions(stepID, worktreeReminderMaterializationOptions{ignoreChatEntryDedupe: true})
}

type worktreeReminderMaterializationOptions struct {
	ignoreChatEntryDedupe bool
}

const worktreeReminderDedupeTailLimit = 512

func (e *Engine) materializePendingWorktreeReminderWithOptions(stepID string, opts worktreeReminderMaterializationOptions) error {
	state := cloneRuntimeWorktreeReminderState(e.store.Meta().WorktreeReminder)
	compactionCount := e.compactionRuntimeState().Count()
	if !shouldInjectWorktreeReminder(state, compactionCount) {
		return nil
	}
	message, ok := worktreeReminderMessage(*state)
	if !ok {
		return nil
	}
	if latestMaterializedWorktreeReminderMatches(e.transcriptRuntimeState().SnapshotItems(), message) || (!opts.ignoreChatEntryDedupe && latestMaterializedWorktreeReminderEntryMatches(e.recentTranscriptEntries(worktreeReminderDedupeTailLimit), message)) {
		state.HasIssuedInGeneration = true
		state.IssuedCompactionCount = compactionCount
		return e.store.SetWorktreeReminderState(state)
	}
	if err := e.steer(stepID, steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{message})); err != nil {
		return err
	}
	state.HasIssuedInGeneration = true
	state.IssuedCompactionCount = e.compactionRuntimeState().Count()
	return e.store.SetWorktreeReminderState(state)
}

func latestMaterializedWorktreeReminderMatches(items []llm.ResponseItem, message llm.Message) bool {
	for idx := len(items) - 1; idx >= 0; idx-- {
		item := items[idx]
		if item.Type != llm.ResponseItemTypeMessage ||
			(item.MessageType != llm.MessageTypeWorktreeMode && item.MessageType != llm.MessageTypeWorktreeModeExit) {
			continue
		}
		return item.Role == message.Role &&
			item.MessageType == message.MessageType &&
			strings.TrimSpace(item.Content) == strings.TrimSpace(message.Content) &&
			strings.TrimSpace(item.CompactContent) == strings.TrimSpace(message.CompactContent) &&
			strings.TrimSpace(item.SourcePath) == strings.TrimSpace(message.SourcePath)
	}
	return false
}

func latestMaterializedWorktreeReminderEntryMatches(entries []ChatEntry, message llm.Message) bool {
	for idx := len(entries) - 1; idx >= 0; idx-- {
		entry := entries[idx]
		if entry.MessageType != llm.MessageTypeWorktreeMode && entry.MessageType != llm.MessageTypeWorktreeModeExit {
			continue
		}
		return entry.MessageType == message.MessageType &&
			strings.TrimSpace(entry.Text) == strings.TrimSpace(message.Content) &&
			strings.TrimSpace(entry.CondensedText) == strings.TrimSpace(message.CompactContent) &&
			strings.TrimSpace(entry.SourcePath) == strings.TrimSpace(message.SourcePath)
	}
	return false
}

func shouldInjectWorktreeReminder(state *session.WorktreeReminderState, compactionCount int) bool {
	if state == nil {
		return false
	}
	if !state.HasIssuedInGeneration {
		return true
	}
	return state.IssuedCompactionCount != compactionCount
}

func worktreeReminderMessage(state session.WorktreeReminderState) (llm.Message, bool) {
	switch state.Mode {
	case session.WorktreeReminderModeEnter:
		return worktreeModeMetaMessage(state)
	case session.WorktreeReminderModeExit:
		return worktreeModeExitMetaMessage(state)
	default:
		return llm.Message{}, false
	}
}

func cloneRuntimeWorktreeReminderState(state *session.WorktreeReminderState) *session.WorktreeReminderState {
	if state == nil {
		return nil
	}
	copyState := *state
	copyState.Branch = strings.TrimSpace(copyState.Branch)
	copyState.WorktreePath = strings.TrimSpace(copyState.WorktreePath)
	copyState.WorkspaceRoot = strings.TrimSpace(copyState.WorkspaceRoot)
	copyState.EffectiveCwd = strings.TrimSpace(copyState.EffectiveCwd)
	return &copyState
}
