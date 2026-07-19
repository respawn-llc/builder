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
	TransitionID                      workflow.TransitionID
	State                             string
	ResolvedApprovalProjections       []ApprovalProjection
	ResolvedInterruptedRunProjections []InterruptedRunProjection
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

type PendingApprovalProjectionProvider interface {
	PendingApprovalProjection(ctx context.Context, transitionID workflow.TransitionID) (ApprovalProjection, bool, error)
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

type PendingInterruptedRunProjectionProvider interface {
	PendingInterruptedRunProjection(ctx context.Context, runID workflow.RunID) (InterruptedRunProjection, bool, error)
}

type Publisher interface {
	PublishPending(scope attentionnotify.RoutingScope, notification clientui.AttentionNotification) error
	PublishResolved(scope attentionnotify.RoutingScope, id clientui.AttentionNotificationID, kind clientui.AttentionNotificationKind, occurredAt time.Time) error
}

type Finalizer struct {
	mu          sync.Mutex
	projection  PendingApprovalProjectionProvider
	runs        PendingInterruptedRunProjectionProvider
	publisher   Publisher
	resolved    map[workflow.TransitionID]struct{}
	runResolved map[interruptedRunOccurrenceKey]struct{}
}

type interruptedRunOccurrenceKey struct {
	runID            workflow.RunID
	occurredAtUnixMs int64
}

func NewFinalizer(projection PendingApprovalProjectionProvider, publisher Publisher) *Finalizer {
	runs, _ := projection.(PendingInterruptedRunProjectionProvider)
	return &Finalizer{
		projection:  projection,
		runs:        runs,
		publisher:   publisher,
		resolved:    map[workflow.TransitionID]struct{}{},
		runResolved: map[interruptedRunOccurrenceKey]struct{}{},
	}
}

func (f *Finalizer) FinalizeTransition(ctx context.Context, result TransitionResult) {
	if f == nil {
		return
	}
	for _, projection := range result.ResolvedApprovalProjections {
		if projection.TransitionID == "" {
			continue
		}
		f.ResolveApproval(projection)
	}
	for _, projection := range result.ResolvedInterruptedRunProjections {
		f.ResolveInterruptedRun(projection)
	}
	if result.TransitionID == "" {
		return
	}
	if strings.TrimSpace(result.State) == "pending_approval" {
		f.PublishPendingApproval(ctx, result.TransitionID)
	}
}

func (f *Finalizer) PublishPendingApproval(ctx context.Context, transitionID workflow.TransitionID) {
	f.publishPending(ctx, transitionID)
}

func (f *Finalizer) PublishPendingInterruptedRun(ctx context.Context, runID workflow.RunID) {
	if f == nil || f.publisher == nil || f.runs == nil || runID == "" {
		return
	}
	projection, ok, err := f.runs.PendingInterruptedRunProjection(ctx, runID)
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
}

func (f *Finalizer) ResolveApproval(projection ApprovalProjection) {
	if f == nil || projection.TransitionID == "" {
		return
	}
	f.publishResolvedWithScope(projection.TransitionID, approvalRoutingScope(projection))
}

func (f *Finalizer) ResolveInterruptedRun(projection InterruptedRunProjection) {
	runID := projection.RunID
	if f == nil || f.publisher == nil || runID == "" {
		return
	}
	occurrence := interruptedRunOccurrence(projection)
	f.mu.Lock()
	if _, resolved := f.runResolved[occurrence]; resolved {
		f.mu.Unlock()
		return
	}
	f.runResolved[occurrence] = struct{}{}
	f.mu.Unlock()
	scope := interruptedRunRoutingScope(projection)
	if err := f.publisher.PublishResolved(scope, interruptedRunNotificationID(runID), clientui.AttentionNotificationKindInterruptedRun, time.Now().UTC()); err != nil {
		slog.Warn("workflow interrupted-run attention resolved publish failed", "run_id", string(runID), "error", err)
	}
}

func (f *Finalizer) publishPending(ctx context.Context, transitionID workflow.TransitionID) {
	if f.projection == nil || f.publisher == nil {
		return
	}
	projection, ok, err := f.projection.PendingApprovalProjection(ctx, transitionID)
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
}

func (f *Finalizer) publishResolvedWithScope(transitionID workflow.TransitionID, scope attentionnotify.RoutingScope) {
	if f.publisher == nil {
		return
	}
	f.mu.Lock()
	f.resolved[transitionID] = struct{}{}
	f.mu.Unlock()
	if err := f.publisher.PublishResolved(scope, approvalNotificationID(transitionID), clientui.AttentionNotificationKindApproval, time.Now().UTC()); err != nil {
		slog.Warn("workflow approval attention resolved publish failed", "transition_id", string(transitionID), "error", err)
	}
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
