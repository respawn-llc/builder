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
	WaitingQuestion bool
}

type TaskExecutionSnapshot struct {
	Executions []TaskExecution
}

type WorkflowExecutionMapRevision uint64

type WorkflowExecutionPromptRevision uint64

type AllWorkflowExecutionSnapshot struct {
	ExecutionMapRevision WorkflowExecutionMapRevision
	Executions           []WorkflowExecutionObservation
}

type WorkflowExecutionObservation struct {
	Execution      TaskExecution
	PromptRevision WorkflowExecutionPromptRevision
}

func (a *Authority) CurrentTaskExecutionSnapshot(taskID workflow.TaskID) (TaskExecutionSnapshot, error) {
	snapshots, err := a.CurrentTaskExecutionSnapshots([]workflow.TaskID{taskID})
	if err != nil {
		return TaskExecutionSnapshot{}, err
	}
	return snapshots[taskID], nil
}

func (a *Authority) CurrentTaskExecutionSnapshots(taskIDs []workflow.TaskID) (map[workflow.TaskID]TaskExecutionSnapshot, error) {
	if a == nil {
		return nil, errors.New("session runtime authority is required")
	}
	wanted := make(map[workflow.TaskID]struct{}, len(taskIDs))
	snapshots := make(map[workflow.TaskID]TaskExecutionSnapshot, len(taskIDs))
	for _, taskID := range taskIDs {
		if strings.TrimSpace(string(taskID)) == "" {
			return nil, errors.New("workflow task id is required")
		}
		if _, exists := wanted[taskID]; exists {
			return nil, errors.New("workflow task id is duplicated")
		}
		wanted[taskID] = struct{}{}
		snapshots[taskID] = TaskExecutionSnapshot{Executions: []TaskExecution{}}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for ref, execution := range a.byWorkflow {
		if !execution.activated.Load() || execution.finalizing.Load() {
			continue
		}
		if _, exists := wanted[ref.TaskID]; !exists {
			continue
		}
		observed, err := workflowExecutionObservation(ref, execution)
		if err != nil {
			return nil, err
		}
		taskSnapshot := snapshots[ref.TaskID]
		taskSnapshot.Executions = append(taskSnapshot.Executions, observed.Execution)
		snapshots[ref.TaskID] = taskSnapshot
	}
	for taskID, taskSnapshot := range snapshots {
		sort.Slice(taskSnapshot.Executions, func(i, j int) bool {
			if taskSnapshot.Executions[i].Ref.RunID != taskSnapshot.Executions[j].Ref.RunID {
				return taskSnapshot.Executions[i].Ref.RunID < taskSnapshot.Executions[j].Ref.RunID
			}
			return taskSnapshot.Executions[i].Ref.Generation < taskSnapshot.Executions[j].Ref.Generation
		})
		snapshots[taskID] = taskSnapshot
	}
	return snapshots, nil
}

func (a *Authority) AllWorkflowExecutionSnapshot() (AllWorkflowExecutionSnapshot, error) {
	if a == nil {
		return AllWorkflowExecutionSnapshot{}, errors.New("session runtime authority is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.allWorkflowExecutionSnapshotLocked()
}

func (a *Authority) allWorkflowExecutionSnapshotLocked() (AllWorkflowExecutionSnapshot, error) {
	snapshot := AllWorkflowExecutionSnapshot{
		ExecutionMapRevision: a.executionMapRevision,
		Executions:           make([]WorkflowExecutionObservation, 0, len(a.byWorkflow)),
	}
	for ref, execution := range a.byWorkflow {
		if !execution.activated.Load() || execution.finalizing.Load() {
			continue
		}
		target, err := workflowExecutionObservation(ref, execution)
		if err != nil {
			return AllWorkflowExecutionSnapshot{}, err
		}
		snapshot.Executions = append(snapshot.Executions, target)
	}
	sort.Slice(snapshot.Executions, func(i, j int) bool {
		if snapshot.Executions[i].Execution.Ref.TaskID != snapshot.Executions[j].Execution.Ref.TaskID {
			return snapshot.Executions[i].Execution.Ref.TaskID < snapshot.Executions[j].Execution.Ref.TaskID
		}
		if snapshot.Executions[i].Execution.Ref.RunID != snapshot.Executions[j].Execution.Ref.RunID {
			return snapshot.Executions[i].Execution.Ref.RunID < snapshot.Executions[j].Execution.Ref.RunID
		}
		return snapshot.Executions[i].Execution.Ref.Generation < snapshot.Executions[j].Execution.Ref.Generation
	})
	return snapshot, nil
}

func workflowExecutionObservation(ref WorkflowExecutionRef, execution *execution) (WorkflowExecutionObservation, error) {
	waitingQuestion, promptRevision := execution.prompts.observation()
	target := TaskExecution{
		Ref:             ref,
		WaitingQuestion: waitingQuestion,
	}
	if resource, ok := execution.scope.Resource(); ok {
		target.Agent = &TaskAgentExecutionTarget{SessionID: resource.SessionID()}
	} else {
		if execution.script == nil {
			return WorkflowExecutionObservation{}, errors.New("live workflow script execution is missing its target")
		}
		target.Script = &TaskScriptExecutionTarget{Path: execution.script.Path}
	}
	if err := target.validate(); err != nil {
		return WorkflowExecutionObservation{}, err
	}
	return WorkflowExecutionObservation{Execution: target, PromptRevision: promptRevision}, nil
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
	return nil
}
