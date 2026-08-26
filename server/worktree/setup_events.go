package worktree

import (
	"context"
	"core/shared/worktreecontract"
	"errors"
	"fmt"
	"io"
	"sync"
)

type setupEventBroker struct {
	mu          sync.Mutex
	subscribers map[worktreecontract.SetupOperationID]map[*setupSubscription]struct{}
}

type setupSubscription struct {
	broker *setupEventBroker
	id     worktreecontract.SetupOperationID
	events chan worktreecontract.SetupEvent
	mu     sync.Mutex
	closed bool
}

func newSetupEventBroker() *setupEventBroker {
	return &setupEventBroker{subscribers: make(map[worktreecontract.SetupOperationID]map[*setupSubscription]struct{})}
}

func (b *setupEventBroker) Subscribe(req worktreecontract.SetupSubscribeRequest) (worktreecontract.SetupSubscription, error) {
	if b == nil {
		return nil, errors.New("worktree setup broker is required")
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	id := req.SetupOperationID
	sub := &setupSubscription{broker: b, id: id, events: make(chan worktreecontract.SetupEvent, 16)}
	b.mu.Lock()
	if b.subscribers[id] == nil {
		b.subscribers[id] = make(map[*setupSubscription]struct{})
	}
	b.subscribers[id][sub] = struct{}{}
	b.mu.Unlock()
	return sub, nil
}

func (b *setupEventBroker) Publish(evt worktreecontract.SetupEvent) {
	if b == nil {
		return
	}
	if err := evt.Validate(); err != nil {
		panic(fmt.Sprintf("publish invalid worktree setup event: %v; event=%+v", err, evt))
	}
	id := evt.SetupOperationID
	b.mu.Lock()
	subscribers := make([]*setupSubscription, 0, len(b.subscribers[id]))
	for sub := range b.subscribers[id] {
		subscribers = append(subscribers, sub)
	}
	if evt.Phase == worktreecontract.SetupPhaseCompleted ||
		evt.Phase == worktreecontract.SetupPhaseNotRequired ||
		evt.Phase == worktreecontract.SetupPhaseFailed {
		delete(b.subscribers, id)
	}
	b.mu.Unlock()
	for _, sub := range subscribers {
		sub.publish(evt)
		if evt.Phase == worktreecontract.SetupPhaseCompleted ||
			evt.Phase == worktreecontract.SetupPhaseNotRequired ||
			evt.Phase == worktreecontract.SetupPhaseFailed {
			sub.Close()
		}
	}
}

func (s *setupSubscription) Next(ctx context.Context) (worktreecontract.SetupEvent, error) {
	if s == nil {
		return worktreecontract.SetupEvent{}, io.EOF
	}
	select {
	case <-ctx.Done():
		return worktreecontract.SetupEvent{}, ctx.Err()
	case evt, ok := <-s.events:
		if !ok {
			return worktreecontract.SetupEvent{}, io.EOF
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

func (s *setupSubscription) publish(evt worktreecontract.SetupEvent) {
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

func (s *Service) SubscribeWorktreeSetup(_ context.Context, req worktreecontract.SetupSubscribeRequest) (worktreecontract.SetupSubscription, error) {
	if s == nil || s.setupBroker == nil {
		return nil, errors.New("worktree setup broker is required")
	}
	return s.setupBroker.Subscribe(req)
}
