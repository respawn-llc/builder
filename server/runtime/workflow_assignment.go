package runtime

import (
	"context"
	"errors"
	"fmt"
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
	done chan struct{}
	once sync.Once
	err  error
}

type queuedWorkflowAssignment struct {
	intent steeringIntent
	steer  WorkflowAssignmentSteer
}

func newWorkflowAssignmentSteer() WorkflowAssignmentSteer {
	return WorkflowAssignmentSteer{state: &workflowAssignmentSteerState{done: make(chan struct{})}}
}

func CompletedWorkflowAssignmentSteer(err error) WorkflowAssignmentSteer {
	steer := newWorkflowAssignmentSteer()
	steer.complete(err)
	return steer
}

func (s WorkflowAssignmentSteer) Wait(ctx context.Context) error {
	if s.state == nil {
		return errors.New("workflow assignment steer is uninitialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-s.state.done:
		return s.state.err
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (s WorkflowAssignmentSteer) complete(err error) {
	if s.state == nil {
		return
	}
	s.state.once.Do(func() {
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
		steer.complete(err)
		return steer, err
	}
	if active {
		return steer, nil
	}
	receipt, err := e.steerWithCommitReceipt("", intent)
	if err == nil && !receipt.Committed {
		err = errors.New("workflow assignment message was not committed")
	}
	steer.complete(err)
	return steer, err
}

func SteerPersistedWorkflowAssignment(store *session.Store, assignment WorkflowAssignment) (WorkflowAssignmentSteer, error) {
	if store == nil {
		return WorkflowAssignmentSteer{}, errors.New("session store is required")
	}
	message, err := buildWorkflowAssignmentMessage(assignment)
	if err != nil {
		return WorkflowAssignmentSteer{}, err
	}
	record, err := sessionMessageRecordFromLLM(message)
	if err != nil {
		return WorkflowAssignmentSteer{}, fmt.Errorf("adapt workflow assignment message: %w", err)
	}
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		return WorkflowAssignmentSteer{}, err
	}
	_, receipt, err := eventLog.AppendRecord(nil, record)
	if err != nil {
		return WorkflowAssignmentSteer{}, err
	}
	if !receipt.Committed {
		return WorkflowAssignmentSteer{}, errors.New("workflow assignment message was not committed")
	}
	return CompletedWorkflowAssignmentSteer(nil), nil
}

func (e *Engine) flushPendingWorkflowAssignments(stepID string) error {
	e.workflowAssignmentMu.Lock()
	pending := append([]queuedWorkflowAssignment(nil), e.pendingWorkflowAssignments...)
	e.pendingWorkflowAssignments = nil
	e.workflowAssignmentMu.Unlock()
	for index, assignment := range pending {
		receipt, err := e.steerWithCommitReceipt(stepID, assignment.intent)
		if err == nil && !receipt.Committed {
			err = errors.New("workflow assignment message was not committed")
		}
		assignment.steer.complete(err)
		if err != nil {
			for _, remaining := range pending[index+1:] {
				remaining.steer.complete(err)
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
		assignment.steer.complete(err)
	}
}
