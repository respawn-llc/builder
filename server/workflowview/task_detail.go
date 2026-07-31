package workflowview

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type TaskDetail struct {
	queries      *sqlitegen.Queries
	definitions  *DefinitionProjection
	projector    *TaskProjector
	authority    *sessionruntime.Authority
	quiescence   TaskQuiescenceSource
	dependencies *TaskDependencies
}

func NewTaskDetail(metadataStore *metadata.Store, definitions *DefinitionProjection, projector *TaskProjector, authority *sessionruntime.Authority, quiescence TaskQuiescenceSource, dependencies *TaskDependencies) (*TaskDetail, error) {
	if metadataStore == nil || metadataStore.Queries() == nil {
		return nil, errors.New("metadata store is required")
	}
	if definitions == nil {
		return nil, errors.New("definition projection is required")
	}
	if projector == nil {
		return nil, errors.New("task projector is required")
	}
	if authority == nil {
		return nil, errors.New("session runtime authority is required")
	}
	if quiescence == nil {
		return nil, errors.New("task quiescence source is required")
	}
	if dependencies == nil {
		return nil, errors.New("task dependencies read model is required")
	}
	return &TaskDetail{
		queries:      metadataStore.Queries(),
		definitions:  definitions,
		projector:    projector,
		authority:    authority,
		quiescence:   quiescence,
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
	definition, err := d.definitions.snapshot(ctx, task.WorkflowID)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	statusFact, err := loadWorkflowTaskStatusFact(ctx, d.queries, d.projector, task.ID)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
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
	snapshot, err := d.authority.CurrentScopedTaskExecutionSnapshot(task.ProjectID, runtimeids.WorkflowID(task.WorkflowID), workflow.TaskID(task.ID))
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	currentExecutions := currentTaskExecutions(snapshot.Executions)
	statusFact = taskDetailStatusFact(statusFact, currentExecutions)
	currentNodes, err := d.definitions.CurrentNodesByTask(ctx, []workflow.TaskID{workflow.TaskID(task.ID)})
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	taskCurrentNodes := currentNodes[workflow.TaskID(task.ID)]
	retainedSessionCount, err := d.queries.CountTaskSessions(ctx, sql.NullString{String: task.ID, Valid: true})
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	pendingApprovals, err := d.queries.ListTaskPendingApprovals(ctx, task.ID)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	attentionCount := taskAttentionCount(taskCurrentNodes, currentExecutions, len(pendingApprovals))
	taskID := workflow.TaskID(task.ID)
	quiescence, err := d.quiescence.CurrentTaskQuiescence([]workflow.TaskID{taskID})
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	canDelete, exists := quiescence[taskID]
	if !exists {
		return serverapi.WorkflowTaskDetail{}, fmt.Errorf("workflow execution omitted Task %q from Quiescence snapshot", task.ID)
	}
	facts := d.projector.ProjectTaskFacts(TaskFactsInput{
		Task:           task,
		Status:         statusFact,
		CurrentNodes:   taskCurrentNodes,
		LiveExecutions: currentExecutions,
		Definition:     definition,
		CanDelete:      canDelete,
	})

	detail := serverapi.WorkflowTaskDetail{
		Summary: facts.Summary,
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
		CurrentNodes:         workflowCurrentNodes(taskCurrentNodes),
		LiveSessionIDs:       make([]string, 0, len(currentExecutions)),
		CurrentScripts:       make([]serverapi.WorkflowTaskCurrentScript, 0, len(currentExecutions)),
		RetainedSessionCount: int(retainedSessionCount),
		Status:               facts.Status,
		Actions:              facts.Actions,
		LabelIDs:             labelIDsByTask[task.ID],
		AttentionCount:       attentionCount,
	}
	detail.Dependencies, err = d.dependencies.GetTaskDependencies(ctx, task.ID)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	for _, execution := range currentExecutions {
		switch {
		case execution.Agent != nil:
			detail.LiveSessionIDs = append(detail.LiveSessionIDs, execution.Agent.SessionID.String())
		case execution.Script != nil:
			node, exists := workflowNodesByID(definition.api)[string(execution.Ref.CurrentNode.NodeID)]
			if !exists || node.ScriptPath == nil || strings.TrimSpace(*node.ScriptPath) == "" {
				return serverapi.WorkflowTaskDetail{}, fmt.Errorf("task %q live script current node %q has no latest script path", task.ID, execution.Ref.CurrentNode.NodeID)
			}
			detail.CurrentScripts = append(detail.CurrentScripts, serverapi.WorkflowTaskCurrentScript{
				CurrentNode: workflowCurrentNodeReference(execution.Ref.CurrentNode),
				Path:        *node.ScriptPath,
			})
		default:
			return serverapi.WorkflowTaskDetail{}, fmt.Errorf("task %q live workflow execution has no target", task.ID)
		}
	}
	sort.Strings(detail.LiveSessionIDs)
	sort.Slice(detail.CurrentScripts, func(i, j int) bool {
		left := detail.CurrentScripts[i].CurrentNode
		right := detail.CurrentScripts[j].CurrentNode
		if left.NodeID != right.NodeID {
			return left.NodeID < right.NodeID
		}
		return optionalStringValue(left.TransitionBranchKey) < optionalStringValue(right.TransitionBranchKey)
	})
	return detail, nil
}

func currentTaskExecutions(executions []sessionruntime.TaskExecution) []sessionruntime.TaskExecution {
	return append([]sessionruntime.TaskExecution(nil), executions...)
}

func taskDetailStatusFact(durable workflowTaskStatusFact, current []sessionruntime.TaskExecution) workflowTaskStatusFact {
	if durable.Done {
		return durable
	}
	hasWaitingQuestion := false
	hasRunning := false
	hasQueued := false
	for _, execution := range current {
		hasWaitingQuestion = hasWaitingQuestion || execution.WaitingQuestion
		hasRunning = hasRunning || !execution.Queued
		hasQueued = hasQueued || execution.Queued
	}
	switch {
	case hasWaitingQuestion:
		return workflowTaskStatusFact{
			Status: taskDetailStatus(durable.Status, serverapi.WorkflowTaskStatusKindWaitingQuestion, true),
		}
	case durable.Status.Kind == serverapi.WorkflowTaskStatusKindWaitingApproval:
		return durable
	case hasRunning:
		return workflowTaskStatusFact{
			Status: taskDetailStatus(durable.Status, serverapi.WorkflowTaskStatusKindRunning, false),
		}
	case hasQueued:
		return workflowTaskStatusFact{
			Status: taskDetailStatus(durable.Status, serverapi.WorkflowTaskStatusKindQueued, false),
		}
	case durable.Status.Kind == serverapi.WorkflowTaskStatusKindRunning ||
		durable.Status.Kind == serverapi.WorkflowTaskStatusKindQueued ||
		durable.Status.Kind == serverapi.WorkflowTaskStatusKindWaitingQuestion:
		return workflowTaskStatusFact{
			Status: taskDetailStatus(durable.Status, serverapi.WorkflowTaskStatusKindActive, false),
		}
	default:
		return durable
	}
}

func sortedUniqueStrings(groups ...[]string) []string {
	seen := make(map[string]struct{})
	for _, group := range groups {
		for _, value := range group {
			seen[value] = struct{}{}
		}
	}
	values := make([]string, 0, len(seen))
	for value := range seen {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

func taskDetailStatus(current serverapi.WorkflowTaskStatus, kind serverapi.WorkflowTaskStatusKind, question bool) serverapi.WorkflowTaskStatus {
	nativeState, ok := kind.NativeState()
	if !ok {
		panic(fmt.Sprintf("task detail status kind %q has no native state", kind))
	}
	current.Kind = kind
	current.NativeState = nativeState
	current.AttentionTypes = slices.DeleteFunc(
		append([]serverapi.WorkflowTaskAttentionKind(nil), current.AttentionTypes...),
		func(value serverapi.WorkflowTaskAttentionKind) bool {
			return value == serverapi.WorkflowTaskAttentionKindQuestion
		},
	)
	if question {
		current.AttentionTypes = append(current.AttentionTypes, serverapi.WorkflowTaskAttentionKindQuestion)
		sort.Slice(current.AttentionTypes, func(i, j int) bool {
			return current.AttentionTypes[i] < current.AttentionTypes[j]
		})
	}
	return current
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

func taskAttentionCount(currentNodes []workflow.CurrentNode, executions []sessionruntime.TaskExecution, pendingApprovalCount int) int {
	count := pendingApprovalCount
	for _, currentNode := range currentNodes {
		if currentNode.Scheduling == nil || currentNode.Scheduling.Interruption == nil {
			continue
		}
		if workflow.IsActionableCurrentNodeInterruptionReason(currentNode.Scheduling.Interruption.Reason) {
			count++
		}
	}
	for _, execution := range executions {
		if execution.WaitingQuestion {
			count++
		}
	}
	return count
}

func optionalStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
