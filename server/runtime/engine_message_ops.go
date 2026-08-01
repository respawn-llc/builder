package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	shelltool "core/server/tools/shell"
	"core/shared/rollbacktarget"
	"core/shared/textutil"
	"core/shared/transcript"

	"github.com/google/uuid"
)

func (e *Engine) persistToolCompletionRaw(stepID string, r tools.Result) (session.CommitReceipt, error) {
	payload, backgroundSessionID, hasBackgroundSession := e.prepareStoredToolCompletion(r)
	record, adaptErr := sessionToolCompletionRecordFromStored(payload)
	if adaptErr != nil {
		return session.CommitReceipt{}, fmt.Errorf("adapt tool completion record: %w", adaptErr)
	}
	_, receipt, err := e.eventLog.AppendRecord(textutil.OptionalExactString(stepID), record)
	if receipt.Committed {
		e.applyCommittedStoredToolCompletion(
			payload,
			backgroundSessionID,
			hasBackgroundSession,
		)
	}
	return receipt, err
}

func (e *Engine) persistFinalizedToolCompletionRaw(
	stepID string,
	completion finalizedToolCompletion,
) (session.CommitReceipt, error) {
	if completion.OperatorFeedback == nil {
		return e.persistToolCompletionRaw(stepID, completion.Result)
	}
	payload, backgroundSessionID, hasBackgroundSession := e.prepareStoredToolCompletion(
		completion.Result,
	)
	feedback, normalizeErr := normalizeStoredLocalEntry(*completion.OperatorFeedback)
	if normalizeErr != nil {
		panic(fmt.Sprintf(
			"tool completion presentation fallback requires valid operator feedback (call_id=%q tool=%q error=%v)",
			completion.Result.CallID,
			completion.Result.Name,
			normalizeErr,
		))
	}
	completionRecord, adaptErr := sessionToolCompletionRecordFromStored(payload)
	if adaptErr != nil {
		return session.CommitReceipt{}, fmt.Errorf("adapt tool completion record: %w", adaptErr)
	}
	feedbackRecord, adaptErr := sessionLocalEntryRecordFromRuntime(feedback)
	if adaptErr != nil {
		return session.CommitReceipt{}, fmt.Errorf("adapt operator feedback record: %w", adaptErr)
	}
	_, receipt, err := e.eventLog.AppendRecordsAtomic(
		textutil.OptionalExactString(stepID),
		[]session.EventRecordPayload{completionRecord, feedbackRecord},
	)
	if receipt.Committed {
		e.applyCommittedStoredToolCompletion(
			payload,
			backgroundSessionID,
			hasBackgroundSession,
		)
		e.transcriptRuntimeState().AppendLocalEntryRecord(
			*localEntryChatEntryForStep(feedback, stepID),
			feedback.AfterToolCallID,
		)
	}
	return receipt, err
}

func (e *Engine) prepareStoredToolCompletion(
	r tools.Result,
) (storedToolCompletion, string, bool) {
	if r.PresentationDelta != nil {
		panic(fmt.Sprintf(
			"tool result presentation invariant violated: unconsumed presentation delta reached persistence (call_id=%q tool=%q)",
			r.CallID,
			r.Name,
		))
	}
	if r.Presentation == nil {
		panic(fmt.Sprintf(
			"tool result presentation invariant violated: live completion reached persistence without finalized presentation (call_id=%q tool=%q)",
			r.CallID,
			r.Name,
		))
	}
	backgroundSessionID, hasBackgroundSession := harvestedBackgroundCompletionSessionID(r)
	payload := storedToolCompletion{
		CallID:        r.CallID,
		Name:          string(r.Name),
		IsError:       r.IsError,
		Output:        append(json.RawMessage(nil), r.Output...),
		Summary:       r.Summary,
		CondensedText: r.CondensedText,
		Presentation:  r.Presentation,
		ProviderItems: e.providerItemsForToolCompletion(r),
	}
	return payload, backgroundSessionID, hasBackgroundSession
}

func (e *Engine) applyCommittedStoredToolCompletion(
	payload storedToolCompletion,
	backgroundSessionID string,
	hasBackgroundSession bool,
) {
	e.markCurrentRequestShapeDirtyForSignificantMutation()
	e.transcriptRuntimeState().RecordStoredToolCompletion(payload)
	if hasBackgroundSession {
		e.ensureOrchestrationCollaborators()
		if suppressor, ok := e.backgroundFlow.(interface {
			SuppressPendingBackgroundNotice(string)
		}); ok {
			suppressor.SuppressPendingBackgroundNotice(backgroundSessionID)
		} else {
			e.backgroundFlow.ConsumePendingBackgroundNotice(backgroundSessionID)
		}
	}
}

