package registry

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"core/server/attentionnotify"
	askquestion "core/server/tools"
	"core/shared/clientui"
	"core/shared/serverapi"
)

func (r *RuntimeRegistry) WithAttentionNotifications(broker *attentionnotify.Broker) *RuntimeRegistry {
	if r == nil {
		return r
	}
	r.attentionBroker = broker
	if broker != nil {
		r.questionBatches = attentionnotify.NewQuestionBatchTracker(broker)
	}
	return r
}

func (r *RuntimeRegistry) SubscribeAttentionNotifications(_ context.Context, req serverapi.AttentionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if r == nil || r.attentionBroker == nil {
		return nil, fmt.Errorf("attention notification stream is unavailable: %w", serverapi.ErrStreamUnavailable)
	}
	return r.attentionBroker.SubscribeDesktop()
}

func (r *RuntimeRegistry) SubscribeSessionAttentionNotifications(_ context.Context, req serverapi.AttentionSessionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if r == nil || r.attentionBroker == nil {
		return nil, fmt.Errorf("attention notification stream is unavailable: %w", serverapi.ErrStreamUnavailable)
	}
	entry := r.directory.Entry(req.SessionID)
	if entry == nil {
		return nil, fmt.Errorf("runtime %q is unavailable", req.SessionID)
	}
	if !req.IncludePendingPromptSnapshot {
		return r.attentionBroker.SubscribeSession(req.SessionID)
	}
	return entry.pendingPrompts.WithLockedAttentionSnapshotResult(func(items []PendingPromptSnapshot) (serverapi.AttentionNotificationSubscription, error) {
		sub, err := r.attentionBroker.SubscribeSession(req.SessionID)
		if err != nil {
			return nil, err
		}
		taskBatches := taskQuestionBatchSnapshotGroups(items)
		processedTaskBatches := map[string]struct{}{}
		for _, item := range items {
			if item.Request.QuestionBatch != nil && item.Request.AttentionTarget != nil && item.Request.AttentionTarget.Kind == clientui.AttentionNotificationTargetWorkflowTask {
				batchID := questionBatchUUID(*item.Request.QuestionBatch)
				if _, ok := processedTaskBatches[batchID]; ok {
					continue
				}
				processedTaskBatches[batchID] = struct{}{}
				if err := r.enqueueTaskQuestionBatchSnapshot(sub, req.SessionID, taskBatches[batchID]); err != nil {
					_ = sub.Close()
					return nil, err
				}
				continue
			}
			event := attentionPendingEventFromPrompt(req.SessionID, item, clientui.AttentionNotificationSourceSnapshot)
			if event.Pending == nil {
				continue
			}
			scope := attentionScopeForRequest(req.SessionID, item.Request)
			if err := r.attentionBroker.EnqueueInitial(sub, scope, event); err != nil {
				_ = sub.Close()
				return nil, err
			}
		}
		complete := clientui.AttentionNotificationEvent{
			Source:    clientui.AttentionNotificationSourceSnapshot,
			Type:      clientui.AttentionNotificationEventSnapshotComplete,
			SessionID: req.SessionID,
		}
		if err := r.attentionBroker.EnqueueInitial(sub, attentionnotify.RoutingScope{Kind: attentionnotify.RoutingSessionPrompt, SessionID: req.SessionID}, complete); err != nil {
			_ = sub.Close()
			return nil, err
		}
		return sub, nil
	})
}

func taskQuestionBatchSnapshotGroups(items []PendingPromptSnapshot) map[string][]PendingPromptSnapshot {
	groups := map[string][]PendingPromptSnapshot{}
	for _, item := range items {
		req := item.Request
		if req.QuestionBatch == nil || req.AttentionTarget == nil || req.AttentionTarget.Kind != clientui.AttentionNotificationTargetWorkflowTask {
			continue
		}
		batchID := questionBatchUUID(*req.QuestionBatch)
		groups[batchID] = append(groups[batchID], item)
	}
	return groups
}

