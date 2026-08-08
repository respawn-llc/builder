package sessionruntime

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"core/server/workflow"
)

func TestWorkflowScriptInterruptVisibilityStartsAtRunningPublication(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh unavailable: %v", err)
	}
	authority := NewAuthority(AuthorityOptions{})
	t.Cleanup(func() {
		_ = authority.Close(context.Background())
	})
	taskID := workflow.TaskID("task-script-publication-boundary")
	entered := make(chan struct{})
	release := make(chan struct{})
	publication := workflowRunningPublicationStub{
		publish: func(
			_ context.Context,
			_ TaskExecution,
			activation WorkflowRunningActivation,
		) error {
			close(entered)
			<-release
			return activation.Activate()
		},
	}
	handle, err := authority.StartScriptExecution(context.Background(), ScriptExecutionRequest{
		Workflow: releasedWorkflowLeaseForTest(
			t,
			authority,
			workflowExecutionRefForTest(t, taskID, "node-script", nil),
		),
		Command: ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "while :; do sleep 1; done"},
		},
		RunningPublication: publication,
	})
	if err != nil {
		t.Fatalf("StartScriptExecution: %v", err)
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		_ = handle.Stop(context.Background())
	})

	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("Script did not enter running publication")
	}
	assertWorkflowInterruptUnavailable(t, authority, taskID)

	close(release)
	selected := awaitWorkflowInterruptSelection(t, authority, taskID)
	if len(selected.Interruptible) != 1 ||
		selected.Interruptible[0].Scope().ID() != handle.Scope().ID() {
		t.Fatalf("Script selection = %+v, want published Exact Scope %s", selected, handle.Scope().ID())
	}
}

func TestWorkflowAgentInterruptVisibilityStartsAtRunningPublication(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	taskID := workflow.TaskID("task-agent-publication-boundary")
	entered := make(chan struct{})
	release := make(chan struct{})
	publication := workflowRunningPublicationStub{
		publish: func(
			_ context.Context,
			_ TaskExecution,
			activation WorkflowRunningActivation,
		) error {
			close(entered)
			<-release
			return activation.Activate()
		},
	}
	handle, err := fixture.authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Runtime:    &plan,
		Workflow: releasedWorkflowLeaseForTest(
			t,
			fixture.authority,
			workflowExecutionRefForTest(t, taskID, "node-agent", nil),
		),
		Resource:           OpenAgentResource{},
		RunningPublication: publication,
		Runner: func(ctx context.Context, _ ExecutionScope, _ AgentRuntimeBridge) error {
			<-ctx.Done()
			return context.Cause(ctx)
		},
	})
	if err != nil {
		t.Fatalf("StartAgentExecution: %v", err)
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		_ = handle.Stop(context.Background())
	})

	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("Agent did not enter running publication")
	}
	assertWorkflowInterruptUnavailable(t, fixture.authority, taskID)

	close(release)
	selected := awaitWorkflowInterruptSelection(t, fixture.authority, taskID)
	if len(selected.Interruptible) != 1 ||
		selected.Interruptible[0].Scope().ID() != handle.Scope().ID() {
		t.Fatalf("Agent selection = %+v, want published Exact Scope %s", selected, handle.Scope().ID())
	}
}

