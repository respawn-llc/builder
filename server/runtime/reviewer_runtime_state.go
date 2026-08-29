package runtime

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"core/server/llm"
	"core/shared/clientui"
	"core/shared/runtimeids"
)

type reviewerRuntimeState struct {
	mu      sync.Mutex
	client  *observedModelClient
	factory func() (*observedModelClient, error)
	active  *runtimeids.StepID
	phase   clientui.ReviewerActivity
}

func newReviewerRuntimeState(client llm.Client, factory func() (llm.Client, error)) *reviewerRuntimeState {
	var observedFactory func() (*observedModelClient, error)
	if factory != nil {
		observedFactory = func() (*observedModelClient, error) {
			client, err := factory()
			if err != nil {
				return nil, err
			}
			return newObservedModelClient(client), nil
		}
	}
	return &reviewerRuntimeState{
		client:  newObservedModelClient(client),
		factory: observedFactory,
		phase:   clientui.ReviewerActivityInactive,
	}
}

func (s *reviewerRuntimeState) Reserve(stepID string) bool {
	if s == nil {
		return false
	}
	normalized := strings.TrimSpace(stepID)
	if normalized == "" {
		panic("reviewer active step id is required")
	}
	active, err := runtimeids.ParseStepID(normalized)
	if err != nil {
		panic(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil {
		return false
	}
	s.active = &active
	s.phase = clientui.ReviewerActivityInactive
	return true
}

func (s *reviewerRuntimeState) Start(stepID string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil ||
		s.active.String() != strings.TrimSpace(stepID) ||
		s.phase != clientui.ReviewerActivityInactive {
		return false
	}
	s.phase = clientui.ReviewerActivityInvoking
	return true
}

func (s *reviewerRuntimeState) SetAddressingFeedback(stepID string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil ||
		s.active.String() != strings.TrimSpace(stepID) ||
		s.phase != clientui.ReviewerActivityInvoking {
		return false
	}
	s.phase = clientui.ReviewerActivityAddressingFeedback
	return true
}

func (s *reviewerRuntimeState) Clear(stepID string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil && s.active.String() == strings.TrimSpace(stepID) {
		s.active = nil
		s.phase = clientui.ReviewerActivityInactive
		return true
	}
	return false
}

func (s *reviewerRuntimeState) Active() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active != nil
}

func (s *reviewerRuntimeState) Activity() clientui.ReviewerActivity {
	if s == nil {
		return clientui.ReviewerActivityInactive
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.phase
}

func (e *Engine) ReviewerActive() bool {
	return e != nil && e.reviewerRuntimeState().Active()
}

func (e *Engine) ReviewerActivity() clientui.ReviewerActivity {
	if e == nil {
		return clientui.ReviewerActivityInactive
	}
	return e.reviewerRuntimeState().Activity()
}

func (e *Engine) startReviewerActivity(stepID string) (bool, error) {
	if e == nil || e.closed.Load() {
		return false, ErrEngineClosed
	}
	e.outputMutationMu.Lock()
	defer e.outputMutationMu.Unlock()
	return e.startReviewerActivityRaw(stepID)
}

func (e *Engine) startReviewerActivityRaw(stepID string) (bool, error) {
	if !e.reviewerRuntimeState().Start(stepID) {
		return false, nil
	}
	revision, err := e.TranscriptRevision()
	if err != nil {
		e.reviewerRuntimeState().Clear(stepID)
		return false, err
	}
	err = e.emitRawAtRevision(Event{
		Kind: EventRuntimeActivityChanged,
	}, revision)
	if err != nil {
		e.reviewerRuntimeState().Clear(stepID)
		return false, err
	}
	return true, nil
}

func (e *Engine) reserveReviewerActivity(stepID string) bool {
	if e == nil || e.closed.Load() {
		return false
	}
	e.outputMutationMu.Lock()
	defer e.outputMutationMu.Unlock()
	return e.reviewerRuntimeState().Reserve(stepID)
}

func (e *Engine) releaseReviewerActivity(stepID string) {
	if e == nil {
		return
	}
	e.outputMutationMu.Lock()
	defer e.outputMutationMu.Unlock()
	e.reviewerRuntimeState().Clear(stepID)
}

func (e *Engine) completeReviewerActivity(stepID string) error {
	if e == nil {
		return nil
	}
	e.outputMutationMu.Lock()
	defer e.outputMutationMu.Unlock()
	return e.completeReviewerActivityRaw(stepID)
}

func (e *Engine) completeReviewerActivityRaw(stepID string) error {
	if !e.reviewerRuntimeState().Clear(stepID) {
		return nil
	}
	revision, err := e.TranscriptRevision()
	if err != nil {
		return err
	}
	return e.emitRawAtRevision(Event{
		Kind: EventRuntimeActivityChanged,
	}, revision)
}

func (e *Engine) setReviewerAddressingFeedback(stepID string) error {
	if e == nil {
		return nil
	}
	e.outputMutationMu.Lock()
	defer e.outputMutationMu.Unlock()
	if !e.reviewerRuntimeState().SetAddressingFeedback(stepID) {
		return nil
	}
	revision, err := e.TranscriptRevision()
	if err != nil {
		return err
	}
	return e.emitRawAtRevision(Event{
		Kind: EventRuntimeActivityChanged,
	}, revision)
}

func (s *reviewerRuntimeState) Client() *observedModelClient {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client
}

func (s *reviewerRuntimeState) EnsureClient() error {
	if s == nil {
		return errors.New("reviewer state is not configured")
	}
	s.mu.Lock()
	if s.client != nil {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	if s.factory == nil {
		return errors.New("reviewer client is not configured")
	}
	client, err := s.factory()
	if err != nil {
		return fmt.Errorf("configure reviewer client: %w", err)
	}
	s.mu.Lock()
	if s.client == nil {
		s.client = client
	}
	s.mu.Unlock()
	return nil
}
