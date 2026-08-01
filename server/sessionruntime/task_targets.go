package sessionruntime

import (
	"errors"
	"sort"
	"strings"

	"core/server/workflow"
	"core/shared/runtimeids"
)

type TaskScriptExecutionTarget struct {
	Path string
}

type TaskAgentExecutionTarget struct {
	SessionID runtimeids.SessionID
}

type TaskExecution struct {
	Ref             WorkflowExecutionRef
	Agent           *TaskAgentExecutionTarget
	Script          *TaskScriptExecutionTarget
	Queued          bool
	WaitingQuestion bool
}

type TaskExecutionSnapshot struct {
	Executions []TaskExecution
}

func (a *Authority) CurrentScopedTaskExecutionSnapshot(projectID string, workflowID runtimeids.WorkflowID, taskID workflow.TaskID) (TaskExecutionSnapshot, error) {
	snapshots, err := a.CurrentScopedTaskExecutionSnapshots(projectID, workflowID, []workflow.TaskID{taskID})
	if err != nil {
		return TaskExecutionSnapshot{}, err
	}
	return snapshots[taskID], nil
}

// CurrentWorkflowTaskExecutionSnapshots captures every live workflow Exact
// Execution Scope once. Read models use this bounded process-local snapshot as
// liveness evidence; SQLite never substitutes for it.
func (a *Authority) CurrentWorkflowTaskExecutionSnapshots() (map[workflow.TaskID]TaskExecutionSnapshot, error) {
	if a == nil {
		return nil, errors.New("session runtime authority is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	snapshots := map[workflow.TaskID]TaskExecutionSnapshot{}
	var snapshotErr error
	a.forEachWorkflowExecutionLocked(func(execution *execution) {
		if snapshotErr != nil {
			return
		}
		snapshotErr = appendTaskExecutionSnapshot(snapshots, execution)
	})
	if snapshotErr != nil {
		return nil, snapshotErr
	}
	for taskID, snapshot := range snapshots {
		sort.Slice(snapshot.Executions, func(i, j int) bool {
			return workflowExecutionLess(snapshot.Executions[i], snapshot.Executions[j])
		})
		snapshots[taskID] = snapshot
	}
	return snapshots, nil
}

func (a *Authority) CurrentProjectTaskExecutionSnapshots(projectID string) (map[workflow.TaskID]TaskExecutionSnapshot, error) {
	if a == nil {
		return nil, errors.New("session runtime authority is required")
	}
	if strings.TrimSpace(projectID) == "" {
		return nil, errors.New("workflow project id is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	snapshots := map[workflow.TaskID]TaskExecutionSnapshot{}
	var snapshotErr error
	for _, byTask := range a.workflowExecutions[projectID] {
		for _, executions := range byTask {
			for _, execution := range executions {
				if snapshotErr == nil {
					snapshotErr = appendTaskExecutionSnapshot(snapshots, execution)
				}
			}
		}
	}
	if snapshotErr != nil {
		return nil, snapshotErr
	}
	sortTaskExecutionSnapshots(snapshots)
	return snapshots, nil
}

func (a *Authority) CurrentProjectWorkflowTaskExecutionSnapshots(projectID string, workflowID runtimeids.WorkflowID) (map[workflow.TaskID]TaskExecutionSnapshot, error) {
	if a == nil {
		return nil, errors.New("session runtime authority is required")
	}
	if strings.TrimSpace(projectID) == "" || workflowID.IsZero() {
		return nil, errors.New("workflow execution scope is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	snapshots := map[workflow.TaskID]TaskExecutionSnapshot{}
	var snapshotErr error
	for _, executions := range a.workflowExecutions[projectID][workflowID] {
		for _, execution := range executions {
			if snapshotErr == nil {
				snapshotErr = appendTaskExecutionSnapshot(snapshots, execution)
			}
		}
	}
	if snapshotErr != nil {
		return nil, snapshotErr
	}
	sortTaskExecutionSnapshots(snapshots)
	return snapshots, nil
}

func (a *Authority) CurrentScopedTaskExecutionSnapshots(projectID string, workflowID runtimeids.WorkflowID, taskIDs []workflow.TaskID) (map[workflow.TaskID]TaskExecutionSnapshot, error) {
	if a == nil {
		return nil, errors.New("session runtime authority is required")
	}
	if strings.TrimSpace(projectID) == "" || workflowID.IsZero() {
		return nil, errors.New("workflow execution scope is required")
	}
	snapshots := make(map[workflow.TaskID]TaskExecutionSnapshot, len(taskIDs))
	for _, taskID := range taskIDs {
		if strings.TrimSpace(string(taskID)) == "" {
			return nil, errors.New("workflow task id is required")
		}
		if _, duplicate := snapshots[taskID]; duplicate {
			return nil, errors.New("workflow task id is duplicated")
		}
		snapshots[taskID] = TaskExecutionSnapshot{Executions: []TaskExecution{}}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	byTask := a.workflowExecutions[projectID][workflowID]
	for taskID := range snapshots {
		for _, execution := range byTask[taskID] {
			if err := appendTaskExecutionSnapshot(snapshots, execution); err != nil {
				return nil, err
			}
		}
	}
	sortTaskExecutionSnapshots(snapshots)
	return snapshots, nil
}

func appendTaskExecutionSnapshot(snapshots map[workflow.TaskID]TaskExecutionSnapshot, execution *execution) error {
	ref, ok := execution.scope.Workflow()
	if !ok {
		return errors.New("workflow execution index contains a non-workflow scope")
	}
	if execution.phase != executionPhaseQueued && execution.phase != executionPhaseRunning {
		return errors.New("live workflow execution has an invalid phase")
	}
	target := TaskExecution{
		Ref:             ref,
		Queued:          execution.phase == executionPhaseQueued,
		WaitingQuestion: execution.prompts.hasPending(),
	}
	if resource, ok := execution.scope.Resource(); ok {
		target.Agent = &TaskAgentExecutionTarget{SessionID: resource.SessionID()}
	} else {
		if execution.script == nil {
			return errors.New("live workflow script execution is missing its target")
		}
		target.Script = &TaskScriptExecutionTarget{Path: execution.script.Path}
	}
	if err := target.validate(); err != nil {
		return err
	}
	snapshot := snapshots[ref.CurrentNode.TaskID]
	snapshot.Executions = append(snapshot.Executions, target)
	snapshots[ref.CurrentNode.TaskID] = snapshot
	return nil
}

func sortTaskExecutionSnapshots(snapshots map[workflow.TaskID]TaskExecutionSnapshot) {
	for taskID, snapshot := range snapshots {
		sort.Slice(snapshot.Executions, func(i, j int) bool {
			return workflowExecutionLess(snapshot.Executions[i], snapshot.Executions[j])
		})
		snapshots[taskID] = snapshot
	}
}

func workflowExecutionLess(leftExecution TaskExecution, rightExecution TaskExecution) bool {
	left := leftExecution.Ref.CurrentNode
	right := rightExecution.Ref.CurrentNode
	if left.NodeID != right.NodeID {
		return left.NodeID < right.NodeID
	}
	leftBranch, leftScoped := left.TransitionBranchKey()
	rightBranch, rightScoped := right.TransitionBranchKey()
	if leftScoped != rightScoped {
		return !leftScoped
	}
	return leftBranch < rightBranch
}

func (e TaskExecution) validate() error {
	if err := e.Ref.Validate(); err != nil {
		return err
	}
	if (e.Agent == nil) == (e.Script == nil) {
		return errors.New("live workflow execution must have exactly one target")
	}
	if e.Agent != nil && e.Agent.SessionID.IsZero() {
		return errors.New("live workflow agent execution has no session id")
	}
	if e.Script != nil {
		if strings.TrimSpace(e.Script.Path) == "" {
			return errors.New("live workflow script execution has no executable path")
		}
		if e.WaitingQuestion {
			return errors.New("live workflow script execution cannot wait for a question")
		}
	}
	if e.Queued && e.WaitingQuestion {
		return errors.New("queued workflow execution cannot wait for a question")
	}
	return nil
}
