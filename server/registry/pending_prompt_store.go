package registry

import (
	"sort"
	"strings"
	"sync"
	"time"

	"core/server/attentionnotify"
	askquestion "core/server/tools"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type PendingPromptSnapshot struct {
	Request    askquestion.AskQuestionRequest
	CreatedAt  time.Time
	Resource   runtimeids.SessionResourceRef
	ScopeID    runtimeids.ExecutionScopeID
	occurrence attentionnotify.OccurrenceMetadata
}

type pendingPromptStore struct {
	mu                     sync.RWMutex
	pending                map[string]map[string]PendingPromptSnapshot
	nextOrdinaryOccurrence map[string]attentionnotify.OrdinaryOccurrenceOrdinal
}

func newPendingPromptStore() *pendingPromptStore {
	return &pendingPromptStore{
		pending:                make(map[string]map[string]PendingPromptSnapshot),
		nextOrdinaryOccurrence: make(map[string]attentionnotify.OrdinaryOccurrenceOrdinal),
	}
}

func (s *pendingPromptStore) Begin(sessionID string, resource runtimeids.SessionResourceRef, scopeID runtimeids.ExecutionScopeID, req askquestion.AskQuestionRequest, createdAt time.Time, publish func(PendingPromptSnapshot)) bool {
	id, requestID := strings.TrimSpace(sessionID), strings.TrimSpace(req.ID)
	if id == "" || requestID == "" {
		return false
	}
	s.mu.Lock()
	snapshot := s.newSnapshotLocked(id, resource, scopeID, req, createdAt)
	pending := s.pending[id]
	if pending == nil {
		pending = make(map[string]PendingPromptSnapshot)
		s.pending[id] = pending
	}
	pending[requestID] = snapshot
	s.mu.Unlock()
	if publish != nil {
		publish(snapshot)
	}
	return true
}

func (s *pendingPromptStore) Complete(sessionID string, resource runtimeids.SessionResourceRef, scopeID runtimeids.ExecutionScopeID, requestID string) (PendingPromptSnapshot, bool) {
	id, askID := strings.TrimSpace(sessionID), strings.TrimSpace(requestID)
	if id == "" || askID == "" {
		return PendingPromptSnapshot{}, false
	}
	s.mu.Lock()
	pending := s.pending[id]
	entry, exists := pending[askID]
	if exists && resource.Validate() == nil && !scopeID.IsZero() &&
		(entry.Resource != resource || entry.ScopeID != scopeID) {
		exists = false
	}
	if exists {
		delete(pending, askID)
	}
	if len(pending) == 0 {
		delete(s.pending, id)
	}
	s.mu.Unlock()
	if !exists {
		return PendingPromptSnapshot{}, false
	}
	return entry, true
}

func (s *pendingPromptStore) List(sessionID string) []PendingPromptSnapshot {
	s.mu.RLock()
	items := listPendingPrompts(s.pending[strings.TrimSpace(sessionID)])
	s.mu.RUnlock()
	return items
}

func (s *pendingPromptStore) CloseSession(sessionID string, resolve func(PendingPromptSnapshot)) {
	id := strings.TrimSpace(sessionID)
	s.mu.Lock()
	items := listPendingPrompts(s.pending[id])
	delete(s.pending, id)
	delete(s.nextOrdinaryOccurrence, id)
	for _, item := range items {
		if resolve != nil {
			resolve(item)
		}
	}
	s.mu.Unlock()
}

type pendingAttentionSnapshot struct {
	items                       []PendingPromptSnapshot
	ordinaryOccurrenceWatermark attentionnotify.OrdinaryOccurrenceWatermark
}

func (s *pendingPromptStore) WithLockedAttentionSnapshotResult(sessionID string, fn func(pendingAttentionSnapshot) (serverapi.AttentionNotificationSubscription, error)) (serverapi.AttentionNotificationSubscription, error) {
	id := strings.TrimSpace(sessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	return fn(pendingAttentionSnapshot{
		items:                       listPendingPrompts(s.pending[id]),
		ordinaryOccurrenceWatermark: attentionnotify.OrdinaryOccurrenceWatermark(s.nextOrdinaryOccurrence[id]),
	})
}

func (s *pendingPromptStore) newSnapshotLocked(
	sessionID string,
	resource runtimeids.SessionResourceRef,
	scopeID runtimeids.ExecutionScopeID,
	req askquestion.AskQuestionRequest,
	createdAt time.Time,
) PendingPromptSnapshot {
	snapshot := PendingPromptSnapshot{
		Request:   req,
		CreatedAt: createdAt,
		Resource:  resource,
		ScopeID:   scopeID,
	}
	if req.QuestionBatch != nil &&
		req.AttentionTarget != nil &&
		req.AttentionTarget.Kind == clientui.AttentionNotificationTargetWorkflowTask {
		snapshot.occurrence = attentionnotify.NewTaskQuestionBatchOccurrenceMetadata(req.QuestionBatch.BatchID)
		return snapshot
	}
	s.nextOrdinaryOccurrence[sessionID]++
	snapshot.occurrence = attentionnotify.NewOrdinaryOccurrenceMetadata(s.nextOrdinaryOccurrence[sessionID])
	return snapshot
}

func listPendingPrompts(pending map[string]PendingPromptSnapshot) []PendingPromptSnapshot {
	items := make([]PendingPromptSnapshot, 0, len(pending))
	for _, item := range pending {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		return pendingPromptOrderLess(items[i].CreatedAt, items[i].Request.ID, items[j].CreatedAt, items[j].Request.ID)
	})
	return items
}

func pendingPromptOrderLess(leftCreatedAt time.Time, leftID string, rightCreatedAt time.Time, rightID string) bool {
	if leftCreatedAt.Equal(rightCreatedAt) {
		return leftID < rightID
	}
	return leftCreatedAt.Before(rightCreatedAt)
}
