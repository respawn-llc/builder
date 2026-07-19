package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"core/server/llm"
	"core/shared/runtimeids"
)

var ErrNoActiveLiveRun = errors.New("no active live run")

var ErrLiveRunNoFinalAnswer = errors.New("live run completed without a final answer")

var ErrLiveRunGroupClosing = &LiveRunGroupClosingError{}

type LiveRunResultKind string
type LiveRunNoFinalAnswerReason string
type LiveRunFailureCode string
type liveStepToolStartCount uint8
type liveRunPhase uint8

type LiveRunGroupClosingError struct {
	GroupID runtimeids.LiveRunGroupID
}

func (e *LiveRunGroupClosingError) Error() string {
	if e == nil || e.GroupID.IsZero() {
		return "live run group is closing"
	}
	return fmt.Sprintf("live run group %s is closing", e.GroupID.String())
}

func (e *LiveRunGroupClosingError) Is(target error) bool {
	_, ok := target.(*LiveRunGroupClosingError)
	return ok
}

func liveRunGroupClosingError(group *liveRunGroup) error {
	if group == nil {
		return ErrLiveRunGroupClosing
	}
	return &LiveRunGroupClosingError{GroupID: group.id}
}

const (
	LiveRunResultAssistantFinalAnswer LiveRunResultKind = "assistant_final_answer"
	LiveRunResultRuntimeFailure       LiveRunResultKind = "runtime_failure"
	LiveRunResultCompletedNoFinal     LiveRunResultKind = "completed_no_final_answer"
	LiveRunResultInterrupted          LiveRunResultKind = "interrupted"
	LiveRunResultWorkflowCompleted    LiveRunResultKind = "workflow_completed"
	LiveRunResultNonTaskActivity      LiveRunResultKind = "non_task_activity"

	LiveRunFailureCodeRuntime LiveRunFailureCode = "runtime_failure"

	LiveRunNoFinalAnswerReasonGoalLoop   LiveRunNoFinalAnswerReason = "goal_loop"
	LiveRunNoFinalAnswerReasonUserShell  LiveRunNoFinalAnswerReason = "shell_command"
	LiveRunNoFinalAnswerReasonWorkflow   LiveRunNoFinalAnswerReason = "workflow_completion"
	LiveRunNoFinalAnswerReasonBackground LiveRunNoFinalAnswerReason = "background_notice"
	LiveRunNoFinalAnswerReasonUnknown    LiveRunNoFinalAnswerReason = "unknown"
)

const (
	liveStepToolStartsNone liveStepToolStartCount = iota
	liveStepToolStartsOne
	liveStepToolStartsMultiple
)

const (
	liveRunPhaseOpen liveRunPhase = iota
	liveRunPhasePrepared
)

type LiveRunFailureDiagnostic struct {
	Code   LiveRunFailureCode
	Detail string
}

type LiveRunResult struct {
	GroupID           runtimeids.LiveRunGroupID
	RunID             runtimeids.RunID
	StepID            runtimeids.StepID
	Status            RunStatus
	WorkPerformed     bool
	ResultKind        LiveRunResultKind
	NoFinalReason     LiveRunNoFinalAnswerReason
	AssistantMessage  llm.Message
	FailureDiagnostic *LiveRunFailureDiagnostic
	Error             error
	StartedAt         time.Time
	FinishedAt        time.Time
}

type LiveRunWaitHandle struct {
	coordinator *liveRunCoordinator
	group       *liveRunGroup
	done        <-chan struct{}
	ctx         context.Context
}

type liveRunCoordinator struct {
	mu                          sync.Mutex
	queueFlushCommitMu          sync.Mutex
	current                     *liveRunGroup
	stoppedQueueItems           map[runtimeids.QueueItemID]struct{}
	stoppedPublishingQueueItems map[runtimeids.QueueItemID]struct{}
}

type liveRunGroup struct {
	id                runtimeids.LiveRunGroupID
	runID             runtimeids.RunID
	stepID            runtimeids.StepID
	stepToolStarts    liveStepToolStartCount
	workPerformed     bool
	goalLoop          bool
	phase             liveRunPhase
	status            RunStatus
	resultKind        LiveRunResultKind
	resultKindSet     bool
	noFinalReason     LiveRunNoFinalAnswerReason
	assistantMessage  llm.Message
	failureDiagnostic *LiveRunFailureDiagnostic
	err               error
	startedAt         time.Time
	finishedAt        time.Time
	done              chan struct{}
	frozenResult      *LiveRunResult
	completionToken   *liveRunCompletionToken
	reservations      int
	taggedQueueItems  map[runtimeids.QueueItemID]struct{}
	publishingItems   map[runtimeids.QueueItemID]struct{}
	goalLoopHolding   bool
	waiters           int
}

type liveRunAdmission struct {
	group *liveRunGroup
}

// liveRunCompletionToken is deliberately opaque outside live-run coordination.
// A later terminal-fact publisher owns the prepare-to-commit gap.
type liveRunCompletionToken struct {
	group *liveRunGroup
}

