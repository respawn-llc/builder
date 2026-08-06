package workflowview

import (
	"core/internal/testharness/workflowtest"
	"testing"

	"core/server/workflow"
	"core/server/workflowstore"
)

func TestAttentionPagesTypedDurableNotificationReferences(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, true)
	approvalTask := fixture.startTask(t, "Approval")
	completed, err := workflowtest.CompleteCurrentNode(fixture.store, fixture.ctx, workflowstore.CurrentNodeCompletionRequest{
		Source:       approvalTask.currentNode,
		TransitionID: "done",
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	if completed.PendingApproval == nil {
		t.Fatal("pending Approval is missing")
	}
	firstInterrupted := fixture.startTask(t, "Interrupted first")
	secondInterrupted := fixture.startTask(t, "Interrupted second")
	for _, task := range []startedCurrentNodeViewTask{firstInterrupted, secondInterrupted} {
		if err := fixture.store.InterruptCurrentNode(
			fixture.ctx,
			task.currentNode,
			workflow.CurrentNodeInterruptionReason("server_restart"),
			workflow.CurrentNodeInterruptionDetail{Code: "restart"},
		); err != nil {
			t.Fatalf("InterruptCurrentNode %s: %v", task.task.ID, err)
		}
	}
	fixture.setApprovalCreatedAt(t, completed.PendingApproval.ID.String(), 3_000)
	fixture.setCurrentNodeInterruptedAt(t, firstInterrupted.currentNode, 2_000)
	fixture.setCurrentNodeInterruptedAt(t, secondInterrupted.currentNode, 1_000)
	attention := fixture.attention(t)

	first, err := attention.ListDurableNotificationReferences(fixture.ctx, nil, 2)
	if err != nil {
		t.Fatalf("ListDurableNotificationReferences first: %v", err)
	}
	if len(first.References) != 2 || first.Next == nil {
		t.Fatalf("first durable reference page = %+v", first)
	}
	approval, ok := first.References[0].(DurableApprovalAttentionReference)
	if !ok || approval.ApprovalID != completed.PendingApproval.ID {
		t.Fatalf("first durable reference = %#v", first.References[0])
	}
	interrupted, ok := first.References[1].(DurableInterruptedCurrentNodeAttentionReference)
	if !ok || !interrupted.CurrentNode.Equal(firstInterrupted.currentNode) {
		t.Fatalf("second durable reference = %#v", first.References[1])
	}

	second, err := attention.ListDurableNotificationReferences(fixture.ctx, first.Next, 2)
	if err != nil {
		t.Fatalf("ListDurableNotificationReferences second: %v", err)
	}
	if len(second.References) != 1 || second.Next != nil {
		t.Fatalf("second durable reference page = %+v", second)
	}
	interrupted, ok = second.References[0].(DurableInterruptedCurrentNodeAttentionReference)
	if !ok || !interrupted.CurrentNode.Equal(secondInterrupted.currentNode) {
		t.Fatalf("third durable reference = %#v", second.References[0])
	}
}
