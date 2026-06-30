package workflowattention

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/attentionnotify"
	"core/server/workflow"
	"core/shared/clientui"
)

func TestFinalizerPublishesPendingApprovalAndResolvedForActiveTransition(t *testing.T) {
	publisher := &recordingPublisher{}
	finalizer := NewFinalizer(approvalProjectionProviderFunc(func(context.Context, workflow.TransitionID) (ApprovalProjection, bool, error) {
		return ApprovalProjection{
			TransitionID:     "transition-1",
			ProjectID:        "project-1",
			WorkflowID:       "workflow-1",
			TaskID:           "task-1",
			TaskShortID:      "RUN-1",
			TaskTitle:        "Task title",
			RunID:            "run-1",
			SessionID:        "session-1",
			Message:          "Review generated work",
			OccurredAtUnixMs: 1_700_000_000_000,
		}, true, nil
	}), publisher)

	finalizer.FinalizeTransition(context.Background(), TransitionResult{TransitionID: "transition-1", State: "pending_approval"})

	if len(publisher.pending) != 1 {
		t.Fatalf("pending publications = %+v, want one", publisher.pending)
	}
	pending := publisher.pending[0]
	if pending.scope.Kind != attentionnotify.DeliveryTaskDetail || pending.scope.TaskID != "task-1" || pending.scope.SessionID != "session-1" {
		t.Fatalf("pending scope = %+v", pending.scope)
	}
	if pending.notification.Target.SessionID != "session-1" || pending.notification.Target.RunID != "run-1" {
		t.Fatalf("pending target session/run = %+v", pending.notification.Target)
	}
	if pending.notification.ID != "approval:transition-1" || pending.notification.Kind != clientui.AttentionNotificationKindApproval {
		t.Fatalf("pending notification = %+v", pending.notification)
	}
	if pending.notification.Target.Focus == nil ||
		pending.notification.Target.Focus.Kind != clientui.AttentionNotificationFocusApproval ||
		pending.notification.Target.Focus.TaskTransitionID != "transition-1" {
		t.Fatalf("pending target focus = %+v", pending.notification.Target.Focus)
	}
	if pending.notification.Presentation.Title != "RUN-1: Action required" || pending.notification.Presentation.Body != "Review generated work" {
		t.Fatalf("pending presentation = %+v", pending.notification.Presentation)
	}

	finalizer.FinalizeTransition(context.Background(), TransitionResult{TransitionID: "transition-1", State: "approved"})

	if len(publisher.resolved) != 1 {
		t.Fatalf("resolved publications = %+v, want one", publisher.resolved)
	}
	resolved := publisher.resolved[0]
	if resolved.scope.TaskID != "task-1" || resolved.id != "approval:transition-1" || resolved.kind != clientui.AttentionNotificationKindApproval {
		t.Fatalf("resolved publication = %+v", resolved)
	}
}

