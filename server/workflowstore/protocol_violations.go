package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata/sqlitegen"
)

// RecordProtocolViolation increments the active run's protocol-violation
// counter. If the run became terminal before the increment, no new violation is
// recorded and the existing counter is returned with Interrupted=true so callers
// can stop retrying without surfacing a stale terminal-run error.
func (s *Store) RecordProtocolViolation(ctx context.Context, req RecordProtocolViolationRequest) (RecordProtocolViolationResult, error) {
	if strings.TrimSpace(string(req.RunID)) == "" {
		return RecordProtocolViolationResult{}, errors.New("run id is required")
	}
	if req.MaxCount <= 0 {
		return RecordProtocolViolationResult{}, errors.New("protocol violation max count must be > 0")
	}
	detail := strings.TrimSpace(req.Detail)
	if detail == "" {
		detail = "{}"
	}
	now := s.now().UnixMilli()
	var count int64
	var interruptedAt int64
	var err error
	requireGeneration := int64(0)
	if req.RequireGeneration {
		requireGeneration = 1
	}
	switch req.Kind {
	case ProtocolViolationInvalidCompletion:
		row, recordErr := s.queries.RecordInvalidCompletionProtocolViolation(ctx, sqlitegen.RecordInvalidCompletionProtocolViolationParams{
			UpdatedAtUnixMs:        now,
			MaxCount:               int64(req.MaxCount),
			InterruptedAtUnixMs:    now,
			InterruptionDetailJson: detail,
			RunID:                  string(req.RunID),
			RequireGeneration:      requireGeneration,
			ExpectedGeneration:     req.ExpectedGeneration,
		})
		err = recordErr
		count = row.InvalidCompletionCount
		interruptedAt = row.InterruptedAtUnixMs
	default:
		return RecordProtocolViolationResult{}, fmt.Errorf("unsupported protocol violation kind %q", req.Kind)
	}
	if errors.Is(err, sql.ErrNoRows) {
		run, getErr := s.queries.GetTaskRun(ctx, string(req.RunID))
		if getErr != nil {
			return RecordProtocolViolationResult{}, getErr
		}
		if run.CompletedAtUnixMs != 0 {
			return RecordProtocolViolationResult{Count: protocolViolationCount(run, req.Kind), Interrupted: true}, nil
		}
		if run.InterruptedAtUnixMs != 0 {
			return RecordProtocolViolationResult{Count: protocolViolationCount(run, req.Kind), Interrupted: true}, nil
		}
		if req.RequireGeneration && run.RunGeneration != req.ExpectedGeneration {
			return RecordProtocolViolationResult{}, fmt.Errorf("%w: got %d want %d", ErrStaleRunGeneration, req.ExpectedGeneration, run.RunGeneration)
		}
		return RecordProtocolViolationResult{}, sql.ErrNoRows
	}
	if err != nil {
		return RecordProtocolViolationResult{}, err
	}
	return RecordProtocolViolationResult{Count: count, Interrupted: interruptedAt != 0}, nil
}

func (s *Store) ResetProtocolViolationBudget(ctx context.Context, req ResetProtocolViolationBudgetRequest) error {
	if strings.TrimSpace(string(req.RunID)) == "" {
		return errors.New("run id is required")
	}
	requireGeneration := int64(0)
	if req.RequireGeneration {
		requireGeneration = 1
	}
	updated, err := s.queries.ResetInvalidCompletionProtocolViolationBudget(ctx, sqlitegen.ResetInvalidCompletionProtocolViolationBudgetParams{
		UpdatedAtUnixMs:    s.now().UnixMilli(),
		RunID:              string(req.RunID),
		RequireGeneration:  requireGeneration,
		ExpectedGeneration: req.ExpectedGeneration,
	})
	if err != nil {
		return err
	}
	if updated != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func protocolViolationCount(run sqlitegen.TaskRunRecord, kind ProtocolViolationKind) int64 {
	switch kind {
	case ProtocolViolationInvalidCompletion:
		return run.InvalidCompletionCount
	default:
		return 0
	}
}
