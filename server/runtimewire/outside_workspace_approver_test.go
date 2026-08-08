package runtimewire

import (
	"context"
	"testing"

	askquestion "core/server/tools"
	patchtool "core/server/tools/patch"
)

func TestOutsideWorkspaceApprovalRetainsExecutingToolIdentity(t *testing.T) {
	broker := askquestion.NewAskQuestionBroker()
	var received askquestion.AskQuestionRequest
	broker.SetAskHandler(func(_ context.Context, request askquestion.AskQuestionRequest) (askquestion.AskQuestionResolution, error) {
		received = request
		return askquestion.AskQuestionApproval{Decision: askquestion.AskQuestionApprovalDecisionAllowOnce}, nil
	})
	approver := NewOutsideWorkspaceApprover(broker, "editing")

	_, err := approver.Approve(
		askquestion.WithExecutionIdentity(context.Background(), askquestion.ExecutionIdentity{
			RunID:  "11111111-1111-4111-8111-111111111111",
			StepID: "22222222-2222-4222-8222-222222222222",
		}),
		patchtool.OutsideWorkspaceRequest{RequestedPath: "/outside/file", ResolvedPath: "/outside/file"},
	)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if received.RunID != "11111111-1111-4111-8111-111111111111" ||
		received.StepID != "22222222-2222-4222-8222-222222222222" {
		t.Fatalf("approval identity = run %q step %q", received.RunID, received.StepID)
	}
}
