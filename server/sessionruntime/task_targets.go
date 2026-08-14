package sessionruntime

import (
	"errors"
	"fmt"
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

type PendingPromptKind string

const (
	PendingPromptKindQuestion        PendingPromptKind = "question"
	PendingPromptKindSessionApproval PendingPromptKind = "session_approval"
)

type PendingPromptReference struct {
	ID   string
	Kind PendingPromptKind
}

type TaskExecution struct {
	Ref            WorkflowExecutionRef
	Agent          *TaskAgentExecutionTarget
	Script         *TaskScriptExecutionTarget
	Queued         bool
	PendingPrompts []PendingPromptReference
}

type TaskExecutionSnapshot struct {
	Executions []TaskExecution
}

type workflowTaskExecutionReadSnapshot struct {
	executions map[workflow.TaskID]TaskExecutionSnapshot
}

// CurrentWorkflowTaskExecutionReadSnapshot returns the latest completed
// immutable read projection without entering live runtime ownership.
func (a *Authority) CurrentWorkflowTaskExecutionReadSnapshot() (map[workflow.TaskID]TaskExecutionSnapshot, error) {
	if a == nil {
		return nil, errors.New("session runtime authority is required")
	}
	current := a.workflowTaskReads.Load()
	if current == nil {
		return map[workflow.TaskID]TaskExecutionSnapshot{}, nil
	}
	return cloneTaskExecutionSnapshots(current.executions), nil
}

func (a *Authority) workflowTaskExecutionSnapshotsLocked() (map[workflow.TaskID]TaskExecutionSnapshot, error) {
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
	sortTaskExecutionSnapshots(snapshots)
	return snapshots, nil
}

func (a *Authority) publishWorkflowTaskExecutionReadSnapshot() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.publishWorkflowTaskExecutionReadSnapshotLocked()
}

func (a *Authority) publishWorkflowTaskExecutionReadSnapshotLocked() {
	snapshots, err := a.workflowTaskExecutionSnapshotsLocked()
	if err != nil {
		panic(fmt.Sprintf("publish workflow Task execution read snapshot: %v", err))
	}
	a.workflowTaskReads.Store(&workflowTaskExecutionReadSnapshot{executions: snapshots})
}

type WorkflowTaskExecutionState struct {
	Running          int
	WaitingQuestions int
	WaitingApprovals int
	Queued           int
	Finalizing       int
}

func (a *Authority) CurrentWorkflowTaskExecutionState(taskID workflow.TaskID) (WorkflowTaskExecutionState, error) {
	if a == nil {
		return WorkflowTaskExecutionState{}, errors.New("session runtime authority is required")
	}
	if strings.TrimSpace(string(taskID)) == "" {
		return WorkflowTaskExecutionState{}, errors.New("workflow task id is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	state := WorkflowTaskExecutionState{}
	for _, execution := range a.byScope {
		ref, ok := execution.scope.Workflow()
		if !ok || ref.CurrentNode.TaskID != taskID {
			continue
		}
		switch execution.phase {
		case executionPhaseQueued:
			state.Queued++
		case executionPhaseRunning:
			pending, err := execution.prompts.pendingReferences()
			if err != nil {
				return WorkflowTaskExecutionState{}, err
			}
			questionPending := false
			approvalPending := false
			for _, prompt := range pending {
				switch prompt.Kind {
				case PendingPromptKindQuestion:
					questionPending = true
				case PendingPromptKindSessionApproval:
					approvalPending = true
				}
			}
			if questionPending {
				state.WaitingQuestions++
			} else if approvalPending {
				state.WaitingApprovals++
			} else if len(pending) == 0 {
				state.Running++
			}
		case executionPhaseFinalizing:
			state.Finalizing++
		default:
			return WorkflowTaskExecutionState{}, fmt.Errorf("workflow execution scope %s has invalid phase", execution.scope.ID())
		}
	}
	return state, nil
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
	snapshots, err := a.workflowTaskExecutionSnapshotsLocked()
	if err != nil {
		return nil, err
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
	pendingPrompts, err := execution.prompts.pendingReferences()
	if err != nil {
		return err
	}
	return appendTaskExecutionSnapshotWithPrompts(snapshots, execution, pendingPrompts)
}

func appendTaskExecutionSnapshotWithPrompts(
	snapshots map[workflow.TaskID]TaskExecutionSnapshot,
	execution *execution,
	pendingPrompts []PendingPromptReference,
) error {
	ref, ok := execution.scope.Workflow()
	if !ok {
		return errors.New("workflow execution index contains a non-workflow scope")
	}
	if execution.phase != executionPhaseQueued && execution.phase != executionPhaseRunning {
		return errors.New("live workflow execution has an invalid phase")
	}
	target := TaskExecution{
		Ref:    ref,
		Queued: execution.phase == executionPhaseQueued,
	}
	target.PendingPrompts = pendingPrompts
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

func cloneTaskExecutionSnapshots(source map[workflow.TaskID]TaskExecutionSnapshot) map[workflow.TaskID]TaskExecutionSnapshot {
	cloned := make(map[workflow.TaskID]TaskExecutionSnapshot, len(source))
	for taskID, snapshot := range source {
		executions := make([]TaskExecution, len(snapshot.Executions))
		for index, execution := range snapshot.Executions {
			executions[index] = execution
			executions[index].PendingPrompts = append([]PendingPromptReference(nil), execution.PendingPrompts...)
			if execution.Agent != nil {
				agent := *execution.Agent
				executions[index].Agent = &agent
			}
			if execution.Script != nil {
				script := *execution.Script
				executions[index].Script = &script
			}
		}
		cloned[taskID] = TaskExecutionSnapshot{Executions: executions}
	}
	return cloned
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
	for index, prompt := range e.PendingPrompts {
		if err := prompt.validate(); err != nil {
			return fmt.Errorf("pending prompt %d: %w", index, err)
		}
		if index != 0 && e.PendingPrompts[index-1].ID >= prompt.ID {
			return errors.New("live workflow execution pending prompts are not sorted and unique")
		}
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
		if len(e.PendingPrompts) != 0 {
			return errors.New("live workflow script execution cannot have pending prompts")
		}
	}
	if e.Queued && len(e.PendingPrompts) != 0 {
		return errors.New("queued workflow execution cannot have pending prompts")
	}
	return nil
}

func (p PendingPromptReference) validate() error {
	if strings.TrimSpace(p.ID) == "" {
		return errors.New("pending prompt id is required")
	}
	switch p.Kind {
	case PendingPromptKindQuestion, PendingPromptKindSessionApproval:
		return nil
	default:
		return fmt.Errorf("pending prompt %q has invalid kind %q", p.ID, p.Kind)
	}
}

func (e TaskExecution) HasPendingPromptKind(kind PendingPromptKind) bool {
	for _, prompt := range e.PendingPrompts {
		if prompt.Kind == kind {
			return true
		}
	}
	return false
}

func (e TaskExecution) HasPendingPrompts() bool {
	return len(e.PendingPrompts) != 0
}
