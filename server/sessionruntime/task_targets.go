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
		if _, exists := wanted[ref.TaskID]; !exists {
			continue
		}
		target := TaskExecution{
			Ref:             ref,
			WaitingQuestion: execution.prompts.hasPending(),
		}
		if resource, ok := execution.scope.Resource(); ok {
			target.Agent = &TaskAgentExecutionTarget{SessionID: resource.SessionID()}
		} else {
			if execution.script == nil {
				return nil, errors.New("live workflow script execution is missing its target")
			}
			target.Script = &TaskScriptExecutionTarget{Path: execution.script.Path}
		}
		if err := target.validate(); err != nil {
			return nil, err
		}
		snapshot := snapshots[ref.TaskID]
		snapshot.Executions = append(snapshot.Executions, target)
		snapshots[ref.TaskID] = snapshot
	}
	for taskID, snapshot := range snapshots {
		sort.Slice(snapshot.Executions, func(i, j int) bool {
			if snapshot.Executions[i].Ref.RunID != snapshot.Executions[j].Ref.RunID {
				return snapshot.Executions[i].Ref.RunID < snapshot.Executions[j].Ref.RunID
			}
			return snapshot.Executions[i].Ref.Generation < snapshot.Executions[j].Ref.Generation
		})
		snapshots[taskID] = snapshot
	}
	return snapshots, nil
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
