package attentionnotify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"core/shared/clientui"
	"core/shared/serverapi"
)

const defaultBufferSize = 64

type RoutingKind string

const (
	RoutingWorkflowTask  RoutingKind = "workflow_task"
	RoutingSessionPrompt RoutingKind = "session_prompt"
)

type RoutingScope struct {
	Kind       RoutingKind
	ProjectID  string
	WorkflowID string
	TaskID     string
	SessionID  string
}

type Broker struct {
	mu          sync.Mutex
	nextID      uint64
	nextSeq     uint64
	bufferSize  int
	closed      bool
	subscribers map[uint64]*Subscription
}

type Subscription struct {
	filter  deliveryFilter
	ch      chan clientui.AttentionNotificationEvent
	onClose func()

	mu   sync.Mutex
	err  error
	done bool
}

type deliveryFilter struct {
	desktopRoot bool
	sessionID   string
}

type Option func(*Broker)

func WithBufferSize(size int) Option {
	return func(b *Broker) {
		if size > 0 {
			b.bufferSize = size
		}
	}
}

func NewBroker(options ...Option) *Broker {
	broker := &Broker{
		bufferSize:  defaultBufferSize,
		subscribers: map[uint64]*Subscription{},
	}
	for _, option := range options {
		if option != nil {
			option(broker)
		}
	}
	return broker
}

func (b *Broker) SubscribeDesktop() (*Subscription, error) {
	return b.subscribe(deliveryFilter{desktopRoot: true})
}

func (b *Broker) SubscribeSession(sessionID string) (*Subscription, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("attention notification session subscription: %w", serverapi.ErrSessionIDRequired)
	}
	return b.subscribe(deliveryFilter{sessionID: sessionID})
}

func (b *Broker) subscribe(filter deliveryFilter) (*Subscription, error) {
	if b == nil {
		return nil, fmt.Errorf("attention notification stream is unavailable: %w", serverapi.ErrStreamUnavailable)
	}
	sub := &Subscription{filter: filter, ch: make(chan clientui.AttentionNotificationEvent, b.bufferSize)}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		sub.closeWithError(io.EOF)
		return sub, nil
	}
	id := b.nextID
	b.nextID++
	b.subscribers[id] = sub
	b.mu.Unlock()
	sub.onClose = func() {
		b.mu.Lock()
		delete(b.subscribers, id)
		b.mu.Unlock()
	}
	return sub, nil
}

func (b *Broker) EnqueueInitial(sub *Subscription, scope RoutingScope, event clientui.AttentionNotificationEvent) error {
	if b == nil || sub == nil {
		return fmt.Errorf("attention notification stream is unavailable: %w", serverapi.ErrStreamUnavailable)
	}
	if !deliveryMatches(sub.filter, scope) {
		return nil
	}
	if err := serverapi.ValidateAttentionNotificationEvent(withSequenceForValidation(event)); err != nil {
		return err
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return io.EOF
	}
	b.nextSeq++
	event.Sequence = b.nextSeq
	b.mu.Unlock()
	if !sub.publish(event) {
		sub.closeWithError(serverapi.ErrStreamGap)
		return serverapi.ErrStreamGap
	}
	return nil
}

func (b *Broker) PublishPending(scope RoutingScope, notification clientui.AttentionNotification) error {
	event := clientui.AttentionNotificationEvent{
		Source:  clientui.AttentionNotificationSourceLive,
		Type:    clientui.AttentionNotificationEventPending,
		Pending: &notification,
	}
	if err := serverapi.ValidateAttentionNotificationEvent(withSequenceForValidation(event)); err != nil {
		return err
	}
	return b.publish(scope, event)
}

func (b *Broker) PublishResolved(scope RoutingScope, id clientui.AttentionNotificationID, kind clientui.AttentionNotificationKind, occurredAt time.Time) error {
	resolvedID := id
	resolvedAt := occurredAt
	event := clientui.AttentionNotificationEvent{
		Source:     clientui.AttentionNotificationSourceLive,
		Type:       clientui.AttentionNotificationEventResolved,
		ID:         &resolvedID,
		Kind:       kind,
		OccurredAt: &resolvedAt,
	}
	if err := serverapi.ValidateAttentionNotificationEvent(withSequenceForValidation(event)); err != nil {
		return err
	}
	return b.publish(scope, event)
}

func withSequenceForValidation(event clientui.AttentionNotificationEvent) clientui.AttentionNotificationEvent {
	event.Sequence = 1
	return event
}

func (b *Broker) publish(scope RoutingScope, event clientui.AttentionNotificationEvent) error {
	if b == nil {
		return fmt.Errorf("attention notification stream is unavailable: %w", serverapi.ErrStreamUnavailable)
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return io.EOF
	}
	b.nextSeq++
	event.Sequence = b.nextSeq
	subs := make([]*Subscription, 0, len(b.subscribers))
	for _, sub := range b.subscribers {
		if deliveryMatches(sub.filter, scope) {
			subs = append(subs, sub)
		}
	}
	b.mu.Unlock()
	for _, sub := range subs {
		if !sub.publish(event) {
			sub.closeWithError(serverapi.ErrStreamGap)
		}
	}
	return nil
}

func (b *Broker) Close(err error) {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	subs := make([]*Subscription, 0, len(b.subscribers))
	for id, sub := range b.subscribers {
		subs = append(subs, sub)
		delete(b.subscribers, id)
	}
	b.mu.Unlock()
	for _, sub := range subs {
		sub.closeWithError(err)
	}
}

func deliveryMatches(filter deliveryFilter, scope RoutingScope) bool {
	if filter.desktopRoot {
		return scope.Kind == RoutingWorkflowTask
	}
	if filter.sessionID == "" {
		return false
	}
	switch scope.Kind {
	case RoutingSessionPrompt:
		return scope.SessionID == filter.sessionID
	case RoutingWorkflowTask:
		return scope.SessionID == filter.sessionID
	default:
		return false
	}
}

func (s *Subscription) publish(event clientui.AttentionNotificationEvent) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return false
	}
	select {
	case s.ch <- event:
		return true
	default:
		return false
	}
}

func (s *Subscription) Next(ctx context.Context) (clientui.AttentionNotificationEvent, error) {
	if s == nil {
		return clientui.AttentionNotificationEvent{}, io.EOF
	}
	select {
	case <-ctx.Done():
		return clientui.AttentionNotificationEvent{}, ctx.Err()
	case event, ok := <-s.ch:
		if ok {
			return event, nil
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.err != nil {
			return clientui.AttentionNotificationEvent{}, serverapi.NormalizeStreamError(s.err)
		}
		return clientui.AttentionNotificationEvent{}, io.EOF
	}
}

func (s *Subscription) Close() error {
	if s == nil {
		return nil
	}
	s.closeWithError(io.EOF)
	return nil
}

func (s *Subscription) closeWithError(err error) {
	if s == nil {
		return
	}
	var onClose func()
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return
	}
	s.done = true
	s.err = err
	close(s.ch)
	onClose = s.onClose
	s.mu.Unlock()
	if onClose != nil {
		onClose()
	}
}

var _ serverapi.AttentionNotificationSubscription = (*Subscription)(nil)

var ErrBatchNotFound = errors.New("question batch is not registered")
