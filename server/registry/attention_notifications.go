package registry

import (
	"context"
	"fmt"
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
			if item.Request.QuestionBatch != nil && item.Request.AttentionTarget != nil && item.Request.AttentionTarget.Kind == clientui.AttentionNotificationTargetTaskDetail {
				batchID := questionBatchNotificationID(*item.Request.QuestionBatch)
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
		if err := r.attentionBroker.EnqueueInitial(sub, attentionnotify.DeliveryScope{Kind: attentionnotify.DeliverySessionPrompt, SessionID: req.SessionID}, complete); err != nil {
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
		if req.QuestionBatch == nil || req.AttentionTarget == nil || req.AttentionTarget.Kind != clientui.AttentionNotificationTargetTaskDetail {
			continue
		}
		batchID := questionBatchNotificationID(*req.QuestionBatch)
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
	display := clientui.AttentionNotificationPresentation{Title: "Question", Body: "question from agent"}
	if req.AttentionPresentation != nil {
		display = *req.AttentionPresentation
	}
	materializedAskIDs := make([]string, 0, len(items))
	for _, item := range items {
		materializedAskIDs = append(materializedAskIDs, item.Request.ID)
	}
	return r.questionBatches.EnqueueSnapshot(subscription, attentionnotify.QuestionBatch{
		ID:             questionBatchNotificationID(*req.QuestionBatch),
		Delivery:       attentionScopeForRequest(sessionID, req),
		Target:         *req.AttentionTarget,
		Presentation:   display,
		PreparedAskIDs: append([]string(nil), req.QuestionBatch.BatchPromptIDs...),
		OccurredAt:     items[0].CreatedAt,
	}, materializedAskIDs)
}

func (r *RuntimeRegistry) publishAttentionPending(sessionID string, snapshot PendingPromptSnapshot) {
	if r == nil || r.attentionBroker == nil {
		return
	}
	req := snapshot.Request
	if req.QuestionBatch != nil && req.AttentionTarget != nil && req.AttentionTarget.Kind == clientui.AttentionNotificationTargetTaskDetail {
		batchID := questionBatchNotificationID(*req.QuestionBatch)
		_ = r.PrepareTaskQuestionBatch(*req.QuestionBatch, sessionID, req.AttentionTarget, req.AttentionPresentation, snapshot.CreatedAt)
		_ = r.questionBatches.MarkMaterialized(batchID, req.ID)
		return
	}
	event := attentionPendingEventFromPrompt(sessionID, snapshot, clientui.AttentionNotificationSourceLive)
	if event.Pending != nil {
		_ = r.attentionBroker.PublishPending(attentionScopeForRequest(sessionID, req), *event.Pending)
	}
}

func (r *RuntimeRegistry) publishAttentionResolved(sessionID string, snapshot PendingPromptSnapshot) {
	if r == nil || r.attentionBroker == nil {
		return
	}
	req := snapshot.Request
	if req.QuestionBatch != nil && req.AttentionTarget != nil && req.AttentionTarget.Kind == clientui.AttentionNotificationTargetTaskDetail {
		return
	}
	_ = r.attentionBroker.PublishResolved(attentionScopeForRequest(sessionID, req), promptNotificationID(sessionID, req.ID), promptNotificationKind(req), time.Now().UTC())
}

func (r *RuntimeRegistry) PrepareTaskQuestionBatch(batch askquestion.AskQuestionBatchMetadata, sessionID string, target *clientui.AttentionNotificationTarget, presentation *clientui.AttentionNotificationPresentation, occurredAt time.Time) error {
	if r == nil || r.attentionBroker == nil {
		return nil
	}
	if target == nil || target.Kind != clientui.AttentionNotificationTargetTaskDetail {
		return fmt.Errorf("task question batch %q cannot be prepared without task-detail target", batch.BatchID)
	}
	if r.questionBatches == nil {
		r.questionBatches = attentionnotify.NewQuestionBatchTracker(r.attentionBroker)
	}
	display := clientui.AttentionNotificationPresentation{Title: "Question", Body: "question from agent"}
	if presentation != nil {
		display = *presentation
	}
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	return r.questionBatches.Prepare(attentionnotify.QuestionBatch{
		ID:             questionBatchNotificationID(batch),
		Delivery:       attentionnotify.DeliveryScope{Kind: attentionnotify.DeliveryTaskDetail, TaskID: target.TaskID, SessionID: sessionID},
		Target:         *target,
		Presentation:   display,
		PreparedAskIDs: append([]string(nil), batch.BatchPromptIDs...),
		OccurredAt:     occurredAt,
	})
}

func (r *RuntimeRegistry) MarkTaskQuestionCleared(batch askquestion.AskQuestionBatchMetadata, askID string) {
	if r == nil || r.questionBatches == nil {
		return
	}
	_ = r.questionBatches.MarkDurablyCleared(questionBatchNotificationID(batch), askID)
}

func (r *RuntimeRegistry) MarkTaskQuestionSkipped(batch askquestion.AskQuestionBatchMetadata) {
	if r == nil || r.questionBatches == nil {
		return
	}
	_ = r.questionBatches.MarkSkipped(questionBatchNotificationID(batch), batch.PromptID)
}

func attentionPendingEventFromPrompt(sessionID string, snapshot PendingPromptSnapshot, source clientui.AttentionNotificationSource) clientui.AttentionNotificationEvent {
	notification := clientui.AttentionNotification{
		ID:         promptNotificationID(sessionID, snapshot.Request.ID),
		Kind:       promptNotificationKind(snapshot.Request),
		OccurredAt: snapshot.CreatedAt,
		Revision:   1,
		Target: clientui.AttentionNotificationTarget{
			Kind:      clientui.AttentionNotificationTargetSessionPrompt,
			SessionID: sessionID,
		},
		Presentation: clientui.AttentionNotificationPresentation{
			Title: promptTitle(snapshot.Request),
			Body:  promptBody(snapshot.Request),
			Count: 1,
		},
	}
	return clientui.AttentionNotificationEvent{
		Source:  source,
		Type:    clientui.AttentionNotificationEventPending,
		Pending: &notification,
	}
}

func attentionScopeForRequest(sessionID string, req askquestion.AskQuestionRequest) attentionnotify.DeliveryScope {
	if req.AttentionTarget != nil && req.AttentionTarget.Kind == clientui.AttentionNotificationTargetTaskDetail {
		return attentionnotify.DeliveryScope{Kind: attentionnotify.DeliveryTaskDetail, TaskID: req.AttentionTarget.TaskID, SessionID: sessionID}
	}
	return attentionnotify.DeliveryScope{Kind: attentionnotify.DeliverySessionPrompt, SessionID: sessionID}
}

func questionBatchNotificationID(batch askquestion.AskQuestionBatchMetadata) string {
	return "question_batch:" + batch.RunID + ":" + batch.BatchID
}

func promptNotificationID(sessionID string, promptID string) string {
	return "prompt:" + sessionID + ":" + promptID
}

func promptNotificationKind(req askquestion.AskQuestionRequest) clientui.AttentionNotificationKind {
	if req.Approval {
		return clientui.AttentionNotificationKindApproval
	}
	return clientui.AttentionNotificationKindQuestion
}

func promptTitle(req askquestion.AskQuestionRequest) string {
	if req.Approval {
		return "Action required"
	}
	return "Question"
}

func promptBody(req askquestion.AskQuestionRequest) string {
	if req.Question != "" {
		return req.Question
	}
	if req.Approval {
		return "action required"
	}
	return "question from agent"
}
