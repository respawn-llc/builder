package workflowstore

import (
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/workflow"
)

func TestTaskStartRejectsCurrentInvalidWorkflow(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: "group-terminal-invalid", WorkflowID: workflowID, SourceNodeID: workflow.NodeIDOf(done), TransitionID: "invalid", DisplayName: "Invalid"}); err != nil {
		t.Fatalf("AddTransitionGroup invalid terminal group: %v", err)
	}
	var terminalErr WorkflowValidationError
	if _, err := store.StartTask(ctx, task.ID); !errors.As(err, &terminalErr) || !terminalErr.HasCode(workflow.CodeTerminalHasOutgoingEdge) {
		t.Fatalf("expected current workflow validation error, got %v", err)
	}
}

func TestTaskCreateAllowsInvalidWorkflowBacklogButRejectsUnlinkedWorkflow(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	invalid, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Invalid"})
	if err != nil {
		t.Fatalf("CreateWorkflow invalid: %v", err)
	}
	if _, err := store.LinkWorkflow(ctx, binding.ProjectID, invalid.ID, true); err != nil {
		t.Fatalf("LinkWorkflow invalid: %v", err)
	}
	task, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Task", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask invalid default workflow backlog: %v", err)
	}
	if _, err := store.StartTask(ctx, task.ID); !errors.Is(err, ErrWorkflowValidationFailed) {
		t.Fatalf("expected invalid workflow start error, got %v", err)
	}
	updatedTitle := "Updated"
	updatedBody := "Updated body"
	if _, err := store.UpdateTask(ctx, UpdateTaskRequest{TaskID: task.ID, Title: &updatedTitle, Body: &updatedBody, SourceWorkspaceID: binding.WorkspaceID}); err != nil {
		t.Fatalf("UpdateTask invalid workflow backlog: %v", err)
	}
	if _, err := store.AddComment(ctx, task.ID, "Comment", "user", "operator"); err != nil {
		t.Fatalf("AddComment invalid workflow backlog: %v", err)
	}
	valid := createValidWorkflow(t, ctx, store)
	if _, err := store.LinkWorkflow(ctx, binding.ProjectID, valid, false); err != nil {
		t.Fatalf("LinkWorkflow valid explicit: %v", err)
	}
	if task, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: valid, Title: "Explicit", Body: "Body"}); err != nil {
		t.Fatalf("CreateTask explicit valid workflow: %v", err)
	} else if !strings.HasPrefix(task.ShortID, "WOR-2") {
		t.Fatalf("explicit task short id = %q, want WOR-2", task.ShortID)
	}
	unlinked, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Unlinked"})
	if err != nil {
		t.Fatalf("CreateWorkflow unlinked: %v", err)
	}
	if _, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: unlinked.ID, Title: "Task", Body: "Body"}); err == nil {
		t.Fatalf("expected unlinked workflow task creation to fail")
	}
}