func (e *Engine) providerItemsForToolCompletion(r tools.Result) []llm.ResponseItem {
	callID := strings.TrimSpace(r.CallID)
	if callID == "" {
		return nil
	}
	var callItem *llm.ResponseItem
	for _, item := range e.transcriptRuntimeState().SnapshotItems() {
		if !isToolCallItem(item.Type) {
			continue
		}
		itemCallID, _ := textutil.FirstOptionalTrimmed(item.CallID, item.ID)
		if itemCallID != callID {
			continue
		}
		copyItem := item
		callItem = &copyItem
	}
	custom := false
	name := strings.TrimSpace(string(r.Name))
	if callItem != nil {
		custom = callItem.Type == llm.ResponseItemTypeCustomToolCall
		itemName, _ := textutil.OptionalTrimmed(callItem.Name)
		name = firstNonEmpty(name, itemName)
	}
	return llm.PrepareOpenAIInputItems([]llm.ResponseItem{{
		Type:   llm.ToolOutputItemType(custom),
		CallID: textutil.Value(callID),
		Name:   textutil.OptionalExactString(name),
		Output: append(json.RawMessage(nil), r.Output...),
	}})
}

func (e *Engine) steerPersistedDiagnosticEntry(stepID, diagnosticKey, role, text string) error {
	diagnosticKey = strings.TrimSpace(diagnosticKey)
	if diagnosticKey == "" {
		return e.steer(stepID, steerLocalEntryIntent(storedLocalEntry{
			Visibility: transcript.EntryVisibilityAuto,
			Role:       role,
			Text:       text,
		}))
	}
	if !e.diagnosticDedupeStore().BeginLocal(diagnosticKey) {
		return nil
	}
	entry := storedLocalEntry{
		Visibility:    transcript.EntryVisibilityAuto,
		Role:          role,
		Text:          text,
		DiagnosticKey: textutil.Value(diagnosticKey),
	}
	entry.Role = strings.TrimSpace(entry.Role)
	entry.Text = strings.TrimSpace(entry.Text)
	if entry.Role == "" || entry.Text == "" {
		e.diagnosticDedupeStore().ClearLocal(diagnosticKey)
		return nil
	}
	if err := e.steer(stepID, steerLocalEntryIntent(entry)); err != nil {
		e.diagnosticDedupeStore().ClearLocal(diagnosticKey)
		return err
	}
	return nil
}

func (e *Engine) appendPersistedLocalEntryRecordRaw(stepID string, entry storedLocalEntry) (session.CommitReceipt, error) {
	entry, err := normalizeStoredLocalEntry(entry)
	if err != nil {
		return session.CommitReceipt{}, fmt.Errorf("normalize local entry: %w", err)
	}
	record, adaptErr := sessionLocalEntryRecordFromRuntime(entry)
	if adaptErr != nil {
		return session.CommitReceipt{}, fmt.Errorf("adapt local entry record: %w", adaptErr)
	}
	_, receipt, err := e.eventLog.AppendRecord(textutil.OptionalExactString(stepID), record)
	if receipt.Committed {
		projected := localEntryChatEntryForStep(entry, stepID)
		e.transcriptRuntimeState().AppendLocalEntryRecord(*projected, entry.AfterToolCallID)
		e.emitRaw(Event{
			Kind:                       EventLocalEntryAdded,
			StepID:                     stepID,
			LocalEntry:                 projected,
			CommittedTranscriptChanged: true,
		})
	}
	return receipt, err
}

func normalizeStoredLocalEntry(entry storedLocalEntry) (storedLocalEntry, error) {
	entry.Role = strings.TrimSpace(entry.Role)
	entry.Text = strings.TrimSpace(entry.Text)
	entry.CondensedText = normalizeOptionalStoredLocalFact(entry.CondensedText)
	entry.DiagnosticKey = normalizeOptionalStoredLocalFact(entry.DiagnosticKey)
	entry.NoticeID = normalizeOptionalStoredLocalFact(entry.NoticeID)
	if entry.AfterToolCallID != nil {
		callID := strings.TrimSpace(*entry.AfterToolCallID)
		if callID == "" {
			return storedLocalEntry{}, errors.New("after-tool call identity is required when present")
		}
		entry.AfterToolCallID = &callID
	}
	if entry.Role == "" {
		return storedLocalEntry{}, errors.New("role is required")
	}
	if entry.Text == "" {
		return storedLocalEntry{}, errors.New("text is required")
	}
	return entry, nil
}

