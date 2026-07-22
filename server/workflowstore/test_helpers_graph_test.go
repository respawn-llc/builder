package workflowstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"core/internal/testharness/testsetup"
	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/session"
	"core/server/workflow"
	"core/shared/config"
	"core/shared/sessioncontract"
)

func removeWorkflowGraphSaveEdgesTouchingNode(def workflow.Definition, edges []EdgeRecord, nodeID workflow.NodeID) []EdgeRecord {
	sourceByGroup := map[workflow.TransitionGroupID]workflow.NodeID{}
	for _, group := range def.TransitionGroups {
		sourceByGroup[group.ID] = group.SourceNodeID
	}
	filtered := make([]EdgeRecord, 0, len(edges))
	for _, edge := range edges {
		if edge.TargetNodeID != nodeID && sourceByGroup[edge.TransitionGroupID] != nodeID {
			filtered = append(filtered, edge)
		}
	}
	return filtered
}

func workflowGraphSaveBlockerCount(blockers []WorkflowGraphSaveBlocker, code string) int64 {
	for _, blocker := range blockers {
		if blocker.Code == code {
			return blocker.Count
		}
	}
	return 0
}

func nodeByID(t *testing.T, def workflow.Definition, nodeID workflow.NodeID) workflow.Node {
	t.Helper()
	for _, node := range def.Nodes {
		if workflow.NodeIDOf(node) == nodeID {
			return node
		}
	}
	t.Fatalf("node %q not found in %+v", nodeID, def.Nodes)
	return nil
}

func workflowGraphEditPolicyErrorHasBlocker(err error, code string) bool {
	var policyErr WorkflowGraphEditPolicyError
	if !errors.As(err, &policyErr) {
		return false
	}
	for _, blocker := range policyErr.Blockers {
		if blocker.Code == code {
			return true
		}
	}
	return false
}

func forceWorkflowGraphRowsForSnapshotTest(t *testing.T, ctx context.Context, store *Store, workflowID workflow.WorkflowID, nodes []NodeRecord, groups []TransitionGroupRecord, edges []EdgeRecord) {
	t.Helper()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx force graph rows: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := store.queries.WithTx(tx)
	for i, node := range nodes {
		if node.WorkflowID == "" {
			node.WorkflowID = workflowID
		}
		if err := upsertWorkflowNode(ctx, q, node, int64(10000+i*100), "force workflow node"); err != nil {
			t.Fatalf("force workflow node %s: %v", node.ID, err)
		}
	}
	for i, group := range groups {
		if group.WorkflowID == "" {
			group.WorkflowID = workflowID
		}
		if err := upsertWorkflowTransitionGroup(ctx, q, group, int64(10000+i*100), "force workflow transition group"); err != nil {
			t.Fatalf("force workflow transition group %s: %v", group.ID, err)
		}
	}
	for i, edge := range edges {
		if edge.WorkflowID == "" {
			edge.WorkflowID = workflowID
		}
		if err := upsertWorkflowEdge(ctx, q, edge, int64(10000+i*100), "force workflow edge"); err != nil {
			t.Fatalf("force workflow edge %s: %v", edge.ID, err)
		}
	}
	if _, err := q.IncrementWorkflowVersion(ctx, sqlitegen.IncrementWorkflowVersionParams{ID: string(workflowID), UpdatedAtUnixMs: store.now().UnixMilli()}); err != nil {
		t.Fatalf("increment forced workflow version: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit forced graph rows: %v", err)
	}
}

func newTestStore(t *testing.T) (*Store, metadata.Binding) {
	t.Helper()
	store, binding, _ := newTestStoreWithConfig(t)
	return store, binding
}

func newTestStoreContext(t *testing.T) (context.Context, *Store, metadata.Binding) {
	t.Helper()
	store, binding := newTestStore(t)
	return context.Background(), store, binding
}

func newTestStoreWithConfigContext(t *testing.T) (context.Context, *Store, metadata.Binding, config.App) {
	t.Helper()
	store, binding, cfg := newTestStoreWithConfig(t)
	return context.Background(), store, binding, cfg
}

