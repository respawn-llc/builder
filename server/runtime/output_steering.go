package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	shelltool "core/server/tools/shell"
	"core/shared/textutil"
	"core/shared/transcript"

	"github.com/google/uuid"
)

type steeringPriority int

type directOrderedMutationTurn struct{}

func (directOrderedMutationTurn) Apply(apply func() error) error {
	if apply == nil {
		return errors.New("ordered mutation is required")
	}
	return apply()
}

func (directOrderedMutationTurn) RetainLease() (OrderedMutationLease, error) {
	return nil, errors.New("direct ordered mutation turn cannot retain queue capacity")
}

const (
	steeringPriorityRuntimeContext steeringPriority = iota
	steeringPriorityUser
	steeringPriorityNormal
	steeringPriorityRuntimeEvent
)

type steeringIntent struct {
	priority steeringPriority
	items    []steeringItem
}

type steeringItem struct {
	message                     *steeringMessage
	committedAssistant          *steeringCommittedAssistantMessage
	completedResponseResolution *steeringCompletedResponseResolution
	localEntry                  *steeringLocalEntry
	historyReplace              *steeringHistoryReplacement
	toolCompletion              *tools.Result
	queuedFlush                 *steeringQueuedUserMessageFlush
	pendingUserClaim            *steeringPendingUserInjectionClaim
	event                       *Event
	streaming                   *steeringStreamingOutput
	cacheWarning                *steeringCacheWarning
	cacheObservation            *steeringCacheObservation
	liveToolAbort               *steeringLiveToolAbort
	commitReceipt               *session.CommitReceipt
}

type steeringMessage struct {
	message     llm.Message
	eventPolicy steeringMessageEventPolicy
	persist     bool
}

type steeringLocalEntry struct {
	entry storedLocalEntry
}

type steeringCommittedAssistantMessage struct {
	message    llm.Message
	coordinate *committedAssistantCoordinate
}

type committedAssistantCoordinate struct {
	start int
}

type completedResponseResolutionInstructionKind uint8

const (
	completedResponseResolutionInstructionInvalid completedResponseResolutionInstructionKind = iota
	completedResponseResolutionInstructionFinalize
	completedResponseResolutionInstructionDiscard
)

type completedResponseResolutionInstruction struct {
	kind               completedResponseResolutionInstructionKind
	committedAssistant *steeringCommittedAssistantMessage
	abortReason        *AssistantStreamAbortReason
}

type completedResponseResolutionKind uint8

const (
	completedResponseResolutionInvalid completedResponseResolutionKind = iota
	completedResponseResolutionFinalized
	completedResponseResolutionDiscarded
	completedResponseResolutionAbsent
)

type completedResponseResolutionOutcome struct {
	kind                             completedResponseResolutionKind
	streamID                         *uuid.UUID
	committedAssistantEventPublished bool
}

type steeringCompletedResponseResolution struct {
	instruction completedResponseResolutionInstruction
	outcome     *completedResponseResolutionOutcome
}

type steeringHistoryReplacement struct {
	payload          historyReplacementPayload
	projectedEntries []ChatEntry
}

type steeringStreamingOutput struct {
	assistantDelta *llm.AssistantDelta
	reasoningDelta *llm.ReasoningSummaryDelta
	clearState     bool
	resetEvents    bool
	abortReason    *AssistantStreamAbortReason
}

type steeringCacheWarning struct {
	warning    transcript.CacheWarning
	visibility transcript.EntryVisibility
	emit       bool
}

type steeringCacheObservation struct {
	records    []session.EventRecordPayload
	response   persistedCacheResponseObserved
	warning    transcript.CacheWarning
	hasWarning bool
	visibility transcript.EntryVisibility
	emit       bool
}

type steeringLiveToolAbort struct {
	reason string
}

type steeringQueuedUserMessageFlush struct {
	text       string
	batch      []string
	queueItems []QueuedUserMessage
}

