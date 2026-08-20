package workflow

import (
	"fmt"

	"core/shared/workflowcontract"
)

const MaxTaskDependencies = workflowcontract.MaxTaskDependencies

type TaskDependencyTaskFacts struct {
	ID        TaskID
	ProjectID string
}

type TaskDependencyRole string

const (
	TaskDependencyRoleBlocker TaskDependencyRole = "blocker"
	TaskDependencyRoleBlocked TaskDependencyRole = "blocked"
)

type TaskDependencyCreateIntent struct {
	RelatedTaskID TaskID
	NewTaskRole   TaskDependencyRole
}

type TaskDependencyPairFacts struct {
	BlockerTaskID TaskID
	BlockedTaskID TaskID
	Blocker       *TaskDependencyTaskFacts
	Blocked       *TaskDependencyTaskFacts
}

type TaskDependencyAttachFacts struct {
	TaskDependencyPairFacts
	ExactPairPresent     bool
	ReversePairPresent   bool
	BlockerOutgoingCount int64
	BlockedIncomingCount int64
}

type TaskDependencyAttachDecision string

const (
	TaskDependencyAttachAdded          TaskDependencyAttachDecision = "added"
	TaskDependencyAttachAlreadyPresent TaskDependencyAttachDecision = "already_present"
	TaskDependencyAttachRejected       TaskDependencyAttachDecision = "rejected"
)

type TaskDependencyAddAvailabilityKind string

const (
	TaskDependencyAddAvailable    TaskDependencyAddAvailabilityKind = "available"
	TaskDependencyAddLimitReached TaskDependencyAddAvailabilityKind = "limit_reached"
)

type TaskDependencyAddAvailability struct {
	Kind              TaskDependencyAddAvailabilityKind
	RemainingCapacity *int64
}

type TaskDependencyAvailabilityError struct {
	Count int64
}

func (e TaskDependencyAvailabilityError) Error() string {
	return fmt.Sprintf("task dependency count %d is outside the allowed range", e.Count)
}

type TaskDependencyPolicyErrorReason string

const (
	TaskDependencyMissingTask     TaskDependencyPolicyErrorReason = "missing_task"
	TaskDependencySelf            TaskDependencyPolicyErrorReason = "self_dependency"
	TaskDependencyProjectMismatch TaskDependencyPolicyErrorReason = "project_mismatch"
	TaskDependencyReciprocal      TaskDependencyPolicyErrorReason = "reciprocal_dependency"
	TaskDependencyBlockerLimit    TaskDependencyPolicyErrorReason = "blocker_limit"
	TaskDependencyBlockedLimit    TaskDependencyPolicyErrorReason = "blocked_limit"
)

type TaskDependencyPolicyError struct {
	Reason         TaskDependencyPolicyErrorReason
	BlockerTaskID  TaskID
	BlockedTaskID  TaskID
	MissingTaskID  *TaskID
	BlockerProject *string
	BlockedProject *string
	CurrentCount   *int64
	Limit          *int64
}

func (e TaskDependencyPolicyError) Error() string {
	switch e.Reason {
	case TaskDependencyMissingTask:
		if e.MissingTaskID != nil {
			return fmt.Sprintf("task dependency task %q does not exist", *e.MissingTaskID)
		}
		return "task dependency task does not exist"
	case TaskDependencySelf:
		return fmt.Sprintf("task %q cannot depend on itself", e.BlockerTaskID)
	case TaskDependencyProjectMismatch:
		return fmt.Sprintf("task dependency tasks %q and %q belong to different projects", e.BlockerTaskID, e.BlockedTaskID)
	case TaskDependencyReciprocal:
		return fmt.Sprintf("task dependency %q -> %q has a reciprocal relationship", e.BlockerTaskID, e.BlockedTaskID)
	case TaskDependencyBlockerLimit:
		return fmt.Sprintf("blocker task %q reached its dependency limit", e.BlockerTaskID)
	case TaskDependencyBlockedLimit:
		return fmt.Sprintf("blocked task %q reached its dependency limit", e.BlockedTaskID)
	default:
		return fmt.Sprintf("task dependency policy failed for %q -> %q", e.BlockerTaskID, e.BlockedTaskID)
	}
}

