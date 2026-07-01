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

func (s *Store) ListRunnableRuns(ctx context.Context, limit int64) ([]RunnableRunRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.queries.ListRunnableWorkflowRuns(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]RunnableRunRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, RunnableRunRecord{RunRecord: runRecordFromTaskRun(row), WorkflowRevisionSeen: row.WorkflowRevisionSeen})
	}
	return out, nil
}

func (s *Store) ClaimRun(ctx context.Context, runID workflow.RunID, expectedGeneration int64) (RunnableRunRecord, error) {
	now := s.now().UnixMilli()
	row, err := s.queries.ClaimWorkflowRun(ctx, sqlitegen.ClaimWorkflowRunParams{ID: string(runID), ExpectedGeneration: expectedGeneration, UpdatedAtUnixMs: now, StartedAtUnixMs: now})
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
		EffectiveCompletionMode: trimmedMode,
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
	updated, err := s.queries.InterruptWorkflowRun(ctx, sqlitegen.InterruptWorkflowRunParams{ID: string(runID), UpdatedAtUnixMs: now, InterruptedAtUnixMs: now, InterruptionReason: strings.TrimSpace(reason), InterruptionDetailJson: detailJSON})
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
		InterruptedAtUnixMs:    now,
		InterruptionReason:     strings.TrimSpace(reason),
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
		updated, err := q.InterruptWorkflowRun(ctx, sqlitegen.InterruptWorkflowRunParams{ID: string(candidate.ID), UpdatedAtUnixMs: now, InterruptedAtUnixMs: now, InterruptionReason: interruptReason, InterruptionDetailJson: "{}"})
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
		updated, err := q.InterruptWorkflowRun(ctx, sqlitegen.InterruptWorkflowRunParams{ID: string(candidate.ID), UpdatedAtUnixMs: now, InterruptedAtUnixMs: now, InterruptionReason: strings.TrimSpace(reason), InterruptionDetailJson: "{}"})
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

func (s *Store) ResumeTaskRuns(ctx context.Context, taskID workflow.TaskID) ([]RunRecord, error) {
	if strings.TrimSpace(string(taskID)) == "" {
		return nil, errors.New("task id is required")
	}
	task, err := s.queries.GetTask(ctx, string(taskID))
	if err != nil {
		return nil, err
	}
	if task.CanceledAtUnixMs != 0 {
		return nil, ErrTaskCanceled
	}
	candidates, err := s.queries.ListResumeTaskRunCandidates(ctx, string(taskID))
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, errors.New("task has no interrupted workflow run to resume")
	}
	for _, candidate := range candidates {
		snapshot := runStartSnapshot{}
		if err := workflow.UnmarshalString(candidate.RunStartSnapshotJson, &snapshot); err != nil {
			return nil, err
		}
		if err := s.validateRunnableRole(snapshot.Node.SubagentRole); err != nil {
			return nil, err
		}
	}
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	resumed := make([]RunRecord, 0, len(candidates))
	for _, candidate := range candidates {
		updated, err := q.ResumeTaskRun(ctx, sqlitegen.ResumeTaskRunParams{UpdatedAtUnixMs: now, RunID: candidate.ID})
		if err != nil {
			return nil, err
		}
		if updated != 1 {
			return nil, sql.ErrNoRows
		}
		run, err := q.GetTaskRun(ctx, candidate.ID)
		if err != nil {
			return nil, err
		}
		resumed = append(resumed, runRecordFromTaskRun(run))
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return resumed, nil
}

func (s *Store) validateRunnableRole(role string) error {
	trimmed := strings.TrimSpace(role)
	if trimmed == "" {
		return WorkflowValidationError{Codes: []workflow.ValidationErrorCode{workflow.CodeAgentRoleRequired}}
	}
	if workflow.IsDefaultAgentRole(trimmed) {
		return nil
	}
	if s.roleResolver != nil && !s.roleResolver.RoleExists(trimmed) {
		return WorkflowValidationError{Codes: []workflow.ValidationErrorCode{workflow.CodeAgentRoleMissing}}
	}
	return nil
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
	inputResolution, err := s.resolveRunInputContext(ctx, run.PlacementID, taskRecordFromTask(task))
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
	parameterValues := map[string]string{}
	for key, value := range inputValues {
		parameterValues[key] = value
	}
	if _, exists := parameterValues[workflow.RuntimePromptParameterCommentary]; !exists {
		parameterValues[workflow.RuntimePromptParameterCommentary] = inputResolution.Commentary
	}
	priorParameterValues := clonePriorParameterValues(runMetadata.PriorParameterValues)
	parameters := append([]workflow.Parameter(nil), runMetadata.Parameters...)
	worktreeID := strings.TrimSpace(task.ManagedWorktreeID.String)
	if worktreeID == "" {
		return RunStartContext{
			Run:                            runRecordFromTaskRun(run),
			Task:                           taskRecordFromTask(task),
			Workflow:                       workflowRecord,
			Node:                           nodeRecordFromSnapshot(snapshot.Node, snapshot.WorkflowID),
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
		}, nil
	}
	worktree, err := s.metadata.GetWorktreeRecordByID(ctx, worktreeID)
	if err != nil {
		return RunStartContext{}, err
	}
	workspace, err := s.metadata.GetWorkspaceByID(ctx, worktree.WorkspaceID)
	if err != nil {
		return RunStartContext{}, err
	}
	return RunStartContext{
		Run:                            runRecordFromTaskRun(run),
		Task:                           taskRecordFromTask(task),
		Workflow:                       workflowRecord,
		Node:                           nodeRecordFromSnapshot(snapshot.Node, snapshot.WorkflowID),
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
		WorkspaceID:                    workspace.ID,
		WorkspaceRoot:                  workspace.CanonicalRootPath,
		WorktreeID:                     worktree.ID,
		WorktreeRoot:                   worktree.CanonicalRoot,
	}, nil
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
		AskID:           trimmedAskID,
		RunID:           string(runID),
		RunGeneration:   expectedGeneration,
	})
	if err != nil {
		return fmt.Errorf("set workflow run waiting ask: %w", err)
	}
	if updated != 1 {
		return sql.ErrNoRows
	}
	event, err := runWaitingAskWorkflowEvent(ctx, s.queries, string(runID), "question_waiting", trimmedAskID, now)
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
		AskID:           trimmedAskID,
	})
	if err != nil {
		return fmt.Errorf("clear workflow run waiting ask: %w", err)
	}
	if updated != 1 {
		return sql.ErrNoRows
	}
	event, err := runWaitingAskWorkflowEvent(ctx, s.queries, string(runID), "question_cleared", trimmedAskID, now)
	if err != nil {
		return err
	}
	return s.PublishWorkflowEvent(ctx, event)
}

