package workflowstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/shared/serverapi"
)

type CurrentNodeSchedulingTarget struct {
	Reference workflow.CurrentNodeReference
	Expected  workflow.CurrentNodeSchedulingState
}

type CurrentNodeSchedulingInterruptionResult struct {
	Interrupted       []workflow.CurrentNodeReference
	NotificationError error
}

func (s *Store) InterruptCurrentNodeSchedulingSet(
	ctx context.Context,
	taskID workflow.TaskID,
	targets []CurrentNodeSchedulingTarget,
	reason workflow.CurrentNodeInterruptionReason,
	detail workflow.CurrentNodeInterruptionDetail,
) (CurrentNodeSchedulingInterruptionResult, error) {
	if strings.TrimSpace(string(taskID)) == "" {
		return CurrentNodeSchedulingInterruptionResult{}, errors.New("task id is required")
	}
	if len(targets) == 0 {
		return CurrentNodeSchedulingInterruptionResult{}, errors.New("current node scheduling interruption targets are required")
	}
	if strings.TrimSpace(string(reason)) == "" {
		return CurrentNodeSchedulingInterruptionResult{}, errors.New("current node interruption reason is required")
	}
	seen := make(map[workflow.CurrentNodeReferenceKey]struct{}, len(targets))
	for index, target := range targets {
		if err := target.Reference.Validate(); err != nil {
			return CurrentNodeSchedulingInterruptionResult{}, fmt.Errorf("validate interruption target %d: %w", index, err)
		}
		if target.Reference.TaskID != taskID {
			return CurrentNodeSchedulingInterruptionResult{}, fmt.Errorf("interruption target %d belongs to another Task", index)
		}
		switch target.Expected {
		case workflow.CurrentNodeSchedulingReady, workflow.CurrentNodeSchedulingAdmitted:
		default:
			return CurrentNodeSchedulingInterruptionResult{}, fmt.Errorf(
				"interruption target %d has unsupported expected scheduling %q",
				index,
				target.Expected,
			)
		}
		key, err := target.Reference.Key()
		if err != nil {
			return CurrentNodeSchedulingInterruptionResult{}, err
		}
		if _, duplicate := seen[key]; duplicate {
			return CurrentNodeSchedulingInterruptionResult{}, fmt.Errorf("interruption target %d is duplicated", index)
		}
		seen[key] = struct{}{}
	}
	detailJSON, err := json.Marshal(detail)
	if err != nil {
		return CurrentNodeSchedulingInterruptionResult{}, fmt.Errorf("encode current node interruption detail: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CurrentNodeSchedulingInterruptionResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	locked, err := q.AcquireManualMoveTaskWriteLock(ctx, string(taskID))
	if err != nil {
		return CurrentNodeSchedulingInterruptionResult{}, err
	}
	if locked != 1 {
		return CurrentNodeSchedulingInterruptionResult{}, sql.ErrNoRows
	}
	now := s.now().UTC().UnixMilli()
	for _, target := range targets {
		updated, err := interruptExpectedCurrentNode(
			ctx,
			q,
			target,
			reason,
			string(detailJSON),
			now,
		)
		if err != nil {
			return CurrentNodeSchedulingInterruptionResult{}, err
		}
		if updated != 1 {
			return CurrentNodeSchedulingInterruptionResult{}, sql.ErrNoRows
		}
	}
	if err := tx.Commit(); err != nil {
		return CurrentNodeSchedulingInterruptionResult{}, err
	}
	interrupted := make([]workflow.CurrentNodeReference, 0, len(targets))
	for _, target := range targets {
		interrupted = append(interrupted, target.Reference)
	}
	result := CurrentNodeSchedulingInterruptionResult{Interrupted: interrupted}
	result.NotificationError = s.publishCurrentNodeTaskEvent(
		ctx,
		taskID,
		serverapi.WorkflowProjectEventActionInterrupted,
	)
	return result, nil
}

func interruptExpectedCurrentNode(
	ctx context.Context,
	q *sqlitegen.Queries,
	target CurrentNodeSchedulingTarget,
	reason workflow.CurrentNodeInterruptionReason,
	detailJSON string,
	now int64,
) (int64, error) {
	branchKey, branchScoped := target.Reference.TransitionBranchKey()
	switch target.Expected {
	case workflow.CurrentNodeSchedulingReady:
		if branchScoped {
			return q.InterruptBranchReadyCurrentNode(ctx, sqlitegen.InterruptBranchReadyCurrentNodeParams{
				TaskID:                 string(target.Reference.TaskID),
				NodeID:                 string(target.Reference.NodeID),
				TransitionBranchKey:    sql.NullString{String: string(branchKey), Valid: true},
				InterruptionReason:     sql.NullString{String: string(reason), Valid: true},
				InterruptionDetailJson: sql.NullString{String: detailJSON, Valid: true},
				InterruptedAtUnixMs:    sql.NullInt64{Int64: now, Valid: true},
			})
		}
		return q.InterruptSerialReadyCurrentNode(ctx, sqlitegen.InterruptSerialReadyCurrentNodeParams{
			TaskID:                 string(target.Reference.TaskID),
			NodeID:                 string(target.Reference.NodeID),
			InterruptionReason:     sql.NullString{String: string(reason), Valid: true},
			InterruptionDetailJson: sql.NullString{String: detailJSON, Valid: true},
			InterruptedAtUnixMs:    sql.NullInt64{Int64: now, Valid: true},
		})
	case workflow.CurrentNodeSchedulingAdmitted:
		if branchScoped {
			return q.InterruptBranchAdmittedCurrentNode(ctx, sqlitegen.InterruptBranchAdmittedCurrentNodeParams{
				TaskID:                 string(target.Reference.TaskID),
				NodeID:                 string(target.Reference.NodeID),
				TransitionBranchKey:    sql.NullString{String: string(branchKey), Valid: true},
				InterruptionReason:     sql.NullString{String: string(reason), Valid: true},
				InterruptionDetailJson: sql.NullString{String: detailJSON, Valid: true},
				InterruptedAtUnixMs:    sql.NullInt64{Int64: now, Valid: true},
			})
		}
		return q.InterruptSerialAdmittedCurrentNode(ctx, sqlitegen.InterruptSerialAdmittedCurrentNodeParams{
			TaskID:                 string(target.Reference.TaskID),
			NodeID:                 string(target.Reference.NodeID),
			InterruptionReason:     sql.NullString{String: string(reason), Valid: true},
			InterruptionDetailJson: sql.NullString{String: detailJSON, Valid: true},
			InterruptedAtUnixMs:    sql.NullInt64{Int64: now, Valid: true},
		})
	default:
		panic(fmt.Sprintf("unsupported Current-Node scheduling expectation %q", target.Expected))
	}
}
