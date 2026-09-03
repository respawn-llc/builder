package runtimewire

import (
	"context"
	"reflect"
	"testing"
	"time"

	askquestion "core/server/tools"
)

func TestOutsideWorkspaceApprovalRetainsExecutingToolIdentity(t *testing.T) {
	broker := askquestion.NewAskQuestionBroker()
	var received askquestion.AskQuestionRequest
	broker.SetAskHandler(func(_ context.Context, request askquestion.AskQuestionRequest) (askquestion.AskQuestionResolution, error) {
		received = request
		return askquestion.AskQuestionApproval{Decision: askquestion.AskQuestionApprovalDecisionAllowOnce}, nil
	})
	approver := NewOutsideWorkspaceApprover(broker)
	targets := []askquestion.FileAccessTarget{{
		RequestedPath: "/outside/file",
		ResolvedPath:  "/real/outside/file",
	}}

	_, err := approver.Approve(
		askquestion.WithApprovalLifecycle(askquestion.WithExecutionIdentity(context.Background(), askquestion.ExecutionIdentity{
			RunID:      "11111111-1111-4111-8111-111111111111",
			StepID:     "22222222-2222-4222-8222-222222222222",
			ToolCallID: "call-edit",
		}), askquestion.NewApprovalLifecycle()),
		askquestion.FileAccessApprovalRequest{Targets: targets},
	)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if received.RunID != "11111111-1111-4111-8111-111111111111" ||
		received.StepID != "22222222-2222-4222-8222-222222222222" ||
		received.ToolCallID != "call-edit" {
		t.Fatalf("approval identity = run %q step %q tool %q", received.RunID, received.StepID, received.ToolCallID)
	}
	if !reflect.DeepEqual(received.AccessTargets, targets) {
		t.Fatalf("approval targets = %+v, want %+v", received.AccessTargets, targets)
	}
	if received.Question != "" {
		t.Fatalf("server materialized access Approval copy %q", received.Question)
	}
}

func TestOutsideWorkspaceApproverAcceptsTaskDetailNormalizedApprovalFromSharedBroker(t *testing.T) {
	broker := askquestion.NewAskQuestionBroker()
	approver := NewOutsideWorkspaceApprover(broker)
	result := make(chan askquestion.FileAccessApproval, 1)
	errs := make(chan error, 1)
	go func() {
		approval, err := approver.Approve(
			askquestion.WithApprovalLifecycle(askquestion.WithExecutionIdentity(context.Background(), askquestion.ExecutionIdentity{
				RunID:      "11111111-1111-4111-8111-111111111111",
				StepID:     "22222222-2222-4222-8222-222222222222",
				ToolCallID: "call-edit",
			}), askquestion.NewApprovalLifecycle()),
			askquestion.FileAccessApprovalRequest{Targets: []askquestion.FileAccessTarget{{
				RequestedPath: "/outside/file",
				ResolvedPath:  "/outside/file",
			}}},
		)
		if err != nil {
			errs <- err
			return
		}
		result <- approval
	}()

	deadline := time.Now().Add(time.Second)
	var pending []askquestion.AskQuestionRequest
	for len(pending) == 0 && time.Now().Before(deadline) {
		pending = broker.Pending()
		time.Sleep(time.Millisecond)
	}
	if len(pending) != 1 {
		t.Fatalf("pending approvals = %d, want 1", len(pending))
	}
	commentary := "  approved from Task Detail  "
	if err := broker.Submit(pending[0].ToolCallID, askquestion.AskQuestionApproval{
		Decision:   askquestion.AskQuestionApprovalDecisionAllowOnce,
		Commentary: &commentary,
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	select {
	case err := <-errs:
		t.Fatalf("Approve: %v", err)
	case approval := <-result:
		if approval.Kind != askquestion.FileAccessApprovalAllowOnce {
			t.Fatalf("approval kind = %v", approval.Kind)
		}
		if approval.Commentary == nil || *approval.Commentary != commentary {
			t.Fatalf("approval commentary = %v, want exact Task Detail commentary", approval.Commentary)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for approval")
	}
}