func newLiveRunCoordinator() *liveRunCoordinator {
	return &liveRunCoordinator{}
}

func (e *Engine) HasActiveLiveRunGroup() bool {
	if e == nil {
		return false
	}
	e.ensureOrchestrationCollaborators()
	return e.liveRun.hasActive()
}

func (e *Engine) WaitForActiveRunResult(ctx context.Context) (LiveRunResult, error) {
	if e == nil {
		return LiveRunResult{}, ErrNoActiveLiveRun
	}
	e.ensureOrchestrationCollaborators()
	handle, err := e.liveRun.captureWait(ctx)
	if err != nil {
		return LiveRunResult{}, err
	}
	return handle.Wait()
}

func (e *Engine) CaptureActiveRunResult(ctx context.Context) (*LiveRunWaitHandle, error) {
	if e == nil {
		return nil, ErrNoActiveLiveRun
	}
	e.ensureOrchestrationCollaborators()
	return e.liveRun.captureWait(ctx)
}

func (e *Engine) TryInterruptActiveRun() (bool, error) {
	if e == nil {
		return false, nil
	}
	e.ensureOrchestrationCollaborators()
	snapshot := e.stepLifecycle.Snapshot()
	if (snapshot == nil || !activeKindInterruptibleByLiveStop(snapshot.ActiveKind)) && !e.liveRun.hasPendingStopTarget() {
		return false, nil
	}
	interrupted, taggedQueueItems, goalLoop, token := e.liveRun.interrupt()
	if !interrupted {
		if snapshot == nil || !activeKindInterruptibleByLiveStop(snapshot.ActiveKind) {
			return false, nil
		}
		tracker := goalLoopInterruptTracker{engine: e, match: true}
		interruptedSnapshot, err := e.stepLifecycle.InterruptCurrent(tracker.onSnapshot)
		tracker.resolve(err, interruptedSnapshot)
		if err != nil {
			return interruptedSnapshot != nil, err
		}
		return interruptedSnapshot != nil, err
	}
	e.failStoppedLiveRunQueueItems(taggedQueueItems)
	e.publishLiveRunCompletion(token)
	if snapshot == nil || !activeKindInterruptibleByLiveStop(snapshot.ActiveKind) {
		return true, nil
	}
	tracker := goalLoopInterruptTracker{engine: e, match: goalLoop}
	interruptedSnapshot, err := e.stepLifecycle.InterruptCurrent(tracker.onSnapshot)
	tracker.resolve(err, interruptedSnapshot)
	if err != nil {
		return true, err
	}
	if goalLoop && !tracker.pending && e.goalActive() {
		e.goalLoopState().Suspend()
	}
	return true, nil
}

type goalLoopInterruptTracker struct {
	engine  *Engine
	match   bool
	pending bool
}

func (t *goalLoopInterruptTracker) onSnapshot(snapshot *RunSnapshot) {
	if t == nil || !t.match || t.engine == nil || !t.engine.goalActive() || snapshot == nil || snapshot.ActiveKind != ActiveKindGoalLoop {
		return
	}
	t.engine.goalLoopState().MarkInterruptPending()
	t.pending = true
}

func (t *goalLoopInterruptTracker) resolve(err error, snapshot *RunSnapshot) {
	if t == nil || !t.pending || t.engine == nil {
		return
	}
	if err == nil && t.engine.goalActive() && snapshot != nil && snapshot.ActiveKind == ActiveKindGoalLoop {
		t.engine.goalLoopState().CommitInterrupt()
		return
	}
	t.engine.goalLoopState().ClearInterruptPending()
}

func (e *Engine) QueueUserMessageForActiveRun(ctx context.Context, text string, clientRequestID runtimeids.RuntimeClientRequestID, beforeQueue func() error) (QueuedUserMessage, bool, error) {
	if e == nil {
		return QueuedUserMessage{}, false, ErrNoActiveLiveRun
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return QueuedUserMessage{}, false, err
	}
	if text == "" {
		return QueuedUserMessage{}, false, errors.New("empty message")
	}
	e.ensureOrchestrationCollaborators()
	admission, err := e.liveRun.beginAdmission()
	if err != nil {
		return QueuedUserMessage{}, false, err
	}
	admissionResolved := false
	defer func() {
		if !admissionResolved {
			e.publishLiveRunCompletion(e.liveRun.rollbackAdmission(admission))
		}
	}()
	if beforeQueue != nil {
		if err := beforeQueue(); err != nil {
			return QueuedUserMessage{}, false, err
		}
	}
	if err := ctx.Err(); err != nil {
		return QueuedUserMessage{}, false, err
	}
	item := QueuedUserMessage{ID: runtimeids.NewQueueItemID().String(), Text: text, ClientRequestID: clientRequestID.String()}
	finalized, token, err := e.liveRun.finishAdmission(admission, mustQueueItemID(item.ID), func(queueItemID string) {
		e.markQueuedUserInjectionForAutoDrain(queueItemID)
	})
	admissionResolved = true
	e.publishLiveRunCompletion(token)
	if err != nil {
		return QueuedUserMessage{}, false, err
	}
	if !finalized {
		return QueuedUserMessage{}, false, context.Canceled
	}
	item = e.messageFlow.QueueUserMessageWithID(item)
	e.emitQueuedUserMessageStatus(item, QueuedUserMessageAccepted, "", false)
	queueItemID := mustQueueItemID(item.ID)
	if e.liveRun.finishQueueItemPublication(queueItemID) {
		e.failStoppedLiveRunQueueItems(map[runtimeids.QueueItemID]struct{}{queueItemID: {}})
	} else {
		e.scheduleQueuedUserInjectionsIfIdle()
	}
	return item, true, nil
}

