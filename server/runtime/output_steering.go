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
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"

	"github.com/google/uuid"
)

type steeringIntent struct {
	items []steeringMutation
}

type steeringMutation interface {
	runtimeMutation()
}

func validateSteeringMutation(mutation steeringMutation) error {
	if mutation == nil {
		return errors.New("Runtime mutation is required")
	}
	switch mutation := mutation.(type) {
	case *steeringMessage:
		if mutation == nil {
			return errors.New("message Runtime mutation is required")
		}
	case *steeringAssistantCommit:
		if mutation == nil {
			return errors.New("assistant commit Runtime mutation is required")
		}
	case *steeringGoalNoticeAndStatus:
		if mutation == nil {
			return errors.New("Goal Runtime mutation is required")
		}
	case *steeringGoalMutation:
		if mutation == nil {
			return errors.New("Goal command Runtime mutation is required")
		}
	case *steeringThinkingLevel:
		if mutation == nil {
			return errors.New("thinking-level Runtime mutation is required")
		}
	case *steeringFastMode:
		if mutation == nil {
			return errors.New("Fast Mode Runtime mutation is required")
		}
	case *steeringReviewerMode:
		if mutation == nil {
			return errors.New("Reviewer Runtime mutation is required")
		}
	case *steeringAutoCompaction:
		if mutation == nil {
			return errors.New("auto-compaction Runtime mutation is required")
		}
	case *steeringQuestions:
		if mutation == nil {
			return errors.New("Questions Runtime mutation is required")
		}
	case *steeringCommittedAssistantMessage:
		if mutation == nil {
			return errors.New("committed assistant Runtime mutation is required")
		}
	case *steeringCompletedResponseResolution:
		if mutation == nil {
			return errors.New("completed response Runtime mutation is required")
		}
	case *steeringLocalEntry:
		if mutation == nil {
			return errors.New("local entry Runtime mutation is required")
		}
	case *steeringReviewerFeedback:
		if mutation == nil {
			return errors.New("Reviewer feedback Runtime mutation is required")
		}
	case *steeringReviewerError:
		if mutation == nil {
			return errors.New("Reviewer error Runtime mutation is required")
		}
	case *steeringHistoryReplacement:
		if mutation == nil {
			return errors.New("history replacement Runtime mutation is required")
		}
	case *steeringToolCompletion:
		if mutation == nil {
			return errors.New("tool completion Runtime mutation is required")
		}
	case *steeringResultGroupReport:
		if mutation == nil {
			return errors.New("Result Group report Runtime mutation is required")
		}
	case *steeringResultGroupFlush:
		if mutation == nil {
			return errors.New("Result Group flush Runtime mutation is required")
		}
	case *steeringResultGroupClose:
		if mutation == nil {
			return errors.New("Result Group close Runtime mutation is required")
		}
	case *steeringMissingToolOutputRepair:
		if mutation == nil {
			return errors.New("missing tool output Runtime mutation is required")
		}
	case *steeringQueuedUserMessageFlush:
		if mutation == nil {
			return errors.New("queued user flush Runtime mutation is required")
		}
	case *steeringQueuedUserMessageRestore:
		if mutation == nil {
			return errors.New("queued user restore Runtime mutation is required")
		}
	case *steeringEvent:
		if mutation == nil {
			return errors.New("event Runtime mutation is required")
		}
	case *steeringStreamingOutput:
		if mutation == nil {
			return errors.New("streaming Runtime mutation is required")
		}
	case *steeringCacheWarning:
		if mutation == nil {
			return errors.New("cache warning Runtime mutation is required")
		}
	case *steeringCacheObservation:
		if mutation == nil {
			return errors.New("cache observation Runtime mutation is required")
		}
	case *steeringLiveToolAbort:
		if mutation == nil {
			return errors.New("live tool abort Runtime mutation is required")
		}
	default:
		return fmt.Errorf("unsupported Runtime mutation %T", mutation)
	}
	return nil
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

type steeringGoalMutation struct {
	mutation GoalMutation
	result   *GoalCommandResult
}

type steeringThinkingLevel struct {
	level string
}

type steeringFastMode struct {
	enabled  bool
	feedback func(bool) string
	changed  *bool
}

type steeringReviewerMode struct {
	enabled  bool
	feedback func(bool, string, bool) string
	changed  *bool
	mode     *string
}

type steeringAutoCompaction struct {
	enabled       bool
	changed       *bool
	resultEnabled *bool
}

type steeringQuestions struct {
	enabled       bool
	feedback      func(bool, bool) string
	changed       *bool
	resultEnabled *bool
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

type steeringToolCompletion struct {
	result tools.Result
}

type steeringEvent struct {
	event Event
}

func (*steeringMessage) runtimeMutation()                     {}
func (*steeringAssistantCommit) runtimeMutation()             {}
func (*steeringGoalNoticeAndStatus) runtimeMutation()         {}
func (*steeringGoalMutation) runtimeMutation()                {}
func (*steeringThinkingLevel) runtimeMutation()               {}
func (*steeringFastMode) runtimeMutation()                    {}
func (*steeringReviewerMode) runtimeMutation()                {}
func (*steeringAutoCompaction) runtimeMutation()              {}
func (*steeringQuestions) runtimeMutation()                   {}
func (*steeringCommittedAssistantMessage) runtimeMutation()   {}
func (*steeringCompletedResponseResolution) runtimeMutation() {}
func (*steeringLocalEntry) runtimeMutation()                  {}
func (*steeringReviewerFeedback) runtimeMutation()            {}
func (*steeringReviewerError) runtimeMutation()               {}
func (*steeringHistoryReplacement) runtimeMutation()          {}
func (*steeringToolCompletion) runtimeMutation()              {}
func (*steeringResultGroupReport) runtimeMutation()           {}
func (*steeringResultGroupFlush) runtimeMutation()            {}
func (*steeringResultGroupClose) runtimeMutation()            {}
func (*steeringMissingToolOutputRepair) runtimeMutation()     {}
func (*steeringQueuedUserMessageFlush) runtimeMutation()      {}
func (*steeringQueuedUserMessageRestore) runtimeMutation()    {}
func (*steeringEvent) runtimeMutation()                       {}
func (*steeringStreamingOutput) runtimeMutation()             {}
func (*steeringCacheWarning) runtimeMutation()                {}
func (*steeringCacheObservation) runtimeMutation()            {}
func (*steeringLiveToolAbort) runtimeMutation()               {}

type steeringMessageEventPolicy uint8

const (
	steeringMessageEventDefault steeringMessageEventPolicy = iota
	steeringMessageEventNone
)

func steerMessagesWithPersistenceIntent(eventPolicy steeringMessageEventPolicy, persist bool, messages []llm.Message) steeringIntent {
	items := make([]steeringMutation, 0, len(messages))
	for _, message := range messages {
		msg := message
		items = append(items, &steeringMessage{
			message:     msg,
			eventPolicy: eventPolicy,
			persist:     persist,
		})
	}
	return steeringIntent{items: items}
}

func steerAssistantCommitIntent(
	msg llm.Message,
	executableCallIDs map[string]struct{},
	result *steeringAssistantCommitResult,
) steeringIntent {
	return steeringIntent{
		items: []steeringMutation{
			&steeringAssistantCommit{
				message:           msg,
				executableCallIDs: executableCallIDs,
				result:            result,
			},
		},
	}
}

func steerUserMessageWithFlushIntent(msg llm.Message) steeringIntent {
	var provenance *TranscriptCommittedRowProvenance
	return steeringIntent{items: []steeringMutation{&steeringMessage{
		message:               msg,
		eventPolicy:           steeringMessageEventNone,
		persist:               true,
		provenanceDestination: &provenance,
		emitUserFlushEvent:    true,
	}}}
}

func steerLocalEntryIntent(entry storedLocalEntry) steeringIntent {
	copyEntry := entry
	return steeringIntent{
		items: []steeringMutation{&steeringLocalEntry{
			entry: copyEntry,
		}},
	}
}

func steerReasoningLocalEntryIntent(entry storedLocalEntry, identity TranscriptReasoningTraceIdentity) steeringIntent {
	copyEntry := entry
	return steeringIntent{
		items: []steeringMutation{&steeringLocalEntry{
			entry:                  copyEntry,
			reasoningTraceIdentity: &identity,
		}},
	}
}

func steerReviewerFeedbackIntent(suggestions []string, visibility transcript.EntryVisibility) steeringIntent {
	return steeringIntent{items: []steeringMutation{&steeringReviewerFeedback{
		suggestions: append([]string(nil), suggestions...), visibility: visibility,
	}}}
}

func steerReviewerErrorIntent(detail string) steeringIntent {
	return steeringIntent{items: []steeringMutation{&steeringReviewerError{detail: detail}}}
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
		items: []steeringMutation{&steeringHistoryReplacement{
			payload:          payload,
			projectedEntries: transcriptEntriesFromHistoryReplacement(payload.Items, payload.CompactionNumber),
		}},
	}
}

func steerToolCompletionIntent(result tools.Result) steeringIntent {
	copyResult := cloneToolResult(result)
	return steeringIntent{
		items: []steeringMutation{&steeringToolCompletion{result: copyResult}},
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
		items: []steeringMutation{&steeringResultGroupReport{
			collector: collector,
			callID:    callID,
			unit:      &copyUnit,
			outcome:   outcome,
		}},
	}
}

func steerResultGroupFlushIntent(
	collector *resultGroupCollector,
	reason ResultGroupFlushReason,
) steeringIntent {
	return steeringIntent{
		items: []steeringMutation{&steeringResultGroupFlush{
			collector: collector,
			reason:    reason,
		}},
	}
}

func steerQueuedUserMessageFlushIntent(message llm.Message, batch []string, queueItems []QueuedUserMessage) steeringIntent {
	return steeringIntent{
		items: []steeringMutation{&steeringQueuedUserMessageFlush{
			message:    message,
			batch:      append([]string(nil), batch...),
			queueItems: append([]QueuedUserMessage(nil), queueItems...),
		}},
	}
}

func steerQueuedUserMessageRestoreIntent(items []queuedUserMessage) steeringIntent {
	return steeringIntent{
		items: []steeringMutation{&steeringQueuedUserMessageRestore{
			items: append([]queuedUserMessage(nil), items...),
		}},
	}
}

func steerEventIntent(evt Event) steeringIntent {
	copyEvent := evt
	return steeringIntent{
		items: []steeringMutation{&steeringEvent{event: copyEvent}},
	}
}

func steerGoalNoticeAndStatusIntent(message llm.Message, update GoalStatusUpdate) steeringIntent {
	return steeringIntent{
		items: []steeringMutation{&steeringGoalNoticeAndStatus{
			message: message,
			update:  update,
		}},
	}
}

func steerThinkingLevelIntent(level string) steeringIntent {
	return steeringIntent{items: []steeringMutation{&steeringThinkingLevel{level: level}}}
}

func steerFastModeIntent(enabled bool, feedback func(bool) string, changed *bool) steeringIntent {
	return steeringIntent{items: []steeringMutation{&steeringFastMode{
		enabled: enabled, feedback: feedback, changed: changed,
	}}}
}

func steerReviewerModeIntent(
	enabled bool,
	feedback func(bool, string, bool) string,
	changed *bool,
	mode *string,
) steeringIntent {
	return steeringIntent{items: []steeringMutation{&steeringReviewerMode{
		enabled: enabled, feedback: feedback, changed: changed, mode: mode,
	}}}
}

func steerAutoCompactionIntent(enabled bool, changed *bool, resultEnabled *bool) steeringIntent {
	return steeringIntent{items: []steeringMutation{&steeringAutoCompaction{
		enabled: enabled, changed: changed, resultEnabled: resultEnabled,
	}}}
}

func steerQuestionsIntent(
	enabled bool,
	feedback func(bool, bool) string,
	changed *bool,
	resultEnabled *bool,
) steeringIntent {
	return steeringIntent{items: []steeringMutation{&steeringQuestions{
		enabled: enabled, feedback: feedback, changed: changed, resultEnabled: resultEnabled,
	}}}
}

func steerCommittedAssistantMessageIntent(msg llm.Message, coordinate *committedAssistantCoordinate, provenances ...*TranscriptCommittedRowProvenance) steeringIntent {
	return steeringIntent{
		items: []steeringMutation{&steeringCommittedAssistantMessage{
			message:    msg,
			coordinate: cloneCommittedAssistantCoordinate(coordinate),
			provenance: firstTranscriptCommittedRowProvenance(provenances),
		}},
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

func steerAssistantDeltaIntent(delta llm.AssistantDelta) steeringIntent {
	copyDelta := delta
	return steeringIntent{
		items: []steeringMutation{&steeringStreamingOutput{assistantDelta: &copyDelta}},
	}
}

func steerReasoningDeltaIntent(delta llm.ReasoningSummaryDelta) steeringIntent {
	copyDelta := delta
	return steeringIntent{
		items: []steeringMutation{&steeringStreamingOutput{reasoningDelta: &copyDelta}},
	}
}

func steerClearStreamingStateIntent() steeringIntent {
	reason := AssistantStreamAbortSuperseded
	return steeringIntent{
		items: []steeringMutation{&steeringStreamingOutput{clearState: true, resetEvents: true, abortReason: &reason}},
	}
}

func steerClearReasoningStateIntent() steeringIntent {
	return steeringIntent{
		items: []steeringMutation{&steeringStreamingOutput{clearReasoning: true}},
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
		items: []steeringMutation{&steeringStreamingOutput{reasoningReset: &steeringReasoningReset{}}},
	}
}

func steerCacheObservationIntent(records []session.EventRecordPayload, response persistedCacheResponseObserved, warning *transcript.CacheWarning, visibility transcript.EntryVisibility, emit bool) steeringIntent {
	copyRecords := append([]session.EventRecordPayload(nil), records...)
	var copyWarning transcript.CacheWarning
	if warning != nil {
		copyWarning = *warning
	}
	return steeringIntent{
		items: []steeringMutation{&steeringCacheObservation{
			records:    copyRecords,
			response:   response,
			warning:    copyWarning,
			hasWarning: warning != nil,
			visibility: normalizeRuntimeEntryVisibility(visibility),
			emit:       emit,
		}},
	}
}

func (e *Engine) steer(stepID string, intents ...steeringIntent) error {
	stepID = strings.TrimSpace(stepID)
	if stepID == "" {
		return errors.New("exact Runtime output requires a Step ID")
	}
	if e.closed.Load() {
		return ErrEngineClosed
	}
	_, err := e.enqueueExactOutputSteering(stepID, false, intents...)
	return err
}

func (e *Engine) steerWithCommitReceipt(stepID string, intent steeringIntent) (session.CommitReceipt, error) {
	stepID = strings.TrimSpace(stepID)
	if stepID == "" {
		return session.CommitReceipt{}, errors.New("exact Runtime output requires a Step ID")
	}
	if e.closed.Load() {
		return session.CommitReceipt{}, ErrEngineClosed
	}
	if len(intent.items) != 1 {
		return session.CommitReceipt{}, fmt.Errorf(
			"commit receipt requires exactly one steering item (items=%d)",
			len(intent.items),
		)
	}
	return e.enqueueExactOutputSteering(stepID, true, intent)
}

func (e *Engine) steerRuntime(intents ...steeringIntent) error {
	if e.closed.Load() {
		return ErrEngineClosed
	}
	_, err := e.enqueueRuntimeSteering(false, intents...)
	return err
}

func (e *Engine) steerRuntimeWithCommitReceipt(intent steeringIntent) (session.CommitReceipt, error) {
	if e.closed.Load() {
		return session.CommitReceipt{}, ErrEngineClosed
	}
	if len(intent.items) != 1 {
		return session.CommitReceipt{}, fmt.Errorf(
			"commit receipt requires exactly one steering item (items=%d)",
			len(intent.items),
		)
	}
	return e.enqueueRuntimeSteering(true, intent)
}

func (e *Engine) enqueueExactOutputSteering(
	stepID string,
	commitReceipt bool,
	intents ...steeringIntent,
) (session.CommitReceipt, error) {
	if e == nil || e.steering == nil {
		return session.CommitReceipt{}, errors.New("Runtime Steering is unavailable")
	}
	entry := newExactOutputSteeringQueueEntry(stepID, commitReceipt, intents...)
	if err := e.stepLifecycle.ValidateExactOutput(stepID, exactOutputAllowsClosing(intents)); err != nil {
		if completeErr := entry.completeOutput(steeringOutputReply{err: err}); completeErr != nil {
			return session.CommitReceipt{}, e.runtimeInvariant("complete rejected exact Runtime output", completeErr)
		}
		return entry.waitOutput(context.Background())
	}
	return e.applyExactOutputSteeringEntry(entry)
}

func (e *Engine) enqueueRuntimeSteering(
	commitReceipt bool,
	intents ...steeringIntent,
) (session.CommitReceipt, error) {
	if e == nil || e.steering == nil {
		return session.CommitReceipt{}, errors.New("Runtime Steering is unavailable")
	}
	entry := newRuntimeOutputSteeringQueueEntry(commitReceipt, intents...)
	wake, err := e.steering.append(entry)
	if err != nil {
		return session.CommitReceipt{}, err
	}
	if wake {
		e.wakeSteeringDrain()
	}
	return entry.waitOutput(context.Background())
}

func (e *Engine) steerCurrentStepOrRuntime(intent steeringIntent) error {
	if e.closed.Load() {
		return ErrEngineClosed
	}
	stepID, err := e.stepLifecycle.ResolveActiveOutputStep(nil)
	if err == nil && stepID != nil {
		err = e.steer(*stepID, intent)
		if err == nil || !errors.Is(err, ErrActiveStepInactive) {
			return err
		}
	} else if err != nil && !errors.Is(err, ErrActiveStepInactive) {
		return err
	}
	return e.steerRuntime(intent)
}

func (e *Engine) steerActiveStep(expectedStepID string, intent steeringIntent) error {
	expectedStepID = strings.TrimSpace(expectedStepID)
	if expectedStepID == "" {
		return errors.New("exact Runtime output requires a Step ID")
	}
	if e.closed.Load() {
		return ErrEngineClosed
	}
	stepID, err := e.stepLifecycle.ResolveActiveOutputStep(&expectedStepID)
	if err != nil {
		return err
	}
	if stepID == nil {
		return ErrActiveStepInactive
	}
	return e.steer(*stepID, intent)
}

func (e *Engine) applyExactOutputSteeringEntry(entry *steeringQueueEntry) (session.CommitReceipt, error) {
	reply := e.applySteeringQueueEntry(entry)
	if err := entry.completeOutput(reply); err != nil {
		return session.CommitReceipt{}, e.runtimeInvariant("complete exact Runtime output", err)
	}
	return entry.waitOutput(context.Background())
}

func exactOutputAllowsClosing(intents []steeringIntent) bool {
	for _, intent := range intents {
		for _, mutation := range intent.items {
			switch mutation.(type) {
			case *steeringResultGroupReport,
				*steeringResultGroupFlush,
				*steeringResultGroupClose,
				*steeringToolCompletion,
				*steeringCompletedResponseResolution,
				*steeringCommittedAssistantMessage,
				*steeringLiveToolAbort,
				*steeringStreamingOutput:
			default:
				return false
			}
		}
	}
	return len(intents) != 0
}

func (e *Engine) wakeSteeringDrain() {
	if e == nil || e.steering == nil {
		return
	}
	if lifecycle, ok := e.stepLifecycle.(*defaultExclusiveStepLifecycle); ok &&
		lifecycle.Snapshot() != nil {
		return
	}
	if !e.launchLifecycleTask(func(context.Context) *resultGroupFatal {
		e.drainSteeringIncludingDeferredHuman(true)
		return nil
	}) {
		e.steering.close()
	}
}

func (e *Engine) wakeSteeringBoundaryDrain() {
	if e == nil || e.steering == nil {
		return
	}
	if !e.launchLifecycleTask(func(ctx context.Context) *resultGroupFatal {
		e.drainSteeringIncludingDeferredHumanWithContext(ctx, true)
		return nil
	}) {
		e.steering.close()
	}
}

func (e *Engine) drainSteering() {
	e.drainSteeringIncludingDeferredHuman(e.stepLifecycle.Snapshot() == nil)
}

func (e *Engine) drainSteeringIncludingDeferredHuman(includeDeferredHuman bool) {
	e.ensureLifecycle()
	e.drainSteeringIncludingDeferredHumanWithContext(e.lifecycleCtx, includeDeferredHuman)
}

func (e *Engine) drainSteeringIncludingDeferredHumanWithContext(
	ctx context.Context,
	includeDeferredHuman bool,
) {
	if e == nil || e.steering == nil {
		return
	}
	for {
		entry, ok := e.steering.beginNext(includeDeferredHuman)
		if !ok {
			if !includeDeferredHuman && e.steering.pauseForDeferredHuman() {
				return
			}
			if e.steering.finishDrain(e.releaseEmptyDrainDecision) {
				return
			}
			continue
		}
		fatal := false
		switch {
		case entry.output != nil:
			reply := e.applySteeringQueueEntry(entry)
			if err := entry.completeOutput(reply); err != nil {
				e.surfaceRunError(e.runtimeInvariant("complete Runtime Steering output", err))
				e.closeAdmissionAfterRuntimeAbort()
				return
			}
			fatal = steeringFailureRequiresRuntimeClose(reply)
		case entry.start != nil:
			reply := e.applyWorkflowAssignmentQueueEntry(entry)
			entry.start.reply <- reply
			close(entry.start.reply)
			fatal = steeringFailureRequiresRuntimeClose(reply)
		case entry.shell != nil:
			result, err := e.applyUserShellQueueEntry(entry.shell)
			if completeErr := entry.completeShell(userShellReply{result: result, err: err}); completeErr != nil {
				e.surfaceRunError(e.runtimeInvariant("complete user-shell Steering output", completeErr))
				e.closeAdmissionAfterRuntimeAbort()
				return
			}
			fatal = errors.Is(err, session.ErrMutationDefinitelyUncommitted)
		case entry.worktree != nil:
			err := e.applyWorktreeTransitionQueueEntry(entry.worktree)
			fatal = errors.Is(err, session.ErrMutationDefinitelyUncommitted)
		case entry.compaction != nil:
			lifecycle, ok := e.stepLifecycle.(*defaultExclusiveStepLifecycle)
			if !ok {
				err := errors.New("manual compaction requires the default Agent Step lifecycle")
				entry.compaction.reply <- compactionReply{err: err}
				close(entry.compaction.reply)
				e.surfaceRunError(err)
				break
			}
			receipt, err := e.executePendingCompaction(ctx, lifecycle, entry.compaction)
			entry.compaction.reply <- compactionReply{receipt: receipt, err: err}
			close(entry.compaction.reply)
			if err != nil &&
				!errors.Is(err, ErrManualCompactionTooSoon) &&
				!errors.Is(err, errCompactionDisabledModeNone) {
				e.queueRuntimeErrorFeedback(err)
			}
		default:
			e.surfaceRunError(e.runtimeInvariant("apply Runtime Steering current head", errors.New("Steering queue head has no concrete operation")))
			e.closeAdmissionAfterRuntimeAbort()
			return
		}
		if err := e.steering.finishCurrent(entry); err != nil {
			e.surfaceRunError(e.runtimeInvariant("finish Runtime Steering current head", err))
			e.closeAdmissionAfterRuntimeAbort()
			return
		}
		if fatal {
			e.closeAdmissionAfterSteeringFailure(entry)
			return
		}
	}
}

func (e *Engine) SubmitWorktreeTransition(
	operation func(context.Context, func(clientui.SessionExecutionTarget, *session.WorktreeReminderState) error) error,
) error {
	if e == nil || e.steering == nil {
		return ErrSteeringUnavailable
	}
	entry := newWorktreeTransitionQueueEntry(operation)
	wake, err := e.steering.append(entry)
	if err != nil {
		return err
	}
	if wake {
		e.wakeSteeringDrain()
	}
	return nil
}

func (e *Engine) applyWorktreeTransitionQueueEntry(work *steeringWorktreeTransition) error {
	if work == nil || work.operation == nil {
		return errors.New("Worktree Steering operation is required")
	}
	e.ensureLifecycle()
	err := work.operation(e.lifecycleCtx, func(target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) error {
		if e.cfg.ApplyWorktreeTarget == nil {
			return errors.New("Runtime Worktree target applier is unavailable")
		}
		if err := e.cfg.ApplyWorktreeTarget(target, reminder); err != nil {
			return err
		}
		e.SetTranscriptWorkingDir(target.EffectiveWorkdir)
		return e.SetWorktreeReminderState(reminder)
	})
	if err == nil {
		return nil
	}
	persistErr := e.applyRuntimeMutations(runtimeSteeringOutputProvenance(), steerMessagesWithPersistenceIntent(
		steeringMessageEventDefault,
		true,
		[]llm.Message{{
			Role:        llm.RoleDeveloper,
			MessageType: textutil.Value(llm.MessageTypeErrorFeedback),
			Content:     textutil.Value(fmt.Sprintf("Scheduled Worktree operation failed: %s", err)),
		}},
	).items, nil)
	return errors.Join(err, persistErr)
}

func (e *Engine) applyUserShellQueueEntry(shell *steeringUserShell) (tools.Result, error) {
	if shell == nil {
		return tools.Result{}, errors.New("user-shell Steering operation is required")
	}
	if shell.onActive != nil {
		shell.onActive()
	}
	e.ensureLifecycle()
	call := llm.ToolCall{
		ID:   uuid.NewString(),
		Name: string(toolspec.ToolExecCommand),
		Input: mustJSON(map[string]any{
			"cmd":            shell.command,
			"user_initiated": true,
		}),
	}
	receipt := session.CommitReceipt{}
	err := e.applyRuntimeMutations(runtimeSteeringOutputProvenance(), steerMessagesWithPersistenceIntent(
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call}}},
	).items, &receipt)
	if err != nil || !receipt.Committed {
		return tools.Result{}, err
	}
	handler, registered := e.registry.Get(toolspec.ToolExecCommand)
	if !registered {
		result := tools.Result{
			CallID:  call.ID,
			Name:    toolspec.ToolExecCommand,
			IsError: true,
			Output:  mustJSON(map[string]any{"error": "unknown tool"}),
			Summary: textutil.Value("unknown tool"),
		}
		return result, errors.Join(
			e.applyRuntimeMutations(
				runtimeSteeringOutputProvenance(),
				steerToolCompletionIntent(result).items,
				nil,
			),
			errUnknownTool,
		)
	}
	result, callErr := handler.Call(e.lifecycleCtx, tools.Call{
		ID:    call.ID,
		Name:  toolspec.ToolExecCommand,
		Input: call.Input,
	})
	if !toolResultHasCompletedOutcome(result) && callErr != nil {
		result = tools.Result{
			CallID:  call.ID,
			Name:    toolspec.ToolExecCommand,
			IsError: true,
			Output:  mustJSON(map[string]any{"error": callErr.Error()}),
			Summary: textutil.Value(callErr.Error()),
		}
	}
	result.CallID = call.ID
	result.Name = toolspec.ToolExecCommand
	result = tools.MaterializeModelWarnings(result)
	if e.closed.Load() {
		return result, errors.Join(callErr, &resultGroupFatal{Cause: ErrEngineClosed})
	}
	persistErr := e.applyRuntimeMutations(
		runtimeSteeringOutputProvenance(),
		steerToolCompletionIntent(result).items,
		nil,
	)
	return result, errors.Join(callErr, persistErr)
}

