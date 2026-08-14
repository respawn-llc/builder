package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/server/workflow/label"
	"core/shared/runtimeids"
)

type (
	TaskStartConflictReason string
	TaskStartConflictError  struct {
		TaskID workflow.TaskID
		Reason TaskStartConflictReason
	}
)

const TaskStartConflictAlreadyStarted TaskStartConflictReason = "already_started"

func (e TaskStartConflictError) Error() string {
	return fmt.Sprintf("task %q start conflict: %s", e.TaskID, e.Reason)
}

type CreateTaskRequest struct {
	ProjectID         string
	WorkflowID        *runtimeids.WorkflowID
	Title             string
	Body              string
	SourceURL         string
	SourceWorkspaceID string
	LabelIDs          []string
	DependencyIntents []workflow.TaskDependencyCreateIntent
}

type preparedTaskCreate struct {
	projectID         string
	workflowID        *runtimeids.WorkflowID
	title             string
	body              string
	sourceURL         string
	sourceWorkspaceID string
	taskID            string
	labelIDs          []label.ID
	dependencyIntents []workflow.TaskDependencyCreateIntent
	nowUnixMs         int64
}

type UpdateTaskRequest struct {
	TaskID            workflow.TaskID
	Title             *string
	Body              *string
	SourceWorkspaceID string
}

type StartTaskResult struct {
	Mutation workflow.CurrentNodeMutationResult
}

type TaskExecutionScope struct {
	ProjectID  string
	WorkflowID runtimeids.WorkflowID
}

func (s *Store) TaskExecutionScope(ctx context.Context, taskID workflow.TaskID) (TaskExecutionScope, error) {
	if strings.TrimSpace(string(taskID)) == "" {
		return TaskExecutionScope{}, errors.New("task id is required")
	}
	row, err := s.queries.GetTaskProjectWorkflowIDs(ctx, string(taskID))
	if err != nil {
		return TaskExecutionScope{}, err
	}
	if strings.TrimSpace(row.ProjectID) == "" || row.WorkflowID.IsZero() {
		return TaskExecutionScope{}, fmt.Errorf("task %q has incomplete execution scope", taskID)
	}
	return TaskExecutionScope{ProjectID: row.ProjectID, WorkflowID: row.WorkflowID}, nil
}

type CompletionValidationIssue struct {
	Code    string
	Field   string
	Message string
}

type CompletionValidationError struct {
	Issues []CompletionValidationIssue
}

func (e CompletionValidationError) Error() string {
	if len(e.Issues) == 0 {
		return "workflow completion is invalid"
	}
	parts := make([]string, 0, len(e.Issues))
	for _, issue := range e.Issues {
		if strings.TrimSpace(issue.Field) != "" {
			parts = append(parts, issue.Field+": "+issue.Message)
			continue
		}
		parts = append(parts, issue.Message)
	}
	return "workflow completion is invalid: " + strings.Join(parts, "; ")
}

type CompletionHandoff struct {
	SourceNodeDisplayName  string
	DestinationDisplayName string
}

type DeleteTaskResult struct {
	TaskRecord
	TaskAttentionResolution
}

type ManualMoveRequest struct {
	TaskID        workflow.TaskID
	TargetNodeID  workflow.NodeID
	TransitionKey *workflow.TransitionID
	Values        map[workflow.ModelKey]map[string]string
	Commentary    string
}

type ManualMoveResultOutcome string

const (
	ManualMoveResultOutcomeNoOp    ManualMoveResultOutcome = "no_op"
	ManualMoveResultOutcomeApplied ManualMoveResultOutcome = "applied"
)

type ManualMoveResult struct {
	Outcome      ManualMoveResultOutcome
	CurrentNodes []workflow.CurrentNode
	Mutation     workflow.CurrentNodeMutationResult
	TaskAttentionResolution
}

func (r ManualMoveResult) Validate() error {
	switch r.Outcome {
	case ManualMoveResultOutcomeNoOp:
		if len(r.CurrentNodes) == 0 {
			return errors.New("manual move no-op must return current nodes")
		}
		if len(r.Mutation.Removed) != 0 || len(r.Mutation.Created) != 0 {
			return errors.New("manual move no-op must not return a mutation")
		}
	case ManualMoveResultOutcomeApplied:
		if len(r.CurrentNodes) != 0 {
			return errors.New("applied manual move must not return no-op current nodes")
		}
		if len(r.Mutation.Removed) == 0 || len(r.Mutation.Created) == 0 {
			return errors.New("applied manual move must return a non-empty replacement mutation")
		}
	default:
		return fmt.Errorf("manual move result outcome %q is invalid", r.Outcome)
	}
	return nil
}

