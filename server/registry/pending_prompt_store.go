package registry

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	askquestion "core/server/tools"
	"core/shared/serverapi"
)

type PendingPromptSnapshot struct {
	Request   askquestion.AskQuestionRequest
	CreatedAt time.Time
}

type pendingPromptEntry struct {
	PendingPromptSnapshot
	response chan promptResponseResult
	closed   bool
}

type promptResponseResult struct {
	response askquestion.AskQuestionResponse
	err      error
}

type pendingPromptStore struct {
	mu      sync.RWMutex
	pending map[string]*pendingPromptEntry
}

func newPendingPromptStore() *pendingPromptStore {
	return &pendingPromptStore{pending: make(map[string]*pendingPromptEntry)}
}

func (s *pendingPromptStore) Begin(req askquestion.AskQuestionRequest, publish func(PendingPromptSnapshot, pendingPromptEventType)) (PendingPromptSnapshot, bool) {
	if s == nil {
		return PendingPromptSnapshot{}, false
	}
	requestID := strings.TrimSpace(req.ID)
	if requestID == "" {
		return PendingPromptSnapshot{}, false
	}
	snapshot := PendingPromptSnapshot{Request: req, CreatedAt: time.Now()}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending[requestID] = &pendingPromptEntry{PendingPromptSnapshot: snapshot}
	if publish != nil {
		publish(snapshot, pendingPromptEventPending)
	}
	return snapshot, true
}

func (s *pendingPromptStore) Complete(requestID string) (PendingPromptSnapshot, bool) {
	if s == nil {
		return PendingPromptSnapshot{}, false
	}
	id := strings.TrimSpace(requestID)
	if id == "" {
		return PendingPromptSnapshot{}, false
	}
	var snapshot PendingPromptSnapshot
	s.mu.Lock()
	if pending, ok := s.pending[id]; ok {
		pending.closed = true
		snapshot = pending.PendingPromptSnapshot
	}
	delete(s.pending, id)
	s.mu.Unlock()
	return snapshot, snapshot.Request.ID != ""
}

func (s *pendingPromptStore) List() []PendingPromptSnapshot {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	items := s.listLocked()
	s.mu.RUnlock()
	return items
}

func (s *pendingPromptStore) Has(requestID string) bool {
	if s == nil {
		return false
	}
	id := strings.TrimSpace(requestID)
	if id == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	pending := s.pending[id]
	return pending != nil && !pending.closed
}

func (s *pendingPromptStore) Await(ctx context.Context, req askquestion.AskQuestionRequest, publish func(PendingPromptSnapshot, pendingPromptEventType)) (askquestion.AskQuestionResponse, error) {
	if s == nil {
		return askquestion.AskQuestionResponse{}, fmt.Errorf("pending prompt store is required")
	}
	requestID := strings.TrimSpace(req.ID)
	if requestID == "" {
		return askquestion.AskQuestionResponse{}, fmt.Errorf("session id and request id are required")
	}
	pending := &pendingPromptEntry{
		PendingPromptSnapshot: PendingPromptSnapshot{Request: req, CreatedAt: time.Now()},
		response:              make(chan promptResponseResult, 1),
	}
	s.mu.Lock()
	if _, exists := s.pending[requestID]; exists {
		s.mu.Unlock()
		return askquestion.AskQuestionResponse{}, fmt.Errorf("prompt %q is already pending", requestID)
	}
	s.pending[requestID] = pending
	func() {
		defer s.mu.Unlock()
		if publish != nil {
			publish(pending.PendingPromptSnapshot, pendingPromptEventPending)
		}
	}()
	defer func() {
		var shouldPublishResolved bool
		s.mu.Lock()
		current, ok := s.pending[requestID]
		if ok && current == pending {
			shouldPublishResolved = !current.closed
			current.closed = true
			delete(s.pending, requestID)
		}
		s.mu.Unlock()
		if shouldPublishResolved && publish != nil {
			publish(pending.PendingPromptSnapshot, pendingPromptEventResolved)
		}
	}()
	select {
	case <-ctx.Done():
		return askquestion.AskQuestionResponse{}, ctx.Err()
	case result := <-pending.response:
		return result.response, result.err
	}
}

func (s *pendingPromptStore) Submit(resp askquestion.AskQuestionResponse, err error, publish func(PendingPromptSnapshot, pendingPromptEventType)) error {
	if s == nil {
		return fmt.Errorf("pending prompt store is required")
	}
	requestID := strings.TrimSpace(resp.RequestID)
	if requestID == "" {
		return fmt.Errorf("session id and request id are required")
	}
	s.mu.Lock()
	pending := s.pending[requestID]
	if pending == nil {
		s.mu.Unlock()
		return fmt.Errorf("prompt %q not found: %w", requestID, serverapi.ErrPromptNotFound)
	}
	if pending.closed {
		s.mu.Unlock()
		return fmt.Errorf("prompt %q is already resolved: %w", requestID, serverapi.ErrPromptAlreadyResolved)
	}
	if pending.response == nil {
		s.mu.Unlock()
		return fmt.Errorf("prompt %q cannot be answered through the shared boundary: %w", requestID, serverapi.ErrPromptUnsupported)
	}
	pending.closed = true
	snapshot := pending.PendingPromptSnapshot
	ch := pending.response
	delete(s.pending, requestID)
	s.mu.Unlock()
	ch <- promptResponseResult{response: resp, err: err}
	publish(snapshot, pendingPromptEventResolved)
	return nil
}

func (s *pendingPromptStore) Close(err error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	items := make([]*pendingPromptEntry, 0, len(s.pending))
	for id, pending := range s.pending {
		if pending == nil {
			delete(s.pending, id)
			continue
		}
		pending.closed = true
		items = append(items, pending)
		delete(s.pending, id)
	}
	s.mu.Unlock()
	for _, pending := range items {
		if pending.response == nil {
			continue
		}
		select {
		case pending.response <- promptResponseResult{err: err}:
		default:
		}
	}
}

func (s *pendingPromptStore) WithLockedSnapshotResult(fn func([]PendingPromptSnapshot) (*promptActivitySubscription, error)) (*promptActivitySubscription, error) {
	if s == nil {
		return nil, fmt.Errorf("pending prompt store is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return fn(s.listLocked())
}

func (s *pendingPromptStore) WithLockedAttentionSnapshotResult(fn func([]PendingPromptSnapshot) (serverapi.AttentionNotificationSubscription, error)) (serverapi.AttentionNotificationSubscription, error) {
	if s == nil {
		return nil, fmt.Errorf("pending prompt store is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return fn(s.listLocked())
}

func (s *pendingPromptStore) listLocked() []PendingPromptSnapshot {
	items := make([]PendingPromptSnapshot, 0, len(s.pending))
	for _, item := range s.pending {
		if item == nil {
			continue
		}
		items = append(items, item.PendingPromptSnapshot)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].Request.ID < items[j].Request.ID
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
	return items
}
