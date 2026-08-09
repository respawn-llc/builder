package serverapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/shared/protocol"
)

type WorkflowExecutionTargetMode string

const (
	WorkflowExecutionTargetModeNone                WorkflowExecutionTargetMode = "none"
	WorkflowExecutionTargetModeHead                WorkflowExecutionTargetMode = "head"
	WorkflowExecutionTargetModeDefaultBranch       WorkflowExecutionTargetMode = "default_branch"
	WorkflowExecutionTargetModeCustomRef           WorkflowExecutionTargetMode = "custom_ref"
	WorkflowExecutionTargetModeAskOnFirstExecution WorkflowExecutionTargetMode = "ask_on_first_execution"
)

type WorkflowExecutionTargetConfiguration struct {
	Mode      WorkflowExecutionTargetMode `json:"mode"`
	CustomRef *string                     `json:"custom_ref,omitempty"`
}

type WorkflowExecutionTargetSelection struct {
	Mode      WorkflowExecutionTargetMode `json:"mode"`
	CustomRef *string                     `json:"custom_ref,omitempty"`
}

type WorkflowExecutionTargetProvenance string

const (
	WorkflowExecutionTargetProvenanceResolved       WorkflowExecutionTargetProvenance = "resolved"
	WorkflowExecutionTargetProvenanceLegacyObserved WorkflowExecutionTargetProvenance = "legacy_observed"
)

type WorkflowExecutionTarget struct {
	Mode         WorkflowExecutionTargetMode       `json:"mode"`
	RequestedRef *string                           `json:"requested_ref,omitempty"`
	ResolvedRef  *string                           `json:"resolved_ref,omitempty"`
	CommitOID    *string                           `json:"commit_oid,omitempty"`
	Provenance   WorkflowExecutionTargetProvenance `json:"provenance"`
}

type WorkflowExecutionTargetConfiguredTarget struct {
	Mode         WorkflowExecutionTargetMode `json:"mode"`
	RequestedRef *string                     `json:"requested_ref,omitempty"`
}

type WorkflowExecutionTargetSelectionReason string

const (
	WorkflowExecutionTargetSelectionReasonPolicyRequiresSelection     WorkflowExecutionTargetSelectionReason = "policy_requires_selection"
	WorkflowExecutionTargetSelectionReasonConfiguredTargetUnavailable WorkflowExecutionTargetSelectionReason = "configured_target_unavailable"
)

type WorkflowExecutionTargetUnavailableCause string

const (
	WorkflowExecutionTargetUnavailableCauseInvalidRevision        WorkflowExecutionTargetUnavailableCause = "invalid_revision"
	WorkflowExecutionTargetUnavailableCauseNonCommit              WorkflowExecutionTargetUnavailableCause = "non_commit"
	WorkflowExecutionTargetUnavailableCauseDefaultBranchMissing   WorkflowExecutionTargetUnavailableCause = "default_branch_missing"
	WorkflowExecutionTargetUnavailableCauseDefaultBranchAmbiguous WorkflowExecutionTargetUnavailableCause = "default_branch_ambiguous"
	WorkflowExecutionTargetUnavailableCauseGitFailure             WorkflowExecutionTargetUnavailableCause = "git_failure"
)

type WorkflowExecutionTargetSelectionRequirement struct {
	Reason           WorkflowExecutionTargetSelectionReason   `json:"reason"`
	ConfiguredTarget *WorkflowExecutionTargetConfiguredTarget `json:"configured_target,omitempty"`
	UnavailableCause WorkflowExecutionTargetUnavailableCause  `json:"unavailable_cause,omitempty"`
}

type WorkflowExecutionTargetActionOutcome string

const (
	WorkflowExecutionTargetActionOutcomeApplied                        WorkflowExecutionTargetActionOutcome = "applied"
	WorkflowExecutionTargetActionOutcomeNoOp                           WorkflowExecutionTargetActionOutcome = "no_op"
	WorkflowExecutionTargetActionOutcomeSelectionRequired              WorkflowExecutionTargetActionOutcome = "selection_required"
	WorkflowExecutionTargetActionOutcomeDependencyConfirmationRequired WorkflowExecutionTargetActionOutcome = "dependency_confirmation_required"
)

type WorkflowExecutionTargetResolutionErrorCode string

