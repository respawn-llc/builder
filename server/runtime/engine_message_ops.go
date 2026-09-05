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
	"core/shared/runtimeids"
	"core/shared/textutil"
	"core/shared/transcript"

	"github.com/google/uuid"
)

type preparedFinalizedToolCompletion struct {
	completion           finalizedToolCompletion
	storedCompletion     storedToolCompletion
	backgroundSessionID  string
	hasBackgroundSession bool
	feedback             *storedLocalEntry
	records              []session.EventRecordPayload
}

type appliedFinalizedToolCompletion struct {
	completionProvenance TranscriptCommittedRowProvenance
	feedbackProvenance   *TranscriptCommittedRowProvenance
	completionCount      int
	feedbackCount        int
}

func (e *Engine) prepareFinalizedToolCompletion(
	completion finalizedToolCompletion,
) (preparedFinalizedToolCompletion, error) {
	stored, backgroundSessionID, hasBackgroundSession, err := e.prepareStoredToolCompletion(completion.Result)
	if err != nil {
		return preparedFinalizedToolCompletion{}, err
	}
	completionRecord, err := sessionToolCompletionRecordFromStored(stored)
	if err != nil {
		return preparedFinalizedToolCompletion{}, fmt.Errorf("adapt tool completion record: %w", err)
	}
	prepared := preparedFinalizedToolCompletion{
		completion:           completion,
		storedCompletion:     stored,
		backgroundSessionID:  backgroundSessionID,
		hasBackgroundSession: hasBackgroundSession,
		records:              []session.EventRecordPayload{completionRecord},
	}
	if completion.OperatorFeedback == nil {
		return prepared, nil
	}
	feedback, normalizeErr := normalizeStoredLocalEntry(*completion.OperatorFeedback)
	if normalizeErr != nil {
		panic(fmt.Sprintf(
			"tool completion presentation fallback requires valid operator feedback (call_id=%q tool=%q error=%v)",
			completion.Result.CallID,
			completion.Result.Name,
			normalizeErr,
		))
	}
	feedbackRecord, err := sessionLocalEntryRecordFromRuntime(feedback)
	if err != nil {
		return preparedFinalizedToolCompletion{}, fmt.Errorf("adapt operator feedback record: %w", err)
	}
	prepared.feedback = &feedback
	prepared.records = append(prepared.records, feedbackRecord)
	return prepared, nil
}

func (e *Engine) applyPreparedFinalizedToolCompletion(
	stepID *string,
	prepared preparedFinalizedToolCompletion,
	records []session.EventRecord,
) (appliedFinalizedToolCompletion, error) {
	if len(records) != len(prepared.records) {
		return appliedFinalizedToolCompletion{}, fmt.Errorf(
			"apply finalized tool completion received %d records, want %d",
			len(records),
			len(prepared.records),
		)
	}
	completionProvenance, err := transcriptProvenanceFromRecord(records[0])
	if err != nil {
		return appliedFinalizedToolCompletion{}, err
	}
	e.applyCommittedStoredToolCompletion(
		prepared.storedCompletion,
		prepared.backgroundSessionID,
		prepared.hasBackgroundSession,
		&completionProvenance,
	)
	applied := appliedFinalizedToolCompletion{
		completionProvenance: completionProvenance,
		completionCount:      e.CommittedTranscriptEntryCount(),
		feedbackCount:        e.CommittedTranscriptEntryCount(),
	}
	if prepared.feedback == nil {
		return applied, nil
	}
	feedbackProvenance, err := transcriptProvenanceFromRecord(records[1])
	if err != nil {
		return appliedFinalizedToolCompletion{}, err
	}
	e.transcriptRuntimeState().AppendLocalEntryRecord(
		*localEntryChatEntryForStep(*prepared.feedback, stepID),
		prepared.feedback.AfterToolCallID,
		&feedbackProvenance,
	)
	applied.feedbackProvenance = &feedbackProvenance
	applied.feedbackCount = e.CommittedTranscriptEntryCount()
	return applied, nil
}

