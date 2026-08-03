package workflowexecution

import (
	"errors"

	"core/server/workflow"
	"core/shared/serverapi"
)

// CurrentNodeStartFailure carries the interruption projection selected by the
// slow runner when an admitted Current Node cannot begin execution.
type CurrentNodeStartFailure struct {
	Cause  error
	Reason workflow.CurrentNodeInterruptionReason
	Detail workflow.CurrentNodeInterruptionDetail
}

type ExecutionTargetPreparationFailure struct {
	Cause           error
	Selection       workflow.ExecutionTargetSelection
	SelectionSource ExecutionTargetSelectionSource
}

type ExecutionTargetSelectionSource string

const (
	ExecutionTargetSelectionSourceConfigured ExecutionTargetSelectionSource = "configured"
	ExecutionTargetSelectionSourceExplicit   ExecutionTargetSelectionSource = "explicit"
)

const (
	executionTargetFailureFieldCause           = "code"
	executionTargetFailureFieldRequestedRef    = "requested_ref"
	executionTargetFailureFieldSelectionMode   = "selection_mode"
	executionTargetFailureFieldSelectionSource = "selection_source"
)

type ExecutionTargetResolutionFailureMetadata struct {
	Cause           serverapi.WorkflowExecutionTargetUnavailableCause
	SelectionMode   workflow.ExecutionTargetMode
	RequestedRef    *string
	SelectionSource ExecutionTargetSelectionSource
}

func (s ExecutionTargetSelectionSource) Validate() error {
	switch s {
	case ExecutionTargetSelectionSourceConfigured, ExecutionTargetSelectionSourceExplicit:
		return nil
	default:
		return errors.New("execution target selection source is invalid")
	}
}

func (m ExecutionTargetResolutionFailureMetadata) Fields() (map[string]string, error) {
	if m.Cause == "" {
		return nil, errors.New("execution target resolution failure cause is required")
	}
	if m.SelectionMode != workflow.ExecutionTargetModeHead &&
		m.SelectionMode != workflow.ExecutionTargetModeDefaultBranch &&
		m.SelectionMode != workflow.ExecutionTargetModeCustomRef {
		return nil, errors.New("execution target resolution failure selection mode is invalid")
	}
	if err := m.SelectionSource.Validate(); err != nil {
		return nil, err
	}
	fields := map[string]string{
		executionTargetFailureFieldCause:           string(m.Cause),
		executionTargetFailureFieldSelectionMode:   string(m.SelectionMode),
		executionTargetFailureFieldSelectionSource: string(m.SelectionSource),
	}
	if m.RequestedRef != nil {
		if *m.RequestedRef == "" {
			return nil, errors.New("execution target resolution failure requested ref must be non-empty")
		}
		fields[executionTargetFailureFieldRequestedRef] = *m.RequestedRef
	}
	return fields, nil
}

func ExecutionTargetResolutionFailureMetadataFromFields(fields map[string]string) (ExecutionTargetResolutionFailureMetadata, error) {
	cause := serverapi.WorkflowExecutionTargetUnavailableCause(fields[executionTargetFailureFieldCause])
	switch cause {
	case serverapi.WorkflowExecutionTargetUnavailableCauseInvalidRevision,
		serverapi.WorkflowExecutionTargetUnavailableCauseNonCommit,
		serverapi.WorkflowExecutionTargetUnavailableCauseDefaultBranchMissing,
		serverapi.WorkflowExecutionTargetUnavailableCauseDefaultBranchAmbiguous,
		serverapi.WorkflowExecutionTargetUnavailableCauseGitFailure:
	default:
		return ExecutionTargetResolutionFailureMetadata{}, errors.New("execution target resolution failure cause is invalid")
	}
	mode := workflow.ExecutionTargetMode(fields[executionTargetFailureFieldSelectionMode])
	if mode != workflow.ExecutionTargetModeHead &&
		mode != workflow.ExecutionTargetModeDefaultBranch &&
		mode != workflow.ExecutionTargetModeCustomRef {
		return ExecutionTargetResolutionFailureMetadata{}, errors.New("execution target resolution failure selection mode is invalid")
	}
	source := ExecutionTargetSelectionSource(fields[executionTargetFailureFieldSelectionSource])
	if err := source.Validate(); err != nil {
		return ExecutionTargetResolutionFailureMetadata{}, err
	}
	var requestedRef *string
	if value, exists := fields[executionTargetFailureFieldRequestedRef]; exists {
		if value == "" {
			return ExecutionTargetResolutionFailureMetadata{}, errors.New("execution target resolution failure requested ref must be non-empty")
		}
		requestedRef = &value
	}
	return ExecutionTargetResolutionFailureMetadata{
		Cause:           cause,
		SelectionMode:   mode,
		RequestedRef:    requestedRef,
		SelectionSource: source,
	}, nil
}

func (e *ExecutionTargetPreparationFailure) Error() string {
	if e == nil || e.Cause == nil {
		return "execution target preparation failed"
	}
	return e.Cause.Error()
}

func (e *ExecutionTargetPreparationFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *CurrentNodeStartFailure) Error() string {
	if e == nil || e.Cause == nil {
		return "current node start failed"
	}
	return e.Cause.Error()
}

func (e *CurrentNodeStartFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *CurrentNodeStartFailure) Validate() error {
	if e == nil {
		return errors.New("current node start failure is required")
	}
	if e.Cause == nil {
		return errors.New("current node start failure cause is required")
	}
	if e.Reason == "" {
		return errors.New("current node start failure reason is required")
	}
	if e.Detail.Code == "" {
		return errors.New("current node start failure detail code is required")
	}
	return nil
}
