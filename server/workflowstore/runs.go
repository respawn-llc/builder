package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/shared/serverapi"
)

func (s *Store) ClaimRun(ctx context.Context, runID workflow.RunID, expectedGeneration int64) (RunnableRunRecord, error) {
	return s.AdmitRun(ctx, RunAdmission{RunID: runID, ExpectedGeneration: expectedGeneration})
}

func (s *Store) AdmitRun(ctx context.Context, admission RunAdmission) (RunnableRunRecord, error) {
	now := s.now().UnixMilli()
	sessionID := sql.NullString{}
	if admission.SessionID != nil {
		sessionID = nullableString(*admission.SessionID)
	}
	effectiveMode := sql.NullString{}
	if admission.EffectiveCompletionMode != nil {
		effectiveMode = nullableString(*admission.EffectiveCompletionMode)
	}
	row, err := s.queries.ClaimWorkflowRun(ctx, sqlitegen.ClaimWorkflowRunParams{
		ID:                      string(admission.RunID),
		ExpectedGeneration:      admission.ExpectedGeneration,
		UpdatedAtUnixMs:         now,
		StartedAtUnixMs:         sql.NullInt64{Int64: now, Valid: true},
		SessionID:               sessionID,
		EffectiveCompletionMode: effectiveMode,
	})
	if err != nil {
		return RunnableRunRecord{}, err
	}
	return RunnableRunRecord{RunRecord: runRecordFromClaimedTaskRun(row), WorkflowRevisionSeen: row.WorkflowRevisionSeen}, nil
}

func (s *Store) GetRun(ctx context.Context, runID workflow.RunID) (RunRecord, error) {
	if strings.TrimSpace(string(runID)) == "" {
		return RunRecord{}, errors.New("run id is required")
	}
	row, err := s.queries.GetTaskRun(ctx, string(runID))
	if err != nil {
		return RunRecord{}, err
	}
	return runRecordFromTaskRun(row), nil
}

