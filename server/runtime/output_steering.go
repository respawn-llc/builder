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
	"core/shared/textutil"
	"core/shared/transcript"

	"github.com/google/uuid"
)

type steeringPriority int

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
	goalNoticeAndStatus         *steeringGoalNoticeAndStatus
	committedAssistant          *steeringCommittedAssistantMessage
	completedResponseResolution *steeringCompletedResponseResolution
	localEntry                  *steeringLocalEntry
	historyReplace              *steeringHistoryReplacement
	toolCompletion              *tools.Result
	queuedFlush                 *steeringQueuedUserMessageFlush
	event                       *Event
	streaming                   *steeringStreamingOutput
	cacheWarning                *steeringCacheWarning
	cacheObservation            *steeringCacheObservation
	liveToolAbort               *steeringLiveToolAbort
	agentStepFinalization       *steeringAgentStepFinalization
	commitReceipt               *session.CommitReceipt
}

type steeringAgentStepFinalization struct {
	payloads                 []session.EventRecordPayload
	receipt                  *session.CommitReceipt
	deferredToolStarts       []Event
	stagedToolResults        []tools.Result
	stagedQueuedFlushes      []steeringQueuedUserMessageFlush
	deferredStreamCleanup    *deferredAssistantStreamCleanup
	deferredTranscriptUpdate bool
}

type steeringMessage struct {
	message     llm.Message
	eventPolicy steeringMessageEventPolicy
	persist     bool
}

