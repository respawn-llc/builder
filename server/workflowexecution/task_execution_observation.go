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

type WorkflowTaskLifecycleReader interface {
	ObserveSelected(context.Context, []workflow.TaskID) (WorkflowTaskExecutionObservation, error)
	PendingQuestions(
		context.Context,
		workflowstore.LifecycleQuestionCursor,
		int,
	) ([]workflowstore.LifecyclePendingQuestion, error)
	PendingQuestionsForTask(
		context.Context,
		workflow.TaskID,
	) ([]workflowstore.LifecyclePendingQuestion, error)
}

type workflowTaskLifecycleReader struct {
	capture workflowstore.LifecycleBoundedReadCapture
}

// ObserveWorkflowTaskExecutions captures either the selected Tasks or the
// global published lifecycle when no Task IDs are supplied.
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

func (c *CurrentNodeController) CaptureWorkflowTaskBoundedLifecycleRead(
	ctx context.Context,
	operation func(string, *sqlitegen.Queries, WorkflowTaskLifecycleReader) error,
) error {
	if c == nil {
		return errors.New("current node workflow controller is required")
	}
	if operation == nil {
		return errors.New("workflow Task bounded lifecycle read operation is required")
	}
	publication, ok := c.publication.(interface {
		CaptureBoundedRead(
			context.Context,
			func(string, workflowstore.LifecycleBoundedReadCapture, *sqlitegen.Queries) error,
		) error
	})
	if !ok {
		return errors.New("workflow lifecycle publication does not support bounded reads")
	}
	return publication.CaptureBoundedRead(
		ctx,
		func(
			token string,
			capture workflowstore.LifecycleBoundedReadCapture,
			queries *sqlitegen.Queries,
		) error {
			return operation(token, queries, workflowTaskLifecycleReader{capture: capture})
		},
	)
}

func (r workflowTaskLifecycleReader) ObserveSelected(
	ctx context.Context,
	taskIDs []workflow.TaskID,
) (WorkflowTaskExecutionObservation, error) {
	return workflowTaskExecutionObservationFromCapture(ctx, r.capture, taskIDs, taskIDs)
}

func (r workflowTaskLifecycleReader) PendingQuestions(
	ctx context.Context,
	cursor workflowstore.LifecycleQuestionCursor,
	limit int,
) ([]workflowstore.LifecyclePendingQuestion, error) {
	return r.capture.PendingQuestions(ctx, cursor, limit)
}

func (r workflowTaskLifecycleReader) PendingQuestionsForTask(
	ctx context.Context,
	taskID workflow.TaskID,
) ([]workflowstore.LifecyclePendingQuestion, error) {
	return r.capture.PendingQuestionsForTask(ctx, taskID)
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

	capture, err := c.publication.Capture(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := capture.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	lifecycleTaskIDs := append([]workflow.TaskID(nil), taskIDs...)
	if len(lifecycleTaskIDs) == 0 {
		lifecycleTaskIDs = capture.TaskIDs()
	}
	observation, err := workflowTaskExecutionObservationFromCapture(
		ctx,
		capture,
		lifecycleTaskIDs,
		lifecycleTaskIDs,
	)
	if err != nil {
		return err
	}
	return capture.WithQueries(func(queries *sqlitegen.Queries) error {
		return operation(observation, queries)
	})
}

type workflowTaskLifecycleCapture interface {
	CurrentNodes(context.Context, workflow.TaskID) ([]workflow.CurrentNode, error)
	QueuedCurrentNodes(workflow.TaskID) []workflow.CurrentNodeReference
	ExactExecutions(workflow.TaskID) []workflowstore.LifecycleExactExecution
}

func workflowTaskExecutionObservationFromCapture(
	ctx context.Context,
	capture workflowTaskLifecycleCapture,
	lifecycleTaskIDs []workflow.TaskID,
	quiescenceTaskIDs []workflow.TaskID,
) (WorkflowTaskExecutionObservation, error) {
	observation := WorkflowTaskExecutionObservation{
		Executions: map[workflow.TaskID]sessionruntime.TaskExecutionSnapshot{},
		Quiescence: map[workflow.TaskID]bool{},
		Lifecycle:  map[workflow.TaskID]WorkflowTaskLifecycleSnapshot{},
	}
	seenLifecycle := make(map[workflow.TaskID]struct{}, len(lifecycleTaskIDs))
	for _, taskID := range lifecycleTaskIDs {
		if taskID == "" {
			return WorkflowTaskExecutionObservation{}, errors.New("workflow task id is required")
		}
		if _, duplicate := seenLifecycle[taskID]; duplicate {
			return WorkflowTaskExecutionObservation{}, fmt.Errorf("workflow task id %q is duplicated", taskID)
		}
		seenLifecycle[taskID] = struct{}{}
		queued := capture.QueuedCurrentNodes(taskID)
		exact := capture.ExactExecutions(taskID)
		if len(queued) == 0 && len(exact) == 0 {
			continue
		}
		currentNodes, err := capture.CurrentNodes(ctx, taskID)
		if err != nil {
			return WorkflowTaskExecutionObservation{}, err
		}
		observation.Lifecycle[taskID] = WorkflowTaskLifecycleSnapshot{
			CurrentNodes:       currentNodes,
			QueuedCurrentNodes: queued,
			ExactExecutions:    exact,
		}
		executions := sessionruntime.TaskExecutionSnapshot{}
		for _, published := range exact {
			execution, err := taskExecutionFromLifecycleExact(published)
			if err != nil {
				return WorkflowTaskExecutionObservation{}, err
			}
			executions.Executions = append(executions.Executions, execution)
		}
		if len(executions.Executions) != 0 {
			observation.Executions[taskID] = executions
		}
	}
	for _, taskID := range quiescenceTaskIDs {
		if taskID == "" {
			return WorkflowTaskExecutionObservation{}, errors.New("workflow task id is required")
		}
		_, owned := observation.Lifecycle[taskID]
		observation.Quiescence[taskID] = !owned
	}
	return observation, nil
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