func (s *Store) SetRunEffectiveCompletionMode(ctx context.Context, runID workflow.RunID, expectedGeneration int64, mode string) error {
	trimmedMode := strings.TrimSpace(mode)
	if strings.TrimSpace(string(runID)) == "" {
		return errors.New("run id is required")
	}
	if !validEffectiveCompletionMode(trimmedMode) {
		return fmt.Errorf("%w %q", ErrInvalidEffectiveCompletionMode, mode)
	}
	now := s.now().UnixMilli()
	updated, err := s.queries.SetTaskRunEffectiveCompletionMode(ctx, sqlitegen.SetTaskRunEffectiveCompletionModeParams{
		ID:                      string(runID),
		ExpectedGeneration:      expectedGeneration,
		EffectiveCompletionMode: sql.NullString{String: trimmedMode, Valid: true},
		UpdatedAtUnixMs:         now,
	})
	if err != nil {
		return err
	}
	if updated != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func validEffectiveCompletionMode(mode string) bool {
	switch mode {
	case "structured_output", "tool", "shell_command", "unstructured_output":
		return true
	default:
		return false
	}
}

func (s *Store) InterruptRun(ctx context.Context, runID workflow.RunID, reason string, detailJSON string) error {
	if strings.TrimSpace(detailJSON) == "" {
		detailJSON = "{}"
	}
	now := s.now().UnixMilli()
	updated, err := s.queries.InterruptWorkflowRun(ctx, sqlitegen.InterruptWorkflowRunParams{ID: string(runID), UpdatedAtUnixMs: now, InterruptedAtUnixMs: sql.NullInt64{Int64: now, Valid: true}, InterruptionReason: nullableString(reason), InterruptionDetailJson: detailJSON})
	if err != nil {
		return err
	}
	if updated != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) InterruptRunGeneration(ctx context.Context, runID workflow.RunID, generation int64, reason string, detailJSON string) error {
	if strings.TrimSpace(detailJSON) == "" {
		detailJSON = "{}"
	}
	now := s.now().UnixMilli()
	updated, err := s.queries.InterruptRunGeneration(ctx, sqlitegen.InterruptRunGenerationParams{
		UpdatedAtUnixMs:        now,
		InterruptedAtUnixMs:    sql.NullInt64{Int64: now, Valid: true},
		InterruptionReason:     nullableString(reason),
		InterruptionDetailJson: detailJSON,
		RunID:                  string(runID),
		RunGeneration:          generation,
	})
	if err != nil {
		return err
	}
	if updated != 1 {
		return sql.ErrNoRows
	}
	return nil
}

type ExactRunRef struct {
	TaskID     workflow.TaskID
	RunID      workflow.RunID
	Generation int64
}

func (s *Store) InterruptExactRuns(ctx context.Context, refs []ExactRunRef, reason string, detailJSON string) ([]RunRecord, error) {
	if len(refs) == 0 {
		return nil, errors.New("at least one exact workflow run is required")
	}
	if strings.TrimSpace(detailJSON) == "" {
		detailJSON = "{}"
	}
	seen := make(map[workflow.RunID]struct{}, len(refs))
	for _, ref := range refs {
		if strings.TrimSpace(string(ref.TaskID)) == "" {
			return nil, errors.New("exact workflow run task id is required")
		}
		if strings.TrimSpace(string(ref.RunID)) == "" {
			return nil, errors.New("exact workflow run id is required")
		}
		if ref.Generation < 0 {
			return nil, errors.New("exact workflow run generation is invalid")
		}
		if _, exists := seen[ref.RunID]; exists {
			return nil, fmt.Errorf("exact workflow run %q is duplicated", ref.RunID)
		}
		seen[ref.RunID] = struct{}{}
	}
	interruptReason := strings.TrimSpace(reason)
	if interruptReason == "" {
		interruptReason = "user_interrupt"
	}
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	interrupted := make([]RunRecord, 0, len(refs))
	for _, ref := range refs {
		current, err := q.GetTaskRun(ctx, string(ref.RunID))
		if err != nil {
			return nil, err
		}
		if current.TaskID != string(ref.TaskID) || current.RunGeneration != ref.Generation {
			return nil, sql.ErrNoRows
		}
		updated, err := q.InterruptRunGeneration(ctx, sqlitegen.InterruptRunGenerationParams{
			UpdatedAtUnixMs:        now,
			InterruptedAtUnixMs:    sql.NullInt64{Int64: now, Valid: true},
			InterruptionReason:     sql.NullString{String: interruptReason, Valid: true},
			InterruptionDetailJson: detailJSON,
			RunID:                  string(ref.RunID),
			RunGeneration:          ref.Generation,
		})
		if err != nil {
			return nil, err
		}
		if updated != 1 {
			return nil, sql.ErrNoRows
		}
		row, err := q.GetTaskRun(ctx, string(ref.RunID))
		if err != nil {
			return nil, err
		}
		interrupted = append(interrupted, runRecordFromTaskRun(row))
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return interrupted, nil
}

func (s *Store) InterruptTaskRuns(ctx context.Context, taskID workflow.TaskID, sessionID string, reason string) ([]RunRecord, error) {
	if strings.TrimSpace(string(taskID)) == "" {
		return nil, errors.New("task id is required")
	}
	rows, err := s.queries.ListInterruptTaskRunCandidates(ctx, sqlitegen.ListInterruptTaskRunCandidatesParams{
		TaskID:    string(taskID),
		SessionID: strings.TrimSpace(sessionID),
	})
	if err != nil {
		return nil, err
	}
	candidates := runRecordsFromTaskRunRecords(rows)
	if len(candidates) == 0 {
		return nil, errors.New("task has no active workflow run to interrupt")
	}
	interruptReason := strings.TrimSpace(reason)
	if interruptReason == "" {
		interruptReason = "user_interrupt"
	}
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	interrupted := make([]RunRecord, 0, len(candidates))
	for _, candidate := range candidates {
		updated, err := q.InterruptWorkflowRun(ctx, sqlitegen.InterruptWorkflowRunParams{ID: string(candidate.ID), UpdatedAtUnixMs: now, InterruptedAtUnixMs: sql.NullInt64{Int64: now, Valid: true}, InterruptionReason: sql.NullString{String: interruptReason, Valid: true}, InterruptionDetailJson: "{}"})
		if err != nil {
			return nil, err
		}
		if updated != 1 {
			return nil, sql.ErrNoRows
		}
		run, err := q.GetTaskRun(ctx, string(candidate.ID))
		if err != nil {
			return nil, err
		}
		interrupted = append(interrupted, runRecordFromTaskRun(run))
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return interrupted, nil
}

func (s *Store) ReconcileStartedRuns(ctx context.Context, reason string) ([]RunRecord, error) {
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	rows, err := q.ListStartedWorkflowRunRecoveryCandidates(ctx)
	if err != nil {
		return nil, err
	}
	candidates := make([]RunRecord, 0, len(rows))
	for _, row := range rows {
		candidates = append(candidates, runRecordFromStartedRecoveryCandidate(row))
	}
	interrupted := make([]RunRecord, 0, len(candidates))
	for _, candidate := range candidates {
		updated, err := q.InterruptWorkflowRun(ctx, sqlitegen.InterruptWorkflowRunParams{ID: string(candidate.ID), UpdatedAtUnixMs: now, InterruptedAtUnixMs: sql.NullInt64{Int64: now, Valid: true}, InterruptionReason: sql.NullString{String: strings.TrimSpace(reason), Valid: true}, InterruptionDetailJson: "{}"})
		if err != nil {
			return nil, err
		}
		if updated != 1 {
			return nil, sql.ErrNoRows
		}
		run, err := q.GetTaskRun(ctx, string(candidate.ID))
		if err != nil {
			return nil, err
		}
		interrupted = append(interrupted, runRecordFromTaskRun(run))
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return interrupted, nil
}

func (s *Store) ReconcileUnstartedRuns(ctx context.Context, reason string) ([]RunRecord, error) {
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	rows, err := q.ListUnstartedWorkflowRunRecoveryCandidates(ctx)
	if err != nil {
		return nil, err
	}
	candidates := make([]RunRecord, 0, len(rows))
	for _, row := range rows {
		candidates = append(candidates, runRecordFromUnstartedRecoveryCandidate(row))
	}
	interrupted := make([]RunRecord, 0, len(candidates))
	for _, candidate := range candidates {
		updated, err := q.InterruptWorkflowRun(ctx, sqlitegen.InterruptWorkflowRunParams{
			ID:                     string(candidate.ID),
			UpdatedAtUnixMs:        now,
			InterruptedAtUnixMs:    sql.NullInt64{Int64: now, Valid: true},
			InterruptionReason:     sql.NullString{String: strings.TrimSpace(reason), Valid: true},
			InterruptionDetailJson: "{}",
		})
		if err != nil {
			return nil, err
		}
		if updated != 1 {
			return nil, sql.ErrNoRows
		}
		run, err := q.GetTaskRun(ctx, string(candidate.ID))
		if err != nil {
			return nil, err
		}
		interrupted = append(interrupted, runRecordFromTaskRun(run))
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return interrupted, nil
}

func (s *Store) ListWaitingAskRuns(ctx context.Context) ([]RunRecord, error) {
	rows, err := s.queries.ListWaitingAskWorkflowRuns(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RunRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, runRecordFromTaskRun(row))
	}
	return out, nil
}

func (s *Store) ListTaskResumeCandidates(ctx context.Context, taskID workflow.TaskID) ([]RunRecord, error) {
	if strings.TrimSpace(string(taskID)) == "" {
		return nil, errors.New("task id is required")
	}
	task, err := s.queries.GetTask(ctx, string(taskID))
	if err != nil {
		return nil, err
	}
	if task.CanceledAtUnixMs.Valid {
		return nil, ErrTaskCanceled
	}
	candidates, err := s.queries.ListResumeTaskRunCandidates(ctx, string(taskID))
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, errors.New("task has no interrupted workflow run to resume")
	}
	runs := make([]RunRecord, 0, len(candidates))
	for _, candidate := range candidates {
		snapshot := runStartSnapshot{}
		if err := workflow.UnmarshalString(candidate.RunStartSnapshotJson, &snapshot); err != nil {
			return nil, err
		}
		if snapshot.Node.Kind != workflow.NodeKindAgent {
			run, err := s.GetRun(ctx, workflow.RunID(candidate.ID))
			if err != nil {
				return nil, err
			}
			runs = append(runs, run)
			continue
		}
		if err := s.validateRunnableRole(snapshot.Node.SubagentRole); err != nil {
			return nil, err
		}
		run, err := s.GetRun(ctx, workflow.RunID(candidate.ID))
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, nil
}

func (s *Store) AdmitTaskResume(ctx context.Context, taskID workflow.TaskID, admissions []RunAdmission) (ResumeTaskRunsResult, error) {
	if strings.TrimSpace(string(taskID)) == "" {
		return ResumeTaskRunsResult{}, errors.New("task id is required")
	}
	if len(admissions) == 0 {
		return ResumeTaskRunsResult{}, errors.New("at least one workflow run admission is required")
	}
	seen := make(map[workflow.RunID]struct{}, len(admissions))
	for _, admission := range admissions {
		if strings.TrimSpace(string(admission.RunID)) == "" {
			return ResumeTaskRunsResult{}, errors.New("workflow run admission id is required")
		}
		if admission.ExpectedGeneration < 0 {
			return ResumeTaskRunsResult{}, errors.New("workflow run admission generation is invalid")
		}
		if _, exists := seen[admission.RunID]; exists {
			return ResumeTaskRunsResult{}, fmt.Errorf("workflow run admission %q is duplicated", admission.RunID)
		}
		seen[admission.RunID] = struct{}{}
	}
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ResumeTaskRunsResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	task, err := q.GetTask(ctx, string(taskID))
	if err != nil {
		return ResumeTaskRunsResult{}, fmt.Errorf("admit workflow task resume %s: load task: %w", taskID, err)
	}
	if task.CanceledAtUnixMs.Valid {
		return ResumeTaskRunsResult{}, ErrTaskCanceled
	}
	candidates, err := q.ListResumeTaskRunCandidates(ctx, string(taskID))
	if err != nil {
		return ResumeTaskRunsResult{}, fmt.Errorf("admit workflow task resume %s: reload candidates: %w", taskID, err)
	}
	if len(candidates) != len(admissions) {
		return ResumeTaskRunsResult{}, fmt.Errorf(
			"admit workflow task resume %s: candidate count changed: got %d want %d: %w",
			taskID,
			len(candidates),
			len(admissions),
			sql.ErrNoRows,
		)
	}
	for _, candidate := range candidates {
		if _, exists := seen[workflow.RunID(candidate.ID)]; !exists {
			return ResumeTaskRunsResult{}, fmt.Errorf(
				"admit workflow task resume %s: candidate run %s was not prepared: %w",
				taskID,
				candidate.ID,
				sql.ErrNoRows,
			)
		}
	}
	resolution, err := taskAttentionResolution(ctx, q, string(taskID))
	if err != nil {
		return ResumeTaskRunsResult{}, fmt.Errorf("admit workflow task resume %s: capture attention resolution: %w", taskID, err)
	}
	resumed := make([]RunRecord, 0, len(admissions))
	for _, admission := range admissions {
		sessionID := sql.NullString{}
		if admission.SessionID != nil {
			sessionID = nullableString(*admission.SessionID)
		}
		effectiveMode := sql.NullString{}
		if admission.EffectiveCompletionMode != nil {
			effectiveMode = nullableString(*admission.EffectiveCompletionMode)
		}
		updated, err := q.AdmitResumedTaskRun(ctx, sqlitegen.AdmitResumedTaskRunParams{
			UpdatedAtUnixMs:         now,
			StartedAtUnixMs:         sql.NullInt64{Int64: now, Valid: true},
			SessionID:               sessionID,
			EffectiveCompletionMode: effectiveMode,
			RunID:                   string(admission.RunID),
			ExpectedGeneration:      admission.ExpectedGeneration,
		})
		if err != nil {
			return ResumeTaskRunsResult{}, fmt.Errorf(
				"admit workflow task resume %s run %s generation %d: update: %w",
				taskID,
				admission.RunID,
				admission.ExpectedGeneration,
				err,
			)
		}
		if updated != 1 {
			return ResumeTaskRunsResult{}, fmt.Errorf(
				"admit workflow task resume %s run %s generation %d: updated %d rows: %w",
				taskID,
				admission.RunID,
				admission.ExpectedGeneration,
				updated,
				sql.ErrNoRows,
			)
		}
		run, err := q.GetTaskRun(ctx, string(admission.RunID))
		if err != nil {
			return ResumeTaskRunsResult{}, fmt.Errorf(
				"admit workflow task resume %s run %s generation %d: reload admitted run: %w",
				taskID,
				admission.RunID,
				admission.ExpectedGeneration,
				err,
			)
		}
		resumed = append(resumed, runRecordFromTaskRun(run))
	}
	if err := tx.Commit(); err != nil {
		return ResumeTaskRunsResult{}, fmt.Errorf("admit workflow task resume %s: commit: %w", taskID, err)
	}
	return ResumeTaskRunsResult{
		Runs: resumed,
		TaskAttentionResolution: TaskAttentionResolution{
			ResolvedInterruptedRunProjections: resolution.ResolvedInterruptedRunProjections,
		},
	}, nil
}

func (s *Store) validateRunnableRole(role string) error {
	trimmed := strings.TrimSpace(role)
	if trimmed == "" {
		return WorkflowValidationError{Diagnostics: []workflow.ValidationError{{
			Code:          workflow.CodeAgentRoleRequired,
			Message:       "agent node requires a subagent role",
			BlocksContext: true,
		}}}
	}
	if workflow.IsDefaultAgentRole(trimmed) {
		return nil
	}
	if s.roleResolver != nil && !s.roleResolver.RoleExists(trimmed) {
		return WorkflowValidationError{Diagnostics: []workflow.ValidationError{{
			Code:          workflow.CodeAgentRoleMissing,
			Message:       "agent node references a missing subagent role",
			AgentRole:     &trimmed,
			BlocksContext: true,
		}}}
	}
	return nil
}

type RunCompletionContext struct {
	TransitionIDs     []string
	TransitionOptions []TransitionOption
}

func (s *Store) GetRunCompletionContext(ctx context.Context, runID workflow.RunID) (RunCompletionContext, error) {
	run, err := s.queries.GetTaskRun(ctx, string(runID))
	if err != nil {
		return RunCompletionContext{}, err
	}
	task, err := s.queries.GetTask(ctx, run.TaskID)
	if err != nil {
		return RunCompletionContext{}, err
	}
	snapshot := runStartSnapshot{}
	if err := workflow.UnmarshalString(run.RunStartSnapshotJson, &snapshot); err != nil {
		return RunCompletionContext{}, err
	}
	if snapshot.Node.Kind == workflow.NodeKindScript {
		refreshed, err := s.liveScriptRunStartSnapshot(ctx, task, snapshot.Node.ID)
		if err != nil {
			return RunCompletionContext{}, err
		}
		snapshot = refreshed
	}
	return RunCompletionContext{
		TransitionIDs:     transitionIDsFromSnapshot(snapshot),
		TransitionOptions: transitionOptionsFromSnapshot(snapshot),
	}, nil
}

func (s *Store) GetRunStartContext(ctx context.Context, runID workflow.RunID) (RunStartContext, error) {
	run, err := s.queries.GetTaskRun(ctx, string(runID))
	if err != nil {
		return RunStartContext{}, err
	}
	task, err := s.queries.GetTask(ctx, run.TaskID)
	if err != nil {
		return RunStartContext{}, err
	}
	workflowRow, err := s.queries.GetWorkflow(ctx, task.WorkflowID)
	if err != nil {
		return RunStartContext{}, err
	}
	workflowRecord := WorkflowRecord{ID: workflow.WorkflowID(workflowRow.ID), Name: workflowRow.Name, Description: workflowRow.Description, Version: workflowRow.Version}
	snapshot := runStartSnapshot{}
	if err := workflow.UnmarshalString(run.RunStartSnapshotJson, &snapshot); err != nil {
		return RunStartContext{}, err
	}
	taskRecord, err := taskRecordFromTask(task)
	if err != nil {
		return RunStartContext{}, err
	}
	var executionRoot *ExecutionRoot
	if taskRecord.ExecutionTarget != nil {
		root, err := executionRootForTask(ctx, s.queries, task)
		if err != nil {
			return RunStartContext{}, err
		}
		executionRoot = &root
	}
	inputResolution, err := s.resolveRunInputContext(ctx, run.PlacementID, taskRecord)
	if err != nil {
		return RunStartContext{}, err
	}
	inputValues := inputResolution.Values
	transitionContext, err := s.resolveRunTransitionContext(ctx, run.PlacementID, run.MetadataJson)
	if err != nil {
		return RunStartContext{}, err
	}
	isFanoutBranch, err := s.placementIsFanoutBranch(ctx, run.TaskID, run.PlacementID)
	if err != nil {
		return RunStartContext{}, err
	}
	runMetadata := workflowRunMetadata{}
	if strings.TrimSpace(run.MetadataJson) != "" {
		if err := workflow.UnmarshalString(run.MetadataJson, &runMetadata); err != nil {
			return RunStartContext{}, fmt.Errorf("resolve workflow run metadata: %w", err)
		}
	}
	nodeRecord, err := s.runStartNodeRecord(ctx, snapshot)
	if err != nil {
		return RunStartContext{}, err
	}
	parameterValues := map[string]string{}
	for key, value := range inputValues {
		parameterValues[key] = value
	}
	if _, exists := parameterValues[workflow.RuntimePromptParameterCommentary]; !exists {
		parameterValues[workflow.RuntimePromptParameterCommentary] = inputResolution.Commentary
	}
	priorParameterValues := clonePriorParameterValues(runMetadata.PriorParameterValues)
	parameters := append([]workflow.Parameter(nil), runMetadata.Parameters...)
	return RunStartContext{
		Run:                            runRecordFromTaskRun(run),
		Task:                           taskRecord,
		Workflow:                       workflowRecord,
		Node:                           nodeRecord,
		ContextMode:                    transitionContext.ContextMode,
		WorkflowHasContinueSessionEdge: snapshot.hasContinueSessionEdge(),
		SourceRunID:                    transitionContext.SourceRunID,
		SourceSessionID:                transitionContext.SourceSessionID,
		SourceNode:                     transitionContext.SourceNode,
		AcceptedTransitionPath:         transitionContext.AcceptedTransitionPath,
		IsFanoutBranch:                 isFanoutBranch,
		TransitionIDs:                  transitionIDsFromSnapshot(snapshot),
		TransitionOptions:              transitionOptionsFromSnapshot(snapshot),
		PromptTemplate:                 strings.TrimSpace(runMetadata.PromptTemplate),
		Parameters:                     parameters,
		ParameterValues:                parameterValues,
		PriorParameterValues:           priorParameterValues,
		InputValues:                    inputValues,
		NodeOutputValues:               runMetadata.NodeOutputValues,
		ExecutionRoot:                  executionRoot,
	}, nil
}

func (s *Store) runStartNodeRecord(ctx context.Context, snapshot runStartSnapshot) (NodeRecord, error) {
	node := nodeRecordFromSnapshot(snapshot.Node, snapshot.WorkflowID)
	if node.Kind != workflow.NodeKindScript {
		return node, nil
	}
	live, err := s.queries.GetWorkflowNode(ctx, string(node.ID))
	if err != nil {
		return NodeRecord{}, fmt.Errorf("load live script node %q: %w", node.ID, err)
	}
	if live.WorkflowID != string(snapshot.WorkflowID) {
		return NodeRecord{}, fmt.Errorf("live script node %q belongs to workflow %q, want %q", node.ID, live.WorkflowID, snapshot.WorkflowID)
	}
	if workflow.NodeKind(live.Kind) != workflow.NodeKindScript {
		return NodeRecord{}, fmt.Errorf("live node %q is %q, want script", node.ID, live.Kind)
	}
	node.ScriptPath = ""
	if live.ScriptPath.Valid {
		node.ScriptPath = live.ScriptPath.String
	}
	return node, nil
}

// placementIsFanoutBranch reports whether the run's placement was created as a
// branch of a parallel fan-out transition group. The scheduler records this by
// setting ParallelBranchEdgeID only on fan-out branch placements.
func (s *Store) placementIsFanoutBranch(ctx context.Context, taskID, placementID string) (bool, error) {
	placementID = strings.TrimSpace(placementID)
	if placementID == "" {
		return false, nil
	}
	placements, err := s.queries.ListTaskNodePlacements(ctx, taskID)
	if err != nil {
		return false, fmt.Errorf("list task node placements: %w", err)
	}
	for _, placement := range placements {
		if placement.ID != placementID {
			continue
		}
		return placement.ParallelBranchEdgeID.Valid && strings.TrimSpace(placement.ParallelBranchEdgeID.String) != "", nil
	}
	return false, nil
}

type runTransitionContext struct {
	ContextMode            workflow.ContextMode
	SourceRunID            workflow.RunID
	SourceSessionID        string
	SourceNode             NodeRecord
	AcceptedTransitionPath AcceptedTransitionPath
}

func (s *Store) resolveRunTransitionContext(ctx context.Context, placementID string, runMetadataJSON string) (runTransitionContext, error) {
	row, err := s.queries.GetRunTransitionContext(ctx, placementID)
	if errors.Is(err, sql.ErrNoRows) {
		return runTransitionContext{ContextMode: workflow.ContextModeNewSession}, nil
	}
	if err != nil {
		return runTransitionContext{}, fmt.Errorf("resolve workflow run transition context: %w", err)
	}
	sourceRunID := row.SourceRunID
	resolved := runTransitionContext{
		ContextMode: workflow.ContextMode(strings.TrimSpace(row.ContextMode)),
		AcceptedTransitionPath: AcceptedTransitionPath{
			SourceNodeDisplayName: strings.TrimSpace(row.SourceNodeDisplayName),
			TargetNodeDisplayName: strings.TrimSpace(row.TargetNodeDisplayName),
		},
	}
	if resolved.ContextMode == "" {
		resolved.ContextMode = workflow.ContextModeNewSession
	}
	runMetadata := workflowRunMetadata{}
	if strings.TrimSpace(runMetadataJSON) != "" {
		if err := workflow.UnmarshalString(runMetadataJSON, &runMetadata); err != nil {
			return runTransitionContext{}, fmt.Errorf("resolve workflow run metadata: %w", err)
		}
		if strings.TrimSpace(runMetadata.ContextMode) != "" {
			resolved.ContextMode = workflow.ContextMode(strings.TrimSpace(runMetadata.ContextMode))
		}
		if runMetadata.ContextResolutionFrozen && strings.TrimSpace(runMetadata.SourceRunID) == "" {
			sourceRunID = sql.NullString{}
		} else if strings.TrimSpace(runMetadata.SourceRunID) != "" {
			sourceRunID = sql.NullString{String: strings.TrimSpace(runMetadata.SourceRunID), Valid: true}
		}
	}
	if !sourceRunID.Valid || strings.TrimSpace(sourceRunID.String) == "" {
		return resolved, nil
	}
	sourceRun, err := s.queries.GetTaskRun(ctx, sourceRunID.String)
	if err != nil {
		return runTransitionContext{}, err
	}
	sourceSnapshot := runStartSnapshot{}
	if err := workflow.UnmarshalString(sourceRun.RunStartSnapshotJson, &sourceSnapshot); err != nil {
		return runTransitionContext{}, err
	}
	resolved.SourceRunID = workflow.RunID(sourceRun.ID)
	resolved.SourceSessionID = strings.TrimSpace(sourceRun.SessionID.String)
	resolved.SourceNode = nodeRecordFromSnapshot(sourceSnapshot.Node, sourceSnapshot.WorkflowID)
	return resolved, nil
}

type runInputContext struct {
	Values     map[string]string
	Commentary string
}

func (s *Store) resolveRunInputContext(ctx context.Context, placementID string, task TaskRecord) (runInputContext, error) {
	row, err := s.queries.GetRunInputValues(ctx, placementID)
	if errors.Is(err, sql.ErrNoRows) {
		return runInputContext{Values: map[string]string{}}, nil
	}
	if err != nil {
		return runInputContext{}, fmt.Errorf("resolve workflow run input values: %w", err)
	}
	outputValues := map[string]string{}
	if err := workflow.UnmarshalString(row.OutputValuesJson, &outputValues); err != nil {
		return runInputContext{}, err
	}
	bindings := []workflow.InputBinding{}
	if err := workflow.UnmarshalString(row.InputBindingsJson, &bindings); err != nil {
		return runInputContext{}, err
	}
	values, err := resolveInputBindingValues(task, row.Commentary, outputValues, bindings)
	if err != nil {
		return runInputContext{}, err
	}
	return runInputContext{Values: values, Commentary: row.Commentary}, nil
}

func resolveInputBindingValues(task TaskRecord, commentary string, outputValues map[string]string, bindings []workflow.InputBinding) (map[string]string, error) {
	values := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		name := strings.TrimSpace(binding.Name)
		if name == "" {
			continue
		}
		switch binding.Source {
		case workflow.BindingSourceTask:
			values[name] = taskInputBindingValue(task, binding.Field)
		case workflow.BindingSourceTransitionOutput:
			field := strings.TrimSpace(binding.Field)
			if field == "commentary" {
				values[name] = commentary
			} else {
				values[name] = outputValues[field]
			}
		case workflow.BindingSourceJoin:
			values[name] = outputValues[strings.TrimSpace(binding.Field)]
		default:
			return nil, fmt.Errorf("unsupported input binding source %q", binding.Source)
		}
	}
	return values, nil
}

func taskInputBindingValue(task TaskRecord, field string) string {
	switch strings.TrimSpace(field) {
	case "short_id":
		return task.ShortID
	case "title":
		return task.Title
	case "body":
		return task.Body
	case "source_url":
		return task.SourceURL
	default:
		return ""
	}
}

func (s *Store) AttachRunSession(ctx context.Context, runID workflow.RunID, expectedGeneration int64, sessionID string) error {
	updated, err := s.queries.AttachRunSession(ctx, sqlitegen.AttachRunSessionParams{
		UpdatedAtUnixMs: s.now().UnixMilli(),
		SessionID:       sql.NullString{String: strings.TrimSpace(sessionID), Valid: true},
		RunID:           string(runID),
		RunGeneration:   expectedGeneration,
	})
	if err != nil {
		return fmt.Errorf("attach workflow run session: %w", err)
	}
	if updated != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) SetRunWaitingAsk(ctx context.Context, runID workflow.RunID, expectedGeneration int64, askID string) error {
	trimmedAskID := strings.TrimSpace(askID)
	if trimmedAskID == "" {
		return fmt.Errorf("ask id is required")
	}
	now := s.now().UnixMilli()
	updated, err := s.queries.SetRunWaitingAsk(ctx, sqlitegen.SetRunWaitingAskParams{
		UpdatedAtUnixMs: now,
		AskID:           sql.NullString{String: trimmedAskID, Valid: true},
		RunID:           string(runID),
		RunGeneration:   expectedGeneration,
	})
	if err != nil {
		return fmt.Errorf("set workflow run waiting ask: %w", err)
	}
	if updated != 1 {
		return sql.ErrNoRows
	}
	event, err := runWaitingAskWorkflowEvent(ctx, s.queries, string(runID), serverapi.WorkflowProjectEventActionQuestionWaiting, trimmedAskID, now)
	if err != nil {
		return err
	}
	return s.PublishWorkflowEvent(ctx, event)
}

func (s *Store) ClearRunWaitingAsk(ctx context.Context, runID workflow.RunID, expectedGeneration int64, askID string) error {
	trimmedAskID := strings.TrimSpace(askID)
	if trimmedAskID == "" {
		return fmt.Errorf("ask id is required")
	}
	now := s.now().UnixMilli()
	updated, err := s.queries.ClearRunWaitingAsk(ctx, sqlitegen.ClearRunWaitingAskParams{
		UpdatedAtUnixMs: now,
		RunID:           string(runID),
		RunGeneration:   expectedGeneration,
		AskID:           sql.NullString{String: trimmedAskID, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("clear workflow run waiting ask: %w", err)
	}
	if updated != 1 {
		return sql.ErrNoRows
	}
	event, err := runWaitingAskWorkflowEvent(ctx, s.queries, string(runID), serverapi.WorkflowProjectEventActionQuestionCleared, trimmedAskID, now)
	if err != nil {
		return err
	}
	return s.PublishWorkflowEvent(ctx, event)
}

func runWaitingAskWorkflowEvent(
	ctx context.Context,
	q *sqlitegen.Queries,
	runID string,
	action serverapi.WorkflowProjectEventAction,
	askID string,
	occurredAtUnixMs int64,
) (WorkflowEventRecord, error) {
	row, err := q.GetRunWaitingAskEventIdentity(ctx, strings.TrimSpace(runID))
	if err != nil {
		return WorkflowEventRecord{}, fmt.Errorf("load waiting ask event run identity: %w", err)
	}
	projectID := row.ProjectID
	workflowID := row.WorkflowID
	return WorkflowEventRecord{
		ProjectID:        &projectID,
		WorkflowID:       &workflowID,
		Resource:         serverapi.WorkflowProjectEventResourceTask,
		Action:           action,
		PrimaryEntityID:  row.TaskID,
		RelatedIDs:       []string{strings.TrimSpace(runID), strings.TrimSpace(askID)},
		OccurredAtUnixMs: occurredAtUnixMs,
	}, nil
}

func (s *Store) ResolveTaskWaitingAsk(ctx context.Context, taskID workflow.TaskID, runID workflow.RunID, askID string) (ResolvedWaitingAsk, error) {
	trimmedTaskID := strings.TrimSpace(string(taskID))
	trimmedRunID := strings.TrimSpace(string(runID))
	trimmedAskID := strings.TrimSpace(askID)
	if trimmedTaskID == "" {
		return ResolvedWaitingAsk{}, errors.New("task id is required")
	}
	if trimmedAskID == "" {
		return ResolvedWaitingAsk{}, errors.New("ask id is required")
	}
	rows, err := s.queries.ResolveTaskWaitingAsk(ctx, sqlitegen.ResolveTaskWaitingAskParams{
		TaskID: trimmedTaskID,
		AskID:  sql.NullString{String: trimmedAskID, Valid: true},
		RunID:  trimmedRunID,
	})
	if err != nil {
		return ResolvedWaitingAsk{}, err
	}
	matches := make([]ResolvedWaitingAsk, 0, len(rows))
	for _, row := range rows {
		matches = append(matches, ResolvedWaitingAsk{
			Run:        runRecordFromTaskRun(row.TaskRunRecord),
			ProjectID:  row.ProjectID,
			WorkflowID: workflow.WorkflowID(row.WorkflowID),
		})
	}
	if len(matches) == 0 {
		return ResolvedWaitingAsk{}, ErrTaskAskNotPending
	}
	if trimmedRunID == "" && len(matches) != 1 {
		return ResolvedWaitingAsk{}, fmt.Errorf("task has multiple matching pending asks; %w", ErrRunIDRequired)
	}
	return matches[0], nil
}

func (s *Store) ResolveActiveRunCompletionTarget(ctx context.Context, selector ActiveRunCompletionTargetSelector) (RunCompletionTarget, error) {
	matches, err := s.activeRunCompletionTargetMatches(ctx, selector)
	if err != nil {
		return RunCompletionTarget{}, err
	}
	if len(matches) == 0 {
		return RunCompletionTarget{}, sql.ErrNoRows
	}
	if len(matches) != 1 {
		return RunCompletionTarget{}, fmt.Errorf("selector matched multiple active workflow runs; %w", ErrRunIDRequired)
	}
	return RunCompletionTarget{Run: matches[0]}, nil
}

func (s *Store) ResolveIdleSessionRunCompletionTarget(ctx context.Context, sessionID string) (RunCompletionTarget, error) {
	trimmedSessionID := strings.TrimSpace(sessionID)
	if trimmedSessionID == "" {
		return RunCompletionTarget{}, errors.New("session id is required")
	}
	runs, err := s.sessionRunCompletionTargets(ctx, trimmedSessionID)
	if err != nil {
		return RunCompletionTarget{}, err
	}
	matches := make([]RunRecord, 0, len(runs))
	for _, run := range runs {
		if run.InterruptedAt != nil {
			matches = append(matches, run)
		}
	}
	if len(matches) == 0 {
		return RunCompletionTarget{}, sql.ErrNoRows
	}
	if len(matches) != 1 {
		return RunCompletionTarget{}, fmt.Errorf("session matched multiple idle workflow runs; %w", ErrRunIDRequired)
	}
	return RunCompletionTarget{Run: matches[0]}, nil
}

func (s *Store) activeRunCompletionTargetMatches(ctx context.Context, selector ActiveRunCompletionTargetSelector) ([]RunRecord, error) {
	runID := strings.TrimSpace(string(selector.RunID))
	sessionID := strings.TrimSpace(selector.SessionID)
	taskID := strings.TrimSpace(string(selector.TaskID))
	projectID := strings.TrimSpace(selector.ProjectID)
	shortID := strings.TrimSpace(selector.ShortID)
	count := 0
	for _, value := range []string{runID, sessionID, taskID, shortID} {
		if value != "" {
			count++
		}
	}
	if count != 1 {
		return nil, errors.New("exactly one completion target selector is required")
	}
	var rows []sqlitegen.TaskRunRecord
	var err error
	switch {
	case runID != "":
		rows, err = s.queries.ResolveActiveRunCompletionTargetByRunID(ctx, runID)
	case sessionID != "":
		runs, sessionErr := s.sessionRunCompletionTargets(ctx, sessionID)
		if sessionErr != nil {
			return nil, sessionErr
		}
		matches := make([]RunRecord, 0, len(runs))
		for _, run := range runs {
			if run.InterruptedAt == nil {
				matches = append(matches, run)
			}
		}
		return matches, nil
	case taskID != "":
		rows, err = s.queries.ResolveActiveRunCompletionTargetByTaskID(ctx, taskID)
	case projectID != "":
		rows, err = s.queries.ResolveActiveRunCompletionTargetByProjectShortID(ctx, sqlitegen.ResolveActiveRunCompletionTargetByProjectShortIDParams{ShortID: shortID, ProjectID: projectID})
	default:
		rows, err = s.queries.ResolveActiveRunCompletionTargetByShortID(ctx, shortID)
	}
	if err != nil {
		return nil, err
	}
	return runRecordsFromTaskRunRecords(rows), nil
}

func (s *Store) sessionRunCompletionTargets(ctx context.Context, sessionID string) ([]RunRecord, error) {
	rows, err := s.queries.ResolveSessionRunCompletionTargets(
		ctx,
		sql.NullString{String: strings.TrimSpace(sessionID), Valid: true},
	)
	if err != nil {
		return nil, err
	}
	return runRecordsFromTaskRunRecords(rows), nil
}

func runRecordsFromTaskRunRecords(rows []sqlitegen.TaskRunRecord) []RunRecord {
	out := make([]RunRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, runRecordFromTaskRun(row))
	}
	return out
}