func (e *Engine) publishCommittedFinalizedToolCompletion(
	completionStepID *string, feedbackStepID *string,
	completion finalizedToolCompletion,
	completionProvenance, feedbackProvenance *TranscriptCommittedRowProvenance,
) error {
	result := cloneToolResult(completion.Result)
	e.transcriptRuntimeState().CompleteLiveTool(result.CallID)
	err := e.emitRaw(Event{Kind: EventToolCallCompleted, ToolResult: &result, CommittedTranscriptChanged: true, CommittedProvenance: cloneTranscriptCommittedRowProvenance(completionProvenance)}.withStepID(completionStepID))
	if completion.OperatorFeedback == nil {
		return err
	}
	entry := localEntryChatEntryForStep(*completion.OperatorFeedback, feedbackStepID)
	return errors.Join(err, e.emitRaw(Event{Kind: EventLocalEntryAdded, LocalEntry: entry, CommittedTranscriptChanged: true, CommittedProvenance: cloneTranscriptCommittedRowProvenance(feedbackProvenance)}.withStepID(feedbackStepID)))
}

func (e *Engine) persistToolCompletionRaw(stepID string, result tools.Result) (session.CommitReceipt, *TranscriptCommittedRowProvenance, error) {
	receipt, provenance, _, err := e.persistFinalizedToolCompletionRaw(
		textutil.OptionalExactString(stepID),
		finalizedToolCompletion{Result: result},
	)
	return receipt, provenance, err
}

func (e *Engine) persistFinalizedToolCompletionRaw(
	stepID *string,
	completion finalizedToolCompletion,
) (session.CommitReceipt, *TranscriptCommittedRowProvenance, *TranscriptCommittedRowProvenance, error) {
	prepared, err := e.prepareFinalizedToolCompletion(completion)
	if err != nil {
		return session.CommitReceipt{}, nil, nil, err
	}
	records, receipt, err := e.eventLog.AppendRecordsAtomic(
		stepID,
		prepared.records,
	)
	if !receipt.Committed {
		return receipt, nil, nil, err
	}
	applied, applyErr := e.applyPreparedFinalizedToolCompletion(stepID, prepared, records)
	if applyErr != nil {
		return receipt, nil, nil, errors.Join(err, applyErr)
	}
	return receipt, &applied.completionProvenance, applied.feedbackProvenance, err
}

func (e *Engine) prepareStoredToolCompletion(
	r tools.Result,
) (storedToolCompletion, string, bool, error) {
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
	backgroundSessionID, hasBackgroundSession, err := harvestedBackgroundCompletionSessionID(r)
	if err != nil {
		if e.cfg.Debug {
			panic(err)
		}
		return storedToolCompletion{}, "", false, err
	}
	payload := storedToolCompletion{
		CallID:         r.CallID,
		Name:           string(r.Name),
		IsError:        r.IsError,
		Output:         append(json.RawMessage(nil), r.Output...),
		Summary:        r.Summary,
		CondensedText:  r.CondensedText,
		Presentation:   r.Presentation,
		ProviderItems:  e.providerItemsForToolCompletion(r),
		QuestionAnswer: cloneAskQuestionAnswer(r.QuestionAnswer),
	}
	return payload, backgroundSessionID, hasBackgroundSession, nil
}

