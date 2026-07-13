package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
)

type executableRunRequest struct {
	PlacementID   string
	NodeID        workflow.NodeID
	Snapshot      runStartSnapshot
	Metadata      workflowRunMetadata
	ExecutionRoot *ExecutionRoot
	Now           int64
}

type executableRunResult struct {
	RunID       workflow.RunID
	Interrupted bool
}

func (s *Store) insertExecutableRun(ctx context.Context, q *sqlitegen.Queries, req executableRunRequest) (executableRunResult, error) {
	if strings.TrimSpace(req.PlacementID) == "" || strings.TrimSpace(string(req.NodeID)) == "" {
		return executableRunResult{}, errors.New("executable run placement and node are required")
	}
	snapshotJSON, err := workflow.MarshalString(req.Snapshot)
	if err != nil {
		return executableRunResult{}, err
	}
	metadataJSON, err := workflow.MarshalString(req.Metadata)
	if err != nil {
		return executableRunResult{}, err
	}
	interruptionReason, interruptionDetail, invalidScript, err := s.scriptNodeInterruption(ctx, q, req.NodeID, req.ExecutionRoot)
	if err != nil {
		return executableRunResult{}, err
	}
	interruptedAt := sql.NullInt64{}
	if invalidScript {
		interruptedAt = sql.NullInt64{Int64: req.Now, Valid: true}
	}
	runID := prefixedID("run")
	if err := q.InsertTaskRun(ctx, sqlitegen.InsertTaskRunParams{
		ID:                          runID,
		PlacementID:                 req.PlacementID,
		WorkflowRevisionSeen:        req.Snapshot.WorkflowRevisionSeen,
		AutomationRequestedAtUnixMs: sql.NullInt64{Int64: req.Now, Valid: true},
		CreatedAtUnixMs:             req.Now,
		UpdatedAtUnixMs:             req.Now,
		InterruptedAtUnixMs:         interruptedAt,
		InterruptionReason:          nullableString(interruptionReason),
		InterruptionDetailJson:      interruptionDetail,
		RunStartSnapshotJson:        snapshotJSON,
		MetadataJson:                metadataJSON,
	}); err != nil {
		return executableRunResult{}, fmt.Errorf("insert executable task run: %w", err)
	}
	return executableRunResult{RunID: workflow.RunID(runID), Interrupted: invalidScript}, nil
}
