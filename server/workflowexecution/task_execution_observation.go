package workflowexecution

import (
	"context"
	"errors"
	"fmt"

	"core/server/metadata/sqlitegen"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowstore"
)

type WorkflowTaskLifecycleSnapshot struct {
	CurrentNodes       []workflow.CurrentNode
	QueuedCurrentNodes []workflow.CurrentNodeReference
	ExactExecutions    []workflowstore.LifecycleExactExecution
}

// WorkflowTaskExecutionObservation is materialized entirely from one pinned
// lifecycle root and its compatible durable snapshot.
type WorkflowTaskExecutionObservation struct {
	Executions map[workflow.TaskID]sessionruntime.TaskExecutionSnapshot
	Quiescence map[workflow.TaskID]bool
	Lifecycle  map[workflow.TaskID]WorkflowTaskLifecycleSnapshot
}

// ObserveWorkflowTaskExecutions captures the global published lifecycle and,
// when taskIDs are supplied, derives their quiescence from that same root.
func (c *CurrentNodeController) ObserveWorkflowTaskExecutions(taskIDs []workflow.TaskID) (WorkflowTaskExecutionObservation, error) {
	var observation WorkflowTaskExecutionObservation
	err := c.CaptureWorkflowTaskExecutions(
		context.Background(),
		taskIDs,
		func(captured WorkflowTaskExecutionObservation, _ *sqlitegen.Queries) error {
			observation = captured
			return nil
		},
	)
	return observation, err
}

func (c *CurrentNodeController) CaptureWorkflowTaskLifecycleQuery(
	ctx context.Context,
	operation func(string, *sqlitegen.Queries) error,
) error {
	if c == nil {
		return errors.New("current node workflow controller is required")
	}
	if operation == nil {
		return errors.New("workflow Task lifecycle query operation is required")
	}
	queryPublication, ok := c.publication.(interface {
		CaptureQuery(context.Context, func(string, *sqlitegen.Queries) error) error
	})
	if !ok {
		return errors.New("workflow lifecycle publication does not support bounded queries")
	}
	return queryPublication.CaptureQuery(ctx, operation)
}

func (c *CurrentNodeController) CaptureWorkflowTaskExecutions(
	ctx context.Context,
	taskIDs []workflow.TaskID,
	operation func(WorkflowTaskExecutionObservation, *sqlitegen.Queries) error,
) (err error) {
	if c == nil {
		return errors.New("current node workflow controller is required")
	}
	if operation == nil {
		return errors.New("workflow Task execution capture operation is required")
	}
	selected := make(map[workflow.TaskID]struct{}, len(taskIDs))
	for _, taskID := range taskIDs {
		if taskID == "" {
			return errors.New("workflow task id is required")
		}
		if _, exists := selected[taskID]; exists {
			return fmt.Errorf("workflow task id %q is duplicated", taskID)
		}
		selected[taskID] = struct{}{}
	}

	observation := WorkflowTaskExecutionObservation{
		Executions: map[workflow.TaskID]sessionruntime.TaskExecutionSnapshot{},
		Quiescence: map[workflow.TaskID]bool{},
		Lifecycle:  map[workflow.TaskID]WorkflowTaskLifecycleSnapshot{},
	}
	capture, err := c.publication.Capture(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := capture.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	for _, taskID := range capture.TaskIDs() {
		selected[taskID] = struct{}{}
		currentNodes, err := capture.CurrentNodes(ctx, taskID)
		if err != nil {
			return err
		}
		observation.Lifecycle[taskID] = WorkflowTaskLifecycleSnapshot{
			CurrentNodes:       currentNodes,
			QueuedCurrentNodes: capture.QueuedCurrentNodes(taskID),
			ExactExecutions:    capture.ExactExecutions(taskID),
		}
	}
	for taskID, lifecycle := range observation.Lifecycle {
		executions := sessionruntime.TaskExecutionSnapshot{}
		for _, exact := range lifecycle.ExactExecutions {
			execution, err := taskExecutionFromLifecycleExact(exact)
			if err != nil {
				return err
			}
			executions.Executions = append(executions.Executions, execution)
		}
		if len(executions.Executions) != 0 {
			observation.Executions[taskID] = executions
		}
	}
	for taskID := range selected {
		_, owned := observation.Lifecycle[taskID]
		observation.Quiescence[taskID] = !owned
	}
	return capture.WithQueries(func(queries *sqlitegen.Queries) error {
		return operation(observation, queries)
	})
}

func taskExecutionFromLifecycleExact(exact workflowstore.LifecycleExactExecution) (sessionruntime.TaskExecution, error) {
	target := sessionruntime.TaskExecution{
		Ref: sessionruntime.WorkflowExecutionRef{
			ProjectID:   exact.ProjectID,
			WorkflowID:  exact.WorkflowID,
			CurrentNode: exact.CurrentNode,
		},
		ScopeID: exact.ScopeID,
	}
	if exact.Agent != nil {
		target.Agent = &sessionruntime.TaskAgentExecutionTarget{SessionID: exact.Agent.SessionID}
	}
	if exact.Script != nil {
		target.Script = &sessionruntime.TaskScriptExecutionTarget{Path: exact.Script.Path}
	}
	for _, prompt := range exact.PendingPrompts {
		reference := sessionruntime.PendingPromptReference{ID: prompt.ID}
		switch prompt.Kind {
		case workflowstore.LifecyclePendingPromptQuestion:
			reference.Kind = sessionruntime.PendingPromptKindQuestion
		case workflowstore.LifecyclePendingPromptSessionApproval:
			reference.Kind = sessionruntime.PendingPromptKindSessionApproval
		default:
			return sessionruntime.TaskExecution{}, fmt.Errorf(
				"published Exact execution scope %s has invalid pending prompt kind",
				exact.ScopeID,
			)
		}
		target.PendingPrompts = append(target.PendingPrompts, reference)
	}
	switch exact.Phase {
	case workflowstore.LifecycleExactExecutionRunning,
		workflowstore.LifecycleExactExecutionFinalizing:
	default:
		return sessionruntime.TaskExecution{}, fmt.Errorf(
			"published Exact execution scope %s has invalid phase",
			exact.ScopeID,
		)
	}
	if err := target.Validate(); err != nil {
		return sessionruntime.TaskExecution{}, err
	}
	return target, nil
}
