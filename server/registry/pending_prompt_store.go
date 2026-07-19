package registry

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	askquestion "core/server/tools"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type PendingPromptSnapshot struct {
	Request   askquestion.AskQuestionRequest
	CreatedAt time.Time
	Resource  runtimeids.SessionResourceRef
	ScopeID   runtimeids.ExecutionScopeID
}

type pendingPromptEntry struct {
	PendingPromptSnapshot
	response chan promptResponseResult
}

type promptResponseResult struct {
	response askquestion.AskQuestionResponse
	err      error
}

type pendingPromptStore struct {
	mu      sync.RWMutex
	pending map[string]map[string]*pendingPromptEntry
}

func newPendingPromptStore() *pendingPromptStore {
	return &pendingPromptStore{pending: make(map[string]map[string]*pendingPromptEntry)}
}

func (s *pendingPromptStore) Begin(sessionID string, resource runtimeids.SessionResourceRef, scopeID runtimeids.ExecutionScopeID, req askquestion.AskQuestionRequest, createdAt time.Time, publish func(PendingPromptSnapshot, pendingPromptEventType)) (PendingPromptSnapshot, bool) {
	id, requestID := strings.TrimSpace(sessionID), strings.TrimSpace(req.ID)
	if id == "" || requestID == "" {
		return PendingPromptSnapshot{}, false
	}
	snapshot := PendingPromptSnapshot{Request: req, CreatedAt: createdAt, Resource: resource, ScopeID: scopeID}
	s.mu.Lock()
	pending := s.pending[id]
	if pending == nil {
		pending = make(map[string]*pendingPromptEntry)
		s.pending[id] = pending
	}
	pending[requestID] = &pendingPromptEntry{PendingPromptSnapshot: snapshot}
	s.mu.Unlock()
	if publish != nil {
		publish(snapshot, pendingPromptEventPending)
	}
	return snapshot, true
}

func (s *pendingPromptStore) Complete(sessionID string, resource runtimeids.SessionResourceRef, scopeID runtimeids.ExecutionScopeID, requestID string) (PendingPromptSnapshot, bool) {
	id, askID := strings.TrimSpace(sessionID), strings.TrimSpace(requestID)
	if id == "" || askID == "" {
		return PendingPromptSnapshot{}, false
	}
	s.mu.Lock()
	pending := s.pending[id]
	entry := pending[askID]
	if entry != nil && resource.Validate() == nil && !scopeID.IsZero() &&
		(entry.Resource != resource || entry.ScopeID != scopeID) {
		entry = nil
	}
	if entry != nil {
		delete(pending, askID)
	}
	if len(pending) == 0 {
		delete(s.pending, id)
	}
	s.mu.Unlock()
	if entry == nil {
		return PendingPromptSnapshot{}, false
	}
	return entry.PendingPromptSnapshot, true
}

func (s *pendingPromptStore) List(sessionID string) []PendingPromptSnapshot {
	s.mu.RLock()
	items := listPendingPrompts(s.pending[strings.TrimSpace(sessionID)])
	s.mu.RUnlock()
	return items
}

func (s *pendingPromptStore) Await(ctx context.Context, sessionID string, req askquestion.AskQuestionRequest, publish func(PendingPromptSnapshot, pendingPromptEventType)) (askquestion.AskQuestionResponse, error) {
	id, requestID := strings.TrimSpace(sessionID), strings.TrimSpace(req.ID)
	if id == "" || requestID == "" {
		return askquestion.AskQuestionResponse{}, fmt.Errorf("session id and request id are required")
	}
	entry := &pendingPromptEntry{
		PendingPromptSnapshot: PendingPromptSnapshot{Request: req, CreatedAt: time.Now().UTC()},
		response:              make(chan promptResponseResult, 1),
	}
	s.mu.Lock()
	pending := s.pending[id]
	if pending == nil {
		pending = make(map[string]*pendingPromptEntry)
		s.pending[id] = pending
	}
	if _, exists := pending[requestID]; exists {
		s.mu.Unlock()
		return askquestion.AskQuestionResponse{}, fmt.Errorf("prompt %q is already pending", requestID)
	}
	pending[requestID] = entry
	s.mu.Unlock()
	if publish != nil {
		publish(entry.PendingPromptSnapshot, pendingPromptEventPending)
	}
	defer func() {
		var resolved bool
		s.mu.Lock()
		pending := s.pending[id]
		if current := pending[requestID]; current == entry {
			resolved = true
			delete(pending, requestID)
			if len(pending) == 0 {
				delete(s.pending, id)
			}
		}
		s.mu.Unlock()
		if resolved && publish != nil {
			publish(entry.PendingPromptSnapshot, pendingPromptEventResolved)
		}
	}()
	select {
	case <-ctx.Done():
		return askquestion.AskQuestionResponse{}, ctx.Err()
	case result := <-entry.response:
		return result.response, result.err
	}
}

func (s *pendingPromptStore) Submit(sessionID string, resp askquestion.AskQuestionResponse, err error, publish func(PendingPromptSnapshot, pendingPromptEventType)) error {
	id, requestID := strings.TrimSpace(sessionID), strings.TrimSpace(resp.RequestID)
	if id == "" || requestID == "" {
		return fmt.Errorf("session id and request id are required")
	}
	s.mu.Lock()
	pending := s.pending[id]
	entry := pending[requestID]
	if entry == nil {
		s.mu.Unlock()
		return fmt.Errorf("prompt %q not found: %w", requestID, serverapi.ErrPromptNotFound)
	}
	if entry.response == nil {
		s.mu.Unlock()
		return fmt.Errorf("prompt %q cannot be answered through the shared boundary: %w", requestID, serverapi.ErrPromptUnsupported)
	}
	if err == nil {
		if validateErr := askquestion.ValidateAskQuestionResponse(entry.Request, resp); validateErr != nil {
			s.mu.Unlock()
			return validateErr
		}
	}
	delete(pending, requestID)
	if len(pending) == 0 {
		delete(s.pending, id)
	}
	s.mu.Unlock()
	entry.response <- promptResponseResult{response: resp, err: err}
	if publish != nil {
		publish(entry.PendingPromptSnapshot, pendingPromptEventResolved)
	}
	return nil
}

func (s *pendingPromptStore) CloseSession(sessionID string, err error) {
	id := strings.TrimSpace(sessionID)
	s.mu.Lock()
	pending := s.pending[id]
	delete(s.pending, id)
	s.mu.Unlock()
	for _, entry := range pending {
		if entry.response != nil {
			entry.response <- promptResponseResult{err: err}
		}
	}
}

func (s *pendingPromptStore) WithLockedAttentionSnapshotResult(sessionID string, fn func([]PendingPromptSnapshot) (serverapi.AttentionNotificationSubscription, error)) (serverapi.AttentionNotificationSubscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fn(listPendingPrompts(s.pending[strings.TrimSpace(sessionID)]))
}

func listPendingPrompts(pending map[string]*pendingPromptEntry) []PendingPromptSnapshot {
	items := make([]PendingPromptSnapshot, 0, len(pending))
	for _, item := range pending {
		if item != nil {
			items = append(items, item.PendingPromptSnapshot)
		}
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
