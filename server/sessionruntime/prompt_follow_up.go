package sessionruntime

import (
	"context"
	"errors"
	"io"
	"sync"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type promptFollowUpState struct {
	descriptor *validatedQuestionBatchDescriptor
	resolved   bool
	watchers   map[*promptFollowUpSubscription]struct{}
}

type promptFollowUpKey struct {
	sessionID runtimeids.SessionID
	stepID    runtimeids.StepID
	promptID  clientui.PromptID
}

type promptFollowUpSubscription struct {
	mu       sync.Mutex
	events   chan serverapi.PromptFollowUpEvent
	canceled chan struct{}
	closed   bool
	onClose  func()
}

func (a *Authority) SubscribePromptFollowUp(
	_ context.Context,
	sessionID runtimeids.SessionID,
	stepID runtimeids.StepID,
	promptID clientui.PromptID,
) (serverapi.PromptFollowUpSubscription, error) {
	if a == nil {
		return nil, errors.New("session runtime authority is required")
	}
	execution := a.sessionExecution(sessionID)
	if execution == nil {
		return nil, serverapi.ErrPromptNotFound
	}
	return execution.prompts.subscribePromptFollowUp(stepID, promptID)
}

func (s *executionPromptStore) subscribePromptFollowUp(
	stepID runtimeids.StepID,
	promptID clientui.PromptID,
) (serverapi.PromptFollowUpSubscription, error) {
	if s == nil || s.authority == nil {
		return nil, errors.New("session runtime authority is required")
	}
	if stepID.IsZero() {
		return nil, errors.New("step id is required")
	}
	if err := promptID.Validate(); err != nil {
		return nil, err
	}
	key, err := s.promptFollowUpKey(stepID, promptID)
	if err != nil {
		return nil, err
	}
	rawPromptID := string(key.promptID)
	s.mu.Lock()
	state := s.promptFollowUps[key]
	if state == nil {
		entry := s.pending[rawPromptID]
		if entry == nil || entry.snapshot.Request.StepID != key.stepID.String() {
			s.mu.Unlock()
			return nil, serverapi.ErrPromptNotFound
		}
		descriptor, err := validateQuestionBatchMetadata(entry.snapshot.Request)
		if err != nil {
			s.mu.Unlock()
			return nil, err
		}
		subscription := s.registerPromptFollowUpLocked(key, descriptor)
		s.mu.Unlock()
		return subscription, nil
	}
	subscription := s.registerPromptFollowUpLocked(key, state.descriptor)
	s.mu.Unlock()
	return subscription, nil
}

func (s *executionPromptStore) promptFollowUpKey(
	stepID runtimeids.StepID,
	promptID clientui.PromptID,
) (promptFollowUpKey, error) {
	resource, ok := s.scope.Resource()
	if !ok {
		return promptFollowUpKey{}, errors.New("prompt follow-up requires an agent Session scope")
	}
	return promptFollowUpKey{
		sessionID: resource.SessionID(),
		stepID:    stepID,
		promptID:  promptID,
	}, nil
}

func (s *executionPromptStore) registerPromptFollowUpLocked(
	key promptFollowUpKey,
	descriptor *validatedQuestionBatchDescriptor,
) *promptFollowUpSubscription {
	state := s.promptFollowUps[key]
	if state == nil {
		state = &promptFollowUpState{
			descriptor: descriptor,
			watchers:   make(map[*promptFollowUpSubscription]struct{}),
		}
		if s.promptFollowUps == nil {
			s.promptFollowUps = make(map[promptFollowUpKey]*promptFollowUpState)
		}
		s.promptFollowUps[key] = state
	}
	subscription := &promptFollowUpSubscription{
		events:   make(chan serverapi.PromptFollowUpEvent, 1),
		canceled: make(chan struct{}),
	}
	subscription.onClose = func() {
		s.mu.Lock()
		delete(state.watchers, subscription)
		s.mu.Unlock()
	}
	state.watchers[subscription] = struct{}{}
	return subscription
}

func (s *executionPromptStore) resolvePromptFollowUpLocked(
	stepID runtimeids.StepID,
	promptID clientui.PromptID,
	descriptor *validatedQuestionBatchDescriptor,
) {
	if len(s.promptFollowUps) == 0 {
		return
	}
	key, err := s.promptFollowUpKey(stepID, promptID)
	if err != nil {
		return
	}
	state := s.promptFollowUps[key]
	if state == nil {
		return
	}
	state.descriptor = descriptor
	state.resolved = true
	successorIDs := descriptor.successorPromptIDs()
	if len(successorIDs) == 0 {
		s.broadcastPromptFollowUpLocked(key, serverapi.PromptFollowUpNoPreparedSuccessor)
		return
	}
	for _, successorID := range successorIDs {
		if _, pending := s.pending[successorID]; pending {
			s.broadcastPromptFollowUpLocked(key, serverapi.PromptFollowUpSuccessorReady)
			return
		}
	}
}

func (s *executionPromptStore) observePromptFollowUpsLocked(rawStepID string, successorID string) {
	stepID, err := runtimeids.ParseStepID(rawStepID)
	if err != nil {
		return
	}
	keys := make([]promptFollowUpKey, 0)
	for key, state := range s.promptFollowUps {
		if !state.resolved || key.stepID != stepID {
			continue
		}
		for _, expectedID := range state.descriptor.successorPromptIDs() {
			if expectedID == successorID {
				keys = append(keys, key)
				break
			}
		}
	}
	for _, key := range keys {
		s.broadcastPromptFollowUpLocked(key, serverapi.PromptFollowUpSuccessorReady)
	}
}

func (s *executionPromptStore) closePromptFollowUpsLocked() {
	keys := make([]promptFollowUpKey, 0, len(s.promptFollowUps))
	for key := range s.promptFollowUps {
		keys = append(keys, key)
	}
	for _, key := range keys {
		s.broadcastPromptFollowUpLocked(key, serverapi.PromptFollowUpExecutionClosed)
	}
}

func (s *executionPromptStore) broadcastPromptFollowUpLocked(
	key promptFollowUpKey,
	kind serverapi.PromptFollowUpEventKind,
) {
	state := s.promptFollowUps[key]
	if state == nil {
		return
	}
	delete(s.promptFollowUps, key)
	event := serverapi.PromptFollowUpEvent{Kind: kind}
	for watcher := range state.watchers {
		watcher.publish(event)
	}
}

func (s *promptFollowUpSubscription) publish(event serverapi.PromptFollowUpEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.events <- event
	close(s.events)
}

func (s *promptFollowUpSubscription) Next(ctx context.Context) (serverapi.PromptFollowUpEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case event, ok := <-s.events:
		if !ok {
			return serverapi.PromptFollowUpEvent{}, io.EOF
		}
		return event, nil
	case <-s.canceled:
		return serverapi.PromptFollowUpEvent{}, io.EOF
	case <-ctx.Done():
		return serverapi.PromptFollowUpEvent{}, context.Cause(ctx)
	}
}

func (s *promptFollowUpSubscription) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.canceled)
	onClose := s.onClose
	s.mu.Unlock()
	if onClose != nil {
		onClose()
	}
	return nil
}
