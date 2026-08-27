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
	"core/shared/runtimeids"
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
	assistantCommit             *steeringAssistantCommit
	goalNoticeAndStatus         *steeringGoalNoticeAndStatus
	committedAssistant          *steeringCommittedAssistantMessage
	completedResponseResolution *steeringCompletedResponseResolution
	localEntry                  *steeringLocalEntry
	reviewerFeedback            *steeringReviewerFeedback
	reviewerError               *steeringReviewerError
	historyReplace              *steeringHistoryReplacement
	toolCompletion              *tools.Result
	resultGroupReport           *steeringResultGroupReport
	resultGroupFlush            *steeringResultGroupFlush
	resultGroupClose            *steeringResultGroupClose
	missingToolOutputRepair     *steeringMissingToolOutputRepair
	queuedFlush                 *steeringQueuedUserMessageFlush
	queuedRestore               *steeringQueuedUserMessageRestore
	compactionActivity          *steeringCompactionActivity
	event                       *Event
	streaming                   *steeringStreamingOutput
	cacheWarning                *steeringCacheWarning
	cacheObservation            *steeringCacheObservation
	liveToolAbort               *steeringLiveToolAbort
	commitReceipt               *session.CommitReceipt
}

type steeringMessage struct {
	message               llm.Message
	eventPolicy           steeringMessageEventPolicy
	persist               bool
	provenanceDestination **TranscriptCommittedRowProvenance
	emitUserFlushEvent    bool
}

type steeringAssistantCommit struct {
	message           llm.Message
	executableCallIDs map[string]struct{}
	result            *steeringAssistantCommitResult
}

type steeringAssistantCommitResult struct {
	provenance *TranscriptCommittedRowProvenance
	coordinate *committedAssistantCoordinate
	resolution completedResponseResolutionOutcome
}

type steeringGoalNoticeAndStatus struct {
	message llm.Message
	update  GoalStatusUpdate
}

type steeringLocalEntry struct {
	entry                  storedLocalEntry
	reasoningTraceIdentity *TranscriptReasoningTraceIdentity
}

type steeringReviewerFeedback struct {
	suggestions []string
	visibility  transcript.EntryVisibility
}

type steeringReviewerError struct {
	detail string
}

type steeringCommittedAssistantMessage struct {
	message    llm.Message
	coordinate *committedAssistantCoordinate
	provenance *TranscriptCommittedRowProvenance
}

type committedAssistantCoordinate struct {
	start int
}

type completedResponseResolutionInstructionKind uint8

const (
	completedResponseResolutionInstructionInvalid completedResponseResolutionInstructionKind = iota
	completedResponseResolutionInstructionFinalize
	completedResponseResolutionInstructionAbort
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

type steeringResultGroupReport struct {
	collector          *resultGroupCollector
	callID             string
	unit               *resultGroupUnit
	operationalFailure error
	outcome            **resultGroupReportOutcome
}

type steeringResultGroupFlush struct {
	collector *resultGroupCollector
	reason    ResultGroupFlushReason
	committed bool
}

type steeringResultGroupClose struct {
	collector *resultGroupCollector
}

type steeringStreamingOutput struct {
	assistantDelta *llm.AssistantDelta
	reasoningDelta *llm.ReasoningSummaryDelta
	clearState     bool
	clearReasoning bool
	reasoningReset *steeringReasoningReset
	resetEvents    bool
	abortReason    *AssistantStreamAbortReason
}

type steeringReasoningReset struct{}

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
	message    llm.Message
	batch      []string
	queueItems []QueuedUserMessage
}

type steeringQueuedUserMessageRestore struct {
	items []queuedUserMessage
}