func (e *Engine) releaseEmptyDrainDecision() {
	if err := e.startPendingGoalLoop(); err != nil {
		e.surfaceRunError(err)
	}
}

func (e *Engine) drainSteeringAtBoundary(ctx context.Context, stepID string) error {
	return e.drainSteeringAtBoundaryWithProvenance(
		ctx,
		exactSteeringOutputProvenance(stepID),
	)
}

func (e *Engine) drainSteeringAtRuntimeBoundary(ctx context.Context) error {
	return e.drainSteeringAtBoundaryWithProvenance(
		ctx,
		runtimeSteeringOutputProvenance(),
	)
}

func (e *Engine) drainSteeringAtBoundaryWithProvenance(
	ctx context.Context,
	provenance steeringOutputProvenance,
) error {
	if e == nil || e.steering == nil {
		return nil
	}
	e.ensureLifecycle()
	drainCtx, cancelDrain := context.WithCancelCause(ctx)
	stopLifecycleCancellation := context.AfterFunc(e.lifecycleCtx, func() {
		cancelDrain(context.Cause(e.lifecycleCtx))
	})
	defer func() {
		stopLifecycleCancellation()
		cancelDrain(context.Canceled)
	}()
	if err := e.steering.bindDeferredHumanProvenance(provenance); err != nil {
		return err
	}
	e.steering.requestWake()
	e.drainSteeringIncludingDeferredHumanWithContext(drainCtx, true)
	return e.steering.waitUntilMutationsApplied(drainCtx)
}