func (s *Store) CreateTask(ctx context.Context, req CreateTaskRequest) (TaskRecord, error) {
	projectID := strings.TrimSpace(req.ProjectID)
	if projectID == "" {
		return TaskRecord{}, errors.New("project id is required")
	}
	var workflowID *runtimeids.WorkflowID
	if req.WorkflowID != nil {
		if req.WorkflowID.IsZero() {
			return TaskRecord{}, errors.New("workflow id is required when provided")
		}
		value := *req.WorkflowID
		workflowID = &value
	}
	if len(req.LabelIDs) > label.MaxProjectLabels {
		limit := label.MaxProjectLabels
		return TaskRecord{}, TaskLabelMutationError{Reason: TaskLabelMutationTooManyAdd, Field: "label_ids", Limit: &limit}
	}
	roleCounts := map[workflow.TaskDependencyRole]int{}
	dependencyIntents := append([]workflow.TaskDependencyCreateIntent(nil), req.DependencyIntents...)
	for _, intent := range dependencyIntents {
		if strings.TrimSpace(string(intent.RelatedTaskID)) == "" {
			return TaskRecord{}, errors.New("dependency related task id is required")
		}
		switch intent.NewTaskRole {
		case workflow.TaskDependencyRoleBlocker, workflow.TaskDependencyRoleBlocked:
		default:
			return TaskRecord{}, errors.New("dependency new task role is invalid")
		}
		roleCounts[intent.NewTaskRole]++
		if roleCounts[intent.NewTaskRole] > workflow.MaxTaskDependencies {
			return TaskRecord{}, fmt.Errorf("dependency intents exceed the %d per-role limit", workflow.MaxTaskDependencies)
		}
	}
	labelIDs, _, err := parseUniqueLabelIDs(req.LabelIDs, "label_ids", TaskLabelMutationDuplicateAdd)
	if err != nil {
		return TaskRecord{}, err
	}
	prepared := preparedTaskCreate{
		projectID: projectID, workflowID: workflowID, title: strings.TrimSpace(req.Title),
		body: strings.TrimSpace(req.Body), sourceURL: strings.TrimSpace(req.SourceURL),
		sourceWorkspaceID: strings.TrimSpace(req.SourceWorkspaceID), taskID: prefixedID("task"),
		labelIDs: labelIDs, dependencyIntents: dependencyIntents, nowUnixMs: s.now().UnixMilli(),
	}
	if prepared.title == "" {
		return TaskRecord{}, errors.New("task title is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TaskRecord{}, taskCreateStoreError(err)
	}
	defer func() { _ = tx.Rollback() }()
	task, err := createTaskWithQueries(ctx, s.queries.WithTx(tx), prepared)
	if err != nil {
		return TaskRecord{}, taskCreateStoreError(err)
	}
	if err := tx.Commit(); err != nil {
		return TaskRecord{}, taskCreateStoreError(err)
	}
	return task, nil
}

func createTaskWithQueries(ctx context.Context, q *sqlitegen.Queries, prepared preparedTaskCreate) (TaskRecord, error) {
	link, err := resolveTaskWorkflowLinkWithQueries(ctx, q, prepared.projectID, prepared.workflowID)
	if err != nil {
		return TaskRecord{}, err
	}
	sourceWorkspaceID, err := resolveTaskSourceWorkspaceWithQueries(ctx, q, prepared.projectID, prepared.sourceWorkspaceID)
	if err != nil {
		return TaskRecord{}, err
	}
	definition, record, err := workflowDefinitionFromQueries(ctx, q, link.WorkflowID)
	if err != nil {
		return TaskRecord{}, err
	}
	start, err := startNode(definition)
	if err != nil {
		return TaskRecord{}, err
	}
	if err := validateTaskLabelReferences(ctx, q, workflow.TaskID(prepared.taskID), prepared.projectID, prepared.labelIDs); err != nil {
		return TaskRecord{}, err
	}
	allocated, err := q.AllocateProjectTaskSequence(ctx, sqlitegen.AllocateProjectTaskSequenceParams{ProjectID: prepared.projectID, UpdatedAtUnixMs: prepared.nowUnixMs})
	if err != nil {
		return TaskRecord{}, fmt.Errorf("allocate task sequence: %w", err)
	}
	sequence := allocated.NextTaskSeq - 1
	shortID := fmt.Sprintf("%s-%d", strings.TrimSpace(allocated.ProjectKey), sequence)
	metadataJSON, err := taskMetadataWithSourceWorkspaceSnapshot(ctx, q, "{}", sourceWorkspaceID)
	if err != nil {
		return TaskRecord{}, err
	}
	if err := q.InsertTask(ctx, sqlitegen.InsertTaskParams{
		ID: prepared.taskID, ProjectWorkflowLinkID: link.ID, WorkflowRevisionSeen: record.Version,
		TaskSeq: sequence, ShortID: shortID, Title: prepared.title, Body: prepared.body,
		SourceUrl: prepared.sourceURL, SourceWorkspaceID: nullableString(sourceWorkspaceID),
		ManagedWorktreeID:               sql.NullString{},
		PendingInitialManagedBranchName: nullableString(shortID),
		CreatedAtUnixMs:                 prepared.nowUnixMs,
		UpdatedAtUnixMs:                 prepared.nowUnixMs, MetadataJson: metadataJSON,
	}); err != nil {
		return TaskRecord{}, fmt.Errorf("insert task: %w", err)
	}
	currentNode, err := newBacklogCurrentNode(workflow.TaskID(prepared.taskID), workflow.NodeIDOf(start))
	if err != nil {
		return TaskRecord{}, err
	}
	if err := insertTaskCurrentNode(ctx, q, currentNode, time.UnixMilli(prepared.nowUnixMs).UTC()); err != nil {
		return TaskRecord{}, fmt.Errorf("insert task start current node: %w", err)
	}
	for _, id := range prepared.labelIDs {
		if err := q.InsertTaskLabelAssignment(ctx, sqlitegen.InsertTaskLabelAssignmentParams{TaskID: prepared.taskID, LabelID: id.String()}); err != nil {
			return TaskRecord{}, fmt.Errorf("insert task label: %w", err)
		}
	}
	for _, intent := range prepared.dependencyIntents {
		dependencyRequest := TaskDependencyAddRequest{
			BlockerTaskID: workflow.TaskID(prepared.taskID),
			BlockedTaskID: intent.RelatedTaskID,
		}
		if intent.NewTaskRole == workflow.TaskDependencyRoleBlocked {
			dependencyRequest.BlockerTaskID = intent.RelatedTaskID
			dependencyRequest.BlockedTaskID = workflow.TaskID(prepared.taskID)
		}
		decision, err := attachTaskDependencyWithQueries(ctx, q, dependencyRequest)
		if err != nil {
			return TaskRecord{}, fmt.Errorf("attach task dependency during task creation: %w", err)
		}
		if decision == workflow.TaskDependencyAttachAdded {
			for _, taskID := range []workflow.TaskID{dependencyRequest.BlockerTaskID, dependencyRequest.BlockedTaskID} {
				if err := touchTaskUpdatedAt(ctx, q, string(taskID), prepared.nowUnixMs); err != nil {
					return TaskRecord{}, fmt.Errorf("touch task %q after dependency creation: %w", taskID, err)
				}
			}
		}
	}
	return TaskRecord{
		ID: workflow.TaskID(prepared.taskID), ProjectID: prepared.projectID, WorkflowID: link.WorkflowID,
		LinkID: link.ID, ShortID: shortID, Title: prepared.title, Body: prepared.body,
		SourceURL: prepared.sourceURL, SourceWorkspaceID: sourceWorkspaceID,
		PendingInitialManagedBranchName: metadata.OptionalString(nullableString(shortID)),
		Version:                         record.Version,
	}, nil
}

func (s *Store) UpdateTask(ctx context.Context, req UpdateTaskRequest) (TaskRecord, error) {
	if strings.TrimSpace(string(req.TaskID)) == "" {
		return TaskRecord{}, errors.New("task id is required")
	}
	if req.Title != nil && strings.TrimSpace(*req.Title) == "" {
		return TaskRecord{}, errors.New("task title is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TaskRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	task, err := q.GetTask(ctx, string(req.TaskID))
	if err != nil {
		return TaskRecord{}, err
	}
	title, body := task.Title, task.Body
	if req.Title != nil {
		title = strings.TrimSpace(*req.Title)
	}
	if req.Body != nil {
		body = strings.TrimSpace(*req.Body)
	}
	sourceWorkspaceID := strings.TrimSpace(task.SourceWorkspaceID.String)
	metadataJSON := task.MetadataJson
	if requested := strings.TrimSpace(req.SourceWorkspaceID); requested != "" && requested != sourceWorkspaceID {
		backlog, err := taskCurrentPositionIsBacklog(ctx, q, workflow.TaskID(task.ID))
		if err != nil {
			return TaskRecord{}, err
		}
		if !backlog {
			return TaskRecord{}, ErrSourceWorkspaceAfterAutomation
		}
		if task.ManagedWorktreeID.Valid && strings.TrimSpace(task.ManagedWorktreeID.String) != "" {
			return TaskRecord{}, ErrSourceWorkspaceAfterAutomation
		}
		sessions, err := q.CountTaskSessions(ctx, sql.NullString{String: task.ID, Valid: true})
		if err != nil {
			return TaskRecord{}, err
		}
		if sessions != 0 {
			return TaskRecord{}, ErrSourceWorkspaceAfterAutomation
		}
		sourceWorkspaceID, err = resolveTaskSourceWorkspaceWithQueries(ctx, q, task.ProjectID, requested)
		if err != nil {
			return TaskRecord{}, err
		}
		metadataJSON, err = taskMetadataWithSourceWorkspaceSnapshot(ctx, q, task.MetadataJson, sourceWorkspaceID)
		if err != nil {
			return TaskRecord{}, err
		}
	}
	updated, err := q.UpdateTaskEditableFields(ctx, sqlitegen.UpdateTaskEditableFieldsParams{
		ID: task.ID, Title: title, Body: body, SourceWorkspaceID: nullableString(sourceWorkspaceID),
		MetadataJson: metadataJSON, UpdatedAtUnixMs: s.now().UnixMilli(),
	})
	if err != nil {
		return TaskRecord{}, fmt.Errorf("update task: %w", err)
	}
	if updated != 1 {
		return TaskRecord{}, sql.ErrNoRows
	}
	row, err := q.GetTask(ctx, task.ID)
	if err != nil {
		return TaskRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return TaskRecord{}, err
	}
	return taskRecordFromTask(row)
}

func (s *Store) DeleteTask(ctx context.Context, taskID workflow.TaskID) (DeleteTaskResult, error) {
	if strings.TrimSpace(string(taskID)) == "" {
		return DeleteTaskResult{}, errors.New("task id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DeleteTaskResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	task, err := q.GetTask(ctx, string(taskID))
	if err != nil {
		return DeleteTaskResult{}, err
	}
	record, err := taskRecordFromTask(task)
	if err != nil {
		return DeleteTaskResult{}, err
	}
	if _, err := q.AcquireTaskDependencyWriteLock(ctx, string(taskID)); err != nil {
		return DeleteTaskResult{}, fmt.Errorf("lock task dependency project for task deletion: %w", err)
	}
	neighbors, err := q.ListTaskDependencyNeighborIDs(ctx, string(taskID))
	if err != nil {
		return DeleteTaskResult{}, fmt.Errorf("list task dependency neighbors for task deletion: %w", err)
	}
	if _, err := q.DeleteTaskDependenciesByTask(ctx, string(taskID)); err != nil {
		return DeleteTaskResult{}, fmt.Errorf("delete task dependencies for task deletion: %w", err)
	}
	if len(neighbors) > 0 {
		touched, err := q.TouchTasksUpdatedAt(ctx, sqlitegen.TouchTasksUpdatedAtParams{
			UpdatedAtUnixMs: s.now().UnixMilli(),
			TaskIds:         neighbors,
		})
		if err != nil {
			return DeleteTaskResult{}, fmt.Errorf("touch task dependency neighbors after task deletion: %w", err)
		}
		if touched != int64(len(neighbors)) {
			return DeleteTaskResult{}, fmt.Errorf("touch task dependency neighbors affected %d rows, want %d", touched, len(neighbors))
		}
	}
	resolution, err := s.taskAttentionResolution(ctx, q, taskID)
	if err != nil {
		return DeleteTaskResult{}, err
	}
	if _, err := q.DeleteTaskPendingApprovalsByTask(ctx, string(taskID)); err != nil {
		return DeleteTaskResult{}, err
	}
	if _, err := q.DeleteTaskLabelAssignmentsByTask(ctx, string(taskID)); err != nil {
		return DeleteTaskResult{}, err
	}
	if _, err := q.DeleteTaskCurrentNodes(ctx, string(taskID)); err != nil {
		return DeleteTaskResult{}, err
	}
	if _, err := q.DeleteTaskActiveFanout(ctx, string(taskID)); err != nil {
		return DeleteTaskResult{}, err
	}
	if _, err := q.DeleteTaskCommentsByTask(ctx, string(taskID)); err != nil {
		return DeleteTaskResult{}, err
	}
	deleted, err := q.DeleteTask(ctx, string(taskID))
	if err != nil {
		return DeleteTaskResult{}, err
	}
	if deleted != 1 {
		return DeleteTaskResult{}, sql.ErrNoRows
	}
	if err := tx.Commit(); err != nil {
		return DeleteTaskResult{}, err
	}
	return DeleteTaskResult{TaskRecord: record, TaskAttentionResolution: resolution}, nil
}

func (s *Store) StartTask(ctx context.Context, taskID workflow.TaskID) (StartTaskResult, error) {
	return s.startTask(ctx, taskID, nil, false)
}

func (s *Store) StartTaskWithExecutionTarget(ctx context.Context, taskID workflow.TaskID, candidate *ExecutionTargetCandidate) (StartTaskResult, error) {
	return s.startTask(ctx, taskID, candidate, true)
}

func (s *Store) startTask(ctx context.Context, taskID workflow.TaskID, candidate *ExecutionTargetCandidate, requireTarget bool) (StartTaskResult, error) {
	prepared, err := s.prepareTaskStart(ctx, taskID)
	if err != nil {
		return StartTaskResult{}, err
	}
	var targetMutation preparedExecutionTargetMutation
	if requireTarget {
		targetMutation, err = s.prepareExecutionTargetMutation(ctx, prepared.task, candidate)
		if err != nil {
			return StartTaskResult{}, err
		}
	}
	executionRoot, err := executionRootForLockedTaskIfPresent(ctx, s.queries, prepared.task)
	if err != nil {
		return StartTaskResult{}, err
	}
	if prepared.target.Kind() == workflow.NodeKindScript {
		if err := s.validateScriptNodeForExecution(ctx, s.queries, workflow.NodeIDOf(prepared.target), executionRoot); err != nil {
			return StartTaskResult{}, err
		}
	}
	var targetSelection *workflow.AgentExecutionSelection
	if prepared.target.Kind() == workflow.NodeKindAgent {
		selectionPlan, selectionErr := workflow.PlanTransitionSelection(workflow.TransitionParameterContractRequest{
			Edge:       prepared.startEdge,
			SourceKind: prepared.start.Kind(),
			TargetKind: prepared.target.Kind(),
			TargetRole: workflow.NodeSubagentRole(prepared.target),
			Catalog:    s.roleResolver,
			Materialization: &workflow.TransitionSelectionMaterializationRequest{
				FallbackRole: workflow.NodeSubagentRole(prepared.target),
			},
		})
		var value workflow.AgentExecutionSelection
		if selectionErr == nil && selectionPlan.ExecutionSelection != nil {
			value = *selectionPlan.ExecutionSelection
		} else if selectionErr == nil {
			selectionErr = errors.New("transition selection planner omitted Agent execution selection")
		}
		if selectionErr != nil {
			return StartTaskResult{}, fmt.Errorf("materialize Agent target selection: %w", selectionErr)
		}
		targetSelection = &value
	}
	target, err := newReadyCurrentNode(taskID, workflow.NodeIDOf(prepared.target), prepared.startEdge.ID, targetSelection)
	if err != nil {
		return StartTaskResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return StartTaskResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	nowTime := s.now().UTC()
	now := nowTime.UnixMilli()
	if requireTarget {
		if err := applyPreparedExecutionTargetMutation(ctx, q, prepared.task, targetMutation, now); err != nil {
			return StartTaskResult{}, err
		}
	}
	removed, err := q.DeleteSerialTaskCurrentNode(ctx, sqlitegen.DeleteSerialTaskCurrentNodeParams{TaskID: string(taskID), NodeID: string(workflow.NodeIDOf(prepared.start))})
	if err != nil {
		return StartTaskResult{}, err
	}
	if removed != 1 {
		return StartTaskResult{}, sql.ErrNoRows
	}
	if err := insertTaskCurrentNode(ctx, q, target, nowTime); err != nil {
		return StartTaskResult{}, err
	}
	if err := touchTaskUpdatedAt(ctx, q, string(taskID), now); err != nil {
		return StartTaskResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return StartTaskResult{}, err
	}
	return StartTaskResult{Mutation: workflow.CurrentNodeMutationResult{
		Removed: []workflow.CurrentNodeReference{prepared.startCurrentNode.Reference},
		Created: []workflow.CurrentNode{target},
	}}, nil
}

func (s *Store) ValidateTaskStart(ctx context.Context, taskID workflow.TaskID) error {
	_, err := s.prepareTaskStart(ctx, taskID)
	return err
}

type preparedTaskStart struct {
	task             sqlitegen.TaskRecord
	start            workflow.Node
	target           workflow.Node
	startEdge        workflow.Edge
	startCurrentNode workflow.CurrentNode
}

func (s *Store) prepareTaskStart(ctx context.Context, taskID workflow.TaskID) (preparedTaskStart, error) {
	task, err := s.queries.GetTask(ctx, string(taskID))
	if err != nil {
		return preparedTaskStart{}, err
	}
	definition, _, err := s.GetDefinition(ctx, task.WorkflowID)
	if err != nil {
		return preparedTaskStart{}, err
	}
	start, err := startNode(definition)
	if err != nil {
		return preparedTaskStart{}, err
	}
	reference, err := workflow.NewCurrentNodeReference(taskID, workflow.NodeIDOf(start), nil)
	if err != nil {
		return preparedTaskStart{}, err
	}
	current, err := s.currentNodeForReference(ctx, s.queries, reference)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return preparedTaskStart{}, TaskStartConflictError{TaskID: taskID, Reason: TaskStartConflictAlreadyStarted}
		}
		return preparedTaskStart{}, err
	}
	if current.SessionID != nil || current.Scheduling != nil {
		return preparedTaskStart{}, sql.ErrNoRows
	}
	if err := s.preflightInitialExecution(definition); err != nil {
		return preparedTaskStart{}, err
	}
	_, edge, target, err := startTransition(definition, workflow.NodeIDOf(start))
	if err != nil {
		return preparedTaskStart{}, err
	}
	return preparedTaskStart{task: task, start: start, target: target, startEdge: edge, startCurrentNode: current}, nil
}

func (s *Store) preflightInitialExecution(definition workflow.Definition) error {
	validation := workflow.ValidateDefinition(definition, workflow.ValidationOptions{Context: workflow.ValidationContextExecution, RoleResolver: s.roleResolver})
	if !validation.HasBlockingErrors() {
		return nil
	}
	return WorkflowValidationError{Diagnostics: validation.BlockingErrors()}
}

func touchTaskUpdatedAt(ctx context.Context, q *sqlitegen.Queries, taskID string, now int64) error {
	updated, err := q.TouchTaskUpdatedAt(ctx, sqlitegen.TouchTaskUpdatedAtParams{UpdatedAtUnixMs: now, TaskID: taskID})
	if err != nil {
		return fmt.Errorf("update task timestamp: %w", err)
	}
	if updated != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func advanceTaskUpdatedAt(ctx context.Context, q *sqlitegen.Queries, taskID string, now int64) error {
	updated, err := q.AdvanceTaskUpdatedAt(ctx, sqlitegen.AdvanceTaskUpdatedAtParams{UpdatedAtUnixMs: now, TaskID: taskID})
	if err != nil {
		return fmt.Errorf("advance task timestamp: %w", err)
	}
	if updated != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func taskRecordFromTask(row sqlitegen.TaskRecord) (TaskRecord, error) {
	target, err := executionTargetSnapshotFromTask(row)
	if err != nil {
		return TaskRecord{}, err
	}
	managedWorktreeID := metadata.OptionalString(row.ManagedWorktreeID)
	return TaskRecord{
		ID: workflow.TaskID(row.ID), ProjectID: row.ProjectID, WorkflowID: row.WorkflowID,
		LinkID: row.ProjectWorkflowLinkID, ShortID: row.ShortID, Title: row.Title, Body: row.Body,
		SourceURL: row.SourceUrl, SourceWorkspaceID: strings.TrimSpace(row.SourceWorkspaceID.String),
		ManagedWorktreeID:               managedWorktreeID,
		PendingInitialManagedBranchName: metadata.OptionalString(row.PendingInitialManagedBranchName),
		ExecutionTarget:                 target,
		Version:                         row.WorkflowRevisionSeen,
	}, nil
}

func resolveTaskSourceWorkspaceWithQueries(ctx context.Context, q *sqlitegen.Queries, projectID, workspaceID string) (string, error) {
	if strings.TrimSpace(projectID) == "" {
		return "", errors.New("project id is required")
	}
	if workspaceID = strings.TrimSpace(workspaceID); workspaceID != "" {
		workspace, err := q.GetWorkspaceByID(ctx, workspaceID)
		if err != nil {
			return "", fmt.Errorf("source workspace %q: %w", workspaceID, err)
		}
		if strings.TrimSpace(workspace.ProjectID) != strings.TrimSpace(projectID) {
			return "", fmt.Errorf("source workspace %q does not belong to project %q: %w", workspaceID, projectID, ErrSourceWorkspaceNotInProject)
		}
		return workspaceID, nil
	}
	return metadata.ResolveProjectSourceWorkspaceID(ctx, q, projectID)
}

func taskCurrentPositionIsBacklog(ctx context.Context, q *sqlitegen.Queries, taskID workflow.TaskID) (bool, error) {
	currentNodes, err := q.ListTaskCurrentNodes(ctx, string(taskID))
	if err != nil {
		return false, err
	}
	if len(currentNodes) != 1 {
		return false, nil
	}
	node, err := q.GetWorkflowNode(ctx, currentNodes[0].NodeID)
	if err != nil {
		return false, err
	}
	return workflow.NodeKind(node.Kind) == workflow.NodeKindStart, nil
}

func taskMetadataWithSourceWorkspaceSnapshot(ctx context.Context, q *sqlitegen.Queries, currentMetadata, sourceWorkspaceID string) (string, error) {
	payload := map[string]any{}
	if strings.TrimSpace(currentMetadata) != "" {
		if err := workflow.UnmarshalString(currentMetadata, &payload); err != nil {
			return "", fmt.Errorf("decode task metadata json: %w", err)
		}
	}
	if sourceWorkspaceID = strings.TrimSpace(sourceWorkspaceID); sourceWorkspaceID == "" {
		delete(payload, "source_workspace_snapshot")
		return workflow.MarshalString(payload)
	}
	workspace, err := q.GetWorkspaceByID(ctx, sourceWorkspaceID)
	if err != nil {
		return "", fmt.Errorf("source workspace snapshot %q: %w", sourceWorkspaceID, err)
	}
	payload["source_workspace_snapshot"] = map[string]string{
		"workspace_id": workspace.ID, "display_name": workspaceSnapshotDisplayName(workspace.CanonicalRootPath), "root_path": workspace.CanonicalRootPath,
	}
	return workflow.MarshalString(payload)
}

func workspaceSnapshotDisplayName(rootPath string) string {
	if rootPath = strings.TrimSpace(rootPath); rootPath == "" {
		return ""
	}
	base := filepath.Base(filepath.Clean(rootPath))
	if base == "." || base == string(filepath.Separator) {
		return ""
	}
	return base
}

func startNode(definition workflow.Definition) (workflow.Node, error) {
	for _, node := range definition.Nodes {
		if node.Kind() == workflow.NodeKindStart {
			return node, nil
		}
	}
	return nil, errors.New("workflow has no start node")
}

func startTransition(definition workflow.Definition, startNodeID workflow.NodeID) (workflow.TransitionGroup, workflow.Edge, workflow.Node, error) {
	var groups []workflow.TransitionGroup
	for _, group := range definition.TransitionGroups {
		if group.SourceNodeID == startNodeID {
			groups = append(groups, group)
		}
	}
	if len(groups) != 1 {
		return workflow.TransitionGroup{}, workflow.Edge{}, nil, errors.New("start node must have exactly one transition group")
	}
	var edges []workflow.Edge
	for _, edge := range definition.Edges {
		if edge.TransitionGroupID == groups[0].ID {
			edges = append(edges, edge)
		}
	}
	if len(edges) != 1 {
		return workflow.TransitionGroup{}, workflow.Edge{}, nil, errors.New("start transition group must have exactly one edge")
	}
	for _, node := range definition.Nodes {
		if workflow.NodeIDOf(node) == edges[0].TargetNodeID {
			return groups[0], edges[0], node, nil
		}
	}
	return workflow.TransitionGroup{}, workflow.Edge{}, nil, errors.New("start transition target missing")
}
