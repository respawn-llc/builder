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
	"core/server/tools"
	shelltool "core/server/tools/shell"
	"core/shared/rollbacktarget"
	"core/shared/transcript"

	"github.com/google/uuid"
)

func (e *Engine) persistToolCompletionRaw(stepID string, r tools.Result) error {
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
	if sessionID, ok := harvestedBackgroundCompletionSessionID(r); ok {
		e.ensureOrchestrationCollaborators()
		e.backgroundFlow.ConsumePendingBackgroundNotice(sessionID)
	}
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
	_, _, err := e.store.AppendEvent(stepID, "tool_completed", payload)
	if err == nil {
		e.markCurrentRequestShapeDirtyForSignificantMutation()
		newTranscriptPersistenceCoordinator(e.transcriptRuntimeState()).RecordStoredToolCompletion(payload)
	}
	return err
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
		itemCallID := strings.TrimSpace(item.CallID)
		if itemCallID == "" {
			itemCallID = strings.TrimSpace(item.ID)
		}
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
		name = firstNonEmpty(name, strings.TrimSpace(callItem.Name))
	}
	return llm.PrepareOpenAIInputItems([]llm.ResponseItem{{
		Type:   llm.ToolOutputItemType(custom),
		CallID: callID,
		Name:   name,
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
		DiagnosticKey: diagnosticKey,
	}
	entry.Role = strings.TrimSpace(entry.Role)
	entry.Text = strings.TrimSpace(entry.Text)
	entry.DiagnosticKey = strings.TrimSpace(entry.DiagnosticKey)
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

func (e *Engine) appendPersistedLocalEntryRecordRaw(stepID string, entry storedLocalEntry) error {
	entry.Role = strings.TrimSpace(entry.Role)
	entry.Text = strings.TrimSpace(entry.Text)
	entry.CondensedText = strings.TrimSpace(entry.CondensedText)
	entry.DiagnosticKey = strings.TrimSpace(entry.DiagnosticKey)
	entry.NoticeID = strings.TrimSpace(entry.NoticeID)
	if entry.Role == "" || entry.Text == "" {
		return nil
	}
	if e.beforePersistLocalEntry != nil {
		if err := e.beforePersistLocalEntry(entry); err != nil {
			return err
		}
	}
	_, _, err := e.store.AppendEvent(stepID, "local_entry", entry)
	if err == nil {
		newTranscriptPersistenceCoordinator(e.transcriptRuntimeState()).AppendLocalEntryRecord(*localEntryChatEntry(entry))
		e.emitRaw(Event{Kind: EventLocalEntryAdded, StepID: stepID, LocalEntry: localEntryChatEntry(entry), CommittedTranscriptChanged: true})
	}
	return err
}

func localEntryChatEntry(entry storedLocalEntry) *ChatEntry {
	return &ChatEntry{
		Visibility:    normalizeRuntimeEntryVisibility(entry.Visibility),
		Role:          strings.TrimSpace(entry.Role),
		Text:          strings.TrimSpace(entry.Text),
		CondensedText: strings.TrimSpace(entry.CondensedText),
		NoticeID:      strings.TrimSpace(entry.NoticeID),
	}
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

func (e *Engine) appendMessageRaw(stepID string, msg llm.Message, eventPolicy steeringMessageEventPolicy, persist bool) error {
	msg = normalizeMessageForTranscript(msg, e.transcriptWorkingDir())
	var err error
	msg, err = normalizePersistedMessageWorktreeContext(msg)
	if err != nil {
		return err
	}
	previousCommittedCount := e.CommittedTranscriptEntryCount()
	if e.beforePersistMessage != nil {
		if err := e.beforePersistMessage(msg); err != nil {
			return err
		}
	}
	if mutation := tokenUsageMutationForMessage(msg); mutation == tokenUsageMutationSignificant {
		e.markCurrentRequestShapeDirtyForSignificantMutation()
	} else {
		e.markCurrentRequestShapeDirty()
	}
	newTranscriptPersistenceCoordinator(e.transcriptRuntimeState()).AppendMessage(msg)
	if persist {
		if err := e.appendPersistedMessageEvent(stepID, msg); err != nil {
			return err
		}
	}
	currentCommittedCount := e.CommittedTranscriptEntryCount()
	if eventPolicy != steeringMessageEventNone && currentCommittedCount > previousCommittedCount && shouldEmitCommittedMessageEvent(msg) {
		e.emitRaw(Event{Kind: EventConversationUpdated, StepID: stepID, CommittedTranscriptChanged: true, Message: msg})
	}
	return nil
}

func (e *Engine) appendPersistedMessageEvent(stepID string, msg llm.Message) error {
	if !isRollbackCandidateMessage(msg) {
		_, _, err := e.store.AppendEvent(stepID, "message", msg)
		return err
	}
	appended, err := e.store.AppendEventWithEndByteCursor(stepID, "message", msg)
	if appended.Committed {
		if appended.EndByteCursor == nil {
			panic(fmt.Sprintf(
				"committed rollback candidate message is missing its event-log end-byte cursor (event_seq=%d)",
				appended.Event.Seq,
			))
		}
		e.transcriptRuntimeState().SetLatestRollbackCandidate(rollbacktarget.CandidateLocator{
			UserMessageSeq:       appended.Event.Seq,
			CandidatePageEndByte: *appended.EndByteCursor,
		})
	}
	return err
}

func (e *Engine) emitLiveToolAbortsRaw(stepID string, reason string) {
	if e == nil || e.store == nil {
		return
	}
	starts := e.transcriptRuntimeState().AbortLiveTools()
	for _, start := range starts {
		call := llm.ToolCall{ID: start.ToolCallID, Name: start.ToolName}
		e.emitRaw(Event{
			Kind:            EventToolCallAborted,
			StepID:          strings.TrimSpace(stepID),
			ToolCall:        &call,
			ToolAbortReason: strings.TrimSpace(reason),
		})
	}
}

func shouldEmitCommittedMessageEvent(msg llm.Message) bool {
	return len(VisibleChatEntriesFromMessage(msg)) > 0
}

func (e *Engine) appendQueuedUserMessageFlush(stepID string, text string, batch []string, queueItems []QueuedUserMessage) error {
	msg := normalizeMessageForTranscript(llm.Message{Role: llm.RoleUser, Content: text}, e.transcriptWorkingDir())
	if strings.TrimSpace(msg.Content) == "" {
		return nil
	}
	if e.beforePersistMessage != nil {
		if err := e.beforePersistMessage(msg); err != nil {
			return err
		}
	}
	if mutation := tokenUsageMutationForMessage(msg); mutation == tokenUsageMutationSignificant {
		e.markCurrentRequestShapeDirtyForSignificantMutation()
	} else {
		e.markCurrentRequestShapeDirty()
	}
	normalizedItems := normalizedQueuedUserMessageStatusItems(queueItems)
	normalizedIDs := queuedUserMessageStatusItemIDs(normalizedItems)
	if err := e.appendPersistedMessageEvent(stepID, msg); err != nil {
		return err
	}
	newTranscriptPersistenceCoordinator(e.transcriptRuntimeState()).AppendMessage(msg)
	e.emitRaw(Event{
		Kind:                         EventUserMessageFlushed,
		StepID:                       stepID,
		UserMessage:                  msg.Content,
		UserMessageBatch:             append([]string(nil), batch...),
		UserMessageBatchQueueItemIDs: normalizedIDs,
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
	return nil
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

func (e *Engine) emitQueuedUserMessageStatus(item QueuedUserMessage, status QueuedUserMessageStatus, reason QueuedUserMessageFailureReason, restore bool) {
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
	pending := e.messageFlow.DrainPendingUserInjections()
	messages := make([]QueuedUserMessage, 0, len(pending))
	for _, item := range pending {
		messages = append(messages, item)
		e.unmarkQueuedUserInjectionForAutoDrain(item.ID)
		e.emitQueuedUserMessageStatus(item, QueuedUserMessageFailed, reason, true)
	}
	e.completeLiveRunQueueItems(queuedUserMessageIDSet(messages))
	return messages
}

func (e *Engine) clearStreamingAssistantStateRaw() (*AssistantStreamMetadata, *uuid.UUID) {
	return newTranscriptPersistenceCoordinator(e.transcriptRuntimeState()).ClearStreamingAssistantState()
}

func (e *Engine) emitStreamingAssistantResetEventsRaw(stepID string, metadata *AssistantStreamMetadata, streamID *uuid.UUID, abortReason *AssistantStreamAbortReason) {
	abortReasonValue := ""
	if abortReason != nil {
		abortReasonValue = string(*abortReason)
	}
	e.emitRaw(Event{Kind: EventConversationUpdated, StepID: stepID})
	e.emitRaw(Event{Kind: EventAssistantDeltaReset, StepID: stepID, AssistantStreamMetadata: cloneAssistantStreamMetadata(metadata), AssistantTranscriptStreamID: cloneTranscriptStreamID(streamID), AssistantStreamAbortReason: abortReasonValue})
	e.emitRaw(Event{Kind: EventReasoningDeltaReset, StepID: stepID})
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
	e.emitRaw(event)
	return nil
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
			newTranscriptPersistenceCoordinator(e.transcriptRuntimeState()).RecordAssistantStreamFinalization(committed.coordinate.start, clearedStreamID)
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
		e.emitStreamingAssistantResetEventsRaw(stepID, clearedMetadata, clearedStreamID, nil)
		return completedResponseResolutionOutcome{
			kind:                             completedResponseResolutionFinalized,
			streamID:                         cloneTranscriptStreamID(clearedStreamID),
			committedAssistantEventPublished: true,
		}, nil
	}

	if clearedStreamID == nil {
		return completedResponseResolutionOutcome{kind: completedResponseResolutionAbsent}, nil
	}
	e.emitStreamingAssistantResetEventsRaw(stepID, clearedMetadata, clearedStreamID, instruction.abortReason)
	return completedResponseResolutionOutcome{
		kind:     completedResponseResolutionDiscarded,
		streamID: cloneTranscriptStreamID(clearedStreamID),
	}, nil
}

func flushedUserMessageEvent(msg llm.Message, stepID string) *Event {
	if msg.Role != llm.RoleUser {
		return nil
	}
	if msg.MessageType == llm.MessageTypeCompactionSummary {
		return nil
	}
	if strings.TrimSpace(msg.Content) == "" {
		return nil
	}
	return &Event{Kind: EventUserMessageFlushed, StepID: stepID, UserMessage: msg.Content, UserMessageBatch: []string{msg.Content}, CommittedTranscriptChanged: true}
}

func (e *Engine) flushPendingUserInjections(stepID string, queueItemIDs map[string]struct{}) (int, error) {
	e.ensureOrchestrationCollaborators()
	return e.messageFlow.FlushPendingUserInjections(stepID, queueItemIDs)
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
