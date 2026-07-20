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
	"core/server/worktree"
	"core/shared/serverapi"
)

type TaskDetail struct {
	metadata    *metadata.Store
	queries     *sqlitegen.Queries
	definitions *DefinitionProjection
	projector   *TaskProjector
	git         *worktree.GitInspector
}

func NewTaskDetail(metadataStore *metadata.Store, definitions *DefinitionProjection, projector *TaskProjector, git *worktree.GitInspector) (*TaskDetail, error) {
	if metadataStore == nil || metadataStore.Queries() == nil {
		return nil, errors.New("metadata store is required")
	}
	if definitions == nil {
		return nil, errors.New("definition projection is required")
	}
	if projector == nil {
		return nil, errors.New("task projector is required")
	}
	if git == nil {
		return nil, errors.New("git inspector is required")
	}
	return &TaskDetail{
		metadata:    metadataStore,
		queries:     metadataStore.Queries(),
		definitions: definitions,
		projector:   projector,
		git:         git,
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
	snapshot, err := d.definitions.snapshot(ctx, task.WorkflowID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return serverapi.WorkflowTaskDetail{}, err
	}
	placements, err := d.queries.ListTaskNodePlacements(ctx, task.ID)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	pendingApprovalPlacements, err := loadPendingApprovalSourcePlacementsByTask(ctx, d.queries, []string{task.ID})
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	placements = append(placements, pendingApprovalPlacements[task.ID]...)
	runs, err := d.queries.ListTaskRuns(ctx, task.ID)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	transitions, err := d.queries.ListTaskTransitions(ctx, task.ID)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	comments, err := d.queries.ListTaskComments(ctx, sqlitegen.ListTaskCommentsParams{TaskID: task.ID, OffsetRows: 0, LimitRows: -1})
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	project, err := d.metadata.GetProjectOverview(ctx, task.ProjectID)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	workspaceContext := boardProjectWorkspaceContext(project)
	linkByWorkflowID := map[string]sqlitegen.ProjectWorkflowLinkRecord{}
	links, err := d.queries.ListProjectWorkflowLinks(ctx, task.ProjectID)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	for _, link := range links {
		if linkByWorkflowID[link.WorkflowID].ID == "" {
			linkByWorkflowID[link.WorkflowID] = link
		}
	}
	statusFact, err := loadWorkflowTaskStatusFact(ctx, d.queries, d.projector, task.ID)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	labelIDsByTask, err := loadTaskLabelIDsByTask(ctx, d.queries, []string{task.ID})
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	facts := d.projector.ProjectTaskFacts(TaskFactsInput{
		Task:       task,
		Status:     statusFact,
		Placements: placements,
		Runs:       runs,
		Definition: snapshot,
	})
	sourceWorkspace := sourceWorkspaceForTask(task, workspaceContext.byID, workspaceContext.primary)
	executionTarget, err := d.executionTargetForTask(ctx, task, sourceWorkspace)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	detail := serverapi.WorkflowTaskDetail{
		Summary:         facts.Summary,
		Project:         projectBoardProject(project, workspaceContext),
		Workflow:        workflowPickerItem(snapshot.api, linkByWorkflowID[task.WorkflowID], nil),
		Body:            task.Body,
		SourceURL:       task.SourceUrl,
		SourceWorkspace: sourceWorkspace,
		ExecutionTarget: executionTarget,
		Status:          facts.Status,
		Actions:         facts.Actions,
		LabelIDs:        labelIDsByTask[task.ID],
	}
	attentionCount, err := d.queries.CountWorkflowTaskAttentionCandidates(ctx, task.ID)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	detail.AttentionCount = int(attentionCount)
	nodes := workflowNodeByID(snapshot.api)
	for _, placement := range placements {
		detail.Placements = append(detail.Placements, d.projector.ProjectPlacement(PlacementProjectionInput{Placement: placement, Nodes: nodes}))
	}
	sessionNames, err := loadSessionNamesByRun(ctx, d.queries, runs)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	for _, run := range runs {
		detail.Runs = append(detail.Runs, d.projector.ProjectRun(RunProjectionInput{Run: run, Nodes: nodes, SessionNames: sessionNames}))
	}
	edgesByTransitionID, err := loadTransitionEdgesByTransitionID(ctx, d.queries, transitions)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	for _, transition := range transitions {
		dto, err := d.projector.ProjectTransition(TransitionProjectionInput{Transition: transition, Edges: edgesByTransitionID[transition.ID]})
		if err != nil {
			return serverapi.WorkflowTaskDetail{}, err
		}
		detail.Transitions = append(detail.Transitions, dto)
	}
	for _, comment := range comments {
		detail.Comments = append(detail.Comments, d.projector.ProjectComment(comment))
	}
	return detail, nil
}

func (d *TaskDetail) executionTargetForTask(ctx context.Context, task sqlitegen.TaskRecord, sourceWorkspace serverapi.ProjectWorkspaceSummary) (*serverapi.WorkflowExecutionTarget, error) {
	if !task.ExecutionTargetMode.Valid {
		if task.ExecutionTargetRequestedRef.Valid ||
			task.ExecutionTargetResolvedRef.Valid ||
			task.ExecutionTargetCommitOid.Valid ||
			task.ExecutionTargetProvenance.Valid {
			return nil, errors.New("unlocked task has execution target facts")
		}
		return nil, nil
	}
	target := &serverapi.WorkflowExecutionTarget{
		Mode:         serverapi.WorkflowExecutionTargetMode(task.ExecutionTargetMode.String),
		RequestedRef: metadata.OptionalString(task.ExecutionTargetRequestedRef),
		ResolvedRef:  metadata.OptionalString(task.ExecutionTargetResolvedRef),
		CommitOID:    metadata.OptionalString(task.ExecutionTargetCommitOid),
		Provenance:   serverapi.WorkflowExecutionTargetProvenance(task.ExecutionTargetProvenance.String),
	}
	if target.Mode == serverapi.WorkflowExecutionTargetModeNone {
		root := sourceWorkspace.RootPath
		target.EffectiveRoot = &root
		if err := target.Validate(); err != nil {
			return nil, fmt.Errorf("project task execution target: %w", err)
		}
		return target, nil
	}

	worktreeID := strings.TrimSpace(task.ManagedWorktreeID.String)
	if task.ManagedWorktreeID.Valid && worktreeID != "" {
		row, err := d.queries.GetWorktreeByID(ctx, worktreeID)
		switch {
		case err == nil:
			facts := worktreeKentFacts(row)
			managedWorktree := serverapi.WorkflowExecutionTargetWorktree{
				WorktreeID:    facts.WorktreeID,
				DisplayName:   facts.DisplayName,
				CanonicalRoot: facts.CanonicalRoot,
				Availability:  worktree.InspectPath(facts.CanonicalRoot).Availability,
				Managed:       facts.Managed,
			}
			target.ManagedWorktree = &managedWorktree
			if managedWorktree.Availability == serverapi.WorktreePathAvailabilityAvailable {
				root := facts.CanonicalRoot
				target.EffectiveRoot = &root
				if identity, inspectErr := d.git.ValidateManagedWorktreeIdentity(ctx, worktree.ManagedWorktreeIdentitySpec{
					SourceWorkspaceRoot:  sourceWorkspace.RootPath,
					ExpectedWorktreeRoot: facts.CanonicalRoot,
				}); inspectErr == nil {
					if branchName, ok := identity.NamedBranch(); ok {
						target.CurrentBranch = &branchName
					}
				} else if ctxErr := ctx.Err(); ctxErr != nil {
					return nil, ctxErr
				}
			} else if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
		case !errors.Is(err, sql.ErrNoRows):
			return nil, err
		}
	}
	if err := target.Validate(); err != nil {
		return nil, fmt.Errorf("project task execution target: %w", err)
	}
	return target, nil
}

func worktreeKentFacts(row sqlitegen.GetWorktreeByIDRow) serverapi.WorktreeKentFacts {
	facts := serverapi.WorktreeKentFacts{
		WorktreeID:    row.ID,
		DisplayName:   displayNameForPath(row.CanonicalRootPath),
		CanonicalRoot: row.CanonicalRootPath,
		Managed:       row.Managed != 0,
		CreatedBranch: row.CreatedBranch != 0,
	}
	if originSessionID := strings.TrimSpace(row.OriginSessionID); originSessionID != "" {
		facts.OriginSessionID = &originSessionID
	}
	return facts
}

func displayNameForPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	base := filepath.Base(filepath.Clean(trimmed))
	if base == "." || base == string(filepath.Separator) {
		return ""
	}
	return base
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
