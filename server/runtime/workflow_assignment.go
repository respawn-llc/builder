package runtime

import (
	"context"
	"errors"
	"sync"

	"core/server/llm"
	"core/server/session"
	"core/server/workflow"
	"core/server/workflowruntime"
)

type WorkflowAssignment struct {
	ContextMode    workflow.ContextMode
	CompletionMode workflowruntime.CompletionMode
	Prompt         workflowruntime.PromptContract
}

type WorkflowAssignmentSteer struct {
	state *workflowAssignmentSteerState
}

type workflowAssignmentSteerState struct {
	done    chan struct{}
	once    sync.Once
	receipt session.CommitReceipt
	err     error
}

type queuedWorkflowAssignment struct {
	intent steeringIntent
	steer  WorkflowAssignmentSteer
}

func newWorkflowAssignmentSteer() WorkflowAssignmentSteer {
	return WorkflowAssignmentSteer{state: &workflowAssignmentSteerState{done: make(chan struct{})}}
}

func CompletedWorkflowAssignmentSteer(receipt session.CommitReceipt, err error) WorkflowAssignmentSteer {
	steer := newWorkflowAssignmentSteer()
	steer.complete(receipt, err)
	return steer
}

func (s WorkflowAssignmentSteer) Wait(ctx context.Context) (session.CommitReceipt, error) {
	if s.state == nil {
		return session.CommitReceipt{}, errors.New("workflow assignment steer is uninitialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-s.state.done:
		return s.state.receipt, s.state.err
	case <-ctx.Done():
		return session.CommitReceipt{}, context.Cause(ctx)
	}
}

func (s WorkflowAssignmentSteer) complete(receipt session.CommitReceipt, err error) {
	if s.state == nil {
		return
	}
	if err == nil && !receipt.Committed {
		err = errors.New("workflow assignment message was not committed")
	}
	s.state.once.Do(func() {
		s.state.receipt = receipt
		s.state.err = err
		close(s.state.done)
	})
}

func (e *Engine) SteerWorkflowAssignment(assignment WorkflowAssignment) (WorkflowAssignmentSteer, error) {
	message, err := buildWorkflowAssignmentMessage(assignment)
	if err != nil {
		return WorkflowAssignmentSteer{}, err
	}
	steer := newWorkflowAssignmentSteer()
	intent := steerMessagesWithPersistenceIntent(
		steeringPriorityRuntimeContext,
		steeringMessageEventDefault,
		true,
		[]llm.Message{message},
	)
	e.ensureOrchestrationCollaborators()
	active, err := e.stepLifecycle.WithActiveStep(func(string) error {
		e.workflowAssignmentMu.Lock()
		defer e.workflowAssignmentMu.Unlock()
		if e.closed.Load() {
			return ErrEngineClosed
		}
		e.pendingWorkflowAssignments = append(e.pendingWorkflowAssignments, queuedWorkflowAssignment{
			intent: intent,
			steer:  steer,
		})
		return nil
	})
	if err != nil {
		steer.complete(session.CommitReceipt{}, err)
		return steer, err
	}
	if active {
		return steer, nil
	}
	receipt, err := e.steerWithCommitReceipt("", intent)
	steer.complete(receipt, err)
	return steer, nil
}

func SteerPersistedWorkflowAssignment(store *session.Store, assignment WorkflowAssignment) (WorkflowAssignmentSteer, error) {
	if store == nil {
		return WorkflowAssignmentSteer{}, errors.New("session store is required")
	}
	message, err := buildWorkflowAssignmentMessage(assignment)
	if err != nil {
		return WorkflowAssignmentSteer{}, err
	}
	receipt, err := SteerPersistedMessage(store, "", message)
	return CompletedWorkflowAssignmentSteer(receipt, err), nil
}

func (e *Engine) flushPendingWorkflowAssignments(stepID string) error {
	e.workflowAssignmentMu.Lock()
	pending := append([]queuedWorkflowAssignment(nil), e.pendingWorkflowAssignments...)
	e.pendingWorkflowAssignments = nil
	e.workflowAssignmentMu.Unlock()
	for index, assignment := range pending {
		receipt, err := e.steerWithCommitReceipt(stepID, assignment.intent)
		assignment.steer.complete(receipt, err)
		if err != nil {
			for _, remaining := range pending[index+1:] {
				remaining.steer.complete(session.CommitReceipt{}, err)
			}
			return err
		}
		if !receipt.Committed {
			err = errors.New("workflow assignment message was not committed")
			for _, remaining := range pending[index+1:] {
				remaining.steer.complete(session.CommitReceipt{}, err)
			}
			return err
		}
	}
	return nil
}

func (e *Engine) failPendingWorkflowAssignments(err error) {
	e.workflowAssignmentMu.Lock()
	pending := append([]queuedWorkflowAssignment(nil), e.pendingWorkflowAssignments...)
	e.pendingWorkflowAssignments = nil
	e.workflowAssignmentMu.Unlock()
	for _, assignment := range pending {
		assignment.steer.complete(session.CommitReceipt{}, err)
	}
}
