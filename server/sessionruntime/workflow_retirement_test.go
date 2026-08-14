package sessionruntime

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"core/server/workflow"
)

func TestWorkflowExecutionRetirementReportsClosedDisposition(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("true executable unavailable: %v", err)
	}
	for _, test := range []struct {
		name        string
		complete    bool
		disposition WorkflowRetirementDisposition
	}{
		{name: "completed", complete: true, disposition: WorkflowRetirementCompleted},
		{name: "outcome-less", disposition: WorkflowRetirementOutcomeLess},
	} {
		t.Run(test.name, func(t *testing.T) {
			retired := make(chan WorkflowRetirementOutcome, 1)
			authority := NewAuthority(AuthorityOptions{
				WorkflowExecutionRetired: WorkflowExecutionRetiredFunc(func(outcome WorkflowRetirementOutcome) {
					retired <- outcome
				}),
			})
			t.Cleanup(func() {
				if err := authority.Close(context.Background()); err != nil {
					t.Errorf("close Authority: %v", err)
				}
			})
			ref := workflowExecutionRefForTest(
				t,
				workflow.TaskID("task-retirement-"+test.name),
				workflow.NodeID("node-script"),
				nil,
			)
			detached, err := authority.PrepareDetachedScriptExecution(
				context.Background(),
				DetachedScriptExecutionRequest{
					Workflow: ref,
					Command:  ScriptCommand{Path: truePath},
					Finalize: func(_ context.Context, scope ExecutionScope, _ ScriptResult, _ error) error {
						if !test.complete {
							return nil
						}
						return authority.CompleteFinalizingScript(scope.ID(), func() error { return nil })
					},
				},
			)
			if err != nil {
				t.Fatalf("prepare Script: %v", err)
			}
			handle, launch, err := detached.Publish(context.Background(), func() error { return nil }, nil)
			if err != nil {
				t.Fatalf("publish Script: %v", err)
			}
			launch()
			if _, err := handle.Wait(context.Background()); err != nil {
				t.Fatalf("wait Script: %v", err)
			}
			select {
			case outcome := <-retired:
				if outcome.Operation.OperationID != ref.OperationID ||
					!outcome.Operation.CurrentNode.Equal(ref.CurrentNode) ||
					outcome.Kind != ExecutionScopeScript ||
					outcome.Disposition != test.disposition {
					t.Fatalf("retirement outcome = %+v, want operation %+v kind Script disposition %v", outcome, ref.Operation(), test.disposition)
				}
			case <-time.After(time.Second):
				t.Fatal("Workflow retirement callback was not emitted")
			}
		})
	}
}
