package registry

import (
	"context"
	"fmt"
	"io"
	"sync"

	"core/shared/clientui"
	"core/shared/serverapi"
)

type pendingPromptEventType = clientui.PendingPromptEventType

const (
	pendingPromptEventPending  = clientui.PendingPromptEventPending
	pendingPromptEventResolved = clientui.PendingPromptEventResolved
)

type promptActivityBroker struct {
	mu          sync.Mutex
	nextID      uint64
	nextSeq     uint64
	history     []clientui.PendingPromptEvent
	closed      bool
	subscribers map[uint64]*promptActivitySubscription
}

type promptActivitySubscription struct {
	ch      chan clientui.PendingPromptEvent
	onClose func()

	mu      sync.Mutex
	initial []clientui.PendingPromptEvent
	err     error
	done    bool
}

func newPromptActivityBroker() *promptActivityBroker {
	return &promptActivityBroker{subscribers: make(map[uint64]*promptActivitySubscription)}
}

func (e *runtimeEntry) SubscribePromptActivityInitial(sessionID string, beforeSubscribe func()) (*promptActivitySubscription, error) {
	if e == nil || e.promptActivity == nil {
		return nil, fmt.Errorf("prompt activity stream is unavailable: %w", serverapi.ErrStreamUnavailable)
	}
	return e.pendingPrompts.WithLockedSnapshotResult(func(items []PendingPromptSnapshot) (*promptActivitySubscription, error) {
		initial := make([]clientui.PendingPromptEvent, 0, len(items)+1)
		for _, item := range items {
			initial = append(initial, pendingPromptEventFromSnapshot(sessionID, item, pendingPromptEventPending))
		}
		initial = append(initial, clientui.PendingPromptEvent{Type: clientui.PendingPromptEventSnapshot, SessionID: sessionID})
		if beforeSubscribe != nil {
			beforeSubscribe()
		}
		return e.promptActivity.Subscribe(initial, 0)
	})
}

func (e *runtimeEntry) PublishPendingPrompt(sessionID string, snapshot PendingPromptSnapshot, eventType pendingPromptEventType) {
	if e == nil || e.promptActivity == nil || snapshot.Request.ID == "" {
		return
	}
	e.promptActivity.Publish(pendingPromptEventFromSnapshot(sessionID, snapshot, eventType))
}

func (b *promptActivityBroker) Subscribe(initial []clientui.PendingPromptEvent, afterSequence uint64) (*promptActivitySubscription, error) {
	if b == nil {
		return nil, fmt.Errorf("prompt activity stream is unavailable: %w", serverapi.ErrStreamUnavailable)
	}
	sub := &promptActivitySubscription{ch: make(chan clientui.PendingPromptEvent, promptActivityBufferSize), initial: append([]clientui.PendingPromptEvent(nil), initial...)}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		sub.closeWithError(io.EOF)
		return sub, nil
	}
	if afterSequence > 0 && !b.canReplayLocked(afterSequence) {
		b.mu.Unlock()
		return nil, fmt.Errorf("prompt activity cursor %d is outside retained range: %w", afterSequence, serverapi.ErrStreamGap)
	}
	if afterSequence > 0 {
		for _, evt := range b.replayAfterLocked(afterSequence) {
			if !sub.publish(evt) {
				b.mu.Unlock()
				sub.closeWithError(serverapi.ErrStreamGap)
				return sub, nil
			}
		}
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

func (b *promptActivityBroker) Publish(evt clientui.PendingPromptEvent) {
	if b == nil || evt.IsZero() {
		return
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.nextSeq++
	evt.Sequence = b.nextSeq
	b.history = append(b.history, evt)
	if len(b.history) > promptActivityBufferSize {
		copy(b.history, b.history[len(b.history)-promptActivityBufferSize:])
		b.history = b.history[:promptActivityBufferSize]
	}
	subs := make([]*promptActivitySubscription, 0, len(b.subscribers))
	for _, sub := range b.subscribers {
		subs = append(subs, sub)
	}
	b.mu.Unlock()
	for _, sub := range subs {
		if !sub.publish(evt) {
			sub.closeWithError(serverapi.ErrStreamGap)
		}
	}
}

func (b *promptActivityBroker) SubscriberCount() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subscribers)
}

