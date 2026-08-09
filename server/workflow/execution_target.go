package workflow

import (
	"errors"
	"strings"
)

type ExecutionTargetMode string

const (
	ExecutionTargetModeNone                ExecutionTargetMode = "none"
	ExecutionTargetModeHead                ExecutionTargetMode = "head"
	ExecutionTargetModeDefaultBranch       ExecutionTargetMode = "default_branch"
	ExecutionTargetModeCustomRef           ExecutionTargetMode = "custom_ref"
	ExecutionTargetModeAskOnFirstExecution ExecutionTargetMode = "ask_on_first_execution"
)

type ExecutionTargetPolicy struct {
	Mode      ExecutionTargetMode
	CustomRef *string
}

type ExecutionTargetSelection struct {
	Mode      ExecutionTargetMode
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
	Mode         ExecutionTargetMode             `json:"mode"`
	RequestedRef *string                         `json:"requested_ref,omitempty"`
	Cause        ExecutionTargetUnavailableCause `json:"cause"`
}

func (u ConfiguredExecutionTargetUnavailable) Validate() error {
	selection := ExecutionTargetSelection{Mode: u.Mode, CustomRef: u.RequestedRef}
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
	return ExecutionTargetPolicy{Mode: ExecutionTargetModeAskOnFirstExecution}
}

func (p ExecutionTargetPolicy) Canonical() ExecutionTargetPolicy {
	if p.Mode == "" {
		return DefaultExecutionTargetPolicy()
	}
	return p
}

func (s ExecutionTargetSelection) Validate() error {
	if !validConcreteExecutionTargetMode(s.Mode) {
		return errors.New("execution target selection mode must be concrete")
	}
	if s.Mode != ExecutionTargetModeCustomRef {
		if s.CustomRef != nil {
			return errors.New("execution target custom ref is only valid for custom_ref selection")
		}
		return nil
	}
	if s.CustomRef == nil || strings.TrimSpace(*s.CustomRef) == "" {
		return errors.New("execution target custom ref is required")
	}
	return nil
}

func validExecutionTargetPolicyMode(mode ExecutionTargetMode) bool {
	switch mode {
	case ExecutionTargetModeNone, ExecutionTargetModeHead, ExecutionTargetModeDefaultBranch, ExecutionTargetModeCustomRef, ExecutionTargetModeAskOnFirstExecution:
		return true
	default:
		return false
	}
}

func validConcreteExecutionTargetMode(mode ExecutionTargetMode) bool {
	switch mode {
	case ExecutionTargetModeNone, ExecutionTargetModeHead, ExecutionTargetModeDefaultBranch, ExecutionTargetModeCustomRef:
		return true
	default:
		return false
	}
}
