package workflowstore

import (
	"errors"
	"fmt"
	"strings"

	"core/server/workflow"
)

// Sentinel errors returned by the workflow store. Callers (including tests)
// must match these with errors.Is/errors.As rather than comparing the rendered
// message text, which is free to change without affecting behavior. Dynamic
// context (ids, keys, counts) is wrapped via fmt.Errorf("... %w", Err...).
var (
	// ErrWorkflowNameRequired is returned when a workflow name is blank.
	ErrWorkflowNameRequired = errors.New("workflow name is required")

	// ErrEventResourceRequired and ErrEventActionRequired guard
	// PublishWorkflowEvent inputs.
	ErrEventResourceRequired = errors.New("event resource is required")
	ErrEventActionRequired   = errors.New("event action is required")

	// ErrCommentAuthorKindInvalid is returned when a comment author kind is not
	// one of the accepted values.
	ErrCommentAuthorKindInvalid = errors.New("comment author kind must be user or agent")

	// ErrBelongsToOtherWorkflow is returned when a graph element references a
	// workflow it does not belong to.
	ErrBelongsToOtherWorkflow = errors.New("workflow graph element belongs to a different workflow")

	// ErrTaskCanceled is returned when an operation targets a canceled task.
	ErrTaskCanceled = errors.New("task is canceled")

	// Source-workspace edit guards. Each names a distinct condition under which
	// a task's source workspace cannot be changed.
	ErrSourceWorkspaceForCanceledTask            = errors.New("cannot edit source workspace for canceled task")
	ErrSourceWorkspaceAfterAutomation            = errors.New("cannot edit source workspace after automation starts")
	ErrSourceWorkspaceNotInProject               = errors.New("source workspace does not belong to project")
	ErrTaskExecutionTargetNotMaterialized        = errors.New("task execution target is not materialized")
	ErrTaskExecutionRootUnavailable              = errors.New("task execution root is unavailable")
	ErrTaskExecutionTargetClaimChanged           = errors.New("task execution target claim changed")
	ErrTaskExecutionTargetReplacementUnavailable = errors.New("task execution target replacement is unavailable")
	ErrTaskExecutionTargetNegotiationRequired    = errors.New("task execution target negotiation is required")
	ErrTaskExecutionTargetNegotiationChanged     = errors.New("task execution target negotiation changed")
	ErrTaskExecutionTargetNegotiationInProgress  = errors.New("task execution target negotiation is in progress")
	ErrLegacyTaskExecutionTargetMissing          = errors.New("legacy task execution target is unavailable")

	// ErrRunAlreadyCompleted is returned when completing a run that already has a
	// completion timestamp.
	ErrRunAlreadyCompleted = errors.New("run already completed")

	// ErrStaleRunGeneration is returned when an optimistic-generation guard fails.
	ErrStaleRunGeneration = errors.New("stale workflow run generation")

	// ErrInvalidEffectiveCompletionMode is returned when a run completion-mode
	// snapshot value is not one of the runtime-supported modes.
	ErrInvalidEffectiveCompletionMode = errors.New("invalid workflow effective completion mode")

	// ErrTransitionIDRequired is returned when a transition id is required but
	// blank.
	ErrTransitionIDRequired = errors.New("transition id is required")

	// ErrRunIDRequired is returned when a run id must be supplied to
	// disambiguate among multiple matching runs.
	ErrRunIDRequired = errors.New("run_id is required")

	// ErrTaskAskNotPending is returned when resolving a task waiting-ask that has
	// no matching pending ask.
	ErrTaskAskNotPending = errors.New("task ask is not pending")

	// ErrReplacementDefaultInvalid is returned when an unlink replacement-default
	// link is missing or self-referential.
	ErrReplacementDefaultInvalid = errors.New("replacement default workflow link is invalid")

	// ErrNodeHasTaskHistory and ErrEdgeHasTaskHistory guard physical deletion of
	// graph elements that are still referenced by task history.
	ErrNodeHasTaskHistory = errors.New("workflow node has task history references")
	ErrEdgeHasTaskHistory = errors.New("workflow edge has task history references")

	// Manual-move guards. Each names a distinct unsupported/invalid manual-move
	// condition.
	ErrManualMoveSelectedContextSource      = errors.New("manual move with selected context source is not supported")
	ErrManualMovePreviousTargetContext      = errors.New("manual move with previous target context source is not supported")
	ErrManualMoveContinueSessionNeedsSource = errors.New("continue_session requires source session for manual move")
	ErrManualMoveApprovalNeedsSourceRun     = errors.New("manual move requiring approval needs a source run")
	ErrManualMoveDuringParallelBatch        = errors.New("manual move during active parallel batch is not supported")
	ErrManualMoveDuringActiveRun            = errors.New("manual move during active workflow run is not supported")
	ErrManualMoveExecutableTargetNeedsEdge  = errors.New("manual move to executable target requires a workflow edge")
	ErrManualMoveNoSourcePosition           = errors.New("manual move has no active placement or pending approval to move from")
	ErrManualMoveMultiplePendingApprovals   = errors.New("manual move with multiple pending approvals is not supported")
	ErrManualMovePendingApprovalResolved    = errors.New("pending approval was resolved before the manual move could override it")
)