func (b *promptActivityBroker) canReplayLocked(afterSequence uint64) bool {
	if afterSequence == 0 || afterSequence == b.nextSeq {
		return true
	}
	if afterSequence > b.nextSeq {
		return false
	}
	if len(b.history) == 0 {
		return false
	}
	return afterSequence >= b.history[0].Sequence-1
}

func (b *promptActivityBroker) replayAfterLocked(afterSequence uint64) []clientui.PendingPromptEvent {
	if afterSequence == 0 || len(b.history) == 0 {
		return nil
	}
	replay := make([]clientui.PendingPromptEvent, 0)
	for _, evt := range b.history {
		if evt.Sequence > afterSequence {
			replay = append(replay, evt)
		}
	}
	return replay
}

func (b *promptActivityBroker) Close(err error) {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	subs := make([]*promptActivitySubscription, 0, len(b.subscribers))
	for id, sub := range b.subscribers {
		subs = append(subs, sub)
		delete(b.subscribers, id)
	}
	b.mu.Unlock()
	for _, sub := range subs {
		sub.closeWithError(err)
	}
}

func (s *promptActivitySubscription) publish(evt clientui.PendingPromptEvent) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return false
	}
	select {
	case s.ch <- evt:
		return true
	default:
		return false
	}
}

func (s *promptActivitySubscription) Next(ctx context.Context) (clientui.PendingPromptEvent, error) {
	if s == nil {
		return clientui.PendingPromptEvent{}, io.EOF
	}
	s.mu.Lock()
	// Close must win over initial replay; closed streams should not emit stale
	// snapshot events after the subscriber has been torn down.
	if s.done {
		err := s.err
		s.mu.Unlock()
		if err != nil {
			return clientui.PendingPromptEvent{}, serverapi.NormalizeStreamError(err)
		}
		return clientui.PendingPromptEvent{}, io.EOF
	}
	if len(s.initial) > 0 {
		evt := s.initial[0]
		s.initial = s.initial[1:]
		s.mu.Unlock()
		return evt, nil
	}
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return clientui.PendingPromptEvent{}, ctx.Err()
	case evt, ok := <-s.ch:
		if ok {
			return evt, nil
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.err != nil {
			return clientui.PendingPromptEvent{}, serverapi.NormalizeStreamError(s.err)
		}
		return clientui.PendingPromptEvent{}, io.EOF
	}
}

func (s *promptActivitySubscription) Close() error {
	if s == nil {
		return nil
	}
	s.closeWithError(io.EOF)
	return nil
}

func (s *promptActivitySubscription) closeWithError(err error) {
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

func pendingPromptEventFromSnapshot(sessionID string, snapshot PendingPromptSnapshot, eventType pendingPromptEventType) clientui.PendingPromptEvent {
	evt := clientui.PendingPromptEvent{
		Type:                   eventType,
		PromptID:               snapshot.Request.ID,
		SessionID:              sessionID,
		Question:               snapshot.Request.Question,
		Suggestions:            append([]string(nil), snapshot.Request.Suggestions...),
		RecommendedOptionIndex: snapshot.Request.RecommendedOptionIndex,
		Approval:               snapshot.Request.Approval,
		CreatedAt:              snapshot.CreatedAt,
	}
	if len(snapshot.Request.ApprovalOptions) > 0 {
		evt.ApprovalOptions = make([]clientui.ApprovalOption, 0, len(snapshot.Request.ApprovalOptions))
		for _, option := range snapshot.Request.ApprovalOptions {
			evt.ApprovalOptions = append(evt.ApprovalOptions, clientui.ApprovalOption{Decision: clientui.ApprovalDecision(option.Decision), Label: option.Label})
		}
	}
	return evt
}

var _ serverapi.PromptActivitySubscription = (*promptActivitySubscription)(nil)
