package runtimewire

import (
	"context"
	"reflect"
	"testing"

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
