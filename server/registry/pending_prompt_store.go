package registry

import (
	"sort"
	"strings"
	"sync"
	"time"

	"core/server/sessionruntime"
	askquestion "core/server/tools"
	"core/shared/runtimeids"
)

type PendingPromptSnapshot struct {
	Request   askquestion.AskQuestionRequest
	CreatedAt time.Time
	Resource  runtimeids.SessionResourceRef
	ScopeID   runtimeids.ExecutionScopeID
}

type pendingPromptStore struct {
	mu        sync.Mutex
	pending   map[string]map[string]PendingPromptSnapshot
	published sync.Map
}

func newPendingPromptStore() *pendingPromptStore {
	return &pendingPromptStore{pending: make(map[string]map[string]PendingPromptSnapshot)}
}

func (s *pendingPromptStore) Begin(sessionID string, resource runtimeids.SessionResourceRef, scopeID runtimeids.ExecutionScopeID, req askquestion.AskQuestionRequest, createdAt time.Time) (PendingPromptSnapshot, bool) {
	id, requestID := strings.TrimSpace(sessionID), strings.TrimSpace(req.ID)
	if id == "" || requestID == "" {
		return PendingPromptSnapshot{}, false
	}
	snapshot := PendingPromptSnapshot{Request: req.Clone(), CreatedAt: createdAt, Resource: resource, ScopeID: scopeID}
	s.mu.Lock()
	pending := s.pending[id]
	if pending == nil {
		pending = make(map[string]PendingPromptSnapshot)
		s.pending[id] = pending
	}
	pending[requestID] = snapshot
	s.publishSessionLocked(id, pending)
	s.mu.Unlock()
	return clonePendingPromptSnapshot(snapshot), true
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
		if len(pending) == 0 {
			delete(s.pending, id)
		}
		s.publishSessionLocked(id, pending)
	}
	s.mu.Unlock()
	if !exists {
		return PendingPromptSnapshot{}, false
	}
	return entry, true
}

func (s *pendingPromptStore) List(sessionID string) []PendingPromptSnapshot {
	if s == nil {
		return nil
	}
	value, ok := s.published.Load(strings.TrimSpace(sessionID))
	if !ok {
		return nil
	}
	items, ok := value.([]PendingPromptSnapshot)
	if !ok {
		panic("Pending Prompt index contains an invalid entry")
	}
	cloned := make([]PendingPromptSnapshot, len(items))
	for index, item := range items {
		cloned[index] = clonePendingPromptSnapshot(item)
	}
	return cloned
}

func (s *pendingPromptStore) CloseSession(sessionID string, resolve func(PendingPromptSnapshot)) {
	id := strings.TrimSpace(sessionID)
	s.mu.Lock()
	items := listPendingPrompts(s.pending[id])
	delete(s.pending, id)
	s.publishSessionLocked(id, nil)
	s.mu.Unlock()
	for _, item := range items {
		if resolve != nil {
			resolve(item)
		}
	}
}

func (s *pendingPromptStore) publishSessionLocked(sessionID string, pending map[string]PendingPromptSnapshot) {
	items := listPendingPrompts(pending)
	if len(items) == 0 {
		s.published.Delete(sessionID)
		return
	}
	s.published.Store(sessionID, items)
}

func listPendingPrompts(pending map[string]PendingPromptSnapshot) []PendingPromptSnapshot {
	items := make([]PendingPromptSnapshot, 0, len(pending))
	for _, item := range pending {
		items = append(items, clonePendingPromptSnapshot(item))
	}
	sort.Slice(items, func(i, j int) bool {
		return sessionruntime.PendingPromptOrderLess(items[i].CreatedAt, items[i].Request.ID, items[j].CreatedAt, items[j].Request.ID)
	})
	return items
}

func clonePendingPromptSnapshot(snapshot PendingPromptSnapshot) PendingPromptSnapshot {
	snapshot.Request = snapshot.Request.Clone()
	return snapshot
}
