package registry

import (
	"maps"
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
	mu       sync.Mutex
	sessions sync.Map
}

func (s *pendingPromptStore) Begin(sessionID string, resource runtimeids.SessionResourceRef, scopeID runtimeids.ExecutionScopeID, req askquestion.AskQuestionRequest, createdAt time.Time) (PendingPromptSnapshot, bool) {
	id, requestID := strings.TrimSpace(sessionID), strings.TrimSpace(req.ID)
	if id == "" || requestID == "" {
		return PendingPromptSnapshot{}, false
	}
	snapshot := PendingPromptSnapshot{Request: req.Clone(), CreatedAt: createdAt, Resource: resource, ScopeID: scopeID}
	s.mu.Lock()
	pending := maps.Clone(s.load(id))
	if pending == nil {
		pending = make(map[string]PendingPromptSnapshot)
	}
	pending[requestID] = snapshot
	s.sessions.Store(id, pending)
	s.mu.Unlock()
	return clonePendingPromptSnapshot(snapshot), true
}

func (s *pendingPromptStore) Complete(sessionID string, resource runtimeids.SessionResourceRef, scopeID runtimeids.ExecutionScopeID, requestID string) (PendingPromptSnapshot, bool) {
	id, askID := strings.TrimSpace(sessionID), strings.TrimSpace(requestID)
	if id == "" || askID == "" {
		return PendingPromptSnapshot{}, false
	}
	s.mu.Lock()
	pending := s.load(id)
	entry, exists := pending[askID]
	if exists && resource.Validate() == nil && !scopeID.IsZero() &&
		(entry.Resource != resource || entry.ScopeID != scopeID) {
		exists = false
	}
	if exists {
		next := maps.Clone(pending)
		delete(next, askID)
		if len(next) == 0 {
			s.sessions.Delete(id)
		} else {
			s.sessions.Store(id, next)
		}
	}
	s.mu.Unlock()
	if !exists {
		return PendingPromptSnapshot{}, false
	}
	return clonePendingPromptSnapshot(entry), true
}

func (s *pendingPromptStore) List(sessionID string) []PendingPromptSnapshot {
	if s == nil {
		return nil
	}
	return listPendingPrompts(s.load(strings.TrimSpace(sessionID)))
}

func (s *pendingPromptStore) CloseSession(sessionID string, resolve func(PendingPromptSnapshot)) {
	id := strings.TrimSpace(sessionID)
	s.mu.Lock()
	items := listPendingPrompts(s.load(id))
	s.sessions.Delete(id)
	s.mu.Unlock()
	for _, item := range items {
		if resolve != nil {
			resolve(item)
		}
	}
}

func (s *pendingPromptStore) load(sessionID string) map[string]PendingPromptSnapshot {
	value, ok := s.sessions.Load(sessionID)
	if !ok {
		return nil
	}
	items, ok := value.(map[string]PendingPromptSnapshot)
	if !ok {
		panic("Pending Prompt index contains an invalid entry")
	}
	return items
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