func (e *Engine) AcceptHumanSteering(text string, accept CommandAcceptance) (QueuedUserMessage, error) {
	return e.acceptHumanMessageSteering(
		llm.Message{Role: llm.RoleUser, Content: textutil.Value(text)},
		accept,
	)
}

func (e *Engine) AcceptAgentSteering(steer AgentSteer, accept CommandAcceptance) (QueuedUserMessage, error) {
	return e.acceptHumanMessageSteering(steer.Message(), accept)
}

func (e *Engine) acceptHumanMessageSteering(message llm.Message, accept CommandAcceptance) (QueuedUserMessage, error) {
	if e == nil || e.steering == nil || e.workflowControl == nil {
		return QueuedUserMessage{}, ErrSteeringUnavailable
	}
	if message.Content == nil || strings.TrimSpace(*message.Content) == "" {
		return QueuedUserMessage{}, errors.New("empty message")
	}
	if err := e.workflowControl.validateSteering(steeringAdmissionSend); err != nil {
		return QueuedUserMessage{}, err
	}
	item := QueuedUserMessage{
		ID:      runtimeids.NewQueueItemID().String(),
		Message: message,
	}
	committed, err := runCommandAcceptance(accept, func() (bool, error) {
		var wake bool
		admitted, err := e.workflowControl.withSteeringAdmission(steeringAdmissionSend, func() (bool, error) {
			deferUntilStepBoundary := e.stepLifecycle.Snapshot() != nil
			entry := newHumanSteeringQueueEntry(item, deferUntilStepBoundary)
			var scope *runtimeids.ExecutionScopeID
			if execution, active := e.currentNodeExecutionConfig(); active {
				scopeID := execution.ScopeID
				scope = &scopeID
			}
			var appendErr error
			wake, appendErr = e.steering.appendHuman(entry, scope, !deferUntilStepBoundary)
			return appendErr == nil, appendErr
		})
		if err != nil || !admitted {
			return admitted, err
		}
		e.emitQueuedUserMessageStatus(item, QueuedUserMessageAccepted, "", false)
		if wake {
			e.wakeSteeringDrain()
		}
		return true, nil
	})
	if err := commandAcceptanceResult(committed, err); err != nil {
		return QueuedUserMessage{}, err
	}
	return item, nil
}

