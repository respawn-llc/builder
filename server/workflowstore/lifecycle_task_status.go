package workflowstore

import (
	"fmt"

	"core/server/workflow"
)

type LifecycleTaskExecutionStatus struct {
	CurrentNode     workflow.CurrentNodeReference
	WaitingQuestion bool
	WaitingApproval bool
}

type LifecycleTaskStatus struct {
	HasRunning      bool
	HasQueued       bool
	WaitingQuestion bool
	WaitingApproval bool
}

func DeriveLifecycleTaskStatus(
	taskID workflow.TaskID,
	queued []workflow.CurrentNodeReference,
	executions []LifecycleTaskExecutionStatus,
) (LifecycleTaskStatus, error) {
	if taskID == "" {
		return LifecycleTaskStatus{}, fmt.Errorf("lifecycle Task status Task id is required")
	}
	running := make(map[workflow.CurrentNodeReferenceKey]struct{}, len(executions))
	status := LifecycleTaskStatus{HasRunning: len(executions) != 0}
	for _, execution := range executions {
		if execution.CurrentNode.TaskID != taskID {
			return LifecycleTaskStatus{}, fmt.Errorf(
				"lifecycle Task %q execution belongs to Task %q",
				taskID,
				execution.CurrentNode.TaskID,
			)
		}
		key, err := execution.CurrentNode.Key()
		if err != nil {
			return LifecycleTaskStatus{}, err
		}
		if _, duplicate := running[key]; duplicate {
			return LifecycleTaskStatus{}, fmt.Errorf(
				"lifecycle Task %q contains duplicate running Current Node %v",
				taskID,
				execution.CurrentNode,
			)
		}
		running[key] = struct{}{}
		status.WaitingQuestion = status.WaitingQuestion || execution.WaitingQuestion
		status.WaitingApproval = status.WaitingApproval || execution.WaitingApproval
	}
	seenQueued := make(map[workflow.CurrentNodeReferenceKey]struct{}, len(queued))
	for _, reference := range queued {
		if reference.TaskID != taskID {
			return LifecycleTaskStatus{}, fmt.Errorf(
				"lifecycle Task %q queued Run belongs to Task %q",
				taskID,
				reference.TaskID,
			)
		}
		key, err := reference.Key()
		if err != nil {
			return LifecycleTaskStatus{}, err
		}
		if _, duplicate := seenQueued[key]; duplicate {
			return LifecycleTaskStatus{}, fmt.Errorf(
				"lifecycle Task %q contains duplicate queued Current Node %v",
				taskID,
				reference,
			)
		}
		seenQueued[key] = struct{}{}
		if _, active := running[key]; !active {
			status.HasQueued = true
		}
	}
	return status, nil
}
