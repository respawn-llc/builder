package workflowcontract

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

type ExecutionTargetSelection struct {
	Mode      ExecutionTargetMode `json:"mode"`
	CustomRef *string             `json:"custom_ref,omitempty"`
}

func (s ExecutionTargetSelection) Validate() error {
	if !IsConcreteExecutionTargetMode(s.Mode) {
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

func (s ExecutionTargetSelection) Equal(other ExecutionTargetSelection) bool {
	if s.Mode != other.Mode {
		return false
	}
	if s.CustomRef == nil || other.CustomRef == nil {
		return s.CustomRef == nil && other.CustomRef == nil
	}
	return *s.CustomRef == *other.CustomRef
}

func IsExecutionTargetPolicyMode(mode ExecutionTargetMode) bool {
	switch mode {
	case ExecutionTargetModeNone, ExecutionTargetModeHead, ExecutionTargetModeDefaultBranch, ExecutionTargetModeCustomRef, ExecutionTargetModeAskOnFirstExecution:
		return true
	default:
		return false
	}
}

func IsConcreteExecutionTargetMode(mode ExecutionTargetMode) bool {
	switch mode {
	case ExecutionTargetModeNone, ExecutionTargetModeHead, ExecutionTargetModeDefaultBranch, ExecutionTargetModeCustomRef:
		return true
	default:
		return false
	}
}

func IsManagedExecutionTargetMode(mode ExecutionTargetMode) bool {
	switch mode {
	case ExecutionTargetModeHead, ExecutionTargetModeDefaultBranch, ExecutionTargetModeCustomRef:
		return true
	default:
		return false
	}
}