func (e *Engine) HasPendingSteering() bool {
	return e != nil && e.steering != nil && e.steering.pendingWork()
}

func (e *Engine) RemoveStoppedHumanSteering(scopeID runtimeids.ExecutionScopeID) []QueuedUserMessage {
	if e == nil || e.steering == nil {
		return nil
	}
	removed := e.steering.removeHumanByScope(scopeID)
	removed = append(removed, e.messageFlow.DrainPendingUserInjectionsByScope(scopeID)...)
	if len(removed) == 0 {
		return nil
	}
	sort.SliceStable(removed, func(i, j int) bool {
		return removed[i].ordinal < removed[j].ordinal
	})
	items := make([]QueuedUserMessage, 0, len(removed))
	eventItems := make([]InterruptedHumanInput, 0, len(removed))
	for _, removedItem := range removed {
		item := removedItem.item
		text, err := item.DisplayText()
		if err != nil {
			e.surfaceRunError(fmt.Errorf("interrupted human input: %w", err))
			continue
		}
		items = append(items, item)
		eventItems = append(eventItems, InterruptedHumanInput{
			QueueItemID: item.ID,
			Text:        text,
		})
	}
	if len(eventItems) != 0 {
		e.emitRaw(Event{
			Kind: EventHumanInputInterrupted,
			HumanInputInterrupted: &HumanInputInterruptedEvent{
				Items: eventItems,
			},
		})
	}
	return items
}

