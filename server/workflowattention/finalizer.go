package workflowattention

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"core/server/attentionnotify"
	"core/server/workflow"
	"core/shared/clientui"
)

type Resolution struct {
	Approvals               []ApprovalProjection
	InterruptedCurrentNodes []InterruptedCurrentNodeProjection
}

type ApprovalProjection struct {
	ApprovalID       workflow.ApprovalID
	Source           workflow.CurrentNodeReference
	ProjectID        string
	WorkflowID       string
	TaskShortID      string
	TaskTitle        string
	SessionID        string
	Message          string
	OccurredAtUnixMs int64
}

type PendingApprovalProjectionProvider interface {
	PendingApprovalProjection(context.Context, workflow.ApprovalID) (ApprovalProjection, bool, error)
}

type InterruptedCurrentNodeProjection struct {
	CurrentNode      workflow.CurrentNodeReference
	ProjectID        string
	WorkflowID       string
	TaskShortID      string
	TaskTitle        string
	SessionID        string
	Message          string
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
	resolvedApprovals       map[workflow.ApprovalID]struct{}
	resolvedInterruptions   map[interruptedCurrentNodeOccurrenceKey]struct{}
}

type interruptedCurrentNodeOccurrenceKey struct {
	currentNode      workflow.CurrentNodeReferenceKey
	occurredAtUnixMs int64
}

func NewFinalizer(provider PendingApprovalProjectionProvider, publisher Publisher) *Finalizer {
	interrupted, _ := provider.(PendingInterruptedCurrentNodeProjectionProvider)
	return &Finalizer{
		approvals:               provider,
		interruptedCurrentNodes: interrupted,
		publisher:               publisher,
		resolvedApprovals:       map[workflow.ApprovalID]struct{}{},
		resolvedInterruptions:   map[interruptedCurrentNodeOccurrenceKey]struct{}{},
	}
}

func (f *Finalizer) PublishPendingApproval(ctx context.Context, approvalID workflow.ApprovalID) {
	if f == nil || f.approvals == nil || f.publisher == nil {
		return
	}
	projection, ok, err := f.approvals.PendingApprovalProjection(ctx, approvalID)
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
	projection, ok, err := f.interruptedCurrentNodes.PendingInterruptedCurrentNodeProjection(ctx, reference)
	if err != nil {
		slog.Warn("workflow interrupted-current-node attention projection failed", "task_id", reference.TaskID, "node_id", reference.NodeID, "error", err)
		return
	}
	if !ok {
		return
	}
	f.publishPendingInterruptedCurrentNode(projection)
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
	if err := projection.ApprovalID.Validate(); err != nil {
		slog.Warn("workflow approval attention projection has invalid identity", "error", err)
		return
	}
	f.mu.Lock()
	_, resolved := f.resolvedApprovals[projection.ApprovalID]
	f.mu.Unlock()
	if resolved {
		return
	}
	if err := f.publisher.PublishPending(approvalRoutingScope(projection), approvalNotification(projection)); err != nil {
		slog.Warn("workflow approval attention publish failed", "approval_id", projection.ApprovalID.String(), "task_id", projection.Source.TaskID, "error", err)
	}
}

func (f *Finalizer) publishPendingInterruptedCurrentNode(projection InterruptedCurrentNodeProjection) {
	occurrence, err := interruptedCurrentNodeOccurrence(projection)
	if err != nil {
		slog.Warn("workflow interrupted-current-node attention projection is invalid", "error", err)
		return
	}
	f.mu.Lock()
	_, resolved := f.resolvedInterruptions[occurrence]
	f.mu.Unlock()
	if resolved {
		return
	}
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
	f.resolvedApprovals[projection.ApprovalID] = struct{}{}
	f.mu.Unlock()
	if err := f.publisher.PublishResolved(approvalRoutingScope(projection), approvalNotificationID(projection.ApprovalID), clientui.AttentionNotificationKindApproval, time.Now().UTC()); err != nil {
		slog.Warn("workflow approval attention resolved publish failed", "approval_id", projection.ApprovalID.String(), "error", err)
	}
}

