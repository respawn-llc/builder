package runtime

import (
	"core/server/llm"
	"strings"
	"sync"

	"github.com/google/uuid"
)

type queuedUserMessageStore struct {
	mu      sync.Mutex
	pending []queuedUserSteeringIntent
}

type queuedUserSteeringIntent struct {
	message QueuedUserMessage
	intent  steeringIntent
}

func newQueuedUserMessageStore() *queuedUserMessageStore {
	return &queuedUserMessageStore{}
}

func (s *queuedUserMessageStore) Queue(text string, clientRequestID ...string) QueuedUserMessage {
	requestID := ""
	if len(clientRequestID) > 0 {
		requestID = clientRequestID[0]
	}
	return s.QueueItem(QueuedUserMessage{ID: uuid.NewString(), Text: text, ClientRequestID: strings.TrimSpace(requestID)})
}

func (s *queuedUserMessageStore) QueueItem(item QueuedUserMessage) QueuedUserMessage {
	item.ID = strings.TrimSpace(item.ID)
	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	item.ClientRequestID = strings.TrimSpace(item.ClientRequestID)
	intent := steerMessagesWithPersistenceIntent(steeringPriorityUser, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleUser, Content: item.Text}})
	s.mu.Lock()
	s.pending = append(s.pending, queuedUserSteeringIntent{message: item, intent: intent})
	s.mu.Unlock()
	return item
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

func (s *queuedUserMessageStore) Drain() []queuedUserSteeringIntent {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	pending := append([]queuedUserSteeringIntent(nil), s.pending...)
	s.pending = nil
	s.mu.Unlock()
	return pending
}

func (s *queuedUserMessageStore) DrainByID(ids map[string]struct{}) []queuedUserSteeringIntent {
	if s == nil || len(ids) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	matched := make([]queuedUserSteeringIntent, 0, len(ids))
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

func (s *queuedUserMessageStore) RestoreFront(items []queuedUserSteeringIntent) {
	if s == nil || len(items) == 0 {
		return
	}
	restored := append([]queuedUserSteeringIntent(nil), items...)
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