func (e *Engine) applySteeringQueueEntry(entry *steeringQueueEntry) steeringOutputReply {
	if err := entry.validate(); err != nil {
		return steeringOutputReply{err: err}
	}
	if entry.output == nil {
		return steeringOutputReply{err: errors.New("unsupported Steering queue operation")}
	}
	operation := entry.output
	if !operation.commitReceipt {
		items := make([]steeringMutation, 0)
		for _, intent := range operation.intents {
			items = append(items, intent.items...)
		}
		reply := steeringOutputReply{err: e.applyRuntimeMutations(operation.provenance, items, nil)}
		e.finishHumanSteering(operation, reply.err)
		return reply
	}
	receipt := session.CommitReceipt{}
	intent := operation.intents[0]
	err := e.applyRuntimeMutations(operation.provenance, intent.items, &receipt)
	e.finishHumanSteering(operation, err)
	return steeringOutputReply{receipt: receipt, err: err}
}

func (e *Engine) finishHumanSteering(operation *steeringOutputOperation, err error) {
	if operation == nil || operation.humanItem == nil {
		return
	}
	item := *operation.humanItem
	if err != nil {
		e.emitQueuedUserMessageStatus(item, QueuedUserMessageFailed, QueuedUserMessageFailureRuntimeUnavailable, true)
		return
	}
	e.emitQueuedUserMessageStatus(item, QueuedUserMessageSubmitted, "", false)
}

func (e *Engine) applyWorkflowAssignmentQueueEntry(entry *steeringQueueEntry) steeringOutputReply {
	start := entry.start
	assignment := start.assignment
	if start.persisted {
		if err := e.validatePersistedWorkflowAssignment(start.reference, assignment); err != nil {
			return steeringOutputReply{err: err}
		}
		return steeringOutputReply{receipt: session.CommitReceipt{Committed: true}}
	}
	message, err := buildWorkflowAssignmentMessage(assignment)
	if err != nil {
		return steeringOutputReply{err: err}
	}
	intent := steerMessagesWithPersistenceIntent(steeringMessageEventDefault,
		true,
		[]llm.Message{message},
	)
	receipt := session.CommitReceipt{}
	err = e.applyRuntimeMutations(runtimeSteeringOutputProvenance(), intent.items, &receipt)
	return steeringOutputReply{receipt: receipt, err: err}
}

func steeringFailureRequiresRuntimeClose(reply steeringOutputReply) bool {
	if errors.Is(reply.err, session.ErrStoreRecoveryRequired) {
		panic(fmt.Sprintf("Runtime Steering lost coherent Session mutation authority: %v", reply.err))
	}
	return errors.Is(reply.err, session.ErrMutationDefinitelyUncommitted)
}

func (e *Engine) closeAdmissionAfterSteeringFailure(failed *steeringQueueEntry) {
	if e == nil {
		return
	}
	interrupted := e.steering.pendingHumanForFailure(failed)
	if e.messageFlow != nil {
		interrupted = append(interrupted, e.messageFlow.DrainInterruptedUserInjections()...)
	}
	sort.SliceStable(interrupted, func(i, j int) bool {
		return interrupted[i].ordinal < interrupted[j].ordinal
	})
	eventItems := make([]InterruptedHumanInput, 0, len(interrupted))
	for _, input := range interrupted {
		text, err := input.item.DisplayText()
		if err != nil {
			e.surfaceRunError(fmt.Errorf("fatal Steering human input: %w", err))
			continue
		}
		eventItems = append(eventItems, InterruptedHumanInput{
			QueueItemID: input.item.ID,
			Text:        text,
		})
	}
	e.closeAdmissionAfterRuntimeAbort()
	if len(eventItems) != 0 {
		e.emitRaw(Event{
			Kind: EventHumanInputInterrupted,
			HumanInputInterrupted: &HumanInputInterruptedEvent{
				Items: eventItems,
			},
		})
	}
}

