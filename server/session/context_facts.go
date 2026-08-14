package session

import (
	"context"
	"errors"
)

// SessionContextFacts are bounded presentation facts persisted independently
// from recoverable Session metadata and its append-recovery checksum.
type SessionContextFacts struct {
	CompletedCompactionCount *int
	ManualCompactEligible    *bool
}

type ContextSnapshot struct {
	Meta  Meta
	Facts SessionContextFacts
}

type SessionContextFactWriter interface {
	WriteSessionContextFacts(context.Context, string, SessionContextFacts) error
	WriteManualCompactEligibility(context.Context, string, bool) error
}

func independentSessionContextFacts() SessionContextFacts {
	count := 0
	eligible := false
	return SessionContextFacts{
		CompletedCompactionCount: &count,
		ManualCompactEligible:    &eligible,
	}
}

func (facts SessionContextFacts) Clone() SessionContextFacts {
	cloned := SessionContextFacts{}
	if facts.CompletedCompactionCount != nil {
		value := *facts.CompletedCompactionCount
		cloned.CompletedCompactionCount = &value
	}
	if facts.ManualCompactEligible != nil {
		value := *facts.ManualCompactEligible
		cloned.ManualCompactEligible = &value
	}
	return cloned
}

func normalizeSessionContextFacts(facts SessionContextFacts) SessionContextFacts {
	normalized := facts.Clone()
	if normalized.CompletedCompactionCount != nil && *normalized.CompletedCompactionCount < 0 {
		count := 0
		normalized.CompletedCompactionCount = &count
	}
	return normalized
}

func (s *Store) ContextFacts() SessionContextFacts {
	if s == nil {
		return SessionContextFacts{}
	}
	s.contextFactsMu.Lock()
	defer s.contextFactsMu.Unlock()
	return s.contextFacts.Clone()
}

func (s *Store) ContextSnapshot() ContextSnapshot {
	if s == nil {
		return ContextSnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contextFactsMu.Lock()
	defer s.contextFactsMu.Unlock()
	return ContextSnapshot{
		Meta:  cloneMeta(s.meta),
		Facts: s.contextFacts.Clone(),
	}
}

func (s *Store) SetSessionContextFacts(completedCompactionCount int, manualCompactEligible bool) error {
	if s == nil {
		return errors.New("session store is required")
	}
	if completedCompactionCount < 0 {
		return errors.New("completed compaction count must be non-negative")
	}
	if s.options.contextFactWriter == nil {
		return errors.New("Session Context-fact writer is required")
	}
	sessionID := s.metaSnapshot().meta.SessionID
	facts := SessionContextFacts{
		CompletedCompactionCount: &completedCompactionCount,
		ManualCompactEligible:    &manualCompactEligible,
	}
	if err := s.options.contextFactWriter.WriteSessionContextFacts(
		context.Background(),
		sessionID,
		facts,
	); err != nil {
		return err
	}
	s.contextFactsMu.Lock()
	s.contextFacts = facts.Clone()
	s.contextFactsMu.Unlock()
	return nil
}

func (s *Store) SetManualCompactEligibility(eligible bool) error {
	if s == nil {
		return errors.New("session store is required")
	}
	if s.options.contextFactWriter == nil {
		return errors.New("Session Context-fact writer is required")
	}
	sessionID := s.metaSnapshot().meta.SessionID
	if err := s.options.contextFactWriter.WriteManualCompactEligibility(
		context.Background(),
		sessionID,
		eligible,
	); err != nil {
		return err
	}
	s.contextFactsMu.Lock()
	s.contextFacts.ManualCompactEligible = &eligible
	s.contextFactsMu.Unlock()
	return nil
}