const (
	WorkflowExecutionTargetResolutionErrorInvalidRevision WorkflowExecutionTargetResolutionErrorCode = "invalid_revision"
	WorkflowExecutionTargetResolutionErrorNonCommit       WorkflowExecutionTargetResolutionErrorCode = "non_commit"
	WorkflowExecutionTargetResolutionErrorGitFailure      WorkflowExecutionTargetResolutionErrorCode = "git_failure"
)

type WorkflowExecutionTargetResolutionError struct {
	Code         WorkflowExecutionTargetResolutionErrorCode
	RequestedRef string
}

type WorkflowLockedExecutionTargetCause string

const (
	WorkflowLockedExecutionTargetCauseDetachedHead     WorkflowLockedExecutionTargetCause = "detached_head"
	WorkflowLockedExecutionTargetCauseInvalidRoot      WorkflowLockedExecutionTargetCause = "invalid_root"
	WorkflowLockedExecutionTargetCauseRootInaccessible WorkflowLockedExecutionTargetCause = "root_inaccessible"
	WorkflowLockedExecutionTargetCauseMissingBranch    WorkflowLockedExecutionTargetCause = "missing_branch"
	WorkflowLockedExecutionTargetCauseConflict         WorkflowLockedExecutionTargetCause = "conflict"
	WorkflowLockedExecutionTargetCauseGitFailure       WorkflowLockedExecutionTargetCause = "git_failure"
)

type WorkflowLockedExecutionTargetError struct {
	Cause WorkflowLockedExecutionTargetCause
}

func (e *WorkflowLockedExecutionTargetError) Error() string {
	if e == nil {
		return "locked workflow execution target is unavailable"
	}
	return "locked workflow execution target is unavailable: " + string(e.Cause)
}

func (e *WorkflowLockedExecutionTargetError) RPCErrorCode() int {
	return protocol.ErrCodeWorkflowLockedExecutionTarget
}

func (e *WorkflowLockedExecutionTargetError) RPCErrorData() json.RawMessage {
	if e == nil {
		return nil
	}
	return marshalRPCErrorData(struct {
		Type  string                             `json:"type"`
		Cause WorkflowLockedExecutionTargetCause `json:"cause"`
	}{
		Type:  "workflow_locked_execution_target_error",
		Cause: e.Cause,
	})
}

func (e *WorkflowExecutionTargetResolutionError) Error() string {
	if e == nil {
		return "workflow execution target resolution failed"
	}
	return "workflow execution target resolution failed: " + string(e.Code)
}

func (e *WorkflowExecutionTargetResolutionError) RPCErrorCode() int {
	return protocol.ErrCodeWorkflowExecutionTargetResolution
}

func (e *WorkflowExecutionTargetResolutionError) RPCErrorData() json.RawMessage {
	if e == nil {
		return nil
	}
	return marshalRPCErrorData(struct {
		Type         string                                     `json:"type"`
		Code         WorkflowExecutionTargetResolutionErrorCode `json:"code"`
		RequestedRef string                                     `json:"requested_ref"`
	}{
		Type:         "workflow_execution_target_resolution_error",
		Code:         e.Code,
		RequestedRef: e.RequestedRef,
	})
}

func DecodeWorkflowExecutionTargetResolutionError(data json.RawMessage, message string) error {
	var envelope struct {
		Type         string                                     `json:"type"`
		Code         WorkflowExecutionTargetResolutionErrorCode `json:"code"`
		RequestedRef string                                     `json:"requested_ref"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil || envelope.Type != "workflow_execution_target_resolution_error" || !validWorkflowExecutionTargetResolutionErrorCode(envelope.Code) || strings.TrimSpace(envelope.RequestedRef) == "" {
		return errors.New(strings.TrimSpace(message))
	}
	return &WorkflowExecutionTargetResolutionError{Code: envelope.Code, RequestedRef: envelope.RequestedRef}
}

func DecodeWorkflowLockedExecutionTargetError(data json.RawMessage, message string) error {
	var envelope struct {
		Type  string                             `json:"type"`
		Cause WorkflowLockedExecutionTargetCause `json:"cause"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil ||
		envelope.Type != "workflow_locked_execution_target_error" ||
		!validWorkflowLockedExecutionTargetCause(envelope.Cause) {
		return errors.New(strings.TrimSpace(message))
	}
	return &WorkflowLockedExecutionTargetError{Cause: envelope.Cause}
}

func (p WorkflowExecutionTargetConfiguration) Validate(allowIncompleteCustomRef bool) error {
	if !validWorkflowExecutionTargetPolicyMode(p.Mode) {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "execution_target.mode", "execution_target.mode is invalid")
	}
	if p.Mode != WorkflowExecutionTargetModeCustomRef {
		if p.CustomRef != nil {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "execution_target.custom_ref", "execution_target.custom_ref is only valid for custom_ref")
		}
		return nil
	}
	if p.CustomRef == nil {
		if allowIncompleteCustomRef {
			return nil
		}
		return workflowRequestError(WorkflowRequestErrorRequired, "execution_target.custom_ref", "execution_target.custom_ref is required")
	}
	if strings.TrimSpace(*p.CustomRef) == "" {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "execution_target.custom_ref", "execution_target.custom_ref must be non-blank")
	}
	return nil
}