func TestFinalizerPublishesApprovalNotificationThroughAttentionBroker(t *testing.T) {
	broker := attentionnotify.NewBroker()
	sub, err := broker.SubscribeDesktop()
	if err != nil {
		t.Fatalf("SubscribeDesktop: %v", err)
	}
	sessionSub, err := broker.SubscribeSession("session-1")
	if err != nil {
		t.Fatalf("SubscribeSession: %v", err)
	}
	finalizer := NewFinalizer(approvalProjectionProviderFunc(func(context.Context, workflow.TransitionID) (ApprovalProjection, bool, error) {
		return ApprovalProjection{
			TransitionID:     "transition-1",
			ProjectID:        "project-1",
			WorkflowID:       "workflow-1",
			TaskID:           "task-1",
			TaskShortID:      "RUN-1",
			TaskTitle:        "Task title",
			RunID:            "run-1",
			SessionID:        "session-1",
			Message:          "Approve generated transition",
			OccurredAtUnixMs: 1_700_000_000_000,
		}, true, nil
	}), broker)

	finalizer.FinalizeTransition(context.Background(), TransitionResult{TransitionID: "transition-1", State: "pending_approval"})
	pending := nextAttentionEvent(t, sub)
	if pending.Type != clientui.AttentionNotificationEventPending ||
		pending.Pending == nil ||
		pending.Pending.ID != "approval:transition-1" ||
		pending.Pending.Target.Focus == nil ||
		pending.Pending.Target.Focus.TaskTransitionID != "transition-1" {
		t.Fatalf("pending event = %+v", pending)
	}
	sessionPending := nextAttentionEvent(t, sessionSub)
	if sessionPending.Type != clientui.AttentionNotificationEventPending || sessionPending.Pending == nil || sessionPending.Pending.ID != "approval:transition-1" {
		t.Fatalf("session pending event = %+v", sessionPending)
	}

	finalizer.FinalizeTransition(context.Background(), TransitionResult{TransitionID: "transition-1", State: "approved"})
	resolved := nextAttentionEvent(t, sub)
	if resolved.Type != clientui.AttentionNotificationEventResolved || resolved.ID != "approval:transition-1" {
		t.Fatalf("resolved event = %+v", resolved)
	}
	sessionResolved := nextAttentionEvent(t, sessionSub)
	if sessionResolved.Type != clientui.AttentionNotificationEventResolved || sessionResolved.ID != "approval:transition-1" {
		t.Fatalf("session resolved event = %+v", sessionResolved)
	}
}

func TestFinalizerDoesNotResolveApprovalThatWasNotActiveInMemory(t *testing.T) {
	publisher := &recordingPublisher{}
	finalizer := NewFinalizer(nil, publisher)

	finalizer.FinalizeTransition(context.Background(), TransitionResult{TransitionID: "transition-1", State: "approved"})

	if len(publisher.resolved) != 0 {
		t.Fatalf("resolved publications = %+v, want none", publisher.resolved)
	}
}

func TestFinalizerResolvesApprovalFromProjectionWhenNotActiveInMemory(t *testing.T) {
	publisher := &recordingPublisher{}
	finalizer := NewFinalizer(approvalProjectionProviderFunc(func(context.Context, workflow.TransitionID) (ApprovalProjection, bool, error) {
		return ApprovalProjection{
			TransitionID: "transition-1",
			ProjectID:    "project-1",
			WorkflowID:   "workflow-1",
			TaskID:       "task-1",
			SessionID:    "session-1",
		}, true, nil
	}), publisher)

	finalizer.FinalizeTransition(context.Background(), TransitionResult{TransitionID: "transition-1", State: "approved"})

	if len(publisher.resolved) != 1 {
		t.Fatalf("resolved publications = %+v, want one", publisher.resolved)
	}
	resolved := publisher.resolved[0]
	if resolved.scope.TaskID != "task-1" || resolved.scope.SessionID != "session-1" || resolved.id != "approval:transition-1" {
		t.Fatalf("resolved publication = %+v", resolved)
	}
}

func TestFinalizerResolvesProvidedApprovalProjectionWithoutActiveMemory(t *testing.T) {
	publisher := &recordingPublisher{}
	finalizer := NewFinalizer(nil, publisher)

	finalizer.FinalizeTransition(context.Background(), TransitionResult{
		ResolvedApprovalProjections: []ApprovalProjection{{
			TransitionID: "transition-1",
			ProjectID:    "project-1",
			WorkflowID:   "workflow-1",
			TaskID:       "task-1",
			SessionID:    "session-1",
		}},
	})

	if len(publisher.resolved) != 1 {
		t.Fatalf("resolved publications = %+v, want one", publisher.resolved)
	}
	resolved := publisher.resolved[0]
	if resolved.scope.TaskID != "task-1" || resolved.scope.SessionID != "session-1" || resolved.id != "approval:transition-1" {
		t.Fatalf("resolved publication = %+v", resolved)
	}
}