func (f *Finalizer) resolveInterruptedCurrentNode(projection InterruptedCurrentNodeProjection) {
	if f == nil || f.publisher == nil {
		return
	}
	occurrence, err := interruptedCurrentNodeOccurrence(projection)
	if err != nil {
		slog.Warn("workflow interrupted-current-node resolution is invalid", "error", err)
		return
	}
	f.mu.Lock()
	if _, resolved := f.resolvedInterruptions[occurrence]; resolved {
		f.mu.Unlock()
		return
	}
	f.resolvedInterruptions[occurrence] = struct{}{}
	f.mu.Unlock()
	if err := f.publisher.PublishResolved(interruptedCurrentNodeRoutingScope(projection), interruptedCurrentNodeNotificationID(projection.CurrentNode), clientui.AttentionNotificationKindInterruptedCurrentNode, time.Now().UTC()); err != nil {
		slog.Warn("workflow interrupted-current-node attention resolved publish failed", "task_id", projection.CurrentNode.TaskID, "node_id", projection.CurrentNode.NodeID, "error", err)
	}
}

func interruptedCurrentNodeOccurrence(projection InterruptedCurrentNodeProjection) (interruptedCurrentNodeOccurrenceKey, error) {
	if err := projection.CurrentNode.Validate(); err != nil {
		return interruptedCurrentNodeOccurrenceKey{}, err
	}
	if projection.OccurredAtUnixMs <= 0 {
		return interruptedCurrentNodeOccurrenceKey{}, fmt.Errorf("interrupted current node occurrence time is required")
	}
	currentNodeKey, err := projection.CurrentNode.Key()
	if err != nil {
		return interruptedCurrentNodeOccurrenceKey{}, err
	}
	return interruptedCurrentNodeOccurrenceKey{
		currentNode:      currentNodeKey,
		occurredAtUnixMs: projection.OccurredAtUnixMs,
	}, nil
}

func approvalNotification(projection ApprovalProjection) clientui.AttentionNotification {
	return clientui.AttentionNotification{
		ID:         approvalNotificationID(projection.ApprovalID),
		Kind:       clientui.AttentionNotificationKindApproval,
		OccurredAt: time.UnixMilli(projection.OccurredAtUnixMs).UTC(),
		Revision:   1,
		Approval: &clientui.AttentionNotificationApprovalState{
			ApprovalID: projection.ApprovalID.String(),
			Message:    strings.TrimSpace(projection.Message),
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
			Message:    strings.TrimSpace(projection.Message),
			Reason:     strings.TrimSpace(projection.Reason),
			DetailJSON: strings.TrimSpace(projection.DetailJSON),
		},
		Target: workflowTaskTarget(projection.ProjectID, projection.WorkflowID, projection.CurrentNode, projection.TaskShortID, projection.TaskTitle, projection.SessionID, clientui.AttentionNotificationFocusInterruptedCurrentNode, ""),
	}
}

func workflowTaskTarget(projectID, workflowID string, reference workflow.CurrentNodeReference, shortID, title, sessionID string, focusKind clientui.AttentionNotificationFocusKind, approvalID string) clientui.AttentionNotificationTarget {
	nodeID := string(reference.NodeID)
	target := clientui.AttentionNotificationTarget{
		Kind:          clientui.AttentionNotificationTargetWorkflowTask,
		ProjectID:     strings.TrimSpace(projectID),
		WorkflowID:    strings.TrimSpace(workflowID),
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
		WorkflowID: strings.TrimSpace(projection.WorkflowID),
		TaskID:     string(projection.Source.TaskID),
		SessionID:  strings.TrimSpace(projection.SessionID),
	}
}

func interruptedCurrentNodeRoutingScope(projection InterruptedCurrentNodeProjection) attentionnotify.RoutingScope {
	return attentionnotify.RoutingScope{
		Kind:       attentionnotify.RoutingWorkflowTask,
		ProjectID:  strings.TrimSpace(projection.ProjectID),
		WorkflowID: strings.TrimSpace(projection.WorkflowID),
		TaskID:     string(projection.CurrentNode.TaskID),
		SessionID:  strings.TrimSpace(projection.SessionID),
	}
}

func approvalNotificationID(approvalID workflow.ApprovalID) clientui.AttentionNotificationID {
	return clientui.AttentionNotificationID{Kind: clientui.AttentionNotificationKindApproval, UUID: approvalID.String()}
}

func interruptedCurrentNodeNotificationID(reference workflow.CurrentNodeReference) clientui.AttentionNotificationID {
	branchKey, branchScoped := reference.TransitionBranchKey()
	id := string(reference.TaskID) + ":" + string(reference.NodeID)
	if branchScoped {
		id += ":" + string(branchKey)
	}
	return clientui.AttentionNotificationID{Kind: clientui.AttentionNotificationKindInterruptedCurrentNode, UUID: id}
}