func (s WorkflowExecutionTargetSelection) Validate() error {
	if !validWorkflowConcreteExecutionTargetMode(s.Mode) {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "execution_target.mode", "execution_target.mode must be a concrete target")
	}
	if s.Mode != WorkflowExecutionTargetModeCustomRef {
		if s.CustomRef != nil {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "execution_target.custom_ref", "execution_target.custom_ref is only valid for custom_ref")
		}
		return nil
	}
	if s.CustomRef == nil {
		return workflowRequestError(WorkflowRequestErrorRequired, "execution_target.custom_ref", "execution_target.custom_ref is required")
	}
	if strings.TrimSpace(*s.CustomRef) == "" {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "execution_target.custom_ref", "execution_target.custom_ref must be non-blank")
	}
	return nil
}

func (t WorkflowExecutionTarget) Validate() error {
	if t.Provenance != WorkflowExecutionTargetProvenanceResolved && t.Provenance != WorkflowExecutionTargetProvenanceLegacyObserved {
		return errors.New("execution target provenance is invalid")
	}
	if t.Mode == WorkflowExecutionTargetModeNone {
		if t.Provenance != WorkflowExecutionTargetProvenanceResolved {
			return errors.New("none execution target provenance must be resolved")
		}
		if t.RequestedRef != nil || t.ResolvedRef != nil || t.CommitOID != nil {
			return errors.New("none execution target cannot include managed target facts")
		}
		return nil
	}
	if !validWorkflowManagedExecutionTargetMode(t.Mode) {
		return errors.New("execution target mode is invalid")
	}
	for _, fact := range []struct {
		name  string
		value *string
	}{
		{"requested_ref", t.RequestedRef},
		{"commit_oid", t.CommitOID},
	} {
		if fact.value == nil || strings.TrimSpace(*fact.value) == "" {
			return errors.New("managed execution target " + fact.name + " is required")
		}
	}
	if t.ResolvedRef != nil && strings.TrimSpace(*t.ResolvedRef) == "" {
		return errors.New("execution target resolved_ref must be non-blank when present")
	}
	return nil
}

func (r WorkflowExecutionTargetSelectionRequirement) Validate() error {
	switch r.Reason {
	case WorkflowExecutionTargetSelectionReasonPolicyRequiresSelection:
		if r.ConfiguredTarget != nil || r.UnavailableCause != "" {
			return errors.New("policy selection requirement cannot include configured target failure")
		}
	case WorkflowExecutionTargetSelectionReasonConfiguredTargetUnavailable:
		if r.ConfiguredTarget == nil || !validWorkflowExecutionTargetUnavailableCause(r.UnavailableCause) {
			return errors.New("configured target requirement requires configured target and unavailable cause")
		}
		if !validWorkflowManagedExecutionTargetMode(r.ConfiguredTarget.Mode) {
			return errors.New("configured target requirement mode must be managed")
		}
		if r.ConfiguredTarget.Mode == WorkflowExecutionTargetModeCustomRef && (r.ConfiguredTarget.RequestedRef == nil || strings.TrimSpace(*r.ConfiguredTarget.RequestedRef) == "") {
			return errors.New("configured custom ref requirement requires requested ref")
		}
		if r.ConfiguredTarget.Mode != WorkflowExecutionTargetModeCustomRef && r.ConfiguredTarget.RequestedRef != nil {
			return errors.New("configured non-custom requirement cannot include requested ref")
		}
	default:
		return errors.New("execution target selection requirement reason is invalid")
	}
	return nil
}