type steeringPendingUserInjectionClaim struct {
	selection userInjectionSelection
	result    *userInjectionCommitResult
}

type steeringMessageEventPolicy uint8

const (
	steeringMessageEventDefault steeringMessageEventPolicy = iota
	steeringMessageEventNone
)

func steerMessagesWithPersistenceIntent(priority steeringPriority, eventPolicy steeringMessageEventPolicy, persist bool, messages []llm.Message) steeringIntent {
	items := make([]steeringItem, 0, len(messages))
	for _, message := range messages {
		msg := message
		items = append(items, steeringItem{message: &steeringMessage{
			message:     msg,
			eventPolicy: eventPolicy,
			persist:     persist,
		}})
	}
	return steeringIntent{priority: priority, items: items}
}

func steerLocalEntryIntent(entry storedLocalEntry) steeringIntent {
	copyEntry := entry
	return steeringIntent{
		priority: steeringPriorityNormal,
		items: []steeringItem{{localEntry: &steeringLocalEntry{
			entry: copyEntry,
		}}},
	}
}

func steerHistoryReplacementIntent(engine string, mode compactionMode, compactionNumber int, pendingHandoffFutureMessage string, lastCommittedAssistantFinalAnswer string, items []llm.ResponseItem) steeringIntent {
	preparedItems := llm.PrepareOpenAIInputItems(items)
	payload := historyReplacementPayload{
		Engine:                            normalizeHistoryReplacementEngine(engine),
		Mode:                              string(mode),
		CompactionNumber:                  textutil.Value(compactionNumber),
		PendingHandoffFutureMessage:       textutil.OptionalExactString(pendingHandoffFutureMessage),
		LastCommittedAssistantFinalAnswer: textutil.OptionalExactString(lastCommittedAssistantFinalAnswer),
		Items:                             llm.CloneResponseItems(preparedItems),
	}
	return steeringIntent{
		priority: steeringPriorityNormal,
		items: []steeringItem{{historyReplace: &steeringHistoryReplacement{
			payload:          payload,
			projectedEntries: transcriptEntriesFromHistoryReplacement(payload.Items),
		}}},
	}
}

func steerToolCompletionIntent(result tools.Result) steeringIntent {
	copyResult := cloneToolResult(result)
	return steeringIntent{
		priority: steeringPriorityNormal,
		items:    []steeringItem{{toolCompletion: &copyResult}},
	}
}

func steerQueuedUserMessageFlushIntent(text string, batch []string, queueItems []QueuedUserMessage) steeringIntent {
	return steeringIntent{
		priority: steeringPriorityUser,
		items: []steeringItem{{queuedFlush: &steeringQueuedUserMessageFlush{
			text:       text,
			batch:      append([]string(nil), batch...),
			queueItems: append([]QueuedUserMessage(nil), queueItems...),
		}}},
	}
}

func steerPendingUserInjectionClaimIntent(selection userInjectionSelection, result *userInjectionCommitResult) steeringIntent {
	return steeringIntent{
		priority: steeringPriorityUser,
		items: []steeringItem{{pendingUserClaim: &steeringPendingUserInjectionClaim{
			selection: selection,
			result:    result,
		}}},
	}
}

func steerEventIntent(evt Event) steeringIntent {
	copyEvent := evt
	return steeringIntent{
		priority: steeringPriorityRuntimeEvent,
		items:    []steeringItem{{event: &copyEvent}},
	}
}

func steerLiveToolAbortIntent(reason string) steeringIntent {
	return steeringIntent{
		priority: steeringPriorityRuntimeEvent,
		items:    []steeringItem{{liveToolAbort: &steeringLiveToolAbort{reason: strings.TrimSpace(reason)}}},
	}
}

