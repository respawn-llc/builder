package workflowsvc

import (
	"context"
	"io"
	"sync"
	"time"

	"core/server/workflowstore"
	"core/shared/serverapi"
)

const workflowProjectEventBufferSize = 64

type workflowProjectEventBroker struct {
	mu          sync.Mutex
	nextID      uint64
	closed      bool
	subscribers map[uint64]*workflowProjectSubscription
}

type workflowProjectSubscription struct {
	projectID  string
	workflowID string
	ch         chan serverapi.WorkflowProjectEvent
	onClose    func()

	mu   sync.Mutex
	err  error
	done bool
}

func newWorkflowProjectEventBroker() *workflowProjectEventBroker {
	return &workflowProjectEventBroker{subscribers: make(map[uint64]*workflowProjectSubscription)}
}

func (b *workflowProjectEventBroker) subscribe(projectID string, workflowID string) (*workflowProjectSubscription, error) {
	sub := &workflowProjectSubscription{
		projectID:  projectID,
		workflowID: workflowID,
		ch:         make(chan serverapi.WorkflowProjectEvent, workflowProjectEventBufferSize),
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		sub.closeWithError(io.EOF)
		return sub, nil
	}
	id := b.nextID
	b.nextID++
	sub.onClose = func() {
		b.mu.Lock()
		delete(b.subscribers, id)
		b.mu.Unlock()
	}
	b.subscribers[id] = sub
	b.mu.Unlock()
	return sub, nil
}

func (b *workflowProjectEventBroker) PublishWorkflowEvent(_ context.Context, event workflowstore.WorkflowEventRecord) error {
	if b == nil {
		return nil
	}
	occurredAt := event.OccurredAtUnixMs
	if occurredAt == 0 {
		occurredAt = time.Now().UTC().UnixMilli()
	}
	b.publish(serverapi.WorkflowProjectEvent{
		ProjectID:        event.ProjectID,
		WorkflowID:       event.WorkflowID,
		Resource:         event.Resource,
		Action:           event.Action,
		PrimaryEntityID:  event.PrimaryEntityID,
		RelatedIDs:       append([]string(nil), event.RelatedIDs...),
		OccurredAtUnixMs: occurredAt,
	})
	return nil
}

func (b *workflowProjectEventBroker) publish(event serverapi.WorkflowProjectEvent) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	subs := make([]*workflowProjectSubscription, 0, len(b.subscribers))
	for _, sub := range b.subscribers {
		if workflowSubscriptionMatches(sub, event) {
			subs = append(subs, sub)
		}
	}
	b.mu.Unlock()
	for _, sub := range subs {
		if !sub.publish(event) {
			sub.closeWithError(serverapi.ErrStreamGap)
		}
	}
}

func workflowProjectEventMatches(subscribedProjectID string, eventProjectID *string) bool {
	return subscribedProjectID == "" || (eventProjectID != nil && subscribedProjectID == *eventProjectID)
}

func workflowSubscriptionMatches(sub *workflowProjectSubscription, event serverapi.WorkflowProjectEvent) bool {
	if sub.workflowID != "" {
		return event.ProjectID == nil && event.WorkflowID != nil && sub.workflowID == *event.WorkflowID
	}
	return workflowProjectEventMatches(sub.projectID, event.ProjectID)
}

func (b *workflowProjectEventBroker) Close(err error) {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	subs := make([]*workflowProjectSubscription, 0, len(b.subscribers))
	for id, sub := range b.subscribers {
		subs = append(subs, sub)
		delete(b.subscribers, id)
	}
	b.mu.Unlock()
	for _, sub := range subs {
		sub.closeWithError(err)
	}
}

func (s *workflowProjectSubscription) publish(event serverapi.WorkflowProjectEvent) bool {
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

func (s *workflowProjectSubscription) Next(ctx context.Context) (serverapi.WorkflowProjectEvent, error) {
	if s == nil {
		return serverapi.WorkflowProjectEvent{}, io.EOF
	}
	select {
	case <-ctx.Done():
		return serverapi.WorkflowProjectEvent{}, ctx.Err()
	case event, ok := <-s.ch:
		if ok {
			return event, nil
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.err != nil {
			return serverapi.WorkflowProjectEvent{}, serverapi.NormalizeStreamError(s.err)
		}
		return serverapi.WorkflowProjectEvent{}, io.EOF
	}
}

func (s *workflowProjectSubscription) Close() error {
	if s == nil {
		return nil
	}
	s.closeWithError(io.EOF)
	return nil
}

func (s *workflowProjectSubscription) closeWithError(err error) {
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

var _ serverapi.WorkflowProjectSubscription = (*workflowProjectSubscription)(nil)