func (e *Engine) validatePersistedWorkflowAssignment(
	reference workflow.CurrentNodeReference,
	assignment WorkflowAssignment,
) error {
	if err := reference.Validate(); err != nil {
		return err
	}
	if assignment.Prompt.Identity != workflowruntime.CurrentNodePromptIdentity(reference) {
		return fmt.Errorf("persisted Workflow assignment does not match Current Node %v", reference)
	}
	messages := e.transcriptRuntimeState().SnapshotMessages()
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.MessageType == nil || *message.MessageType != llm.MessageTypeWorkflowMode {
			continue
		}
		if message.SourcePath == nil ||
			*message.SourcePath != workflowruntime.CurrentNodePromptIdentity(reference) {
			return fmt.Errorf("persisted Workflow assignment does not match Current Node %v", reference)
		}
		return nil
	}
	return fmt.Errorf("persisted Workflow assignment for Current Node %v is absent", reference)
}

func (e *Engine) applyRuntimeMutations(
	provenance steeringOutputProvenance,
	items []steeringMutation,
	commitReceipt *session.CommitReceipt,
) error {
	stepID, err := steeringOutputStepID(provenance)
	if err != nil {
		return err
	}
	for _, item := range items {
		if err := e.applySteeringMutation(stepID, item, commitReceipt); err != nil {
			return err
		}
		if _, replacesHistory := item.(*steeringHistoryReplacement); !replacesHistory {
			e.compactionRuntimeState().ApplyWorkflowPostCompletionActivity(
				workflowPostCompletionActivityForSteeringMutation(item),
			)
		}
	}
	return nil
}

func (e *Engine) applyNestedRuntimeMutation(
	stepID *string,
	mutation steeringMutation,
	commitReceipt *session.CommitReceipt,
) error {
	return e.applySteeringMutation(stepID, mutation, commitReceipt)
}

func (e *Engine) applyExactRuntimeMutation(stepID string, mutation steeringMutation) error {
	if e == nil {
		return nil
	}
	if err := validateSteeringMutation(mutation); err != nil {
		return err
	}
	return e.applyRuntimeMutations(
		exactSteeringOutputProvenance(stepID),
		[]steeringMutation{mutation},
		nil,
	)
}

func workflowPostCompletionActivityForSteeringMutation(mutation steeringMutation) workflowPostCompletionActivity {
	if message, ok := mutation.(*steeringMessage); ok {
		return workflowPostCompletionMessageActivity(message.message)
	}
	switch mutation := mutation.(type) {
	case *steeringAssistantCommit,
		*steeringCommittedAssistantMessage,
		*steeringCompletedResponseResolution,
		*steeringGoalNoticeAndStatus,
		*steeringReviewerFeedback,
		*steeringReviewerError,
		*steeringToolCompletion,
		*steeringQueuedUserMessageFlush:
		return workflowPostCompletionDurableActivity
	case *steeringResultGroupFlush:
		if mutation.committed {
			return workflowPostCompletionDurableActivity
		}
	case *steeringMissingToolOutputRepair:
		if mutation.repaired > 0 {
			return workflowPostCompletionDurableActivity
		}
	}
	return workflowPostCompletionNoActivity
}

func (e *Engine) resolveCompletedResponseStream(stepID string, instruction completedResponseResolutionInstruction) (completedResponseResolutionOutcome, error) {
	outcome := completedResponseResolutionOutcome{}
	if err := e.applyExactRuntimeMutation(stepID, &steeringCompletedResponseResolution{
		instruction: instruction,
		outcome:     &outcome,
	}); err != nil {
		return completedResponseResolutionOutcome{}, err
	}
	if outcome.kind == completedResponseResolutionInvalid {
		return completedResponseResolutionOutcome{}, errors.New("completed response stream resolution produced no outcome")
	}
	return outcome, nil
}