func (e TaskDependencyPolicyError) Is(target error) bool {
	other, ok := target.(TaskDependencyPolicyError)
	return ok && e.Reason == other.Reason
}

type TaskDependencyPolicy struct{}

func (TaskDependencyPolicy) ValidatePair(facts TaskDependencyPairFacts) error {
	if facts.Blocker == nil {
		missingTaskID := facts.BlockerTaskID
		return TaskDependencyPolicyError{
			Reason:        TaskDependencyMissingTask,
			BlockerTaskID: facts.BlockerTaskID,
			BlockedTaskID: facts.BlockedTaskID,
			MissingTaskID: &missingTaskID,
		}
	}
	if facts.Blocked == nil {
		missingTaskID := facts.BlockedTaskID
		return TaskDependencyPolicyError{
			Reason:        TaskDependencyMissingTask,
			BlockerTaskID: facts.BlockerTaskID,
			BlockedTaskID: facts.BlockedTaskID,
			MissingTaskID: &missingTaskID,
		}
	}
	if facts.Blocker.ID == facts.Blocked.ID {
		return TaskDependencyPolicyError{
			Reason:        TaskDependencySelf,
			BlockerTaskID: facts.Blocker.ID,
			BlockedTaskID: facts.Blocked.ID,
		}
	}
	if facts.Blocker.ProjectID != facts.Blocked.ProjectID {
		blockerProject := facts.Blocker.ProjectID
		blockedProject := facts.Blocked.ProjectID
		return TaskDependencyPolicyError{
			Reason:         TaskDependencyProjectMismatch,
			BlockerTaskID:  facts.Blocker.ID,
			BlockedTaskID:  facts.Blocked.ID,
			BlockerProject: &blockerProject,
			BlockedProject: &blockedProject,
		}
	}
	return nil
}

func (TaskDependencyPolicy) AddAvailability(currentCount int64) (TaskDependencyAddAvailability, error) {
	if currentCount < 0 || currentCount > MaxTaskDependencies {
		return TaskDependencyAddAvailability{}, TaskDependencyAvailabilityError{Count: currentCount}
	}
	if currentCount == MaxTaskDependencies {
		return TaskDependencyAddAvailability{Kind: TaskDependencyAddLimitReached}, nil
	}
	remaining := int64(MaxTaskDependencies) - currentCount
	return TaskDependencyAddAvailability{
		Kind:              TaskDependencyAddAvailable,
		RemainingCapacity: &remaining,
	}, nil
}

func (TaskDependencyPolicy) EvaluateAttach(facts TaskDependencyAttachFacts) (TaskDependencyAttachDecision, error) {
	if err := (TaskDependencyPolicy{}).ValidatePair(facts.TaskDependencyPairFacts); err != nil {
		return TaskDependencyAttachRejected, err
	}
	if facts.ExactPairPresent {
		return TaskDependencyAttachAlreadyPresent, nil
	}
	if facts.ReversePairPresent {
		return TaskDependencyAttachRejected, TaskDependencyPolicyError{
			Reason:        TaskDependencyReciprocal,
			BlockerTaskID: facts.Blocker.ID,
			BlockedTaskID: facts.Blocked.ID,
		}
	}
	if facts.BlockerOutgoingCount >= MaxTaskDependencies {
		currentCount := facts.BlockerOutgoingCount
		limit := int64(MaxTaskDependencies)
		return TaskDependencyAttachRejected, TaskDependencyPolicyError{
			Reason:        TaskDependencyBlockerLimit,
			BlockerTaskID: facts.Blocker.ID,
			BlockedTaskID: facts.Blocked.ID,
			CurrentCount:  &currentCount,
			Limit:         &limit,
		}
	}
	if facts.BlockedIncomingCount >= MaxTaskDependencies {
		currentCount := facts.BlockedIncomingCount
		limit := int64(MaxTaskDependencies)
		return TaskDependencyAttachRejected, TaskDependencyPolicyError{
			Reason:        TaskDependencyBlockedLimit,
			BlockerTaskID: facts.Blocker.ID,
			BlockedTaskID: facts.Blocked.ID,
			CurrentCount:  &currentCount,
			Limit:         &limit,
		}
	}
	return TaskDependencyAttachAdded, nil
}
