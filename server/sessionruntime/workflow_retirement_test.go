package sessionruntime

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"core/server/session"
	"core/server/workflow"
	"core/server/workflowruntime"
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
						_, err := authority.CompleteFinalizingScript(scope.ID(), func() (workflowruntime.CompletionDecision, error) {
							return workflowruntime.CompletionDecision{
								CommitReceipt:        session.CommitReceipt{Committed: true},
								PostCommitDiagnostic: errors.New("committed observer unavailable"),
							}, nil
						})
						return err
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

func TestDefinitelyUncommittedWorkflowCompletionRetiresOutcomeLess(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("true executable unavailable: %v", err)
	}
	retired := make(chan WorkflowRetirementOutcome, 1)
	completionErr := make(chan error, 1)
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
	ref := workflowExecutionRefForTest(t, "task-definitely-uncommitted", "node-script", nil)
	detached, err := authority.PrepareDetachedScriptExecution(
		context.Background(),
		DetachedScriptExecutionRequest{
			Workflow: ref,
			Command:  ScriptCommand{Path: truePath},
			Finalize: func(_ context.Context, scope ExecutionScope, _ ScriptResult, _ error) error {
				_, err := authority.CompleteFinalizingScript(scope.ID(), func() (workflowruntime.CompletionDecision, error) {
					return workflowruntime.CompletionDecision{}, session.DefinitelyUncommittedMutation(errors.New("association query failed"))
				})
				completionErr <- err
				return nil
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
	if err := <-completionErr; !errors.Is(err, session.ErrMutationDefinitelyUncommitted) {
		t.Fatalf("completion error = %v, want definitely uncommitted", err)
	}
	select {
	case outcome := <-retired:
		if outcome.Disposition != WorkflowRetirementOutcomeLess {
			t.Fatalf("retirement disposition = %v, want outcome-less", outcome.Disposition)
		}
	case <-time.After(time.Second):
		t.Fatal("Workflow retirement callback was not emitted")
	}
}

func TestIndeterminateWorkflowCompletionCertaintyFailsFast(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("true executable unavailable: %v", err)
	}
	panicked := make(chan any, 1)
	authority := NewAuthority(AuthorityOptions{Debug: true})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close Authority: %v", err)
		}
	})
	ref := workflowExecutionRefForTest(t, "task-indeterminate", "node-script", nil)
	detached, err := authority.PrepareDetachedScriptExecution(
		context.Background(),
		DetachedScriptExecutionRequest{
			Workflow: ref,
			Command:  ScriptCommand{Path: truePath},
			Finalize: func(_ context.Context, scope ExecutionScope, _ ScriptResult, _ error) error {
				func() {
					defer func() { panicked <- recover() }()
					_, _ = authority.CompleteFinalizingScript(scope.ID(), func() (workflowruntime.CompletionDecision, error) {
						return workflowruntime.CompletionDecision{}, errors.New("commit certainty unavailable")
					})
				}()
				return nil
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
	case recovered := <-panicked:
		if recovered == nil {
			t.Fatal("indeterminate completion certainty did not panic in debug mode")
		}
	case <-time.After(time.Second):
		t.Fatal("indeterminate completion certainty did not finish")
	}
}
