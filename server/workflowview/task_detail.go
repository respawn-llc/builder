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
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/shared/clientui"
	"core/shared/serverapi"
)

type TaskDetail struct {
	queries   *sqlitegen.Queries
	projector *TaskProjector
	authority *sessionruntime.Authority
}

func NewTaskDetail(metadataStore *metadata.Store, projector *TaskProjector, authority *sessionruntime.Authority) (*TaskDetail, error) {
	if metadataStore == nil || metadataStore.Queries() == nil {
		return nil, errors.New("metadata store is required")
	}
	if projector == nil {
		return nil, errors.New("task projector is required")
	}
	if authority == nil {
		return nil, errors.New("session runtime authority is required")
	}
	return &TaskDetail{
		queries:   metadataStore.Queries(),
		projector: projector,
		authority: authority,
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
	workflowRecord, err := d.queries.GetWorkflow(ctx, task.WorkflowID)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	statusFact, err := loadWorkflowTaskStatusFact(ctx, d.queries, d.projector, task.ID)
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
	link, err := d.queries.GetActiveProjectWorkflowLinkByWorkflow(ctx, sqlitegen.GetActiveProjectWorkflowLinkByWorkflowParams{
		ProjectID:  task.ProjectID,
		WorkflowID: task.WorkflowID,
	})
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	executionTarget, worktreePath, err := d.executionTargetForTask(ctx, task, sourceWorkspace.WorkspaceID)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	targets, err := d.authority.CurrentTaskExecutionTargets(workflow.TaskID(task.ID))
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	attentionCount, err := d.queries.CountWorkflowTaskAttentionCandidates(ctx, task.ID)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}

	detail := serverapi.WorkflowTaskDetail{
		Summary: taskSummary(task, statusFact.Status, statusFact.Done),
		Project: serverapi.ProjectBoardProject{
			ProjectKey:             projectState.ProjectKey,
			DisplayName:            projectState.DisplayName,
			DefaultWorkspaceID:     primaryWorkspaceID,
			AttachedWorkspaceCount: int(workspaceCount),
		},
		Workflow: serverapi.WorkflowPickerItem{
			WorkflowID:           workflowRecord.ID,
			DisplayName:          workflowRecord.Name,
			Description:          workflowRecord.Description,
			Version:              workflowRecord.Version,
			IsProjectDefault:     link.IsDefault != 0,
			ValidForTaskCreation: true,
		},
		Body:              task.Body,
		SourceURL:         task.SourceUrl,
		SourceWorkspace:   sourceWorkspace,
		ExecutionTarget:   executionTarget,
		WorktreePath:      worktreePath,
		CurrentSessionIDs: make([]string, 0, len(targets.SessionIDs)),
		CurrentScripts:    make([]serverapi.WorkflowTaskCurrentScript, 0, len(targets.Scripts)),
		Status:            statusFact.Status,
		Actions:           taskDetailActions(task, statusFact, targets.HasExecutions),
		AttentionCount:    int(attentionCount),
	}
	for _, sessionID := range targets.SessionIDs {
		detail.CurrentSessionIDs = append(detail.CurrentSessionIDs, sessionID.String())
	}
	for _, script := range targets.Scripts {
		detail.CurrentScripts = append(detail.CurrentScripts, serverapi.WorkflowTaskCurrentScript{
			RunID: string(script.RunID),
			Path:  script.Path,
		})
	}
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

func taskDetailActions(task sqlitegen.TaskRecord, status workflowTaskStatusFact, hasLiveExecutions bool) serverapi.WorkflowTaskActions {
	active := !task.CanceledAtUnixMs.Valid && !status.Done
	return serverapi.WorkflowTaskActions{
		CanStart:     active && !hasLiveExecutions && status.Status.Kind == serverapi.WorkflowTaskStatusKindBacklog,
		CanInterrupt: hasLiveExecutions,
		CanResume:    active && !hasLiveExecutions && status.Status.Kind == serverapi.WorkflowTaskStatusKindInterrupted,
		CanCancel:    active,
	}
}

func displayNameForPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	return filepath.Base(filepath.Clean(trimmed))
}

func loadPendingApprovalSourcePlacementsByTask(ctx context.Context, queries *sqlitegen.Queries, taskIDs []string) (map[string][]sqlitegen.TaskNodePlacementRecord, error) {
	rows, err := queries.ListPendingApprovalSourcePlacementsByTasks(ctx, taskIDs)
	if err != nil {
		return nil, err
	}
	byTaskID := make(map[string][]sqlitegen.TaskNodePlacementRecord)
	for _, row := range rows {
		byTaskID[row.TaskID] = append(byTaskID[row.TaskID], pendingApprovalSourcePlacement(row))
	}
	return byTaskID, nil
}

func pendingApprovalSourcePlacement(row sqlitegen.ListPendingApprovalSourcePlacementsByTasksRow) sqlitegen.TaskNodePlacementRecord {
	return sqlitegen.TaskNodePlacementRecord{
		ID:                        row.ID,
		TaskID:                    row.TaskID,
		NodeID:                    row.NodeID,
		State:                     row.State,
		CreatedByTransitionID:     row.CreatedByTransitionID,
		ParallelBatchTransitionID: row.ParallelBatchTransitionID,
		ParallelBranchEdgeID:      row.ParallelBranchEdgeID,
		CreatedAtUnixMs:           row.CreatedAtUnixMs,
		UpdatedAtUnixMs:           row.UpdatedAtUnixMs,
	}
}
