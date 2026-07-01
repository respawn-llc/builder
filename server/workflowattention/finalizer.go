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

type InterruptedRunProjection struct {
	ProjectID        string
	WorkflowID       string
	TaskID           workflow.TaskID
	TaskShortID      string
	TaskTitle        string
	RunID            workflow.RunID
	SessionID        string
	Message          string
	Reason           string
	DetailJSON       string
	OccurredAtUnixMs int64
}

type InterruptedRunProjectionProvider interface {
	InterruptedRunProjection(ctx context.Context, runID workflow.RunID) (InterruptedRunProjection, bool, error)
}

type Publisher interface {
	PublishPending(scope attentionnotify.RoutingScope, notification clientui.AttentionNotification) error
	PublishResolved(scope attentionnotify.RoutingScope, id clientui.AttentionNotificationID, kind clientui.AttentionNotificationKind, occurredAt time.Time) error
}

type Finalizer struct {
	mu          sync.Mutex
	projection  ApprovalProjectionProvider
	runs        InterruptedRunProjectionProvider
	publisher   Publisher
	active      map[workflow.TransitionID]attentionnotify.RoutingScope
	resolved    map[workflow.TransitionID]struct{}
	runActive   map[workflow.RunID]interruptedRunActiveNotification
	runResolved map[interruptedRunOccurrenceKey]struct{}
}

type interruptedRunActiveNotification struct {
	scope      attentionnotify.RoutingScope
	occurrence interruptedRunOccurrenceKey
}

type interruptedRunOccurrenceKey struct {
	runID            workflow.RunID
	occurredAtUnixMs int64
}

func NewFinalizer(projection ApprovalProjectionProvider, publisher Publisher) *Finalizer {
	runs, _ := projection.(InterruptedRunProjectionProvider)
	return &Finalizer{
		projection:  projection,
		runs:        runs,
		publisher:   publisher,
		active:      map[workflow.TransitionID]attentionnotify.RoutingScope{},
		resolved:    map[workflow.TransitionID]struct{}{},
		runActive:   map[workflow.RunID]interruptedRunActiveNotification{},
		runResolved: map[interruptedRunOccurrenceKey]struct{}{},
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
		f.publishResolvedWithScope(projection.TransitionID, approvalRoutingScope(projection))
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
	case "approved":
		f.publishResolved(ctx, result.TransitionID)
	}
}

func (f *Finalizer) FinalizeInterruptedRun(ctx context.Context, runID workflow.RunID) {
	if f == nil || f.publisher == nil || f.runs == nil || runID == "" {
		return
	}
	projection, ok, err := f.runs.InterruptedRunProjection(ctx, runID)
	if err != nil {
		slog.Warn("workflow interrupted-run attention projection failed", "run_id", string(runID), "error", err)
		return
	}
	if !ok {
		return
	}
	scope := interruptedRunRoutingScope(projection)
	notification := interruptedRunNotification(projection)
	occurrence := interruptedRunOccurrence(projection)
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.runResolved[occurrence]; ok {
		return
	}
	if err := f.publisher.PublishPending(scope, notification); err != nil {
		slog.Warn("workflow interrupted-run attention publish failed", "run_id", string(runID), "task_id", string(projection.TaskID), "error", err)
		return
	}
	f.runActive[runID] = interruptedRunActiveNotification{scope: scope, occurrence: occurrence}
}

