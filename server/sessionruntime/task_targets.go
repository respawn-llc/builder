package sessionruntime

import (
	"errors"
	"sort"
	"strings"

	"core/server/workflow"
	"core/shared/runtimeids"
)

const MaxTaskExecutionTargets = 200

type TaskScriptExecutionTarget struct {
	RunID workflow.RunID
	Path  string
}

type TaskExecutionTargets struct {
	HasExecutions bool
	SessionIDs    []runtimeids.SessionID
	Scripts       []TaskScriptExecutionTarget
}

func (a *Authority) CurrentTaskExecutionTargets(taskID workflow.TaskID) (TaskExecutionTargets, error) {
	if a == nil {
		return TaskExecutionTargets{}, errors.New("session runtime authority is required")
	}
	if strings.TrimSpace(string(taskID)) == "" {
		return TaskExecutionTargets{}, errors.New("workflow task id is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	targets := TaskExecutionTargets{}
	for ref, execution := range a.byWorkflow {
		if ref.TaskID != taskID {
			continue
		}
		targets.HasExecutions = true
		if resource, ok := execution.scope.Resource(); ok {
			if len(targets.SessionIDs) < MaxTaskExecutionTargets {
				targets.SessionIDs = append(targets.SessionIDs, resource.SessionID())
			}
			continue
		}
		if execution.script == nil {
			return TaskExecutionTargets{}, errors.New("live workflow script execution is missing its target")
		}
		if len(targets.Scripts) < MaxTaskExecutionTargets {
			targets.Scripts = append(targets.Scripts, *execution.script)
		}
	}
	sort.Slice(targets.SessionIDs, func(i, j int) bool {
		return targets.SessionIDs[i].String() < targets.SessionIDs[j].String()
	})
	sort.Slice(targets.Scripts, func(i, j int) bool {
		return targets.Scripts[i].RunID < targets.Scripts[j].RunID
	})
	return targets, nil
}