type steeringGoalNoticeAndStatus struct {
	message llm.Message
	update  GoalStatusUpdate
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
	payloadIndex int
	text         string
	batch        []string
	queueItems   []QueuedUserMessage
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
			projectedEntries: transcriptEntriesFromHistoryReplacement(payload.Items, payload.CompactionNumber),
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

func steerEventIntent(evt Event) steeringIntent {
	copyEvent := evt
	return steeringIntent{
		priority: steeringPriorityRuntimeEvent,
		items:    []steeringItem{{event: &copyEvent}},
	}
}

func steerGoalNoticeAndStatusIntent(message llm.Message, update GoalStatusUpdate) steeringIntent {
	return steeringIntent{
		priority: steeringPriorityRuntimeContext,
		items: []steeringItem{{goalNoticeAndStatus: &steeringGoalNoticeAndStatus{
			message: message,
			update:  update,
		}}},
	}
}

func steerLiveToolAbortIntent(reason string) steeringIntent {
	return steeringIntent{
		priority: steeringPriorityRuntimeEvent,
		items:    []steeringItem{{liveToolAbort: &steeringLiveToolAbort{reason: strings.TrimSpace(reason)}}},
	}
}

func steerAgentStepFinalizationIntent(
	payloads []session.EventRecordPayload,
	receipt *session.CommitReceipt,
	deferredToolStarts []Event,
	stagedToolResults []tools.Result,
	stagedQueuedFlushes []steeringQueuedUserMessageFlush,
	deferredStreamCleanup *deferredAssistantStreamCleanup,
	deferredTranscriptUpdate bool,
) steeringIntent {
	return steeringIntent{
		priority: steeringPriorityRuntimeEvent,
		items: []steeringItem{{agentStepFinalization: &steeringAgentStepFinalization{
			payloads:                 append([]session.EventRecordPayload(nil), payloads...),
			receipt:                  receipt,
			deferredToolStarts:       append([]Event(nil), deferredToolStarts...),
			stagedToolResults:        append([]tools.Result(nil), stagedToolResults...),
			stagedQueuedFlushes:      append([]steeringQueuedUserMessageFlush(nil), stagedQueuedFlushes...),
			deferredStreamCleanup:    deferredStreamCleanup,
			deferredTranscriptUpdate: deferredTranscriptUpdate,
		}}},
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
	if len(intent.items) != 1 {
		return session.CommitReceipt{}, fmt.Errorf(
			"commit receipt requires exactly one steering item (items=%d)",
			len(intent.items),
		)
	}
	receipt := session.CommitReceipt{}
	intent.items[0].commitReceipt = &receipt
	err := e.steer(stepID, intent)
	return receipt, err
}

func (e *Engine) steerOrdered(stepID string, intents ...steeringIntent) error {
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
	e.outputMutationMu.Lock()
	defer e.outputMutationMu.Unlock()
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].priority < ordered[j].priority
	})
	for _, intent := range ordered {
		for _, item := range intent.items {
			if err := e.applySteeringItem(stepID, item); err != nil {
				return err
			}
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

func (e *Engine) applySteeringItem(stepID string, item steeringItem) error {
	if item.message != nil {
		if item.message.persist && shouldStageAgentStepMessage(item.message.message) && e.agentStepBoundary(stepID) != nil && e.agentStepBoundary(stepID).Capturing() {
			return e.stageAgentStepMessageRaw(stepID, item.message.message, item.message.eventPolicy)
		}
		receipt, err := e.appendMessageRaw(stepID, item.message.message, item.message.eventPolicy, item.message.persist)
		item.recordCommitReceipt(receipt)
		return err
	}
	if item.goalNoticeAndStatus != nil {
		notice := item.goalNoticeAndStatus
		receipt, noticeErr := e.appendMessageRaw(
			stepID,
			notice.message,
			steeringMessageEventDefault,
			true,
		)
		item.recordCommitReceipt(receipt)
		if !receipt.Committed {
			return noticeErr
		}
		statusErr := e.emitRaw(Event{
			Kind:       EventGoalStatusUpdated,
			StepID:     stepID,
			GoalStatus: &notice.update,
		})
		return errors.Join(noticeErr, statusErr)
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
		if e.agentStepBoundary(stepID) != nil && e.agentStepBoundary(stepID).Capturing() && shouldStageAgentStepLocalEntry(item.localEntry.entry) {
			return e.stageAgentStepLocalEntryRaw(stepID, item.localEntry.entry)
		}
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
		if e.agentStepBoundary(stepID) != nil && e.agentStepBoundary(stepID).Capturing() {
			return e.stageAgentStepToolCompletionRaw(stepID, *item.toolCompletion)
		}
		completion := e.finalizeLiveToolCompletion(*item.toolCompletion)
		receipt, err := e.persistFinalizedToolCompletionRaw(stepID, completion)
		item.recordCommitReceipt(receipt)
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
		if boundary := e.agentStepBoundary(stepID); boundary != nil && boundary.Capturing() {
			return e.stageAgentStepQueuedUserMessageFlushRaw(stepID, *item.queuedFlush)
		}
		receipt, err := e.appendQueuedUserMessageFlush(stepID, item.queuedFlush.text, item.queuedFlush.batch, item.queuedFlush.queueItems)
		item.recordCommitReceipt(receipt)
		return err
	}
	if item.event != nil {
		evt := *item.event
		if evt.StepID == "" {
			evt.StepID = stepID
		}
		if evt.CommittedTranscriptChanged {
			if boundary := e.agentStepBoundary(stepID); boundary != nil && boundary.Capturing() {
				if evt.Kind == EventConversationUpdated && len(TranscriptEntriesFromEvent(evt)) == 0 {
					boundary.DeferCommittedTranscriptUpdate()
				}
				if evt.Kind == EventToolCallStarted {
					boundary.DeferCommittedToolStart(evt)
				}
				// Live runtime events may describe staged output, but they
				// cannot advance the committed transcript until the boundary
				// transaction has returned a durable receipt.
				evt.CommittedTranscriptChanged = false
			}
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
	if item.agentStepFinalization != nil {
		records, receipt, err := e.eventLog.AppendAgentStepFinalization(stepID, item.agentStepFinalization.payloads)
		if item.agentStepFinalization.receipt != nil {
			*item.agentStepFinalization.receipt = receipt
		}
		if receipt.Committed {
			committedEntryStart := e.CommittedTranscriptEntryCount()
			queuedFlushesByPayloadIndex := make(map[int]steeringQueuedUserMessageFlush, len(item.agentStepFinalization.stagedQueuedFlushes))
			for _, flush := range item.agentStepFinalization.stagedQueuedFlushes {
				queuedFlushesByPayloadIndex[flush.payloadIndex] = flush
			}
			deferredToolStartsEmitted := false
			emitDeferredToolStarts := func() {
				if deferredToolStartsEmitted {
					return
				}
				deferredToolStartsEmitted = true
				for _, deferred := range item.agentStepFinalization.deferredToolStarts {
					deferred.CommittedTranscriptChanged = true
					deferred.CommittedEntryCount = e.CommittedTranscriptEntryCount()
					err = errors.Join(err, e.emitRaw(deferred))
				}
			}
			for recordIndex, record := range records {
				payload, payloadErr := record.Payload()
				if payloadErr != nil {
					err = errors.Join(err, payloadErr)
					continue
				}
				switch typed := payload.(type) {
				case session.MessageRecord:
					message, restoreErr := llmMessageFromSessionRecord(typed)
					if restoreErr != nil {
						err = errors.Join(err, restoreErr)
						continue
					}
					start := committedEntryStart
					if mutation := tokenUsageMutationForMessage(message); mutation == tokenUsageMutationSignificant {
						e.markCurrentRequestShapeDirtyForSignificantMutation()
					} else {
						e.markCurrentRequestShapeDirty()
					}
					if projectionErr := e.transcriptRuntimeState().AppendMessage(stepID, message); projectionErr != nil {
						err = errors.Join(err, projectionErr)
						continue
					}
					committedEntryStart = e.CommittedTranscriptEntryCount()
					if flush, queuedFlush := queuedFlushesByPayloadIndex[recordIndex]; queuedFlush {
						normalizedItems := normalizedQueuedUserMessageStatusItems(flush.queueItems)
						messageText := ""
						if message.Content != nil {
							messageText = *message.Content
						}
						e.emitRaw(Event{
							Kind:                         EventUserMessageFlushed,
							StepID:                       stepID,
							UserMessage:                  messageText,
							UserMessageBatch:             append([]string(nil), flush.batch...),
							UserMessageBatchQueueItemIDs: queuedUserMessageStatusItemIDs(normalizedItems),
							UserMessageBatchQueuedItems:  queuedUserMessageIdentities(normalizedItems),
							CommittedTranscriptChanged:   true,
							CommittedEntryStart:          start,
							CommittedEntryStartSet:       true,
							CommittedEntryCount:          e.CommittedTranscriptEntryCount(),
						})
						for _, item := range normalizedItems {
							e.unmarkQueuedUserInjectionForAutoDrain(item.ID)
							e.emitQueuedUserMessageStatus(item, QueuedUserMessageSubmitted, "", false)
						}
						e.completeLiveRunQueueItems(queuedUserMessageIDSet(normalizedItems))
					} else if message.Role != llm.RoleTool && shouldEmitCommittedMessageEvent(message) && e.CommittedTranscriptEntryCount() > start {
						eventKind := EventConversationUpdated
						if message.Role == llm.RoleAssistant {
							eventKind = EventAssistantMessage
						}
						var streamMetadata *AssistantStreamMetadata
						var streamID *uuid.UUID
						if eventKind == EventAssistantMessage {
							if cleanup := item.agentStepFinalization.deferredStreamCleanup; cleanup != nil && cleanup.finalizeAssistant {
								streamMetadata = cloneAssistantStreamMetadata(cleanup.metadata)
								streamID = cloneTranscriptStreamID(cleanup.streamID)
							}
						}
						err = errors.Join(err, e.emitRaw(Event{
							Kind:                        eventKind,
							StepID:                      stepID,
							CommittedTranscriptChanged:  true,
							CommittedEntryStart:         start,
							CommittedEntryStartSet:      true,
							CommittedEntryCount:         e.CommittedTranscriptEntryCount(),
							Message:                     message,
							AssistantStreamMetadata:     streamMetadata,
							AssistantTranscriptStreamID: streamID,
						}))
					}
				case session.ToolCompletionRecord:
					emitDeferredToolStarts()
					completion, restoreErr := storedToolCompletionFromSessionRecord(typed)
					if restoreErr != nil {
						err = errors.Join(err, restoreErr)
						continue
					}
					before := e.CommittedTranscriptEntryCount()
					backgroundSessionID, hasBackgroundSession := harvestedBackgroundCompletionSessionIDFromStored(completion)
					e.applyCommittedStoredToolCompletion(
						completion,
						backgroundSessionID,
						hasBackgroundSession,
					)
					e.transcriptRuntimeState().CompleteLiveTool(completion.CallID)
					result := storedToolCompletionResult(completion)
					for _, stagedResult := range item.agentStepFinalization.stagedToolResults {
						if stagedResult.CallID == result.CallID {
							result = cloneToolResult(stagedResult)
							break
						}
					}
					err = errors.Join(err, e.emitRaw(Event{
						Kind:                       EventToolCallCompleted,
						StepID:                     stepID,
						ToolResult:                 &result,
						CommittedTranscriptChanged: e.CommittedTranscriptEntryCount() > before,
						CommittedEntryStart:        before,
						CommittedEntryStartSet:     e.CommittedTranscriptEntryCount() > before,
						CommittedEntryCount:        e.CommittedTranscriptEntryCount(),
					}))
				case session.LocalEntryRecord:
					entry, restoreErr := storedLocalEntryFromSessionRecord(typed)
					if restoreErr != nil {
						err = errors.Join(err, restoreErr)
						continue
					}
					projected := localEntryChatEntryForStep(entry, stepID)
					e.transcriptRuntimeState().AppendLocalEntryRecord(*projected, entry.AfterToolCallID)
					err = errors.Join(err, e.emitRaw(Event{
						Kind:                       EventLocalEntryAdded,
						StepID:                     stepID,
						LocalEntry:                 projected,
						CommittedTranscriptChanged: true,
					}))
				}
			}
			emitDeferredToolStarts()
			if item.agentStepFinalization.deferredTranscriptUpdate {
				err = errors.Join(err, e.emitRaw(Event{
					Kind:                       EventConversationUpdated,
					StepID:                     stepID,
					CommittedTranscriptChanged: true,
					CommittedEntryCount:        e.CommittedTranscriptEntryCount(),
				}))
			}
			if cleanup := item.agentStepFinalization.deferredStreamCleanup; cleanup != nil {
				if cleanup.finalizeAssistant && cleanup.streamID != nil {
					e.transcriptRuntimeState().RecordAssistantStreamFinalization(
						cleanup.committedEntryStart,
						cleanup.streamID,
					)
				}
				err = errors.Join(err, e.emitStreamingAssistantCleanupEventsRaw(
					stepID,
					cleanup.metadata,
					cleanup.streamID,
					cleanup.abortReason,
				))
			}
		}
		return err
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

func shouldStageAgentStepMessage(message llm.Message) bool {
	if message.Role == llm.RoleAssistant || message.Role == llm.RoleTool {
		return true
	}
	if message.MessageType == nil {
		return false
	}
	return *message.MessageType == llm.MessageTypeReviewerFeedback || *message.MessageType == llm.MessageTypeGoal
}

func shouldStageAgentStepLocalEntry(entry storedLocalEntry) bool {
	return strings.TrimSpace(entry.Role) != "" && strings.TrimSpace(entry.Text) != ""
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
	if receipt.Committed {
		e.compactionRuntimeState().SetManualCompactionEligible(false)
	}
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
	e.transcriptRuntimeState().ReplaceHistoryAtCommittedEntryStart(
		stepID,
		preparedItems,
		&projectedStart,
		replacement.projectedEntries,
	)
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