func localEntryChatEntry(entry storedLocalEntry) *ChatEntry {
	condensedText, _ := textutil.OptionalExact(entry.CondensedText)
	noticeID, _ := textutil.OptionalExact(entry.NoticeID)
	return &ChatEntry{
		Visibility:    normalizeRuntimeEntryVisibility(entry.Visibility),
		Role:          strings.TrimSpace(entry.Role),
		Text:          strings.TrimSpace(entry.Text),
		CondensedText: condensedText,
		NoticeID:      noticeID,
	}
}

func normalizeOptionalStoredLocalFact(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return value
	}
	return &trimmed
}

func localEntryChatEntryForStep(entry storedLocalEntry, stepID string) *ChatEntry {
	projected := localEntryChatEntry(entry)
	projected.StepID = strings.TrimSpace(stepID)
	return projected
}

func (e *Engine) resetLocalDiagnostics() {
	if e == nil {
		return
	}
	e.diagnosticDedupeStore().Reset()
}

func (e *Engine) diagnosticDedupeStore() *diagnosticDedupeStore {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.diagnostics == nil {
		e.diagnostics = newDiagnosticDedupeStore()
	}
	return e.diagnostics
}

func (e *Engine) appendMessageRaw(stepID string, msg llm.Message, eventPolicy steeringMessageEventPolicy, persist bool) (session.CommitReceipt, error) {
	msg = normalizeMessageForTranscript(msg, e.transcriptWorkingDir())
	var err error
	msg, err = normalizePersistedMessageWorktreeContext(msg)
	if err != nil {
		return session.CommitReceipt{}, err
	}
	// Reject conflicting provider identity before durable append so one malformed
	// response cannot poison the session or crash the server projection.
	if err := e.transcriptRuntimeState().ValidateMessage(stepID, msg); err != nil {
		return session.CommitReceipt{}, fmt.Errorf("validate message projection: %w", err)
	}
	previousCommittedCount := e.CommittedTranscriptEntryCount()
	receipt := session.CommitReceipt{}
	var appendErr error
	if persist {
		appended, err := e.appendPersistedMessageEvent(stepID, msg)
		receipt = appended.CommitReceipt
		appendErr = err
		if !receipt.Committed {
			return receipt, appendErr
		}
	}
	if mutation := tokenUsageMutationForMessage(msg); mutation == tokenUsageMutationSignificant {
		e.markCurrentRequestShapeDirtyForSignificantMutation()
	} else {
		e.markCurrentRequestShapeDirty()
	}
	if projectionErr := e.transcriptRuntimeState().AppendMessage(stepID, msg); projectionErr != nil {
		return receipt, errors.Join(appendErr, fmt.Errorf("append message projection: %w", projectionErr))
	}
	currentCommittedCount := e.CommittedTranscriptEntryCount()
	if eventPolicy != steeringMessageEventNone && currentCommittedCount > previousCommittedCount && shouldEmitCommittedMessageEvent(msg) {
		e.emitRaw(Event{
			Kind:                       EventConversationUpdated,
			StepID:                     stepID,
			CommittedTranscriptChanged: true,
			Message:                    msg,
		})
	}
	return receipt, appendErr
}

func (e *Engine) appendPersistedMessageEvent(stepID string, msg llm.Message) (session.EventRecordAppendResult, error) {
	record, err := sessionMessageRecordFromLLM(msg)
	if err != nil {
		return session.EventRecordAppendResult{}, fmt.Errorf("adapt message record: %w", err)
	}
	if !isRollbackCandidateMessage(msg) {
		appended, receipt, appendErr := e.eventLog.AppendRecord(textutil.OptionalExactString(stepID), record)
		return session.EventRecordAppendResult{
			Record:        appended,
			CommitReceipt: receipt,
		}, appendErr
	}
	appended, err := e.eventLog.AppendRecordWithEndByteCursor(textutil.OptionalExactString(stepID), record)
	if appended.Committed {
		if appended.EndByteCursor == nil {
			panic(fmt.Sprintf(
				"committed rollback candidate message is missing its event-log end-byte cursor (event_seq=%d)",
				appended.Record.Seq(),
			))
		}
		e.transcriptRuntimeState().SetLatestRollbackCandidate(rollbacktarget.CandidateLocator{
			UserMessageSeq:       appended.Record.Seq(),
			CandidatePageEndByte: *appended.EndByteCursor,
		})
	}
	return appended, err
}

