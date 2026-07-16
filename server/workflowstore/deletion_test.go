package workflowstore

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

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
	// Intentional invalid-state fixture: batch graph saves reject terminal
	// transitions, while task start must still reject invalid persisted graphs.
	forceWorkflowGraphRowsForSnapshotTest(t, ctx, store, workflowID, nil,
		[]TransitionGroupRecord{{ID: "group-terminal-invalid", WorkflowID: workflowID, SourceNodeID: workflow.NodeIDOf(done), TransitionID: "invalid", DisplayName: "Invalid"}},
		nil,
	)
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
	linkWorkflow(t, ctx, store, binding.ProjectID, invalid.ID, true)
	task := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Task", Body: "Body"})
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
	linkWorkflow(t, ctx, store, binding.ProjectID, valid, false)
	if task := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: valid, Title: "Explicit", Body: "Body"}); !strings.HasPrefix(task.ShortID, "WOR-2") {
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
	f := newWorkflowDeletionFixture(t)
	_, link := f.linkedWorkflow(t, true)
	_, otherLink := f.linkedWorkflow(t, false)
	_, spareLink := f.linkedWorkflow(t, false)
	if _, err := f.store.UnlinkProjectWorkflow(f.ctx, link.ID, "missing-link"); !errors.Is(err, ErrReplacementDefaultInvalid) {
		t.Fatalf("expected invalid replacement default guard, got %v", err)
	}
	if _, err := f.store.UnlinkProjectWorkflow(f.ctx, link.ID, link.ID); !errors.Is(err, ErrReplacementDefaultInvalid) {
		t.Fatalf("expected self replacement default guard, got %v", err)
	}
	links := f.links(t)
	if len(links) != 3 || !links[0].IsDefault {
		t.Fatalf("links after invalid replacement = %+v, want original default preserved", links)
	}
	blockedDefault := f.unlink(t, link.ID, "")
	if blockedDefault.Unlinked || !hasProjectWorkflowUnlinkBlocker(blockedDefault.Blockers, "default_replacement_required", 2) {
		t.Fatalf("blocked default unlink = %+v, want replacement-required blocker", blockedDefault)
	}
	links = f.links(t)
	if len(links) != 3 || !links[0].IsDefault {
		t.Fatalf("links after missing replacement = %+v, want original default preserved", links)
	}
	if result := f.unlink(t, spareLink.ID, ""); !result.Unlinked {
		t.Fatalf("unlink unused non-default link = %+v, want physical deletion", result)
	}
	if result := f.unlink(t, link.ID, otherLink.ID); !result.Unlinked {
		t.Fatalf("unlink default with valid replacement = %+v, want physical deletion", result)
	}
	links = f.links(t)
	if len(links) != 1 || links[0].ID != otherLink.ID || !links[0].IsDefault {
		t.Fatalf("links after valid replacement = %+v, want replacement default", links)
	}
	link = otherLink
	task := createDefaultTask(t, f.ctx, f.store, f.projectID)
	blocked := f.unlink(t, link.ID, "")
	if blocked.Unlinked || !hasProjectWorkflowUnlinkBlocker(blocked.Blockers, "task_references", 1) {
		t.Fatalf("blocked unlink = %+v, want task reference blocker", blocked)
	}
	startTask(t, f.ctx, f.store, task.ID)
}

