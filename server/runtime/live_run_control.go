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

type LiveRunResultKind string
type LiveRunNoFinalAnswerReason string

const (
	LiveRunResultAssistantFinalAnswer LiveRunResultKind = "assistant_final_answer"
	LiveRunResultNoFinalAnswer        LiveRunResultKind = "no_final_answer"

	LiveRunNoFinalAnswerReasonGoalLoop   LiveRunNoFinalAnswerReason = "goal_loop"
	LiveRunNoFinalAnswerReasonUserShell  LiveRunNoFinalAnswerReason = "shell_command"
	LiveRunNoFinalAnswerReasonWorkflow   LiveRunNoFinalAnswerReason = "workflow_completion"
	LiveRunNoFinalAnswerReasonBackground LiveRunNoFinalAnswerReason = "background_notice"
	LiveRunNoFinalAnswerReasonUnknown    LiveRunNoFinalAnswerReason = "unknown"
)

type LiveRunResult struct {
	GroupID          runtimeids.LiveRunGroupID
	RunID            runtimeids.RunID
	StepID           runtimeids.StepID
	Status           RunStatus
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
	mu                          sync.Mutex
	queueFlushCommitMu          sync.Mutex
	current                     *liveRunGroup
	stoppedQueueItems           map[runtimeids.QueueItemID]struct{}
	stoppedPublishingQueueItems map[runtimeids.QueueItemID]struct{}
}

type liveRunGroup struct {
	id               runtimeids.LiveRunGroupID
	runID            runtimeids.RunID
	stepID           runtimeids.StepID
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
	reservations     int
	taggedQueueItems map[runtimeids.QueueItemID]struct{}
	publishingItems  map[runtimeids.QueueItemID]struct{}
	goalLoopHolding  bool
	waiters          int
}

