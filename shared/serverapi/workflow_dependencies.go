package serverapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/shared/protocol"
)

type WorkflowTaskDependencyRole string

const (
	WorkflowTaskDependencyRoleBlocker WorkflowTaskDependencyRole = "blocker"
	WorkflowTaskDependencyRoleBlocked WorkflowTaskDependencyRole = "blocked"
)

type WorkflowTaskDependencyCreateIntent struct {
	RelatedTaskID string                     `json:"related_task_id"`
	NewTaskRole   WorkflowTaskDependencyRole `json:"new_task_role"`
}

type WorkflowTaskDependencyAddRequest struct {
	BlockerTaskID string `json:"blocker_task_id"`
	BlockedTaskID string `json:"blocked_task_id"`
}

type WorkflowTaskDependencyRemoveRequest struct {
	BlockerTaskID string `json:"blocker_task_id"`
	BlockedTaskID string `json:"blocked_task_id"`
}

type WorkflowTaskDependencyListRequest struct {
	TaskID    string                           `json:"task_id"`
	Direction *WorkflowTaskDependencyDirection `json:"direction,omitempty"`
}

type WorkflowTaskDependencyOutcome string

const (
	WorkflowTaskDependencyOutcomeAdded          WorkflowTaskDependencyOutcome = "added"
	WorkflowTaskDependencyOutcomeAlreadyPresent WorkflowTaskDependencyOutcome = "already_present"
	WorkflowTaskDependencyOutcomeRemoved        WorkflowTaskDependencyOutcome = "removed"
	WorkflowTaskDependencyOutcomeAlreadyAbsent  WorkflowTaskDependencyOutcome = "already_absent"
)

type WorkflowTaskDependencyMutationResponse struct {
	Outcome        WorkflowTaskDependencyOutcome `json:"outcome"`
	BlockerTaskID  string                        `json:"blocker_task_id"`
	BlockerShortID string                        `json:"blocker_short_id"`
	BlockedTaskID  string                        `json:"blocked_task_id"`
	BlockedShortID string                        `json:"blocked_short_id"`
}

type WorkflowTaskDependencyAddResponse WorkflowTaskDependencyMutationResponse
type WorkflowTaskDependencyRemoveResponse WorkflowTaskDependencyMutationResponse

type WorkflowTaskDependencyListDirectionProjection struct {
	Direction        WorkflowTaskDependencyDirection `json:"direction"`
	TotalCount       int                             `json:"total_count"`
	UnsatisfiedCount *int                            `json:"unsatisfied_count,omitempty"`
	Items            []WorkflowTaskDependencyItem    `json:"items"`
}

type WorkflowTaskDependencyListResponse struct {
	TaskID     string                                          `json:"task_id"`
	ShortID    string                                          `json:"short_id"`
	Directions []WorkflowTaskDependencyListDirectionProjection `json:"directions"`
}

type WorkflowTaskActionOutcome string

const (
	WorkflowTaskActionOutcomeApplied                        WorkflowTaskActionOutcome = "applied"
	WorkflowTaskActionOutcomeDependencyConfirmationRequired WorkflowTaskActionOutcome = "dependency_confirmation_required"
	WorkflowTaskActionOutcomeSelectionRequired              WorkflowTaskActionOutcome = "selection_required"
)

type WorkflowTaskDependencyErrorReason string

const (
	WorkflowTaskDependencyErrorReasonMissingTask     WorkflowTaskDependencyErrorReason = "missing_task"
	WorkflowTaskDependencyErrorReasonSelf            WorkflowTaskDependencyErrorReason = "self_dependency"
	WorkflowTaskDependencyErrorReasonProjectMismatch WorkflowTaskDependencyErrorReason = "project_mismatch"
	WorkflowTaskDependencyErrorReasonReciprocal      WorkflowTaskDependencyErrorReason = "reciprocal_dependency"
	WorkflowTaskDependencyErrorReasonBlockerLimit    WorkflowTaskDependencyErrorReason = "blocker_limit"
	WorkflowTaskDependencyErrorReasonBlockedLimit    WorkflowTaskDependencyErrorReason = "blocked_limit"
)