// LegacyTaskExecutionTargetMissingError identifies a historical task that
// predates execution targets but no longer has its managed worktree attachment.
// The workflow policy cannot safely reconstruct that lost execution history.
type LegacyTaskExecutionTargetMissingError struct {
	TaskID workflow.TaskID
}

func (e LegacyTaskExecutionTargetMissingError) Error() string {
	return fmt.Sprintf("legacy task %q has no managed execution root: %v", e.TaskID, ErrLegacyTaskExecutionTargetMissing)
}

func (e LegacyTaskExecutionTargetMissingError) Is(target error) bool {
	return target == ErrLegacyTaskExecutionTargetMissing
}

// ContextSourceKind identifies which context-source resolution failed to find a
// completed run.
type ContextSourceKind string

const (
	ContextSourceKindSelected       ContextSourceKind = "selected"
	ContextSourceKindPreviousTarget ContextSourceKind = "previous_target"
)

// ContextSourceNoCompletedRunError is returned when a context source resolves to
// a node that has no completed run for the task. It carries the node key and the
// resolution kind so callers can inspect both without parsing the message.
type ContextSourceNoCompletedRunError struct {
	Kind    ContextSourceKind
	NodeKey string
}

func (e ContextSourceNoCompletedRunError) Error() string {
	switch e.Kind {
	case ContextSourceKindPreviousTarget:
		return fmt.Sprintf("previous target context source node %q has no completed run for task", e.NodeKey)
	default:
		return fmt.Sprintf("selected context source node %q has no completed run for task", e.NodeKey)
	}
}

// ErrWorkflowValidationFailed marks any WorkflowValidationError so callers can
// detect a validation failure generically with errors.Is.
var ErrWorkflowValidationFailed = errors.New("workflow validation failed")

// WorkflowValidationError reports that a workflow definition failed validation,
// carrying the blocking validation codes so callers can assert on a specific
// code with HasCode rather than parsing the rendered message.
type WorkflowValidationError struct {
	Codes []workflow.ValidationErrorCode
}

func (e WorkflowValidationError) Error() string {
	codes := make([]string, 0, len(e.Codes))
	for _, code := range e.Codes {
		codes = append(codes, string(code))
	}
	return fmt.Sprintf("workflow validation failed: [%s]", strings.Join(codes, " "))
}

// Is reports a match against ErrWorkflowValidationFailed so a generic
// "validation failed" check succeeds for any code set.
func (e WorkflowValidationError) Is(target error) bool {
	return target == ErrWorkflowValidationFailed
}

// HasCode reports whether the validation failure includes the given code.
func (e WorkflowValidationError) HasCode(code workflow.ValidationErrorCode) bool {
	for _, c := range e.Codes {
		if c == code {
			return true
		}
	}
	return false
}

// CompletionCode is the stable, structured identifier for a run-completion
// validation issue. The string values are a cross-package contract consumed by
// server/workflowruntime and must not change.
type CompletionCode = string

const (
	CompletionCodeTransitionIDRequired  CompletionCode = "transition_id_required"
	CompletionCodeInvalidTransitionID   CompletionCode = "invalid_transition_id"
	CompletionCodeNoOutgoingTransition  CompletionCode = "no_outgoing_transition"
	CompletionCodeRequiredOutputMissing CompletionCode = "required_output_missing"
	CompletionCodeUnknownOutputField    CompletionCode = "unknown_output_field"
	CompletionCodeOutputFieldRequired   CompletionCode = "output_field_required"
	CompletionCodeOutputTooLarge        CompletionCode = "output_too_large"
	CompletionCodeCommentaryTooLarge    CompletionCode = "commentary_too_large"
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