func TestTaskInterruptBeforeAgentActivationPreventsRunnerStart(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh unavailable: %v", err)
	}
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	taskID := workflow.TaskID("task-agent-activation-interrupt")
	sibling, err := fixture.authority.StartScriptExecution(context.Background(), ScriptExecutionRequest{
		Workflow: releasedWorkflowLeaseForTest(
			t,
			fixture.authority,
			workflowExecutionRefForTest(t, taskID, "node-sibling", nil),
		),
		Command: ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "while :; do sleep 1; done"},
		},
	})
	if err != nil {
		t.Fatalf("start sibling Script: %v", err)
	}
	t.Cleanup(func() { _ = sibling.Stop(context.Background()) })
	awaitWorkflowInterruptSelection(t, fixture.authority, taskID)

	publicationEntered := make(chan struct{})
	releasePublication := make(chan struct{})
	runnerEntered := make(chan struct{})
	target, err := fixture.authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Runtime:    &plan,
		Workflow: releasedWorkflowLeaseForTest(
			t,
			fixture.authority,
			workflowExecutionRefForTest(t, taskID, "node-target", nil),
		),
		Resource: OpenAgentResource{},
		RunningPublication: workflowRunningPublicationStub{
			publish: func(
				_ context.Context,
				_ TaskExecution,
				activation WorkflowRunningActivation,
			) error {
				close(publicationEntered)
				<-releasePublication
				return activation.Activate()
			},
		},
		Runner: func(context.Context, ExecutionScope, AgentRuntimeBridge) error {
			close(runnerEntered)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("start target Agent: %v", err)
	}
	t.Cleanup(func() {
		select {
		case <-releasePublication:
		default:
			close(releasePublication)
		}
		_ = target.Close(context.Background())
	})
	select {
	case <-publicationEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("target Agent did not enter running publication")
	}

	sessionSelectionCalled := false
	sessionErr := fixture.authority.WithWorkflowInterruptSelection(
		taskID,
		&sessionID,
		func(WorkflowInterruptSelection) error {
			sessionSelectionCalled = true
			return nil
		},
	)
	if !errors.Is(sessionErr, ErrExecutionNoLongerLive) || sessionSelectionCalled {
		t.Fatalf(
			"pre-activation Session Interrupt = %v, called=%t; want unavailable",
			sessionErr,
			sessionSelectionCalled,
		)
	}

	manualMoveErr := errors.New("observe manual move selection")
	var manualSelection WorkflowInterruptSelection
	if err := fixture.authority.WithWorkflowManualMoveSelection(
		taskID,
		func(selection WorkflowInterruptSelection) error {
			manualSelection = selection
			return manualMoveErr
		},
	); !errors.Is(err, manualMoveErr) {
		t.Fatalf("Manual Move selection: %v", err)
	}
	if len(manualSelection.Queued) != 1 ||
		manualSelection.Queued[0].Scope().ID() != target.Scope().ID() {
		t.Fatalf("pre-activation Manual Move selection = %+v, want target queued", manualSelection)
	}

	if err := fixture.authority.WithWorkflowInterruptSelection(
		taskID,
		nil,
		func(selection WorkflowInterruptSelection) error {
			for _, handle := range selection.Interruptible {
				handle.RequestStop()
			}
			for _, handle := range selection.Queued {
				handle.RequestStop()
			}
			return nil
		},
	); err != nil {
		t.Fatalf("Task Interrupt selection: %v", err)
	}
	close(releasePublication)
	if _, err := target.Wait(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("target Wait error = %v, want canceled publication", err)
	}
	select {
	case <-runnerEntered:
		t.Fatal("Agent runner started after Task Interrupt won activation")
	default:
	}
}

func assertWorkflowInterruptUnavailable(t *testing.T, authority *Authority, taskID workflow.TaskID) {
	t.Helper()
	called := false
	err := authority.WithWorkflowInterruptSelection(taskID, nil, func(WorkflowInterruptSelection) error {
		called = true
		return nil
	})
	if !errors.Is(err, ErrExecutionNoLongerLive) {
		t.Fatalf("pre-publication Interrupt selection error = %v, want %v", err, ErrExecutionNoLongerLive)
	}
	if called {
		t.Fatal("pre-publication Interrupt reached selection callback")
	}
}

func awaitWorkflowInterruptSelection(
	t *testing.T,
	authority *Authority,
	taskID workflow.TaskID,
) WorkflowInterruptSelection {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		var selected WorkflowInterruptSelection
		err := authority.WithWorkflowInterruptSelection(taskID, nil, func(got WorkflowInterruptSelection) error {
			selected = got
			return nil
		})
		if err == nil {
			return selected
		}
		select {
		case <-deadline:
			t.Fatalf("post-publication Interrupt selection: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