func validWorkflowExecutionTargetUnavailableCause(cause WorkflowExecutionTargetUnavailableCause) bool {
	switch cause {
	case WorkflowExecutionTargetUnavailableCauseInvalidRevision,
		WorkflowExecutionTargetUnavailableCauseNonCommit,
		WorkflowExecutionTargetUnavailableCauseDefaultBranchMissing,
		WorkflowExecutionTargetUnavailableCauseDefaultBranchAmbiguous,
		WorkflowExecutionTargetUnavailableCauseGitFailure:
		return true
	default:
		return false
	}
}

func validWorkflowExecutionTargetPolicyMode(mode WorkflowExecutionTargetMode) bool {
	switch mode {
	case WorkflowExecutionTargetModeNone, WorkflowExecutionTargetModeHead, WorkflowExecutionTargetModeDefaultBranch, WorkflowExecutionTargetModeCustomRef, WorkflowExecutionTargetModeAskOnFirstExecution:
		return true
	default:
		return false
	}
}

func validWorkflowConcreteExecutionTargetMode(mode WorkflowExecutionTargetMode) bool {
	switch mode {
	case WorkflowExecutionTargetModeNone, WorkflowExecutionTargetModeHead, WorkflowExecutionTargetModeDefaultBranch, WorkflowExecutionTargetModeCustomRef:
		return true
	default:
		return false
	}
}

func validWorkflowManagedExecutionTargetMode(mode WorkflowExecutionTargetMode) bool {
	switch mode {
	case WorkflowExecutionTargetModeHead, WorkflowExecutionTargetModeDefaultBranch, WorkflowExecutionTargetModeCustomRef:
		return true
	default:
		return false
	}
}

func validWorkflowExecutionTargetResolutionErrorCode(code WorkflowExecutionTargetResolutionErrorCode) bool {
	switch code {
	case WorkflowExecutionTargetResolutionErrorInvalidRevision, WorkflowExecutionTargetResolutionErrorNonCommit, WorkflowExecutionTargetResolutionErrorGitFailure:
		return true
	default:
		return false
	}
}

func validWorkflowLockedExecutionTargetCause(cause WorkflowLockedExecutionTargetCause) bool {
	switch cause {
	case WorkflowLockedExecutionTargetCauseDetachedHead,
		WorkflowLockedExecutionTargetCauseInvalidRoot,
		WorkflowLockedExecutionTargetCauseRootInaccessible,
		WorkflowLockedExecutionTargetCauseMissingBranch,
		WorkflowLockedExecutionTargetCauseConflict,
		WorkflowLockedExecutionTargetCauseGitFailure:
		return true
	default:
		return false
	}
}

func (r WorkflowTaskStartResponse) Validate() error {
	if r.Outcome == WorkflowTaskActionOutcomeApplied {
		if r.Applied == nil || r.SelectionRequired != nil || r.UnsatisfiedDependencyCount != nil {
			return errors.New("start action response applied outcome requires only applied payload")
		}
		return validateWorkflowTaskStartApplied(*r.Applied)
	}
	if r.Outcome == WorkflowTaskActionOutcomeSelectionRequired {
		if r.Applied != nil || r.SelectionRequired == nil || r.UnsatisfiedDependencyCount != nil {
			return errors.New("start action response selection_required outcome requires only selection requirement")
		}
		return r.SelectionRequired.Validate()
	}
	if r.Outcome == WorkflowTaskActionOutcomeDependencyConfirmationRequired {
		if r.Applied != nil || r.SelectionRequired != nil || r.UnsatisfiedDependencyCount == nil || *r.UnsatisfiedDependencyCount <= 0 {
			return errors.New("start action response dependency confirmation outcome requires only a positive count")
		}
		return nil
	}
	return errors.New("start action response outcome is invalid")
}

func (r WorkflowTaskApproveResponse) Validate() error {
	if r.Outcome == WorkflowExecutionTargetActionOutcomeApplied {
		if r.Applied == nil || r.SelectionRequired != nil {
			return errors.New("approve action response applied outcome requires only applied payload")
		}
		return validateWorkflowTaskApproveApplied(*r.Applied)
	}
	if r.Outcome == WorkflowExecutionTargetActionOutcomeSelectionRequired {
		if r.Applied != nil || r.SelectionRequired == nil {
			return errors.New("approve action response selection_required outcome requires only selection requirement")
		}
		return r.SelectionRequired.Validate()
	}
	return errors.New("approve action response outcome is invalid")
}

