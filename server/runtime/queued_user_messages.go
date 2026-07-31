package runtime

import (
	"core/server/llm"
	"core/shared/textutil"
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
	claimed bool
}

type queuedUserMessageClaim struct {
	store *queuedUserMessageStore
	items []queuedUserSteeringIntent
	ids   map[string]struct{}
	done  bool
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
	intent := steerMessagesWithPersistenceIntent(steeringPriorityUser, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value(item.Text)}})
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
		if pending.claimed {
			filtered = append(filtered, pending)
			continue
		}
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

func (s *queuedUserMessageStore) ClaimAll() queuedUserMessageClaim {
	return s.claim(func(queuedUserSteeringIntent) bool { return true })
}

func (s *queuedUserMessageStore) ClaimByID(ids map[string]struct{}) queuedUserMessageClaim {
	return s.claim(func(item queuedUserSteeringIntent) bool {
		_, ok := ids[strings.TrimSpace(item.message.ID)]
		return ok
	})
}

func (s *queuedUserMessageStore) claim(selectItem func(queuedUserSteeringIntent) bool) queuedUserMessageClaim {
	claim := queuedUserMessageClaim{store: s}
	if s == nil || selectItem == nil {
		return claim
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.pending {
		if s.pending[index].claimed || !selectItem(s.pending[index]) {
			continue
		}
		s.pending[index].claimed = true
		claim.items = append(claim.items, s.pending[index])
		if claim.ids == nil {
			claim.ids = make(map[string]struct{})
		}
		claim.ids[s.pending[index].message.ID] = struct{}{}
	}
	return claim
}

func (c *queuedUserMessageClaim) Items() []queuedUserSteeringIntent {
	if c == nil {
		return nil
	}
	return append([]queuedUserSteeringIntent(nil), c.items...)
}

func (c *queuedUserMessageClaim) Restore() {
	c.finish(nil)
}

func (c *queuedUserMessageClaim) Commit(ids []string) {
	selected := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		selected[strings.TrimSpace(id)] = struct{}{}
	}
	c.finish(selected)
}

func (c *queuedUserMessageClaim) finish(selected map[string]struct{}) {
	if c == nil || c.done || c.store == nil {
		return
	}
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	if c.done {
		return
	}
	remaining := c.store.pending[:0]
	for _, item := range c.store.pending {
		if !item.claimed {
			remaining = append(remaining, item)
			continue
		}
		_, claimedByThis := c.ids[item.message.ID]
		if !claimedByThis {
			remaining = append(remaining, item)
			continue
		}
		if _, remove := selected[item.message.ID]; !remove {
			item.claimed = false
			remaining = append(remaining, item)
		}
	}
	c.store.pending = remaining
	c.done = true
}

func idsFromQueuedUserIntents(items []queuedUserSteeringIntent) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.message.ID)
	}
	return ids
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
