package workflowattention

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"core/server/attentionnotify"
	"core/server/workflow"
	"core/shared/clientui"
)

type TransitionResult struct {
	TransitionID                  workflow.TransitionID
	State                         string
	ResolvedApprovalTransitionIDs []workflow.TransitionID
	ResolvedApprovalProjections   []ApprovalProjection
}

type ApprovalProjection struct {
	TransitionID     workflow.TransitionID
	ProjectID        string
	WorkflowID       string
	TaskID           workflow.TaskID
	TaskShortID      string
	TaskTitle        string
	RunID            string
	SessionID        string
	Message          string
	OccurredAtUnixMs int64
}

type ApprovalProjectionProvider interface {
	ApprovalProjection(ctx context.Context, transitionID workflow.TransitionID) (ApprovalProjection, bool, error)
}

type Publisher interface {
	PublishPending(scope attentionnotify.DeliveryScope, notification clientui.AttentionNotification) error
	PublishResolved(scope attentionnotify.DeliveryScope, id string, kind clientui.AttentionNotificationKind, occurredAt time.Time) error
}

type Finalizer struct {
	mu         sync.Mutex
	projection ApprovalProjectionProvider
	publisher  Publisher
	active     map[workflow.TransitionID]attentionnotify.DeliveryScope
	resolved   map[workflow.TransitionID]struct{}
}

func NewFinalizer(projection ApprovalProjectionProvider, publisher Publisher) *Finalizer {
	return &Finalizer{
		projection: projection,
		publisher:  publisher,
		active:     map[workflow.TransitionID]attentionnotify.DeliveryScope{},
		resolved:   map[workflow.TransitionID]struct{}{},
	}
}

func (f *Finalizer) FinalizeTransition(ctx context.Context, result TransitionResult) {
	if f == nil {
		return
	}
	resolved := map[workflow.TransitionID]struct{}{}
	for _, projection := range result.ResolvedApprovalProjections {
		if projection.TransitionID == "" {
			continue
		}
		f.publishResolvedWithScope(projection.TransitionID, approvalDeliveryScope(projection))
		resolved[projection.TransitionID] = struct{}{}
	}
	for _, transitionID := range result.ResolvedApprovalTransitionIDs {
		if _, ok := resolved[transitionID]; ok {
			continue
		}
		f.publishResolved(ctx, transitionID)
	}
	if result.TransitionID == "" {
		return
	}
	switch strings.TrimSpace(result.State) {
	case "pending_approval":
		f.publishPending(ctx, result.TransitionID)
	case "approved", "applied":
		f.publishResolved(ctx, result.TransitionID)
	}
}

func (f *Finalizer) publishPending(ctx context.Context, transitionID workflow.TransitionID) {
	if f.projection == nil || f.publisher == nil {
		return
	}
	projection, ok, err := f.projection.ApprovalProjection(ctx, transitionID)
	if err != nil {
		slog.Warn("workflow approval attention projection failed", "transition_id", string(transitionID), "error", err)
		return
	}
	if !ok {
		return
	}
	scope := approvalDeliveryScope(projection)
	notification := approvalNotification(projection)
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.resolved[transitionID]; ok {
		return
	}
	if err := f.publisher.PublishPending(scope, notification); err != nil {
		slog.Warn("workflow approval attention publish failed", "transition_id", string(transitionID), "task_id", string(projection.TaskID), "error", err)
		return
	}
	f.active[transitionID] = scope
}

func (f *Finalizer) publishResolved(ctx context.Context, transitionID workflow.TransitionID) {
	if f.publisher == nil {
		return
	}
	f.mu.Lock()
	scope, ok := f.active[transitionID]
	if ok {
		delete(f.active, transitionID)
	}
	f.resolved[transitionID] = struct{}{}
	f.mu.Unlock()
	if !ok {
		var resolved bool
		scope, resolved = f.resolveApprovalScope(ctx, transitionID)
		if !resolved {
			return
		}
	}
	if err := f.publisher.PublishResolved(scope, approvalNotificationID(transitionID), clientui.AttentionNotificationKindApproval, time.Now().UTC()); err != nil {
		slog.Warn("workflow approval attention resolved publish failed", "transition_id", string(transitionID), "error", err)
	}
}