func TestProjectWorkflowUnlinkHardDeletesUnusedLinksAndBlocksTaskReferences(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	link, err := store.LinkWorkflow(ctx, binding.ProjectID, workflowID, true)
	if err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	otherWorkflowID := createValidWorkflow(t, ctx, store)
	otherLink, err := store.LinkWorkflow(ctx, binding.ProjectID, otherWorkflowID, false)
	if err != nil {
		t.Fatalf("LinkWorkflow other: %v", err)
	}
	spareWorkflowID := createValidWorkflow(t, ctx, store)
	spareLink, err := store.LinkWorkflow(ctx, binding.ProjectID, spareWorkflowID, false)
	if err != nil {
		t.Fatalf("LinkWorkflow spare: %v", err)
	}
	if _, err := store.UnlinkProjectWorkflow(ctx, link.ID, "missing-link"); !errors.Is(err, ErrReplacementDefaultInvalid) {
		t.Fatalf("expected invalid replacement default guard, got %v", err)
	}
	if _, err := store.UnlinkProjectWorkflow(ctx, link.ID, link.ID); !errors.Is(err, ErrReplacementDefaultInvalid) {
		t.Fatalf("expected self replacement default guard, got %v", err)
	}
	links, err := store.ListProjectWorkflowLinks(ctx, binding.ProjectID)
	if err != nil {
		t.Fatalf("ListProjectWorkflowLinks after invalid replacement: %v", err)
	}
	if len(links) != 3 || !links[0].IsDefault {
		t.Fatalf("links after invalid replacement = %+v, want original default preserved", links)
	}
	blockedDefault, err := store.UnlinkProjectWorkflow(ctx, link.ID, "")
	if err != nil {
		t.Fatalf("unlink default without replacement should return typed blocker, got error: %v", err)
	}
	if blockedDefault.Unlinked || !hasProjectWorkflowUnlinkBlocker(blockedDefault.Blockers, "default_replacement_required", 2) {
		t.Fatalf("blocked default unlink = %+v, want replacement-required blocker", blockedDefault)
	}
	links, err = store.ListProjectWorkflowLinks(ctx, binding.ProjectID)
	if err != nil {
		t.Fatalf("ListProjectWorkflowLinks after missing replacement: %v", err)
	}
	if len(links) != 3 || !links[0].IsDefault {
		t.Fatalf("links after missing replacement = %+v, want original default preserved", links)
	}
	if result, err := store.UnlinkProjectWorkflow(ctx, spareLink.ID, ""); err != nil || !result.Unlinked {
		t.Fatalf("unlink unused non-default link should physically delete: %v", err)
	}
	if result, err := store.UnlinkProjectWorkflow(ctx, link.ID, otherLink.ID); err != nil || !result.Unlinked {
		t.Fatalf("unlink default with valid replacement: %v", err)
	}
	links, err = store.ListProjectWorkflowLinks(ctx, binding.ProjectID)
	if err != nil {
		t.Fatalf("ListProjectWorkflowLinks after replacement: %v", err)
	}
	if len(links) != 1 || links[0].ID != otherLink.ID || !links[0].IsDefault {
		t.Fatalf("links after valid replacement = %+v, want replacement default", links)
	}
	link = otherLink
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	blocked, err := store.UnlinkProjectWorkflow(ctx, link.ID, "")
	if err != nil {
		t.Fatalf("task reference unlink guard should return typed blockers, got error: %v", err)
	}
	if blocked.Unlinked || !hasProjectWorkflowUnlinkBlocker(blocked.Blockers, "task_references", 1) {
		t.Fatalf("blocked unlink = %+v, want task reference blocker", blocked)
	}
	startTask(t, ctx, store, task.ID)
}

func TestProjectWorkflowUnlinkBlocksTerminalTaskHistory(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	link, err := store.LinkWorkflow(ctx, binding.ProjectID, workflowID, true)
	if err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	blocked, err := store.UnlinkProjectWorkflow(ctx, link.ID, "")
	if err != nil {
		t.Fatalf("terminal task history unlink guard should return typed blockers, got error: %v", err)
	}
	if blocked.Unlinked || !hasProjectWorkflowUnlinkBlocker(blocked.Blockers, "task_references", 1) {
		t.Fatalf("blocked unlink = %+v, want terminal task history blocker", blocked)
	}
	links, err := store.ListProjectWorkflowLinks(ctx, binding.ProjectID)
	if err != nil {
		t.Fatalf("ListProjectWorkflowLinks: %v", err)
	}
	if len(links) != 1 || links[0].ID != link.ID || !links[0].IsDefault {
		t.Fatalf("links after blocked unlink = %+v", links)
	}
	if _, err := store.queries.GetTask(ctx, string(task.ID)); err != nil {
		t.Fatalf("task history should remain readable after soft unlink: %v", err)
	}
}