func runWaitingAskWorkflowEvent(
	ctx context.Context,
	q *sqlitegen.Queries,
	runID string,
	action string,
	askID string,
	occurredAtUnixMs int64,
) (WorkflowEventRecord, error) {
	row, err := q.GetRunWaitingAskEventIdentity(ctx, strings.TrimSpace(runID))
	if err != nil {
		return WorkflowEventRecord{}, fmt.Errorf("load waiting ask event run identity: %w", err)
	}
	return WorkflowEventRecord{
		ProjectID:        row.ProjectID,
		WorkflowID:       row.WorkflowID,
		Resource:         "task",
		Action:           action,
		ChangedIDs:       []string{row.TaskID, strings.TrimSpace(runID), strings.TrimSpace(askID)},
		OccurredAtUnixMs: occurredAtUnixMs,
	}, nil
}

func (s *Store) ResolveTaskWaitingAsk(ctx context.Context, taskID workflow.TaskID, runID workflow.RunID, askID string) (RunRecord, error) {
	trimmedTaskID := strings.TrimSpace(string(taskID))
	trimmedRunID := strings.TrimSpace(string(runID))
	trimmedAskID := strings.TrimSpace(askID)
	if trimmedTaskID == "" {
		return RunRecord{}, errors.New("task id is required")
	}
	if trimmedAskID == "" {
		return RunRecord{}, errors.New("ask id is required")
	}
	rows, err := s.queries.ResolveTaskWaitingAsk(ctx, sqlitegen.ResolveTaskWaitingAskParams{
		TaskID: trimmedTaskID,
		AskID:  trimmedAskID,
		RunID:  trimmedRunID,
	})
	if err != nil {
		return RunRecord{}, err
	}
	matches := runRecordsFromTaskRunRecords(rows)
	if len(matches) == 0 {
		return RunRecord{}, ErrTaskAskNotPending
	}
	if trimmedRunID == "" && len(matches) != 1 {
		return RunRecord{}, fmt.Errorf("task has multiple matching pending asks; %w", ErrRunIDRequired)
	}
	return matches[0], nil
}

func (s *Store) ResolveActiveRunCompletionTarget(ctx context.Context, selector ActiveRunCompletionTargetSelector) (ActiveRunCompletionTarget, error) {
	matches, err := s.activeRunCompletionTargetMatches(ctx, selector)
	if err != nil {
		return ActiveRunCompletionTarget{}, err
	}
	if len(matches) == 0 {
		return ActiveRunCompletionTarget{}, sql.ErrNoRows
	}
	if len(matches) != 1 {
		return ActiveRunCompletionTarget{}, fmt.Errorf("selector matched multiple active workflow runs; %w", ErrRunIDRequired)
	}
	return ActiveRunCompletionTarget{Run: matches[0]}, nil
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
		rows, err = s.queries.ResolveActiveRunCompletionTargetBySessionID(ctx, sql.NullString{String: sessionID, Valid: true})
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

func runRecordsFromTaskRunRecords(rows []sqlitegen.TaskRunRecord) []RunRecord {
	out := make([]RunRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, runRecordFromTaskRun(row))
	}
	return out
}