type liveRunAdmission struct {
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
	interrupted, taggedQueueItems, goalLoop := e.liveRun.interrupt()
	if !interrupted {
		if snapshot == nil || !activeKindInterruptibleByLiveStop(snapshot.ActiveKind) {
			return false, nil
		}
		goalLoopInterruptPending := false
		interruptedSnapshot, err := e.stepLifecycle.InterruptCurrent(func(snapshot *RunSnapshot) {
			if e.goalActive() && snapshot != nil && snapshot.ActiveKind == ActiveKindGoalLoop {
				e.goalLoopState().MarkInterruptPending()
				goalLoopInterruptPending = true
			}
		})
		if err != nil {
			if goalLoopInterruptPending {
				e.goalLoopState().ClearInterruptPending()
			}
			return interruptedSnapshot != nil, err
		}
		if goalLoopInterruptPending {
			if e.goalActive() && interruptedSnapshot != nil && interruptedSnapshot.ActiveKind == ActiveKindGoalLoop {
				e.goalLoopState().CommitInterrupt()
			} else {
				e.goalLoopState().ClearInterruptPending()
			}
		}
		return interruptedSnapshot != nil, err
	}
	e.failStoppedLiveRunQueueItems(taggedQueueItems)
	if snapshot == nil || !activeKindInterruptibleByLiveStop(snapshot.ActiveKind) {
		return true, nil
	}
	goalLoopInterruptPending := false
	snapshot, err := e.stepLifecycle.InterruptCurrent(func(snapshot *RunSnapshot) {
		if goalLoop && e.goalActive() && snapshot != nil && snapshot.ActiveKind == ActiveKindGoalLoop {
			e.goalLoopState().MarkInterruptPending()
			goalLoopInterruptPending = true
		}
	})
	if err != nil {
		if goalLoopInterruptPending {
			e.goalLoopState().ClearInterruptPending()
		}
		return true, err
	}
	if goalLoop {
		switch {
		case goalLoopInterruptPending && e.goalActive() && snapshot != nil && snapshot.ActiveKind == ActiveKindGoalLoop:
			e.goalLoopState().CommitInterrupt()
		case goalLoopInterruptPending:
			e.goalLoopState().ClearInterruptPending()
		case e.goalActive():
			e.goalLoopState().Suspend()
		}
	}
	return true, nil
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
	admission, admitted := e.liveRun.beginAdmission()
	if !admitted {
		return QueuedUserMessage{}, false, ErrNoActiveLiveRun
	}
	committed := false
	defer func() {
		if !committed {
			e.liveRun.rollbackAdmission(admission)
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
	finalized := e.liveRun.finishAdmission(admission, mustQueueItemID(item.ID), func(queueItemID string) {
		e.markQueuedUserInjectionForAutoDrain(queueItemID)
	})
	if !finalized {
		return QueuedUserMessage{}, false, context.Canceled
	}
	committed = true
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
	stoppedQueueItems := e.liveRun.finishStep(snapshot, status, err, e.shouldHoldLiveRunForGoalLoopContinuation(snapshot, status))
	e.failStoppedLiveRunQueueItems(stoppedQueueItems)
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

func (e *Engine) completeLiveRunQueueItems(ids map[string]struct{}) {
	if e == nil || len(ids) == 0 {
		return
	}
	e.ensureOrchestrationCollaborators()
	e.liveRun.completeQueueItems(typedQueueItemIDSet(ids))
}

func (e *Engine) tagLiveRunQueueItem(queueItemID string) bool {
	if e == nil {
		return false
	}
	e.ensureOrchestrationCollaborators()
	return e.liveRun.tagQueueItem(mustQueueItemID(queueItemID))
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

func (c *liveRunCoordinator) acceptsQueueItemPublication() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current == nil {
		return false
	}
	return c.current.status == RunStatusRunning || c.current.status == RunStatusCompleted
}

func (c *liveRunCoordinator) hasPendingStopTarget() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	group := c.current
	return group != nil && (len(group.taggedQueueItems) > 0 || len(group.publishingItems) > 0 || group.reservations > 0 || group.goalLoopHolding)
}

func (c *liveRunCoordinator) beginStep(snapshot *RunSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current != nil {
		c.current.runID = mustRunID(snapshot.RunID)
		c.current.stepID = mustStepID(snapshot.StepID)
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

func (c *liveRunCoordinator) finishStep(snapshot *RunSnapshot, status RunStatus, err error, holdGoalLoop bool) map[runtimeids.QueueItemID]struct{} {
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
	if !group.resultKindSet {
		group.resultKind = LiveRunResultNoFinalAnswer
		group.resultKindSet = true
		group.noFinalReason = liveRunNoFinalAnswerReason(snapshot.ActiveKind)
	}
	var stoppedQueueItems map[runtimeids.QueueItemID]struct{}
	var done chan struct{}
	if status == RunStatusFailed || status == RunStatusInterrupted {
		stoppedQueueItems = cloneTypedQueueItemIDSet(group.taggedQueueItems)
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
		group.reservations = 0
		c.current = nil
		done = group.done
		c.mu.Unlock()
		close(done)
		return stoppedQueueItems
	}
	group.goalLoopHolding = snapshot.GoalLoop && holdGoalLoop
	if group.reservations == 0 && len(group.taggedQueueItems) == 0 && !group.goalLoopHolding {
		c.current = nil
		done = group.done
	}
	c.mu.Unlock()
	if done != nil {
		close(done)
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
	if group.status != RunStatusRunning && group.reservations == 0 && len(group.taggedQueueItems) == 0 {
		c.current = nil
		done = group.done
	}
	c.mu.Unlock()
	if done != nil {
		close(done)
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

func (c *liveRunCoordinator) beginAdmission() (liveRunAdmission, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current == nil {
		return liveRunAdmission{}, false
	}
	if c.current.status == RunStatusFailed || c.current.status == RunStatusInterrupted {
		return liveRunAdmission{}, false
	}
	c.current.reservations++
	return liveRunAdmission{group: c.current}, true
}

func (c *liveRunCoordinator) finishAdmission(admission liveRunAdmission, queueItemID runtimeids.QueueItemID, markAutoDrain func(string)) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current == nil || c.current != admission.group || c.current.status == RunStatusFailed || c.current.status == RunStatusInterrupted {
		return false
	}
	if c.current.reservations > 0 {
		c.current.reservations--
	}
	c.current.trackQueuedItemForLiveRun(queueItemID)
	if markAutoDrain != nil {
		markAutoDrain(queueItemID.String())
	}
	return true
}

func (c *liveRunCoordinator) tagQueueItem(queueItemID runtimeids.QueueItemID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current == nil {
		return false
	}
	c.current.trackQueuedItemForLiveRun(queueItemID)
	return true
}

func (c *liveRunCoordinator) beginQueueItemPublication(queueItemID runtimeids.QueueItemID, markAutoDrain func(string)) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current == nil || (c.current.status != RunStatusRunning && c.current.status != RunStatusCompleted) {
		return false
	}
	c.current.trackQueuedItemForLiveRun(queueItemID)
	if markAutoDrain != nil {
		markAutoDrain(queueItemID.String())
	}
	return true
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

func (c *liveRunCoordinator) rollbackAdmission(admission liveRunAdmission) {
	c.mu.Lock()
	group := c.current
	if group == nil || group != admission.group {
		c.mu.Unlock()
		return
	}
	var done chan struct{}
	if group.reservations > 0 {
		group.reservations--
	}
	if group.status != RunStatusRunning && group.reservations == 0 && len(group.taggedQueueItems) == 0 && !group.goalLoopHolding {
		c.current = nil
		done = group.done
	}
	c.mu.Unlock()
	if done != nil {
		close(done)
	}
}

func (c *liveRunCoordinator) completeQueueItems(ids map[runtimeids.QueueItemID]struct{}) {
	c.mu.Lock()
	group := c.current
	if group == nil {
		c.mu.Unlock()
		return
	}
	var done chan struct{}
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
	if group.status != RunStatusRunning && group.reservations == 0 && len(group.taggedQueueItems) == 0 && !group.goalLoopHolding {
		c.current = nil
		done = group.done
	}
	c.mu.Unlock()
	if done != nil {
		close(done)
	}
}

func (c *liveRunCoordinator) interrupt() (bool, map[runtimeids.QueueItemID]struct{}, bool) {
	c.queueFlushCommitMu.Lock()
	defer c.queueFlushCommitMu.Unlock()
	c.mu.Lock()
	group := c.current
	if group == nil {
		c.mu.Unlock()
		return false, nil, false
	}
	goalLoop := group.goalLoop
	ids := cloneTypedQueueItemIDSet(group.taggedQueueItems)
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
	group.resultKind = LiveRunResultNoFinalAnswer
	group.resultKindSet = true
	group.noFinalReason = LiveRunNoFinalAnswerReasonUnknown
	group.finishedAt = time.Now().UTC()
	group.taggedQueueItems = nil
	group.reservations = 0
	c.current = nil
	done := group.done
	c.mu.Unlock()
	close(done)
	return true, ids, goalLoop
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
	return LiveRunResult{
		GroupID:          h.group.id,
		RunID:            h.group.runID,
		StepID:           h.group.stepID,
		Status:           h.group.status,
		ResultKind:       h.group.resultKind,
		NoFinalReason:    h.group.noFinalReason,
		AssistantMessage: h.group.assistantMessage,
		Error:            h.group.err,
		StartedAt:        h.group.startedAt,
		FinishedAt:       h.group.finishedAt,
	}, liveRunResultError(h.group)
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

func cloneTypedQueueItemIDSet(ids map[runtimeids.QueueItemID]struct{}) map[runtimeids.QueueItemID]struct{} {
	if len(ids) == 0 {
		return nil
	}
	out := make(map[runtimeids.QueueItemID]struct{}, len(ids))
	for id := range ids {
		out[id] = struct{}{}
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