func steerCommittedAssistantMessageIntent(msg llm.Message, coordinate *committedAssistantCoordinate) steeringIntent {
	return steeringIntent{
		priority: steeringPriorityRuntimeEvent,
		items: []steeringItem{{committedAssistant: &steeringCommittedAssistantMessage{
			message:    msg,
			coordinate: cloneCommittedAssistantCoordinate(coordinate),
		}}},
	}
}

func completedResponseFinalizeInstruction(msg llm.Message, coordinate *committedAssistantCoordinate) completedResponseResolutionInstruction {
	return completedResponseResolutionInstruction{
		kind: completedResponseResolutionInstructionFinalize,
		committedAssistant: &steeringCommittedAssistantMessage{
			message:    msg,
			coordinate: cloneCommittedAssistantCoordinate(coordinate),
		},
	}
}

func cloneCommittedAssistantCoordinate(coordinate *committedAssistantCoordinate) *committedAssistantCoordinate {
	if coordinate == nil {
		return nil
	}
	copyCoordinate := *coordinate
	return &copyCoordinate
}

func completedResponseDiscardInstruction() completedResponseResolutionInstruction {
	reason := AssistantStreamAbortSuperseded
	return completedResponseResolutionInstruction{
		kind:        completedResponseResolutionInstructionDiscard,
		abortReason: &reason,
	}
}

func steerCompletedResponseResolutionIntent(instruction completedResponseResolutionInstruction, outcome *completedResponseResolutionOutcome) steeringIntent {
	return steeringIntent{
		priority: steeringPriorityRuntimeEvent,
		items: []steeringItem{{completedResponseResolution: &steeringCompletedResponseResolution{
			instruction: instruction,
			outcome:     outcome,
		}}},
	}
}

func steerAssistantDeltaIntent(delta llm.AssistantDelta) steeringIntent {
	copyDelta := delta
	return steeringIntent{
		priority: steeringPriorityRuntimeEvent,
		items:    []steeringItem{{streaming: &steeringStreamingOutput{assistantDelta: &copyDelta}}},
	}
}

func steerReasoningDeltaIntent(delta llm.ReasoningSummaryDelta) steeringIntent {
	copyDelta := delta
	return steeringIntent{
		priority: steeringPriorityRuntimeEvent,
		items:    []steeringItem{{streaming: &steeringStreamingOutput{reasoningDelta: &copyDelta}}},
	}
}

func steerClearStreamingStateIntent() steeringIntent {
	reason := AssistantStreamAbortSuperseded
	return steeringIntent{
		priority: steeringPriorityRuntimeEvent,
		items:    []steeringItem{{streaming: &steeringStreamingOutput{clearState: true, resetEvents: true, abortReason: &reason}}},
	}
}

func steerCacheWarningIntent(warning transcript.CacheWarning, visibility transcript.EntryVisibility, emit bool) steeringIntent {
	copyWarning := warning
	return steeringIntent{
		priority: steeringPriorityRuntimeEvent,
		items: []steeringItem{{cacheWarning: &steeringCacheWarning{
			warning:    copyWarning,
			visibility: normalizeRuntimeEntryVisibility(visibility),
			emit:       emit,
		}}},
	}
}

func steerCacheObservationIntent(records []session.EventRecordPayload, response persistedCacheResponseObserved, warning *transcript.CacheWarning, visibility transcript.EntryVisibility, emit bool) steeringIntent {
	copyRecords := append([]session.EventRecordPayload(nil), records...)
	var copyWarning transcript.CacheWarning
	if warning != nil {
		copyWarning = *warning
	}
	return steeringIntent{
		priority: steeringPriorityRuntimeEvent,
		items: []steeringItem{{cacheObservation: &steeringCacheObservation{
			records:    copyRecords,
			response:   response,
			warning:    copyWarning,
			hasWarning: warning != nil,
			visibility: normalizeRuntimeEntryVisibility(visibility),
			emit:       emit,
		}}},
	}
}

