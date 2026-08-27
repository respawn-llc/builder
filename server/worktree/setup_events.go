package worktree

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"core/shared/apicontract"
	"core/shared/protoapi"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/worktreecontract"
)

type SetupSubscription = apicontract.WorktreeSetupSubscription

type setupEventBroker struct {
	mu          sync.Mutex
	subscribers map[worktreecontract.SetupOperationID]map[*setupSubscription]struct{}
}

type setupSubscription struct {
	broker *setupEventBroker
	id     worktreecontract.SetupOperationID
	events chan *worktreepb.SetupEvent
	mu     sync.Mutex
	closed bool
}

func newSetupEventBroker() *setupEventBroker {
	return &setupEventBroker{
		subscribers: make(map[worktreecontract.SetupOperationID]map[*setupSubscription]struct{}),
	}
}

func (b *setupEventBroker) Subscribe(req *worktreepb.SetupSubscribeRequest) (SetupSubscription, error) {
	if b == nil {
		return nil, errors.New("worktree setup broker is required")
	}
	id, err := worktreecontract.ParseSetupOperationID(req.SetupOperationId)
	if err != nil {
		return nil, err
	}
	sub := &setupSubscription{
		broker: b,
		id:     id,
		events: make(chan *worktreepb.SetupEvent, 16),
	}
	b.mu.Lock()
	if b.subscribers[id] == nil {
		b.subscribers[id] = make(map[*setupSubscription]struct{})
	}
	b.subscribers[id][sub] = struct{}{}
	b.mu.Unlock()
	return sub, nil
}

func (b *setupEventBroker) Publish(evt *worktreepb.SetupEvent) {
	if b == nil {
		return
	}
	if err := protoapi.Validate(evt); err != nil {
		panic(fmt.Sprintf("publish invalid worktree setup event: %v; event=%+v", err, evt))
	}
	id, err := worktreecontract.ParseSetupOperationID(evt.SetupOperationId)
	if err != nil {
		panic(fmt.Sprintf("publish invalid worktree setup operation identity: %v", err))
	}
	terminal := setupEventTerminal(evt)
	b.mu.Lock()
	subscribers := make([]*setupSubscription, 0, len(b.subscribers[id]))
	for sub := range b.subscribers[id] {
		subscribers = append(subscribers, sub)
	}
	if terminal {
		delete(b.subscribers, id)
	}
	b.mu.Unlock()
	for _, sub := range subscribers {
		sub.publish(evt)
		if terminal {
			sub.Close()
		}
	}
}

func setupEventTerminal(evt *worktreepb.SetupEvent) bool {
	switch evt.GetPhase().(type) {
	case *worktreepb.SetupEvent_Completed,
		*worktreepb.SetupEvent_NotRequired,
		*worktreepb.SetupEvent_Failed:
		return true
	default:
		return false
	}
}

func (s *setupSubscription) Next(ctx context.Context) (*worktreepb.SetupEvent, error) {
	if s == nil {
		return nil, io.EOF
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case evt, ok := <-s.events:
		if !ok {
			return nil, io.EOF
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

func (s *setupSubscription) publish(evt *worktreepb.SetupEvent) {
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

func (s *Service) SubscribeWorktreeSetup(_ context.Context, req *worktreepb.SetupSubscribeRequest) (SetupSubscription, error) {
	if s == nil || s.setupBroker == nil {
		return nil, errors.New("worktree setup broker is required")
	}
	return s.setupBroker.Subscribe(req)
}
