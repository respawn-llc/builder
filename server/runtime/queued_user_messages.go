package runtime

import (
	"errors"
	"strings"
	"sync"

	"core/server/llm"
	"core/shared/runtimeids"
	"core/shared/textutil"

	"github.com/google/uuid"
)

var errInvalidQueuedUserMessage = errors.New("queued message requires a role and content")

type queuedUserMessageStore struct {
	mu      sync.Mutex
	pending []queuedUserMessage
}

type queuedUserMessage struct {
	message   QueuedUserMessage
	admission uint64
	scope     *runtimeids.ExecutionScopeID
}

func newQueuedUserMessageStore() *queuedUserMessageStore {
	return &queuedUserMessageStore{}
}

func (s *queuedUserMessageStore) Queue(text string, association ...queuedUserMessageAssociation) (QueuedUserMessage, error) {
	return s.QueueItem(QueuedUserMessage{
		ID:      uuid.NewString(),
		Message: llm.Message{Role: llm.RoleUser, Content: textutil.Value(text)},
	}, association...)
}

type queuedUserMessageAssociation struct {
	admission uint64
	scope     *runtimeids.ExecutionScopeID
}

type interruptedHumanSteering struct {
	ordinal uint64
	item    QueuedUserMessage
}

func cloneExecutionScopeID(value *runtimeids.ExecutionScopeID) *runtimeids.ExecutionScopeID {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func (s *queuedUserMessageStore) QueueItem(item QueuedUserMessage, associations ...queuedUserMessageAssociation) (QueuedUserMessage, error) {
	item.ID = strings.TrimSpace(item.ID)
	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	if item.Message.Content == nil ||
		strings.TrimSpace(*item.Message.Content) == "" ||
		item.Message.Role == "" {
		return QueuedUserMessage{}, errInvalidQueuedUserMessage
	}
	var association queuedUserMessageAssociation
	if len(associations) != 0 {
		association = associations[0]
	}
	s.mu.Lock()
	s.pending = append(s.pending, queuedUserMessage{
		message:   item,
		admission: association.admission,
		scope:     cloneExecutionScopeID(association.scope),
	})
	s.mu.Unlock()
	return item, nil
}

func (s *queuedUserMessageStore) DrainByScope(scopeID runtimeids.ExecutionScopeID) []interruptedHumanSteering {
	if s == nil || scopeID.IsZero() {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := make([]interruptedHumanSteering, 0)
	remaining := s.pending[:0]
	for _, pending := range s.pending {
		if pending.scope == nil || *pending.scope != scopeID {
			remaining = append(remaining, pending)
			continue
		}
		removed = append(removed, interruptedHumanSteering{
			ordinal: pending.admission,
			item:    pending.message,
		})
	}
	s.pending = remaining
	return removed
}

func (s *queuedUserMessageStore) DrainInterrupted() []interruptedHumanSteering {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]interruptedHumanSteering, 0, len(s.pending))
	for _, pending := range s.pending {
		items = append(items, interruptedHumanSteering{
			ordinal: pending.admission,
			item:    pending.message,
		})
	}
	s.pending = nil
	return items
}

func (m QueuedUserMessage) DisplayText() (string, error) {
	if m.Message.Content == nil {
		return "", errInvalidQueuedUserMessage
	}
	return *m.Message.Content, nil
}

func (s *queuedUserMessageStore) Discard(queueItemID string) bool {
	_, removed := s.DiscardItem(queueItemID)
	return removed
}

func (s *queuedUserMessageStore) DiscardItem(queueItemID string) (QueuedUserMessage, bool) {
	id := strings.TrimSpace(queueItemID)
	if id == "" || s == nil {
		return QueuedUserMessage{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	filtered := s.pending[:0]
	removed := false
	var item QueuedUserMessage
	for _, pending := range s.pending {
		if pending.message.ID == id {
			removed = true
			item = pending.message
			continue
		}
		filtered = append(filtered, pending)
	}
	s.pending = filtered
	return item, removed
}

func (s *queuedUserMessageStore) Drain() []queuedUserMessage {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	pending := append([]queuedUserMessage(nil), s.pending...)
	s.pending = nil
	s.mu.Unlock()
	return pending
}

func (s *queuedUserMessageStore) DrainByID(ids map[string]struct{}) []queuedUserMessage {
	if s == nil || len(ids) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	matched := make([]queuedUserMessage, 0, len(ids))
	remaining := s.pending[:0]
	for _, pending := range s.pending {
		if _, ok := ids[strings.TrimSpace(pending.message.ID)]; ok {
			matched = append(matched, pending)
			continue
		}
		remaining = append(remaining, pending)
	}
	s.pending = remaining
	return matched
}

func (s *queuedUserMessageStore) RestoreFront(items []queuedUserMessage) {
	if s == nil || len(items) == 0 {
		return
	}
	restored := append([]queuedUserMessage(nil), items...)
	s.mu.Lock()
	s.pending = append(restored, s.pending...)
	s.mu.Unlock()
}

func (s *queuedUserMessageStore) HasPending() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending) > 0
}

func (s *queuedUserMessageStore) Snapshot() []QueuedUserMessage {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]QueuedUserMessage, 0, len(s.pending))
	for _, pending := range s.pending {
		out = append(out, pending.message)
	}
	return out
}
