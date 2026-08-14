package registry

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"core/server/sessionruntime"
	askquestion "core/server/tools"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type PendingPromptSnapshot struct {
	Request   askquestion.AskQuestionRequest
	CreatedAt time.Time
	Resource  runtimeids.SessionResourceRef
	ScopeID   runtimeids.ExecutionScopeID
	SessionID runtimeids.SessionID
	PromptID  clientui.PromptID
	StepID    runtimeids.StepID
}

type pendingPromptStore struct {
	mu      sync.RWMutex
	pending map[string]map[string]PendingPromptSnapshot
}

func newPendingPromptStore() *pendingPromptStore {
	return &pendingPromptStore{pending: make(map[string]map[string]PendingPromptSnapshot)}
}

func (s *pendingPromptStore) Begin(sessionID string, resource runtimeids.SessionResourceRef, scopeID runtimeids.ExecutionScopeID, req askquestion.AskQuestionRequest, createdAt time.Time) (PendingPromptSnapshot, bool) {
	id, requestID := strings.TrimSpace(sessionID), strings.TrimSpace(req.ID)
	if id == "" || requestID == "" {
		return PendingPromptSnapshot{}, false
	}
	promptID := clientui.PromptID(requestID)
	if err := promptID.Validate(); err != nil {
		panic(fmt.Sprintf("pending prompt store received invalid Prompt ID %q: %v", req.ID, err))
	}
	stepID, err := runtimeids.ParseStepID(req.StepID)
	if err != nil {
		panic(fmt.Sprintf("pending prompt store received invalid Step ID %q: %v", req.StepID, err))
	}
	req.ID = requestID
	snapshot := PendingPromptSnapshot{
		Request:   req,
		CreatedAt: createdAt,
		Resource:  resource,
		ScopeID:   scopeID,
		SessionID: resource.SessionID(),
		PromptID:  promptID,
		StepID:    stepID,
	}
	s.mu.Lock()
	pending := s.pending[id]
	if pending == nil {
		pending = make(map[string]PendingPromptSnapshot)
		s.pending[id] = pending
	}
	pending[requestID] = snapshot
	s.mu.Unlock()
	return snapshot, true
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
	s.mu.Unlock()
	for _, item := range items {
		if resolve != nil {
			resolve(item)
		}
	}
}

func (s *pendingPromptStore) WithLockedAttentionSnapshotResult(sessionID string, fn func([]PendingPromptSnapshot) (serverapi.AttentionNotificationSubscription, error)) (serverapi.AttentionNotificationSubscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fn(listPendingPrompts(s.pending[strings.TrimSpace(sessionID)]))
}

func listPendingPrompts(pending map[string]PendingPromptSnapshot) []PendingPromptSnapshot {
	items := make([]PendingPromptSnapshot, 0, len(pending))
	for _, item := range pending {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		return sessionruntime.PendingPromptOrderLess(items[i].CreatedAt, items[i].Request.ID, items[j].CreatedAt, items[j].Request.ID)
	})
	return items
}