func (e *Engine) emitLiveToolAbortsRaw(stepID string, reason string) error {
	if e == nil || e.store == nil {
		return errors.New("runtime engine is required")
	}
	starts := e.transcriptRuntimeState().AbortLiveTools()
	for _, start := range starts {
		call := llm.ToolCall{ID: start.ToolCallID, Name: start.ToolName}
		if err := e.emitRaw(Event{
			Kind:            EventToolCallAborted,
			StepID:          strings.TrimSpace(stepID),
			ToolCall:        &call,
			ToolAbortReason: strings.TrimSpace(reason),
		}); err != nil {
			return err
		}
	}
	return nil
}

func shouldEmitCommittedMessageEvent(msg llm.Message) bool {
	return len(VisibleChatEntriesFromMessage(msg)) > 0
}

func (e *Engine) appendQueuedUserMessageFlush(stepID string, text string, batch []string, queueItems []QueuedUserMessage) (session.CommitReceipt, error) {
	msg := normalizeMessageForTranscript(llm.Message{Role: llm.RoleUser, Content: textutil.Value(text)}, e.transcriptWorkingDir())
	if msg.Content == nil || strings.TrimSpace(*msg.Content) == "" {
		return session.CommitReceipt{}, nil
	}
	normalizedItems := normalizedQueuedUserMessageStatusItems(queueItems)
	normalizedIDs := queuedUserMessageStatusItemIDs(normalizedItems)
	appended, appendErr := e.appendPersistedMessageEvent(stepID, msg)
	if !appended.Committed {
		return appended.CommitReceipt, appendErr
	}
	if mutation := tokenUsageMutationForMessage(msg); mutation == tokenUsageMutationSignificant {
		e.markCurrentRequestShapeDirtyForSignificantMutation()
	} else {
		e.markCurrentRequestShapeDirty()
	}
	if projectionErr := e.transcriptRuntimeState().AppendMessage(stepID, msg); projectionErr != nil {
		return appended.CommitReceipt, errors.Join(appendErr, fmt.Errorf("append queued message projection: %w", projectionErr))
	}
	e.emitRaw(Event{
		Kind:                         EventUserMessageFlushed,
		StepID:                       stepID,
		UserMessage:                  *msg.Content,
		UserMessageBatch:             append([]string(nil), batch...),
		UserMessageBatchQueueItemIDs: normalizedIDs,
		UserMessageBatchQueuedItems:  queuedUserMessageIdentities(normalizedItems),
		CommittedTranscriptChanged:   true,
	})
	for _, item := range normalizedItems {
		e.unmarkQueuedUserInjectionForAutoDrain(item.ID)
		e.emitRaw(Event{
			Kind: EventQueuedUserMessageStatus,
			QueuedUserMessageStatus: &QueuedUserMessageStatusEvent{
				SessionID:       e.SessionID(),
				QueueItemID:     item.ID,
				ClientRequestID: item.ClientRequestID,
				Status:          QueuedUserMessageSubmitted,
			},
		})
	}
	e.completeLiveRunQueueItems(queuedUserMessageIDSet(normalizedItems))
	return appended.CommitReceipt, appendErr
}

func normalizedQueuedUserMessageStatusItems(raw []QueuedUserMessage) []QueuedUserMessage {
	out := make([]QueuedUserMessage, 0, len(raw))
	seen := map[string]bool{}
	for _, item := range raw {
		item.ID = strings.TrimSpace(item.ID)
		item.ClientRequestID = strings.TrimSpace(item.ClientRequestID)
		if item.ID == "" || seen[item.ID] {
			continue
		}
		seen[item.ID] = true
		out = append(out, item)
	}
	return out
}

func queuedUserMessageStatusItemIDs(items []QueuedUserMessage) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.ID) != "" {
			ids = append(ids, strings.TrimSpace(item.ID))
		}
	}
	return ids
}

