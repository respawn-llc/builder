package runtime

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/shared/config"
	"core/shared/textutil"
	"core/shared/toolspec"
)

type defaultMessageLifecycle struct {
	engine     *Engine
	background backgroundNoticeScheduler
	queue      *queuedUserMessageStore
}

func newDefaultMessageLifecycle(engine *Engine, background backgroundNoticeScheduler) *defaultMessageLifecycle {
	return &defaultMessageLifecycle{
		engine:     engine,
		background: background,
		queue:      newQueuedUserMessageStore(),
	}
}

func (m *defaultMessageLifecycle) RestoreMessages() error {
	e := m.engine
	meta := e.store.Meta()
	recoveredHandoff := newPersistedHandoffRecovery()
	reminderIssued := meta.CompactionSoonReminderIssued
	var matchErr error
	activeWindow, err := e.eventLog.ReadNewestSegmentBackward(compactionBoundaryMatcher(&matchErr))
	if err != nil {
		return err
	}
	if matchErr != nil {
		return matchErr
	}
	var rollbackLocator rollbackCandidateLocatorTracker
	for _, record := range activeWindow.Records {
		stepID, _ := textutil.OptionalExact(record.StepID())
		payload, err := record.Payload()
		if err != nil {
			return err
		}
		switch payload := payload.(type) {
		case session.MessageRecord:
			msg, err := llmMessageFromSessionRecord(payload)
			if err != nil {
				return fmt.Errorf("restore session message record: %w", err)
			}
			if err := rollbackLocator.ObserveMessage(record.Seq(), msg); err != nil {
				return err
			}
			if err := e.transcriptRuntimeState().AppendMessage(stepID, msg); err != nil {
				return fmt.Errorf("restore session message projection: %w", err)
			}
			recoveredHandoff.ApplyMessage(msg)
			if isCompactionSoonReminderMessage(msg) {
				reminderIssued = true
			}
		case session.ToolCompletionRecord:
			if err := e.transcriptRuntimeState().RestoreToolCompletionRecord(payload); err != nil {
				return err
			}
			completion, err := storedToolCompletionFromSessionRecord(payload)
			if err != nil {
				return fmt.Errorf("restore session tool completion record: %w", err)
			}
			if err := recoveredHandoff.ApplyToolCompletion(completion); err != nil {
				return err
			}
		case session.LocalEntryRecord:
			entry, err := storedLocalEntryFromSessionRecord(payload)
			if err != nil {
				return fmt.Errorf("restore session local entry record: %w", err)
			}
			if entry.DiagnosticKey != nil {
				e.diagnosticDedupeStore().RestoreLocal(*entry.DiagnosticKey)
			}
			restored := *localEntryChatEntryForStep(entry, stepID)
			e.transcriptRuntimeState().AppendLocalEntryRecord(restored, entry.AfterToolCallID)
		case session.CacheWarningRecord:
			applyPersistedCacheWarningToTranscript(e.transcriptRuntimeState(), payload, e.cfg.CacheWarningMode)
		case session.CacheRequestObservationRecord:
			// Cache requests are observational. The following response record
			// reconstructs the cache tracker state.
		case session.CacheResponseObservationRecord:
			if cache := e.modelRequests().RequestCache(); cache != nil {
				cache.RecordResponse(persistedCacheResponseObservedFromSessionRecord(payload))
			}
		case session.HistoryReplacementRecord:
			replacement, err := historyReplacementPayloadFromSessionRecord(payload)
			if err != nil {
				return fmt.Errorf("restore session history replacement record: %w", err)
			}
			e.resetLocalDiagnostics()
			e.transcriptRuntimeState().ReplaceHistoryAtCommittedEntryStart(stepID, replacement.Items, replacement.CommittedEntryStart)
			if replacement.LastCommittedAssistantFinalAnswer != nil {
				e.transcriptRuntimeState().SeedLastCommittedAssistantFinalAnswerIfEmpty(
					*replacement.LastCommittedAssistantFinalAnswer,
				)
			}
			if replacement.CompactionNumber != nil && *replacement.CompactionNumber > 0 {
				e.compactionRuntimeState().SetCount(*replacement.CompactionNumber)
			} else {
				e.compactionRuntimeState().IncrementCount()
			}
			rollbackLocator.ObserveHistoryReplacement(replacement)
			recoveredHandoff.ClearSatisfiedByCompaction()
			if replacement.PendingHandoffFutureMessage != nil {
				recoveredHandoff.SeedFutureMessage(*replacement.PendingHandoffFutureMessage)
			}
			reminderIssued = false
		}
	}
	restoredRollbackCandidate, err := rollbackLocator.Resolve(activeWindow.EndOffset)
	if err != nil {
		return fmt.Errorf("restore latest rollback candidate: %w", err)
	}
	if restoredRollbackCandidate != nil {
		e.transcriptRuntimeState().SetLatestRollbackCandidate(*restoredRollbackCandidate)
	}
	e.compactionRuntimeState().SetSoonReminderIssued(reminderIssued)
	if err := e.store.SetCompactionSoonReminderIssued(reminderIssued); err != nil {
		return err
	}
	// Base meta context is injected once at the birth of a session's active list
	// (fresh-session boot injects it first; compaction reinjects it into the
	// history_replaced payload). Any restored history therefore already carries
	// it, so a non-empty restore means injection has happened. This is a
	// deterministic length check, never a scan of which messages are present.
	e.baseMetaInjected = len(e.transcriptRuntimeState().SnapshotMessages()) > 0
	if futureMessage := recoveredHandoff.PendingFutureMessage(); futureMessage != "" {
		e.handoffRuntimeState().QueueFutureMessage(futureMessage)
	}
	if req, ok := recoveredHandoff.PendingRequest(); ok {
		e.handoffRuntimeState().QueueRequest(req.summarizerPrompt, req.futureAgentMessage)
	}
	return nil
}