func (f *Finalizer) publishResolvedWithScope(transitionID workflow.TransitionID, scope attentionnotify.DeliveryScope) {
	if f.publisher == nil {
		return
	}
	f.mu.Lock()
	delete(f.active, transitionID)
	f.resolved[transitionID] = struct{}{}
	f.mu.Unlock()
	if err := f.publisher.PublishResolved(scope, approvalNotificationID(transitionID), clientui.AttentionNotificationKindApproval, time.Now().UTC()); err != nil {
		slog.Warn("workflow approval attention resolved publish failed", "transition_id", string(transitionID), "error", err)
	}
}

func (f *Finalizer) resolveApprovalScope(ctx context.Context, transitionID workflow.TransitionID) (attentionnotify.DeliveryScope, bool) {
	if f.projection == nil {
		return attentionnotify.DeliveryScope{}, false
	}
	projection, ok, err := f.projection.ApprovalProjection(ctx, transitionID)
	if err != nil {
		slog.Warn("workflow approval attention resolution projection failed", "transition_id", string(transitionID), "error", err)
		return attentionnotify.DeliveryScope{}, false
	}
	if !ok {
		return attentionnotify.DeliveryScope{}, false
	}
	return approvalDeliveryScope(projection), true
}

func approvalNotification(projection ApprovalProjection) clientui.AttentionNotification {
	body := strings.TrimSpace(projection.Message)
	if body == "" {
		body = "action required"
	}
	taskShortID := strings.TrimSpace(projection.TaskShortID)
	if taskShortID == "" {
		taskShortID = strings.TrimSpace(string(projection.TaskID))
	}
	return clientui.AttentionNotification{
		ID:         approvalNotificationID(projection.TransitionID),
		Kind:       clientui.AttentionNotificationKindApproval,
		OccurredAt: time.UnixMilli(projection.OccurredAtUnixMs).UTC(),
		Revision:   1,
		Target: clientui.AttentionNotificationTarget{
			Kind:        clientui.AttentionNotificationTargetTaskDetail,
			ProjectID:   strings.TrimSpace(projection.ProjectID),
			WorkflowID:  strings.TrimSpace(projection.WorkflowID),
			TaskID:      strings.TrimSpace(string(projection.TaskID)),
			TaskShortID: taskShortID,
			TaskTitle:   strings.TrimSpace(projection.TaskTitle),
			RunID:       strings.TrimSpace(projection.RunID),
			SessionID:   strings.TrimSpace(projection.SessionID),
			Focus: &clientui.AttentionNotificationTaskDetailFocus{
				Kind:             clientui.AttentionNotificationFocusApproval,
				TaskTransitionID: strings.TrimSpace(string(projection.TransitionID)),
			},
		},
		Presentation: clientui.AttentionNotificationPresentation{
			Title:        taskShortID + ": Action required",
			Body:         body,
			Preview:      body,
			FallbackBody: "action required",
			Count:        1,
		},
	}
}

func approvalDeliveryScope(projection ApprovalProjection) attentionnotify.DeliveryScope {
	return attentionnotify.DeliveryScope{
		Kind:       attentionnotify.DeliveryTaskDetail,
		ProjectID:  strings.TrimSpace(projection.ProjectID),
		WorkflowID: strings.TrimSpace(projection.WorkflowID),
		TaskID:     strings.TrimSpace(string(projection.TaskID)),
		SessionID:  strings.TrimSpace(projection.SessionID),
	}
}

func approvalNotificationID(transitionID workflow.TransitionID) string {
	return "approval:" + strings.TrimSpace(string(transitionID))
}