func (e *Engine) applySteeringMutation(
	stepID *string,
	mutation steeringMutation,
	commitReceipt *session.CommitReceipt,
) error {
	switch mutation := mutation.(type) {
	case *steeringMissingToolOutputRepair:
		repair := mutation
		repaired, err := e.repairMissingToolOutputsByAppendingRaw(repair.repairStepID, repair.disposition)
		repair.repaired = repaired
		return err
	case *steeringThinkingLevel:
		return e.applyThinkingLevel(mutation.level)
	case *steeringFastMode:
		changed, receipt, err := e.applyFastModeWithCommittedFeedback(mutation.enabled, mutation.feedback, func(feedback steeringMutation, receipt *session.CommitReceipt) error {
			return e.applyNestedRuntimeMutation(stepID, feedback, receipt)
		})
		recordSteeringCommitReceipt(commitReceipt, receipt)
		if mutation.changed != nil {
			*mutation.changed = changed
		}
		return err
	case *steeringReviewerMode:
		changed, mode, receipt, err := e.applyReviewerWithCommittedFeedback(mutation.enabled, mutation.feedback, func(feedback steeringMutation, receipt *session.CommitReceipt) error {
			return e.applyNestedRuntimeMutation(stepID, feedback, receipt)
		})
		recordSteeringCommitReceipt(commitReceipt, receipt)
		if mutation.changed != nil {
			*mutation.changed = changed
		}
		if mutation.mode != nil {
			*mutation.mode = mode
		}
		return err
	case *steeringAutoCompaction:
		changed, enabled := e.applyAutoCompaction(mutation.enabled)
		if mutation.changed != nil {
			*mutation.changed = changed
		}
		if mutation.resultEnabled != nil {
			*mutation.resultEnabled = enabled
		}
		return nil
	case *steeringQuestions:
		changed, enabled, receipt, err := e.applyQuestionsWithCommittedFeedback(mutation.enabled, mutation.feedback, func(feedback steeringMutation, receipt *session.CommitReceipt) error {
			return e.applyNestedRuntimeMutation(stepID, feedback, receipt)
		})
		recordSteeringCommitReceipt(commitReceipt, receipt)
		if mutation.changed != nil {
			*mutation.changed = changed
		}
		if mutation.resultEnabled != nil {
			*mutation.resultEnabled = enabled
		}
		return err
	case *steeringResultGroupReport:
		report := mutation
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
	case *steeringResultGroupFlush:
		exactStepID, err := requireExactSteeringStepID(stepID, "result group flush")
		if err != nil {
			return err
		}
		flush := mutation
		if flush.collector == nil {
			return e.flushResultGroup(exactStepID, nil, flush.reason)
		}
		cursor := flush.collector.cursor
		err = e.flushResultGroup(exactStepID, flush.collector, flush.reason)
		flush.committed = err == nil && flush.collector.cursor > cursor
		return err
	case *steeringResultGroupClose:
		exactStepID, err := requireExactSteeringStepID(stepID, "result group close")
		if err != nil {
			return err
		}
		return e.closeResultGroup(exactStepID, mutation.collector)
	case *steeringAssistantCommit:
		exactStepID, err := requireExactSteeringStepID(stepID, "assistant commit")
		if err != nil {
			return err
		}
		commit := mutation
		if commit.result == nil {
			return errors.New("assistant commit steering item requires a result destination")
		}
		receipt, err := e.appendMessageRaw(stepID, commit.message, steeringMessageEventNone, true, &commit.result.provenance)
		recordSteeringCommitReceipt(commitReceipt, receipt)
		if err != nil || !receipt.Committed {
			return err
		}
		if len(VisibleChatEntriesFromMessage(commit.message)) == 0 {
			outcome, resolveErr := e.resolveCompletedResponseStreamRaw(exactStepID, completedResponseAbortInstruction())
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
			outcome, resolveErr := e.resolveCompletedResponseStreamRaw(exactStepID, instruction)
			commit.result.resolution = outcome
			return resolveErr
		}
		commit.result.resolution = completedResponseResolutionOutcome{
			kind: completedResponseResolutionAbsent,
		}
		if err := e.emitCommittedAssistantMessageEventRaw(exactStepID, steeringCommittedAssistantMessage{
			message:    commit.message,
			coordinate: cloneCommittedAssistantCoordinate(coordinate),
			provenance: cloneTranscriptCommittedRowProvenance(commit.result.provenance),
		}, nil, nil); err != nil {
			return err
		}
		outcome, resolveErr := e.resolveCompletedResponseStreamRaw(exactStepID, completedResponseAbortInstruction())
		commit.result.resolution = outcome
		return resolveErr
	case *steeringMessage:
		receipt, err := e.appendMessageRaw(
			stepID, mutation.message, mutation.eventPolicy, mutation.persist, mutation.provenanceDestination,
		)
		recordSteeringCommitReceipt(commitReceipt, receipt)
		if err == nil && receipt.Committed && mutation.emitUserFlushEvent {
			if flushed := flushedUserMessageEvent(
				*mutation.provenanceDestination, mutation.message, stepID,
			); flushed != nil {
				err = errors.Join(err, e.emitRaw(*flushed))
			}
		}
		return err
	case *steeringGoalNoticeAndStatus:
		notice := mutation
		receipt, noticeErr := e.appendMessageRaw(
			stepID,
			notice.message,
			steeringMessageEventDefault,
			true,
			nil,
		)
		recordSteeringCommitReceipt(commitReceipt, receipt)
		if !receipt.Committed {
			return noticeErr
		}
		statusErr := e.emitRaw(Event{
			Kind:       EventGoalStatusUpdated,
			GoalStatus: &notice.update,
		}.withStepID(stepID))
		return errors.Join(noticeErr, statusErr)
	case *steeringGoalMutation:
		result, err := e.applyGoalMutation(mutation.mutation, func(notice *steeringGoalNoticeAndStatus, receipt *session.CommitReceipt) error {
			return e.applyNestedRuntimeMutation(stepID, notice, receipt)
		})
		if mutation.result != nil {
			*mutation.result = result
		}
		recordSteeringCommitReceipt(commitReceipt, result.MetadataReceipt)
		return err
	case *steeringCommittedAssistantMessage:
		exactStepID, err := requireExactSteeringStepID(stepID, "committed assistant event")
		if err != nil {
			return err
		}
		return e.emitCommittedAssistantMessageRaw(exactStepID, *mutation)
	case *steeringCompletedResponseResolution:
		exactStepID, err := requireExactSteeringStepID(stepID, "completed response resolution")
		if err != nil {
			return err
		}
		resolution := mutation
		if resolution.outcome == nil {
			return errors.New("completed response stream resolution requires an outcome destination")
		}
		outcome, err := e.resolveCompletedResponseStreamRaw(exactStepID, resolution.instruction)
		if err != nil {
			return err
		}
		*resolution.outcome = outcome
		return nil
	case *steeringLocalEntry:
		receipt, _, err := e.appendPersistedLocalEntryRecordRaw(
			stepID, mutation.entry, mutation.reasoningTraceIdentity,
		)
		recordSteeringCommitReceipt(commitReceipt, receipt)
		return err
	case *steeringReviewerFeedback:
		exactStepID, exactErr := requireExactSteeringStepID(stepID, "reviewer feedback")
		if exactErr != nil {
			return exactErr
		}
		id := runtimeids.NewReviewerFeedbackID()
		visibility, visibilityErr := sessionEntryVisibilityFromRuntime(mutation.visibility)
		if visibilityErr != nil {
			return visibilityErr
		}
		record := session.ReviewerFeedbackRecord{ID: id, Suggestions: append([]string(nil), mutation.suggestions...), Visibility: visibility}
		appended, receipt, err := e.eventLog.AppendRecord(stepID, record)
		recordSteeringCommitReceipt(commitReceipt, receipt)
		if receipt.Committed {
			provenance, provenanceErr := transcriptProvenanceFromRecord(appended)
			err = errors.Join(err, provenanceErr)
			if provenanceErr != nil {
				return err
			}
			entry := reviewerFeedbackChatEntryFromSessionRecord(record, exactStepID, &provenance)
			e.transcriptRuntimeState().chatProjection().appendLocalEntryRecord(entry, nil)
			err = errors.Join(err, e.emitRaw(Event{Kind: EventLocalEntryAdded, StepID: exactStepIDPointer(exactStepID), LocalEntry: &entry, LocalEntryProjected: true, CommittedTranscriptChanged: true, CommittedProvenance: &provenance}))
		}
		return err
	case *steeringReviewerError:
		exactStepID, exactErr := requireExactSteeringStepID(stepID, "reviewer error")
		if exactErr != nil {
			return exactErr
		}
		id := runtimeids.NewReviewerErrorID()
		record := session.ReviewerErrorRecord{ID: id, Detail: mutation.detail}
		appended, receipt, err := e.eventLog.AppendRecord(stepID, record)
		recordSteeringCommitReceipt(commitReceipt, receipt)
		if receipt.Committed {
			provenance, provenanceErr := transcriptProvenanceFromRecord(appended)
			err = errors.Join(err, provenanceErr)
			if provenanceErr != nil {
				return err
			}
			entry := reviewerErrorChatEntryFromSessionRecord(record, exactStepID, &provenance)
			e.transcriptRuntimeState().chatProjection().appendLocalEntryRecord(entry, nil)
			err = errors.Join(err, e.emitRaw(Event{Kind: EventLocalEntryAdded, StepID: exactStepIDPointer(exactStepID), LocalEntry: &entry, LocalEntryProjected: true, CommittedTranscriptChanged: true, CommittedProvenance: &provenance}))
		}
		return err
	case *steeringHistoryReplacement:
		receipt, err := e.replaceHistoryRaw(stepID, *mutation)
		recordSteeringCommitReceipt(commitReceipt, receipt)
		return err
	case *steeringToolCompletion:
		completion := e.finalizeLiveToolCompletion(mutation.result)
		receipt, provenance, feedbackProvenance, err := e.persistFinalizedToolCompletionRaw(stepID, completion)
		recordSteeringCommitReceipt(commitReceipt, receipt)
		if receipt.Committed {
			err = errors.Join(err, e.publishCommittedFinalizedToolCompletion(
				stepID,
				stepID,
				completion,
				provenance,
				feedbackProvenance,
			))
		}
		return err
	case *steeringQueuedUserMessageFlush:
		receipt, err := e.appendQueuedUserMessageFlush(stepID, mutation.message, mutation.batch, mutation.queueItems)
		recordSteeringCommitReceipt(commitReceipt, receipt)
		return err
	case *steeringQueuedUserMessageRestore:
		e.messageFlow.RestorePendingUserInjections(mutation.items)
		for _, pending := range mutation.items {
			e.emitQueuedUserMessageStatus(pending.message, QueuedUserMessageAccepted, "", false)
		}
		return nil
	case *steeringEvent:
		evt := mutation.event.withStepID(stepID)
		if evt.Kind == EventReviewerStarted {
			eventStepID, err := requireStepID(evt.StepID, "start Reviewer lifecycle")
			if err != nil {
				return err
			}
			revision, err := e.TranscriptRevision()
			if err != nil {
				return err
			}
			e.reviewerRuntimeState().SetActiveStep(eventStepID)
			return e.emitRawAtRevision(evt, revision)
		}
		if evt.Kind == EventReviewerCompleted {
			eventStepID, err := requireStepID(evt.StepID, "complete Reviewer lifecycle")
			if err != nil {
				return err
			}
			e.reviewerRuntimeState().ClearActiveStep(eventStepID)
			revision, err := e.TranscriptRevision()
			if err != nil {
				return err
			}
			return e.emitRawAtRevision(evt, revision)
		}
		switch evt.Kind {
		case EventCompactionStarted:
			if evt.Compaction != nil {
				eventStepID, err := requireStepID(evt.StepID, "start compaction")
				if err != nil {
					return err
				}
				e.compactionRuntimeState().SetActive(eventStepID, evt.Compaction.Mode, evt.Compaction.Count)
			}
		case EventCompactionCompleted, EventCompactionFailed:
			eventStepID, err := requireStepID(evt.StepID, "terminalize compaction")
			if err != nil {
				return err
			}
			e.compactionRuntimeState().ClearActive(eventStepID)
		}
		if evt.Kind == EventToolCallStarted && evt.ToolCall != nil {
			eventStepID, err := requireStepID(evt.StepID, "record live tool start")
			if err != nil {
				return err
			}
			if err := e.transcriptRuntimeState().RecordLiveToolStart(eventStepID, *evt.ToolCall); err != nil {
				return err
			}
		}
		return e.emitRaw(evt)
	case *steeringCacheWarning:
		warning := mutation.warning
		visibility := normalizeRuntimeEntryVisibility(mutation.visibility)
		record, adaptErr := sessionCacheWarningRecordFromRuntime(warning)
		if adaptErr != nil {
			return fmt.Errorf("adapt cache warning record: %w", adaptErr)
		}
		appended, receipt, appendErr := e.eventLog.AppendRecord(stepID, record)
		recordSteeringCommitReceipt(commitReceipt, receipt)
		if appendErr != nil && !receipt.Committed {
			return appendErr
		}
		provenance, provenanceErr := transcriptProvenanceFromRecord(appended)
		if provenanceErr != nil {
			return errors.Join(appendErr, provenanceErr)
		}
		e.transcriptRuntimeState().AppendCommittedEntryWithVisibility(cacheWarningTranscriptRole, transcript.CacheWarningText(warning), visibility, &provenance)
		if mutation.emit {
			appendErr = errors.Join(appendErr, e.emitRaw(Event{Kind: EventCacheWarning, CacheWarning: copyCacheWarning(&warning), CacheWarningVisibility: visibility, CommittedTranscriptChanged: true, CommittedProvenance: &provenance}.withStepID(stepID)))
		}
		return appendErr
	case *steeringCacheObservation:
		observation := mutation
		records, receipt, appendErr := e.eventLog.AppendRecordsAtomic(stepID, observation.records)
		recordSteeringCommitReceipt(commitReceipt, receipt)
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
				appendErr = errors.Join(appendErr, e.emitRaw(Event{Kind: EventCacheWarning, CacheWarning: copyCacheWarning(&warning), CacheWarningVisibility: visibility, CommittedTranscriptChanged: true, CommittedProvenance: warningProvenance}.withStepID(stepID)))
			}
		}
		return appendErr
	case *steeringLiveToolAbort:
		exactStepID, err := requireExactSteeringStepID(stepID, "live tool abort")
		if err != nil {
			return err
		}
		return e.emitLiveToolAbortsRaw(exactStepID, mutation.reason)
	case *steeringStreamingOutput:
		exactStepID, err := requireExactSteeringStepID(stepID, "streaming output")
		if err != nil {
			return err
		}
		if mutation.reasoningReset != nil {
			e.transcriptRuntimeState().ResetReasoningTraces(exactStepID)
			return e.emitRaw(Event{Kind: EventReasoningDeltaReset, StepID: exactStepIDPointer(exactStepID)})
		}
		if mutation.clearReasoning {
			e.transcriptRuntimeState().ClearReasoningState(exactStepID)
			return nil
		}
		if mutation.assistantDelta != nil {
			delta := *mutation.assistantDelta
			if delta.Text == "" {
				return nil
			}
			revision, err := e.TranscriptRevision()
			if err != nil {
				return err
			}
			metadata, streamID := e.transcriptRuntimeState().AppendStreamingDelta(exactStepID, revision, e.CommittedTranscriptEntryCount(), delta.Text, delta.Phase)
			return e.emitRaw(Event{Kind: EventAssistantDelta, StepID: exactStepIDPointer(exactStepID), AssistantDelta: delta.Text, AssistantDeltaPhase: delta.Phase, AssistantStreamMetadata: metadata, AssistantTranscriptStreamID: streamID})
		}
		if mutation.reasoningDelta != nil {
			delta := *mutation.reasoningDelta
			identity, err := e.transcriptRuntimeState().SetReasoningState(exactStepID, delta)
			if err != nil {
				return err
			}
			return e.emitRaw(Event{
				Kind:                   EventReasoningDelta,
				StepID:                 exactStepIDPointer(exactStepID),
				ReasoningDelta:         &delta,
				ReasoningTraceIdentity: cloneTranscriptReasoningTraceIdentity(identity),
			})
		}
		var clearedMetadata *AssistantStreamMetadata
		var clearedStreamID *uuid.UUID
		if mutation.clearState {
			clearedMetadata, clearedStreamID = e.clearStreamingAssistantStateRaw()
		}
		if mutation.resetEvents {
			return e.emitStreamingAssistantCleanupEventsRaw(exactStepID, clearedMetadata, clearedStreamID, mutation.abortReason)
		}
		return nil
	}
	return fmt.Errorf("unsupported Runtime mutation %T", mutation)

}