func isCompactionSoonReminderMessage(msg llm.Message) bool {
	return msg.Role == llm.RoleDeveloper &&
		msg.MessageType != nil &&
		*msg.MessageType == llm.MessageTypeCompactionSoonReminder &&
		msg.Content != nil &&
		strings.TrimSpace(*msg.Content) != ""
}

type persistedHandoffRecovery struct {
	toolCalls            map[string]llm.ToolCall
	pending              *handoffRequest
	pendingFutureMessage string
}

func newPersistedHandoffRecovery() *persistedHandoffRecovery {
	return &persistedHandoffRecovery{toolCalls: make(map[string]llm.ToolCall)}
}

func (r *persistedHandoffRecovery) ApplyMessage(msg llm.Message) {
	if r == nil {
		return
	}
	if msg.MessageType != nil &&
		*msg.MessageType == llm.MessageTypeHandoffFutureMessage &&
		msg.Content != nil &&
		strings.TrimSpace(*msg.Content) != "" {
		r.pendingFutureMessage = ""
	}
	if msg.Role != llm.RoleAssistant {
		return
	}
	for _, call := range msg.ToolCalls {
		if toolspec.ID(strings.TrimSpace(call.Name)) != toolspec.ToolTriggerHandoff {
			continue
		}
		callID := strings.TrimSpace(call.ID)
		if callID == "" {
			continue
		}
		r.toolCalls[callID] = llm.ToolCall{
			ID:    callID,
			Name:  string(toolspec.ToolTriggerHandoff),
			Input: append(json.RawMessage(nil), call.Input...),
		}
	}
}

func (r *persistedHandoffRecovery) ApplyToolCompletion(completion storedToolCompletion) error {
	if r == nil {
		return nil
	}
	if toolspec.ID(strings.TrimSpace(completion.Name)) != toolspec.ToolTriggerHandoff || completion.IsError {
		delete(r.toolCalls, strings.TrimSpace(completion.CallID))
		return nil
	}
	callID := strings.TrimSpace(completion.CallID)
	if callID == "" {
		return nil
	}
	call, ok := r.toolCalls[callID]
	if !ok {
		return nil
	}
	delete(r.toolCalls, callID)
	req, ok := handoffRequestFromToolCall(call)
	if !ok {
		return nil
	}
	r.pending = req
	return nil
}

func (r *persistedHandoffRecovery) SeedFutureMessage(message string) {
	if r == nil {
		return
	}
	if trimmed := strings.TrimSpace(message); trimmed != "" {
		r.pendingFutureMessage = trimmed
	}
}

func (r *persistedHandoffRecovery) ClearSatisfiedByCompaction() {
	if r == nil {
		return
	}
	if r.pending != nil {
		if futureMessage := strings.TrimSpace(r.pending.futureAgentMessage); futureMessage != "" {
			r.pendingFutureMessage = futureMessage
		}
	}
	r.pending = nil
	r.toolCalls = make(map[string]llm.ToolCall)
}

