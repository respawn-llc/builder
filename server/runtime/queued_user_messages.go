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
var errDuplicateQueuedUserMessageID = errors.New("queued message identity is already pending")

type queuedUserMessageStore struct {
	mu          sync.Mutex
	items       []queuedUserMessage
	nextClaimID queuedUserMessageClaimID
}

type queuedUserMessage struct {
	message         QueuedUserMessage
	steerAdmission  *pendingWorkSteerAdmission
	claimID         *queuedUserMessageClaimID
	removeOnRelease bool
}

type queuedUserMessageClaimID uint64

type queuedUserMessageClaim struct {
	id    queuedUserMessageClaimID
	items []queuedUserMessage
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
	steerAdmission *pendingWorkSteerAdmission
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
	if _, err := runtimeids.ParseQueueItemID(item.ID); err != nil {
		return QueuedUserMessage{}, err
	}
	var association queuedUserMessageAssociation
	if len(associations) != 0 {
		association = associations[0]
	}
	s.mu.Lock()
	for _, pending := range s.items {
		if pending.message.ID == item.ID {
			s.mu.Unlock()
			return QueuedUserMessage{}, errDuplicateQueuedUserMessageID
		}
	}
	s.items = append(s.items, queuedUserMessage{
		message:        item,
		steerAdmission: clonePendingWorkSteerAdmission(association.steerAdmission),
	})
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

func (s *queuedUserMessageStore) DiscardItem(queueItemID string) (queuedUserMessage, bool) {
	id := strings.TrimSpace(queueItemID)
	if id == "" || s == nil {
		return queuedUserMessage{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	filtered := s.items[:0]
	removed := false
	var item queuedUserMessage
	for _, pending := range s.items {
		if pending.message.ID == id && pending.claimID == nil {
			removed = true
			item = pending
			continue
		}
		filtered = append(filtered, pending)
	}
	s.items = filtered
	return item, removed
}

func (s *queuedUserMessageStore) ClaimAll() *queuedUserMessageClaim {
	return s.claim(func(queuedUserMessage) bool { return true })
}

func (s *queuedUserMessageStore) ClaimSteers() *queuedUserMessageClaim {
	return s.claim(func(pending queuedUserMessage) bool {
		return pending.steerAdmission != nil
	})
}

func (s *queuedUserMessageStore) claim(selected func(queuedUserMessage) bool) *queuedUserMessageClaim {
	if s == nil || selected == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.nextClaimID + 1
	if next == 0 {
		panic("queued user message claim identity overflow")
	}
	items := make([]queuedUserMessage, 0)
	for index := range s.items {
		pending := &s.items[index]
		if pending.claimID != nil || !selected(*pending) {
			continue
		}
		pending.claimID = &next
		items = append(items, *pending)
	}
	if len(items) == 0 {
		return nil
	}
	s.nextClaimID = next
	return &queuedUserMessageClaim{id: next, items: items}
}

func (s *queuedUserMessageStore) FinalizeClaimItems(claim *queuedUserMessageClaim, ids map[string]struct{}) []queuedUserMessage {
	if s == nil || claim == nil || len(ids) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	finalized := make([]queuedUserMessage, 0, len(ids))
	remaining := s.items[:0]
	for _, pending := range s.items {
		_, selected := ids[strings.TrimSpace(pending.message.ID)]
		if selected && pending.claimID != nil && *pending.claimID == claim.id {
			finalized = append(finalized, pending)
			continue
		}
		remaining = append(remaining, pending)
	}
	s.items = remaining
	if len(finalized) != len(ids) {
		panic("queued user message claim lost an owned item before finalization")
	}
	return finalized
}

func (s *queuedUserMessageStore) FailClaimItems(
	claim *queuedUserMessageClaim,
	ids map[string]struct{},
) (technical []queuedUserMessage, stopped []QueuedUserMessage) {
	if s == nil || claim == nil || len(ids) == 0 {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	remaining := s.items[:0]
	for _, pending := range s.items {
		_, selected := ids[strings.TrimSpace(pending.message.ID)]
		if !selected || pending.claimID == nil || *pending.claimID != claim.id {
			remaining = append(remaining, pending)
			continue
		}
		if pending.removeOnRelease {
			stopped = append(stopped, pending.message)
		} else {
			technical = append(technical, pending)
		}
	}
	s.items = remaining
	if len(technical)+len(stopped) != len(ids) {
		panic("queued user message claim lost an owned item before failure")
	}
	return technical, stopped
}

func (s *queuedUserMessageStore) ReleaseClaim(claim *queuedUserMessageClaim) []QueuedUserMessage {
	if s == nil || claim == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := make([]QueuedUserMessage, 0)
	remaining := s.items[:0]
	for _, pending := range s.items {
		if pending.claimID == nil || *pending.claimID != claim.id {
			remaining = append(remaining, pending)
			continue
		}
		if pending.removeOnRelease {
			removed = append(removed, pending.message)
			continue
		}
		pending.claimID = nil
		remaining = append(remaining, pending)
	}
	s.items = remaining
	return removed
}

func (s *queuedUserMessageStore) Drain() []queuedUserMessage {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	pending := make([]queuedUserMessage, 0, len(s.items))
	remaining := s.items[:0]
	for _, item := range s.items {
		if item.claimID != nil {
			remaining = append(remaining, item)
			continue
		}
		pending = append(pending, item)
	}
	s.items = remaining
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
	remaining := s.items[:0]
	for _, pending := range s.items {
		if _, ok := ids[strings.TrimSpace(pending.message.ID)]; !ok {
			remaining = append(remaining, pending)
			continue
		}
		if pending.claimID != nil {
			pending.removeOnRelease = true
			remaining = append(remaining, pending)
			continue
		}
		matched = append(matched, pending)
	}
	s.items = remaining
	return matched
}

func (s *queuedUserMessageStore) HasPending() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items) > 0
}

func (s *queuedUserMessageStore) HasPendingSteers() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, pending := range s.items {
		if pending.steerAdmission != nil && pending.claimID == nil {
			return true
		}
	}
	return false
}

func (s *queuedUserMessageStore) Snapshot() []QueuedUserMessage {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]QueuedUserMessage, 0, len(s.items))
	for _, pending := range s.items {
		out = append(out, pending.message)
	}
	return out
}

func (s *queuedUserMessageStore) EntrySnapshot() []queuedUserMessage {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]queuedUserMessage, 0, len(s.items))
	for _, pending := range s.items {
		out = append(out, queuedUserMessage{
			message:        pending.message,
			steerAdmission: clonePendingWorkSteerAdmission(pending.steerAdmission),
		})
	}
	return out
}

func clonePendingWorkSteerAdmission(value *pendingWorkSteerAdmission) *pendingWorkSteerAdmission {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
