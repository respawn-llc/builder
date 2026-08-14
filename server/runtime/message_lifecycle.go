package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/config"
	"core/shared/runtimeids"
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
	manualEligible := false
	type restoredToolGeneration struct {
		expected  map[string]struct{}
		completed map[string]struct{}
	}
	generationsByStep := make(map[string][]*restoredToolGeneration)
	for _, record := range activeWindow.Records {
		stepIDPointer := record.StepID()
		stepID, _ := textutil.OptionalExact(stepIDPointer)
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
			provenance, provenanceErr := transcriptProvenanceFromRecord(record)
			if provenanceErr != nil {
				return fmt.Errorf("restore session message provenance: %w", provenanceErr)
			}
			if err := e.transcriptRuntimeState().AppendMessage(stepIDPointer, msg, &provenance); err != nil {
				return fmt.Errorf("restore session message projection: %w", err)
			}
			if msg.Role == llm.RoleAssistant && len(msg.ToolCalls) > 0 {
				expected := make(map[string]struct{}, len(msg.ToolCalls))
				userShellGeneration := true
				for _, call := range msg.ToolCalls {
					if call.ID != "" {
						expected[call.ID] = struct{}{}
					}
					if !isUserInitiatedShellCall(call) {
						userShellGeneration = false
					}
				}
				if len(expected) > 0 && !userShellGeneration {
					generationsByStep[stepID] = append(generationsByStep[stepID], &restoredToolGeneration{
						expected:  expected,
						completed: make(map[string]struct{}, len(expected)),
					})
				}
			} else if msg.Role == llm.RoleAssistant &&
				((msg.Content != nil && strings.TrimSpace(*msg.Content) != "") || len(msg.ReasoningItems) > 0) {
				manualEligible = true
			}
			recoveredHandoff.ApplyMessage(msg)
			if isCompactionSoonReminderMessage(msg) {
				reminderIssued = true
			}
		case session.ToolCompletionRecord:
			provenance, provenanceErr := transcriptProvenanceFromRecord(record)
			if provenanceErr != nil {
				return fmt.Errorf("restore session tool completion provenance: %w", provenanceErr)
			}
			if err := e.transcriptRuntimeState().RestoreToolCompletionRecord(payload, &provenance); err != nil {
				return err
			}
			for _, generation := range generationsByStep[stepID] {
				if _, ok := generation.expected[payload.CallID]; ok {
					generation.completed[payload.CallID] = struct{}{}
				}
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
			restored := *localEntryChatEntryForStep(entry, stepIDPointer)
			provenance, provenanceErr := transcriptProvenanceFromRecord(record)
			if provenanceErr != nil {
				return fmt.Errorf("restore session local entry provenance: %w", provenanceErr)
			}
			e.transcriptRuntimeState().AppendLocalEntryRecord(restored, entry.AfterToolCallID, &provenance)
		case session.ReviewerFeedbackRecord:
			provenance, provenanceErr := transcriptProvenanceFromRecord(record)
			if provenanceErr != nil {
				return fmt.Errorf("restore Reviewer feedback provenance: %w", provenanceErr)
			}
			e.transcriptRuntimeState().AppendLocalEntryRecord(
				reviewerFeedbackChatEntryFromSessionRecord(payload, stepID, &provenance), nil,
			)
		case session.ReviewerErrorRecord:
			provenance, provenanceErr := transcriptProvenanceFromRecord(record)
			if provenanceErr != nil {
				return fmt.Errorf("restore Reviewer error provenance: %w", provenanceErr)
			}
			e.transcriptRuntimeState().AppendLocalEntryRecord(
				reviewerErrorChatEntryFromSessionRecord(payload, stepID, &provenance), nil,
			)
		case session.CacheWarningRecord:
			provenance, provenanceErr := transcriptProvenanceFromRecord(record)
			if provenanceErr != nil {
				return fmt.Errorf("restore session cache warning provenance: %w", provenanceErr)
			}
			applyPersistedCacheWarningToTranscript(e.transcriptRuntimeState(), payload, e.cfg.CacheWarningMode, &provenance)
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
			manualEligible = false
			provenance, provenanceErr := transcriptProvenanceFromRecord(record)
			if provenanceErr != nil {
				return fmt.Errorf("restore session history replacement provenance: %w", provenanceErr)
			}
			projectedEntries := transcriptEntriesFromHistoryReplacement(
				replacement.Items,
				replacement.CompactionNumber,
			)
			for index := range projectedEntries {
				projectedEntries[index].StepID = exactStepIDPointer(stepID)
			}
			projectedEntries = assignHistoryReplacementEntryProvenance(projectedEntries, &provenance)
			e.transcriptRuntimeState().ReplaceHistoryAtCommittedEntryStart(
				record.StepID(),
				replacement.Items,
				replacement.CommittedEntryStart,
				projectedEntries,
			)
			replacementMode := session.CompactionMode(replacement.Mode)
			if err := e.compactionRuntimeState().SetHistoryReplacementMode(&replacementMode); err != nil {
				return fmt.Errorf("restore history replacement mode: %w", err)
			}
			if replacement.LastCommittedAssistantFinalAnswer != nil {
				e.transcriptRuntimeState().SeedLastCommittedAssistantFinalAnswerIfAbsent(
					replacement.LastCommittedAssistantFinalAnswer,
				)
			}
			if replacement.CompactionNumber != nil && *replacement.CompactionNumber > 0 {
				e.compactionRuntimeState().SetCount(*replacement.CompactionNumber)
			} else {
				count := e.compactionRuntimeState().IncrementCount()
				e.persistCompletedCompactionFactsBestEffort(stepID, count)
			}
			rollbackLocator.ObserveHistoryReplacement(replacement)
			recoveredHandoff.ClearSatisfiedByCompaction()
			if replacement.PendingHandoffFutureMessage != nil {
				recoveredHandoff.SeedFutureMessage(*replacement.PendingHandoffFutureMessage)
			}
			reminderIssued = false
		}
		e.compactionRuntimeState().ApplyWorkflowPostCompletionActivity(
			workflowPostCompletionActivityForSessionRecord(payload),
		)
	}
	for _, generations := range generationsByStep {
		for _, generation := range generations {
			if len(generation.expected) > 0 && len(generation.completed) == len(generation.expected) {
				manualEligible = true
				break
			}
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
	e.compactionRuntimeState().SetManualCompactionEligible(manualEligible)
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

func workflowPostCompletionActivityForSessionRecord(payload any) workflowPostCompletionActivity {
	switch record := payload.(type) {
	case session.MessageRecord:
		message := llm.Message{
			Role:            llm.Role(record.Role),
			SourcePath:      textutil.Pointer(record.SourcePath),
			WorktreeContext: session.CloneWorktreeContext(record.WorktreeContext),
		}
		if record.MessageType != nil {
			messageType := llm.MessageType(*record.MessageType)
			message.MessageType = &messageType
		}
		return workflowPostCompletionMessageActivity(message)
	case session.ToolCompletionRecord:
		return workflowPostCompletionDurableActivity
	case session.CacheRequestObservationRecord:
		return workflowPostCompletionNoActivity
	default:
		return workflowPostCompletionNoActivity
	}
}

func isUserInitiatedShellCall(call llm.ToolCall) bool {
	if call.Name != string(toolspec.ToolExecCommand) {
		return false
	}
	return tools.ParseShellToolCallUserInitiated(call.Input)
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

func queuedUserMessageText(message QueuedUserMessage) (string, error) {
	text, err := message.DisplayText()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(text), nil
}

type queuedUserMessageFlushGroup struct {
	message    llm.Message
	batch      []string
	queueItems []QueuedUserMessage
	pending    []queuedUserMessage
}

func queuedUserMessageFlushGroups(messages []queuedUserMessage) ([]queuedUserMessageFlushGroup, error) {
	groups := make([]queuedUserMessageFlushGroup, 0, len(messages))
	for _, pending := range messages {
		text, err := queuedUserMessageText(pending.message)
		if err != nil {
			return nil, err
		}
		if text == "" {
			continue
		}
		queueItems, err := queuedUserMessagesForFlush([]queuedUserMessage{pending})
		if err != nil {
			return nil, err
		}
		if len(queueItems) == 0 {
			continue
		}
		message := pending.message.Message
		if message.Role == llm.RoleUser && len(groups) > 0 && groups[len(groups)-1].message.Role == llm.RoleUser {
			group := &groups[len(groups)-1]
			group.message.Content = textutil.Value(strings.Join([]string{*group.message.Content, text}, "\n\n"))
			group.batch = append(group.batch, text)
			group.queueItems = append(group.queueItems, queueItems...)
			group.pending = append(group.pending, pending)
			continue
		}
		groups = append(groups, queuedUserMessageFlushGroup{
			message:    message,
			batch:      []string{text},
			queueItems: queueItems,
			pending:    []queuedUserMessage{pending},
		})
	}
	return groups, nil
}

func queuedUserMessagesForFlush(messages []queuedUserMessage) ([]QueuedUserMessage, error) {
	items := make([]QueuedUserMessage, 0, len(messages))
	for _, message := range messages {
		item := message.message
		item.ID = strings.TrimSpace(item.ID)
		text, err := queuedUserMessageText(item)
		if err != nil {
			return nil, err
		}
		if item.ID == "" || text == "" {
			continue
		}
		items = append(items, item)
	}
	return items, nil
}

func (m *defaultMessageLifecycle) FlushPendingUserInjections(stepID string, selection userInjectionSelection) (userInjectionCommitResult, error) {
	result, err := m.CommitPendingUserInjections(stepID, selection)
	if err != nil || result.disposition != userInjectionFlushContinue {
		return result, err
	}
	if m.background != nil {
		flushed, flushErr := m.background.flushPendingNotices(textutil.Value(stepID))
		result.flushed += flushed
		if flushErr != nil {
			return result, flushErr
		}
	}
	return result, nil
}

func (m *defaultMessageLifecycle) CommitPendingUserInjections(stepID string, selection userInjectionSelection) (userInjectionCommitResult, error) {
	var pending []queuedUserMessage
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

func (m *defaultMessageLifecycle) commitPendingUserInjections(stepID string, pending []queuedUserMessage) (userInjectionCommitResult, error) {
	e := m.engine
	result := userInjectionCommitResult{disposition: userInjectionFlushContinue}

	groups, err := queuedUserMessageFlushGroups(pending)
	if err != nil {
		return result, err
	}
	for groupIndex, group := range groups {
		receipt, err := e.steerWithCommitReceipt(
			stepID,
			steerQueuedUserMessageFlushIntent(group.message, group.batch, group.queueItems),
		)
		result.receipt = receipt
		if err != nil {
			if !result.receipt.Committed {
				tail := make([]queuedUserMessage, 0, len(groups)-groupIndex)
				for _, remaining := range groups[groupIndex:] {
					for _, item := range remaining.queueItems {
						for _, original := range pending {
							if original.message.ID == item.ID {
								tail = append(tail, original)
								break
							}
						}
					}
				}
				err = errors.Join(err, e.steer(stepID, steerQueuedUserMessageRestoreIntent(tail)))
			}
			return result, err
		}
		if result.queueItemIDs == nil {
			result.queueItemIDs = queuedUserMessageIDSet(group.queueItems)
		} else {
			for queueItemID := range queuedUserMessageIDSet(group.queueItems) {
				result.queueItemIDs[queueItemID] = struct{}{}
			}
		}
		result.flushed++
	}
	return result, nil
}

func (m *defaultMessageLifecycle) QueueUserMessage(text string, association ...queuedUserMessageAssociation) (QueuedUserMessage, error) {
	if m == nil || m.queue == nil {
		return QueuedUserMessage{}, errors.New("queued user message lifecycle is required")
	}
	return m.queue.Queue(text, association...)
}

func (m *defaultMessageLifecycle) QueueUserMessageWithID(item QueuedUserMessage, association ...queuedUserMessageAssociation) (QueuedUserMessage, error) {
	if m == nil || m.queue == nil {
		return QueuedUserMessage{}, errors.New("queued user message lifecycle is required")
	}
	return m.queue.QueueItem(item, association...)
}

func (m *defaultMessageLifecycle) DrainPendingUserInjectionsByScope(scopeID runtimeids.ExecutionScopeID) []interruptedHumanSteering {
	if m == nil || m.queue == nil {
		return nil
	}
	return m.queue.DrainByScope(scopeID)
}

func (m *defaultMessageLifecycle) DrainInterruptedUserInjections() []interruptedHumanSteering {
	if m == nil || m.queue == nil {
		return nil
	}
	return m.queue.DrainInterrupted()
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

func (m *defaultMessageLifecycle) PendingUserMessages() []QueuedUserMessage {
	if m == nil || m.queue == nil {
		return nil
	}
	return m.queue.Snapshot()
}

func (m *defaultMessageLifecycle) RestorePendingUserInjections(items []queuedUserMessage) {
	if m == nil || m.queue == nil {
		return
	}
	m.queue.RestoreFront(items)
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

func newActiveMetaContextBuilder(meta session.Meta, executionRoot, model, thinkingLevel, globalConfigDir string, skillPolicy config.SkillPolicy, now time.Time) metaContextBuilder {
	roots := activeMetaContextRootsForMeta(meta, executionRoot)
	builder := newMetaContextBuilder(roots.discoveryRoot, model, thinkingLevel, skillPolicy, now).
		withEnvironmentCWD(roots.environmentCWD).
		withGlobalConfigDir(globalConfigDir)
	return builder
}

type activeMetaContextRoots struct {
	discoveryRoot  string
	environmentCWD string
}

func activeMetaContextRootsForMeta(meta session.Meta, executionRoot string) activeMetaContextRoots {
	activeRoot := strings.TrimSpace(executionRoot)
	if activeRoot == "" {
		activeRoot = strings.TrimSpace(meta.WorkspaceRoot)
	}
	roots := activeMetaContextRoots{discoveryRoot: activeRoot, environmentCWD: activeRoot}
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
