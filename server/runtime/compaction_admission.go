package runtime

import (
	"errors"
	"fmt"
	"sync"

	"core/shared/invariant"
	"core/shared/serverapi"
)

var (
	ErrManualCompactionAdmission    = serverapi.ErrManualCompactionAdmission
	ErrCompactionInvariantViolation = errors.New("compaction execution invariant violated")
)

type ManualCompactionAdmissionReason = serverapi.ManualCompactionAdmissionReason

const (
	ManualCompactionAdmissionReasonActive   = serverapi.ManualCompactionAdmissionActive
	ManualCompactionAdmissionReasonDisabled = serverapi.ManualCompactionAdmissionDisabled
	ManualCompactionAdmissionReasonTooSoon  = serverapi.ManualCompactionAdmissionTooSoon
)

type ManualCompactionAdmissionError = serverapi.ManualCompactionAdmissionError

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
	state, err := e.admitManualCompactionPolicy()
	if err != nil {
		return err
	}
	if !state.ManualCompactionEligible() {
		return &ManualCompactionAdmissionError{Reason: ManualCompactionAdmissionReasonTooSoon}
	}
	return nil
}

func (e *Engine) admitManualCompactionPolicy() (*compactionRuntimeState, error) {
	if e == nil {
		return nil, &ManualCompactionAdmissionError{Reason: ManualCompactionAdmissionReasonTooSoon}
	}
	state := e.compactionRuntimeState()
	if state.CompactionActive() {
		return nil, &ManualCompactionAdmissionError{Reason: ManualCompactionAdmissionReasonActive}
	}
	if e.CompactionMode() == "none" {
		return nil, &ManualCompactionAdmissionError{Reason: ManualCompactionAdmissionReasonDisabled}
	}
	return state, nil
}

func (e *Engine) admitManualCompactionForRequest() error {
	_, err := e.admitManualCompactionPolicy()
	return err
}
