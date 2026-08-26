package workflow

import (
	"errors"

	"core/shared/workflowcontract"
)

type ExecutionTargetPolicy struct {
	Mode      workflowcontract.ExecutionTargetMode
	CustomRef *string
}

type ExecutionTargetUnavailableCause string

const (
	ExecutionTargetUnavailableCauseInvalidRevision        ExecutionTargetUnavailableCause = "invalid_revision"
	ExecutionTargetUnavailableCauseNonCommit              ExecutionTargetUnavailableCause = "non_commit"
	ExecutionTargetUnavailableCauseDefaultBranchMissing   ExecutionTargetUnavailableCause = "default_branch_missing"
	ExecutionTargetUnavailableCauseDefaultBranchAmbiguous ExecutionTargetUnavailableCause = "default_branch_ambiguous"
	ExecutionTargetUnavailableCauseGitFailure             ExecutionTargetUnavailableCause = "git_failure"
)

type ConfiguredExecutionTargetUnavailable struct {
	Mode         workflowcontract.ExecutionTargetMode `json:"mode"`
	RequestedRef *string                              `json:"requested_ref,omitempty"`
	Cause        ExecutionTargetUnavailableCause      `json:"cause"`
}

func (u ConfiguredExecutionTargetUnavailable) Validate() error {
	selection := workflowcontract.ExecutionTargetSelection{Mode: u.Mode, CustomRef: u.RequestedRef}
	if err := selection.Validate(); err != nil {
		return err
	}
	switch u.Cause {
	case ExecutionTargetUnavailableCauseInvalidRevision,
		ExecutionTargetUnavailableCauseNonCommit,
		ExecutionTargetUnavailableCauseDefaultBranchMissing,
		ExecutionTargetUnavailableCauseDefaultBranchAmbiguous,
		ExecutionTargetUnavailableCauseGitFailure:
		return nil
	default:
		return errors.New("configured execution target unavailable cause is invalid")
	}
}

func DefaultExecutionTargetPolicy() ExecutionTargetPolicy {
	return ExecutionTargetPolicy{Mode: workflowcontract.ExecutionTargetModeAskOnFirstExecution}
}

func (p ExecutionTargetPolicy) Canonical() ExecutionTargetPolicy {
	if p.Mode == "" {
		return DefaultExecutionTargetPolicy()
	}
	return p
}