func (e *Engine) beginLiveRunStep(snapshot *RunSnapshot) {
	if e == nil || snapshot == nil {
		return
	}
	e.ensureOrchestrationCollaborators()
	e.liveRun.beginStep(snapshot)
}

func (e *Engine) finishLiveRunStep(snapshot *RunSnapshot, status RunStatus, err error) {
	if e == nil || snapshot == nil {
		return
	}
	e.ensureOrchestrationCollaborators()
	stoppedQueueItems, token := e.liveRun.finishStep(snapshot, status, err, e.shouldHoldLiveRunForGoalLoopContinuation(snapshot, status))
	e.failStoppedLiveRunQueueItems(stoppedQueueItems)
	e.publishLiveRunCompletion(token)
}

func (e *Engine) finishLiveRunGoalLoop() {
	if e == nil {
		return
	}
	e.ensureOrchestrationCollaborators()
	e.publishLiveRunCompletion(e.liveRun.finishGoalLoop())
}

func (e *Engine) recordLiveRunAssistantFinalAnswer(stepID string, message llm.Message) {
	if e == nil {
		return
	}
	e.ensureOrchestrationCollaborators()
	e.liveRun.recordAssistantFinalAnswer(stepID, message)
}

func (e *Engine) publishLiveExecutionToolStart(stepID string, call llm.ToolCall, committedEntryStart *int) error {
	event := Event{
		Kind:                       EventToolCallStarted,
		StepID:                     stepID,
		ToolCall:                   &call,
		CommittedTranscriptChanged: true,
	}
	if committedEntryStart != nil {
		event.CommittedEntryStart = *committedEntryStart
		event.CommittedEntryStartSet = true
	}
	if err := e.steer(stepID, steerEventIntent(event)); err != nil {
		return err
	}
	e.liveRun.recordAcceptedToolStart(stepID)
	return nil
}

func (e *Engine) publishRecoveredToolStart(stepID string, call llm.ToolCall) error {
	return e.steer(stepID, steerEventIntent(Event{
		Kind:     EventToolCallStarted,
		StepID:   stepID,
		ToolCall: &call,
	}))
}

func (e *Engine) completeLiveRunQueueItems(ids map[string]struct{}) {
	if e == nil || len(ids) == 0 {
		return
	}
	e.ensureOrchestrationCollaborators()
	e.publishLiveRunCompletion(e.liveRun.completeQueueItems(typedQueueItemIDSet(ids)))
}

// completeLiveRunQueueItemsWithinOutputMutation is deliberately limited to
// mutation preparation. Slice 9 owns publication and post-unlock token commit
// for this path because the queued-user flush already owns outputMutationMu.
func (e *Engine) completeLiveRunQueueItemsWithinOutputMutation(ids map[string]struct{}) {
	if e == nil || len(ids) == 0 {
		return
	}
	e.ensureOrchestrationCollaborators()
	if token := e.liveRun.completeQueueItems(typedQueueItemIDSet(ids)); token != nil {
		panic("queued-user flush prepared a live-run completion token before slice 9 registered post-unlock publication")
	}
}

func (e *Engine) publishLiveRunCompletion(token *liveRunCompletionToken) {
	if e == nil || token == nil {
		return
	}
	e.ensureOrchestrationCollaborators()
	result, ok := e.liveRun.completionResult(token)
	if !ok {
		panic("publish live-run completion with an invalid token")
	}
	e.steerRuntimeCloseEvent(result.StepID.String(), liveRunBatchFinishedEvent(result))
	if !e.liveRun.commitCompletion(token) {
		panic("commit live-run completion after runtime sink acceptance")
	}
}

func (e *Engine) failStoppedLiveRunQueueItems(ids map[runtimeids.QueueItemID]struct{}) {
	if e == nil || len(ids) == 0 {
		return
	}
	stringIDs := stringQueueItemIDSet(ids)
	rawIDs := make([]string, 0, len(stringIDs))
	for id := range stringIDs {
		rawIDs = append(rawIDs, id)
	}
	e.unmarkQueuedUserInjectionForAutoDrain(rawIDs...)
	failed := map[runtimeids.QueueItemID]struct{}{}
	for _, item := range e.messageFlow.DrainPendingUserInjectionsByID(stringIDs) {
		failed[mustQueueItemID(item.ID)] = struct{}{}
		e.emitQueuedUserMessageStatus(item, QueuedUserMessageFailed, QueuedUserMessageFailureStopped, true)
	}
	e.liveRun.clearStoppedQueueItems(failed)
}

