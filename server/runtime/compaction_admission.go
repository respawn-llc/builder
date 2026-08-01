package runtime

import (
	"errors"
	"fmt"
	"sync"

	"core/shared/invariant"
)

var (
	ErrManualCompactionAdmission    = errors.New("manual compaction admission rejected")
	ErrCompactionInvariantViolation = errors.New("compaction execution invariant violated")
)

type ManualCompactionAdmissionReason string

const (
	ManualCompactionAdmissionReasonActive   ManualCompactionAdmissionReason = "active"
	ManualCompactionAdmissionReasonDisabled ManualCompactionAdmissionReason = "disabled"
	ManualCompactionAdmissionReasonTooSoon  ManualCompactionAdmissionReason = "too_soon"
)

type ManualCompactionAdmissionError struct {
	Reason ManualCompactionAdmissionReason
}

func (e *ManualCompactionAdmissionError) Error() string {
	if e == nil {
		return ErrManualCompactionAdmission.Error()
	}
	return fmt.Sprintf("%s: %s", ErrManualCompactionAdmission, e.Reason)
}

func (e *ManualCompactionAdmissionError) Is(target error) bool {
	return target == ErrManualCompactionAdmission
}

type compactionGateLease struct {
	state *compactionRuntimeState
	once  sync.Once
}

func (l *compactionGateLease) release() {
	if l == nil || l.state == nil {
		return
	}
	l.once.Do(func() {
		l.state.releaseCompactionGate()
	})
}

func (s *compactionRuntimeState) CompactionActive() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.compactionActive
}

func (s *compactionRuntimeState) acquireCompactionGate(
	manual bool,
	debug bool,
) (*compactionGateLease, error) {
	if s == nil {
		return nil, errors.New("compaction runtime state is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.compactionActive {
		if manual {
			return nil, &ManualCompactionAdmissionError{
				Reason: ManualCompactionAdmissionReasonActive,
			}
		}
		err := fmt.Errorf("%w: a second compaction reached the engine", ErrCompactionInvariantViolation)
		if debug {
			panic(err)
		}
		invariant.NewPolicy().Check(false, invariant.FailureDiagnostic(
			invariant.ScopeWorkflowExecution,
			"compaction_execution_overlap",
			err,
		))
		return nil, err
	}
	s.compactionActive = true
	return &compactionGateLease{state: s}, nil
}

func (s *compactionRuntimeState) releaseCompactionGate() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.compactionActive = false
	s.mu.Unlock()
}

func (e *Engine) admitManualCompaction() error {
	if e == nil {
		return &ManualCompactionAdmissionError{Reason: ManualCompactionAdmissionReasonTooSoon}
	}
	state := e.compactionRuntimeState()
	if state.CompactionActive() {
		return &ManualCompactionAdmissionError{Reason: ManualCompactionAdmissionReasonActive}
	}
	if e.CompactionMode() == "none" {
		return &ManualCompactionAdmissionError{Reason: ManualCompactionAdmissionReasonDisabled}
	}
	if !state.ManualCompactionEligible() {
		return &ManualCompactionAdmissionError{Reason: ManualCompactionAdmissionReasonTooSoon}
	}
	return nil
}

func (e *Engine) admitManualCompactionForRequest() error {
	if e == nil {
		return &ManualCompactionAdmissionError{Reason: ManualCompactionAdmissionReasonTooSoon}
	}
	if e.compactionRuntimeState().CompactionActive() {
		return &ManualCompactionAdmissionError{Reason: ManualCompactionAdmissionReasonActive}
	}
	if e.CompactionMode() == "none" {
		return &ManualCompactionAdmissionError{Reason: ManualCompactionAdmissionReasonDisabled}
	}
	return nil
}
