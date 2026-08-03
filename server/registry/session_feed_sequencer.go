package registry

import (
	"fmt"
	"sync"

	"core/shared/clientui"
)

type sessionFeedSequencer struct {
	mu     sync.Mutex
	broker *transcriptSubscriptionBroker
}

func newSessionFeedSequencer(broker *transcriptSubscriptionBroker) *sessionFeedSequencer {
	return &sessionFeedSequencer{broker: broker}
}

func (s *sessionFeedSequencer) HasSubscribers() bool {
	return s != nil && s.broker != nil && s.broker.SubscriberCount() > 0
}

func (s *sessionFeedSequencer) Subscribe(
	build func() (clientui.TranscriptHydration, error),
) (*transcriptSubscription, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if build == nil {
		return nil, fmt.Errorf("transcript hydration builder is required")
	}
	hydration, err := build()
	if err != nil {
		return nil, err
	}
	event := clientui.NewTranscriptEvent(hydration)
	if err := event.Validate(); err != nil {
		return nil, fmt.Errorf("build canonical transcript hydration: %w", err)
	}
	return s.broker.Subscribe(event)
}

func (s *sessionFeedSequencer) Publish(events []clientui.TranscriptEvent) {
	if s == nil || len(events) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.validateEvents(events)
	s.broker.Publish(events)
}

func (s *sessionFeedSequencer) PublishBuilt(build func() ([]clientui.TranscriptEvent, error)) error {
	if s == nil {
		return nil
	}
	if build == nil {
		return fmt.Errorf("transcript event builder is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	events, err := build()
	if err != nil {
		return err
	}
	if len(events) == 0 {
		return nil
	}
	s.validateEvents(events)
	s.broker.Publish(events)
	return nil
}

func (s *sessionFeedSequencer) PublishRuntimeReadModel(update clientui.RuntimeReadModelUpdate) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publishRuntimeReadModelLocked(update)
}

func (s *sessionFeedSequencer) CloseWithRuntimeReadModel(update clientui.RuntimeReadModelUpdate, err error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publishRuntimeReadModelLocked(update)
	s.broker.Close(err)
}

func (s *sessionFeedSequencer) Close(err error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.broker.Close(err)
}

func (s *sessionFeedSequencer) publishRuntimeReadModelLocked(update clientui.RuntimeReadModelUpdate) {
	event := clientui.NewTranscriptEvent(update)
	if err := event.Validate(); err != nil {
		panic(fmt.Sprintf("publish invalid canonical runtime read-model update: %+v: %v", update, err))
	}
	s.broker.Publish([]clientui.TranscriptEvent{event})
}

func (s *sessionFeedSequencer) validateEvents(events []clientui.TranscriptEvent) {
	for _, event := range events {
		if err := event.Validate(); err != nil {
			panic(fmt.Sprintf("publish invalid canonical transcript event before batch mutation: %v", err))
		}
	}
}
