package runtime

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"core/server/llm"
	"core/shared/runtimeids"
)

type reviewerRuntimeState struct {
	mu     sync.Mutex
	client llm.Client
	active *runtimeids.StepID
}

func newReviewerRuntimeState(client llm.Client) *reviewerRuntimeState {
	return &reviewerRuntimeState{client: client}
}

func (s *reviewerRuntimeState) TryStart(stepID string) bool {
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
	return true
}

func (s *reviewerRuntimeState) Complete(stepID string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil && s.active.String() == strings.TrimSpace(stepID) {
		s.active = nil
		return true
	}
	return false
}

func (s *reviewerRuntimeState) Running() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active != nil
}

func (e *Engine) ReviewerRunning() bool {
	return e != nil && e.reviewerRuntimeState().Running()
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
	if !e.reviewerRuntimeState().TryStart(stepID) {
		return false, nil
	}
	revision, err := e.TranscriptRevision()
	if err != nil {
		e.reviewerRuntimeState().Complete(stepID)
		return false, err
	}
	err = e.emitRawAtRevision(Event{
		Kind: EventRuntimeActivityChanged,
	}, revision)
	if err != nil {
		e.reviewerRuntimeState().Complete(stepID)
		return false, err
	}
	return true, nil
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
	if !e.reviewerRuntimeState().Complete(stepID) {
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

func (s *reviewerRuntimeState) Client() llm.Client {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client
}

func (s *reviewerRuntimeState) EnsureClient(factory func() (llm.Client, error)) error {
	if s == nil {
		return errors.New("reviewer state is not configured")
	}
	s.mu.Lock()
	if s.client != nil {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	if factory == nil {
		return errors.New("reviewer client is not configured")
	}
	client, err := factory()
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
