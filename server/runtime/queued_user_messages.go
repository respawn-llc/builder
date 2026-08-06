package runtime

import (
	"core/server/llm"
	"core/shared/textutil"
	"errors"
	"strings"
	"sync"

	"github.com/google/uuid"
)

var errInvalidQueuedUserMessage = errors.New("queued message requires a role and content")

type queuedUserMessageStore struct {
	mu      sync.Mutex
	pending []queuedUserMessage
}

type queuedUserMessage struct {
	message QueuedUserMessage
}

func newQueuedUserMessageStore() *queuedUserMessageStore {
	return &queuedUserMessageStore{}
}

func (s *queuedUserMessageStore) Queue(text string, clientRequestID ...string) (QueuedUserMessage, error) {
	requestID := ""
	if len(clientRequestID) > 0 {
		requestID = clientRequestID[0]
	}
	return s.QueueItem(QueuedUserMessage{
		ID:              uuid.NewString(),
		ClientRequestID: strings.TrimSpace(requestID),
		Message:         llm.Message{Role: llm.RoleUser, Content: textutil.Value(text)},
	})
}

func (s *queuedUserMessageStore) QueueItem(item QueuedUserMessage) (QueuedUserMessage, error) {
	item.ID = strings.TrimSpace(item.ID)
	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	item.ClientRequestID = strings.TrimSpace(item.ClientRequestID)
	if item.Message.Content == nil ||
		strings.TrimSpace(*item.Message.Content) == "" ||
		item.Message.Role == "" {
		return QueuedUserMessage{}, errInvalidQueuedUserMessage
	}
	s.mu.Lock()
	s.pending = append(s.pending, queuedUserMessage{message: item})
	s.mu.Unlock()
	return item, nil
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

func queuedUserMessageWithID(id, text, clientRequestID string) QueuedUserMessage {
	return QueuedUserMessage{
		ID:              strings.TrimSpace(id),
		ClientRequestID: strings.TrimSpace(clientRequestID),
		Message:         llm.Message{Role: llm.RoleUser, Content: textutil.Value(text)},
	}
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