func (r WorkflowTaskResumeResponse) Validate() error {
	if r.Outcome == WorkflowExecutionTargetActionOutcomeApplied {
		if r.Applied == nil || r.SelectionRequired != nil {
			return errors.New("resume action response applied outcome requires only applied payload")
		}
		return validateWorkflowTaskCurrentNodes(r.Applied.CurrentNodes, "resume applied payload")
	}
	if r.Outcome == WorkflowExecutionTargetActionOutcomeSelectionRequired {
		if r.Applied != nil || r.SelectionRequired == nil {
			return errors.New("resume action response selection_required outcome requires only selection requirement")
		}
		return r.SelectionRequired.Validate()
	}
	return errors.New("resume action response outcome is invalid")
}

func (r WorkflowTaskMoveResponse) Validate() error {
	if r.Outcome == WorkflowExecutionTargetActionOutcomeNoOp {
		if r.NoOp == nil || r.Applied != nil || r.SelectionRequired != nil || r.UnsatisfiedDependencyCount != nil {
			return errors.New("move action response no_op outcome requires only no_op payload")
		}
		return validateWorkflowTaskMoveNoOp(*r.NoOp)
	}
	if r.Outcome == WorkflowExecutionTargetActionOutcomeApplied {
		if r.Applied == nil || r.NoOp != nil || r.SelectionRequired != nil || r.UnsatisfiedDependencyCount != nil {
			return errors.New("move action response applied outcome requires only applied payload")
		}
		return validateWorkflowTaskMoveApplied(*r.Applied)
	}
	if r.Outcome == WorkflowExecutionTargetActionOutcomeSelectionRequired {
		if r.Applied != nil || r.NoOp != nil || r.SelectionRequired == nil || r.UnsatisfiedDependencyCount != nil {
			return errors.New("move action response selection_required outcome requires only selection requirement")
		}
		return r.SelectionRequired.Validate()
	}
	if r.Outcome == WorkflowExecutionTargetActionOutcomeDependencyConfirmationRequired {
		if r.Applied != nil || r.NoOp != nil || r.SelectionRequired != nil ||
			r.UnsatisfiedDependencyCount == nil || *r.UnsatisfiedDependencyCount <= 0 {
			return errors.New("move action response dependency confirmation outcome requires only a positive count")
		}
		return nil
	}
	return errors.New("move action response outcome is invalid")
}

func validateWorkflowTaskMoveNoOp(noOp WorkflowTaskMoveNoOp) error {
	return validateWorkflowTaskCurrentNodes(noOp.CurrentNodes, "move no_op payload")
}

func validateWorkflowTaskCurrentNodes(currentNodes []WorkflowTaskCurrentNode, payload string) error {
	if len(currentNodes) == 0 {
		return fmt.Errorf("%s requires current_nodes", payload)
	}
	for index, currentNode := range currentNodes {
		if err := validateWorkflowTaskCurrentNode(currentNode); err != nil {
			return fmt.Errorf("%s current node at index %d: %w", payload, index, err)
		}
	}
	return nil
}

func validateWorkflowTaskStartApplied(applied WorkflowTaskStartApplied) error {
	if len(applied.CurrentNodes) == 0 {
		return errors.New("start applied payload requires current_nodes")
	}
	for index, currentNode := range applied.CurrentNodes {
		if strings.TrimSpace(currentNode.NodeID) == "" {
			return fmt.Errorf("start applied current node at index %d requires node_id", index)
		}
		if currentNode.TransitionBranchKey != nil && strings.TrimSpace(*currentNode.TransitionBranchKey) == "" {
			return fmt.Errorf("start applied current node at index %d has blank transition_branch_key", index)
		}
		if currentNode.SessionID != nil && strings.TrimSpace(*currentNode.SessionID) == "" {
			return fmt.Errorf("start applied current node at index %d has blank session_id", index)
		}
	}
	return nil
}

func validateWorkflowTaskApproveApplied(applied WorkflowTaskApproveApplied) error {
	if strings.TrimSpace(applied.TaskID) == "" || len(applied.CurrentNodes) == 0 {
		return errors.New("approve applied payload requires task_id and current_nodes")
	}
	return nil
}

func validateWorkflowTaskMoveApplied(applied WorkflowTaskMoveApplied) error {
	if len(applied.CurrentNodes) == 0 {
		return errors.New("move applied payload requires current_nodes")
	}
	return nil
}
