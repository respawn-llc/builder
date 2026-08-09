package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"core/server/llm"
	"core/server/runtimecommand"
	"core/shared/runtimeids"
	"core/shared/textutil"
)

var ErrNoActiveLiveRun = errors.New("no active live run")

var ErrLiveRunNoFinalAnswer = errors.New("live run completed without a final answer")

type LiveRunResultKind string
type LiveRunNoFinalAnswerReason string
type liveStepToolStartCount uint8

const (
	LiveRunResultAssistantFinalAnswer LiveRunResultKind = "assistant_final_answer"
	LiveRunResultNoFinalAnswer        LiveRunResultKind = "no_final_answer"

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

type LiveRunResult struct {
	GroupID          runtimeids.LiveRunGroupID
	RunID            runtimeids.RunID
	StepID           runtimeids.StepID
	Status           RunStatus
	WorkPerformed    bool
	ResultKind       LiveRunResultKind
	NoFinalReason    LiveRunNoFinalAnswerReason
	AssistantMessage llm.Message
	Error            error
	StartedAt        time.Time
	FinishedAt       time.Time
}

type LiveRunWaitHandle struct {
	coordinator *liveRunCoordinator
	group       *liveRunGroup
	done        <-chan struct{}
	ctx         context.Context
}

type liveRunCoordinator struct {
	mu          sync.Mutex
	current     *liveRunGroup
	onCompleted func(LiveRunResult)
}

type liveRunGroup struct {
	id               runtimeids.LiveRunGroupID
	runID            runtimeids.RunID
	stepID           runtimeids.StepID
	stepToolStarts   liveStepToolStartCount
	workPerformed    bool
	goalLoop         bool
	status           RunStatus
	resultKind       LiveRunResultKind
	resultKindSet    bool
	noFinalReason    LiveRunNoFinalAnswerReason
	assistantMessage llm.Message
	err              error
	startedAt        time.Time
	finishedAt       time.Time
	done             chan struct{}
	goalLoopHolding  bool
	waiters          int
}

func newLiveRunCoordinator(onCompleted ...func(LiveRunResult)) *liveRunCoordinator {
	coordinator := &liveRunCoordinator{}
	if len(onCompleted) > 0 {
		coordinator.onCompleted = onCompleted[0]
	}
	return coordinator
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
	if snapshot == nil || !activeKindInterruptibleByLiveStop(snapshot.ActiveKind) {
		return false, nil
	}
	var stoppedScope runtimeids.ExecutionScopeID
	if lifecycle, ok := e.cfg.StepLifecycle.(AgentStepScopeLifecycle); ok {
		stoppedScope, _ = lifecycle.CurrentAgentExecutionScope(context.Background())
	}
	interrupted, goalLoop := e.liveRun.interrupt()
	tracker := goalLoopInterruptTracker{engine: e, match: goalLoop || !interrupted}
	var interruptedSnapshot *RunSnapshot
	var err error
	if lifecycle, ok := e.stepLifecycle.(*defaultExclusiveStepLifecycle); ok {
		interruptedSnapshot, err = lifecycle.cancelCurrent(tracker.onSnapshot)
	} else {
		interruptedSnapshot, err = e.stepLifecycle.InterruptCurrent(tracker.onSnapshot)
	}
	tracker.resolve(err, interruptedSnapshot)
	if err != nil {
		return interrupted || interruptedSnapshot != nil, err
	}
	stopped := interrupted || interruptedSnapshot != nil
	if stopped {
		if err := e.submitStoppedScopeDisposition(stoppedScope); err != nil {
			return stopped, err
		}
	}
	if goalLoop && !tracker.pending && e.goalActive() {
		e.goalLoopState().Suspend()
	}
	return stopped, nil
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

func (e *Engine) QueueUserMessageForActiveRun(ctx context.Context, text string, beforeQueue func() error) (QueuedUserMessage, bool, error) {
	return e.queueMessageForActiveRun(ctx, llm.Message{Role: llm.RoleUser, Content: textutil.Value(text)}, beforeQueue)
}

func (e *Engine) QueueAgentSteerForActiveRun(ctx context.Context, steer AgentSteer, beforeQueue func() error) (QueuedUserMessage, bool, error) {
	return e.queueMessageForActiveRun(ctx, steer.Message(), beforeQueue)
}

func (e *Engine) queueMessageForActiveRun(ctx context.Context, message llm.Message, beforeQueue func() error) (QueuedUserMessage, bool, error) {
	if e == nil {
		return QueuedUserMessage{}, false, ErrNoActiveLiveRun
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return QueuedUserMessage{}, false, err
	}
	if message.Content == nil || strings.TrimSpace(*message.Content) == "" {
		return QueuedUserMessage{}, false, errors.New("empty message")
	}
	e.ensureOrchestrationCollaborators()
	if !e.liveRun.hasActive() {
		return QueuedUserMessage{}, false, ErrNoActiveLiveRun
	}
	if beforeQueue != nil {
		if err := beforeQueue(); err != nil {
			return QueuedUserMessage{}, false, err
		}
	}
	if err := ctx.Err(); err != nil {
		return QueuedUserMessage{}, false, err
	}
	item, err := newQueuedUserMessage(message)
	if err != nil {
		return QueuedUserMessage{}, false, err
	}
	queuedItem, queueErr := e.acceptHumanAgendaItem(item, boundaryEligibilityStep, true)
	if queueErr != nil {
		return QueuedUserMessage{}, false, queueErr
	}
	return queuedItem, true, nil
}

func (e *Engine) beginLiveRunStep(snapshot *RunSnapshot) {
	if e == nil || snapshot == nil {
		return
	}
	e.ensureOrchestrationCollaborators()
	e.liveRun.beginStep(snapshot)
}

func (e *Engine) finishLiveRunStep(snapshot *RunSnapshot, status RunStatus, err error) func() {
	if e == nil || snapshot == nil {
		return nil
	}
	e.ensureOrchestrationCollaborators()
	result := e.liveRun.finishStepDeferred(snapshot, status, err, e.shouldHoldLiveRunForGoalLoopContinuation(snapshot, status))
	if result == nil {
		return nil
	}
	return func() {
		e.liveRun.publishCompleted(*result)
	}
}

func (e *Engine) finishLiveRunGoalLoop() {
	if e == nil {
		return
	}
	e.ensureOrchestrationCollaborators()
	e.liveRun.finishGoalLoop()
}

func (e *Engine) recordLiveRunAssistantFinalAnswer(stepID string, message llm.Message) {
	if e == nil {
		return
	}
	e.ensureOrchestrationCollaborators()
	e.liveRun.recordAssistantFinalAnswer(stepID, message)
}

func (e *Engine) submitStoppedScopeDisposition(stopped runtimeids.ExecutionScopeID) error {
	if e.runtimeEvents == nil {
		steerErr := e.applySteeringBatch("", steerMessagesWithPersistenceIntent(
			steeringPriorityNormal,
			steeringMessageEventDefault,
			true,
			[]llm.Message{{
				Role:        llm.RoleDeveloper,
				MessageType: textutil.Value(llm.MessageTypeInterruption),
				Content:     textutil.Value(interruptMessage),
			}},
		))
		e.invalidateAgentStepScope(stopped, errBoundaryScopeStopped)
		return steerErr
	}
	_, err := runtimecommand.Submit(
		e.lifecycleCtx,
		e.runtimeEvents,
		stopped,
		func(
			command runtimecommand.Admission,
			scope runtimeids.ExecutionScopeID,
			complete func(struct{}, error),
		) error {
			steerErr := runtimeEventAdmission{engine: e, command: command}.applySteering(
				"",
				steerMessagesWithPersistenceIntent(
					steeringPriorityNormal,
					steeringMessageEventDefault,
					true,
					[]llm.Message{{
						Role:        llm.RoleDeveloper,
						MessageType: textutil.Value(llm.MessageTypeInterruption),
						Content:     textutil.Value(interruptMessage),
					}},
				),
			)
			for _, item := range e.boundaryAgenda.takeHumanScope(scope) {
				item.settleBoundaryAgenda(errBoundaryScopeStopped)
			}
			e.invalidateAgentStepScope(scope, errBoundaryScopeStopped)
			complete(struct{}{}, steerErr)
			return nil
		},
	)
	return runtimeSteeringError(err)
}

func (c *liveRunCoordinator) hasActive() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current != nil
}

func (c *liveRunCoordinator) beginStep(snapshot *RunSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current != nil {
		c.current.runID = mustRunID(snapshot.RunID)
		c.current.stepID = mustStepID(snapshot.StepID)
		c.current.stepToolStarts = liveStepToolStartsNone
		c.current.goalLoop = snapshot.GoalLoop
		c.current.status = RunStatusRunning
		c.current.resultKindSet = false
		c.current.noFinalReason = LiveRunNoFinalAnswerReasonUnknown
		c.current.assistantMessage = llm.Message{}
		c.current.err = nil
		c.current.finishedAt = time.Time{}
		return
	}
	c.current = &liveRunGroup{
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

func (c *liveRunCoordinator) finishStep(snapshot *RunSnapshot, status RunStatus, err error, holdGoalLoop bool) {
	result := c.finishStepDeferred(snapshot, status, err, holdGoalLoop)
	if result != nil {
		c.publishCompleted(*result)
	}
}

func (c *liveRunCoordinator) finishStepDeferred(snapshot *RunSnapshot, status RunStatus, err error, holdGoalLoop bool) *LiveRunResult {
	c.mu.Lock()
	group := c.current
	if group == nil {
		c.mu.Unlock()
		return nil
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
	if !group.resultKindSet {
		group.resultKind = LiveRunResultNoFinalAnswer
		group.resultKindSet = true
		group.noFinalReason = liveRunNoFinalAnswerReason(snapshot.ActiveKind)
	}
	var done chan struct{}
	if status == RunStatusFailed || status == RunStatusInterrupted {
		c.current = nil
		done = group.done
		result := liveRunResultForGroup(group)
		c.mu.Unlock()
		close(done)
		return &result
	}
	group.goalLoopHolding = snapshot.GoalLoop && holdGoalLoop
	if !group.goalLoopHolding {
		c.current = nil
		done = group.done
	}
	c.mu.Unlock()
	if done != nil {
		close(done)
		result := liveRunResultForGroup(group)
		return &result
	}
	return nil
}

func (c *liveRunCoordinator) finishGoalLoop() {
	c.mu.Lock()
	group := c.current
	if group == nil {
		c.mu.Unlock()
		return
	}
	var done chan struct{}
	group.goalLoopHolding = false
	if group.status != RunStatusRunning {
		c.current = nil
		done = group.done
	}
	c.mu.Unlock()
	if done != nil {
		close(done)
		c.publishCompleted(liveRunResultForGroup(group))
	}
}

func (c *liveRunCoordinator) recordToolStart(stepID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current == nil || c.current.stepID != mustStepID(stepID) {
		return
	}
	if c.current.stepToolStarts < liveStepToolStartsMultiple {
		c.current.stepToolStarts++
	}
}

func (c *liveRunCoordinator) recordAssistantFinalAnswer(stepID string, message llm.Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current == nil || c.current.stepID != mustStepID(stepID) {
		return
	}
	if c.current.goalLoop {
		return
	}
	c.current.resultKind = LiveRunResultAssistantFinalAnswer
	c.current.resultKindSet = true
	c.current.err = nil
	c.current.assistantMessage = message
}

func (c *liveRunCoordinator) interrupt() (bool, bool) {
	c.mu.Lock()
	group := c.current
	if group == nil {
		c.mu.Unlock()
		return false, false
	}
	goalLoop := group.goalLoop
	group.status = RunStatusInterrupted
	group.err = context.Canceled
	group.resultKind = LiveRunResultNoFinalAnswer
	group.resultKindSet = true
	group.noFinalReason = LiveRunNoFinalAnswerReasonUnknown
	group.finishedAt = time.Now().UTC()
	c.current = nil
	done := group.done
	c.mu.Unlock()
	close(done)
	c.publishCompleted(liveRunResultForGroup(group))
	return true, goalLoop
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
	return LiveRunResult{
		GroupID:          h.group.id,
		RunID:            h.group.runID,
		StepID:           h.group.stepID,
		Status:           h.group.status,
		WorkPerformed:    h.group.workPerformed,
		ResultKind:       h.group.resultKind,
		NoFinalReason:    h.group.noFinalReason,
		AssistantMessage: h.group.assistantMessage,
		Error:            h.group.err,
		StartedAt:        h.group.startedAt,
		FinishedAt:       h.group.finishedAt,
	}, liveRunResultError(h.group)
}

func liveRunResultForGroup(group *liveRunGroup) LiveRunResult {
	return LiveRunResult{
		GroupID:          group.id,
		RunID:            group.runID,
		StepID:           group.stepID,
		Status:           group.status,
		WorkPerformed:    group.workPerformed,
		ResultKind:       group.resultKind,
		NoFinalReason:    group.noFinalReason,
		AssistantMessage: group.assistantMessage,
		Error:            group.err,
		StartedAt:        group.startedAt,
		FinishedAt:       group.finishedAt,
	}
}

func (c *liveRunCoordinator) publishCompleted(result LiveRunResult) {
	if c.onCompleted != nil {
		c.onCompleted(result)
	}
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

func liveRunResultError(group *liveRunGroup) error {
	if group.err != nil {
		return group.err
	}
	if group.resultKind == LiveRunResultNoFinalAnswer {
		return ErrLiveRunNoFinalAnswer
	}
	return nil
}