func (r *RuntimeRegistry) enqueueTaskQuestionBatchSnapshot(sub serverapi.AttentionNotificationSubscription, sessionID string, items []PendingPromptSnapshot) error {
	if len(items) == 0 {
		return nil
	}
	if r.questionBatches == nil {
		r.questionBatches = attentionnotify.NewQuestionBatchTracker(r.attentionBroker)
	}
	subscription, ok := sub.(*attentionnotify.Subscription)
	if !ok {
		return fmt.Errorf("attention notification snapshot subscription has unexpected type %T", sub)
	}
	req := items[0].Request
	materializedAskIDs := make([]string, 0, len(items))
	for _, item := range items {
		materializedAskIDs = append(materializedAskIDs, item.Request.ID)
	}
	return r.questionBatches.EnqueueSnapshot(subscription, attentionnotify.QuestionBatch{
		ID:             questionBatchUUID(*req.QuestionBatch),
		Route:          attentionScopeForRequest(sessionID, req),
		Target:         *req.AttentionTarget,
		Preview:        strings.TrimSpace(req.Question),
		PreparedAskIDs: append([]string(nil), req.QuestionBatch.BatchPromptIDs...),
		OccurredAt:     items[0].CreatedAt,
	}, materializedAskIDs)
}

func (r *RuntimeRegistry) publishAttentionPending(sessionID string, snapshot PendingPromptSnapshot) {
	if r == nil || r.attentionBroker == nil {
		return
	}
	req := snapshot.Request
	if req.QuestionBatch != nil && req.AttentionTarget != nil && req.AttentionTarget.Kind == clientui.AttentionNotificationTargetWorkflowTask {
		batchID := questionBatchUUID(*req.QuestionBatch)
		if err := r.PrepareTaskQuestionBatch(*req.QuestionBatch, sessionID, req.AttentionTarget, strings.TrimSpace(req.Question), snapshot.CreatedAt); err != nil {
			logAttentionNotificationOperationFailure("prepare task question batch", sessionID, req.ID, err)
			return
		}
		if err := r.questionBatches.MarkMaterialized(batchID, req.ID); err != nil {
			logAttentionNotificationOperationFailure("materialize task question batch", sessionID, req.ID, err)
		}
		return
	}
	event := attentionPendingEventFromPrompt(sessionID, snapshot, clientui.AttentionNotificationSourceLive)
	if event.Pending != nil {
		if err := r.attentionBroker.PublishPending(attentionScopeForRequest(sessionID, req), *event.Pending); err != nil {
			logAttentionNotificationOperationFailure("publish pending prompt", sessionID, req.ID, err)
		}
	}
}

func (r *RuntimeRegistry) publishAttentionResolved(sessionID string, snapshot PendingPromptSnapshot) {
	if r == nil || r.attentionBroker == nil {
		return
	}
	req := snapshot.Request
	if req.QuestionBatch != nil && req.AttentionTarget != nil && req.AttentionTarget.Kind == clientui.AttentionNotificationTargetWorkflowTask {
		return
	}
	kind := promptNotificationKind(req)
	id := clientui.AttentionNotificationID{
		Kind: kind,
		UUID: strings.TrimSpace(req.ID),
	}
	if err := r.attentionBroker.PublishResolved(attentionScopeForRequest(sessionID, req), id, kind, time.Now().UTC()); err != nil {
		logAttentionNotificationOperationFailure("publish resolved prompt", sessionID, req.ID, err)
	}
}

func (r *RuntimeRegistry) PrepareTaskQuestionBatch(batch askquestion.AskQuestionBatchMetadata, sessionID string, target *clientui.AttentionNotificationTarget, preview string, occurredAt time.Time) error {
	if r == nil || r.attentionBroker == nil {
		return nil
	}
	if target == nil || target.Kind != clientui.AttentionNotificationTargetWorkflowTask {
		return fmt.Errorf("task question batch %q cannot be prepared without task-detail target", batch.BatchID)
	}
	if r.questionBatches == nil {
		r.questionBatches = attentionnotify.NewQuestionBatchTracker(r.attentionBroker)
	}
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	return r.questionBatches.Prepare(attentionnotify.QuestionBatch{
		ID:             questionBatchUUID(batch),
		Route:          attentionnotify.RoutingScope{Kind: attentionnotify.RoutingWorkflowTask, TaskID: target.TaskID, SessionID: sessionID},
		Target:         *target,
		Preview:        strings.TrimSpace(preview),
		PreparedAskIDs: append([]string(nil), batch.BatchPromptIDs...),
		OccurredAt:     occurredAt,
	})
}

