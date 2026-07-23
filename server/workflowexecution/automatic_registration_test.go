package workflowexecution

import (
	"errors"
	"testing"

	"core/server/workflow"
	"core/shared/invariant"
)

func TestAutomaticStartRegistrationSignalsFatalFailureWithDiagnosticInEveryInvariantMode(t *testing.T) {
	for _, mode := range []string{"diagnostic", "panic"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("KENT_INVARIANT_MODE", mode)
			cause := errors.New("registrar dropped successors")
			sourceRunID := workflow.RunID("run-source")
			transitionID := workflow.TransitionID("transition-1")
			fatalSignal := NewFatalSignal()
			registration, err := NewAutomaticStartRegistration(
				failingAutomaticStartRegistrar{err: cause},
				fatalSignal,
			)
			if err != nil {
				t.Fatalf("NewAutomaticStartRegistration: %v", err)
			}

			err = registration.Register(AutomaticStartRegistrationRequest{
				Producer:     AutomaticStartProducerTaskCompletion,
				SourceRunID:  &sourceRunID,
				TransitionID: &transitionID,
				RunIDs:       []workflow.RunID{"run-successor"},
			})
			var fatalErr WorkflowExecutionFatalError
			if !errors.As(err, &fatalErr) {
				t.Fatalf("Register error = %T %v, want WorkflowExecutionFatalError", err, err)
			}
			signaled := <-fatalSignal.Failures()
			if !errors.As(signaled, &fatalErr) {
				t.Fatalf("signaled failure = %T %v, want WorkflowExecutionFatalError", signaled, signaled)
			}
			diagnostic := fatalErr.Diagnostic
			if diagnostic.Scope != invariant.ScopeWorkflowExecution || diagnostic.Stack == "" {
				t.Fatalf("diagnostic = %+v, want workflow execution scope with stack", diagnostic)
			}
		})
	}
}

func TestAutomaticIntentsRegisterAutomaticStartsRejectsBatchWithBlankRunID(t *testing.T) {
	intents := NewAutomaticIntents()
	err := intents.RegisterAutomaticStarts([]workflow.RunID{"run-valid", ""})
	if err == nil {
		t.Fatal("blank automatic run id did not fail registration")
	}
	if got := intents.Take(1); len(got) != 0 {
		t.Fatalf("partially registered run IDs = %v, want none", got)
	}
}

type failingAutomaticStartRegistrar struct {
	err error
}

func (f failingAutomaticStartRegistrar) RegisterAutomaticStarts([]workflow.RunID) error {
	return f.err
}
