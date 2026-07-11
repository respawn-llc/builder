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
	if pending.scope.Kind != attentionnotify.RoutingWorkflowTask || pending.scope.TaskID != "task-1" || pending.scope.SessionID != "session-1" {
		t.Fatalf("pending scope = %+v", pending.scope)
	}
	if pending.notification.Target.SessionID != "session-1" || pending.notification.Target.RunID != "run-1" {
		t.Fatalf("pending target session/run = %+v", pending.notification.Target)
	}
	if pending.notification.ID != approvalNotificationID("transition-1") || pending.notification.Kind != clientui.AttentionNotificationKindApproval {
		t.Fatalf("pending notification = %+v", pending.notification)
	}
	if pending.notification.Target.Focus == nil ||
		pending.notification.Target.Focus.Kind != clientui.AttentionNotificationFocusApproval ||
		pending.notification.Target.Focus.TaskTransitionID != "transition-1" {
		t.Fatalf("pending target focus = %+v", pending.notification.Target.Focus)
	}
	if pending.notification.Approval == nil || pending.notification.Approval.Message != "Review generated work" {
		t.Fatalf("pending approval payload = %+v", pending.notification.Approval)
	}

	finalizer.FinalizeTransition(context.Background(), TransitionResult{TransitionID: "transition-1", State: "approved"})

	if len(publisher.resolved) != 1 {
		t.Fatalf("resolved publications = %+v, want one", publisher.resolved)
	}
	resolved := publisher.resolved[0]
	if resolved.scope.TaskID != "task-1" || resolved.id != approvalNotificationID("transition-1") || resolved.kind != clientui.AttentionNotificationKindApproval {
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
		pending.Pending.ID != approvalNotificationID("transition-1") ||
		pending.Pending.Target.Focus == nil ||
		pending.Pending.Target.Focus.TaskTransitionID != "transition-1" {
		t.Fatalf("pending event = %+v", pending)
	}
	sessionPending := nextAttentionEvent(t, sessionSub)
	if sessionPending.Type != clientui.AttentionNotificationEventPending || sessionPending.Pending == nil || sessionPending.Pending.ID != approvalNotificationID("transition-1") {
		t.Fatalf("session pending event = %+v", sessionPending)
	}

	finalizer.FinalizeTransition(context.Background(), TransitionResult{TransitionID: "transition-1", State: "approved"})
	resolved := nextAttentionEvent(t, sub)
	if resolved.Type != clientui.AttentionNotificationEventResolved || !attentionNotificationEventIDMatches(resolved, approvalNotificationID("transition-1")) {
		t.Fatalf("resolved event = %+v", resolved)
	}
	sessionResolved := nextAttentionEvent(t, sessionSub)
	if sessionResolved.Type != clientui.AttentionNotificationEventResolved || !attentionNotificationEventIDMatches(sessionResolved, approvalNotificationID("transition-1")) {
		t.Fatalf("session resolved event = %+v", sessionResolved)
	}
}

func TestFinalizerPublishesPendingInterruptedRunAndResolvedForActiveRun(t *testing.T) {
	publisher := &recordingPublisher{}
	finalizer := NewFinalizer(combinedProjectionProvider{
		run: func(context.Context, workflow.RunID) (InterruptedRunProjection, bool, error) {
			return InterruptedRunProjection{
				ProjectID:        "project-1",
				WorkflowID:       "workflow-1",
				TaskID:           "task-1",
				TaskShortID:      "RUN-1",
				TaskTitle:        "Task title",
				RunID:            "run-1",
				SessionID:        "session-1",
				Message:          "Run interrupted: workflow_runtime_failed: model failed",
				Reason:           "workflow_runtime_failed",
				OccurredAtUnixMs: 1_700_000_000_000,
			}, true, nil
		},
	}, publisher)

	finalizer.FinalizeInterruptedRun(context.Background(), "run-1")

	if len(publisher.pending) != 1 {
		t.Fatalf("pending publications = %+v, want one", publisher.pending)
	}
	pending := publisher.pending[0]
	if pending.scope.Kind != attentionnotify.RoutingWorkflowTask || pending.scope.TaskID != "task-1" || pending.scope.SessionID != "session-1" {
		t.Fatalf("pending scope = %+v", pending.scope)
	}
	if pending.notification.ID != interruptedRunNotificationID("run-1") || pending.notification.Kind != clientui.AttentionNotificationKindInterruptedRun {
		t.Fatalf("pending notification = %+v", pending.notification)
	}
	if pending.notification.InterruptedRun == nil || pending.notification.InterruptedRun.Reason != "workflow_runtime_failed" {
		t.Fatalf("pending interrupted-run payload = %+v", pending.notification.InterruptedRun)
	}
	if pending.notification.Target.Focus == nil ||
		pending.notification.Target.Focus.Kind != clientui.AttentionNotificationFocusInterruptedRun ||
		pending.notification.Target.Focus.RunID != "run-1" {
		t.Fatalf("pending target focus = %+v", pending.notification.Target.Focus)
	}

	finalizer.ResolveInterruptedRun(context.Background(), "run-1")

	if len(publisher.resolved) != 1 {
		t.Fatalf("resolved publications = %+v, want one", publisher.resolved)
	}
	resolved := publisher.resolved[0]
	if resolved.scope.TaskID != "task-1" || resolved.id != interruptedRunNotificationID("run-1") || resolved.kind != clientui.AttentionNotificationKindInterruptedRun {
		t.Fatalf("resolved publication = %+v", resolved)
	}
}