func (r *RuntimeRegistry) MarkTaskQuestionCleared(batch askquestion.AskQuestionBatchMetadata, askID string) {
	if r == nil || r.questionBatches == nil {
		return
	}
	if err := r.questionBatches.MarkDurablyCleared(questionBatchUUID(batch), askID); err != nil {
		logAttentionNotificationOperationFailure("mark task question cleared", "", askID, err)
	}
}

func (r *RuntimeRegistry) MarkTaskQuestionSkipped(batch askquestion.AskQuestionBatchMetadata) {
	if r == nil || r.questionBatches == nil {
		return
	}
	if err := r.questionBatches.MarkSkipped(questionBatchUUID(batch), batch.PromptID); err != nil {
		logAttentionNotificationOperationFailure("mark task question skipped", "", batch.PromptID, err)
	}
}

func logAttentionNotificationOperationFailure(operation string, sessionID string, askID string, err error) {
	if err == nil {
		return
	}
	slog.Warn("attention notification operation failed", "operation", operation, "session_id", sessionID, "ask_id", askID, "error", err)
}

func attentionPendingEventFromPrompt(sessionID string, snapshot PendingPromptSnapshot, source clientui.AttentionNotificationSource) clientui.AttentionNotificationEvent {
	kind := promptNotificationKind(snapshot.Request)
	notification := clientui.AttentionNotification{
		ID: clientui.AttentionNotificationID{
			Kind: kind,
			UUID: strings.TrimSpace(snapshot.Request.ID),
		},
		Kind:       kind,
		OccurredAt: snapshot.CreatedAt,
		Revision:   1,
		Target: clientui.AttentionNotificationTarget{
			Kind:      clientui.AttentionNotificationTargetSessionPrompt,
			SessionID: sessionID,
		},
	}
	if snapshot.Request.Approval {
		notification.Approval = &clientui.AttentionNotificationApprovalState{
			Message: strings.TrimSpace(snapshot.Request.Question),
		}
	} else {
		notification.Question = &clientui.AttentionNotificationQuestionState{
			PreparedAskIDs:          []string{snapshot.Request.ID},
			MaterializedAskIDs:      []string{snapshot.Request.ID},
			CurrentUnresolvedAskIDs: []string{snapshot.Request.ID},
			Preview:                 strings.TrimSpace(snapshot.Request.Question),
			DisplayCount:            1,
			MaterializedCount:       1,
		}
	}
	return clientui.AttentionNotificationEvent{
		Source:  source,
		Type:    clientui.AttentionNotificationEventPending,
		Pending: &notification,
	}
}

func attentionScopeForRequest(sessionID string, req askquestion.AskQuestionRequest) attentionnotify.RoutingScope {
	if req.AttentionTarget != nil && req.AttentionTarget.Kind == clientui.AttentionNotificationTargetWorkflowTask {
		return attentionnotify.RoutingScope{Kind: attentionnotify.RoutingWorkflowTask, TaskID: req.AttentionTarget.TaskID, SessionID: sessionID}
	}
	return attentionnotify.RoutingScope{Kind: attentionnotify.RoutingSessionPrompt, SessionID: sessionID}
}

func promptNotificationKind(req askquestion.AskQuestionRequest) clientui.AttentionNotificationKind {
	if req.Approval {
		return clientui.AttentionNotificationKindApproval
	}
	return clientui.AttentionNotificationKindQuestion
}

func questionBatchUUID(batch askquestion.AskQuestionBatchMetadata) string {
	return strings.TrimSpace(batch.BatchID)
}
