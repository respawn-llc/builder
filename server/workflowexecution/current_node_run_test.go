package workflowexecution

import (
	"errors"
	"testing"

	"core/server/workflow"
	"core/shared/runtimeids"
)

func TestCurrentNodeRunRegistryJoinsOneAgentActivationOutcome(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-agent-run", "node-agent")
	sessionID := runtimeids.NewSessionID()
	controller := &CurrentNodeController{runs: newCurrentNodeRunRegistry()}

	run, created, err := controller.runs.register(newCurrentNodeRun(
		reference,
		workflow.NodeKindAgent,
		currentNodeAdmissionExplicitOverride,
	))
	if err != nil {
		t.Fatalf("register Agent Run: %v", err)
	}
	if !created {
		t.Fatal("first Agent Run registration did not create the Run")
	}
	firstActivation, err := controller.joinAgentRunActivation(reference, sessionID)
	if err != nil {
		t.Fatalf("join first Agent activation: %v", err)
	}

	secondActivation, err := controller.joinAgentRunActivation(reference, sessionID)
	if err != nil {
		t.Fatalf("join second Agent activation: %v", err)
	}
	if firstActivation != secondActivation {
		t.Fatal("Board/CLI and interactive joins received different Agent activation outcomes")
	}
	if registered, exists := controller.runs.get(mustCurrentNodeRunKey(run)); !exists || registered != run {
		t.Fatal("joining Agent activation replaced the Current Node Run")
	}
}

func TestCurrentNodeControllerRejectsDifferentSessionJoiningAgentRun(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-agent-run-session", "node-agent")
	controller := &CurrentNodeController{runs: newCurrentNodeRunRegistry()}
	if _, _, err := controller.runs.register(newCurrentNodeRun(
		reference,
		workflow.NodeKindAgent,
		currentNodeAdmissionExplicitOverride,
	)); err != nil {
		t.Fatalf("register Agent Run: %v", err)
	}

	if _, err := controller.joinAgentRunActivation(reference, runtimeids.NewSessionID()); err != nil {
		t.Fatalf("join first retained Session: %v", err)
	}
	if _, err := controller.joinAgentRunActivation(reference, runtimeids.NewSessionID()); err == nil {
		t.Fatal("different retained Session joined the existing Agent Run")
	}
}

func TestCurrentNodeRunRegistryRejectsDuplicateOwnership(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-duplicate-run", "node-agent")
	registry := newCurrentNodeRunRegistry()
	if _, _, err := registry.register(newCurrentNodeRun(
		reference,
		workflow.NodeKindAgent,
		currentNodeAdmissionAutomaticAgent,
	)); err != nil {
		t.Fatalf("register first Run: %v", err)
	}

	_, _, err := registry.register(newCurrentNodeRun(
		reference,
		workflow.NodeKindAgent,
		currentNodeAdmissionExplicitOverride,
	))
	var conflict *currentNodeRunConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("duplicate registration error = %v, want typed Run conflict", err)
	}
}

func TestCurrentNodeRunDispositionsAreTypedAndTerminalWhenStopped(t *testing.T) {
	run := newCurrentNodeRun(
		currentNodeReferenceForControllerTest(t, "task-run-disposition", "node-agent"),
		workflow.NodeKindAgent,
		currentNodeAdmissionAutomaticAgent,
	)
	if run.disposition != currentNodeRunDispositionQueued {
		t.Fatalf("new Run disposition = %v, want queued", run.disposition)
	}
	if err := run.transitionDisposition(currentNodeRunDispositionRunning, nil); err != nil {
		t.Fatalf("transition Run to running: %v", err)
	}
	stop := currentNodeRunStop{reason: currentNodeRunStopInterrupted}
	if err := run.transitionDisposition(currentNodeRunDispositionStopped, &stop); err != nil {
		t.Fatalf("transition Run to stopped: %v", err)
	}
	if run.stop == nil || *run.stop != stop {
		t.Fatalf("Run stop = %+v, want %+v", run.stop, stop)
	}
	if err := run.transitionDisposition(currentNodeRunDispositionRunning, nil); err == nil {
		t.Fatal("stopped Run transitioned back to running")
	}
}
