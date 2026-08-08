package workflowview

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/clientui"
	"core/shared/serverapi"
)

type TaskDetail struct {
	queries      *sqlitegen.Queries
	projection   *TaskStatusProjection
	dependencies *TaskDependencies
}

func NewTaskDetail(metadataStore *metadata.Store, projection *TaskStatusProjection, dependencies *TaskDependencies) (*TaskDetail, error) {
	if metadataStore == nil || metadataStore.Queries() == nil {
		return nil, errors.New("metadata store is required")
	}
	if projection == nil {
		return nil, errors.New("task status projection is required")
	}
	if dependencies == nil {
		return nil, errors.New("task dependencies read model is required")
	}
	return &TaskDetail{
		queries:      metadataStore.Queries(),
		projection:   projection,
		dependencies: dependencies,
	}, nil
}

func (d *TaskDetail) GetTask(ctx context.Context, taskID string) (serverapi.WorkflowTaskDetail, error) {
	if d == nil {
		return serverapi.WorkflowTaskDetail{}, errors.New("task detail is required")
	}
	if strings.TrimSpace(taskID) == "" {
		return serverapi.WorkflowTaskDetail{}, ErrTaskIDRequired
	}
	task, err := d.queries.GetTask(ctx, taskID)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	return d.task(ctx, task)
}

func (d *TaskDetail) ListCurrentNodes(ctx context.Context, taskID string) ([]workflow.CurrentNode, error) {
	if d == nil || d.queries == nil {
		return nil, errors.New("task detail is required")
	}
	if strings.TrimSpace(taskID) == "" {
		return nil, ErrTaskIDRequired
	}
	nodesByTask, err := workflowstore.ListCurrentNodesByTaskWithQueries(ctx, d.queries, []workflow.TaskID{workflow.TaskID(taskID)})
	if err != nil {
		return nil, err
	}
	return nodesByTask[workflow.TaskID(taskID)], nil
}

func (d *TaskDetail) GetTaskByProjectShortID(ctx context.Context, projectID string, shortID string) (serverapi.WorkflowTaskDetail, error) {
	if d == nil {
		return serverapi.WorkflowTaskDetail{}, errors.New("task detail is required")
	}
	trimmedProjectID := strings.TrimSpace(projectID)
	if trimmedProjectID == "" {
		return serverapi.WorkflowTaskDetail{}, errors.New("project_id is required")
	}
	trimmedShortID := strings.TrimSpace(shortID)
	if trimmedShortID == "" {
		return serverapi.WorkflowTaskDetail{}, errors.New("short_id is required")
	}
	task, err := d.queries.GetTaskByProjectShortID(ctx, sqlitegen.GetTaskByProjectShortIDParams{ProjectID: trimmedProjectID, ShortID: trimmedShortID})
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	return d.task(ctx, task)
}

func (d *TaskDetail) GetTaskByShortID(ctx context.Context, shortID string) (serverapi.WorkflowTaskDetail, error) {
	if d == nil {
		return serverapi.WorkflowTaskDetail{}, errors.New("task detail is required")
	}
	trimmedShortID := strings.TrimSpace(shortID)
	if trimmedShortID == "" {
		return serverapi.WorkflowTaskDetail{}, errors.New("short_id is required")
	}
	tasks, err := d.queries.ListTasksByShortID(ctx, trimmedShortID)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	if len(tasks) == 0 {
		return serverapi.WorkflowTaskDetail{}, sql.ErrNoRows
	}
	if len(tasks) > 1 {
		return serverapi.WorkflowTaskDetail{}, fmt.Errorf("task short_id %q is ambiguous; use task id", trimmedShortID)
	}
	return d.GetTask(ctx, tasks[0].ID)
}

func (d *TaskDetail) task(ctx context.Context, task sqlitegen.TaskRecord) (serverapi.WorkflowTaskDetail, error) {
	taskID := workflow.TaskID(task.ID)
	var projected TaskStatusProjectionResult
	var projectedDependencies serverapi.WorkflowTaskDependencies
	if err := d.projection.WithSnapshot(ctx, []workflow.TaskID{taskID}, func(observation TaskStatusObservation, durable *TaskStatusDurableSnapshot) error {
		results, err := d.projection.Project(ctx, observation, durable, []workflow.TaskID{taskID})
		if err != nil {
			return err
		}
		var ok bool
		projected, ok = results[taskID]
		if !ok {
			return fmt.Errorf("task status projection omitted Task %q", task.ID)
		}
		projectedDependencies, err = d.dependencies.projectTaskDependenciesWithSnapshot(ctx, task.ID, observation, durable)
		return err
	}); err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	task = projected.Task
	definition := projected.Definition
	labelIDsByTask, err := loadTaskLabelIDsByTask(ctx, d.queries, []string{task.ID})
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	projectState, err := d.queries.GetProjectKeyState(ctx, task.ProjectID)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	workspaceCount, err := d.queries.CountProjectWorkspaces(ctx, task.ProjectID)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	primaryWorkspaceID, err := d.queries.GetProjectPrimaryWorkspaceID(ctx, task.ProjectID)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	sourceWorkspace, err := d.sourceWorkspace(ctx, task, primaryWorkspaceID)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	executionTarget, worktreePath, err := d.executionTargetForTask(ctx, task, sourceWorkspace.WorkspaceID)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	retainedSessionCount, err := d.queries.CountTaskSessions(ctx, sql.NullString{String: task.ID, Valid: true})
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}

	detail := serverapi.WorkflowTaskDetail{
		Summary: taskSummary(task, projected.Status, projected.Done),
		Project: serverapi.ProjectBoardProject{
			ProjectKey:             projectState.ProjectKey,
			DisplayName:            projectState.DisplayName,
			DefaultWorkspaceID:     primaryWorkspaceID,
			AttachedWorkspaceCount: int(workspaceCount),
		},
		Workflow: serverapi.WorkflowTaskWorkflowSummary{
			WorkflowID:  definition.api.Workflow.ID,
			DisplayName: definition.api.Workflow.Name,
			Version:     definition.api.Workflow.Version,
		},
		Body:                 task.Body,
		SourceURL:            task.SourceUrl,
		SourceWorkspace:      sourceWorkspace,
		ExecutionTarget:      executionTarget,
		WorktreePath:         worktreePath,
		CurrentNodes:         ProjectCurrentNodes(projected.CurrentNodes),
		LiveSessions:         append(make([]serverapi.WorkflowTaskLiveSession, 0, len(projected.LiveSessions)), projected.LiveSessions...),
		CurrentScripts:       append(make([]serverapi.WorkflowTaskCurrentScript, 0, len(projected.CurrentScripts)), projected.CurrentScripts...),
		RetainedSessionCount: int(retainedSessionCount),
		Status:               projected.Status,
		Actions:              projected.Actions,
		LabelIDs:             labelIDsByTask[task.ID],
		AttentionCount:       projected.AttentionCount,
	}
	detail.Dependencies = projectedDependencies
	return detail, nil
}

