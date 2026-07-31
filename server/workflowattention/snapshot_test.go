package workflowattention

import (
	"context"
	"testing"
	"time"

	"core/server/workflow"
	"core/shared/clientui"
	"core/shared/runtimeids"
)

func TestFinalizerMaterializesCurrentDurableNotificationsWithoutLivePublication(t *testing.T) {
	approvalID, err := workflow.ParseApprovalID("33333333-3333-4333-8333-333333333333")
	if err != nil {
		t.Fatalf("ParseApprovalID: %v", err)
	}
	currentNode, err := workflow.NewCurrentNodeReference("task-1", "node-1", nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	approval := ApprovalProjection{
		ApprovalID:       approvalID,
		Source:           currentNode,
		ProjectID:        "project-1",
		WorkflowID:       runtimeids.NewWorkflowID(),
		OccurredAtUnixMs: 1,
	}
	interrupted := InterruptedCurrentNodeProjection{
		CurrentNode:      currentNode,
		ProjectID:        "project-1",
		WorkflowID:       runtimeids.NewWorkflowID(),
		Reason:           "server_restart",
		OccurredAtUnixMs: 2,
	}
	publisher := &notificationPublisher{}
	finalizer := NewFinalizer(notificationProjectionProvider{approval: approval, interrupted: interrupted}, publisher)

	var approvalNotification clientui.AttentionNotification
	found, err := finalizer.EnqueuePendingApprovalSnapshot(context.Background(), approvalID, func(notification clientui.AttentionNotification) error {
		approvalNotification = notification
		return nil
	})
	if err != nil {
		t.Fatalf("EnqueuePendingApprovalSnapshot: %v", err)
	}
	if !found ||
		approvalNotification.Kind != clientui.AttentionNotificationKindWorkflowApproval ||
		approvalNotification.WorkflowApproval == nil ||
		approvalNotification.WorkflowApproval.ApprovalID != approvalID.String() {
		t.Fatalf("approval snapshot = %+v, found %t", approvalNotification, found)
	}

	var interruptedNotification clientui.AttentionNotification
	found, err = finalizer.EnqueuePendingInterruptedCurrentNodeSnapshot(context.Background(), currentNode, func(notification clientui.AttentionNotification) error {
		interruptedNotification = notification
		return nil
	})
	if err != nil {
		t.Fatalf("EnqueuePendingInterruptedCurrentNodeSnapshot: %v", err)
	}
	if !found ||
		interruptedNotification.Kind != clientui.AttentionNotificationKindInterruptedCurrentNode ||
		interruptedNotification.InterruptedCurrentNode == nil ||
		interruptedNotification.InterruptedCurrentNode.Reason != interrupted.Reason {
		t.Fatalf("interrupted snapshot = %+v, found %t", interruptedNotification, found)
	}
	if len(publisher.pending) != 0 || len(publisher.resolved) != 0 {
		t.Fatalf("snapshot materialization published live events: pending=%+v resolved=%+v", publisher.pending, publisher.resolved)
	}
}

func TestFinalizerEnqueuesSnapshotBeforeConcurrentResolution(t *testing.T) {
	approvalID, err := workflow.ParseApprovalID("44444444-4444-4444-8444-444444444444")
	if err != nil {
		t.Fatalf("ParseApprovalID: %v", err)
	}
	currentNode, err := workflow.NewCurrentNodeReference("task-1", "node-1", nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	projection := ApprovalProjection{
		ApprovalID:       approvalID,
		Source:           currentNode,
		ProjectID:        "project-1",
		WorkflowID:       runtimeids.NewWorkflowID(),
		OccurredAtUnixMs: 1,
	}
	publisher := &blockingPendingPublisher{
		resolvedEntered: make(chan struct{}),
		events:          make(chan clientui.AttentionNotificationEventType, 1),
	}
	finalizer := NewFinalizer(notificationProjectionProvider{approval: projection}, publisher)
	enqueueEntered := make(chan struct{})
	releaseEnqueue := make(chan struct{})
	snapshotDone := make(chan error, 1)
	go func() {
		_, err := finalizer.EnqueuePendingApprovalSnapshot(context.Background(), approvalID, func(clientui.AttentionNotification) error {
			close(enqueueEntered)
			<-releaseEnqueue
			return nil
		})
		snapshotDone <- err
	}()
	<-enqueueEntered

	resolutionDone := make(chan struct{})
	go func() {
		finalizer.FinalizeResolution(Resolution{Approvals: []ApprovalProjection{projection}})
		close(resolutionDone)
	}()
	select {
	case <-publisher.resolvedEntered:
		t.Fatal("resolved notification overtook snapshot enqueue")
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseEnqueue)
	if err := <-snapshotDone; err != nil {
		t.Fatalf("EnqueuePendingApprovalSnapshot: %v", err)
	}
	<-resolutionDone
}