func recordSteeringCommitReceipt(destination *session.CommitReceipt, receipt session.CommitReceipt) {
	if destination != nil {
		*destination = receipt
	}
}

func requireExactSteeringStepID(stepID *string, operation string) (string, error) {
	return requireStepID(stepID, operation+" exact provenance")
}

func (event Event) withStepID(stepID *string) Event {
	if event.StepID == nil {
		event.StepID = cloneOptionalStepID(stepID)
	} else {
		event.StepID = cloneOptionalStepID(event.StepID)
	}
	return event
}

func (e *Engine) replaceHistoryRaw(stepID *string, replacement steeringHistoryReplacement) (session.CommitReceipt, error) {
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
		stepID,
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
	if stepID != nil {
		for index := range replacement.projectedEntries {
			replacement.projectedEntries[index].StepID = cloneOptionalStepID(stepID)
		}
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
	e.resetCurrentPreciseInputTracking()
	e.resetLocalDiagnostics()
	e.transcriptRuntimeState().ReplaceHistoryAtCommittedEntryStart(
		stepID,
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
		e.emitRaw(Event{Kind: EventConversationUpdated}.withStepID(stepID)),
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
	stepID *string,
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
				"history replacement projected row %d lacks filtered ordinal (role=%q)",
				idx,
				copyEntry.Role,
			)
		}
		if err := e.emitRaw(Event{
			Kind:                       EventLocalEntryAdded,
			LocalEntry:                 &copyEntry,
			LocalEntryProjected:        true,
			CommittedTranscriptChanged: true,
			CommittedEntryStart:        start + idx,
			CommittedEntryStartSet:     true,
			CommittedEntryCount:        start + idx + 1,
			CommittedProvenance:        provenance,
		}.withStepID(stepID)); err != nil {
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