func queuedUserMessageIdentities(items []QueuedUserMessage) []QueuedUserMessageIdentity {
	if len(items) == 0 {
		return nil
	}
	identities := make([]QueuedUserMessageIdentity, 0, len(items))
	for _, item := range items {
		identities = append(identities, QueuedUserMessageIdentity{
			QueueItemID:     strings.TrimSpace(item.ID),
			ClientRequestID: strings.TrimSpace(item.ClientRequestID),
		})
	}
	return identities
}

func queuedUserMessageIDSet(items []QueuedUserMessage) map[string]struct{} {
	if len(items) == 0 {
		return nil
	}
	ids := make(map[string]struct{}, len(items))
	for _, item := range items {
		id := strings.TrimSpace(item.ID)
		if id != "" {
			ids[id] = struct{}{}
		}
	}
	return ids
}

func (e *Engine) emitQueuedUserMessageStatus(
	item QueuedUserMessage,
	status QueuedUserMessageStatus,
	reason QueuedUserMessageFailureReason,
	restore bool,
) {
	if e == nil || item.ID == "" {
		return
	}
	event := &QueuedUserMessageStatusEvent{
		SessionID:       e.SessionID(),
		QueueItemID:     item.ID,
		ClientRequestID: item.ClientRequestID,
		Status:          status,
		FailureReason:   reason,
	}
	if restore {
		event.RestoreText = item.Text
	}
	if status == QueuedUserMessageAccepted {
		event.RestoreText = item.Text
	}
	e.emitRaw(Event{Kind: EventQueuedUserMessageStatus, QueuedUserMessageStatus: event})
}

func (e *Engine) FailQueuedUserMessages(reason QueuedUserMessageFailureReason) []QueuedUserMessage {
	e.ensureOrchestrationCollaborators()
	messages := e.messageFlow.FailPendingUserInjections(reason)
	for _, item := range messages {
		e.unmarkQueuedUserInjectionForAutoDrain(item.ID)
		e.emitQueuedUserMessageStatus(item, QueuedUserMessageFailed, reason, true)
	}
	e.completeLiveRunQueueItems(queuedUserMessageIDSet(messages))
	return messages
}

func (e *Engine) clearStreamingAssistantStateRaw() (*AssistantStreamMetadata, *uuid.UUID) {
	return e.transcriptRuntimeState().ClearStreamingAssistantState()
}

func (e *Engine) emitStreamingAssistantTerminalRaw(
	stepID string,
	metadata *AssistantStreamMetadata,
	streamID uuid.UUID,
	abortReason *AssistantStreamAbortReason,
) error {
	abortReasonValue := ""
	if abortReason != nil {
		abortReasonValue = string(*abortReason)
	}
	return e.emitRaw(Event{
		Kind:                        EventAssistantDeltaReset,
		StepID:                      stepID,
		AssistantStreamMetadata:     cloneAssistantStreamMetadata(metadata),
		AssistantTranscriptStreamID: cloneTranscriptStreamID(&streamID),
		AssistantStreamAbortReason:  abortReasonValue,
	})
}

func (e *Engine) emitStreamingAssistantCleanupEventsRaw(
	stepID string,
	metadata *AssistantStreamMetadata,
	streamID *uuid.UUID,
	abortReason *AssistantStreamAbortReason,
) error {
	emissionErrors := []error{
		e.emitRaw(Event{Kind: EventConversationUpdated, StepID: stepID}),
	}
	if streamID != nil {
		emissionErrors = append(emissionErrors, e.emitStreamingAssistantTerminalRaw(stepID, metadata, *streamID, abortReason))
	}
	emissionErrors = append(emissionErrors, e.emitRaw(Event{Kind: EventReasoningDeltaReset, StepID: stepID}))
	return errors.Join(emissionErrors...)
}

func (e *Engine) emitCommittedAssistantMessageRaw(stepID string, committed steeringCommittedAssistantMessage) error {
	return e.emitCommittedAssistantMessageEventRaw(stepID, committed, nil, nil)
}

func (e *Engine) emitCommittedAssistantMessageEventRaw(stepID string, committed steeringCommittedAssistantMessage, streamMetadata *AssistantStreamMetadata, streamID *uuid.UUID) error {
	event := Event{
		Kind:                        EventAssistantMessage,
		StepID:                      stepID,
		Message:                     committed.message,
		AssistantStreamMetadata:     cloneAssistantStreamMetadata(streamMetadata),
		AssistantTranscriptStreamID: cloneTranscriptStreamID(streamID),
		CommittedTranscriptChanged:  true,
	}
	if committed.coordinate != nil {
		event.CommittedEntryStart = committed.coordinate.start
		event.CommittedEntryStartSet = true
	}
	return e.emitRaw(event)
}

