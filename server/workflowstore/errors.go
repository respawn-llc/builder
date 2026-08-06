package workflowstore

import (
	"errors"
	"fmt"
	"strings"

	"core/server/workflow"
	"core/shared/runtimeids"
	sqlitedriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

type TaskWorkflowSelectionReason string

const (
	TaskWorkflowSelectionNoLinkedWorkflows       TaskWorkflowSelectionReason = "no_linked_workflows"
	TaskWorkflowSelectionAmbiguousWithoutDefault TaskWorkflowSelectionReason = "ambiguous_without_default"
	TaskWorkflowSelectionWorkflowNotLinked       TaskWorkflowSelectionReason = "workflow_not_linked"
)

type TaskWorkflowSelectionError struct {
	Reason     TaskWorkflowSelectionReason
	ProjectID  string
	WorkflowID *runtimeids.WorkflowID
}

func (e TaskWorkflowSelectionError) Error() string {
	return fmt.Sprintf("task workflow selection failed for project %q: %s", e.ProjectID, e.Reason)
}

type TaskCreateConflictReason string

const (
	TaskCreateConflictSerialization TaskCreateConflictReason = "serialization_conflict"
)

type TaskCreateConflictError struct {
	Reason TaskCreateConflictReason
	Cause  error
}

func (e TaskCreateConflictError) Error() string {
	return fmt.Sprintf("task create conflict: %s", e.Reason)
}

func (e TaskCreateConflictError) Unwrap() error {
	return e.Cause
}

func taskCreateStoreError(err error) error {
	if err == nil {
		return nil
	}
	var sqliteErr *sqlitedriver.Error
	if !errors.As(err, &sqliteErr) {
		return err
	}
	switch sqliteErr.Code() & 0xff {
	case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED:
		return TaskCreateConflictError{Reason: TaskCreateConflictSerialization, Cause: err}
	default:
		return err
	}
}

// Sentinel errors returned by the workflow store. Callers (including tests)
// must match these with errors.Is/errors.As rather than comparing the rendered
// message text, which is free to change without affecting behavior. Dynamic
// context (ids, keys, counts) is wrapped via fmt.Errorf("... %w", Err...).
var (
	// ErrWorkflowIDRequired is returned when a Workflow identity is required
	// but missing.
	ErrWorkflowIDRequired = errors.New("workflow id is required")

	// ErrWorkflowNameRequired is returned when a workflow name is blank.
	ErrWorkflowNameRequired = errors.New("workflow name is required")

	// ErrCommentAuthorKindInvalid is returned when a comment author kind is not
	// one of the accepted values.
	ErrCommentAuthorKindInvalid = errors.New("comment author kind must be user or agent")

	// ErrBelongsToOtherWorkflow is returned when a graph element references a
	// workflow it does not belong to.
	ErrBelongsToOtherWorkflow = errors.New("workflow graph element belongs to a different workflow")

	// Source-workspace edit guards. Each names a distinct condition under which
	// a task's source workspace cannot be changed.
	ErrSourceWorkspaceAfterAutomation = errors.New("cannot edit source workspace after automation starts")
	ErrSourceWorkspaceNotInProject    = errors.New("source workspace does not belong to project")

	// ErrApprovalIDRequired is returned when an approval id is required but
	// blank.
	ErrApprovalIDRequired = errors.New("approval id is required")

	// ErrCurrentNodePendingApproval is returned when an operation tries to
	// execute a current node that is awaiting its Approval.
	ErrCurrentNodePendingApproval = errors.New("current node has a pending approval")

	// ErrTaskAskNotPending is returned when resolving a task waiting-ask that has
	// no matching pending ask.
	ErrTaskAskNotPending = errors.New("task ask is not pending")

	// ErrReplacementDefaultInvalid is returned when an unlink replacement-default
	// link is missing or self-referential.
	ErrReplacementDefaultInvalid = errors.New("replacement default workflow link is invalid")

	// ErrNodeHasTaskHistory and ErrEdgeHasTaskHistory guard physical deletion of
	// graph elements that are still referenced by task history.
	ErrNodeHasTaskHistory      = errors.New("workflow node has task history references")
	ErrEdgeHasTaskHistory      = errors.New("workflow edge has task history references")
	ErrWorkflowStartNodeExists = errors.New("workflow already has a start node; edit the existing start node instead")

	// ErrProjectLabelNotFound marks a Project-qualified label lookup that found
	// no matching label.
	ErrProjectLabelNotFound = errors.New("project label not found")
	// ErrProjectLabelNameConflict marks a case-fold-equivalent name collision
	// within one Project catalog.
	ErrProjectLabelNameConflict = errors.New("project label name conflicts with an existing label")
	// ErrProjectLabelLimitReached marks a Project catalog at its bounded size.
	ErrProjectLabelLimitReached = errors.New("project label catalog limit reached")
	// ErrProjectLabelOrderInvalid marks a reorder request that is not an exact
	// permutation of the project's current label catalog.
	ErrProjectLabelOrderInvalid = errors.New("project label order is invalid")
	ErrTaskLabelTaskNotFound    = errors.New("task for label assignment not found")
	ErrTaskLabelNotFound        = errors.New("task label reference not found")
	ErrTaskLabelWrongProject    = errors.New("task label belongs to another project")

	// Manual-move guards. Each names a distinct unsupported/invalid manual-move
	// condition.
	ErrManualMoveNoSourcePosition = errors.New("manual move has no active placement or pending approval to move from")
)

type ProjectLabelNotFoundError struct {
	ProjectID string
	LabelID   string
}

func (e ProjectLabelNotFoundError) Error() string {
	return fmt.Sprintf("project label %q was not found in project %q", e.LabelID, e.ProjectID)
}

func (e ProjectLabelNotFoundError) Is(target error) bool {
	return target == ErrProjectLabelNotFound
}

type ProjectLabelNameConflictError struct {
	ProjectID string
	Name      string
}

func (e ProjectLabelNameConflictError) Error() string {
	return fmt.Sprintf("project %q already has a label named %q", e.ProjectID, e.Name)
}

func (e ProjectLabelNameConflictError) Is(target error) bool {
	return target == ErrProjectLabelNameConflict
}

type ProjectLabelLimitError struct {
	ProjectID string
	Limit     int
}

func (e ProjectLabelLimitError) Error() string {
	return fmt.Sprintf("project %q has reached its %d-label catalog limit", e.ProjectID, e.Limit)
}

func (e ProjectLabelLimitError) Is(target error) bool {
	return target == ErrProjectLabelLimitReached
}

type ProjectLabelOrderError struct {
	ProjectID string
}

func (e ProjectLabelOrderError) Error() string {
	return fmt.Sprintf("project %q label order is invalid", e.ProjectID)
}

func (e ProjectLabelOrderError) Is(target error) bool {
	return target == ErrProjectLabelOrderInvalid
}

type TaskLabelTaskNotFoundError struct {
	TaskID string
}

func (e TaskLabelTaskNotFoundError) Error() string {
	return fmt.Sprintf("task %q was not found for label assignment", e.TaskID)
}

func (e TaskLabelTaskNotFoundError) Is(target error) bool {
	return target == ErrTaskLabelTaskNotFound
}

type TaskLabelNotFoundError struct {
	LabelID string
}

func (e TaskLabelNotFoundError) Error() string {
	return fmt.Sprintf("task label %q was not found", e.LabelID)
}

func (e TaskLabelNotFoundError) Is(target error) bool {
	return target == ErrTaskLabelNotFound
}

type TaskLabelWrongProjectError struct {
	TaskID         string
	TaskProjectID  string
	LabelID        string
	LabelProjectID string
}

func (e TaskLabelWrongProjectError) Error() string {
	return fmt.Sprintf(
		"label %q belongs to project %q, not task %q project %q",
		e.LabelID,
		e.LabelProjectID,
		e.TaskID,
		e.TaskProjectID,
	)
}

func (e TaskLabelWrongProjectError) Is(target error) bool {
	return target == ErrTaskLabelWrongProject
}

type TaskLabelMutationErrorReason string

const (
	TaskLabelMutationTooManyAdd      TaskLabelMutationErrorReason = "too_many_add_label_ids"
	TaskLabelMutationTooManyRemove   TaskLabelMutationErrorReason = "too_many_remove_label_ids"
	TaskLabelMutationDuplicateAdd    TaskLabelMutationErrorReason = "duplicate_add_label_id"
	TaskLabelMutationDuplicateRemove TaskLabelMutationErrorReason = "duplicate_remove_label_id"
	TaskLabelMutationOverlap         TaskLabelMutationErrorReason = "add_remove_overlap"
	TaskLabelMutationInvalidID       TaskLabelMutationErrorReason = "invalid_label_id"
)

type TaskLabelMutationError struct {
	Reason  TaskLabelMutationErrorReason
	Field   string
	LabelID *string
	Limit   *int
	Cause   error
}

func (e TaskLabelMutationError) Error() string {
	switch e.Reason {
	case TaskLabelMutationTooManyAdd, TaskLabelMutationTooManyRemove:
		return fmt.Sprintf("%s must contain at most %d label IDs", e.Field, *e.Limit)
	case TaskLabelMutationDuplicateAdd, TaskLabelMutationDuplicateRemove:
		return fmt.Sprintf("%s contains duplicate label ID %q", e.Field, *e.LabelID)
	case TaskLabelMutationOverlap:
		return fmt.Sprintf("label ID %q cannot be both added and removed", *e.LabelID)
	case TaskLabelMutationInvalidID:
		return fmt.Sprintf("%s contains invalid label ID %q", e.Field, *e.LabelID)
	default:
		return "task label mutation is invalid"
	}
}

func (e TaskLabelMutationError) Unwrap() error {
	return e.Cause
}

// ErrWorkflowValidationFailed marks any WorkflowValidationError so callers can
// detect a validation failure generically with errors.Is.
var ErrWorkflowValidationFailed = errors.New("workflow validation failed")

// WorkflowValidationError reports that a workflow definition failed validation
// and retains the authoritative blocking diagnostics.
type WorkflowValidationError struct {
	Diagnostics []workflow.ValidationError
}

func (e WorkflowValidationError) Error() string {
	diagnostics := make([]string, 0, len(e.Diagnostics))
	for _, diagnostic := range e.Diagnostics {
		rendered := strings.TrimSpace(diagnostic.Message)
		if rendered == "" {
			rendered = string(diagnostic.Code)
		}
		diagnostics = append(diagnostics, rendered)
	}
	return fmt.Sprintf("workflow validation failed: [%s]", strings.Join(diagnostics, "; "))
}

// Is reports a match against ErrWorkflowValidationFailed so a generic
// "validation failed" check succeeds for any code set.
func (e WorkflowValidationError) Is(target error) bool {
	return target == ErrWorkflowValidationFailed
}

// HasCode reports whether the validation failure includes the given code.
func (e WorkflowValidationError) HasCode(code workflow.ValidationErrorCode) bool {
	for _, diagnostic := range e.Diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

// CompletionCode is the stable, structured identifier for a Current Node completion
// validation issue. The string values are a cross-package contract consumed by
// server/workflowruntime and must not change.
type CompletionCode = string

const (
	CompletionCodeTransitionIDRequired       CompletionCode = "transition_id_required"
	CompletionCodeInvalidTransitionID        CompletionCode = "invalid_transition_id"
	CompletionCodeNoOutgoingTransition       CompletionCode = "no_outgoing_transition"
	CompletionCodeRequiredOutputMissing      CompletionCode = "required_output_missing"
	CompletionCodeUnknownOutputField         CompletionCode = "unknown_output_field"
	CompletionCodeOutputFieldRequired        CompletionCode = "output_field_required"
	CompletionCodeOutputTooLarge             CompletionCode = "output_too_large"
	CompletionCodeCommentaryTooLarge         CompletionCode = "commentary_too_large"
	CompletionCodeUnavailableTargetAgentRole CompletionCode = "workflow.target_agent.unavailable_role"
)

// HasCode reports whether the completion validation error contains an issue with
// the given code, letting callers assert structured behavior instead of message
// wording.
func (e CompletionValidationError) HasCode(code CompletionCode) bool {
	for _, issue := range e.Issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}