func TestWorkflowDeletePreviewAndConfirmedApplyDeleteDatabaseRows(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	worktreeRoot := t.TempDir()
	const worktreeID = "workflow-delete-worktree"
	if err := store.metadata.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{
		ID: worktreeID, WorkspaceID: binding.WorkspaceID, CanonicalRoot: worktreeRoot, DisplayName: "Workflow delete target", Availability: "available", Managed: true,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	if _, err := store.queries.UpdateTaskManagedWorktree(ctx, sqlitegen.UpdateTaskManagedWorktreeParams{
		ID: string(task.ID), ManagedWorktreeID: nullableString(worktreeID), UpdatedAtUnixMs: store.now().UnixMilli(),
	}); err != nil {
		t.Fatalf("UpdateTaskManagedWorktree: %v", err)
	}
	provisioningGeneration := "workflow-delete-provisioning"
	if err := store.SaveTaskExecutionTarget(ctx, workflow.ExecutionTarget{
		TaskID: task.ID, Policy: workflow.ExecutionPolicyHead,
		ResolvedSource: &workflow.ExecutionTargetResolvedSource{
			Kind: workflow.ExecutionTargetSourceNamedRef, NamedRef: executionTargetStringPointer("refs/heads/main"), Commit: "workflow-delete-commit",
		},
		State: workflow.ExecutionTargetStateLocked, ProvisioningGeneration: &provisioningGeneration,
		SetupProvisioningGeneration: &provisioningGeneration, SetupState: workflow.ExecutionTargetSetupSucceeded,
		RecoveryDisposition: workflow.ExecutionTargetRecoveryAvailable,
	}); err != nil {
		t.Fatalf("SaveTaskExecutionTarget: %v", err)
	}
	sentinelPath := worktreeRoot + "/workflow-delete-sentinel"
	if err := os.WriteFile(sentinelPath, []byte("preserve"), 0o600); err != nil {
		t.Fatalf("write worktree sentinel: %v", err)
	}

	impact, err := store.PreviewWorkflowDelete(ctx, workflowID)
	if err != nil {
		t.Fatalf("PreviewWorkflowDelete: %v", err)
	}
	if impact.WorkflowID != workflowID || impact.Version != 6 || impact.ProjectCount != 1 || impact.LinkCount != 1 || impact.TaskCount != 1 || impact.ActiveRunCount != 0 || impact.RunnableRunCount != 0 || impact.BlockedTaskCount != 0 {
		t.Fatalf("delete impact = %+v, want one linked project/link/task and no run blockers", impact)
	}

	unconfirmed, err := store.DeleteWorkflow(ctx, WorkflowDeleteRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("DeleteWorkflow unconfirmed: %v", err)
	}
	if unconfirmed.Deleted || !hasWorkflowDeleteBlocker(unconfirmed.Blockers, "confirmation_required", 1) {
		t.Fatalf("unconfirmed delete result = %+v, want confirmation blocker", unconfirmed)
	}

	deleted, err := store.DeleteWorkflow(ctx, confirmedWorkflowDeleteRequest(impact))
	if err != nil {
		t.Fatalf("DeleteWorkflow confirmed: %v", err)
	}
	if !deleted.Deleted || len(deleted.Blockers) != 0 {
		t.Fatalf("confirmed delete result = %+v, want deletion without blockers", deleted)
	}
	if _, err := store.queries.GetTask(ctx, string(task.ID)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetTask after workflow delete = %v, want sql.ErrNoRows", err)
	}
	if _, err := store.StartTask(ctx, task.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("StartTask after workflow delete = %v, want %v", err, sql.ErrNoRows)
	}
	if _, err := store.queries.GetWorkflow(ctx, string(workflowID)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetWorkflow after workflow delete = %v, want sql.ErrNoRows", err)
	}
	links, err := store.ListProjectWorkflowLinks(ctx, binding.ProjectID)
	if err != nil {
		t.Fatalf("ListProjectWorkflowLinks after workflow delete: %v", err)
	}
	if len(links) != 0 {
		t.Fatalf("links after workflow delete = %+v, want none", links)
	}
	var nodeCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_nodes WHERE workflow_id = ?`, string(workflowID)).Scan(&nodeCount); err != nil {
		t.Fatalf("count workflow nodes after delete: %v", err)
	}
	if nodeCount != 0 {
		t.Fatalf("workflow node count after delete = %d, want 0", nodeCount)
	}
	if _, err := store.metadata.GetWorktreeRecordByID(ctx, worktreeID); err != nil {
		t.Fatalf("workflow deletion must preserve managed worktree metadata: %v", err)
	}
	if _, err := os.Stat(sentinelPath); err != nil {
		t.Fatalf("workflow deletion must preserve managed worktree files: %v", err)
	}
	var targetCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_execution_targets WHERE task_id = ?`, string(task.ID)).Scan(&targetCount); err != nil {
		t.Fatalf("count task execution targets after workflow delete: %v", err)
	}
	if targetCount != 0 {
		t.Fatalf("task execution targets after workflow delete = %d, want cascade deletion", targetCount)
	}
}

