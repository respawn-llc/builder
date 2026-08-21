package registry

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"core/server/sessionruntime"
	askquestion "core/server/tools"
	"core/shared/clientui"
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
	published atomic.Pointer[pendingPromptReadModel]
}

type pendingPromptReadModel struct {
	bySession map[string][]PendingPromptSnapshot
}

func newPendingPromptStore() *pendingPromptStore {
	store := &pendingPromptStore{pending: make(map[string]map[string]PendingPromptSnapshot)}
	store.published.Store(&pendingPromptReadModel{bySession: make(map[string][]PendingPromptSnapshot)})
	return store
}

func (s *pendingPromptStore) Begin(sessionID string, resource runtimeids.SessionResourceRef, scopeID runtimeids.ExecutionScopeID, req askquestion.AskQuestionRequest, createdAt time.Time) (PendingPromptSnapshot, bool) {
	id, requestID := strings.TrimSpace(sessionID), strings.TrimSpace(req.ID)
	if id == "" || requestID == "" {
		return PendingPromptSnapshot{}, false
	}
	snapshot := PendingPromptSnapshot{Request: cloneAskQuestionRequest(req), CreatedAt: createdAt, Resource: resource, ScopeID: scopeID}
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
	readModel := s.published.Load()
	if readModel == nil {
		return nil
	}
	return clonePendingPromptSnapshots(readModel.bySession[strings.TrimSpace(sessionID)])
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
	current := s.published.Load()
	size := 1
	if current != nil {
		size += len(current.bySession)
	}
	bySession := make(map[string][]PendingPromptSnapshot, size)
	if current != nil {
		for id, items := range current.bySession {
			if id != sessionID {
				bySession[id] = items
			}
		}
	}
	if items := listPendingPrompts(pending); len(items) != 0 {
		bySession[sessionID] = items
	}
	s.published.Store(&pendingPromptReadModel{bySession: bySession})
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

func clonePendingPromptSnapshots(items []PendingPromptSnapshot) []PendingPromptSnapshot {
	if len(items) == 0 {
		return nil
	}
	cloned := make([]PendingPromptSnapshot, 0, len(items))
	for _, item := range items {
		cloned = append(cloned, clonePendingPromptSnapshot(item))
	}
	return cloned
}

func clonePendingPromptSnapshot(snapshot PendingPromptSnapshot) PendingPromptSnapshot {
	snapshot.Request = cloneAskQuestionRequest(snapshot.Request)
	return snapshot
}

func cloneAskQuestionRequest(request askquestion.AskQuestionRequest) askquestion.AskQuestionRequest {
	request.Suggestions = append([]string(nil), request.Suggestions...)
	request.ApprovalOptions = append([]askquestion.AskQuestionApprovalOption(nil), request.ApprovalOptions...)
	if request.QuestionBatch != nil {
		batch := *request.QuestionBatch
		batch.BatchPromptIDs = append([]string(nil), batch.BatchPromptIDs...)
		request.QuestionBatch = &batch
	}
	request.AttentionTarget = cloneAttentionNotificationTarget(request.AttentionTarget)
	return request
}

func cloneAttentionNotificationTarget(target *clientui.AttentionNotificationTarget) *clientui.AttentionNotificationTarget {
	if target == nil {
		return nil
	}
	cloned := *target
	if target.WorkflowID != nil {
		workflowID := *target.WorkflowID
		cloned.WorkflowID = &workflowID
	}
	if target.CurrentNodeID != nil {
		currentNodeID := *target.CurrentNodeID
		cloned.CurrentNodeID = &currentNodeID
	}
	if target.CurrentNodeBranchKey != nil {
		branchKey := *target.CurrentNodeBranchKey
		cloned.CurrentNodeBranchKey = &branchKey
	}
	if target.Focus != nil {
		focus := *target.Focus
		focus.AskIDs = append([]string(nil), target.Focus.AskIDs...)
		cloned.Focus = &focus
	}
	return &cloned
}