func newTestStoreWithConfig(t *testing.T) (*Store, metadata.Binding, config.App) {
	t.Helper()
	home := t.TempDir()
	workspaceRoot := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(config.PersistenceRootEnvName, filepath.Join(home, "kent-root"))
	cfg, err := config.Load(workspaceRoot, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	metadataStore := testsetup.OpenStore(t, cfg.PersistenceRoot)
	binding, err := metadataStore.RegisterWorkspaceBinding(context.Background(), cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	if err := metadataStore.SetProjectKey(context.Background(), binding.ProjectID, "WOR"); err != nil {
		t.Fatalf("SetProjectKey: %v", err)
	}
	store, err := New(metadataStore, WithRoleResolver(testsetup.QuestionsEnabled("coder", "reviewer")))
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	return store, binding, cfg
}

func createTestSession(t *testing.T, ctx context.Context, store *Store, binding metadata.Binding, cfg config.App) string {
	t.Helper()
	sessionRoot := filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions")
	sessionStore, err := session.Create(sessionRoot, filepath.Base(cfg.WorkspaceRoot), cfg.WorkspaceRoot, sessioncontract.SessionCategoryMain, store.metadata.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := sessionStore.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	if _, err := store.metadata.ResolvePersistedSession(ctx, sessionStore.Meta().SessionID); err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	return sessionStore.Meta().SessionID
}

func linkWorkflow(t *testing.T, ctx context.Context, store *Store, projectID string, workflowID workflow.WorkflowID, isDefault bool) ProjectWorkflowLinkRecord {
	t.Helper()
	link, err := store.LinkWorkflow(ctx, projectID, workflowID, isDefault)
	if err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	return link
}

func createLinkedValidWorkflow(t *testing.T, ctx context.Context, store *Store, projectID string) workflow.WorkflowID {
	t.Helper()
	workflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, projectID, workflowID, true)
	return workflowID
}

func createTask(t *testing.T, ctx context.Context, store *Store, req CreateTaskRequest) TaskRecord {
	t.Helper()
	task, err := store.CreateTask(ctx, req)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

func createDefaultTask(t *testing.T, ctx context.Context, store *Store, projectID string) TaskRecord {
	t.Helper()
	return createTask(t, ctx, store, CreateTaskRequest{ProjectID: projectID, Title: "Task", Body: "Body"})
}

func startTask(t *testing.T, ctx context.Context, store *Store, taskID workflow.TaskID) StartTaskResult {
	t.Helper()
	started, err := store.StartTask(ctx, taskID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	return started
}

func completeRun(t *testing.T, ctx context.Context, store *Store, req CompleteRunRequest) CompleteRunResult {
	t.Helper()
	completed, err := store.CompleteRun(ctx, req)
	if err != nil {
		t.Fatalf("CompleteRun: %v", err)
	}
	return completed.Result
}

func createValidWorkflow(t *testing.T, ctx context.Context, store *Store) workflow.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	agentID := workflow.NodeID("node-agent-" + string(created.ID))
	startGroup := workflow.TransitionGroupID("group-start-" + string(created.ID))
	doneGroup := workflow.TransitionGroupID("group-done-" + string(created.ID))
	saveWorkflowGraphFixture(t, ctx, store, created.ID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		start := nodeByKind(t, def, workflow.NodeKindStart)
		done := nodeByKind(t, def, workflow.NodeKindTerminal)
		req.Nodes = append(req.Nodes, NodeRecord{ID: agentID, WorkflowID: created.ID, Key: "agent", Kind: workflow.NodeKindAgent, DisplayName: "Agent", SubagentRole: "coder", PromptTemplate: "Do work.", OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}})
		req.TransitionGroups = append(req.TransitionGroups,
			TransitionGroupRecord{ID: startGroup, WorkflowID: created.ID, SourceNodeID: workflow.NodeIDOf(start), TransitionID: "start", DisplayName: "Start"},
			TransitionGroupRecord{ID: doneGroup, WorkflowID: created.ID, SourceNodeID: agentID, TransitionID: "done", DisplayName: "Done"},
		)
		req.Edges = append(req.Edges,
			EdgeRecord{ID: workflow.EdgeID("edge-start-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: startGroup, Key: "start", TargetNodeID: agentID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Do work."},
			EdgeRecord{ID: workflow.EdgeID("edge-done-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: doneGroup, Key: "done", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession},
		)
	})
	return created.ID
}

func createScriptStartWorkflow(t *testing.T, ctx context.Context, store *Store, scriptPath string) workflow.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Script Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	scriptID := workflow.NodeID("node-script-" + string(created.ID))
	startGroup := workflow.TransitionGroupID("group-start-" + string(created.ID))
	doneGroup := workflow.TransitionGroupID("group-done-" + string(created.ID))
	saveWorkflowGraphFixture(t, ctx, store, created.ID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		start := nodeByKind(t, def, workflow.NodeKindStart)
		done := nodeByKind(t, def, workflow.NodeKindTerminal)
		req.Nodes = append(req.Nodes, NodeRecord{ID: scriptID, WorkflowID: created.ID, Key: "script", Kind: workflow.NodeKindScript, DisplayName: "Script", ScriptPath: scriptPath})
		req.TransitionGroups = append(req.TransitionGroups,
			TransitionGroupRecord{ID: startGroup, WorkflowID: created.ID, SourceNodeID: workflow.NodeIDOf(start), TransitionID: "start", DisplayName: "Start"},
			TransitionGroupRecord{ID: doneGroup, WorkflowID: created.ID, SourceNodeID: scriptID, TransitionID: "done", DisplayName: "Done"},
		)
		req.Edges = append(req.Edges,
			EdgeRecord{ID: workflow.EdgeID("edge-start-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: startGroup, Key: "start", TargetNodeID: scriptID, ContextMode: workflow.ContextModeNewSession},
			EdgeRecord{ID: workflow.EdgeID("edge-done-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: doneGroup, Key: "done", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession},
		)
	})
	return created.ID
}

type scriptExecutionFixture struct {
	ctx        context.Context
	store      *Store
	workflowID workflow.WorkflowID
	scriptID   workflow.NodeID
	task       TaskRecord
}

func newScriptExecutionFixture(t *testing.T, scriptPath string, contents []byte) scriptExecutionFixture {
	t.Helper()
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createScriptStartWorkflow(t, ctx, store, scriptPath)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	worktreeRoot := filepath.Join(t.TempDir(), "script-worktree")
	if contents != nil {
		path := filepath.Join(worktreeRoot, scriptPath)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create script dir: %v", err)
		}
		if err := os.WriteFile(path, contents, 0o755); err != nil {
			t.Fatalf("write script: %v", err)
		}
	}
	attachManagedWorktree(t, ctx, store, binding.WorkspaceID, task.ID, worktreeRoot)
	return scriptExecutionFixture{ctx: ctx, store: store, workflowID: workflowID, scriptID: workflow.NodeID("node-script-" + string(workflowID)), task: task}
}

func (f scriptExecutionFixture) requireLiveSummary(t *testing.T) {
	t.Helper()
	parameters, err := marshalJSONArray([]workflow.Parameter{{Key: "summary", Description: "Live summary."}})
	if err != nil {
		t.Fatalf("marshal parameters: %v", err)
	}
	// Intentional direct graph mutation: graph-edit policy is owned separately;
	// these tests isolate the execution contract once the live graph has changed.
	if _, err := f.store.db.ExecContext(f.ctx, `UPDATE workflow_edges SET parameters_json = ? WHERE id = ?`, parameters, "edge-done-"+string(f.workflowID)); err != nil {
		t.Fatalf("force live script output contract: %v", err)
	}
}

func attachManagedWorktree(t *testing.T, ctx context.Context, store *Store, workspaceID string, taskID workflow.TaskID, worktreeRoot string) {
	t.Helper()
	if err := os.MkdirAll(worktreeRoot, 0o755); err != nil {
		t.Fatalf("create worktree root: %v", err)
	}
	worktreeID := "worktree-" + string(taskID)
	if err := store.metadata.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{ID: worktreeID, WorkspaceID: workspaceID, CanonicalRoot: worktreeRoot, Managed: true, CreatedBranch: true}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
UPDATE tasks
SET source_workspace_id = ?,
    managed_worktree_id = ?,
    execution_target_mode = ?,
    execution_target_requested_ref = ?,
    execution_target_commit_oid = ?,
    execution_target_provenance = ?
WHERE id = ?`,
		workspaceID,
		worktreeID,
		string(workflow.ExecutionTargetModeHead),
		"HEAD",
		"fixture-commit",
		string(ExecutionTargetProvenanceResolved),
		string(taskID),
	); err != nil {
		t.Fatalf("attach managed worktree to task: %v", err)
	}
}

func createChainedContextModeWorkflow(t *testing.T, ctx context.Context, store *Store, contextMode workflow.ContextMode, targetRole string) workflow.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Chained Context Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	planID := workflow.NodeID("node-plan-" + string(created.ID))
	implID := workflow.NodeID("node-impl-" + string(created.ID))
	nodes := []NodeRecord{
		{ID: planID, WorkflowID: created.ID, Key: "plan", Kind: workflow.NodeKindAgent, DisplayName: "Plan", SubagentRole: "coder", PromptTemplate: "Plan work.", OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}},
		{ID: implID, WorkflowID: created.ID, Key: "implement", Kind: workflow.NodeKindAgent, DisplayName: "Implement", SubagentRole: targetRole, PromptTemplate: "Implement {{.Inputs.prior_summary}}.", InputFields: []workflow.InputField{{Name: "prior_summary", Description: "Prior summary."}}, OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}},
	}
	startGroup := workflow.TransitionGroupID("group-start-" + string(created.ID))
	nextGroup := workflow.TransitionGroupID("group-next-" + string(created.ID))
	doneGroup := workflow.TransitionGroupID("group-done-" + string(created.ID))
	saveWorkflowGraphFixture(t, ctx, store, created.ID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		start := nodeByKind(t, def, workflow.NodeKindStart)
		done := nodeByKind(t, def, workflow.NodeKindTerminal)
		req.Nodes = append(req.Nodes, nodes...)
		req.TransitionGroups = append(req.TransitionGroups,
			TransitionGroupRecord{ID: startGroup, WorkflowID: created.ID, SourceNodeID: workflow.NodeIDOf(start), TransitionID: "start", DisplayName: "Start"},
			TransitionGroupRecord{ID: nextGroup, WorkflowID: created.ID, SourceNodeID: planID, TransitionID: "next", DisplayName: "Next", Description: "Continue after planning is complete."},
			TransitionGroupRecord{ID: doneGroup, WorkflowID: created.ID, SourceNodeID: implID, TransitionID: "done", DisplayName: "Done"},
		)
		req.Edges = append(req.Edges,
			EdgeRecord{ID: workflow.EdgeID("edge-start-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: startGroup, Key: "start", TargetNodeID: planID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Plan work."},
			EdgeRecord{ID: workflow.EdgeID("edge-next-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: nextGroup, Key: "next", TargetNodeID: implID, ContextMode: contextMode, PromptTemplate: "Implement {{.Params.prior_summary}}.", Parameters: []workflow.Parameter{{Key: "prior_summary", Description: "Prior summary."}}},
			EdgeRecord{ID: workflow.EdgeID("edge-done-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: doneGroup, Key: "done", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession},
		)
	})
	return created.ID
}

func createPromptNodeReferenceWorkflow(t *testing.T, ctx context.Context, store *Store) workflow.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Prompt Node Reference Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	planID := workflow.NodeID("node-plan-" + string(created.ID))
	reviewID := workflow.NodeID("node-review-" + string(created.ID))
	auditID := workflow.NodeID("node-audit-" + string(created.ID))
	nodes := []NodeRecord{
		{ID: planID, WorkflowID: created.ID, Key: "plan", Kind: workflow.NodeKindAgent, DisplayName: "Plan", SubagentRole: "coder", PromptTemplate: "Plan work.", OutputFields: []workflow.OutputField{{Name: "summary", Description: "Plan summary."}}},
		{ID: reviewID, WorkflowID: created.ID, Key: "review", Kind: workflow.NodeKindAgent, DisplayName: "Review", SubagentRole: "coder", PromptTemplate: "Review {{.Nodes.plan.summary}}."},
		{ID: auditID, WorkflowID: created.ID, Key: "audit", Kind: workflow.NodeKindAgent, DisplayName: "Audit", SubagentRole: "coder", PromptTemplate: "Audit."},
	}
	startGroup := workflow.TransitionGroupID("group-start-" + string(created.ID))
	nextGroup := workflow.TransitionGroupID("group-next-" + string(created.ID))
	auditGroup := workflow.TransitionGroupID("group-audit-" + string(created.ID))
	doneGroup := workflow.TransitionGroupID("group-done-" + string(created.ID))
	saveWorkflowGraphFixture(t, ctx, store, created.ID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		start := nodeByKind(t, def, workflow.NodeKindStart)
		done := nodeByKind(t, def, workflow.NodeKindTerminal)
		req.Nodes = append(req.Nodes, nodes...)
		req.TransitionGroups = append(req.TransitionGroups,
			TransitionGroupRecord{ID: startGroup, WorkflowID: created.ID, SourceNodeID: workflow.NodeIDOf(start), TransitionID: "start", DisplayName: "Start"},
			TransitionGroupRecord{ID: nextGroup, WorkflowID: created.ID, SourceNodeID: planID, TransitionID: "next", DisplayName: "Next", Description: "Continue after planning is complete."},
			TransitionGroupRecord{ID: auditGroup, WorkflowID: created.ID, SourceNodeID: reviewID, TransitionID: "audit", DisplayName: "Audit"},
			TransitionGroupRecord{ID: doneGroup, WorkflowID: created.ID, SourceNodeID: auditID, TransitionID: "done", DisplayName: "Done"},
		)
		req.Edges = append(req.Edges,
			EdgeRecord{ID: workflow.EdgeID("edge-start-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: startGroup, Key: "start", TargetNodeID: planID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Plan work."},
			EdgeRecord{ID: workflow.EdgeID("edge-next-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: nextGroup, Key: "next", TargetNodeID: reviewID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Review {{.Params.summary}}.", Parameters: []workflow.Parameter{{Key: "summary", Description: "Plan summary."}}},
			EdgeRecord{ID: workflow.EdgeID("edge-audit-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: auditGroup, Key: "audit", TargetNodeID: auditID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Audit {{.Params.next.summary}}."},
			EdgeRecord{ID: workflow.EdgeID("edge-done-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: doneGroup, Key: "done", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession},
		)
	})
	return created.ID
}

func createSelectedContextSourceWorkflow(t *testing.T, ctx context.Context, store *Store, contextMode workflow.ContextMode) workflow.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Selected Context Source Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	planID := workflow.NodeID("node-plan-" + string(created.ID))
	implementationID := workflow.NodeID("node-implementation-" + string(created.ID))
	acceptanceID := workflow.NodeID("node-acceptance-" + string(created.ID))
	openPRID := workflow.NodeID("node-open-pr-" + string(created.ID))
	nodes := []NodeRecord{
		{ID: planID, WorkflowID: created.ID, Key: "plan", Kind: workflow.NodeKindAgent, DisplayName: "Plan", SubagentRole: "coder", PromptTemplate: "Plan.", OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}},
		{ID: implementationID, WorkflowID: created.ID, Key: "implementation", Kind: workflow.NodeKindAgent, DisplayName: "Implementation", SubagentRole: "coder", PromptTemplate: "Implement.", InputFields: []workflow.InputField{{Name: "summary", Description: "Plan summary."}}, OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}},
		{ID: acceptanceID, WorkflowID: created.ID, Key: "acceptance", Kind: workflow.NodeKindAgent, DisplayName: "Acceptance", SubagentRole: "coder", PromptTemplate: "Accept.", InputFields: []workflow.InputField{{Name: "summary", Description: "Implementation summary."}}, OutputFields: []workflow.OutputField{{Name: "decision", Description: "Decision."}}},
		{ID: openPRID, WorkflowID: created.ID, Key: "open_pr", Kind: workflow.NodeKindAgent, DisplayName: "Open PR", SubagentRole: "coder", PromptTemplate: "Open PR {{.Inputs.acceptance_decision}}.", InputFields: []workflow.InputField{{Name: "acceptance_decision", Description: "Acceptance decision."}}, OutputFields: []workflow.OutputField{{Name: "pr_url", Description: "PR URL."}}},
	}
	startGroup := workflow.TransitionGroupID("group-start-" + string(created.ID))
	implementGroup := workflow.TransitionGroupID("group-implement-" + string(created.ID))
	acceptGroup := workflow.TransitionGroupID("group-accept-" + string(created.ID))
	openPRGroup := workflow.TransitionGroupID("group-open-pr-" + string(created.ID))
	doneGroup := workflow.TransitionGroupID("group-done-" + string(created.ID))
	saveWorkflowGraphFixture(t, ctx, store, created.ID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		start := nodeByKind(t, def, workflow.NodeKindStart)
		done := nodeByKind(t, def, workflow.NodeKindTerminal)
		req.Nodes = append(req.Nodes, nodes...)
		req.TransitionGroups = append(req.TransitionGroups,
			TransitionGroupRecord{ID: startGroup, WorkflowID: created.ID, SourceNodeID: workflow.NodeIDOf(start), TransitionID: "start", DisplayName: "Start"},
			TransitionGroupRecord{ID: implementGroup, WorkflowID: created.ID, SourceNodeID: planID, TransitionID: "implement", DisplayName: "Implement"},
			TransitionGroupRecord{ID: acceptGroup, WorkflowID: created.ID, SourceNodeID: implementationID, TransitionID: "accept", DisplayName: "Accept"},
			TransitionGroupRecord{ID: openPRGroup, WorkflowID: created.ID, SourceNodeID: acceptanceID, TransitionID: "open_pr", DisplayName: "Open PR"},
			TransitionGroupRecord{ID: doneGroup, WorkflowID: created.ID, SourceNodeID: openPRID, TransitionID: "done", DisplayName: "Done"},
		)
		req.Edges = append(req.Edges,
			EdgeRecord{ID: workflow.EdgeID("edge-start-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: startGroup, Key: "start", TargetNodeID: planID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Plan."},
			EdgeRecord{ID: workflow.EdgeID("edge-implement-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: implementGroup, Key: "implement", TargetNodeID: implementationID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Implement {{.Params.summary}}.", Parameters: []workflow.Parameter{{Key: "summary", Description: "Plan summary."}}},
			EdgeRecord{ID: workflow.EdgeID("edge-accept-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: acceptGroup, Key: "accept", TargetNodeID: acceptanceID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Accept {{.Params.summary}}.", Parameters: []workflow.Parameter{{Key: "summary", Description: "Implementation summary."}}},
			EdgeRecord{ID: workflow.EdgeID("edge-open-pr-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: openPRGroup, Key: "open_pr", TargetNodeID: openPRID, ContextMode: contextMode, ContextSource: workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "implementation"}, PromptTemplate: "Open PR {{.Params.acceptance_decision}}.", Parameters: []workflow.Parameter{{Key: "acceptance_decision", Description: "Acceptance decision."}}},
			EdgeRecord{ID: workflow.EdgeID("edge-done-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: doneGroup, Key: "done", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession},
		)
	})
	return created.ID
}