func (f *Finalizer) ResolveInterruptedRun(ctx context.Context, runID workflow.RunID) {
	if f == nil || f.publisher == nil || runID == "" {
		return
	}
	f.mu.Lock()
	active, ok := f.runActive[runID]
	if ok {
		delete(f.runActive, runID)
		f.runResolved[active.occurrence] = struct{}{}
	}
	f.mu.Unlock()
	var scope attentionnotify.RoutingScope
	if !ok {
		var resolved bool
		var occurrence interruptedRunOccurrenceKey
		scope, occurrence, resolved = f.resolveInterruptedRunScope(ctx, runID)
		if !resolved {
			return
		}
		f.mu.Lock()
		f.runResolved[occurrence] = struct{}{}
		f.mu.Unlock()
	} else {
		scope = active.scope
	}
	if err := f.publisher.PublishResolved(scope, interruptedRunNotificationID(runID), clientui.AttentionNotificationKindInterruptedRun, time.Now().UTC()); err != nil {
		slog.Warn("workflow interrupted-run attention resolved publish failed", "run_id", string(runID), "error", err)
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
	scope := approvalRoutingScope(projection)
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

func (f *Finalizer) publishResolvedWithScope(transitionID workflow.TransitionID, scope attentionnotify.RoutingScope) {
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

func (f *Finalizer) resolveApprovalScope(ctx context.Context, transitionID workflow.TransitionID) (attentionnotify.RoutingScope, bool) {
	if f.projection == nil {
		return attentionnotify.RoutingScope{}, false
	}
	projection, ok, err := f.projection.ApprovalProjection(ctx, transitionID)
	if err != nil {
		slog.Warn("workflow approval attention resolution projection failed", "transition_id", string(transitionID), "error", err)
		return attentionnotify.RoutingScope{}, false
	}
	if !ok {
		return attentionnotify.RoutingScope{}, false
	}
	return approvalRoutingScope(projection), true
}

func (f *Finalizer) resolveInterruptedRunScope(ctx context.Context, runID workflow.RunID) (attentionnotify.RoutingScope, interruptedRunOccurrenceKey, bool) {
	if f.runs == nil {
		return attentionnotify.RoutingScope{}, interruptedRunOccurrenceKey{}, false
	}
	projection, ok, err := f.runs.InterruptedRunProjection(ctx, runID)
	if err != nil {
		slog.Warn("workflow interrupted-run attention resolution projection failed", "run_id", string(runID), "error", err)
		return attentionnotify.RoutingScope{}, interruptedRunOccurrenceKey{}, false
	}
	if !ok {
		return attentionnotify.RoutingScope{}, interruptedRunOccurrenceKey{}, false
	}
	return interruptedRunRoutingScope(projection), interruptedRunOccurrence(projection), true
}

func interruptedRunOccurrence(projection InterruptedRunProjection) interruptedRunOccurrenceKey {
	return interruptedRunOccurrenceKey{runID: projection.RunID, occurredAtUnixMs: projection.OccurredAtUnixMs}
}

func approvalNotification(projection ApprovalProjection) clientui.AttentionNotification {
	taskShortID := strings.TrimSpace(projection.TaskShortID)
	if taskShortID == "" {
		taskShortID = strings.TrimSpace(string(projection.TaskID))
	}
	return clientui.AttentionNotification{
		ID:         approvalNotificationID(projection.TransitionID),
		Kind:       clientui.AttentionNotificationKindApproval,
		OccurredAt: time.UnixMilli(projection.OccurredAtUnixMs).UTC(),
		Revision:   1,
		Approval: &clientui.AttentionNotificationApprovalState{
			TaskTransitionID: strings.TrimSpace(string(projection.TransitionID)),
			Message:          strings.TrimSpace(projection.Message),
		},
		Target: clientui.AttentionNotificationTarget{
			Kind:        clientui.AttentionNotificationTargetWorkflowTask,
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
	}
}

func interruptedRunNotification(projection InterruptedRunProjection) clientui.AttentionNotification {
	taskShortID := strings.TrimSpace(projection.TaskShortID)
	if taskShortID == "" {
		taskShortID = strings.TrimSpace(string(projection.TaskID))
	}
	runID := strings.TrimSpace(string(projection.RunID))
	return clientui.AttentionNotification{
		ID:         interruptedRunNotificationID(projection.RunID),
		Kind:       clientui.AttentionNotificationKindInterruptedRun,
		OccurredAt: time.UnixMilli(projection.OccurredAtUnixMs).UTC(),
		Revision:   1,
		InterruptedRun: &clientui.AttentionNotificationInterruptedRunState{
			RunID:      runID,
			Message:    strings.TrimSpace(projection.Message),
			Reason:     strings.TrimSpace(projection.Reason),
			DetailJSON: strings.TrimSpace(projection.DetailJSON),
		},
		Target: clientui.AttentionNotificationTarget{
			Kind:        clientui.AttentionNotificationTargetWorkflowTask,
			ProjectID:   strings.TrimSpace(projection.ProjectID),
			WorkflowID:  strings.TrimSpace(projection.WorkflowID),
			TaskID:      strings.TrimSpace(string(projection.TaskID)),
			TaskShortID: taskShortID,
			TaskTitle:   strings.TrimSpace(projection.TaskTitle),
			RunID:       runID,
			SessionID:   strings.TrimSpace(projection.SessionID),
			Focus: &clientui.AttentionNotificationTaskDetailFocus{
				Kind:  clientui.AttentionNotificationFocusInterruptedRun,
				RunID: runID,
			},
		},
	}
}

func approvalRoutingScope(projection ApprovalProjection) attentionnotify.RoutingScope {
	return attentionnotify.RoutingScope{
		Kind:       attentionnotify.RoutingWorkflowTask,
		ProjectID:  strings.TrimSpace(projection.ProjectID),
		WorkflowID: strings.TrimSpace(projection.WorkflowID),
		TaskID:     strings.TrimSpace(string(projection.TaskID)),
		SessionID:  strings.TrimSpace(projection.SessionID),
	}
}

func approvalNotificationID(transitionID workflow.TransitionID) clientui.AttentionNotificationID {
	return clientui.AttentionNotificationID{
		Kind: clientui.AttentionNotificationKindApproval,
		UUID: strings.TrimSpace(string(transitionID)),
	}
}

func interruptedRunRoutingScope(projection InterruptedRunProjection) attentionnotify.RoutingScope {
	return attentionnotify.RoutingScope{
		Kind:       attentionnotify.RoutingWorkflowTask,
		ProjectID:  strings.TrimSpace(projection.ProjectID),
		WorkflowID: strings.TrimSpace(projection.WorkflowID),
		TaskID:     strings.TrimSpace(string(projection.TaskID)),
		SessionID:  strings.TrimSpace(projection.SessionID),
	}
}

func interruptedRunNotificationID(runID workflow.RunID) clientui.AttentionNotificationID {
	return clientui.AttentionNotificationID{
		Kind: clientui.AttentionNotificationKindInterruptedRun,
		UUID: strings.TrimSpace(string(runID)),
	}
}