func TestProjectWorkflowUnlinkBlocksTerminalTaskHistory(t *testing.T) {
	f := newWorkflowDeletionFixture(t)
	_, link := f.linkedWorkflow(t, true)
	task := createDefaultTask(t, f.ctx, f.store, f.projectID)
	started := startTask(t, f.ctx, f.store, task.ID)
	completeRun(t, f.ctx, f.store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	blocked := f.unlink(t, link.ID, "")
	if blocked.Unlinked || !hasProjectWorkflowUnlinkBlocker(blocked.Blockers, "task_references", 1) {
		t.Fatalf("blocked unlink = %+v, want terminal task history blocker", blocked)
	}
	links := f.links(t)
	if len(links) != 1 || links[0].ID != link.ID || !links[0].IsDefault {
		t.Fatalf("links after blocked unlink = %+v", links)
	}
	if _, err := f.store.queries.GetTask(f.ctx, string(task.ID)); err != nil {
		t.Fatalf("task history should remain readable after soft unlink: %v", err)
	}
}

func TestWorkflowDeletePreviewAndConfirmedApplyDeleteDatabaseRows(t *testing.T) {
	f := newWorkflowDeletionFixture(t)
	workflowID, _ := f.linkedWorkflow(t, true)
	task := createDefaultTask(t, f.ctx, f.store, f.projectID)
	_, current, err := f.store.GetDefinition(f.ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}

	impact := f.preview(t, workflowID)
	if impact.WorkflowID != workflowID || impact.Version != current.Version || impact.ProjectCount != 1 || impact.LinkCount != 1 || impact.TaskCount != 1 || impact.ActiveRunCount != 0 || impact.RunnableRunCount != 0 || impact.BlockedTaskCount != 0 {
		t.Fatalf("delete impact = %+v, want one linked project/link/task and no run blockers", impact)
	}

	unconfirmed := f.delete(t, WorkflowDeleteRequest{WorkflowID: workflowID})
	if unconfirmed.Deleted || !hasWorkflowDeleteBlocker(unconfirmed.Blockers, "confirmation_required", 1) {
		t.Fatalf("unconfirmed delete result = %+v, want confirmation blocker", unconfirmed)
	}

	cleanup := f.confirmDelete(t, impact, true)
	if cleanup.Deleted || !hasWorkflowDeleteBlocker(cleanup.Blockers, "artifact_cleanup_unsupported", 1) {
		t.Fatalf("cleanup delete result = %+v, want unsupported cleanup blocker", cleanup)
	}

	deleted := f.confirmDelete(t, impact, false)
	if !deleted.Deleted || len(deleted.Blockers) != 0 {
		t.Fatalf("confirmed delete result = %+v, want deletion without blockers", deleted)
	}
	if _, err := f.store.queries.GetTask(f.ctx, string(task.ID)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetTask after workflow delete = %v, want sql.ErrNoRows", err)
	}
	if _, err := f.store.queries.GetWorkflow(f.ctx, string(workflowID)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetWorkflow after workflow delete = %v, want sql.ErrNoRows", err)
	}
	links := f.links(t)
	if len(links) != 0 {
		t.Fatalf("links after workflow delete = %+v, want none", links)
	}
	var nodeCount int
	if err := f.store.db.QueryRowContext(f.ctx, `SELECT COUNT(*) FROM workflow_nodes WHERE workflow_id = ?`, string(workflowID)).Scan(&nodeCount); err != nil {
		t.Fatalf("count workflow nodes after delete: %v", err)
	}
	if nodeCount != 0 {
		t.Fatalf("workflow node count after delete = %d, want 0", nodeCount)
	}
}

func TestWorkflowDeleteBlocksRunnableAndActiveRuns(t *testing.T) {
	f := newWorkflowDeletionFixture(t)
	workflowID, _ := f.linkedWorkflow(t, true)
	task := createDefaultTask(t, f.ctx, f.store, f.projectID)
	started := startTask(t, f.ctx, f.store, task.ID)

	runnableImpact := f.preview(t, workflowID)
	if runnableImpact.RunnableRunCount != 1 || runnableImpact.ActiveRunCount != 0 || runnableImpact.BlockedTaskCount != 1 {
		t.Fatalf("runnable impact = %+v, want one runnable blocked task", runnableImpact)
	}
	runnableDelete := f.confirmDelete(t, runnableImpact, false)
	if runnableDelete.Deleted || !hasWorkflowDeleteBlocker(runnableDelete.Blockers, "runnable_runs", 1) {
		t.Fatalf("runnable delete result = %+v, want runnable_runs blocker", runnableDelete)
	}

	if _, err := f.store.ClaimRun(f.ctx, started.RunID, 0); err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	activeImpact := f.preview(t, workflowID)
	if activeImpact.ActiveRunCount != 1 || activeImpact.RunnableRunCount != 0 || activeImpact.BlockedTaskCount != 1 {
		t.Fatalf("active impact = %+v, want one active blocked task", activeImpact)
	}
	activeDelete := f.confirmDelete(t, activeImpact, false)
	if activeDelete.Deleted || !hasWorkflowDeleteBlocker(activeDelete.Blockers, "active_runs", 1) {
		t.Fatalf("active delete result = %+v, want active_runs blocker", activeDelete)
	}
}

func TestWorkflowDeleteBlocksDefaultReplacementAndDetectsImpactChanges(t *testing.T) {
	f := newWorkflowDeletionFixture(t)
	defaultWorkflowID, defaultLink := f.linkedWorkflow(t, true)
	replacementWorkflowID, replacementLink := f.linkedWorkflow(t, false)

	defaultImpact := f.preview(t, defaultWorkflowID)
	if defaultImpact.DefaultReplacementProjectCount != 1 {
		t.Fatalf("default impact = %+v, want one project requiring replacement default", defaultImpact)
	}
	blockedDefault := f.confirmDelete(t, defaultImpact, false)
	if blockedDefault.Deleted || !hasWorkflowDeleteBlocker(blockedDefault.Blockers, "default_replacement_required", 1) {
		t.Fatalf("default delete result = %+v, want default replacement blocker", blockedDefault)
	}
	links := f.links(t)
	if len(links) != 2 || links[0].ID != defaultLink.ID || !links[0].IsDefault {
		t.Fatalf("links after default blocker = %+v, want original default preserved", links)
	}

	if _, err := f.store.SetDefaultProjectWorkflowLink(f.ctx, f.projectID, replacementWorkflowID); err != nil {
		t.Fatalf("SetDefaultProjectWorkflowLink: %v", err)
	}
	deleteableImpact := f.preview(t, defaultWorkflowID)
	if deleteableImpact.DefaultReplacementProjectCount != 0 {
		t.Fatalf("deleteable impact = %+v, want no replacement blocker", deleteableImpact)
	}
	deleted := f.confirmDelete(t, deleteableImpact, false)
	if !deleted.Deleted || len(deleted.Blockers) != 0 {
		t.Fatalf("delete after replacement = %+v, want deletion", deleted)
	}
	links = f.links(t)
	if len(links) != 1 || links[0].ID != replacementLink.ID || !links[0].IsDefault {
		t.Fatalf("links after deleting old default = %+v, want replacement default preserved", links)
	}

	staleWorkflowID, _ := f.linkedWorkflow(t, false)
	staleImpact := f.preview(t, staleWorkflowID)
	createTask(t, f.ctx, f.store, CreateTaskRequest{ProjectID: f.projectID, WorkflowID: staleWorkflowID, Title: "Stale", Body: "Body"})
	staleDelete := f.confirmDelete(t, staleImpact, false)
	if staleDelete.Deleted || !hasWorkflowDeleteBlocker(staleDelete.Blockers, "impact_changed", 1) || staleDelete.Impact.TaskCount != 1 {
		t.Fatalf("stale delete result = %+v, want impact_changed with refreshed task count", staleDelete)
	}
}

func TestGuardedGraphDeletesRespectTaskHistory(t *testing.T) {
	f := newWorkflowDeletionFixture(t)
	workflowID, _ := f.linkedWorkflow(t, true)
	task := createDefaultTask(t, f.ctx, f.store, f.projectID)
	started := startTask(t, f.ctx, f.store, task.ID)
	agentID := workflow.NodeID("node-agent-" + string(workflowID))
	if _, err := f.store.queries.DeleteWorkflowNode(f.ctx, string(agentID)); err == nil {
		t.Fatal("direct active-node delete succeeded, want current-task trigger guard")
	}
	if err := f.store.DeleteNode(f.ctx, agentID); !errors.Is(err, ErrNodeHasTaskHistory) {
		t.Fatalf("expected active node task-history guard, got %v", err)
	}
	if err := f.store.DeleteEdge(f.ctx, workflow.EdgeID("edge-start-"+string(workflowID))); err != nil {
		t.Fatalf("DeleteEdge unrelated to current active node: %v", err)
	}
	completeRun(t, f.ctx, f.store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	if err := f.store.DeleteEdge(f.ctx, workflow.EdgeID("edge-done-"+string(workflowID))); err != nil {
		t.Fatalf("DeleteEdge completed history edge: %v", err)
	}
	def, _, err := f.store.GetDefinition(f.ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	// Intentional intermediate-state fixture: the preceding guarded deletions
	// leave a graph that the atomic save seam correctly refuses to persist.
	forceWorkflowGraphRowsForSnapshotTest(t, f.ctx, f.store, workflowID,
		[]NodeRecord{{ID: "node-unused", WorkflowID: workflowID, Key: "unused", Kind: workflow.NodeKindTerminal, DisplayName: "Unused"}},
		[]TransitionGroupRecord{{ID: "group-unused", WorkflowID: workflowID, SourceNodeID: agentID, TransitionID: "unused", DisplayName: "Unused"}},
		[]EdgeRecord{{ID: "edge-unused", WorkflowID: workflowID, TransitionGroupID: "group-unused", Key: "unused", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession}},
	)
	if err := f.store.DeleteNode(f.ctx, workflow.NodeIDOf(done)); !errors.Is(err, ErrNodeHasTaskHistory) {
		t.Fatalf("expected terminal physical delete guard, got %v", err)
	}
	if err := f.store.DeleteNode(f.ctx, "node-unused"); err != nil {
		t.Fatalf("DeleteNode unused: %v", err)
	}
	if _, err := f.store.queries.GetWorkflowNode(f.ctx, "node-unused"); err == nil {
		t.Fatalf("unused node still exists after guarded delete")
	}
	if err := f.store.DeleteEdge(f.ctx, "edge-unused"); err != nil {
		t.Fatalf("DeleteEdge unused: %v", err)
	}
	if _, err := f.store.queries.GetWorkflowEdge(f.ctx, "edge-unused"); err == nil {
		t.Fatalf("unused edge still exists after guarded delete")
	}
}
