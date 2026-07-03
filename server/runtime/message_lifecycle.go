package runtime

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"core/server/llm"
	"core/server/session"
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
	activeListEvents, err := e.store.ReadEventsBackwardUntil(isCompactionSegmentBoundary)
	if err != nil {
		return err
	}
	for _, evt := range activeListEvents {
		switch evt.Kind {
		case "message":
			var msg llm.Message
			if err := json.Unmarshal(evt.Payload, &msg); err != nil {
				return fmt.Errorf("decode message event: %w", err)
			}
			newTranscriptPersistenceCoordinator(e.transcriptRuntimeState()).AppendMessage(msg)
			recoveredHandoff.ApplyMessage(msg)
			if isCompactionSoonReminderMessage(msg) {
				reminderIssued = true
			}
		case "tool_completed":
			if err := newTranscriptPersistenceCoordinator(e.transcriptRuntimeState()).RestoreToolCompletionPayload(evt.Payload); err != nil {
				return err
			}
			if err := recoveredHandoff.ApplyToolCompletion(evt.Payload); err != nil {
				return err
			}
		case "local_entry":
			var entry storedLocalEntry
			if err := json.Unmarshal(evt.Payload, &entry); err != nil {
				return fmt.Errorf("decode local_entry event: %w", err)
			}
			e.diagnosticDedupeStore().RestoreLocal(entry.DiagnosticKey)
			newTranscriptPersistenceCoordinator(e.transcriptRuntimeState()).AppendLocalEntryRecord(*localEntryChatEntry(entry))
		case sessionEventCacheWarning:
			if err := applyPersistedCacheWarningToTranscript(newTranscriptPersistenceCoordinator(e.transcriptRuntimeState()), evt.Payload, e.cfg.CacheWarningMode); err != nil {
				return err
			}
		case sessionEventCacheRequestObserved:
			if err := e.restorePromptCacheRequest(evt.Payload); err != nil {
				return err
			}
		case sessionEventCacheResponseObserved:
			if err := e.restorePromptCacheResponse(evt.Payload); err != nil {
				return err
			}
		case "history_replaced":
			payload, ignoredLegacy, err := decodePersistedHistoryReplacementPayload(evt.Payload)
			if err != nil {
				return fmt.Errorf("%w: %w", errDecodeHistoryReplacedEvent, err)
			}
			if ignoredLegacy {
				continue
			}
			e.resetLocalDiagnostics()
			newTranscriptPersistenceCoordinator(e.transcriptRuntimeState()).ReplaceHistory(payload.Items)
			e.transcriptRuntimeState().SeedLastCommittedAssistantFinalAnswerIfEmpty(payload.LastCommittedAssistantFinalAnswer)
			if payload.CompactionNumber > 0 {
				e.compactionRuntimeState().SetCount(payload.CompactionNumber)
			} else {
				e.compactionRuntimeState().IncrementCount()
			}
			e.compactionRuntimeState().SetLastWorkflowRunID(payload.WorkflowRunID)
			recoveredHandoff.ClearSatisfiedByCompaction()
			recoveredHandoff.SeedFutureMessage(payload.PendingHandoffFutureMessage)
			reminderIssued = false
		}
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
	return msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeCompactionSoonReminder && strings.TrimSpace(msg.Content) != ""
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
	if msg.MessageType == llm.MessageTypeHandoffFutureMessage && strings.TrimSpace(msg.Content) != "" {
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

func (r *persistedHandoffRecovery) ApplyToolCompletion(payload []byte) error {
	if r == nil {
		return nil
	}
	var completion storedToolCompletion
	if err := json.Unmarshal(payload, &completion); err != nil {
		return fmt.Errorf("decode tool_completed event: %w", err)
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
		content := strings.TrimSpace(item.message.message.Content)
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

// FlushPendingUserInjections flushes queued user injections for the step. An empty
// queueItemIDs flushes every pending injection; a non-empty set flushes only those
// IDs.
func (m *defaultMessageLifecycle) FlushPendingUserInjections(stepID string, queueItemIDs map[string]struct{}) (int, error) {
	var pending []queuedUserSteeringIntent
	if len(queueItemIDs) == 0 {
		pending = m.queue.Drain()
	} else {
		pending = m.queue.DrainByID(queueItemIDs)
	}
	return m.flushPendingUserInjections(stepID, pending)
}

func (m *defaultMessageLifecycle) flushPendingUserInjections(stepID string, pending []queuedUserSteeringIntent) (int, error) {
	e := m.engine
	flushed := 0
	pendingNotices := []steeringIntent(nil)
	if m.background != nil {
		pendingNotices = m.background.DrainPendingNotices()
	}

	queuedMessages := normalizeQueuedUserMessages(pending)
	if len(queuedMessages) > 0 {
		joined := strings.Join(queuedMessages, "\n\n")
		if err := e.steer(stepID, steerQueuedUserMessageFlushIntent(joined, queuedMessages, queuedUserMessagesForFlush(pending))); err != nil {
			m.queue.RestoreFront(pending)
			return flushed, err
		}
		flushed++
	}
	for _, item := range pending {
		e.unmarkQueuedUserInjectionForAutoDrain(item.message.ID)
	}
	for _, notice := range pendingNotices {
		if err := e.steer(stepID, notice); err != nil {
			return flushed, err
		}
		flushed++
	}
	return flushed, nil
}

func (m *defaultMessageLifecycle) QueueUserMessage(text string, clientRequestID string) QueuedUserMessage {
	if m == nil || m.queue == nil {
		return QueuedUserMessage{}
	}
	return m.queue.Queue(text, clientRequestID)
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

func (m *defaultMessageLifecycle) DiscardQueuedUserMessage(queueItemID string) (QueuedUserMessage, bool) {
	if m == nil || m.queue == nil {
		return QueuedUserMessage{}, false
	}
	return m.queue.DiscardItem(queueItemID)
}

func (m *defaultMessageLifecycle) HasPendingUserInjections() bool {
	return m != nil && m.queue != nil && m.queue.HasPending()
}

func newActiveMetaContextBuilder(meta session.Meta, model, thinkingLevel, globalConfigDir string, disabledSkills map[string]bool, now time.Time) metaContextBuilder {
	roots := activeMetaContextRootsForMeta(meta)
	return newMetaContextBuilder(roots.discoveryRoot, model, thinkingLevel, disabledSkills, now).withEnvironmentCWD(roots.environmentCWD).withGlobalConfigDir(globalConfigDir)
}

type activeMetaContextRoots struct {
	discoveryRoot  string
	environmentCWD string
}

func activeMetaContextRootsForMeta(meta session.Meta) activeMetaContextRoots {
	workspaceRoot := strings.TrimSpace(meta.WorkspaceRoot)
	roots := activeMetaContextRoots{discoveryRoot: workspaceRoot, environmentCWD: workspaceRoot}
	state := cloneRuntimeWorktreeReminderState(meta.WorktreeReminder)
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
