package worktree

import (
	"context"
	"errors"
	"io"
	"sync"

	"core/shared/serverapi"
)

type setupEventBroker struct {
	mu          sync.Mutex
	subscribers map[serverapi.WorktreeSetupOperationID]map[*setupSubscription]struct{}
}

type setupSubscription struct {
	broker *setupEventBroker
	id     serverapi.WorktreeSetupOperationID
	events chan serverapi.WorktreeSetupEvent
	mu     sync.Mutex
	closed bool
}

func newSetupEventBroker() *setupEventBroker {
	return &setupEventBroker{subscribers: make(map[serverapi.WorktreeSetupOperationID]map[*setupSubscription]struct{})}
}

func (b *setupEventBroker) Subscribe(req serverapi.WorktreeSetupSubscribeRequest) (serverapi.WorktreeSetupSubscription, error) {
	if b == nil {
		return nil, errors.New("worktree setup broker is required")
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	id := req.SetupOperationID
	sub := &setupSubscription{broker: b, id: id, events: make(chan serverapi.WorktreeSetupEvent, 16)}
	b.mu.Lock()
	if b.subscribers[id] == nil {
		b.subscribers[id] = make(map[*setupSubscription]struct{})
	}
	b.subscribers[id][sub] = struct{}{}
	b.mu.Unlock()
	return sub, nil
}

func (b *setupEventBroker) Publish(evt serverapi.WorktreeSetupEvent) {
	if b == nil || evt.SetupOperationID.Validate() != nil {
		return
	}
	id := evt.SetupOperationID
	b.mu.Lock()
	subscribers := make([]*setupSubscription, 0, len(b.subscribers[id]))
	for sub := range b.subscribers[id] {
		subscribers = append(subscribers, sub)
	}
	if evt.Phase == serverapi.WorktreeSetupPhaseCompleted || evt.Phase == serverapi.WorktreeSetupPhaseFailed {
		delete(b.subscribers, id)
	}
	b.mu.Unlock()
	for _, sub := range subscribers {
		sub.publish(evt)
		if evt.Phase == serverapi.WorktreeSetupPhaseCompleted || evt.Phase == serverapi.WorktreeSetupPhaseFailed {
			sub.Close()
		}
	}
}

func (s *setupSubscription) Next(ctx context.Context) (serverapi.WorktreeSetupEvent, error) {
	if s == nil {
		return serverapi.WorktreeSetupEvent{}, io.EOF
	}
	select {
	case <-ctx.Done():
		return serverapi.WorktreeSetupEvent{}, ctx.Err()
	case evt, ok := <-s.events:
		if !ok {
			return serverapi.WorktreeSetupEvent{}, io.EOF
		}
		return evt, nil
	}
}

func (s *setupSubscription) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	alreadyClosed := s.closed
	if !s.closed {
		s.closed = true
		close(s.events)
	}
	s.mu.Unlock()
	if !alreadyClosed {
		s.removeFromBroker()
	}
	return nil
}

func (s *setupSubscription) publish(evt serverapi.WorktreeSetupEvent) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.events <- evt:
	default:
		s.closed = true
		close(s.events)
		s.removeFromBroker()
	}
}

func (s *setupSubscription) removeFromBroker() {
	if s == nil || s.broker == nil {
		return
	}
	s.broker.mu.Lock()
	if subscribers := s.broker.subscribers[s.id]; subscribers != nil {
		delete(subscribers, s)
		if len(subscribers) == 0 {
			delete(s.broker.subscribers, s.id)
		}
	}
	s.broker.mu.Unlock()
}

func (s *Service) SubscribeWorktreeSetup(_ context.Context, req serverapi.WorktreeSetupSubscribeRequest) (serverapi.WorktreeSetupSubscription, error) {
	if s == nil || s.setupBroker == nil {
		return nil, errors.New("worktree setup broker is required")
	}
	return s.setupBroker.Subscribe(req)
}
