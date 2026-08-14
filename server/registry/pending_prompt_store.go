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
	mu                  sync.Mutex
	pending             map[string]map[string]PendingPromptSnapshot
	sessionReadModels   map[string]*pendingPromptSessionReadModel
	publishedReadModels atomic.Pointer[pendingPromptReadModels]
}

type pendingPromptSessionReadModel struct {
	snapshot atomic.Pointer[pendingPromptCatalog]
}

type pendingPromptCatalog struct {
	items []PendingPromptSnapshot
}

type pendingPromptReadModels struct {
	bySession map[string]*pendingPromptSessionReadModel
}

func newPendingPromptStore() *pendingPromptStore {
	store := &pendingPromptStore{
		pending:           make(map[string]map[string]PendingPromptSnapshot),
		sessionReadModels: make(map[string]*pendingPromptSessionReadModel),
	}
	store.publishReadModelIndexLocked()
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
	return clonePendingPromptSnapshot(entry), true
}

func (s *pendingPromptStore) List(sessionID string) []PendingPromptSnapshot {
	if s == nil {
		return nil
	}
	readModels := s.publishedReadModels.Load()
	if readModels == nil {
		return nil
	}
	session := readModels.bySession[strings.TrimSpace(sessionID)]
	if session == nil {
		return nil
	}
	catalog := session.snapshot.Load()
	if catalog == nil {
		return nil
	}
	return clonePendingPromptSnapshots(catalog.items)
}

func (s *pendingPromptStore) CloseSession(sessionID string, resolve func(PendingPromptSnapshot)) {
	id := strings.TrimSpace(sessionID)
	s.mu.Lock()
	items := listPendingPrompts(s.pending[id])
	delete(s.pending, id)
	delete(s.sessionReadModels, id)
	s.publishReadModelIndexLocked()
	s.mu.Unlock()
	for _, item := range items {
		if resolve != nil {
			resolve(clonePendingPromptSnapshot(item))
		}
	}
}

func (s *pendingPromptStore) publishSessionLocked(sessionID string, pending map[string]PendingPromptSnapshot) {
	readModel := s.sessionReadModels[sessionID]
	if readModel == nil {
		readModel = &pendingPromptSessionReadModel{}
		readModel.snapshot.Store(&pendingPromptCatalog{items: listPendingPrompts(pending)})
		s.sessionReadModels[sessionID] = readModel
		s.publishReadModelIndexLocked()
		return
	}
	readModel.snapshot.Store(&pendingPromptCatalog{items: listPendingPrompts(pending)})
}

func (s *pendingPromptStore) publishReadModelIndexLocked() {
	bySession := make(map[string]*pendingPromptSessionReadModel, len(s.sessionReadModels))
	for sessionID, readModel := range s.sessionReadModels {
		bySession[sessionID] = readModel
	}
	s.publishedReadModels.Store(&pendingPromptReadModels{bySession: bySession})
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