func TestWorkflowDeleteBlocksRunnableAndActiveRuns(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)

	runnableImpact, err := store.PreviewWorkflowDelete(ctx, workflowID)
	if err != nil {
		t.Fatalf("PreviewWorkflowDelete runnable: %v", err)
	}
	if runnableImpact.RunnableRunCount != 1 || runnableImpact.ActiveRunCount != 0 || runnableImpact.BlockedTaskCount != 1 {
		t.Fatalf("runnable impact = %+v, want one runnable blocked task", runnableImpact)
	}
	runnableDelete, err := store.DeleteWorkflow(ctx, confirmedWorkflowDeleteRequest(runnableImpact))
	if err != nil {
		t.Fatalf("DeleteWorkflow runnable: %v", err)
	}
	if runnableDelete.Deleted || !hasWorkflowDeleteBlocker(runnableDelete.Blockers, "runnable_runs", 1) {
		t.Fatalf("runnable delete result = %+v, want runnable_runs blocker", runnableDelete)
	}

	if _, err := store.ClaimRun(ctx, started.RunID, 0); err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	activeImpact, err := store.PreviewWorkflowDelete(ctx, workflowID)
	if err != nil {
		t.Fatalf("PreviewWorkflowDelete active: %v", err)
	}
	if activeImpact.ActiveRunCount != 1 || activeImpact.RunnableRunCount != 0 || activeImpact.BlockedTaskCount != 1 {
		t.Fatalf("active impact = %+v, want one active blocked task", activeImpact)
	}
	activeDelete, err := store.DeleteWorkflow(ctx, confirmedWorkflowDeleteRequest(activeImpact))
	if err != nil {
		t.Fatalf("DeleteWorkflow active: %v", err)
	}
	if activeDelete.Deleted || !hasWorkflowDeleteBlocker(activeDelete.Blockers, "active_runs", 1) {
		t.Fatalf("active delete result = %+v, want active_runs blocker", activeDelete)
	}
}

func TestWorkflowDeleteBlocksDefaultReplacementAndDetectsImpactChanges(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	defaultWorkflowID := createValidWorkflow(t, ctx, store)
	defaultLink, err := store.LinkWorkflow(ctx, binding.ProjectID, defaultWorkflowID, true)
	if err != nil {
		t.Fatalf("LinkWorkflow default: %v", err)
	}
	replacementWorkflowID := createValidWorkflow(t, ctx, store)
	replacementLink, err := store.LinkWorkflow(ctx, binding.ProjectID, replacementWorkflowID, false)
	if err != nil {
		t.Fatalf("LinkWorkflow replacement: %v", err)
	}

	defaultImpact, err := store.PreviewWorkflowDelete(ctx, defaultWorkflowID)
	if err != nil {
		t.Fatalf("PreviewWorkflowDelete default: %v", err)
	}
	if defaultImpact.DefaultReplacementProjectCount != 1 {
		t.Fatalf("default impact = %+v, want one project requiring replacement default", defaultImpact)
	}
	blockedDefault, err := store.DeleteWorkflow(ctx, confirmedWorkflowDeleteRequest(defaultImpact))
	if err != nil {
		t.Fatalf("DeleteWorkflow default: %v", err)
	}
	if blockedDefault.Deleted || !hasWorkflowDeleteBlocker(blockedDefault.Blockers, "default_replacement_required", 1) {
		t.Fatalf("default delete result = %+v, want default replacement blocker", blockedDefault)
	}
	links, err := store.ListProjectWorkflowLinks(ctx, binding.ProjectID)
	if err != nil {
		t.Fatalf("ListProjectWorkflowLinks after default blocker: %v", err)
	}
	if len(links) != 2 || links[0].ID != defaultLink.ID || !links[0].IsDefault {
		t.Fatalf("links after default blocker = %+v, want original default preserved", links)
	}

	if _, err := store.SetDefaultProjectWorkflowLink(ctx, binding.ProjectID, replacementWorkflowID); err != nil {
		t.Fatalf("SetDefaultProjectWorkflowLink: %v", err)
	}
	deleteableImpact, err := store.PreviewWorkflowDelete(ctx, defaultWorkflowID)
	if err != nil {
		t.Fatalf("PreviewWorkflowDelete after replacement: %v", err)
	}
	if deleteableImpact.DefaultReplacementProjectCount != 0 {
		t.Fatalf("deleteable impact = %+v, want no replacement blocker", deleteableImpact)
	}
	deleted, err := store.DeleteWorkflow(ctx, confirmedWorkflowDeleteRequest(deleteableImpact))
	if err != nil {
		t.Fatalf("DeleteWorkflow after replacement: %v", err)
	}
	if !deleted.Deleted || len(deleted.Blockers) != 0 {
		t.Fatalf("delete after replacement = %+v, want deletion", deleted)
	}
	links, err = store.ListProjectWorkflowLinks(ctx, binding.ProjectID)
	if err != nil {
		t.Fatalf("ListProjectWorkflowLinks after delete: %v", err)
	}
	if len(links) != 1 || links[0].ID != replacementLink.ID || !links[0].IsDefault {
		t.Fatalf("links after deleting old default = %+v, want replacement default preserved", links)
	}

	staleWorkflowID := createValidWorkflow(t, ctx, store)
	if _, err := store.LinkWorkflow(ctx, binding.ProjectID, staleWorkflowID, false); err != nil {
		t.Fatalf("LinkWorkflow stale: %v", err)
	}
	staleImpact, err := store.PreviewWorkflowDelete(ctx, staleWorkflowID)
	if err != nil {
		t.Fatalf("PreviewWorkflowDelete stale: %v", err)
	}
	if _, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: staleWorkflowID, Title: "Stale", Body: "Body"}); err != nil {
		t.Fatalf("CreateTask stale: %v", err)
	}
	staleDelete, err := store.DeleteWorkflow(ctx, confirmedWorkflowDeleteRequest(staleImpact))
	if err != nil {
		t.Fatalf("DeleteWorkflow stale: %v", err)
	}
	if staleDelete.Deleted || !hasWorkflowDeleteBlocker(staleDelete.Blockers, "impact_changed", 1) || staleDelete.Impact.TaskCount != 1 {
		t.Fatalf("stale delete result = %+v, want impact_changed with refreshed task count", staleDelete)
	}
}

