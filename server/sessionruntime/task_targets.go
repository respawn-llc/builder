package sessionruntime

import (
	"errors"
	"sort"
	"strings"

	"core/server/workflow"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

const MaxTaskExecutionTargets = serverapi.WorkflowTaskCurrentExecutionTargetsMax

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
	if a == nil {
		return TaskExecutionSnapshot{}, errors.New("session runtime authority is required")
	}
	if strings.TrimSpace(string(taskID)) == "" {
		return TaskExecutionSnapshot{}, errors.New("workflow task id is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	snapshot := TaskExecutionSnapshot{Executions: make([]TaskExecution, 0)}
	for ref, execution := range a.byWorkflow {
		if ref.TaskID != taskID {
			continue
		}
		if len(snapshot.Executions) >= MaxTaskExecutionTargets {
			return TaskExecutionSnapshot{}, errors.New("workflow task has too many live executions")
		}
		target := TaskExecution{
			Ref:             ref,
			WaitingQuestion: execution.prompts.hasPending(),
		}
		if resource, ok := execution.scope.Resource(); ok {
			target.Agent = &TaskAgentExecutionTarget{SessionID: resource.SessionID()}
		} else {
			if execution.script == nil {
				return TaskExecutionSnapshot{}, errors.New("live workflow script execution is missing its target")
			}
			target.Script = &TaskScriptExecutionTarget{Path: execution.script.Path}
		}
		if err := target.validate(); err != nil {
			return TaskExecutionSnapshot{}, err
		}
		snapshot.Executions = append(snapshot.Executions, target)
	}
	sort.Slice(snapshot.Executions, func(i, j int) bool {
		if snapshot.Executions[i].Ref.RunID != snapshot.Executions[j].Ref.RunID {
			return snapshot.Executions[i].Ref.RunID < snapshot.Executions[j].Ref.RunID
		}
		return snapshot.Executions[i].Ref.Generation < snapshot.Executions[j].Ref.Generation
	})
	return snapshot, nil
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