func (e *Engine) resolveCompletedResponseStreamRaw(stepID string, instruction completedResponseResolutionInstruction) (completedResponseResolutionOutcome, error) {
	switch instruction.kind {
	case completedResponseResolutionInstructionFinalize:
		if instruction.committedAssistant == nil {
			return completedResponseResolutionOutcome{}, errors.New("completed response finalize instruction requires a committed assistant row")
		}
		if instruction.committedAssistant.coordinate == nil {
			return completedResponseResolutionOutcome{}, errors.New("completed response finalize instruction requires committed assistant coordinates")
		}
		if instruction.abortReason != nil {
			return completedResponseResolutionOutcome{}, errors.New("completed response finalize instruction cannot include an abort reason")
		}
	case completedResponseResolutionInstructionDiscard:
		if instruction.committedAssistant != nil {
			return completedResponseResolutionOutcome{}, errors.New("completed response discard instruction cannot include a committed assistant row")
		}
		if instruction.abortReason == nil {
			return completedResponseResolutionOutcome{}, errors.New("completed response discard instruction requires an abort reason")
		}
	default:
		return completedResponseResolutionOutcome{}, errors.New("completed response stream resolution instruction is invalid")
	}

	clearedMetadata, clearedStreamID := e.clearStreamingAssistantStateRaw()
	if instruction.kind == completedResponseResolutionInstructionFinalize {
		committed := instruction.committedAssistant
		if clearedStreamID != nil {
			e.transcriptRuntimeState().RecordAssistantStreamFinalization(committed.coordinate.start, clearedStreamID)
		}
		if err := e.emitCommittedAssistantMessageEventRaw(stepID, steeringCommittedAssistantMessage{
			message:    committed.message,
			coordinate: cloneCommittedAssistantCoordinate(committed.coordinate),
		}, clearedMetadata, clearedStreamID); err != nil {
			return completedResponseResolutionOutcome{}, err
		}
		if clearedStreamID == nil {
			return completedResponseResolutionOutcome{
				kind:                             completedResponseResolutionAbsent,
				committedAssistantEventPublished: true,
			}, nil
		}
		if err := e.emitStreamingAssistantCleanupEventsRaw(stepID, clearedMetadata, clearedStreamID, nil); err != nil {
			return completedResponseResolutionOutcome{}, err
		}
		return completedResponseResolutionOutcome{
			kind:                             completedResponseResolutionFinalized,
			streamID:                         cloneTranscriptStreamID(clearedStreamID),
			committedAssistantEventPublished: true,
		}, nil
	}

	if clearedStreamID == nil {
		return completedResponseResolutionOutcome{kind: completedResponseResolutionAbsent}, nil
	}
	if err := e.emitStreamingAssistantCleanupEventsRaw(stepID, clearedMetadata, clearedStreamID, instruction.abortReason); err != nil {
		return completedResponseResolutionOutcome{}, err
	}
	return completedResponseResolutionOutcome{
		kind:     completedResponseResolutionDiscarded,
		streamID: cloneTranscriptStreamID(clearedStreamID),
	}, nil
}

func flushedUserMessageEvent(msg llm.Message, stepID string) *Event {
	if msg.Role != llm.RoleUser {
		return nil
	}
	if msg.MessageType != nil &&
		*msg.MessageType == llm.MessageTypeCompactionSummary {
		return nil
	}
	if msg.Content == nil || strings.TrimSpace(*msg.Content) == "" {
		return nil
	}
	return &Event{Kind: EventUserMessageFlushed, StepID: stepID, UserMessage: *msg.Content, UserMessageBatch: []string{*msg.Content}, CommittedTranscriptChanged: true}
}

func (e *Engine) flushPendingUserInjections(stepID string, selection userInjectionSelection) (userInjectionCommitResult, error) {
	e.ensureOrchestrationCollaborators()
	return e.messageFlow.FlushPendingUserInjections(stepID, selection)
}

