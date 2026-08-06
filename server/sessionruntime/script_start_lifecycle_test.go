package sessionruntime

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestWorkflowScriptReportsStartedOnlyAfterAdmissionLeaseRelease(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh unavailable: %v", err)
	}
	authority := NewAuthority(AuthorityOptions{})
	t.Cleanup(func() {
		_ = authority.Close(context.Background())
	})
	lease, err := authority.NewWorkflowExecutionLease(
		workflowExecutionRefForTest(t, "task-script-started", "node-script", nil),
	)
	if err != nil {
		t.Fatalf("NewWorkflowExecutionLease: %v", err)
	}
	handle, err := authority.StartScriptExecution(context.Background(), ScriptExecutionRequest{
		Workflow: &lease,
		Command: ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "while :; do sleep 1; done"},
		},
	})
	if err != nil {
		t.Fatalf("StartScriptExecution: %v", err)
	}

	published := make(chan TaskExecution, 1)
	publicationDone := make(chan error, 1)
	go func() {
		publicationDone <- handle.PublishRunning(context.Background(), func(running TaskExecution) error {
			published <- running
			return nil
		})
	}()
	select {
	case running := <-published:
		t.Fatalf("script reported running before lease release: %s", running.ScopeID)
	case <-time.After(50 * time.Millisecond):
	}
	lease.Release()
	var running TaskExecution
	select {
	case running = <-published:
	case <-time.After(3 * time.Second):
		t.Fatal("script did not report running after process start")
	}
	if running.ScopeID != handle.Scope().ID() {
		t.Fatalf("running scope = %s, want %s", running.ScopeID, handle.Scope().ID())
	}
	if err := <-publicationDone; err != nil {
		t.Fatalf("PublishRunning: %v", err)
	}
}
