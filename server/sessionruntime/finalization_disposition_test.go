package sessionruntime

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"core/server/workflow"
)

func TestWorkflowScriptRetainsExactScopeWhenFinalizationPublicationFails(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("true unavailable: %v", err)
	}
	authority := NewAuthority(AuthorityOptions{})
	t.Cleanup(func() {
		_ = authority.Close(context.Background())
	})
	publicationErr := errors.New("interruption publication failed")
	finalized := make(chan struct{})
	handle, err := authority.StartScriptExecution(context.Background(), ScriptExecutionRequest{
		Workflow: releasedWorkflowLeaseForTest(
			t,
			authority,
			workflowExecutionRefForTest(t, workflow.TaskID("task-script-publication-failure"), "node-script", nil),
		),
		Command: ScriptCommand{Path: truePath},
		Finalize: func(context.Context, ExecutionScope, ScriptResult, error) error {
			close(finalized)
			return publicationErr
		},
	})
	if err != nil {
		t.Fatalf("StartScriptExecution: %v", err)
	}
	<-finalized
	requireFinalizationPublicationFailureRetainsScope(t, authority, handle, publicationErr)
}

func TestWorkflowAgentRetainsExactScopeWhenFinalizationPublicationFails(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	publicationErr := errors.New("interruption publication failed")
	finalized := make(chan struct{})
	handle, err := fixture.authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Runtime:    &plan,
		Workflow: releasedWorkflowLeaseForTest(
			t,
			fixture.authority,
			workflowExecutionRefForTest(t, workflow.TaskID("task-agent-publication-failure"), "node-agent", nil),
		),
		Resource: OpenAgentResource{},
		Runner: func(context.Context, ExecutionScope, AgentRuntimeBridge) error {
			return errors.New("runtime failed")
		},
		Finalize: func(context.Context, ExecutionScope, error) error {
			close(finalized)
			return publicationErr
		},
	})
	if err != nil {
		t.Fatalf("StartAgentExecution: %v", err)
	}
	<-finalized
	requireFinalizationPublicationFailureRetainsScope(t, fixture.authority, handle, publicationErr)
}

func requireFinalizationPublicationFailureRetainsScope(
	t *testing.T,
	authority *Authority,
	handle ExecutionHandle,
	publicationErr error,
) {
	t.Helper()
	closeCtx, cancelClose := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelClose()
	if err := handle.Close(closeCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close before durable disposition = %v, want retained finalizer", err)
	}
	if _, live := authority.ExecutionByScope(handle.Scope().ID()); !live {
		t.Fatal("Authority retired Exact Scope before durable disposition")
	}
	if err := authority.ConfirmWorkflowDisposition(handle.Scope().ID()); err != nil {
		t.Fatalf("ConfirmWorkflowDisposition: %v", err)
	}
	_, err := handle.Wait(context.Background())
	if !errors.Is(err, publicationErr) {
		t.Fatalf("Wait error = %v, want publication failure %v", err, publicationErr)
	}
}
