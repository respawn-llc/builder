package workflowattention

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"core/server/attentionnotify"
	"core/server/workflow"
	"core/shared/clientui"
	"core/shared/runtimeids"
)

type Resolution struct {
	Approvals               []ApprovalProjection
	InterruptedCurrentNodes []InterruptedCurrentNodeProjection
}

type ApprovalProjection struct {
	ApprovalID       workflow.ApprovalID
	Source           workflow.CurrentNodeReference
	ProjectID        string
	WorkflowID       runtimeids.WorkflowID
	TaskShortID      string
	TaskTitle        string
	SessionID        string
	OccurredAtUnixMs int64
}

type PendingApprovalProjectionProvider interface {
	PendingApprovalProjection(context.Context, workflow.ApprovalID) (ApprovalProjection, bool, error)
}

type InterruptedCurrentNodeProjection struct {
	CurrentNode      workflow.CurrentNodeReference
	ProjectID        string
	WorkflowID       runtimeids.WorkflowID
	TaskShortID      string
	TaskTitle        string
	SessionID        string
	Reason           string
	DetailJSON       string
	OccurredAtUnixMs int64
}

type PendingInterruptedCurrentNodeProjectionProvider interface {
	PendingInterruptedCurrentNodeProjection(context.Context, workflow.CurrentNodeReference) (InterruptedCurrentNodeProjection, bool, error)
}

type Publisher interface {
	PublishPending(attentionnotify.RoutingScope, clientui.AttentionNotification) error
	PublishResolved(attentionnotify.RoutingScope, clientui.AttentionNotificationID, clientui.AttentionNotificationKind, time.Time) error
}

type Finalizer struct {
	mu                      sync.Mutex
	approvals               PendingApprovalProjectionProvider
	interruptedCurrentNodes PendingInterruptedCurrentNodeProjectionProvider
	publisher               Publisher
}

func NewFinalizer(provider PendingApprovalProjectionProvider, publisher Publisher) *Finalizer {
	interrupted, _ := provider.(PendingInterruptedCurrentNodeProjectionProvider)
	return &Finalizer{
		approvals:               provider,
		interruptedCurrentNodes: interrupted,
		publisher:               publisher,
	}
}

func (f *Finalizer) PublishPendingApproval(ctx context.Context, approvalID workflow.ApprovalID) {
	if f == nil || f.approvals == nil || f.publisher == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	projection, ok, err := f.pendingApprovalProjection(ctx, approvalID)
	if err != nil {
		slog.Warn("workflow approval attention projection failed", "approval_id", approvalID.String(), "error", err)
		return
	}
	if !ok {
		return
	}
	f.publishPendingApproval(projection)
}

func (f *Finalizer) PublishPendingInterruptedCurrentNode(ctx context.Context, reference workflow.CurrentNodeReference) {
	if f == nil || f.interruptedCurrentNodes == nil || f.publisher == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	projection, ok, err := f.pendingInterruptedCurrentNodeProjection(ctx, reference)
	if err != nil {
		slog.Warn("workflow interrupted-current-node attention projection failed", "task_id", reference.TaskID, "node_id", reference.NodeID, "error", err)
		return
	}
	if !ok {
		return
	}
	f.publishPendingInterruptedCurrentNode(projection)
}