func TestFinalizerPublishFailureIsNonFatalAndDoesNotActivateTransition(t *testing.T) {
	publisher := &recordingPublisher{pendingErr: errors.New("publish failed")}
	finalizer := NewFinalizer(approvalProjectionProviderFunc(func(context.Context, workflow.TransitionID) (ApprovalProjection, bool, error) {
		return ApprovalProjection{TransitionID: "transition-1", TaskID: "task-1", TaskShortID: "RUN-1", Message: "Approve", OccurredAtUnixMs: 1}, true, nil
	}), publisher)

	finalizer.FinalizeTransition(context.Background(), TransitionResult{TransitionID: "transition-1", State: "pending_approval"})
	finalizer.FinalizeTransition(context.Background(), TransitionResult{TransitionID: "transition-1", State: "approved"})

	if len(publisher.pending) != 1 {
		t.Fatalf("pending attempts = %+v, want one", publisher.pending)
	}
	if len(publisher.resolved) != 1 {
		t.Fatalf("resolved after failed pending publish = %+v, want one scoped resolved attempt", publisher.resolved)
	}
}

func TestFinalizerResolvesReplacedApprovalBeforePublishingNewPendingApproval(t *testing.T) {
	publisher := &recordingPublisher{}
	finalizer := NewFinalizer(approvalProjectionProviderFunc(func(_ context.Context, transitionID workflow.TransitionID) (ApprovalProjection, bool, error) {
		return ApprovalProjection{TransitionID: transitionID, TaskID: "task-1", TaskShortID: "RUN-1", Message: "Approve", OccurredAtUnixMs: 1}, true, nil
	}), publisher)
	finalizer.FinalizeTransition(context.Background(), TransitionResult{TransitionID: "old", State: "pending_approval"})

	finalizer.FinalizeTransition(context.Background(), TransitionResult{
		TransitionID:                  "new",
		State:                         "pending_approval",
		ResolvedApprovalTransitionIDs: []workflow.TransitionID{"old"},
	})

	want := []string{"pending:approval:old", "resolved:approval:old", "pending:approval:new"}
	if len(publisher.calls) != len(want) {
		t.Fatalf("publish calls = %+v, want %+v", publisher.calls, want)
	}
	for index := range want {
		if publisher.calls[index] != want[index] {
			t.Fatalf("publish calls = %+v, want %+v", publisher.calls, want)
		}
	}
}

type approvalProjectionProviderFunc func(context.Context, workflow.TransitionID) (ApprovalProjection, bool, error)

func (f approvalProjectionProviderFunc) ApprovalProjection(ctx context.Context, transitionID workflow.TransitionID) (ApprovalProjection, bool, error) {
	return f(ctx, transitionID)
}

func nextAttentionEvent(t *testing.T, sub *attentionnotify.Subscription) clientui.AttentionNotificationEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	event, err := sub.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	return event
}

type recordingPublisher struct {
	pendingErr error
	calls      []string
	pending    []pendingPublication
	resolved   []resolvedPublication
}

type pendingPublication struct {
	scope        attentionnotify.DeliveryScope
	notification clientui.AttentionNotification
}

type resolvedPublication struct {
	scope      attentionnotify.DeliveryScope
	id         string
	kind       clientui.AttentionNotificationKind
	occurredAt time.Time
}

func (p *recordingPublisher) PublishPending(scope attentionnotify.DeliveryScope, notification clientui.AttentionNotification) error {
	p.calls = append(p.calls, "pending:"+notification.ID)
	p.pending = append(p.pending, pendingPublication{scope: scope, notification: notification})
	return p.pendingErr
}

func (p *recordingPublisher) PublishResolved(scope attentionnotify.DeliveryScope, id string, kind clientui.AttentionNotificationKind, occurredAt time.Time) error {
	p.calls = append(p.calls, "resolved:"+id)
	p.resolved = append(p.resolved, resolvedPublication{scope: scope, id: id, kind: kind, occurredAt: occurredAt})
	return nil
}