func (e *Engine) dropStoppedLiveRunQueueItems(items []queuedUserSteeringIntent) []queuedUserSteeringIntent {
	if e == nil || len(items) == 0 {
		return items
	}
	ids := make(map[runtimeids.QueueItemID]struct{}, len(items))
	for _, item := range items {
		ids[mustQueueItemID(item.message.ID)] = struct{}{}
	}
	stopped := e.liveRun.takeStoppedQueueItems(ids)
	if len(stopped) == 0 {
		return items
	}
	filtered := items[:0]
	for _, item := range items {
		id := mustQueueItemID(item.message.ID)
		if _, ok := stopped[id]; ok {
			e.unmarkQueuedUserInjectionForAutoDrain(item.message.ID)
			e.emitQueuedUserMessageStatus(item.message, QueuedUserMessageFailed, QueuedUserMessageFailureStopped, true)
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func (e *Engine) commitLiveRunQueueItemsUnlessStopped(items []queuedUserSteeringIntent, commit func() error) (bool, error) {
	if e == nil {
		if commit == nil {
			return true, nil
		}
		return true, commit()
	}
	ids := make(map[runtimeids.QueueItemID]struct{}, len(items))
	for _, item := range items {
		ids[mustQueueItemID(item.message.ID)] = struct{}{}
	}
	return e.liveRun.commitQueueItemsUnlessStopped(ids, commit)
}

func (c *liveRunCoordinator) hasActive() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current != nil
}

func (c *liveRunCoordinator) hasPendingStopTarget() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	group := c.current
	return group != nil && group.status != RunStatusFailed && group.status != RunStatusInterrupted &&
		(len(group.taggedQueueItems) > 0 || len(group.publishingItems) > 0 || group.reservations > 0 || group.goalLoopHolding)
}

func (c *liveRunCoordinator) beginStep(snapshot *RunSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current != nil {
		if c.current.phase != liveRunPhaseOpen {
			panic(fmt.Sprintf("begin live run step while completion is prepared: group_id=%s phase=%d", c.current.id.String(), c.current.phase))
		}
		if c.current.status == RunStatusFailed || c.current.status == RunStatusInterrupted {
			c.current = newLiveRunGroup(snapshot)
			return
		}
		c.current.runID = mustRunID(snapshot.RunID)
		c.current.stepID = mustStepID(snapshot.StepID)
		c.current.stepToolStarts = liveStepToolStartsNone
		c.current.goalLoop = snapshot.GoalLoop
		c.current.status = RunStatusRunning
		c.current.resultKindSet = false
		c.current.noFinalReason = LiveRunNoFinalAnswerReasonUnknown
		c.current.assistantMessage = llm.Message{}
		c.current.failureDiagnostic = nil
		c.current.err = nil
		c.current.finishedAt = time.Time{}
		return
	}
	c.current = newLiveRunGroup(snapshot)
}

func newLiveRunGroup(snapshot *RunSnapshot) *liveRunGroup {
	return &liveRunGroup{
		id:            runtimeids.NewLiveRunGroupID(),
		runID:         mustRunID(snapshot.RunID),
		stepID:        mustStepID(snapshot.StepID),
		goalLoop:      snapshot.GoalLoop,
		status:        RunStatusRunning,
		noFinalReason: LiveRunNoFinalAnswerReasonUnknown,
		startedAt:     snapshot.StartedAt,
		done:          make(chan struct{}),
	}
}

func (c *liveRunCoordinator) finishStep(snapshot *RunSnapshot, status RunStatus, err error, holdGoalLoop bool) (map[runtimeids.QueueItemID]struct{}, *liveRunCompletionToken) {
	c.mu.Lock()
	group := c.current
	if group == nil {
		c.mu.Unlock()
		return nil, nil
	}
	if group.phase != liveRunPhaseOpen {
		c.mu.Unlock()
		return nil, nil
	}
	if group.status == RunStatusFailed || group.status == RunStatusInterrupted {
		token := c.prepareIfCompleteLocked(group)
		c.mu.Unlock()
		return nil, token
	}
	group.runID = mustRunID(snapshot.RunID)
	group.stepID = mustStepID(snapshot.StepID)
	group.goalLoop = snapshot.GoalLoop
	group.status = status
	group.err = err
	group.finishedAt = snapshot.FinishedAt
	if group.stepToolStarts == liveStepToolStartsMultiple {
		group.workPerformed = true
	}
	if status == RunStatusFailed || status == RunStatusInterrupted {
		group.resultKind = liveRunResultKindForNoFinalAnswer(status, LiveRunNoFinalAnswerReasonUnknown)
		group.resultKindSet = true
		group.noFinalReason = LiveRunNoFinalAnswerReasonUnknown
		group.assistantMessage = llm.Message{}
		group.failureDiagnostic = liveRunFailureDiagnostic(status, err)
	} else if !group.resultKindSet {
		group.resultKind = liveRunResultKindForNoFinalAnswer(status, liveRunNoFinalAnswerReason(snapshot.ActiveKind))
		group.resultKindSet = true
		group.noFinalReason = liveRunNoFinalAnswerReason(snapshot.ActiveKind)
	}
	var stoppedQueueItems map[runtimeids.QueueItemID]struct{}
	if status == RunStatusFailed || status == RunStatusInterrupted {
		stoppedQueueItems = cloneMapIfNonEmpty(group.taggedQueueItems)
		for id := range group.publishingItems {
			delete(stoppedQueueItems, id)
			if c.stoppedPublishingQueueItems == nil {
				c.stoppedPublishingQueueItems = make(map[runtimeids.QueueItemID]struct{})
			}
			c.stoppedPublishingQueueItems[id] = struct{}{}
		}
		c.markStoppedQueueItemsLocked(stoppedQueueItems)
		group.taggedQueueItems = nil
		group.publishingItems = nil
		token := c.prepareIfCompleteLocked(group)
		c.mu.Unlock()
		return stoppedQueueItems, token
	}
	group.goalLoopHolding = snapshot.GoalLoop && holdGoalLoop
	token := c.prepareIfCompleteLocked(group)
	c.mu.Unlock()
	return nil, token
}

func (c *liveRunCoordinator) recordAcceptedToolStart(stepID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current == nil || c.current.phase != liveRunPhaseOpen || c.current.stepID != mustStepID(stepID) {
		return
	}
	if c.current.stepToolStarts < liveStepToolStartsMultiple {
		c.current.stepToolStarts++
	}
}

func (c *liveRunCoordinator) finishGoalLoop() *liveRunCompletionToken {
	c.mu.Lock()
	group := c.current
	if group == nil {
		c.mu.Unlock()
		return nil
	}
	if group.phase != liveRunPhaseOpen {
		c.mu.Unlock()
		return nil
	}
	group.goalLoopHolding = false
	token := c.prepareIfCompleteLocked(group)
	c.mu.Unlock()
	return token
}

func (c *liveRunCoordinator) recordAssistantFinalAnswer(stepID string, message llm.Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current == nil || c.current.phase != liveRunPhaseOpen || c.current.stepID != mustStepID(stepID) {
		return
	}
	if c.current.goalLoop {
		return
	}
	c.current.resultKind = LiveRunResultAssistantFinalAnswer
	c.current.resultKindSet = true
	c.current.failureDiagnostic = nil
	c.current.err = nil
	c.current.assistantMessage = message
}

func (c *liveRunCoordinator) prepareIfCompleteLocked(group *liveRunGroup) *liveRunCompletionToken {
	if group == nil || group.phase != liveRunPhaseOpen {
		return nil
	}
	if group.status == RunStatusRunning || group.reservations != 0 || len(group.taggedQueueItems) != 0 || group.goalLoopHolding {
		return nil
	}
	return c.prepareCompletionLocked(group)
}

func (c *liveRunCoordinator) prepareCompletionLocked(group *liveRunGroup) *liveRunCompletionToken {
	if group == nil {
		return nil
	}
	if group.phase == liveRunPhasePrepared {
		return group.completionToken
	}
	if !group.resultKindSet {
		group.resultKind = liveRunResultKindForNoFinalAnswer(group.status, group.noFinalReason)
		group.resultKindSet = true
	}
	group.phase = liveRunPhasePrepared
	frozen := LiveRunResult{
		GroupID:           group.id,
		RunID:             group.runID,
		StepID:            group.stepID,
		Status:            group.status,
		WorkPerformed:     group.workPerformed,
		ResultKind:        group.resultKind,
		NoFinalReason:     group.noFinalReason,
		AssistantMessage:  cloneLLMMessage(group.assistantMessage),
		FailureDiagnostic: cloneLiveRunFailureDiagnostic(group.failureDiagnostic),
		Error:             group.err,
		StartedAt:         group.startedAt,
		FinishedAt:        group.finishedAt,
	}
	group.frozenResult = &frozen
	group.completionToken = &liveRunCompletionToken{group: group}
	return group.completionToken
}

func (c *liveRunCoordinator) commitCompletion(token *liveRunCompletionToken) bool {
	if c == nil || token == nil || token.group == nil {
		return false
	}
	c.mu.Lock()
	group := token.group
	if group.phase != liveRunPhasePrepared || group.completionToken != token {
		c.mu.Unlock()
		return false
	}
	if c.current == group {
		c.current = nil
	}
	group.completionToken = nil
	c.mu.Unlock()
	close(group.done)
	return true
}

func (c *liveRunCoordinator) completionResult(token *liveRunCompletionToken) (LiveRunResult, bool) {
	if c == nil || token == nil || token.group == nil {
		return LiveRunResult{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	group := token.group
	if group.phase != liveRunPhasePrepared || group.completionToken != token || group.frozenResult == nil {
		return LiveRunResult{}, false
	}
	return cloneLiveRunResult(*group.frozenResult), true
}

func liveRunBatchFinishedEvent(result LiveRunResult) Event {
	if result.GroupID.IsZero() || result.RunID.IsZero() || result.StepID.IsZero() || result.Status == RunStatusRunning || result.ResultKind == "" {
		panic(fmt.Sprintf(
			"invalid live-run batch-finished result: group_id=%s run_id=%s step_id=%s status=%s result_kind=%s",
			result.GroupID.String(),
			result.RunID.String(),
			result.StepID.String(),
			result.Status,
			result.ResultKind,
		))
	}
	copyResult := cloneLiveRunResult(result)
	return Event{Kind: EventLiveRunBatchFinished, LiveRunResult: &copyResult}
}

func (c *liveRunCoordinator) beginAdmission() (liveRunAdmission, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current == nil {
		return liveRunAdmission{}, ErrNoActiveLiveRun
	}
	if c.current.phase == liveRunPhasePrepared {
		return liveRunAdmission{}, liveRunGroupClosingError(c.current)
	}
	if c.current.status == RunStatusFailed || c.current.status == RunStatusInterrupted {
		return liveRunAdmission{}, ErrNoActiveLiveRun
	}
	c.current.reservations++
	return liveRunAdmission{group: c.current}, nil
}

func (c *liveRunCoordinator) finishAdmission(admission liveRunAdmission, queueItemID runtimeids.QueueItemID, markAutoDrain func(string)) (bool, *liveRunCompletionToken, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	group := admission.group
	if group != nil && group.phase == liveRunPhasePrepared {
		return false, nil, liveRunGroupClosingError(group)
	}
	if group == nil || (c.current != group && group.status != RunStatusFailed && group.status != RunStatusInterrupted) {
		return false, nil, ErrNoActiveLiveRun
	}
	if group.reservations == 0 {
		panic(fmt.Sprintf("finish live-run admission without reservation: group_id=%s status=%s", group.id.String(), group.status))
	}
	group.reservations--
	if group.status == RunStatusFailed || group.status == RunStatusInterrupted {
		token := c.prepareIfCompleteLocked(group)
		return false, token, liveRunGroupClosingError(group)
	}
	group.trackQueuedItemForLiveRun(queueItemID)
	if markAutoDrain != nil {
		markAutoDrain(queueItemID.String())
	}
	return true, nil, nil
}

func (c *liveRunCoordinator) beginQueueItemPublication(queueItemID runtimeids.QueueItemID, markAutoDrain func(string)) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current == nil {
		return false, ErrNoActiveLiveRun
	}
	if c.current.phase == liveRunPhasePrepared {
		return false, liveRunGroupClosingError(c.current)
	}
	if c.current.status != RunStatusRunning && c.current.status != RunStatusCompleted {
		return false, ErrNoActiveLiveRun
	}
	c.current.trackQueuedItemForLiveRun(queueItemID)
	if markAutoDrain != nil {
		markAutoDrain(queueItemID.String())
	}
	return true, nil
}

func (c *liveRunCoordinator) finishQueueItemPublication(queueItemID runtimeids.QueueItemID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current != nil {
		delete(c.current.publishingItems, queueItemID)
		if len(c.current.publishingItems) == 0 {
			c.current.publishingItems = nil
		}
	}
	if _, stopped := c.stoppedPublishingQueueItems[queueItemID]; stopped {
		delete(c.stoppedPublishingQueueItems, queueItemID)
		if len(c.stoppedPublishingQueueItems) == 0 {
			c.stoppedPublishingQueueItems = nil
		}
		return true
	}
	return false
}

func (g *liveRunGroup) trackQueuedItemForLiveRun(queueItemID runtimeids.QueueItemID) {
	if g.taggedQueueItems == nil {
		g.taggedQueueItems = make(map[runtimeids.QueueItemID]struct{})
	}
	g.taggedQueueItems[queueItemID] = struct{}{}
	if g.publishingItems == nil {
		g.publishingItems = make(map[runtimeids.QueueItemID]struct{})
	}
	g.publishingItems[queueItemID] = struct{}{}
}

func (c *liveRunCoordinator) rollbackAdmission(admission liveRunAdmission) *liveRunCompletionToken {
	c.mu.Lock()
	group := admission.group
	if group == nil || group.phase != liveRunPhaseOpen {
		c.mu.Unlock()
		return nil
	}
	if c.current != group && group.status != RunStatusFailed && group.status != RunStatusInterrupted {
		c.mu.Unlock()
		return nil
	}
	if group.reservations > 0 {
		group.reservations--
	}
	token := c.prepareIfCompleteLocked(group)
	c.mu.Unlock()
	return token
}

func (c *liveRunCoordinator) completeQueueItems(ids map[runtimeids.QueueItemID]struct{}) *liveRunCompletionToken {
	c.mu.Lock()
	group := c.current
	if group == nil {
		c.mu.Unlock()
		return nil
	}
	if group.phase != liveRunPhaseOpen {
		c.mu.Unlock()
		return nil
	}
	for id := range ids {
		delete(group.taggedQueueItems, id)
		delete(group.publishingItems, id)
		delete(c.stoppedQueueItems, id)
	}
	if len(group.taggedQueueItems) == 0 {
		group.taggedQueueItems = nil
	}
	if len(group.publishingItems) == 0 {
		group.publishingItems = nil
	}
	token := c.prepareIfCompleteLocked(group)
	c.mu.Unlock()
	return token
}

func (c *liveRunCoordinator) interrupt() (bool, map[runtimeids.QueueItemID]struct{}, bool, *liveRunCompletionToken) {
	c.queueFlushCommitMu.Lock()
	defer c.queueFlushCommitMu.Unlock()
	c.mu.Lock()
	group := c.current
	if group == nil {
		c.mu.Unlock()
		return false, nil, false, nil
	}
	if group.phase != liveRunPhaseOpen {
		c.mu.Unlock()
		return false, nil, false, nil
	}
	if group.status == RunStatusFailed || group.status == RunStatusInterrupted {
		c.mu.Unlock()
		return false, nil, false, nil
	}
	goalLoop := group.goalLoop
	ids := cloneMapIfNonEmpty(group.taggedQueueItems)
	c.markStoppedQueueItemsLocked(ids)
	for id := range group.publishingItems {
		delete(ids, id)
		if c.stoppedPublishingQueueItems == nil {
			c.stoppedPublishingQueueItems = make(map[runtimeids.QueueItemID]struct{})
		}
		c.stoppedPublishingQueueItems[id] = struct{}{}
	}
	group.status = RunStatusInterrupted
	group.err = context.Canceled
	group.resultKind = LiveRunResultInterrupted
	group.resultKindSet = true
	group.noFinalReason = LiveRunNoFinalAnswerReasonUnknown
	group.failureDiagnostic = nil
	group.finishedAt = time.Now().UTC()
	group.taggedQueueItems = nil
	token := c.prepareIfCompleteLocked(group)
	c.mu.Unlock()
	return true, ids, goalLoop, token
}

func (c *liveRunCoordinator) clearStoppedQueueItems(ids map[runtimeids.QueueItemID]struct{}) {
	if c == nil || len(ids) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for id := range ids {
		delete(c.stoppedQueueItems, id)
	}
	if len(c.stoppedQueueItems) == 0 {
		c.stoppedQueueItems = nil
	}
}

func (c *liveRunCoordinator) takeStoppedQueueItems(ids map[runtimeids.QueueItemID]struct{}) map[runtimeids.QueueItemID]struct{} {
	if c == nil || len(ids) == 0 {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[runtimeids.QueueItemID]struct{}{}
	for id := range ids {
		if _, stopped := c.stoppedQueueItems[id]; stopped {
			out[id] = struct{}{}
			delete(c.stoppedQueueItems, id)
		}
	}
	if len(c.stoppedQueueItems) == 0 {
		c.stoppedQueueItems = nil
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (c *liveRunCoordinator) commitQueueItemsUnlessStopped(ids map[runtimeids.QueueItemID]struct{}, commit func() error) (bool, error) {
	if c == nil {
		if commit == nil {
			return true, nil
		}
		return true, commit()
	}
	c.queueFlushCommitMu.Lock()
	defer c.queueFlushCommitMu.Unlock()
	c.mu.Lock()
	stopped := map[runtimeids.QueueItemID]struct{}{}
	for id := range ids {
		if _, ok := c.stoppedQueueItems[id]; ok {
			stopped[id] = struct{}{}
		}
	}
	if len(stopped) > 0 {
		for id := range stopped {
			delete(c.stoppedQueueItems, id)
		}
		if len(c.stoppedQueueItems) == 0 {
			c.stoppedQueueItems = nil
		}
		c.mu.Unlock()
		return false, nil
	}
	if commit == nil {
		c.mu.Unlock()
		return true, nil
	}
	c.mu.Unlock()
	err := commit()
	return true, err
}

func (c *liveRunCoordinator) markStoppedQueueItemsLocked(ids map[runtimeids.QueueItemID]struct{}) {
	if len(ids) == 0 {
		return
	}
	if c.stoppedQueueItems == nil {
		c.stoppedQueueItems = make(map[runtimeids.QueueItemID]struct{}, len(ids))
	}
	for id := range ids {
		c.stoppedQueueItems[id] = struct{}{}
	}
}

func (c *liveRunCoordinator) captureWait(ctx context.Context) (*LiveRunWaitHandle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	group := c.current
	if group == nil {
		c.mu.Unlock()
		return nil, ErrNoActiveLiveRun
	}
	group.waiters++
	done := group.done
	c.mu.Unlock()
	return &LiveRunWaitHandle{coordinator: c, group: group, done: done, ctx: ctx}, nil
}

func (h *LiveRunWaitHandle) Wait() (LiveRunResult, error) {
	if h == nil || h.coordinator == nil || h.group == nil || h.done == nil {
		return LiveRunResult{}, ErrNoActiveLiveRun
	}
	ctx := h.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-h.done:
	case <-ctx.Done():
		h.coordinator.mu.Lock()
		if h.group.waiters > 0 {
			h.group.waiters--
		}
		h.coordinator.mu.Unlock()
		return LiveRunResult{}, ctx.Err()
	}

	h.coordinator.mu.Lock()
	defer h.coordinator.mu.Unlock()
	if h.group.waiters > 0 {
		h.group.waiters--
	}
	if h.group.frozenResult == nil {
		panic(fmt.Sprintf("live run waiter released without frozen result: group_id=%s phase=%d", h.group.id.String(), h.group.phase))
	}
	result := cloneLiveRunResult(*h.group.frozenResult)
	return result, liveRunResultError(result)
}

func liveRunNoFinalAnswerReason(kind ActiveKind) LiveRunNoFinalAnswerReason {
	switch kind {
	case ActiveKindGoalLoop:
		return LiveRunNoFinalAnswerReasonGoalLoop
	case ActiveKindUserShell:
		return LiveRunNoFinalAnswerReasonUserShell
	case ActiveKindWorkflowTurn:
		return LiveRunNoFinalAnswerReasonWorkflow
	case ActiveKindBackground:
		return LiveRunNoFinalAnswerReasonBackground
	default:
		return LiveRunNoFinalAnswerReasonUnknown
	}
}

func liveRunResultKindForNoFinalAnswer(status RunStatus, reason LiveRunNoFinalAnswerReason) LiveRunResultKind {
	switch status {
	case RunStatusFailed:
		return LiveRunResultRuntimeFailure
	case RunStatusInterrupted:
		return LiveRunResultInterrupted
	}
	switch reason {
	case LiveRunNoFinalAnswerReasonWorkflow:
		return LiveRunResultWorkflowCompleted
	case LiveRunNoFinalAnswerReasonBackground, LiveRunNoFinalAnswerReasonUserShell, LiveRunNoFinalAnswerReasonGoalLoop:
		return LiveRunResultNonTaskActivity
	default:
		return LiveRunResultCompletedNoFinal
	}
}

func cloneLiveRunResult(result LiveRunResult) LiveRunResult {
	result.AssistantMessage = cloneLLMMessage(result.AssistantMessage)
	result.FailureDiagnostic = cloneLiveRunFailureDiagnostic(result.FailureDiagnostic)
	return result
}

func liveRunFailureDiagnostic(status RunStatus, err error) *LiveRunFailureDiagnostic {
	if status != RunStatusFailed {
		return nil
	}
	if err == nil {
		panic("failed live run has no runtime diagnostic")
	}
	return &LiveRunFailureDiagnostic{
		Code:   LiveRunFailureCodeRuntime,
		Detail: err.Error(),
	}
}

func cloneLiveRunFailureDiagnostic(diagnostic *LiveRunFailureDiagnostic) *LiveRunFailureDiagnostic {
	if diagnostic == nil {
		return nil
	}
	cloned := *diagnostic
	return &cloned
}

func mustRunID(raw string) runtimeids.RunID {
	id, err := runtimeids.ParseRunID(raw)
	if err != nil {
		panic(fmt.Sprintf("runtime generated invalid run id %q: %v", raw, err))
	}
	return id
}

func mustStepID(raw string) runtimeids.StepID {
	id, err := runtimeids.ParseStepID(raw)
	if err != nil {
		panic(fmt.Sprintf("runtime generated invalid step id %q: %v", raw, err))
	}
	return id
}

func mustQueueItemID(raw string) runtimeids.QueueItemID {
	id, err := runtimeids.ParseQueueItemID(raw)
	if err != nil {
		panic(fmt.Sprintf("runtime generated invalid queue item id %q: %v", raw, err))
	}
	return id
}

func typedQueueItemIDSet(ids map[string]struct{}) map[runtimeids.QueueItemID]struct{} {
	if len(ids) == 0 {
		return nil
	}
	typed := make(map[runtimeids.QueueItemID]struct{}, len(ids))
	for raw := range ids {
		typed[mustQueueItemID(raw)] = struct{}{}
	}
	return typed
}

func stringQueueItemIDSet(ids map[runtimeids.QueueItemID]struct{}) map[string]struct{} {
	if len(ids) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(ids))
	for id := range ids {
		out[id.String()] = struct{}{}
	}
	return out
}

func liveRunResultError(result LiveRunResult) error {
	if result.Error != nil {
		return result.Error
	}
	if result.ResultKind != LiveRunResultAssistantFinalAnswer {
		return ErrLiveRunNoFinalAnswer
	}
	return nil
}