func (e *Engine) commitPendingUserInjectionsInTurn(stepID string, selection userInjectionSelection, turn OrderedMutationTurn) (userInjectionCommitResult, error) {
	e.ensureOrchestrationCollaborators()
	lifecycle, ok := e.messageFlow.(*defaultMessageLifecycle)
	if !ok {
		return userInjectionCommitResult{}, errors.New("pending user injection claim requires the default message lifecycle")
	}
	return lifecycle.commitPendingUserInjectionsInTurn(stepID, selection, turn)
}

// resolveGlobalConfigDir returns the directory that owns model-visible global
// context: the given root verbatim, or <home>/.kent when empty.
func resolveGlobalConfigDir(globalConfigDir string) (string, error) {
	if trimmed := strings.TrimSpace(globalConfigDir); trimmed != "" {
		return trimmed, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, agentsGlobalDirName), nil
}

func agentsInjectionPaths(workspaceRoot, globalConfigDir string) ([]string, error) {
	globalDir, err := resolveGlobalConfigDir(globalConfigDir)
	if err != nil {
		return nil, err
	}

	paths := make([]string, 0, 2)
	seen := map[string]bool{}
	addPath := func(path string) {
		cleaned := filepath.Clean(path)
		if cleaned == "" || seen[cleaned] {
			return
		}
		seen[cleaned] = true
		paths = append(paths, cleaned)
	}

	addPath(filepath.Join(globalDir, agentsFileName))
	addPath(filepath.Join(workspaceRoot, agentsFileName))
	return paths, nil
}

func environmentContextMessage(workspaceRoot string, model string, now time.Time) (string, error) {
	// Keep the reminder aligned with the default shell-tool workdir so daemon
	// process cwd cannot leak into fresh session environment context.
	cwd := shelltool.ResolveWorkdir(workspaceRoot, "")
	if cwd == "" {
		resolvedCWD, err := os.Getwd()
		if err == nil {
			cwd = strings.TrimSpace(resolvedCWD)
		}
	}
	if cwd == "" {
		cwd = "unknown"
	}

	shell := shellEnvironmentName()
	if strings.TrimSpace(shell) == "" {
		shell = "unknown"
	}

	osName := strings.TrimSpace(goruntime.GOOS)
	if osName == "" {
		osName = "unknown"
	}

	cpuArch := strings.TrimSpace(goruntime.GOARCH)
	if strings.TrimSpace(cpuArch) == "" {
		cpuArch = "unknown"
	}

	tzName, tzOffset := now.Zone()
	tzName = strings.TrimSpace(tzName)
	if tzName == "" {
		tzName = strings.TrimSpace(now.Location().String())
	}
	if tzName == "" {
		tzName = "unknown"
	}

	modelLine, err := environmentModelContextLine(model)
	if err != nil {
		return "", err
	}

	return strings.Join([]string{
		environmentInjectedHeader,
		modelLine,
		fmt.Sprintf("OS: %s", osName),
		fmt.Sprintf("Current TZ: %s (UTC%s)", tzName, formatUTCOffset(tzOffset)),
		fmt.Sprintf("Date/time: %s", now.Format(time.RFC3339)),
		fmt.Sprintf("Shell: %s", shell),
		fmt.Sprintf("CWD: %s", cwd),
		fmt.Sprintf("CPU arch: %s", cpuArch),
	}, "\n"), nil
}

// errEnvironmentContextModelRequired is returned when the environment context line is built without a model.
var errEnvironmentContextModelRequired = errors.New("environment context requires a model")

func environmentModelContextLine(model string) (string, error) {
	normalized := strings.TrimSpace(model)
	if normalized == "" {
		return "", errEnvironmentContextModelRequired
	}
	return fmt.Sprintf("Your model: %s", normalized), nil
}

func shellEnvironmentName() string {
	for _, key := range []string{"SHELL", "COMSPEC"} {
		value := strings.TrimSpace(os.Getenv(key))
		if value == "" {
			continue
		}
		base := filepath.Base(value)
		if base == "" || base == "." || base == string(filepath.Separator) {
			return value
		}
		return base
	}
	return ""
}

func formatUTCOffset(offsetSeconds int) string {
	sign := "+"
	if offsetSeconds < 0 {
		sign = "-"
		offsetSeconds = -offsetSeconds
	}
	hours := offsetSeconds / 3600
	minutes := (offsetSeconds % 3600) / 60
	return fmt.Sprintf("%s%02d:%02d", sign, hours, minutes)
}

func (e *Engine) restoreMessages() error {
	e.ensureOrchestrationCollaborators()
	return e.messageFlow.RestoreMessages()
}