func (f *Finalizer) EnqueuePendingApprovalSnapshot(
	ctx context.Context,
	approvalID workflow.ApprovalID,
	enqueue func(clientui.AttentionNotification) error,
) (bool, error) {
	if f == nil || f.approvals == nil {
		return false, nil
	}
	if enqueue == nil {
		return false, errors.New("workflow approval attention snapshot enqueue is required")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	projection, ok, err := f.pendingApprovalProjection(ctx, approvalID)
	if err != nil || !ok {
		return false, err
	}
	if err := enqueue(approvalNotification(projection)); err != nil {
		return false, err
	}
	return true, nil
}

func (f *Finalizer) EnqueuePendingInterruptedCurrentNodeSnapshot(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
	enqueue func(clientui.AttentionNotification) error,
) (bool, error) {
	if f == nil || f.interruptedCurrentNodes == nil {
		return false, nil
	}
	if enqueue == nil {
		return false, errors.New("workflow interrupted-current-node attention snapshot enqueue is required")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	projection, ok, err := f.pendingInterruptedCurrentNodeProjection(ctx, reference)
	if err != nil || !ok {
		return false, err
	}
	if err := enqueue(interruptedCurrentNodeNotification(projection)); err != nil {
		return false, err
	}
	return true, nil
}

func (f *Finalizer) pendingApprovalProjection(ctx context.Context, approvalID workflow.ApprovalID) (ApprovalProjection, bool, error) {
	projection, ok, err := f.approvals.PendingApprovalProjection(ctx, approvalID)
	if err != nil || !ok {
		return ApprovalProjection{}, ok, err
	}
	if projection.ApprovalID != approvalID {
		return ApprovalProjection{}, false, fmt.Errorf(
			"workflow approval attention projection identity mismatch: requested %q, got %q",
			approvalID,
			projection.ApprovalID,
		)
	}
	if err := validateApprovalOccurrence(projection); err != nil {
		return ApprovalProjection{}, false, err
	}
	return projection, true, nil
}

func (f *Finalizer) pendingInterruptedCurrentNodeProjection(ctx context.Context, reference workflow.CurrentNodeReference) (InterruptedCurrentNodeProjection, bool, error) {
	projection, ok, err := f.interruptedCurrentNodes.PendingInterruptedCurrentNodeProjection(ctx, reference)
	if err != nil || !ok {
		return InterruptedCurrentNodeProjection{}, ok, err
	}
	if !projection.CurrentNode.Equal(reference) {
		return InterruptedCurrentNodeProjection{}, false, fmt.Errorf(
			"workflow interrupted-current-node attention projection identity mismatch: requested %+v, got %+v",
			reference,
			projection.CurrentNode,
		)
	}
	if err := validateInterruptedCurrentNodeOccurrence(projection); err != nil {
		return InterruptedCurrentNodeProjection{}, false, err
	}
	return projection, true, nil
}

func (f *Finalizer) FinalizeResolution(resolution Resolution) {
	if f == nil {
		return
	}
	for _, approval := range resolution.Approvals {
		f.resolveApproval(approval)
	}
	for _, interrupted := range resolution.InterruptedCurrentNodes {
		f.resolveInterruptedCurrentNode(interrupted)
	}
}

func (f *Finalizer) publishPendingApproval(projection ApprovalProjection) {
	if err := f.publisher.PublishPending(approvalRoutingScope(projection), approvalNotification(projection)); err != nil {
		slog.Warn("workflow approval attention publish failed", "approval_id", projection.ApprovalID.String(), "task_id", projection.Source.TaskID, "error", err)
	}
}

func (f *Finalizer) publishPendingInterruptedCurrentNode(projection InterruptedCurrentNodeProjection) {
	if err := f.publisher.PublishPending(interruptedCurrentNodeRoutingScope(projection), interruptedCurrentNodeNotification(projection)); err != nil {
		slog.Warn("workflow interrupted-current-node attention publish failed", "task_id", projection.CurrentNode.TaskID, "node_id", projection.CurrentNode.NodeID, "error", err)
	}
}

func (f *Finalizer) resolveApproval(projection ApprovalProjection) {
	if f == nil || f.publisher == nil {
		return
	}
	if err := projection.ApprovalID.Validate(); err != nil {
		slog.Warn("workflow approval resolution has invalid identity", "error", err)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.publisher.PublishResolved(approvalRoutingScope(projection), approvalNotificationID(projection.ApprovalID), clientui.AttentionNotificationKindWorkflowApproval, time.Now().UTC()); err != nil {
		slog.Warn("workflow approval attention resolved publish failed", "approval_id", projection.ApprovalID.String(), "error", err)
	}
}

func (f *Finalizer) resolveInterruptedCurrentNode(projection InterruptedCurrentNodeProjection) {
	if f == nil || f.publisher == nil {
		return
	}
	if err := validateInterruptedCurrentNodeOccurrence(projection); err != nil {
		slog.Warn("workflow interrupted-current-node resolution is invalid", "error", err)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.publisher.PublishResolved(interruptedCurrentNodeRoutingScope(projection), interruptedCurrentNodeNotificationID(projection.CurrentNode), clientui.AttentionNotificationKindInterruptedCurrentNode, time.Now().UTC()); err != nil {
		slog.Warn("workflow interrupted-current-node attention resolved publish failed", "task_id", projection.CurrentNode.TaskID, "node_id", projection.CurrentNode.NodeID, "error", err)
	}
}

func validateApprovalOccurrence(projection ApprovalProjection) error {
	if err := projection.ApprovalID.Validate(); err != nil {
		return err
	}
	if err := projection.Source.Validate(); err != nil {
		return err
	}
	if projection.WorkflowID.IsZero() {
		return errors.New("workflow approval workflow id is required")
	}
	if projection.OccurredAtUnixMs <= 0 {
		return fmt.Errorf("workflow approval occurrence time is required")
	}
	return nil
}

func validateInterruptedCurrentNodeOccurrence(projection InterruptedCurrentNodeProjection) error {
	if err := projection.CurrentNode.Validate(); err != nil {
		return err
	}
	if projection.WorkflowID.IsZero() {
		return errors.New("interrupted current node workflow id is required")
	}
	if projection.OccurredAtUnixMs <= 0 {
		return fmt.Errorf("interrupted current node occurrence time is required")
	}
	return nil
}

func approvalNotification(projection ApprovalProjection) clientui.AttentionNotification {
	return clientui.AttentionNotification{
		ID:         approvalNotificationID(projection.ApprovalID),
		Kind:       clientui.AttentionNotificationKindWorkflowApproval,
		OccurredAt: time.UnixMilli(projection.OccurredAtUnixMs).UTC(),
		Revision:   1,
		WorkflowApproval: &clientui.AttentionNotificationWorkflowApprovalState{
			ApprovalID: projection.ApprovalID.String(),
		},
		Target: workflowTaskTarget(projection.ProjectID, projection.WorkflowID, projection.Source, projection.TaskShortID, projection.TaskTitle, projection.SessionID, clientui.AttentionNotificationFocusApproval, projection.ApprovalID.String()),
	}
}

func interruptedCurrentNodeNotification(projection InterruptedCurrentNodeProjection) clientui.AttentionNotification {
	return clientui.AttentionNotification{
		ID:         interruptedCurrentNodeNotificationID(projection.CurrentNode),
		Kind:       clientui.AttentionNotificationKindInterruptedCurrentNode,
		OccurredAt: time.UnixMilli(projection.OccurredAtUnixMs).UTC(),
		Revision:   1,
		InterruptedCurrentNode: &clientui.AttentionNotificationInterruptedCurrentNodeState{
			Reason:     strings.TrimSpace(projection.Reason),
			DetailJSON: strings.TrimSpace(projection.DetailJSON),
		},
		Target: workflowTaskTarget(projection.ProjectID, projection.WorkflowID, projection.CurrentNode, projection.TaskShortID, projection.TaskTitle, projection.SessionID, clientui.AttentionNotificationFocusInterruptedCurrentNode, ""),
	}
}

func workflowTaskTarget(projectID string, workflowID runtimeids.WorkflowID, reference workflow.CurrentNodeReference, shortID, title, sessionID string, focusKind clientui.AttentionNotificationFocusKind, approvalID string) clientui.AttentionNotificationTarget {
	nodeID := string(reference.NodeID)
	target := clientui.AttentionNotificationTarget{
		Kind:          clientui.AttentionNotificationTargetWorkflowTask,
		ProjectID:     strings.TrimSpace(projectID),
		WorkflowID:    &workflowID,
		TaskID:        string(reference.TaskID),
		TaskShortID:   strings.TrimSpace(shortID),
		TaskTitle:     strings.TrimSpace(title),
		SessionID:     strings.TrimSpace(sessionID),
		CurrentNodeID: &nodeID,
		Focus:         &clientui.AttentionNotificationTaskDetailFocus{Kind: focusKind, ApprovalID: approvalID},
	}
	if branchKey, branchScoped := reference.TransitionBranchKey(); branchScoped {
		value := string(branchKey)
		target.CurrentNodeBranchKey = &value
	}
	return target
}

func approvalRoutingScope(projection ApprovalProjection) attentionnotify.RoutingScope {
	return attentionnotify.RoutingScope{
		Kind:       attentionnotify.RoutingWorkflowTask,
		ProjectID:  strings.TrimSpace(projection.ProjectID),
		WorkflowID: &projection.WorkflowID,
		TaskID:     string(projection.Source.TaskID),
		SessionID:  strings.TrimSpace(projection.SessionID),
	}
}

func interruptedCurrentNodeRoutingScope(projection InterruptedCurrentNodeProjection) attentionnotify.RoutingScope {
	return attentionnotify.RoutingScope{
		Kind:       attentionnotify.RoutingWorkflowTask,
		ProjectID:  strings.TrimSpace(projection.ProjectID),
		WorkflowID: &projection.WorkflowID,
		TaskID:     string(projection.CurrentNode.TaskID),
		SessionID:  strings.TrimSpace(projection.SessionID),
	}
}

func approvalNotificationID(approvalID workflow.ApprovalID) clientui.AttentionNotificationID {
	return clientui.AttentionNotificationID{Kind: clientui.AttentionNotificationKindWorkflowApproval, UUID: approvalID.String()}
}

func interruptedCurrentNodeNotificationID(reference workflow.CurrentNodeReference) clientui.AttentionNotificationID {
	branchKey, branchScoped := reference.TransitionBranchKey()
	id := string(reference.TaskID) + ":" + string(reference.NodeID)
	if branchScoped {
		id += ":" + string(branchKey)
	}
	return clientui.AttentionNotificationID{Kind: clientui.AttentionNotificationKindInterruptedCurrentNode, UUID: id}
}
