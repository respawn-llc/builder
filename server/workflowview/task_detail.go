package workflowview

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/sessionruntime"
	"core/server/workflow"
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
	nodesByTask, err := d.projection.workflowStore.ListCurrentNodesByTaskWithQueries(ctx, d.queries, []workflow.TaskID{workflow.TaskID(taskID)})
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
	liveSessions, currentScripts, err := taskDetailLiveTargets(
		ctx,
		d.queries.ListSessionNamesByIDs,
		task.ID,
		projected.LiveExecutions,
		definition,
	)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	labelsByTask, err := loadTaskLabelsByTask(ctx, d.queries, []string{task.ID})
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	labelIDsByTask := taskLabelIDsByTask(labelsByTask)
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
		LiveSessions:         liveSessions,
		CurrentScripts:       currentScripts,
		RetainedSessionCount: int(retainedSessionCount),
		Status:               projected.Status,
		Actions:              projected.Actions,
		LabelIDs:             labelIDsByTask[task.ID],
		AttentionCount:       projected.AttentionCount,
	}
	detail.Dependencies = projectedDependencies
	return detail, nil
}

func taskDetailLiveTargets(
	ctx context.Context,
	nameLookup sessionNameLookup,
	taskID string,
	executions []sessionruntime.TaskExecution,
	definition definitionSnapshot,
) ([]serverapi.WorkflowTaskLiveSession, []serverapi.WorkflowTaskCurrentScript, error) {
	type liveAgent struct {
		sessionID       string
		nodeDisplayName string
	}
	agents := make([]liveAgent, 0, len(executions))
	sessionIDs := make([]string, 0, len(executions))
	scripts := make([]serverapi.WorkflowTaskCurrentScript, 0, len(executions))
	nodesByID := workflowNodesByID(definition.api)
	seenSessionIDs := make(map[string]struct{}, len(executions))
	for _, execution := range executions {
		switch {
		case execution.Agent != nil:
			sessionID := execution.Agent.SessionID.String()
			if _, exists := seenSessionIDs[sessionID]; exists {
				return nil, nil, fmt.Errorf("task %q has duplicate live Session %q", taskID, sessionID)
			}
			seenSessionIDs[sessionID] = struct{}{}
			nodeID := string(execution.Ref.CurrentNode.NodeID)
			node, exists := nodesByID[nodeID]
			if !exists {
				return nil, nil, fmt.Errorf("task %q live Agent execution references unknown Node %q", taskID, nodeID)
			}
			if node.Kind != string(workflow.NodeKindAgent) {
				return nil, nil, fmt.Errorf("task %q live Agent execution references %s Node %q", taskID, node.Kind, nodeID)
			}
			if strings.TrimSpace(node.DisplayName) == "" {
				return nil, nil, fmt.Errorf("task %q live Agent execution Node %q has a blank display name", taskID, nodeID)
			}
			sessionIDs = append(sessionIDs, sessionID)
			agents = append(agents, liveAgent{sessionID: sessionID, nodeDisplayName: node.DisplayName})
		case execution.Script != nil:
			if strings.TrimSpace(execution.Script.Path) == "" {
				return nil, nil, fmt.Errorf("task %q live Script execution has a blank target path", taskID)
			}
			scripts = append(scripts, serverapi.WorkflowTaskCurrentScript{
				CurrentNode: workflowCurrentNodeReference(execution.Ref.CurrentNode),
				Path:        execution.Script.Path,
			})
		default:
			return nil, nil, fmt.Errorf("task %q live workflow execution has no target", taskID)
		}
	}
	names, err := resolveSessionNames(ctx, nameLookup, sessionIDs)
	if err != nil {
		return nil, nil, fmt.Errorf("task %q live Sessions: %w", taskID, err)
	}
	liveSessions := make([]serverapi.WorkflowTaskLiveSession, 0, len(agents))
	for _, agent := range agents {
		liveSessions = append(liveSessions, serverapi.WorkflowTaskLiveSession{
			SessionID:       agent.sessionID,
			SessionName:     names[agent.sessionID],
			NodeDisplayName: agent.nodeDisplayName,
		})
	}
	sort.Slice(liveSessions, func(i, j int) bool { return liveSessions[i].SessionID < liveSessions[j].SessionID })
	sortTaskDetailCurrentScripts(scripts)
	return liveSessions, scripts, nil
}

func sortTaskDetailCurrentScripts(scripts []serverapi.WorkflowTaskCurrentScript) {
	sort.Slice(scripts, func(i, j int) bool {
		if scripts[i].CurrentNode.NodeID != scripts[j].CurrentNode.NodeID {
			return scripts[i].CurrentNode.NodeID < scripts[j].CurrentNode.NodeID
		}
		leftBranch := scripts[i].CurrentNode.TransitionBranchKey
		rightBranch := scripts[j].CurrentNode.TransitionBranchKey
		if leftBranch == nil {
			return rightBranch != nil || scripts[i].Path < scripts[j].Path
		}
		if rightBranch == nil {
			return false
		}
		if *leftBranch != *rightBranch {
			return *leftBranch < *rightBranch
		}
		return scripts[i].Path < scripts[j].Path
	})
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
			task.ExecutionTargetProvenance.Valid {
			return nil, nil, errors.New("unlocked task has execution target facts")
		}
		if task.ManagedWorktreeID.Valid && strings.TrimSpace(task.ManagedWorktreeID.String) == "" {
			return nil, nil, errors.New("task managed worktree id is blank")
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
