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
	if !req.IncludePendingPromptSnapshot {
		return r.attentionBroker.SubscribeSession(req.SessionID)
	}
	return r.pendingPrompts.WithLockedAttentionSnapshotResult(req.SessionID, func(snapshot pendingAttentionSnapshot) (serverapi.AttentionNotificationSubscription, error) {
		descriptors, err := r.attentionSnapshotDescriptors(req.SessionID, snapshot.items)
		if err != nil {
			return nil, err
		}
		return r.attentionBroker.SubscribeSessionSnapshot(req.SessionID, descriptors, snapshot.ordinaryOccurrenceWatermark)
	})
}

func (r *RuntimeRegistry) attentionSnapshotDescriptors(sessionID string, items []PendingPromptSnapshot) ([]attentionnotify.SnapshotPendingDescriptor, error) {
	taskBatches := taskQuestionBatchSnapshotGroups(items)
	descriptors := make([]attentionnotify.SnapshotPendingDescriptor, 0, len(items))
	processedTaskBatches := map[string]struct{}{}
	for _, item := range items {
		if item.Request.QuestionBatch != nil && item.Request.AttentionTarget != nil && item.Request.AttentionTarget.Kind == clientui.AttentionNotificationTargetWorkflowTask {
			batchID := questionBatchUUID(*item.Request.QuestionBatch)
			if _, ok := processedTaskBatches[batchID]; ok {
				continue
			}
			processedTaskBatches[batchID] = struct{}{}
			descriptor, err := r.taskQuestionBatchSnapshotDescriptor(sessionID, taskBatches[batchID])
			if err != nil {
				return nil, err
			}
			if descriptor.Notification.ID.UUID != "" {
				descriptors = append(descriptors, descriptor)
			}
			continue
		}
		event := attentionPendingEventFromPrompt(sessionID, item, clientui.AttentionNotificationSourceSnapshot)
		if event.Pending == nil {
			continue
		}
		descriptors = append(descriptors, attentionnotify.SnapshotPendingDescriptor{Notification: *event.Pending, Occurrence: item.occurrence})
	}
	return descriptors, nil
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

func (r *RuntimeRegistry) taskQuestionBatchSnapshotDescriptor(sessionID string, items []PendingPromptSnapshot) (attentionnotify.SnapshotPendingDescriptor, error) {
	if len(items) == 0 {
		return attentionnotify.SnapshotPendingDescriptor{}, nil
	}
	if r.questionBatches == nil {
		r.questionBatches = attentionnotify.NewQuestionBatchTracker(r.attentionBroker)
	}
	req := items[0].Request
	materializedAskIDs := make([]string, 0, len(items))
	for _, item := range items {
		materializedAskIDs = append(materializedAskIDs, item.Request.ID)
	}
	return r.questionBatches.Snapshot(attentionnotify.QuestionBatch{
		ID:             questionBatchUUID(*req.QuestionBatch),
		Route:          attentionScopeForRequest(sessionID, req),
		Target:         *req.AttentionTarget,
		Preview:        strings.TrimSpace(req.Question),
		PreparedAskIDs: append([]string(nil), req.QuestionBatch.BatchPromptIDs...),
		OccurredAt:     items[0].CreatedAt,
		Occurrence:     items[0].occurrence,
	}, materializedAskIDs)
}

func (r *RuntimeRegistry) publishAttentionPending(sessionID string, snapshot PendingPromptSnapshot) {
	if r == nil || r.attentionBroker == nil {
		return
	}
	req := snapshot.Request
	if req.QuestionBatch != nil && req.AttentionTarget != nil && req.AttentionTarget.Kind == clientui.AttentionNotificationTargetWorkflowTask {
		batchID := questionBatchUUID(*req.QuestionBatch)
		if err := r.prepareTaskQuestionBatch(*req.QuestionBatch, sessionID, req.AttentionTarget, strings.TrimSpace(req.Question), snapshot.CreatedAt, snapshot.occurrence); err != nil {
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
		if err := r.attentionBroker.PublishPendingWithOccurrence(attentionScopeForRequest(sessionID, req), *event.Pending, snapshot.occurrence); err != nil {
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
	if req.IsTaskScopedApprovalQuestion() {
		r.rememberTaskApprovalOccurrence(sessionID, req.ID, snapshot.occurrence)
		return
	}
	kind := promptNotificationKind(req)
	id := clientui.AttentionNotificationID{
		Kind: kind,
		UUID: strings.TrimSpace(req.ID),
	}
	if err := r.attentionBroker.PublishResolvedWithOccurrence(attentionScopeForRequest(sessionID, req), id, kind, time.Now().UTC(), snapshot.occurrence); err != nil {
		logAttentionNotificationOperationFailure("publish resolved prompt", sessionID, req.ID, err)
	}
}

func (r *RuntimeRegistry) MarkTaskApprovalQuestionCleared(target clientui.AttentionNotificationTarget, askID string) {
	if r == nil || r.attentionBroker == nil {
		return
	}
	occurrence, found := r.takeTaskApprovalOccurrence(target.SessionID, askID)
	if !found {
		if r.authorityEntryBySession(target.SessionID) == nil {
			return
		}
		panic(fmt.Sprintf("task approval attention occurrence is missing: session_id=%q ask_id=%q", target.SessionID, askID))
	}
	id := clientui.AttentionNotificationID{
		Kind: clientui.AttentionNotificationKindQuestion,
		UUID: strings.TrimSpace(askID),
	}
	if err := r.attentionBroker.PublishResolvedWithOccurrence(attentionnotify.RoutingScope{Kind: attentionnotify.RoutingWorkflowTask, TaskID: target.TaskID, SessionID: target.SessionID}, id, clientui.AttentionNotificationKindQuestion, time.Now().UTC(), occurrence); err != nil {
		logAttentionNotificationOperationFailure("publish task approval question resolved", target.SessionID, askID, err)
	}
}

func (r *RuntimeRegistry) rememberTaskApprovalOccurrence(sessionID string, askID string, occurrence attentionnotify.OccurrenceMetadata) {
	if r == nil {
		return
	}
	key := taskApprovalOccurrenceKey{sessionID: strings.TrimSpace(sessionID), askID: strings.TrimSpace(askID)}
	if key.sessionID == "" || key.askID == "" {
		panic(fmt.Sprintf("task approval attention occurrence key is invalid: session_id=%q ask_id=%q", sessionID, askID))
	}
	if _, ok := occurrence.OrdinaryOrdinal(); !ok {
		panic(fmt.Sprintf("task approval attention occurrence must be ordinary: session_id=%q ask_id=%q", key.sessionID, key.askID))
	}
	r.taskApprovalOccurrenceMu.Lock()
	defer r.taskApprovalOccurrenceMu.Unlock()
	if r.taskApprovalOccurrences == nil {
		r.taskApprovalOccurrences = make(map[taskApprovalOccurrenceKey]attentionnotify.OccurrenceMetadata)
	}
	if _, exists := r.taskApprovalOccurrences[key]; exists {
		panic(fmt.Sprintf("task approval attention occurrence already exists: session_id=%q ask_id=%q", key.sessionID, key.askID))
	}
	r.taskApprovalOccurrences[key] = occurrence
}

func (r *RuntimeRegistry) takeTaskApprovalOccurrence(sessionID string, askID string) (attentionnotify.OccurrenceMetadata, bool) {
	if r == nil {
		return attentionnotify.OccurrenceMetadata{}, false
	}
	key := taskApprovalOccurrenceKey{sessionID: strings.TrimSpace(sessionID), askID: strings.TrimSpace(askID)}
	r.taskApprovalOccurrenceMu.Lock()
	defer r.taskApprovalOccurrenceMu.Unlock()
	occurrence, ok := r.taskApprovalOccurrences[key]
	if ok {
		delete(r.taskApprovalOccurrences, key)
	}
	return occurrence, ok
}

func (r *RuntimeRegistry) clearTaskApprovalOccurrences(sessionID string) {
	if r == nil {
		return
	}
	id := strings.TrimSpace(sessionID)
	r.taskApprovalOccurrenceMu.Lock()
	defer r.taskApprovalOccurrenceMu.Unlock()
	for key := range r.taskApprovalOccurrences {
		if key.sessionID == id {
			delete(r.taskApprovalOccurrences, key)
		}
	}
}

func (r *RuntimeRegistry) PrepareTaskQuestionBatch(batch askquestion.AskQuestionBatchMetadata, sessionID string, target *clientui.AttentionNotificationTarget, preview string, occurredAt time.Time) error {
	return r.prepareTaskQuestionBatch(
		batch,
		sessionID,
		target,
		preview,
		occurredAt,
		attentionnotify.OccurrenceMetadata{},
	)
}

func (r *RuntimeRegistry) prepareTaskQuestionBatch(batch askquestion.AskQuestionBatchMetadata, sessionID string, target *clientui.AttentionNotificationTarget, preview string, occurredAt time.Time, occurrence attentionnotify.OccurrenceMetadata) error {
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
		Occurrence:     occurrence,
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
	target := clientui.AttentionNotificationTarget{
		Kind:      clientui.AttentionNotificationTargetSessionPrompt,
		SessionID: sessionID,
	}
	if snapshot.Request.IsTaskScopedApprovalQuestion() && snapshot.Request.AttentionTarget != nil {
		target = *snapshot.Request.AttentionTarget
	}
	notification := clientui.AttentionNotification{
		ID: clientui.AttentionNotificationID{
			Kind: kind,
			UUID: strings.TrimSpace(snapshot.Request.ID),
		},
		Kind:       kind,
		OccurredAt: snapshot.CreatedAt,
		Revision:   1,
		Target:     target,
	}
	if snapshot.Request.Approval && !snapshot.Request.IsTaskScopedApprovalQuestion() {
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
	if req.Approval && !req.IsTaskScopedApprovalQuestion() {
		return clientui.AttentionNotificationKindApproval
	}
	return clientui.AttentionNotificationKindQuestion
}

func questionBatchUUID(batch askquestion.AskQuestionBatchMetadata) string {
	return strings.TrimSpace(batch.BatchID)
}