func TestFinalizerAllowsSameRunToNotifyAfterNewInterruptionOccurrence(t *testing.T) {
	publisher := &recordingPublisher{}
	occurredAtUnixMs := int64(1_700_000_000_000)
	finalizer := NewFinalizer(combinedProjectionProvider{
		run: func(context.Context, workflow.RunID) (InterruptedRunProjection, bool, error) {
			return InterruptedRunProjection{
				ProjectID:        "project-1",
				WorkflowID:       "workflow-1",
				TaskID:           "task-1",
				TaskShortID:      "RUN-1",
				TaskTitle:        "Task title",
				RunID:            "run-1",
				SessionID:        "session-1",
				Message:          "Run interrupted",
				Reason:           "workflow_runtime_failed",
				OccurredAtUnixMs: occurredAtUnixMs,
			}, true, nil
		},
	}, publisher)

	finalizer.FinalizeInterruptedRun(context.Background(), "run-1")
	finalizer.ResolveInterruptedRun(context.Background(), "run-1")
	occurredAtUnixMs++
	finalizer.FinalizeInterruptedRun(context.Background(), "run-1")

	want := []string{"pending|interrupted_run/run-1", "resolved|interrupted_run/run-1", "pending|interrupted_run/run-1"}
	if len(publisher.calls) != len(want) {
		t.Fatalf("publish calls = %+v, want %+v", publisher.calls, want)
	}
	for index := range want {
		if publisher.calls[index] != want[index] {
			t.Fatalf("publish calls = %+v, want %+v", publisher.calls, want)
		}
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
	if resolved.scope.TaskID != "task-1" || resolved.scope.SessionID != "session-1" || resolved.id != approvalNotificationID("transition-1") {
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
	if resolved.scope.TaskID != "task-1" || resolved.scope.SessionID != "session-1" || resolved.id != approvalNotificationID("transition-1") {
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

	want := []string{"pending|approval/old", "resolved|approval/old", "pending|approval/new"}
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

type combinedProjectionProvider struct {
	approval func(context.Context, workflow.TransitionID) (ApprovalProjection, bool, error)
	run      func(context.Context, workflow.RunID) (InterruptedRunProjection, bool, error)
}

func (p combinedProjectionProvider) ApprovalProjection(ctx context.Context, transitionID workflow.TransitionID) (ApprovalProjection, bool, error) {
	if p.approval == nil {
		return ApprovalProjection{}, false, nil
	}
	return p.approval(ctx, transitionID)
}

func (p combinedProjectionProvider) InterruptedRunProjection(ctx context.Context, runID workflow.RunID) (InterruptedRunProjection, bool, error) {
	if p.run == nil {
		return InterruptedRunProjection{}, false, nil
	}
	return p.run(ctx, runID)
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
	scope        attentionnotify.RoutingScope
	notification clientui.AttentionNotification
}

type resolvedPublication struct {
	scope      attentionnotify.RoutingScope
	id         clientui.AttentionNotificationID
	kind       clientui.AttentionNotificationKind
	occurredAt time.Time
}

func (p *recordingPublisher) PublishPending(scope attentionnotify.RoutingScope, notification clientui.AttentionNotification) error {
	p.calls = append(p.calls, "pending|"+attentionNotificationIDKey(notification.ID))
	p.pending = append(p.pending, pendingPublication{scope: scope, notification: notification})
	return p.pendingErr
}

func (p *recordingPublisher) PublishResolved(scope attentionnotify.RoutingScope, id clientui.AttentionNotificationID, kind clientui.AttentionNotificationKind, occurredAt time.Time) error {
	p.calls = append(p.calls, "resolved|"+attentionNotificationIDKey(id))
	p.resolved = append(p.resolved, resolvedPublication{scope: scope, id: id, kind: kind, occurredAt: occurredAt})
	return nil
}

func attentionNotificationIDKey(id clientui.AttentionNotificationID) string {
	return string(id.Kind) + "/" + id.UUID
}

func attentionNotificationEventIDMatches(event clientui.AttentionNotificationEvent, id clientui.AttentionNotificationID) bool {
	return event.ID != nil && *event.ID == id
}
