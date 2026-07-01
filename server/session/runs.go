package session

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	runStartedEventKind  = "run_started"
	runFinishedEventKind = "run_finished"
)

type RunStatus string

const (
	RunStatusRunning     RunStatus = "running"
	RunStatusCompleted   RunStatus = "completed"
	RunStatusInterrupted RunStatus = "interrupted"
	RunStatusFailed      RunStatus = "failed"
)

type RunRecord struct {
	RunID      string    `json:"run_id"`
	StepID     string    `json:"step_id,omitempty"`
	Status     RunStatus `json:"status"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

func (s *Store) AppendRunStarted(run RunRecord) (Event, error) {
	started := normalizeRunRecord(run)
	started.Status = RunStatusRunning
	if started.StartedAt.IsZero() {
		started.StartedAt = time.Now().UTC()
	}
	evt, _, err := s.AppendEvent(started.StepID, runStartedEventKind, started)
	return evt, err
}

func (s *Store) AppendRunFinished(run RunRecord) (Event, error) {
	finished := normalizeRunRecord(run)
	if !isTerminalRunStatus(finished.Status) {
		return Event{}, fmt.Errorf("finished run requires a terminal status")
	}
	if finished.FinishedAt.IsZero() {
		finished.FinishedAt = time.Now().UTC()
	}
	evt, _, err := s.AppendEvent(finished.StepID, runFinishedEventKind, finished)
	return evt, err
}

// ProjectRuns rebuilds run records from an in-memory event slice. It performs no
// file read; callers supplying a full-history slice (test-only collectors) get
// every run, while bounded windows yield only the runs they contain.
func ProjectRuns(events []Event) []RunRecord {
	projector := newRunProjector()
	for _, evt := range events {
		_ = projector.ApplyEvent(evt)
	}
	return projector.Runs()
}

// LatestRun returns the most recently started run, recovered O(1) from persisted
// metadata maintained on append. It never scans the event log.
func (s *Store) LatestRun() (*RunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.meta.LatestRun == nil {
		return nil, nil
	}
	latest := *s.meta.LatestRun
	return &latest, nil
}

func (s *Store) updateLatestRunLocked(events []Event) {
	for _, evt := range events {
		kind := strings.TrimSpace(evt.Kind)
		if kind != runStartedEventKind && kind != runFinishedEventKind {
			continue
		}
		if len(evt.Payload) == 0 {
			continue
		}
		var run RunRecord
		if err := json.Unmarshal(evt.Payload, &run); err != nil {
			continue
		}
		run = normalizeRunRecord(run)
		if run.RunID == "" {
			continue
		}
		if run.StepID == "" {
			run.StepID = strings.TrimSpace(evt.StepID)
		}
		if kind == runStartedEventKind {
			run.Status = RunStatusRunning
			if run.StartedAt.IsZero() {
				run.StartedAt = evt.Timestamp
			}
		} else if run.FinishedAt.IsZero() {
			run.FinishedAt = evt.Timestamp
		}
		if s.meta.LatestRun != nil && s.meta.LatestRun.RunID == run.RunID {
			merged := mergeRunRecord(*s.meta.LatestRun, run)
			s.meta.LatestRun = &merged
			continue
		}
		latest := run
		s.meta.LatestRun = &latest
	}
}

type runProjector struct {
	orderedIDs []string
	byID       map[string]RunRecord
}

func newRunProjector() *runProjector {
	return &runProjector{byID: make(map[string]RunRecord)}
}

func (p *runProjector) ApplyEvent(evt Event) error {
	if p == nil {
		return nil
	}
	kind := strings.TrimSpace(evt.Kind)
	if kind != runStartedEventKind && kind != runFinishedEventKind {
		return nil
	}
	if len(evt.Payload) == 0 {
		return nil
	}
	var run RunRecord
	if err := json.Unmarshal(evt.Payload, &run); err != nil {
		return nil
	}
	run = normalizeRunRecord(run)
	if run.RunID == "" {
		return nil
	}
	if run.StepID == "" {
		run.StepID = strings.TrimSpace(evt.StepID)
	}
	if kind == runStartedEventKind {
		run.Status = RunStatusRunning
		if run.StartedAt.IsZero() {
			run.StartedAt = evt.Timestamp
		}
	} else if run.FinishedAt.IsZero() {
		run.FinishedAt = evt.Timestamp
	}
	existing, ok := p.byID[run.RunID]
	if !ok {
		p.orderedIDs = append(p.orderedIDs, run.RunID)
		p.byID[run.RunID] = run
		return nil
	}
	p.byID[run.RunID] = mergeRunRecord(existing, run)
	return nil
}

func (p *runProjector) Runs() []RunRecord {
	if p == nil || len(p.orderedIDs) == 0 {
		return nil
	}
	out := make([]RunRecord, 0, len(p.orderedIDs))
	for _, runID := range p.orderedIDs {
		out = append(out, p.byID[runID])
	}
	return out
}

func normalizeRunRecord(run RunRecord) RunRecord {
	run.RunID = strings.TrimSpace(run.RunID)
	run.StepID = strings.TrimSpace(run.StepID)
	run.Status = RunStatus(strings.TrimSpace(string(run.Status)))
	return run
}

func isTerminalRunStatus(status RunStatus) bool {
	switch status {
	case RunStatusCompleted, RunStatusInterrupted, RunStatusFailed:
		return true
	default:
		return false
	}
}

func mergeRunRecord(existing, next RunRecord) RunRecord {
	merged := existing
	if merged.StepID == "" {
		merged.StepID = next.StepID
	}
	if merged.StartedAt.IsZero() {
		merged.StartedAt = next.StartedAt
	}
	if !next.StartedAt.IsZero() && (merged.StartedAt.IsZero() || next.StartedAt.Before(merged.StartedAt)) {
		merged.StartedAt = next.StartedAt
	}
	if !next.FinishedAt.IsZero() {
		merged.FinishedAt = next.FinishedAt
	}
	if next.Status != "" && next.Status != RunStatusRunning {
		merged.Status = next.Status
	} else if merged.Status == "" {
		merged.Status = next.Status
	}
	return merged
}