func (d *TaskDetail) sourceWorkspace(ctx context.Context, task sqlitegen.TaskRecord, primaryWorkspaceID string) (serverapi.ProjectWorkspaceSummary, error) {
	sourceWorkspaceID := primaryWorkspaceID
	if task.SourceWorkspaceID.Valid {
		sourceWorkspaceID = task.SourceWorkspaceID.String
	}
	row, err := d.queries.GetWorkspaceByID(ctx, sourceWorkspaceID)
	if err == nil {
		if row.ProjectID != task.ProjectID {
			return serverapi.ProjectWorkspaceSummary{}, fmt.Errorf("task %q source workspace %q belongs to project %q", task.ID, row.ID, row.ProjectID)
		}
		displayName := displayNameForPath(row.CanonicalRootPath)
		if strings.TrimSpace(row.ID) == "" || strings.TrimSpace(row.CanonicalRootPath) == "" || displayName == "" {
			return serverapi.ProjectWorkspaceSummary{}, fmt.Errorf("task %q source workspace %q has invalid durable identity", task.ID, row.ID)
		}
		return serverapi.ProjectWorkspaceSummary{
			WorkspaceID:     row.ID,
			DisplayName:     displayName,
			RootPath:        row.CanonicalRootPath,
			Availability:    string(clientui.ProjectAvailabilityAvailable),
			IsPrimary:       row.ID == primaryWorkspaceID,
			UpdatedAtUnixMs: row.UpdatedAtUnixMs,
		}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return serverapi.ProjectWorkspaceSummary{}, err
	}
	snapshot := sourceWorkspaceForTask(task, nil, serverapi.ProjectWorkspaceSummary{})
	if strings.TrimSpace(snapshot.RootPath) == "" {
		return serverapi.ProjectWorkspaceSummary{}, err
	}
	if strings.TrimSpace(snapshot.WorkspaceID) == "" {
		return serverapi.ProjectWorkspaceSummary{}, fmt.Errorf("task %q source workspace snapshot has no workspace id", task.ID)
	}
	if strings.TrimSpace(snapshot.DisplayName) == "" {
		snapshot.DisplayName = displayNameForPath(snapshot.RootPath)
	}
	return snapshot, nil
}

func (d *TaskDetail) executionTargetForTask(ctx context.Context, task sqlitegen.TaskRecord, sourceWorkspaceID string) (*serverapi.WorkflowExecutionTarget, *string, error) {
	if !task.ExecutionTargetMode.Valid {
		if task.ExecutionTargetRequestedRef.Valid ||
			task.ExecutionTargetResolvedRef.Valid ||
			task.ExecutionTargetCommitOid.Valid ||
			task.ExecutionTargetProvenance.Valid ||
			task.ManagedWorktreeID.Valid {
			return nil, nil, errors.New("unlocked task has execution target facts")
		}
		return nil, nil, nil
	}
	target := &serverapi.WorkflowExecutionTarget{
		Mode:         serverapi.WorkflowExecutionTargetMode(task.ExecutionTargetMode.String),
		RequestedRef: metadata.OptionalString(task.ExecutionTargetRequestedRef),
		ResolvedRef:  metadata.OptionalString(task.ExecutionTargetResolvedRef),
		CommitOID:    metadata.OptionalString(task.ExecutionTargetCommitOid),
		Provenance:   serverapi.WorkflowExecutionTargetProvenance(task.ExecutionTargetProvenance.String),
	}
	if err := target.Validate(); err != nil {
		return nil, nil, fmt.Errorf("project task execution target: %w", err)
	}
	if !task.ManagedWorktreeID.Valid {
		return target, nil, nil
	}
	worktreeID := strings.TrimSpace(task.ManagedWorktreeID.String)
	if worktreeID == "" {
		return nil, nil, errors.New("task managed worktree id is blank")
	}
	row, err := d.queries.GetWorktreeByID(ctx, worktreeID)
	if err != nil {
		return nil, nil, err
	}
	if row.WorkspaceID != sourceWorkspaceID {
		return nil, nil, fmt.Errorf("task %q managed worktree %q belongs to workspace %q", task.ID, row.ID, row.WorkspaceID)
	}
	path := strings.TrimSpace(row.CanonicalRootPath)
	if path == "" {
		return nil, nil, errors.New("task managed worktree path is blank")
	}
	return target, &path, nil
}

func displayNameForPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	return filepath.Base(filepath.Clean(trimmed))
}
