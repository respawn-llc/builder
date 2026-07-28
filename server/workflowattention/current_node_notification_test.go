package workflowattention

import (
	"context"
	"testing"
	"time"

	"core/server/attentionnotify"
	"core/server/workflow"
	"core/shared/clientui"
	"core/shared/serverapi"
)

func TestFinalizerPublishesCurrentNodeApprovalNotification(t *testing.T) {
	approvalID, err := workflow.ParseApprovalID("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatalf("ParseApprovalID: %v", err)
	}
	source, err := workflow.NewCurrentNodeReference("task-1", "node-1", nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	projection := ApprovalProjection{
		ApprovalID:       approvalID,
		Source:           source,
		ProjectID:        "project-1",
		WorkflowID:       "workflow-1",
		OccurredAtUnixMs: 1,
	}
	broker := attentionnotify.NewBroker()
	subscription, err := broker.SubscribeDesktop()
	if err != nil {
		t.Fatalf("SubscribeDesktop: %v", err)
	}
	finalizer := NewFinalizer(notificationProjectionProvider{approval: projection}, broker)

	finalizer.PublishPendingApproval(context.Background(), approvalID)

	nextContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	pendingEvent, err := subscription.Next(nextContext)
	if err != nil {
		t.Fatalf("Next pending approval: %v", err)
	}
	if pendingEvent.Type != clientui.AttentionNotificationEventPending || pendingEvent.Pending == nil {
		t.Fatalf("pending event = %+v, want pending workflow approval", pendingEvent)
	}
	got := *pendingEvent.Pending
	if got.Kind != clientui.AttentionNotificationKindWorkflowApproval || got.WorkflowApproval == nil {
		t.Fatalf("pending notification = %+v, want workflow approval", got)
	}
	if got.WorkflowApproval.ApprovalID != approvalID.String() || got.Target.TaskID != string(source.TaskID) {
		t.Fatalf("approval notification = %+v, want current-node task and approval identity", got)
	}
	if got.Target.CurrentNodeID == nil ||
		*got.Target.CurrentNodeID != string(source.NodeID) ||
		got.Target.Focus == nil ||
		got.Target.Focus.Kind != clientui.AttentionNotificationFocusApproval ||
		got.Target.Focus.ApprovalID != approvalID.String() {
		t.Fatalf("approval notification target = %+v, want approval focus on current node", got.Target)
	}
	if err := serverapi.ValidateAttentionNotificationEvent(clientui.AttentionNotificationEvent{
		Sequence: 1,
		Source:   clientui.AttentionNotificationSourceLive,
		Type:     clientui.AttentionNotificationEventPending,
		Pending:  &got,
	}); err != nil {
		t.Fatalf("ValidateAttentionNotificationEvent approval: %v", err)
	}

	finalizer.FinalizeResolution(Resolution{Approvals: []ApprovalProjection{projection}})

	resolvedEvent, err := subscription.Next(nextContext)
	if err != nil {
		t.Fatalf("Next resolved approval: %v", err)
	}
	if resolvedEvent.Type != clientui.AttentionNotificationEventResolved ||
		resolvedEvent.ID == nil ||
		*resolvedEvent.ID != got.ID ||
		resolvedEvent.Kind != clientui.AttentionNotificationKindWorkflowApproval {
		t.Fatalf("resolved event = %+v, want resolved workflow approval", resolvedEvent)
	}
}

func TestFinalizerOrdersPendingBeforeConcurrentResolution(t *testing.T) {
	approvalID, err := workflow.ParseApprovalID("22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatalf("ParseApprovalID: %v", err)
	}
	source, err := workflow.NewCurrentNodeReference("task-1", "node-1", nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	projection := ApprovalProjection{
		ApprovalID:       approvalID,
		Source:           source,
		ProjectID:        "project-1",
		WorkflowID:       "workflow-1",
		OccurredAtUnixMs: 1,
	}
	publisher := &blockingPendingPublisher{
		pendingEntered:  make(chan struct{}),
		releasePending:  make(chan struct{}),
		resolvedEntered: make(chan struct{}),
		events:          make(chan clientui.AttentionNotificationEventType, 2),
	}
	finalizer := NewFinalizer(notificationProjectionProvider{approval: projection}, publisher)
	pendingDone := make(chan struct{})
	go func() {
		finalizer.PublishPendingApproval(context.Background(), approvalID)
		close(pendingDone)
	}()
	<-publisher.pendingEntered

	resolvedDone := make(chan struct{})
	go func() {
		finalizer.FinalizeResolution(Resolution{Approvals: []ApprovalProjection{projection}})
		close(resolvedDone)
	}()
	select {
	case <-publisher.resolvedEntered:
		t.Fatal("resolved notification overtook an in-flight pending notification")
	case <-time.After(25 * time.Millisecond):
	}
	close(publisher.releasePending)
	<-pendingDone
	<-resolvedDone

	if first, second := <-publisher.events, <-publisher.events; first != clientui.AttentionNotificationEventPending || second != clientui.AttentionNotificationEventResolved {
		t.Fatalf("notification order = %q, %q; want pending then resolved", first, second)
	}
}

func TestFinalizerPublishesAndResolvesInterruptedCurrentNode(t *testing.T) {
	branch := workflow.TransitionBranchKey("branch-a")
	currentNode, err := workflow.NewCurrentNodeReference("task-1", "node-1", &branch)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	projection := InterruptedCurrentNodeProjection{
		CurrentNode:      currentNode,
		ProjectID:        "project-1",
		WorkflowID:       "workflow-1",
		TaskShortID:      "WOR-1",
		TaskTitle:        "Interrupted task",
		SessionID:        "session-1",
		Message:          "Workflow execution interrupted",
		Reason:           "server_restart",
		DetailJSON:       `{"error":"process stopped"}`,
		OccurredAtUnixMs: 1,
	}
	publisher := &notificationPublisher{}
	finalizer := NewFinalizer(notificationProjectionProvider{interrupted: projection}, publisher)

	finalizer.PublishPendingInterruptedCurrentNode(context.Background(), currentNode)

	if len(publisher.pending) != 1 {
		t.Fatalf("pending notification count = %d, want 1", len(publisher.pending))
	}
	pending := publisher.pending[0]
	if pending.ID != (clientui.AttentionNotificationID{
		Kind: clientui.AttentionNotificationKindInterruptedCurrentNode,
		UUID: "task-1:node-1:branch-a",
	}) ||
		pending.Kind != clientui.AttentionNotificationKindInterruptedCurrentNode ||
		pending.InterruptedCurrentNode == nil ||
		pending.InterruptedCurrentNode.Reason != projection.Reason ||
		pending.Target.TaskID != string(currentNode.TaskID) ||
		pending.Target.CurrentNodeID == nil ||
		*pending.Target.CurrentNodeID != string(currentNode.NodeID) ||
		pending.Target.CurrentNodeBranchKey == nil ||
		*pending.Target.CurrentNodeBranchKey != string(branch) ||
		pending.Target.Focus == nil ||
		pending.Target.Focus.Kind != clientui.AttentionNotificationFocusInterruptedCurrentNode ||
		pending.Target.Focus.ApprovalID != "" {
		t.Fatalf("interrupted notification = %+v, want Current Node focus and identity", pending)
	}
	if err := serverapi.ValidateAttentionNotificationEvent(clientui.AttentionNotificationEvent{
		Sequence: 1,
		Source:   clientui.AttentionNotificationSourceLive,
		Type:     clientui.AttentionNotificationEventPending,
		Pending:  &pending,
	}); err != nil {
		t.Fatalf("ValidateAttentionNotificationEvent interrupted: %v", err)
	}

	finalizer.FinalizeResolution(Resolution{InterruptedCurrentNodes: []InterruptedCurrentNodeProjection{projection}})

	if len(publisher.pending) != 1 {
		t.Fatalf("pending notifications = %d, want one", len(publisher.pending))
	}
	if len(publisher.resolved) != 1 {
		t.Fatalf("resolved notifications = %+v, want one", publisher.resolved)
	}
	resolved := publisher.resolved[0]
	if resolved.id != pending.ID || resolved.kind != clientui.AttentionNotificationKindInterruptedCurrentNode || resolved.occurredAt.IsZero() {
		t.Fatalf("resolved notification = %+v, want interrupted Current Node identity", resolved)
	}
	if err := serverapi.ValidateAttentionNotificationEvent(clientui.AttentionNotificationEvent{
		Sequence:   2,
		Source:     clientui.AttentionNotificationSourceLive,
		Type:       clientui.AttentionNotificationEventResolved,
		ID:         &resolved.id,
		Kind:       resolved.kind,
		OccurredAt: &resolved.occurredAt,
	}); err != nil {
		t.Fatalf("ValidateAttentionNotificationEvent resolution: %v", err)
	}
}

type notificationProjectionProvider struct {
	approval    ApprovalProjection
	interrupted InterruptedCurrentNodeProjection
}

func (p notificationProjectionProvider) PendingApprovalProjection(context.Context, workflow.ApprovalID) (ApprovalProjection, bool, error) {
	return p.approval, p.approval.ApprovalID != "", nil
}

func (p notificationProjectionProvider) PendingInterruptedCurrentNodeProjection(context.Context, workflow.CurrentNodeReference) (InterruptedCurrentNodeProjection, bool, error) {
	return p.interrupted, p.interrupted.CurrentNode.TaskID != "", nil
}

type notificationPublisher struct {
	pending  []clientui.AttentionNotification
	resolved []resolvedNotification
}

type resolvedNotification struct {
	id         clientui.AttentionNotificationID
	kind       clientui.AttentionNotificationKind
	occurredAt time.Time
}

func (p *notificationPublisher) PublishPending(_ attentionnotify.RoutingScope, notification clientui.AttentionNotification) error {
	p.pending = append(p.pending, notification)
	return nil
}

func (p *notificationPublisher) PublishResolved(_ attentionnotify.RoutingScope, id clientui.AttentionNotificationID, kind clientui.AttentionNotificationKind, occurredAt time.Time) error {
	p.resolved = append(p.resolved, resolvedNotification{id: id, kind: kind, occurredAt: occurredAt})
	return nil
}

type blockingPendingPublisher struct {
	pendingEntered  chan struct{}
	releasePending  chan struct{}
	resolvedEntered chan struct{}
	events          chan clientui.AttentionNotificationEventType
}

func (p *blockingPendingPublisher) PublishPending(attentionnotify.RoutingScope, clientui.AttentionNotification) error {
	close(p.pendingEntered)
	<-p.releasePending
	p.events <- clientui.AttentionNotificationEventPending
	return nil
}

func (p *blockingPendingPublisher) PublishResolved(attentionnotify.RoutingScope, clientui.AttentionNotificationID, clientui.AttentionNotificationKind, time.Time) error {
	close(p.resolvedEntered)
	p.events <- clientui.AttentionNotificationEventResolved
	return nil
}
