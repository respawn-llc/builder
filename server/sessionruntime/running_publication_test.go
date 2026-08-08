package sessionruntime

import (
	"context"
	"errors"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"core/server/workflow"
	"core/shared/runtimeids"
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
	var published atomic.Bool
	publication := workflowRunningPublicationStub{
		publish: func(_ context.Context, _ TaskExecution) error {
			close(entered)
			<-release
			published.Store(true)
			return nil
		},
		published: func(runtimeids.ExecutionScopeID) bool {
			return published.Load()
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
	var published atomic.Bool
	publication := workflowRunningPublicationStub{
		publish: func(_ context.Context, _ TaskExecution) error {
			close(entered)
			<-release
			published.Store(true)
			return nil
		},
		published: func(runtimeids.ExecutionScopeID) bool {
			return published.Load()
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