type WorkflowTaskDependencyError struct {
	Reason        WorkflowTaskDependencyErrorReason `json:"reason"`
	BlockerTaskID string                            `json:"blocker_task_id"`
	BlockedTaskID string                            `json:"blocked_task_id"`
	MissingTaskID *string                           `json:"missing_task_id,omitempty"`
	CurrentCount  *int                              `json:"current_count,omitempty"`
	Limit         *int                              `json:"limit,omitempty"`
}

func (e *WorkflowTaskDependencyError) Error() string {
	if e == nil {
		return "workflow task dependency error"
	}
	if e.MissingTaskID != nil {
		return fmt.Sprintf("workflow task dependency error: %s (%s)", e.Reason, *e.MissingTaskID)
	}
	return "workflow task dependency error: " + string(e.Reason)
}

func (e *WorkflowTaskDependencyError) RPCErrorCode() int {
	return protocol.ErrCodeWorkflowTaskDependency
}

func (e *WorkflowTaskDependencyError) RPCErrorData() json.RawMessage {
	if e == nil {
		return nil
	}
	return marshalRPCErrorData(struct {
		Type          string                            `json:"type"`
		Reason        WorkflowTaskDependencyErrorReason `json:"reason"`
		BlockerTaskID string                            `json:"blocker_task_id"`
		BlockedTaskID string                            `json:"blocked_task_id"`
		MissingTaskID *string                           `json:"missing_task_id,omitempty"`
		CurrentCount  *int                              `json:"current_count,omitempty"`
		Limit         *int                              `json:"limit,omitempty"`
	}{
		Type:          "workflow_task_dependency_error",
		Reason:        e.Reason,
		BlockerTaskID: e.BlockerTaskID,
		BlockedTaskID: e.BlockedTaskID,
		MissingTaskID: e.MissingTaskID,
		CurrentCount:  e.CurrentCount,
		Limit:         e.Limit,
	})
}