func (e *Engine) steer(stepID string, intents ...steeringIntent) error {
	if e.closed.Load() {
		return ErrEngineClosed
	}
	return e.steerOrdered(stepID, intents...)
}

func (e *Engine) steerRuntimeClose(stepID string, intents ...steeringIntent) error {
	if e == nil {
		return nil
	}
	return e.steerOrdered(stepID, intents...)
}

func (e *Engine) steerWithCommitReceipt(stepID string, intent steeringIntent) (session.CommitReceipt, error) {
	return e.steerWithCommitReceiptAndTurn(nil, stepID, intent)
}

func (e *Engine) steerWithCommitReceiptAndTurn(turn OrderedMutationTurn, stepID string, intent steeringIntent) (session.CommitReceipt, error) {
	if len(intent.items) != 1 {
		return session.CommitReceipt{}, fmt.Errorf(
			"commit receipt requires exactly one steering item (items=%d)",
			len(intent.items),
		)
	}
	receipt := session.CommitReceipt{}
	intent.items[0].commitReceipt = &receipt
	err := e.steerAndTurn(turn, stepID, intent)
	return receipt, err
}

func (e *Engine) steerOrdered(stepID string, intents ...steeringIntent) error {
	return e.steerAndTurn(nil, stepID, intents...)
}

func (e *Engine) steerAndTurn(turn OrderedMutationTurn, stepID string, intents ...steeringIntent) error {
	ordered := make([]steeringIntent, 0, len(intents))
	for _, intent := range intents {
		if len(intent.items) == 0 {
			continue
		}
		ordered = append(ordered, intent)
	}
	if len(ordered) == 0 {
		return nil
	}
	if turn != nil {
		return turn.Apply(func() error {
			return e.applySteeringIntents(stepID, ordered, turn)
		})
	}
	if mutation := e.executionMutationSnapshot(); mutation != nil {
		return mutation(context.Background(), func(turn OrderedMutationTurn) error {
			return turn.Apply(func() error {
				return e.applySteeringIntents(stepID, ordered, turn)
			})
		})
	}
	if e.cfg.OrderedMutation != nil {
		return e.cfg.OrderedMutation(func(turn OrderedMutationTurn) error {
			if turn == nil {
				return errors.New("ordered mutation turn is required")
			}
			return turn.Apply(func() error {
				return e.applySteeringIntents(stepID, ordered, turn)
			})
		})
	}
	return e.applySteeringIntents(stepID, ordered, nil)
}

