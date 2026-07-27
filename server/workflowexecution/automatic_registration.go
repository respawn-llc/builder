package workflowexecution

import (
	"errors"
	"fmt"
	"strings"

	"core/server/workflow"
	"core/shared/invariant"
)

type AutomaticStartProducer string

const (
	AutomaticStartProducerTaskStart         AutomaticStartProducer = "task_start"
	AutomaticStartProducerTaskCompletion    AutomaticStartProducer = "task_completion"
	AutomaticStartProducerRuntimeCompletion AutomaticStartProducer = "runtime_completion"
	AutomaticStartProducerScriptCompletion  AutomaticStartProducer = "script_completion"
)

type AutomaticStartRegistrar interface {
	RegisterAutomaticStarts([]workflow.RunID) error
}

type automaticStartRequestRegistrar interface {
	RegisterAutomaticStartRequest(AutomaticStartRegistrationRequest) error
}

type AutomaticStartRegistration struct {
	registrar AutomaticStartRegistrar
	fatal     *FatalSignal
}

func NewAutomaticStartRegistration(registrar AutomaticStartRegistrar, fatal *FatalSignal) (*AutomaticStartRegistration, error) {
	if registrar == nil {
		return nil, errors.New("automatic workflow start registrar is required")
	}
	if fatal == nil {
		return nil, errors.New("workflow execution fatal signal is required")
	}
	return &AutomaticStartRegistration{registrar: registrar, fatal: fatal}, nil
}

type AutomaticStartRegistrationRequest struct {
	Producer     AutomaticStartProducer
	SourceRunID  *workflow.RunID
	TransitionID *workflow.TransitionID
	RunIDs       []workflow.RunID
}

type AutomaticStartRegistrationError struct {
	Producer     AutomaticStartProducer
	SourceRunID  *workflow.RunID
	TransitionID *workflow.TransitionID
	RunIDs       []workflow.RunID
	Cause        error
}

func (e AutomaticStartRegistrationError) Error() string {
	parts := []string{
		fmt.Sprintf("producer=%q", e.Producer),
		fmt.Sprintf("run_ids=%v", e.RunIDs),
	}
	if e.SourceRunID != nil {
		parts = append(parts, fmt.Sprintf("source_run_id=%q", *e.SourceRunID))
	}
	if e.TransitionID != nil {
		parts = append(parts, fmt.Sprintf("transition_id=%q", *e.TransitionID))
	}
	if e.Cause == nil {
		return "automatic workflow start registration failed: " + strings.Join(parts, " ")
	}
	return "automatic workflow start registration failed: " + strings.Join(parts, " ") + ": " + e.Cause.Error()
}

func (e AutomaticStartRegistrationError) Unwrap() error {
	return e.Cause
}

type WorkflowExecutionFatalError struct {
	Diagnostic invariant.Diagnostic
	Cause      error
}

func (e WorkflowExecutionFatalError) Error() string {
	if e.Cause == nil {
		return "fatal workflow execution invariant failure"
	}
	return "fatal workflow execution invariant failure: " + e.Cause.Error()
}

func (e WorkflowExecutionFatalError) Unwrap() error {
	return e.Cause
}

func (r *AutomaticStartRegistration) Register(req AutomaticStartRegistrationRequest) error {
	if len(req.RunIDs) == 0 {
		return nil
	}
	runIDs := append([]workflow.RunID(nil), req.RunIDs...)
	cause := validateAutomaticStartRegistrationRequest(req, runIDs)
	if cause == nil {
		if r == nil || r.registrar == nil {
			cause = errors.New("automatic workflow start registration is required")
		} else if registrar, ok := r.registrar.(automaticStartRequestRegistrar); ok {
			request := req
			request.RunIDs = runIDs
			cause = registrar.RegisterAutomaticStartRequest(request)
		} else {
			cause = r.registrar.RegisterAutomaticStarts(runIDs)
		}
	}
	if cause == nil {
		return nil
	}
	registrationErr := AutomaticStartRegistrationError{
		Producer:     req.Producer,
		SourceRunID:  clonePointer(req.SourceRunID),
		TransitionID: clonePointer(req.TransitionID),
		RunIDs:       runIDs,
		Cause:        cause,
	}
	fatalErr := WorkflowExecutionFatalError{
		Diagnostic: invariant.FailureDiagnostic(
			invariant.ScopeWorkflowExecution,
			"register_automatic_workflow_starts",
			registrationErr,
		).WithStack(),
		Cause: registrationErr,
	}
	if r == nil || r.fatal == nil {
		return errors.Join(fatalErr, errors.New("workflow execution fatal signal is required"))
	}
	if reportErr := r.fatal.Report(fatalErr); reportErr != nil {
		return errors.Join(fatalErr, reportErr)
	}
	return fatalErr
}

func validateAutomaticStartRegistrationRequest(req AutomaticStartRegistrationRequest, runIDs []workflow.RunID) error {
	switch req.Producer {
	case AutomaticStartProducerTaskStart:
		if req.SourceRunID != nil {
			return errors.New("task-start automatic registration must not have a source run")
		}
	case AutomaticStartProducerTaskCompletion,
		AutomaticStartProducerRuntimeCompletion,
		AutomaticStartProducerScriptCompletion:
		if req.SourceRunID == nil {
			return errors.New("completion automatic registration requires a source run")
		}
	default:
		return fmt.Errorf("automatic workflow start producer %q is invalid", req.Producer)
	}
	if req.TransitionID == nil {
		return errors.New("automatic workflow start transition is required")
	}
	if *req.TransitionID == "" {
		return errors.New("automatic workflow start transition is blank")
	}
	if req.SourceRunID != nil && *req.SourceRunID == "" {
		return errors.New("automatic workflow start source run is blank")
	}
	for index, runID := range runIDs {
		if runID == "" {
			return fmt.Errorf("automatic workflow successor run id at index %d is blank", index)
		}
	}
	return nil
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
