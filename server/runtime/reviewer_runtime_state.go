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

func (s *reviewerRuntimeState) SetActiveStep(stepID string) {
	if s == nil {
		return
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
	s.active = &active
	s.mu.Unlock()
}

func (s *reviewerRuntimeState) ClearActiveStep(stepID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.active != nil && s.active.String() == strings.TrimSpace(stepID) {
		s.active = nil
	}
	s.mu.Unlock()
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
