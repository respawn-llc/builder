package runtime

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"core/server/llm"
)

type reviewerRuntimeState struct {
	mu     sync.Mutex
	client llm.Client
	active *TranscriptReviewerState
}

func newReviewerRuntimeState(client llm.Client) *reviewerRuntimeState {
	return &reviewerRuntimeState{client: client}
}

func (s *reviewerRuntimeState) SetActiveStep(stepID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	normalized := strings.TrimSpace(stepID)
	if normalized == "" {
		s.mu.Unlock()
		panic("reviewer active step id is required")
	}
	s.active = &TranscriptReviewerState{StepID: normalized}
	s.mu.Unlock()
}

func (s *reviewerRuntimeState) ClearActiveStep(stepID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.active != nil && s.active.StepID == strings.TrimSpace(stepID) {
		s.active = nil
	}
	s.mu.Unlock()
}

func (s *reviewerRuntimeState) ActiveStepSnapshot() *TranscriptReviewerState {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil {
		return nil
	}
	state := *s.active
	return &state
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