func (e *Engine) applyCommittedStoredToolCompletion(
	payload storedToolCompletion,
	backgroundSessionID string,
	hasBackgroundSession bool,
	provenance *TranscriptCommittedRowProvenance,
) {
	e.transcriptRuntimeState().RecordStoredToolCompletion(payload, provenance)
	if hasBackgroundSession {
		e.ensureOrchestrationCollaborators()
		e.backgroundFlow.ConsumePendingBackgroundNotice(backgroundSessionID)
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
	return e.steerDiagnosticEntry(
		diagnosticKey,
		role,
		text,
		func(intent steeringIntent) error { return e.steer(stepID, intent) },
	)
}

func (e *Engine) steerRuntimePersistedDiagnosticEntry(diagnosticKey, role, text string) error {
	return e.steerDiagnosticEntry(
		diagnosticKey,
		role,
		text,
		func(intent steeringIntent) error { return e.steerRuntime(intent) },
	)
}

func (e *Engine) steerDiagnosticEntry(
	diagnosticKey string,
	role string,
	text string,
	apply func(steeringIntent) error,
) error {
	diagnosticKey = strings.TrimSpace(diagnosticKey)
	if diagnosticKey == "" {
		return apply(steerLocalEntryIntent(storedLocalEntry{
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
	if err := apply(steerLocalEntryIntent(entry)); err != nil {
		e.diagnosticDedupeStore().ClearLocal(diagnosticKey)
		return err
	}
	return nil
}

func (e *Engine) appendPersistedLocalEntryRecordRaw(
	stepID *string,
	entry storedLocalEntry,
	reasoningIdentity *TranscriptReasoningTraceIdentity,
) (session.CommitReceipt, *TranscriptCommittedRowProvenance, error) {
	entry, err := normalizeStoredLocalEntry(entry)
	if err != nil {
		return session.CommitReceipt{}, nil, fmt.Errorf("normalize local entry: %w", err)
	}
	record, adaptErr := sessionLocalEntryRecordFromRuntime(entry)
	if adaptErr != nil {
		return session.CommitReceipt{}, nil, fmt.Errorf("adapt local entry record: %w", adaptErr)
	}
	appended, receipt, err := e.eventLog.AppendRecord(stepID, record)
	var provenance *TranscriptCommittedRowProvenance
	if receipt.Committed {
		value, provenanceErr := transcriptProvenanceFromRecord(appended)
		if provenanceErr != nil {
			return receipt, nil, errors.Join(err, provenanceErr)
		}
		provenance = &value
		projected := localEntryChatEntryForStep(entry, stepID)
		e.transcriptRuntimeState().AppendLocalEntryRecord(*projected, entry.AfterToolCallID, provenance)
		e.emitRaw(Event{
			Kind:                       EventLocalEntryAdded,
			LocalEntry:                 projected,
			ReasoningTraceIdentity:     cloneTranscriptReasoningTraceIdentity(reasoningIdentity),
			CommittedTranscriptChanged: true,
			CommittedProvenance:        provenance,
		}.withStepID(stepID))
	}
	return receipt, provenance, err
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
	if entry.Text == "" && entry.ToolOutputRepair == nil && entry.ProviderModelMismatch == nil {
		return storedLocalEntry{}, errors.New("text or typed notice facts are required")
	}
	if entry.ToolOutputRepair != nil && !entry.ToolOutputRepair.Valid() {
		return storedLocalEntry{}, errors.New("tool-output repair facts are invalid")
	}
	if entry.ProviderModelMismatch != nil && !entry.ProviderModelMismatch.Valid() {
		return storedLocalEntry{}, errors.New("provider-model mismatch facts are invalid")
	}
	if entry.ToolOutputRepair != nil && entry.ProviderModelMismatch != nil {
		return storedLocalEntry{}, errors.New("local entry cannot carry multiple typed notice facts")
	}
	return entry, nil
}

func localEntryChatEntry(entry storedLocalEntry) *ChatEntry {
	condensedText, _ := textutil.OptionalExact(entry.CondensedText)
	noticeID, _ := textutil.OptionalExact(entry.NoticeID)
	return &ChatEntry{
		Visibility:            normalizeRuntimeEntryVisibility(entry.Visibility),
		Role:                  strings.TrimSpace(entry.Role),
		Text:                  strings.TrimSpace(entry.Text),
		DurationMs:            textutil.Pointer(entry.DurationMs),
		CondensedText:         condensedText,
		NoticeID:              noticeID,
		ToolOutputRepair:      textutil.Pointer(entry.ToolOutputRepair),
		ProviderModelMismatch: textutil.Pointer(entry.ProviderModelMismatch),
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

func localEntryChatEntryForStep(entry storedLocalEntry, stepID *string) *ChatEntry {
	projected := localEntryChatEntry(entry)
	projected.StepID = cloneOptionalStepID(stepID)
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

type preparedMessageProjection struct {
	message llm.Message
	record  session.MessageRecord
}

func (e *Engine) prepareMessageProjection(stepID *string, msg llm.Message) (preparedMessageProjection, error) {
	msg, err := normalizeMessageForTranscriptChecked(msg, e.transcriptWorkingDir())
	if err != nil {
		return preparedMessageProjection{}, fmt.Errorf("normalize message transcript presentation: %w", err)
	}
	msg, err = normalizePersistedMessageWorktreeContext(msg)
	if err != nil {
		return preparedMessageProjection{}, err
	}
	if err := e.transcriptRuntimeState().ValidateMessage(stepID, msg); err != nil {
		return preparedMessageProjection{}, fmt.Errorf("validate message projection: %w", err)
	}
	record, err := sessionMessageRecordFromLLM(msg)
	if err != nil {
		return preparedMessageProjection{}, fmt.Errorf("adapt message record: %w", err)
	}
	return preparedMessageProjection{message: msg, record: record}, nil
}

func (e *Engine) applyPreparedMessageProjection(stepID *string, prepared preparedMessageProjection, provenance *TranscriptCommittedRowProvenance) error {
	return e.transcriptRuntimeState().AppendMessage(stepID, prepared.message, provenance)
}

func (e *Engine) appendMessageRaw(
	stepID *string,
	msg llm.Message,
	eventPolicy steeringMessageEventPolicy,
	persist bool,
	provenanceDestination **TranscriptCommittedRowProvenance,
) (session.CommitReceipt, error) {
	prepared, err := e.prepareMessageProjection(stepID, msg)
	if err != nil {
		return session.CommitReceipt{}, err
	}
	previousCommittedCount := e.CommittedTranscriptEntryCount()
	receipt := session.CommitReceipt{}
	var appendErr error
	var provenance *TranscriptCommittedRowProvenance
	if persist {
		appended, err := e.appendPreparedMessageEvent(stepID, prepared)
		receipt = appended.CommitReceipt
		appendErr = err
		if !receipt.Committed {
			return receipt, appendErr
		}
		value, provenanceErr := transcriptProvenanceFromRecord(appended.Record)
		if provenanceErr != nil {
			return receipt, errors.Join(appendErr, provenanceErr)
		}
		provenance = &value
	}
	if projectionErr := e.applyPreparedMessageProjection(stepID, prepared, provenance); projectionErr != nil {
		return receipt, errors.Join(appendErr, fmt.Errorf("append message projection: %w", projectionErr))
	}
	if provenanceDestination != nil {
		*provenanceDestination = cloneTranscriptCommittedRowProvenance(provenance)
	}
	currentCommittedCount := e.CommittedTranscriptEntryCount()
	if eventPolicy != steeringMessageEventNone &&
		currentCommittedCount > previousCommittedCount &&
		e.shouldEmitCommittedMessageEvent(prepared.message) {
		e.emitRaw(Event{
			Kind:                       EventConversationUpdated,
			CommittedTranscriptChanged: true,
			Message:                    prepared.message,
			CommittedProvenance:        cloneTranscriptCommittedRowProvenance(provenance),
		}.withStepID(stepID))
	}
	return receipt, appendErr
}

func (e *Engine) appendPreparedMessageEvent(stepID *string, prepared preparedMessageProjection) (session.EventRecordAppendResult, error) {
	if !isRollbackCandidateMessage(prepared.message) {
		appended, receipt, appendErr := e.eventLog.AppendRecord(stepID, prepared.record)
		return session.EventRecordAppendResult{
			Record:        appended,
			CommitReceipt: receipt,
		}, appendErr
	}
	appended, err := e.eventLog.AppendRecordWithEndByteCursor(stepID, prepared.record)
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

func (e *Engine) emitLiveToolAbortsRaw(reason string) error {
	if e == nil || e.store == nil {
		return errors.New("runtime engine is required")
	}
	starts := e.transcriptRuntimeState().LiveToolSnapshot()
	stepIDs := make([]runtimeids.StepID, len(starts))
	for index, start := range starts {
		stepID, err := runtimeids.ParseStepID(start.StepID)
		if err != nil {
			return fmt.Errorf("validate live tool Step identity: %w", err)
		}
		stepIDs[index] = stepID
	}
	for index, start := range starts {
		stepID := stepIDs[index]
		call := llm.ToolCall{ID: start.ToolCallID, Name: start.ToolName}
		if err := e.emitRaw(Event{
			Kind:            EventToolCallAborted,
			StepID:          exactStepIDPointer(stepID.String()),
			ToolCall:        &call,
			ToolAbortReason: strings.TrimSpace(reason),
		}); err != nil {
			return err
		}
		e.transcriptRuntimeState().CompleteLiveTool(start.ToolCallID)
	}
	return nil
}

func shouldEmitCommittedMessageEvent(msg llm.Message) bool {
	return len(VisibleChatEntriesFromMessage(msg)) > 0
}

func (e *Engine) shouldEmitCommittedMessageEvent(msg llm.Message) bool {
	if !shouldEmitCommittedMessageEvent(msg) {
		return false
	}
	if msg.Role != llm.RoleTool {
		return true
	}
	callID, present := textutil.OptionalTrimmed(msg.ToolCallID)
	if !present {
		return true
	}
	_, completed := e.transcriptRuntimeState().ToolCompletionSnapshot(callID)
	return !completed
}

func (e *Engine) appendQueuedUserMessageFlush(stepID *string, message llm.Message, batch []string, queueItems []QueuedUserMessage) (session.CommitReceipt, error) {
	prepared, err := e.prepareMessageProjection(stepID, message)
	if err != nil {
		return session.CommitReceipt{}, err
	}
	if prepared.message.Content == nil || strings.TrimSpace(*prepared.message.Content) == "" {
		return session.CommitReceipt{}, nil
	}
	normalizedItems := normalizedQueuedUserMessageStatusItems(queueItems)
	normalizedIDs := queuedUserMessageStatusItemIDs(normalizedItems)
	appended, appendErr := e.appendPreparedMessageEvent(stepID, prepared)
	if !appended.Committed {
		return appended.CommitReceipt, appendErr
	}
	provenance, provenanceErr := transcriptProvenanceFromRecord(appended.Record)
	if provenanceErr != nil {
		return appended.CommitReceipt, errors.Join(appendErr, provenanceErr)
	}
	if projectionErr := e.applyPreparedMessageProjection(stepID, prepared, &provenance); projectionErr != nil {
		return appended.CommitReceipt, errors.Join(appendErr, fmt.Errorf("append queued message projection: %w", projectionErr))
	}
	event := Event{
		Kind:                       EventConversationUpdated,
		CommittedTranscriptChanged: true,
		Message:                    prepared.message,
		CommittedProvenance:        &provenance,
	}.withStepID(stepID)
	if prepared.message.Role == llm.RoleUser {
		event = Event{
			Kind:                         EventUserMessageFlushed,
			UserMessage:                  *prepared.message.Content,
			UserMessageBatch:             append([]string(nil), batch...),
			UserMessageBatchQueueItemIDs: normalizedIDs,
			UserMessageBatchQueuedItems:  queuedUserMessageIdentities(normalizedItems),
			CommittedTranscriptChanged:   true,
			CommittedProvenance:          &provenance,
		}.withStepID(stepID)
	}
	e.emitRaw(event)
	for _, item := range normalizedItems {
		e.emitRaw(Event{
			Kind: EventQueuedUserMessageStatus,
			QueuedUserMessageStatus: &QueuedUserMessageStatusEvent{
				SessionID:   e.SessionID(),
				QueueItemID: item.ID,
				Status:      QueuedUserMessageSubmitted,
			},
		})
	}
	return appended.CommitReceipt, appendErr
}

func normalizedQueuedUserMessageStatusItems(raw []QueuedUserMessage) []QueuedUserMessage {
	out := make([]QueuedUserMessage, 0, len(raw))
	seen := map[string]bool{}
	for _, item := range raw {
		item.ID = strings.TrimSpace(item.ID)
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
			QueueItemID: strings.TrimSpace(item.ID),
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
		SessionID:     e.SessionID(),
		QueueItemID:   item.ID,
		Status:        status,
		FailureReason: reason,
	}
	text, err := item.DisplayText()
	if err != nil {
		e.surfaceRunError(fmt.Errorf("queued user message status: %w", err))
		return
	}
	if restore {
		event.Text = text
	}
	if status == QueuedUserMessageAccepted {
		event.Text = text
	}
	if err := e.emitRaw(Event{Kind: EventQueuedUserMessageStatus, QueuedUserMessageStatus: event}); err != nil {
		e.surfaceRunError(fmt.Errorf("publish queued user message status: %w", err))
	}
}

func (e *Engine) emitInterruptedHumanInputs(items []QueuedUserMessage) {
	if e == nil || len(items) == 0 {
		return
	}
	eventItems := make([]InterruptedHumanInput, 0, len(items))
	for _, item := range items {
		text, err := item.DisplayText()
		if err != nil {
			e.surfaceRunError(fmt.Errorf("interrupted human input: %w", err))
			continue
		}
		eventItems = append(eventItems, InterruptedHumanInput{
			QueueItemID: item.ID,
			Text:        text,
		})
	}
	if len(eventItems) == 0 {
		return
	}
	e.emitRaw(Event{
		Kind:                  EventHumanInputInterrupted,
		HumanInputInterrupted: &HumanInputInterruptedEvent{Items: eventItems},
	})
}

func (e *Engine) FailQueuedUserMessages(reason QueuedUserMessageFailureReason) []QueuedUserMessage {
	e.ensureOrchestrationCollaborators()
	pending := e.messageFlow.DrainPendingUserInjections()
	messages := append([]QueuedUserMessage(nil), pending...)
	if len(pending) != 0 {
		for _, item := range pending {
			e.emitQueuedUserMessageStatus(item, QueuedUserMessageFailed, reason, true)
		}
		e.publishPendingWorkChanged()
	}
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
		StepID:                      exactStepIDPointer(stepID),
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
		e.emitRaw(Event{Kind: EventConversationUpdated, StepID: exactStepIDPointer(stepID)}),
	}
	if streamID != nil {
		emissionErrors = append(emissionErrors, e.emitStreamingAssistantTerminalRaw(stepID, metadata, *streamID, abortReason))
	}
	return errors.Join(emissionErrors...)
}

func (e *Engine) emitCommittedAssistantMessageRaw(stepID string, committed steeringCommittedAssistantMessage) error {
	return e.emitCommittedAssistantMessageEventRaw(stepID, committed, nil, nil)
}

func (e *Engine) emitCommittedAssistantMessageEventRaw(stepID string, committed steeringCommittedAssistantMessage, streamMetadata *AssistantStreamMetadata, streamID *uuid.UUID) error {
	event := Event{
		Kind:                        EventAssistantMessage,
		StepID:                      exactStepIDPointer(stepID),
		Message:                     committed.message,
		CommittedProvenance:         cloneTranscriptCommittedRowProvenance(committed.provenance),
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
	case completedResponseResolutionInstructionAbort:
		if instruction.committedAssistant != nil {
			return completedResponseResolutionOutcome{}, errors.New("completed response abort instruction cannot include a committed assistant row")
		}
		if instruction.abortReason == nil {
			return completedResponseResolutionOutcome{}, errors.New("completed response abort instruction requires an abort reason")
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
			provenance: cloneTranscriptCommittedRowProvenance(committed.provenance),
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

	if err := e.emitStreamingAssistantCleanupEventsRaw(stepID, clearedMetadata, clearedStreamID, instruction.abortReason); err != nil {
		return completedResponseResolutionOutcome{}, err
	}
	return completedResponseResolutionOutcome{
		kind:     completedResponseResolutionDiscarded,
		streamID: cloneTranscriptStreamID(clearedStreamID),
	}, nil
}

func flushedUserMessageEvent(provenance *TranscriptCommittedRowProvenance, msg llm.Message, stepID *string) *Event {
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
	event := Event{Kind: EventUserMessageFlushed, UserMessage: *msg.Content, UserMessageBatch: []string{*msg.Content}, CommittedTranscriptChanged: true, CommittedProvenance: cloneTranscriptCommittedRowProvenance(provenance)}.withStepID(stepID)
	return &event
}

func (e *Engine) flushPendingUserInjections(stepID string, selection userInjectionSelection) (userInjectionCommitResult, error) {
	e.ensureOrchestrationCollaborators()
	return e.messageFlow.FlushPendingUserInjections(stepID, selection)
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

	rows := []string{
		environmentInjectedHeader,
		modelLine,
	}
	if cutoff, ok := llm.LookupModelKnowledgeCutoff(model); ok {
		rows = append(rows, fmt.Sprintf("Knowledge cutoff: %02d-%04d", cutoff.Month, cutoff.Year))
	}
	rows = append(rows,
		fmt.Sprintf("OS: %s", osName),
		fmt.Sprintf("Current TZ: %s (UTC%s)", tzName, formatUTCOffset(tzOffset)),
		fmt.Sprintf("Date/time: %s", now.Format(time.RFC3339)),
		fmt.Sprintf("Shell: %s", shell),
		fmt.Sprintf("CWD: %s", cwd),
		fmt.Sprintf("CPU arch: %s", cpuArch),
	)
	return strings.Join(rows, "\n"), nil
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