type steeringCompactionActivity struct {
	active    bool
	requestID *runtimeids.CompactionRequestID
	mode      string
	count     int
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

func steerAssistantCommitIntent(
	msg llm.Message,
	executableCallIDs map[string]struct{},
	result *steeringAssistantCommitResult,
) steeringIntent {
	return steeringIntent{
		priority: steeringPriorityNormal,
		items: []steeringItem{{
			assistantCommit: &steeringAssistantCommit{
				message:           msg,
				executableCallIDs: executableCallIDs,
				result:            result,
			},
		}},
	}
}

func steerUserMessageWithFlushIntent(msg llm.Message) steeringIntent {
	intent := steerMessagesWithPersistenceIntent(steeringPriorityUser, steeringMessageEventNone, true, []llm.Message{msg})
	var provenance *TranscriptCommittedRowProvenance
	intent.items[0].message.provenanceDestination = &provenance
	intent.items[0].message.emitUserFlushEvent = true
	return intent
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

func steerReasoningLocalEntryIntent(entry storedLocalEntry, identity TranscriptReasoningTraceIdentity) steeringIntent {
	copyEntry := entry
	return steeringIntent{
		priority: steeringPriorityNormal,
		items: []steeringItem{{localEntry: &steeringLocalEntry{
			entry:                  copyEntry,
			reasoningTraceIdentity: &identity,
		}}},
	}
}

func steerReviewerFeedbackIntent(suggestions []string, visibility transcript.EntryVisibility) steeringIntent {
	return steeringIntent{priority: steeringPriorityNormal, items: []steeringItem{{reviewerFeedback: &steeringReviewerFeedback{
		suggestions: append([]string(nil), suggestions...), visibility: visibility,
	}}}}
}

func steerReviewerErrorIntent(detail string) steeringIntent {
	return steeringIntent{priority: steeringPriorityNormal, items: []steeringItem{{reviewerError: &steeringReviewerError{detail: detail}}}}
}

func steerHistoryReplacementIntent(engine string, mode compactionMode, compactionNumber int, lastCommittedAssistantFinalAnswer *string, items []llm.ResponseItem) steeringIntent {
	preparedItems := llm.PrepareOpenAIInputItems(items)
	payload := historyReplacementPayload{
		Engine:                            normalizeHistoryReplacementEngine(engine),
		Mode:                              string(mode),
		CompactionNumber:                  textutil.Value(compactionNumber),
		LastCommittedAssistantFinalAnswer: textutil.Pointer(lastCommittedAssistantFinalAnswer),
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

func steerResultGroupReportIntent(
	collector *resultGroupCollector,
	callID string,
	unit resultGroupUnit,
	outcome **resultGroupReportOutcome,
) steeringIntent {
	copyUnit := cloneResultGroupUnit(unit)
	return steeringIntent{
		priority: steeringPriorityNormal,
		items: []steeringItem{{resultGroupReport: &steeringResultGroupReport{
			collector: collector,
			callID:    callID,
			unit:      &copyUnit,
			outcome:   outcome,
		}}},
	}
}

func steerResultGroupOperationalFailureIntent(
	collector *resultGroupCollector,
	cause error,
) steeringIntent {
	return steeringIntent{
		priority: steeringPriorityNormal,
		items: []steeringItem{{resultGroupReport: &steeringResultGroupReport{
			collector:          collector,
			operationalFailure: cause,
		}}},
	}
}

func steerResultGroupFlushIntent(
	collector *resultGroupCollector,
	reason ResultGroupFlushReason,
) steeringIntent {
	return steeringIntent{
		priority: steeringPriorityNormal,
		items: []steeringItem{{resultGroupFlush: &steeringResultGroupFlush{
			collector: collector,
			reason:    reason,
		}}},
	}
}

func steerResultGroupCloseIntent(collector *resultGroupCollector) steeringIntent {
	return steeringIntent{
		priority: steeringPriorityNormal,
		items: []steeringItem{{resultGroupClose: &steeringResultGroupClose{
			collector: collector,
		}}},
	}
}

func steerQueuedUserMessageFlushIntent(message llm.Message, batch []string, queueItems []QueuedUserMessage) steeringIntent {
	return steeringIntent{
		priority: steeringPriorityUser,
		items: []steeringItem{{queuedFlush: &steeringQueuedUserMessageFlush{
			message:    message,
			batch:      append([]string(nil), batch...),
			queueItems: append([]QueuedUserMessage(nil), queueItems...),
		}}},
	}
}

func steerQueuedUserMessageRestoreIntent(items []queuedUserMessage) steeringIntent {
	return steeringIntent{
		priority: steeringPriorityUser,
		items: []steeringItem{{queuedRestore: &steeringQueuedUserMessageRestore{
			items: append([]queuedUserMessage(nil), items...),
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

func steerCompactionActivityIntent(
	active bool,
	requestID *runtimeids.CompactionRequestID,
	mode string,
	count int,
) steeringIntent {
	return steeringIntent{
		priority: steeringPriorityRuntimeEvent,
		items: []steeringItem{{compactionActivity: &steeringCompactionActivity{
			active:    active,
			requestID: requestID,
			mode:      strings.TrimSpace(mode),
			count:     count,
		}}},
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

func steerCommittedAssistantMessageIntent(msg llm.Message, coordinate *committedAssistantCoordinate, provenances ...*TranscriptCommittedRowProvenance) steeringIntent {
	return steeringIntent{
		priority: steeringPriorityRuntimeEvent,
		items: []steeringItem{{committedAssistant: &steeringCommittedAssistantMessage{
			message:    msg,
			coordinate: cloneCommittedAssistantCoordinate(coordinate),
			provenance: firstTranscriptCommittedRowProvenance(provenances),
		}}},
	}
}

func completedResponseFinalizeInstruction(msg llm.Message, coordinate *committedAssistantCoordinate, provenances ...*TranscriptCommittedRowProvenance) completedResponseResolutionInstruction {
	return completedResponseResolutionInstruction{
		kind: completedResponseResolutionInstructionFinalize,
		committedAssistant: &steeringCommittedAssistantMessage{
			message:    msg,
			coordinate: cloneCommittedAssistantCoordinate(coordinate),
			provenance: firstTranscriptCommittedRowProvenance(provenances),
		},
	}
}

func firstTranscriptCommittedRowProvenance(values []*TranscriptCommittedRowProvenance) *TranscriptCommittedRowProvenance {
	if len(values) == 0 {
		return nil
	}
	return cloneTranscriptCommittedRowProvenance(values[0])
}

func cloneCommittedAssistantCoordinate(coordinate *committedAssistantCoordinate) *committedAssistantCoordinate {
	if coordinate == nil {
		return nil
	}
	copyCoordinate := *coordinate
	return &copyCoordinate
}

func completedResponseAbortInstruction() completedResponseResolutionInstruction {
	reason := AssistantStreamAbortSuperseded
	return completedResponseResolutionInstruction{
		kind:        completedResponseResolutionInstructionAbort,
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

func steerClearReasoningStateIntent() steeringIntent {
	return steeringIntent{
		priority: steeringPriorityRuntimeEvent,
		items:    []steeringItem{{streaming: &steeringStreamingOutput{clearReasoning: true}}},
	}
}

func (e *Engine) resetReasoningAndClearStreamingState(stepID string) error {
	if e == nil {
		return nil
	}
	resetErr := e.steer(stepID, steerResetReasoningStateIntent())
	clearErr := e.steer(stepID, steerClearStreamingStateIntent())
	return errors.Join(resetErr, clearErr)
}

func steerResetReasoningStateIntent() steeringIntent {
	return steeringIntent{
		priority: steeringPriorityRuntimeEvent,
		items:    []steeringItem{{streaming: &steeringStreamingOutput{reasoningReset: &steeringReasoningReset{}}}},
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

type steeringProvenance struct {
	exactStep *runtimeids.StepID
}

func exactSteeringProvenance(stepID string) (steeringProvenance, error) {
	identity, err := runtimeids.ParseStepID(stepID)
	if err != nil {
		return steeringProvenance{}, err
	}
	return steeringProvenance{exactStep: &identity}, nil
}

func sessionSteeringProvenance() steeringProvenance {
	return steeringProvenance{}
}

func (p steeringProvenance) stepID() *string {
	if p.exactStep == nil {
		return nil
	}
	stepID := p.exactStep.String()
	return &stepID
}

func (p steeringProvenance) requireExactStepID() (string, error) {
	if p.exactStep == nil {
		return "", errors.New("steering intent requires exact Step provenance")
	}
	return p.exactStep.String(), nil
}

func requireExactSteeringStepID(stepID *string) (string, error) {
	if stepID == nil {
		return "", errors.New("steering intent requires exact Step provenance")
	}
	identity, err := runtimeids.ParseStepID(*stepID)
	if err != nil {
		return "", errors.New("steering intent requires exact Step provenance")
	}
	return identity.String(), nil
}

func (e *Engine) steer(stepID string, intents ...steeringIntent) error {
	if e == nil || e.closed.Load() {
		return ErrEngineClosed
	}
	provenance, err := exactSteeringProvenance(stepID)
	if err != nil {
		return err
	}
	return e.steerOrdered(provenance, intents...)
}

func (e *Engine) steerRuntime(intents ...steeringIntent) error {
	_, err := awaitEngineRuntimeOperation(
		context.Background(),
		e,
		func(context.Context) (struct{}, error) {
			return struct{}{}, e.steerOrdered(sessionSteeringProvenance(), intents...)
		},
	)
	return err
}

func (e *Engine) steerInterruption(intents ...steeringIntent) error {
	if e == nil || e.closed.Load() {
		return ErrEngineClosed
	}
	return e.steerOrdered(sessionSteeringProvenance(), intents...)
}

func (e *Engine) steerLifecycleClose(intents ...steeringIntent) error {
	if e == nil {
		return nil
	}
	return e.steerOrdered(sessionSteeringProvenance(), intents...)
}

func (e *Engine) steerRuntimeClose(stepID string, intents ...steeringIntent) error {
	if e == nil {
		return nil
	}
	provenance, err := exactSteeringProvenance(stepID)
	if err != nil {
		return err
	}
	return e.steerOrdered(provenance, intents...)
}

func (e *Engine) steerWithCommitReceipt(stepID string, intent steeringIntent) (session.CommitReceipt, error) {
	if e == nil || e.closed.Load() {
		return session.CommitReceipt{}, ErrEngineClosed
	}
	provenance, err := exactSteeringProvenance(stepID)
	if err != nil {
		return session.CommitReceipt{}, err
	}
	return e.steerWithCommitReceiptRaw(provenance, intent)
}

func (e *Engine) steerRuntimeWithCommitReceipt(intent steeringIntent) (session.CommitReceipt, error) {
	return awaitEngineRuntimeOperation(
		context.Background(),
		e,
		func(context.Context) (session.CommitReceipt, error) {
			return e.steerWithCommitReceiptRaw(sessionSteeringProvenance(), intent)
		},
	)
}

func (e *Engine) steerDormantWithCommitReceipt(intent steeringIntent) (session.CommitReceipt, error) {
	if e == nil {
		return session.CommitReceipt{}, ErrEngineClosed
	}
	return e.steerWithCommitReceiptRaw(sessionSteeringProvenance(), intent)
}

func (e *Engine) steerWithCommitReceiptRaw(provenance steeringProvenance, intent steeringIntent) (session.CommitReceipt, error) {
	if len(intent.items) != 1 {
		return session.CommitReceipt{}, fmt.Errorf(
			"commit receipt requires exactly one steering item (items=%d)",
			len(intent.items),
		)
	}
	receipt := session.CommitReceipt{}
	intent.items[0].commitReceipt = &receipt
	err := e.steerOrdered(provenance, intent)
	return receipt, err
}

func (e *Engine) steerOrdered(provenance steeringProvenance, intents ...steeringIntent) error {
	e.outputMutationMu.Lock()
	defer e.outputMutationMu.Unlock()
	return e.steerOrderedRaw(provenance, intents...)
}

func (e *Engine) steerOrderedRaw(provenance steeringProvenance, intents ...steeringIntent) error {
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
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].priority < ordered[j].priority
	})
	for _, intent := range ordered {
		for _, item := range intent.items {
			if err := e.applySteeringItem(provenance, item); err != nil {
				return err
			}
			if item.historyReplace == nil {
				e.compactionRuntimeState().ApplyWorkflowPostCompletionActivity(
					workflowPostCompletionActivityForSteeringItem(item),
				)
			}
		}
	}
	return nil
}

func workflowPostCompletionActivityForSteeringItem(item steeringItem) workflowPostCompletionActivity {
	if item.message != nil {
		return workflowPostCompletionMessageActivity(item.message.message)
	}
	if item.assistantCommit != nil ||
		item.committedAssistant != nil ||
		item.completedResponseResolution != nil ||
		item.goalNoticeAndStatus != nil ||
		item.reviewerFeedback != nil ||
		item.reviewerError != nil ||
		item.toolCompletion != nil ||
		(item.resultGroupFlush != nil && item.resultGroupFlush.committed) ||
		(item.missingToolOutputRepair != nil && item.missingToolOutputRepair.repaired > 0) ||
		item.queuedFlush != nil {
		return workflowPostCompletionDurableActivity
	}
	return workflowPostCompletionNoActivity
}

func (e *Engine) resolveCompletedResponseStream(stepID string, instruction completedResponseResolutionInstruction) (completedResponseResolutionOutcome, error) {
	outcome := completedResponseResolutionOutcome{}
	if err := e.steer(stepID, steerCompletedResponseResolutionIntent(instruction, &outcome)); err != nil {
		return completedResponseResolutionOutcome{}, err
	}
	if outcome.kind == completedResponseResolutionInvalid {
		return completedResponseResolutionOutcome{}, errors.New("completed response stream resolution produced no outcome")
	}
	return outcome, nil
}

func (e *Engine) applySteeringItem(provenance steeringProvenance, item steeringItem) error {
	if item.compactionActivity != nil {
		stepID, err := provenance.requireExactStepID()
		if err != nil {
			return err
		}
		activity := item.compactionActivity
		if activity.active {
			e.compactionRuntimeState().SetActive(stepID, activity.requestID, activity.mode, activity.count)
		} else {
			e.compactionRuntimeState().ClearActive(stepID)
		}
		return nil
	}
	if item.missingToolOutputRepair != nil {
		repair := item.missingToolOutputRepair
		repaired, err := e.repairMissingToolOutputsByAppendingRaw(repair.repairStepID, repair.disposition)
		repair.repaired = repaired
		return err
	}
	if item.resultGroupReport != nil {
		report := item.resultGroupReport
		if report.collector == nil {
			return errors.New("result group report requires collector")
		}
		if report.operationalFailure != nil {
			if report.unit != nil || report.outcome != nil {
				return errors.New(
					"result group operational failure cannot include a completed result",
				)
			}
			fatal, err := report.collector.abortOperational(
				report.operationalFailure,
			)
			if err != nil {
				return err
			}
			return fatal
		}
		if report.unit == nil || report.outcome == nil {
			return errors.New(
				"result group completed report requires unit and outcome destination",
			)
		}
		outcome, err := report.collector.report(report.callID, *report.unit)
		if err != nil {
			if report.collector.state == resultGroupCollectorActive {
				fatal, abortErr := report.collector.abortOperational(
					fmt.Errorf(
						"report result group call %q: %w",
						report.callID,
						err,
					),
				)
				if abortErr != nil {
					return errors.Join(err, abortErr)
				}
				return fatal
			}
			return err
		}
		*report.outcome = outcome
		return nil
	}
	if item.resultGroupFlush != nil {
		stepID, err := provenance.requireExactStepID()
		if err != nil {
			return err
		}
		flush := item.resultGroupFlush
		if flush.collector == nil {
			return e.flushResultGroup(stepID, nil, flush.reason)
		}
		cursor := flush.collector.cursor
		err = e.flushResultGroup(stepID, flush.collector, flush.reason)
		flush.committed = err == nil && flush.collector.cursor > cursor
		return err
	}
	if item.resultGroupClose != nil {
		stepID, err := provenance.requireExactStepID()
		if err != nil {
			return err
		}
		return e.closeResultGroup(stepID, item.resultGroupClose.collector)
	}
	if item.assistantCommit != nil {
		stepID, err := provenance.requireExactStepID()
		if err != nil {
			return err
		}
		commit := item.assistantCommit
		if commit.result == nil {
			return errors.New("assistant commit steering item requires a result destination")
		}
		if committedAssistantMessageFinalizesStreaming(commit.message) {
			streamed, _, _ := e.transcriptRuntimeState().StreamingSnapshot()
			finalText := ""
			if commit.message.Content != nil {
				finalText = *commit.message.Content
			}
			if streamed != "" && !llm.AssistantResponseTextExtendsStream(streamed, finalText) {
				outcome, resolveErr := e.resolveCompletedResponseStreamRaw(stepID, completedResponseAbortInstruction())
				commit.result.resolution = outcome
				if resolveErr != nil {
					return resolveErr
				}
			}
		}
		receipt, err := e.appendMessageRaw(exactStepIDPointer(stepID), commit.message, steeringMessageEventNone, true, &commit.result.provenance)
		item.recordCommitReceipt(receipt)
		if err != nil || !receipt.Committed {
			return err
		}
		if len(VisibleChatEntriesFromMessage(commit.message)) == 0 {
			outcome, resolveErr := e.resolveCompletedResponseStreamRaw(stepID, completedResponseAbortInstruction())
			commit.result.resolution = outcome
			return resolveErr
		}
		coordinate, toolCallStarts := committedStartsForPersistedAssistantMessage(e, commit.message, commit.executableCallIDs)
		commit.result.coordinate = coordinate
		e.rememberPendingToolCallStarts(toolCallStarts)
		if committedAssistantMessageFinalizesStreaming(commit.message) {
			if coordinate == nil {
				return errors.New("persisted assistant text row has no committed transcript coordinate")
			}
			instruction := completedResponseFinalizeInstruction(commit.message, coordinate, commit.result.provenance)
			outcome, resolveErr := e.resolveCompletedResponseStreamRaw(stepID, instruction)
			commit.result.resolution = outcome
			return resolveErr
		}
		commit.result.resolution = completedResponseResolutionOutcome{
			kind: completedResponseResolutionAbsent,
		}
		if err := e.emitCommittedAssistantMessageEventRaw(stepID, steeringCommittedAssistantMessage{
			message:    commit.message,
			coordinate: cloneCommittedAssistantCoordinate(coordinate),
			provenance: cloneTranscriptCommittedRowProvenance(commit.result.provenance),
		}, nil, nil); err != nil {
			return err
		}
		outcome, resolveErr := e.resolveCompletedResponseStreamRaw(stepID, completedResponseAbortInstruction())
		commit.result.resolution = outcome
		return resolveErr
	}
	if item.message != nil {
		receipt, err := e.appendMessageRaw(
			provenance.stepID(),
			item.message.message,
			item.message.eventPolicy,
			item.message.persist,
			item.message.provenanceDestination,
		)
		item.recordCommitReceipt(receipt)
		if err == nil && receipt.Committed && item.message.emitUserFlushEvent {
			if flushed := flushedUserMessageEvent(
				*item.message.provenanceDestination,
				item.message.message,
				provenance.stepID(),
			); flushed != nil {
				err = errors.Join(err, e.emitRaw(*flushed))
			}
		}
		return err
	}
	if item.goalNoticeAndStatus != nil {
		notice := item.goalNoticeAndStatus
		receipt, noticeErr := e.appendMessageRaw(
			provenance.stepID(),
			notice.message,
			steeringMessageEventDefault,
			true,
			nil,
		)
		item.recordCommitReceipt(receipt)
		if !receipt.Committed {
			return noticeErr
		}
		statusErr := e.emitRaw(Event{
			Kind:       EventGoalStatusUpdated,
			StepID:     provenance.stepID(),
			GoalStatus: &notice.update,
		})
		return errors.Join(noticeErr, statusErr)
	}
	if item.committedAssistant != nil {
		stepID, err := provenance.requireExactStepID()
		if err != nil {
			return err
		}
		return e.emitCommittedAssistantMessageRaw(stepID, *item.committedAssistant)
	}
	if item.completedResponseResolution != nil {
		stepID, err := provenance.requireExactStepID()
		if err != nil {
			return err
		}
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
		receipt, _, err := e.appendPersistedLocalEntryRecordRaw(
			provenance.stepID(),
			item.localEntry.entry,
			item.localEntry.reasoningTraceIdentity,
		)
		item.recordCommitReceipt(receipt)
		return err
	}
	if item.reviewerFeedback != nil {
		stepID, exactErr := provenance.requireExactStepID()
		if exactErr != nil {
			return exactErr
		}
		id := runtimeids.NewReviewerFeedbackID()
		visibility, visibilityErr := sessionEntryVisibilityFromRuntime(item.reviewerFeedback.visibility)
		if visibilityErr != nil {
			return visibilityErr
		}
		record := session.ReviewerFeedbackRecord{ID: id, Suggestions: append([]string(nil), item.reviewerFeedback.suggestions...), Visibility: visibility}
		appended, receipt, err := e.eventLog.AppendRecord(provenance.stepID(), record)
		item.recordCommitReceipt(receipt)
		if receipt.Committed {
			provenance, provenanceErr := transcriptProvenanceFromRecord(appended)
			err = errors.Join(err, provenanceErr)
			if provenanceErr != nil {
				return err
			}
			entry := reviewerFeedbackChatEntryFromSessionRecord(record, stepID, &provenance)
			e.transcriptRuntimeState().chatProjection().appendLocalEntryRecord(entry, nil)
			err = errors.Join(err, e.emitRaw(Event{Kind: EventLocalEntryAdded, StepID: exactStepIDPointer(stepID), LocalEntry: &entry, LocalEntryProjected: true, CommittedTranscriptChanged: true, CommittedProvenance: &provenance}))
		}
		return err
	}
	if item.reviewerError != nil {
		stepID, exactErr := provenance.requireExactStepID()
		if exactErr != nil {
			return exactErr
		}
		id := runtimeids.NewReviewerErrorID()
		record := session.ReviewerErrorRecord{ID: id, Detail: item.reviewerError.detail}
		appended, receipt, err := e.eventLog.AppendRecord(provenance.stepID(), record)
		item.recordCommitReceipt(receipt)
		if receipt.Committed {
			provenance, provenanceErr := transcriptProvenanceFromRecord(appended)
			err = errors.Join(err, provenanceErr)
			if provenanceErr != nil {
				return err
			}
			entry := reviewerErrorChatEntryFromSessionRecord(record, stepID, &provenance)
			e.transcriptRuntimeState().chatProjection().appendLocalEntryRecord(entry, nil)
			err = errors.Join(err, e.emitRaw(Event{Kind: EventLocalEntryAdded, StepID: exactStepIDPointer(stepID), LocalEntry: &entry, LocalEntryProjected: true, CommittedTranscriptChanged: true, CommittedProvenance: &provenance}))
		}
		return err
	}
	if item.historyReplace != nil {
		stepID, err := provenance.requireExactStepID()
		if err != nil {
			return err
		}
		receipt, err := e.replaceHistoryRaw(stepID, *item.historyReplace)
		item.recordCommitReceipt(receipt)
		return err
	}
	if item.toolCompletion != nil {
		stepID, exactErr := provenance.requireExactStepID()
		if exactErr != nil {
			return exactErr
		}
		completion := e.finalizeLiveToolCompletion(*item.toolCompletion)
		stepIDPointer := exactStepIDPointer(stepID)
		receipt, provenance, feedbackProvenance, err := e.persistFinalizedToolCompletionRaw(stepIDPointer, completion)
		item.recordCommitReceipt(receipt)
		if receipt.Committed {
			err = errors.Join(err, e.publishCommittedFinalizedToolCompletion(
				stepIDPointer,
				stepIDPointer,
				completion,
				provenance,
				feedbackProvenance,
			))
		}
		return err
	}
	if item.queuedFlush != nil {
		if _, err := provenance.requireExactStepID(); err != nil {
			return err
		}
		receipt, err := e.appendQueuedUserMessageFlush(provenance.stepID(), item.queuedFlush.message, item.queuedFlush.batch, item.queuedFlush.queueItems)
		item.recordCommitReceipt(receipt)
		return err
	}
	if item.queuedRestore != nil {
		e.messageFlow.RestorePendingUserInjections(item.queuedRestore.items)
		for _, pending := range item.queuedRestore.items {
			e.emitQueuedUserMessageStatus(pending.message, QueuedUserMessageAccepted, "", false)
		}
		e.publishPendingWorkSnapshot()
		return nil
	}
	if item.event != nil {
		evt := *item.event
		if evt.StepID == nil {
			evt.StepID = provenance.stepID()
		}
		if evt.Kind == EventToolCallStarted && evt.ToolCall != nil {
			eventStepID, err := requireExactSteeringStepID(evt.StepID)
			if err != nil {
				return err
			}
			if err := e.transcriptRuntimeState().RecordLiveToolStart(eventStepID, *evt.ToolCall); err != nil {
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
		appended, receipt, appendErr := e.eventLog.AppendRecord(provenance.stepID(), record)
		item.recordCommitReceipt(receipt)
		if appendErr != nil && !receipt.Committed {
			return appendErr
		}
		stepIDPointer := provenance.stepID()
		recordProvenance, provenanceErr := transcriptProvenanceFromRecord(appended)
		if provenanceErr != nil {
			return errors.Join(appendErr, provenanceErr)
		}
		e.transcriptRuntimeState().AppendCommittedEntryWithVisibility(cacheWarningTranscriptRole, transcript.CacheWarningText(warning), visibility, &recordProvenance)
		if item.cacheWarning.emit {
			appendErr = errors.Join(appendErr, e.emitRaw(Event{Kind: EventCacheWarning, StepID: stepIDPointer, CacheWarning: copyCacheWarning(&warning), CacheWarningVisibility: visibility, CommittedTranscriptChanged: true, CommittedProvenance: &recordProvenance}))
		}
		return appendErr
	}
	if item.cacheObservation != nil {
		observation := item.cacheObservation
		records, receipt, appendErr := e.eventLog.AppendRecordsAtomic(provenance.stepID(), observation.records)
		item.recordCommitReceipt(receipt)
		if !receipt.Committed {
			return appendErr
		}
		e.modelRequests().RequestCache().RecordResponse(observation.response)
		if observation.hasWarning {
			warning := observation.warning
			visibility := normalizeRuntimeEntryVisibility(observation.visibility)
			var warningProvenance *TranscriptCommittedRowProvenance
			for _, record := range records {
				payload, payloadErr := record.Payload()
				if payloadErr != nil {
					return errors.Join(appendErr, payloadErr)
				}
				if _, ok := payload.(session.CacheWarningRecord); ok {
					provenance, provenanceErr := transcriptProvenanceFromRecord(record)
					if provenanceErr != nil {
						return errors.Join(appendErr, provenanceErr)
					}
					warningProvenance = &provenance
					break
				}
			}
			if warningProvenance == nil {
				return errors.Join(appendErr, errors.New("cache warning append did not return its warning record"))
			}
			e.transcriptRuntimeState().AppendCommittedEntryWithVisibility(cacheWarningTranscriptRole, transcript.CacheWarningText(warning), visibility, warningProvenance)
			if observation.emit {
				appendErr = errors.Join(appendErr, e.emitRaw(Event{Kind: EventCacheWarning, StepID: provenance.stepID(), CacheWarning: copyCacheWarning(&warning), CacheWarningVisibility: visibility, CommittedTranscriptChanged: true, CommittedProvenance: warningProvenance}))
			}
		}
		return appendErr
	}
	if item.liveToolAbort != nil {
		return e.emitLiveToolAbortsRaw(item.liveToolAbort.reason)
	}
	if item.streaming != nil {
		stepID, err := provenance.requireExactStepID()
		if err != nil {
			return err
		}
		if item.streaming.reasoningReset != nil {
			e.transcriptRuntimeState().ResetReasoningTraces(stepID)
			return e.emitRaw(Event{Kind: EventReasoningDeltaReset, StepID: exactStepIDPointer(stepID)})
		}
		if item.streaming.clearReasoning {
			e.transcriptRuntimeState().ClearReasoningState(stepID)
			return nil
		}
		if item.streaming.assistantDelta != nil {
			delta := *item.streaming.assistantDelta
			if delta.Text == "" {
				return nil
			}
			revision, err := e.TranscriptRevision()
			if err != nil {
				return err
			}
			appended := e.transcriptRuntimeState().AppendStreamingDelta(stepID, revision, e.CommittedTranscriptEntryCount(), delta.Text, delta.Phase)
			if appended.supersededStreamID != nil {
				reason := AssistantStreamAbortSuperseded
				if err := e.emitStreamingAssistantCleanupEventsRaw(
					stepID,
					appended.supersededMetadata,
					appended.supersededStreamID,
					&reason,
				); err != nil {
					return err
				}
			}
			return e.emitRaw(Event{
				Kind:                        EventAssistantDelta,
				StepID:                      exactStepIDPointer(stepID),
				AssistantDelta:              delta.Text,
				AssistantDeltaPhase:         delta.Phase,
				AssistantStreamMetadata:     appended.metadata,
				AssistantTranscriptStreamID: appended.transcriptStreamID,
			})
		}
		if item.streaming.reasoningDelta != nil {
			delta := *item.streaming.reasoningDelta
			identity, err := e.transcriptRuntimeState().SetReasoningState(stepID, delta)
			if err != nil {
				return err
			}
			return e.emitRaw(Event{
				Kind:                   EventReasoningDelta,
				StepID:                 exactStepIDPointer(stepID),
				ReasoningDelta:         &delta,
				ReasoningTraceIdentity: cloneTranscriptReasoningTraceIdentity(identity),
			})
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
	appended, receipt, appendErr := e.eventLog.AppendCompactionHistoryReplacement(
		textutil.OptionalExactString(stepID),
		record,
	)
	if appendErr != nil && !receipt.Committed {
		return receipt, appendErr
	}
	e.resetPromptCacheObservationBaselines()
	provenance, provenanceErr := transcriptProvenanceFromRecord(appended)
	if provenanceErr != nil {
		return receipt, errors.Join(appendErr, provenanceErr)
	}
	for index := range replacement.projectedEntries {
		replacement.projectedEntries[index].StepID = exactStepIDPointer(stepID)
	}
	replacement.projectedEntries = assignHistoryReplacementEntryProvenance(
		replacement.projectedEntries,
		&provenance,
	)
	e.lockedContractState().MarkPromptFacingSnapshotsStale()
	// Compaction reinjects canonical generation context, including base meta,
	// into the same replacement payload. Mirror the restore-time length signal
	// here rather than scanning individual items.
	e.baseMetaInjected = len(preparedItems) > 0
	if replacement.payload.CompactionNumber != nil {
		e.compactionRuntimeState().SetCount(*replacement.payload.CompactionNumber)
	}
	e.resetLocalDiagnostics()
	e.transcriptRuntimeState().ReplaceHistoryAtCommittedEntryStart(
		exactStepIDPointer(stepID),
		preparedItems,
		&projectedStart,
		replacement.projectedEntries,
	)
	replacementMode := session.CompactionMode(replacement.payload.Mode)
	modeErr := e.compactionRuntimeState().SetHistoryReplacementMode(&replacementMode)
	e.compactionRuntimeState().SetSoonReminderIssued(false)
	emitErr := e.emitProjectedHistoryReplacementEntriesRaw(
		stepID,
		projectedStart,
		replacement.projectedEntries,
	)
	emitErr = errors.Join(
		modeErr,
		emitErr,
		e.emitRaw(Event{Kind: EventConversationUpdated, StepID: exactStepIDPointer(stepID)}),
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
		provenance := cloneTranscriptCommittedRowProvenance(entry.CommittedProvenance)
		if _, projected := transcriptCommittedRowFactFromChatEntry(copyEntry); projected && (provenance == nil || provenance.ProjectedOrdinal == nil) {
			return fmt.Errorf(
				"history replacement projected row %d lacks filtered ordinal (step_id=%v role=%q)",
				idx,
				copyEntry.StepID,
				copyEntry.Role,
			)
		}
		if err := e.emitRaw(Event{
			Kind:                       EventLocalEntryAdded,
			StepID:                     exactStepIDPointer(stepID),
			LocalEntry:                 &copyEntry,
			LocalEntryProjected:        true,
			CommittedTranscriptChanged: true,
			CommittedEntryStart:        start + idx,
			CommittedEntryStartSet:     true,
			CommittedEntryCount:        start + idx + 1,
			CommittedProvenance:        provenance,
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

func (e *Engine) emitResultGroupProjectionEvent(event Event) error {
	return e.emitRaw(event)
}