func (r *persistedHandoffRecovery) PendingFutureMessage() string {
	if r == nil {
		return ""
	}
	return strings.TrimSpace(r.pendingFutureMessage)
}

func (r *persistedHandoffRecovery) PendingRequest() (*handoffRequest, bool) {
	if r == nil || r.pending == nil {
		return nil, false
	}
	req := *r.pending
	return &req, true
}

func handoffRequestFromToolCall(call llm.ToolCall) (*handoffRequest, bool) {
	if toolspec.ID(strings.TrimSpace(call.Name)) != toolspec.ToolTriggerHandoff {
		return nil, false
	}
	var input struct {
		SummarizerPrompt   string `json:"summarizer_prompt,omitempty"`
		FutureAgentMessage string `json:"future_agent_message,omitempty"`
	}
	if len(call.Input) > 0 {
		if err := json.Unmarshal(call.Input, &input); err != nil {
			return nil, false
		}
	}
	return &handoffRequest{
		summarizerPrompt:   strings.TrimSpace(input.SummarizerPrompt),
		futureAgentMessage: strings.TrimSpace(input.FutureAgentMessage),
	}, true
}

func normalizeQueuedUserMessages(messages []queuedUserSteeringIntent) []string {
	out := make([]string, 0, len(messages))
	for _, message := range messages {
		trimmed := queuedUserSteeringIntentText(message.intent)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func queuedUserSteeringIntentText(intent steeringIntent) string {
	parts := make([]string, 0, len(intent.items))
	for _, item := range intent.items {
		if item.message == nil || item.message.message.Role != llm.RoleUser {
			continue
		}
		if item.message.message.Content == nil {
			continue
		}
		content := strings.TrimSpace(*item.message.message.Content)
		if content == "" {
			continue
		}
		parts = append(parts, content)
	}
	return strings.Join(parts, "\n\n")
}

func queuedUserMessagesForFlush(messages []queuedUserSteeringIntent) []QueuedUserMessage {
	items := make([]QueuedUserMessage, 0, len(messages))
	for _, message := range messages {
		item := message.message
		item.ID = strings.TrimSpace(item.ID)
		item.ClientRequestID = strings.TrimSpace(item.ClientRequestID)
		if item.ID == "" || queuedUserSteeringIntentText(message.intent) == "" {
			continue
		}
		items = append(items, item)
	}
	return items
}

func (m *defaultMessageLifecycle) FlushPendingUserInjections(stepID string, selection userInjectionSelection) (userInjectionCommitResult, error) {
	result, err := m.CommitPendingUserInjections(stepID, selection)
	if err != nil || !result.continueCombinedFlush {
		return result, err
	}
	pendingNotices := []queuedBackgroundNotice(nil)
	if m.background != nil {
		pendingNotices = m.background.DrainPendingNotices()
	}
	for index, notice := range pendingNotices {
		receipt, err := m.engine.steerWithCommitReceipt(stepID, notice.intent)
		if m.background != nil {
			m.background.FinalizeCommittedBackgroundNotice(notice, receipt)
		}
		if err != nil {
			if scheduler, ok := m.background.(*defaultBackgroundNoticeScheduler); ok {
				if receipt.Committed {
					scheduler.restoreRetryDeferredNoticesFront(pendingNotices[index+1:])
				} else {
					scheduler.restoreRetryDeferredNoticesFront(pendingNotices[index:])
				}
			}
			return result, err
		}
		result.flushed++
	}
	return result, nil
}

func (m *defaultMessageLifecycle) CommitPendingUserInjections(stepID string, selection userInjectionSelection) (userInjectionCommitResult, error) {
	var pending []queuedUserSteeringIntent
	switch selected := selection.(type) {
	case allPendingUserInjectionSelection:
		pending = m.queue.Drain()
	case steerUserInjectionSelection:
		if len(selected.queueItemIDs) > 0 {
			pending = m.queue.DrainByID(selected.queueItemIDs)
		}
	default:
		return userInjectionCommitResult{}, fmt.Errorf("unsupported user injection selection %T", selection)
	}
	return m.commitPendingUserInjections(stepID, pending)
}

func (m *defaultMessageLifecycle) commitPendingUserInjections(stepID string, pending []queuedUserSteeringIntent) (userInjectionCommitResult, error) {
	e := m.engine
	result := userInjectionCommitResult{continueCombinedFlush: true}

	// Recheck immediately before commit because a live-run stop can race the drain.
	pending = e.dropStoppedLiveRunQueueItems(pending)
	queuedMessages := normalizeQueuedUserMessages(pending)
	if len(queuedMessages) > 0 {
		queueItems := queuedUserMessagesForFlush(pending)
		result.queueItemIDs = queuedUserMessageIDSet(queueItems)
		joined := strings.Join(queuedMessages, "\n\n")
		publishAllowed, err := e.commitLiveRunQueueItemsUnlessStopped(pending, func() error {
			receipt, persistErr := e.steerWithCommitReceipt(
				stepID,
				steerQueuedUserMessageFlushIntent(joined, queuedMessages, queueItems),
			)
			result.receipt = receipt
			return persistErr
		})
		if err != nil {
			if !result.receipt.Committed {
				m.queue.RestoreFront(pending)
			}
			return result, err
		}
		if !publishAllowed {
			for _, item := range pending {
				e.unmarkQueuedUserInjectionForAutoDrain(item.message.ID)
				e.emitQueuedUserMessageStatus(item.message, QueuedUserMessageFailed, QueuedUserMessageFailureStopped, true)
			}
			result.continueCombinedFlush = false
			return result, nil
		}
		result.flushed++
	}
	for _, item := range pending {
		e.unmarkQueuedUserInjectionForAutoDrain(item.message.ID)
	}
	return result, nil
}

func (m *defaultMessageLifecycle) QueueUserMessage(text string, clientRequestID string) QueuedUserMessage {
	if m == nil || m.queue == nil {
		return QueuedUserMessage{}
	}
	return m.queue.Queue(text, clientRequestID)
}

func (m *defaultMessageLifecycle) QueueUserMessageWithID(item QueuedUserMessage) QueuedUserMessage {
	if m == nil || m.queue == nil {
		return QueuedUserMessage{}
	}
	return m.queue.QueueItem(item)
}

func (m *defaultMessageLifecycle) DrainPendingUserInjections() []QueuedUserMessage {
	if m == nil || m.queue == nil {
		return nil
	}
	pending := m.queue.Drain()
	out := make([]QueuedUserMessage, 0, len(pending))
	for _, item := range pending {
		out = append(out, item.message)
	}
	return out
}

func (m *defaultMessageLifecycle) DrainPendingUserInjectionsByID(ids map[string]struct{}) []QueuedUserMessage {
	if m == nil || m.queue == nil || len(ids) == 0 {
		return nil
	}
	pending := m.queue.DrainByID(ids)
	out := make([]QueuedUserMessage, 0, len(pending))
	for _, item := range pending {
		out = append(out, item.message)
	}
	return out
}

func (m *defaultMessageLifecycle) DiscardQueuedUserMessage(queueItemID string) (QueuedUserMessage, bool) {
	if m == nil || m.queue == nil {
		return QueuedUserMessage{}, false
	}
	return m.queue.DiscardItem(queueItemID)
}

func (m *defaultMessageLifecycle) HasPendingUserInjections() bool {
	return m != nil && m.queue != nil && m.queue.HasPending()
}

func newActiveMetaContextBuilder(meta session.Meta, model, thinkingLevel, globalConfigDir string, skillPolicy config.SkillPolicy, now time.Time) metaContextBuilder {
	roots := activeMetaContextRootsForMeta(meta)
	builder := newMetaContextBuilder(roots.discoveryRoot, model, thinkingLevel, skillPolicy, now).
		withEnvironmentCWD(roots.environmentCWD).
		withGlobalConfigDir(globalConfigDir)
	return builder
}

type activeMetaContextRoots struct {
	discoveryRoot  string
	environmentCWD string
}

func activeMetaContextRootsForMeta(meta session.Meta) activeMetaContextRoots {
	workspaceRoot := strings.TrimSpace(meta.WorkspaceRoot)
	roots := activeMetaContextRoots{discoveryRoot: workspaceRoot, environmentCWD: workspaceRoot}
	state := session.CloneWorktreeReminderState(meta.WorktreeReminder)
	if state == nil {
		return roots
	}

	switch state.Mode {
	case session.WorktreeReminderModeEnter:
		if state.WorktreePath != "" {
			roots.discoveryRoot = state.WorktreePath
		}
	case session.WorktreeReminderModeExit:
		if state.WorkspaceRoot != "" {
			roots.discoveryRoot = state.WorkspaceRoot
		}
	}
	if state.EffectiveCwd != "" {
		roots.environmentCWD = state.EffectiveCwd
	} else {
		roots.environmentCWD = roots.discoveryRoot
	}
	return roots
}