func DecodeWorkflowTaskDependencyError(data json.RawMessage, message string) error {
	var envelope struct {
		Type          string                            `json:"type"`
		Reason        WorkflowTaskDependencyErrorReason `json:"reason"`
		BlockerTaskID string                            `json:"blocker_task_id"`
		BlockedTaskID string                            `json:"blocked_task_id"`
		MissingTaskID *string                           `json:"missing_task_id,omitempty"`
		CurrentCount  *int                              `json:"current_count,omitempty"`
		Limit         *int                              `json:"limit,omitempty"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil ||
		envelope.Type != "workflow_task_dependency_error" {
		return errors.New(strings.TrimSpace(message))
	}
	decoded := &WorkflowTaskDependencyError{
		Reason:        envelope.Reason,
		BlockerTaskID: envelope.BlockerTaskID,
		BlockedTaskID: envelope.BlockedTaskID,
		MissingTaskID: envelope.MissingTaskID,
		CurrentCount:  envelope.CurrentCount,
		Limit:         envelope.Limit,
	}
	if err := decoded.Validate(); err != nil {
		return errors.New(strings.TrimSpace(message))
	}
	return decoded
}

func (i WorkflowTaskDependencyCreateIntent) Validate() error {
	return i.validate("dependency_intents")
}

func (i WorkflowTaskDependencyCreateIntent) validate(field string) error {
	if err := validateRequired(field+".related_task_id", i.RelatedTaskID); err != nil {
		return err
	}
	if strings.TrimSpace(i.RelatedTaskID) != i.RelatedTaskID {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, field+".related_task_id", "related_task_id must not have leading or trailing whitespace")
	}
	switch i.NewTaskRole {
	case WorkflowTaskDependencyRoleBlocker, WorkflowTaskDependencyRoleBlocked:
		return nil
	default:
		return workflowRequestError(WorkflowRequestErrorInvalidValue, field+".new_task_role", "new_task_role is invalid")
	}
}

func (r WorkflowTaskDependencyAddRequest) Validate() error {
	return validateWorkflowTaskDependencyPair(r.BlockerTaskID, r.BlockedTaskID)
}

func (r WorkflowTaskDependencyRemoveRequest) Validate() error {
	return validateWorkflowTaskDependencyPair(r.BlockerTaskID, r.BlockedTaskID)
}

func (r WorkflowTaskDependencyListRequest) Validate() error {
	if err := validateRequired("task_id", r.TaskID); err != nil {
		return err
	}
	if strings.TrimSpace(r.TaskID) != r.TaskID {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "task_id", "task_id must not have leading or trailing whitespace")
	}
	if r.Direction != nil {
		switch *r.Direction {
		case WorkflowTaskDependencyDirectionBlockedBy, WorkflowTaskDependencyDirectionBlocks:
		default:
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "direction", "direction is invalid")
		}
	}
	return nil
}

func (r WorkflowTaskDependencyMutationResponse) Validate() error {
	return validateWorkflowTaskDependencyMutationResponse(r, false, false)
}

func (r WorkflowTaskDependencyAddResponse) Validate() error {
	return validateWorkflowTaskDependencyMutationResponse(WorkflowTaskDependencyMutationResponse(r), true, false)
}

func (r WorkflowTaskDependencyRemoveResponse) Validate() error {
	return validateWorkflowTaskDependencyMutationResponse(WorkflowTaskDependencyMutationResponse(r), false, true)
}

func validateWorkflowTaskDependencyMutationResponse(r WorkflowTaskDependencyMutationResponse, addOnly, removeOnly bool) error {
	if err := validateRequired("blocker_task_id", r.BlockerTaskID); err != nil {
		return err
	}
	if err := validateRequired("blocker_short_id", r.BlockerShortID); err != nil {
		return err
	}
	if err := validateRequired("blocked_task_id", r.BlockedTaskID); err != nil {
		return err
	}
	if err := validateRequired("blocked_short_id", r.BlockedShortID); err != nil {
		return err
	}
	switch r.Outcome {
	case WorkflowTaskDependencyOutcomeAdded, WorkflowTaskDependencyOutcomeAlreadyPresent:
		if removeOnly {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "outcome", "outcome is invalid for add response")
		}
	case WorkflowTaskDependencyOutcomeRemoved, WorkflowTaskDependencyOutcomeAlreadyAbsent:
		if addOnly {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "outcome", "outcome is invalid for add response")
		}
	default:
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "outcome", "outcome is invalid")
	}
	return nil
}

func (r WorkflowTaskDependencyListResponse) Validate() error {
	if err := validateRequired("task_id", r.TaskID); err != nil {
		return err
	}
	if err := validateRequired("short_id", r.ShortID); err != nil {
		return err
	}
	if r.Directions == nil {
		return workflowRequestError(WorkflowRequestErrorRequired, "directions", "directions is required")
	}
	seen := map[WorkflowTaskDependencyDirection]struct{}{}
	for index, direction := range r.Directions {
		if err := direction.validate(); err != nil {
			return prefixWorkflowProjectionValidationField("directions", index, err)
		}
		if _, exists := seen[direction.Direction]; exists {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "directions", "directions must contain each direction at most once")
		}
		seen[direction.Direction] = struct{}{}
	}
	return nil
}

func (r WorkflowTaskDependencyListDirectionProjection) validate() error {
	switch r.Direction {
	case WorkflowTaskDependencyDirectionBlockedBy:
		if r.UnsatisfiedCount == nil {
			return workflowRequestError(WorkflowRequestErrorRequired, "unsatisfied_count", "unsatisfied_count is required for blocked-by")
		}
		if *r.UnsatisfiedCount < 0 || *r.UnsatisfiedCount > r.TotalCount {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "unsatisfied_count", "unsatisfied_count must be within total_count")
		}
	case WorkflowTaskDependencyDirectionBlocks:
		if r.UnsatisfiedCount != nil {
			return workflowRequestError(WorkflowRequestErrorInvalidMode, "unsatisfied_count", "unsatisfied_count is only valid for blocked-by")
		}
	default:
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "direction", "direction is invalid")
	}
	if r.TotalCount < 0 {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "total_count", "total_count must be non-negative")
	}
	if r.Items == nil {
		return workflowRequestError(WorkflowRequestErrorRequired, "items", "items is required")
	}
	if r.TotalCount != len(r.Items) {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "total_count", "total_count must match items length")
	}
	for index, item := range r.Items {
		if err := item.validate(r.Direction == WorkflowTaskDependencyDirectionBlockedBy); err != nil {
			return prefixWorkflowProjectionValidationField("items", index, err)
		}
	}
	return nil
}

func (i WorkflowTaskDependencyItem) validate(requireSatisfaction bool) error {
	for field, value := range map[string]string{
		"task_id":     i.TaskID,
		"short_id":    i.ShortID,
		"title":       i.Title,
		"workflow_id": i.WorkflowID,
	} {
		if err := validateRequired(field, value); err != nil {
			return err
		}
	}
	if requireSatisfaction && i.Satisfaction == nil {
		return workflowRequestError(WorkflowRequestErrorRequired, "satisfaction", "satisfaction is required for blocked-by items")
	}
	if !requireSatisfaction && i.Satisfaction != nil {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "satisfaction", "satisfaction is only valid for blocked-by items")
	}
	if i.Satisfaction != nil {
		switch *i.Satisfaction {
		case WorkflowTaskDependencySatisfied, WorkflowTaskDependencyUnsatisfied:
		default:
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "satisfaction", "satisfaction is invalid")
		}
	}
	return nil
}

func (d WorkflowTaskDependencyDirectionProjection) validateDetail() error {
	if err := (WorkflowTaskDependencyListDirectionProjection{
		Direction:        d.Direction,
		TotalCount:       d.TotalCount,
		UnsatisfiedCount: d.UnsatisfiedCount,
		Items:            d.Items,
	}).validate(); err != nil {
		return err
	}
	if d.AddAvailability == nil {
		return workflowRequestError(WorkflowRequestErrorRequired, "add_availability", "add_availability is required")
	}
	return d.AddAvailability.Validate()
}

func (d WorkflowTaskDependencyAddAvailability) Validate() error {
	hasAvailable := d.Available != nil
	hasLimitReached := d.LimitReached != nil
	if hasAvailable == hasLimitReached {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "add_availability", "exactly one availability variant is required")
	}
	if hasAvailable {
		if d.Available.RemainingCapacity <= 0 {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "add_availability.available.remaining_capacity", "remaining_capacity must be positive")
		}
		return nil
	}
	return nil
}

func (d WorkflowTaskDependencies) Validate() error {
	if d.BlockerCount < 0 || d.UnsatisfiedBlockerCount < 0 || d.DirectlyBlockedTaskCount < 0 {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "dependencies", "dependency counts must be non-negative")
	}
	if d.UnsatisfiedBlockerCount > d.BlockerCount {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "dependencies.unsatisfied_blocker_count", "unsatisfied_blocker_count must not exceed blocker_count")
	}
	if d.Directions == nil {
		return workflowRequestError(WorkflowRequestErrorRequired, "dependencies.directions", "directions is required")
	}
	if len(d.Directions) != 2 {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "dependencies.directions", "task detail must contain both dependency directions")
	}
	seen := map[WorkflowTaskDependencyDirection]struct{}{}
	for index, direction := range d.Directions {
		if err := direction.validateDetail(); err != nil {
			return prefixWorkflowProjectionValidationField("dependencies.directions", index, err)
		}
		if _, exists := seen[direction.Direction]; exists {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "dependencies.directions", "directions must contain each direction once")
		}
		seen[direction.Direction] = struct{}{}
		switch direction.Direction {
		case WorkflowTaskDependencyDirectionBlockedBy:
			if direction.TotalCount != d.BlockerCount || direction.UnsatisfiedCount == nil || *direction.UnsatisfiedCount != d.UnsatisfiedBlockerCount {
				return workflowRequestError(WorkflowRequestErrorInvalidValue, "dependencies", "blocked-by direction does not match dependency summary")
			}
		case WorkflowTaskDependencyDirectionBlocks:
			if direction.TotalCount != d.DirectlyBlockedTaskCount {
				return workflowRequestError(WorkflowRequestErrorInvalidValue, "dependencies", "blocks direction does not match dependency summary")
			}
		}
	}
	return nil
}

func (p WorkflowTaskDependencyProgress) Validate() error {
	if p.TotalCount <= 0 {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "dependency_progress.total_count", "total_count must be positive")
	}
	if p.SatisfiedCount < 0 || p.SatisfiedCount > p.TotalCount {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "dependency_progress.satisfied_count", "satisfied_count must be within total_count")
	}
	return nil
}

func (e WorkflowTaskDependencyError) Validate() error {
	if err := validateWorkflowTaskDependencyPair(e.BlockerTaskID, e.BlockedTaskID); err != nil {
		return err
	}
	switch e.Reason {
	case WorkflowTaskDependencyErrorReasonMissingTask:
		if e.MissingTaskID == nil {
			return workflowRequestError(WorkflowRequestErrorRequired, "missing_task_id", "missing_task_id is required")
		}
		if strings.TrimSpace(*e.MissingTaskID) != *e.MissingTaskID || strings.TrimSpace(*e.MissingTaskID) == "" {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "missing_task_id", "missing_task_id must be non-blank and trimmed")
		}
		if e.CurrentCount != nil || e.Limit != nil {
			return workflowRequestError(WorkflowRequestErrorInvalidMode, "error", "missing-task errors cannot carry count fields")
		}
	case WorkflowTaskDependencyErrorReasonBlockerLimit, WorkflowTaskDependencyErrorReasonBlockedLimit:
		if e.CurrentCount == nil || e.Limit == nil || *e.CurrentCount < 0 || *e.Limit <= 0 || *e.CurrentCount > *e.Limit {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "error", "limit errors require valid count metadata")
		}
		if e.MissingTaskID != nil {
			return workflowRequestError(WorkflowRequestErrorInvalidMode, "missing_task_id", "limit errors cannot carry missing_task_id")
		}
	case WorkflowTaskDependencyErrorReasonSelf,
		WorkflowTaskDependencyErrorReasonProjectMismatch,
		WorkflowTaskDependencyErrorReasonReciprocal:
		if e.MissingTaskID != nil || e.CurrentCount != nil || e.Limit != nil {
			return workflowRequestError(WorkflowRequestErrorInvalidMode, "error", "this dependency error reason cannot carry optional metadata")
		}
	default:
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "reason", "dependency error reason is invalid")
	}
	return nil
}

func validateWorkflowTaskDependencyPair(blockerTaskID, blockedTaskID string) error {
	if err := validateRequired("blocker_task_id", blockerTaskID); err != nil {
		return err
	}
	if err := validateRequired("blocked_task_id", blockedTaskID); err != nil {
		return err
	}
	if strings.TrimSpace(blockerTaskID) != blockerTaskID {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "blocker_task_id", "blocker_task_id must not have leading or trailing whitespace")
	}
	if strings.TrimSpace(blockedTaskID) != blockedTaskID {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "blocked_task_id", "blocked_task_id must not have leading or trailing whitespace")
	}
	return nil
}