func (e *Engine) applySteeringIntents(stepID string, ordered []steeringIntent, turn OrderedMutationTurn) error {
	e.outputMutationMu.Lock()
	defer e.outputMutationMu.Unlock()
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].priority < ordered[j].priority
	})
	for _, intent := range ordered {
		for _, item := range intent.items {
			if err := e.applySteeringItem(stepID, item, turn); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *Engine) applySteeringIntentInline(stepID string, intent steeringIntent) error {
	for _, item := range intent.items {
		if err := e.applySteeringItem(stepID, item, nil); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) resolveCompletedResponseStream(stepID string, instruction completedResponseResolutionInstruction) (completedResponseResolutionOutcome, error) {
	outcome := completedResponseResolutionOutcome{}
	if err := e.steerOrdered(stepID, steerCompletedResponseResolutionIntent(instruction, &outcome)); err != nil {
		return completedResponseResolutionOutcome{}, err
	}
	if outcome.kind == completedResponseResolutionInvalid {
		return completedResponseResolutionOutcome{}, errors.New("completed response stream resolution produced no outcome")
	}
	return outcome, nil
}

func (e *Engine) applySteeringItem(stepID string, item steeringItem, turn OrderedMutationTurn) error {
	if item.message != nil {
		receipt, err := e.appendMessageRaw(stepID, item.message.message, item.message.eventPolicy, item.message.persist)
		item.recordCommitReceipt(receipt)
		return err
	}
	if item.committedAssistant != nil {
		return e.emitCommittedAssistantMessageRaw(stepID, *item.committedAssistant)
	}
	if item.completedResponseResolution != nil {
		resolution := item.completedResponseResolution
		if resolution.outcome == nil {
			return errors.New("completed response stream resolution requires an outcome destination")
		}
		outcome, err := e.resolveCompletedResponseStreamRaw(stepID, resolution.instruction)
		if err != nil {
			return err
		}
		*resolution.outcome = outcome
		return nil
	}
	if item.localEntry != nil {
		receipt, err := e.appendPersistedLocalEntryRecordRaw(stepID, item.localEntry.entry)
		item.recordCommitReceipt(receipt)
		return err
	}
	if item.historyReplace != nil {
		receipt, err := e.replaceHistoryRaw(stepID, *item.historyReplace)
		item.recordCommitReceipt(receipt)
		return err
	}
	if item.toolCompletion != nil {
		completion := e.finalizeLiveToolCompletion(*item.toolCompletion)
		transition, _ := completion.Result.TransientMetadata.(interface {
			Commit() error
			Abort() error
			Snapshot() shelltool.Snapshot
		})
		completion.Result.TransientMetadata = nil
		receipt, err := e.persistFinalizedToolCompletionRaw(stepID, completion)
		item.recordCommitReceipt(receipt)
		if !receipt.Committed {
			if transition != nil {
				err = errors.Join(err, transition.Abort())
			}
			return err
		}
		if transition != nil {
			if commitErr := transition.Commit(); commitErr != nil {
				err = errors.Join(err, commitErr)
			}
			err = errors.Join(err, e.emitRaw(Event{
				Kind:       EventBackgroundUpdated,
				StepID:     stepID,
				Background: backgroundShellEventFromSnapshot(transition.Snapshot()),
			}))
		}
		if receipt.Committed {
			result := cloneToolResult(completion.Result)
			e.transcriptRuntimeState().CompleteLiveTool(result.CallID)
			err = errors.Join(err, e.emitRaw(Event{Kind: EventToolCallCompleted, StepID: stepID, ToolResult: &result, CommittedTranscriptChanged: true}))
			if completion.OperatorFeedback != nil {
				entry := localEntryChatEntryForStep(*completion.OperatorFeedback, stepID)
				err = errors.Join(err, e.emitRaw(Event{
					Kind:                       EventLocalEntryAdded,
					StepID:                     stepID,
					LocalEntry:                 entry,
					CommittedTranscriptChanged: true,
				}))
			}
		}
		return err
	}
	if item.queuedFlush != nil {
		receipt, err := e.appendQueuedUserMessageFlush(stepID, item.queuedFlush.text, item.queuedFlush.batch, item.queuedFlush.queueItems)
		item.recordCommitReceipt(receipt)
		return err
	}
	if item.pendingUserClaim != nil {
		if item.pendingUserClaim.result == nil {
			return errors.New("pending user injection claim requires a result destination")
		}
		result, err := e.commitPendingUserInjectionsInTurn(stepID, item.pendingUserClaim.selection, turn)
		*item.pendingUserClaim.result = result
		return err
	}
	if item.event != nil {
		evt := *item.event
		if evt.StepID == "" {
			evt.StepID = stepID
		}
		if evt.Kind == EventToolCallStarted && evt.ToolCall != nil {
			if err := e.transcriptRuntimeState().RecordLiveToolStart(evt.StepID, *evt.ToolCall); err != nil {
				return err
			}
		}
		return e.emitRaw(evt)
	}
	if item.cacheWarning != nil {
		warning := item.cacheWarning.warning
		visibility := normalizeRuntimeEntryVisibility(item.cacheWarning.visibility)
		record, adaptErr := sessionCacheWarningRecordFromRuntime(warning)
		if adaptErr != nil {
			return fmt.Errorf("adapt cache warning record: %w", adaptErr)
		}
		_, receipt, appendErr := e.eventLog.AppendRecord(textutil.OptionalExactString(stepID), record)
		item.recordCommitReceipt(receipt)
		if appendErr != nil && !receipt.Committed {
			return appendErr
		}
		e.transcriptRuntimeState().AppendCommittedEntryWithVisibility(cacheWarningTranscriptRole, transcript.CacheWarningText(warning), visibility)
		if item.cacheWarning.emit {
			appendErr = errors.Join(appendErr, e.emitRaw(Event{Kind: EventCacheWarning, StepID: stepID, CacheWarning: copyCacheWarning(&warning), CacheWarningVisibility: visibility, CommittedTranscriptChanged: true}))
		}
		return appendErr
	}
	if item.cacheObservation != nil {
		observation := item.cacheObservation
		_, receipt, appendErr := e.eventLog.AppendRecordsAtomic(textutil.OptionalExactString(stepID), observation.records)
		item.recordCommitReceipt(receipt)
		if !receipt.Committed {
			return appendErr
		}
		e.modelRequests().RequestCache().RecordResponse(observation.response)
		if observation.hasWarning {
			warning := observation.warning
			visibility := normalizeRuntimeEntryVisibility(observation.visibility)
			e.transcriptRuntimeState().AppendCommittedEntryWithVisibility(cacheWarningTranscriptRole, transcript.CacheWarningText(warning), visibility)
			if observation.emit {
				appendErr = errors.Join(appendErr, e.emitRaw(Event{Kind: EventCacheWarning, StepID: stepID, CacheWarning: copyCacheWarning(&warning), CacheWarningVisibility: visibility, CommittedTranscriptChanged: true}))
			}
		}
		return appendErr
	}
	if item.liveToolAbort != nil {
		return e.emitLiveToolAbortsRaw(stepID, item.liveToolAbort.reason)
	}
	if item.streaming != nil {
		if item.streaming.assistantDelta != nil {
			delta := *item.streaming.assistantDelta
			if delta.Text == "" {
				return nil
			}
			revision, err := e.TranscriptRevision()
			if err != nil {
				return err
			}
			metadata, streamID := e.transcriptRuntimeState().AppendStreamingDelta(stepID, revision, e.CommittedTranscriptEntryCount(), delta.Text, delta.Phase)
			return e.emitRaw(Event{Kind: EventAssistantDelta, StepID: stepID, AssistantDelta: delta.Text, AssistantDeltaPhase: delta.Phase, AssistantStreamMetadata: metadata, AssistantTranscriptStreamID: streamID})
		}
		if item.streaming.reasoningDelta != nil {
			delta := *item.streaming.reasoningDelta
			return e.emitRaw(Event{Kind: EventReasoningDelta, StepID: stepID, ReasoningDelta: &delta})
		}
		var clearedMetadata *AssistantStreamMetadata
		var clearedStreamID *uuid.UUID
		if item.streaming.clearState {
			clearedMetadata, clearedStreamID = e.clearStreamingAssistantStateRaw()
		}
		if item.streaming.resetEvents {
			return e.emitStreamingAssistantCleanupEventsRaw(stepID, clearedMetadata, clearedStreamID, item.streaming.abortReason)
		}
		return nil
	}
	return nil
}

func backgroundShellEventFromSnapshot(snapshot shelltool.Snapshot) *BackgroundShellEvent {
	return &BackgroundShellEvent{
		Type:              BackgroundShellEventBackgrounded,
		ID:                snapshot.ID,
		ActivityID:        snapshot.ActivityID,
		OwnerRunID:        snapshot.OwnerRunID,
		OwnerStepID:       snapshot.OwnerStepID,
		State:             snapshot.State,
		Command:           snapshot.Command,
		Workdir:           snapshot.Workdir,
		LogPath:           snapshot.LogPath,
		ExitCode:          snapshot.ExitCode,
		UserRequestedKill: snapshot.KillRequested,
	}
}

func (item steeringItem) recordCommitReceipt(receipt session.CommitReceipt) {
	if item.commitReceipt != nil {
		*item.commitReceipt = receipt
	}
}

func (e *Engine) replaceHistoryRaw(stepID string, replacement steeringHistoryReplacement) (session.CommitReceipt, error) {
	reminderIssued := false
	projectedStart := e.CommittedTranscriptEntryCount()
	replacement.payload.CommittedEntryStart = &projectedStart
	preparedItems := llm.CloneResponseItems(replacement.payload.Items)
	replacement.payload.LatestRollbackCandidate = e.transcriptRuntimeState().LatestRollbackCandidate()
	record, adaptErr := sessionHistoryReplacementRecordFromRuntime(replacement.payload)
	if adaptErr != nil {
		return session.CommitReceipt{}, fmt.Errorf("adapt history replacement record: %w", adaptErr)
	}
	_, receipt, appendErr := e.eventLog.AppendCompactionHistoryReplacement(
		textutil.OptionalExactString(stepID),
		record,
	)
	if appendErr != nil && !receipt.Committed {
		return receipt, appendErr
	}
	e.lockedContractState().MarkPromptFacingSnapshotsStale()
	// Compaction reinjects canonical generation context, including base meta,
	// into the same replacement payload. Mirror the restore-time length signal
	// here rather than scanning individual items.
	e.baseMetaInjected = len(preparedItems) > 0
	if replacement.payload.CompactionNumber != nil {
		e.compactionRuntimeState().SetCount(*replacement.payload.CompactionNumber)
	}
	e.resetCurrentPreciseInputTracking()
	e.resetLocalDiagnostics()
	e.transcriptRuntimeState().ReplaceHistoryAtCommittedEntryStart(stepID, preparedItems, &projectedStart)
	e.compactionRuntimeState().SetSoonReminderIssued(false)
	emitErr := e.emitProjectedHistoryReplacementEntriesRaw(
		stepID,
		projectedStart,
		replacement.projectedEntries,
	)
	emitErr = errors.Join(
		emitErr,
		e.emitRaw(Event{Kind: EventConversationUpdated, StepID: stepID}),
	)
	// The durable history replacement is the compaction boundary. Apply that
	// committed replacement in memory before resetting workflow-adjacent state,
	// so any reset failure cannot make the live engine diverge from restore.
	budgetResetErr := e.resetWorkflowProtocolViolationBudget(context.Background())
	return receipt, errors.Join(
		appendErr,
		budgetResetErr,
		emitErr,
		e.store.SetCompactionSoonReminderIssued(reminderIssued),
	)
}

func (e *Engine) emitProjectedHistoryReplacementEntriesRaw(
	stepID string,
	start int,
	entries []ChatEntry,
) error {
	if e == nil || len(entries) == 0 {
		return nil
	}
	// Live subscribers must observe the same committed transcript progression that
	// restart hydration reconstructs from history_replaced. Emit projected
	// compaction rows before any later local entry.
	if start < 0 {
		start = 0
	}
	for idx, entry := range entries {
		copyEntry := clonePersistedChatEntry(entry)
		if err := e.emitRaw(Event{
			Kind:                       EventLocalEntryAdded,
			StepID:                     stepID,
			LocalEntry:                 &copyEntry,
			LocalEntryProjected:        true,
			CommittedTranscriptChanged: true,
			CommittedEntryStart:        start + idx,
			CommittedEntryStartSet:     true,
			CommittedEntryCount:        start + idx + 1,
		}); err != nil {
			return err
		}
	}
	return nil
}

func cloneToolResult(result tools.Result) tools.Result {
	copyResult := result
	copyResult.Output = append(json.RawMessage(nil), result.Output...)
	copyResult.Presentation = clonePersistedToolCallMeta(result.Presentation)
	return copyResult
}