func TestGuardedGraphDeletesRespectTaskHistory(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	agentID := workflow.NodeID("node-agent-" + string(workflowID))
	if err := store.DeleteNode(ctx, agentID); !errors.Is(err, ErrNodeHasTaskHistory) {
		t.Fatalf("expected active node task-history guard, got %v", err)
	}
	if err := store.DeleteEdge(ctx, workflow.EdgeID("edge-start-"+string(workflowID))); err != nil {
		t.Fatalf("DeleteEdge unrelated to current active node: %v", err)
	}
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	if err := store.DeleteEdge(ctx, workflow.EdgeID("edge-done-"+string(workflowID))); err != nil {
		t.Fatalf("DeleteEdge completed history edge: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	if _, err := store.AddNode(ctx, NodeRecord{ID: "node-unused", WorkflowID: workflowID, Key: "unused", Kind: workflow.NodeKindTerminal, DisplayName: "Unused"}); err != nil {
		t.Fatalf("AddNode unused: %v", err)
	}
	if err := store.DeleteNode(ctx, workflow.NodeIDOf(done)); !errors.Is(err, ErrNodeHasTaskHistory) {
		t.Fatalf("expected terminal physical delete guard, got %v", err)
	}
	if err := store.DeleteNode(ctx, "node-unused"); err != nil {
		t.Fatalf("DeleteNode unused: %v", err)
	}
	if _, err := store.queries.GetWorkflowNode(ctx, "node-unused"); err == nil {
		t.Fatalf("unused node still exists after guarded delete")
	}
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: "group-unused", WorkflowID: workflowID, SourceNodeID: agentID, TransitionID: "unused", DisplayName: "Unused"}); err != nil {
		t.Fatalf("AddTransitionGroup unused: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: "edge-unused", WorkflowID: workflowID, TransitionGroupID: "group-unused", Key: "unused", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession}); err != nil {
		t.Fatalf("AddEdge unused: %v", err)
	}
	if err := store.DeleteEdge(ctx, "edge-unused"); err != nil {
		t.Fatalf("DeleteEdge unused: %v", err)
	}
	if _, err := store.queries.GetWorkflowEdge(ctx, "edge-unused"); err == nil {
		t.Fatalf("unused edge still exists after guarded delete")
	}
}
