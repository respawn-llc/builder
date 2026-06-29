package workflowstore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/session"
	"core/server/workflow"
	"core/shared/config"
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
		if node.ID == nodeID {
			return node
		}
	}
	t.Fatalf("node %q not found in %+v", nodeID, def.Nodes)
	return workflow.Node{}
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
		if err := upsertWorkflowNode(ctx, q, node, int64(10000+i*100)); err != nil {
			t.Fatalf("force workflow node %s: %v", node.ID, err)
		}
	}
	for i, group := range groups {
		if group.WorkflowID == "" {
			group.WorkflowID = workflowID
		}
		if err := upsertWorkflowTransitionGroup(ctx, q, group, int64(10000+i*100)); err != nil {
			t.Fatalf("force workflow transition group %s: %v", group.ID, err)
		}
	}
	for i, edge := range edges {
		if edge.WorkflowID == "" {
			edge.WorkflowID = workflowID
		}
		if err := upsertWorkflowEdge(ctx, q, edge, int64(10000+i*100)); err != nil {
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
	cfg, err := config.Load(workspaceRoot, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	metadataStore, err := metadata.Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
	binding, err := metadataStore.RegisterWorkspaceBinding(context.Background(), cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	if err := metadataStore.SetProjectKey(context.Background(), binding.ProjectID, "WOR"); err != nil {
		t.Fatalf("SetProjectKey: %v", err)
	}
	store, err := New(metadataStore, WithRoleResolver(workflow.StaticRoleResolver{"coder": true, "reviewer": true}))
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	return store, binding, cfg
}

func createTestSession(t *testing.T, ctx context.Context, store *Store, binding metadata.Binding, cfg config.App) string {
	t.Helper()
	sessionRoot := filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions")
	sessionStore, err := session.Create(sessionRoot, filepath.Base(cfg.WorkspaceRoot), cfg.WorkspaceRoot, store.metadata.AuthoritativeSessionStoreOptions()...)
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

func linkWorkflow(t *testing.T, ctx context.Context, store *Store, projectID string, workflowID workflow.WorkflowID, isDefault bool) {
	t.Helper()
	if _, err := store.LinkWorkflow(ctx, projectID, workflowID, isDefault); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
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
	return completed
}

func createValidWorkflow(t *testing.T, ctx context.Context, store *Store) workflow.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := nodeByKind(t, def, workflow.NodeKindStart)
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	if _, err := store.AddNode(ctx, NodeRecord{ID: workflow.NodeID("node-agent-" + string(created.ID)), WorkflowID: created.ID, Key: "agent", Kind: workflow.NodeKindAgent, DisplayName: "Agent", SubagentRole: "coder", PromptTemplate: "Do work.", OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}}); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	agentID := workflow.NodeID("node-agent-" + string(created.ID))
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: workflow.TransitionGroupID("group-start-" + string(created.ID)), WorkflowID: created.ID, SourceNodeID: start.ID, TransitionID: "start", DisplayName: "Start"}); err != nil {
		t.Fatalf("AddTransitionGroup start: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: workflow.EdgeID("edge-start-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: workflow.TransitionGroupID("group-start-" + string(created.ID)), Key: "start", TargetNodeID: agentID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Do work."}); err != nil {
		t.Fatalf("AddEdge start: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: workflow.TransitionGroupID("group-done-" + string(created.ID)), WorkflowID: created.ID, SourceNodeID: agentID, TransitionID: "done", DisplayName: "Done"}); err != nil {
		t.Fatalf("AddTransitionGroup done: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: workflow.EdgeID("edge-done-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: workflow.TransitionGroupID("group-done-" + string(created.ID)), Key: "done", TargetNodeID: done.ID, ContextMode: workflow.ContextModeNewSession}); err != nil {
		t.Fatalf("AddEdge done: %v", err)
	}
	return created.ID
}

func createChainedContextModeWorkflow(t *testing.T, ctx context.Context, store *Store, contextMode workflow.ContextMode, targetRole string) workflow.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Chained Context Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := nodeByKind(t, def, workflow.NodeKindStart)
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	planID := workflow.NodeID("node-plan-" + string(created.ID))
	implID := workflow.NodeID("node-impl-" + string(created.ID))
	for _, node := range []NodeRecord{
		{ID: planID, WorkflowID: created.ID, Key: "plan", Kind: workflow.NodeKindAgent, DisplayName: "Plan", SubagentRole: "coder", PromptTemplate: "Plan work.", OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}},
		{ID: implID, WorkflowID: created.ID, Key: "implement", Kind: workflow.NodeKindAgent, DisplayName: "Implement", SubagentRole: targetRole, PromptTemplate: "Implement {{.Inputs.prior_summary}}.", InputFields: []workflow.InputField{{Name: "prior_summary", Description: "Prior summary."}}, OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}},
	} {
		if _, err := store.AddNode(ctx, node); err != nil {
			t.Fatalf("AddNode %s: %v", node.Key, err)
		}
	}
	startGroup := workflow.TransitionGroupID("group-start-" + string(created.ID))
	nextGroup := workflow.TransitionGroupID("group-next-" + string(created.ID))
	doneGroup := workflow.TransitionGroupID("group-done-" + string(created.ID))
	for _, group := range []TransitionGroupRecord{
		{ID: startGroup, WorkflowID: created.ID, SourceNodeID: start.ID, TransitionID: "start", DisplayName: "Start"},
		{ID: nextGroup, WorkflowID: created.ID, SourceNodeID: planID, TransitionID: "next", DisplayName: "Next", Description: "Continue after planning is complete."},
		{ID: doneGroup, WorkflowID: created.ID, SourceNodeID: implID, TransitionID: "done", DisplayName: "Done"},
	} {
		if _, err := store.AddTransitionGroup(ctx, group); err != nil {
			t.Fatalf("AddTransitionGroup %s: %v", group.TransitionID, err)
		}
	}
	for _, edge := range []EdgeRecord{
		{ID: workflow.EdgeID("edge-start-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: startGroup, Key: "start", TargetNodeID: planID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Plan work."},
		{ID: workflow.EdgeID("edge-next-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: nextGroup, Key: "next", TargetNodeID: implID, ContextMode: contextMode, PromptTemplate: "Implement {{.Params.prior_summary}}.", Parameters: []workflow.Parameter{{Key: "prior_summary", Description: "Prior summary."}}},
		{ID: workflow.EdgeID("edge-done-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: doneGroup, Key: "done", TargetNodeID: done.ID, ContextMode: workflow.ContextModeNewSession},
	} {
		if _, err := store.AddEdge(ctx, edge); err != nil {
			t.Fatalf("AddEdge %s: %v", edge.Key, err)
		}
	}
	return created.ID
}

func createPromptNodeReferenceWorkflow(t *testing.T, ctx context.Context, store *Store) workflow.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Prompt Node Reference Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := nodeByKind(t, def, workflow.NodeKindStart)
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	planID := workflow.NodeID("node-plan-" + string(created.ID))
	reviewID := workflow.NodeID("node-review-" + string(created.ID))
	auditID := workflow.NodeID("node-audit-" + string(created.ID))
	for _, node := range []NodeRecord{
		{ID: planID, WorkflowID: created.ID, Key: "plan", Kind: workflow.NodeKindAgent, DisplayName: "Plan", SubagentRole: "coder", PromptTemplate: "Plan work.", OutputFields: []workflow.OutputField{{Name: "summary", Description: "Plan summary."}}},
		{ID: reviewID, WorkflowID: created.ID, Key: "review", Kind: workflow.NodeKindAgent, DisplayName: "Review", SubagentRole: "coder", PromptTemplate: "Review {{.Nodes.plan.summary}}."},
		{ID: auditID, WorkflowID: created.ID, Key: "audit", Kind: workflow.NodeKindAgent, DisplayName: "Audit", SubagentRole: "coder", PromptTemplate: "Audit."},
	} {
		if _, err := store.AddNode(ctx, node); err != nil {
			t.Fatalf("AddNode %s: %v", node.Key, err)
		}
	}
	startGroup := workflow.TransitionGroupID("group-start-" + string(created.ID))
	nextGroup := workflow.TransitionGroupID("group-next-" + string(created.ID))
	auditGroup := workflow.TransitionGroupID("group-audit-" + string(created.ID))
	doneGroup := workflow.TransitionGroupID("group-done-" + string(created.ID))
	for _, group := range []TransitionGroupRecord{
		{ID: startGroup, WorkflowID: created.ID, SourceNodeID: start.ID, TransitionID: "start", DisplayName: "Start"},
		{ID: nextGroup, WorkflowID: created.ID, SourceNodeID: planID, TransitionID: "next", DisplayName: "Next", Description: "Continue after planning is complete."},
		{ID: auditGroup, WorkflowID: created.ID, SourceNodeID: reviewID, TransitionID: "audit", DisplayName: "Audit"},
		{ID: doneGroup, WorkflowID: created.ID, SourceNodeID: auditID, TransitionID: "done", DisplayName: "Done"},
	} {
		if _, err := store.AddTransitionGroup(ctx, group); err != nil {
			t.Fatalf("AddTransitionGroup %s: %v", group.TransitionID, err)
		}
	}
	for _, edge := range []EdgeRecord{
		{ID: workflow.EdgeID("edge-start-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: startGroup, Key: "start", TargetNodeID: planID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Plan work."},
		{ID: workflow.EdgeID("edge-next-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: nextGroup, Key: "next", TargetNodeID: reviewID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Review {{.Params.summary}}.", Parameters: []workflow.Parameter{{Key: "summary", Description: "Plan summary."}}},
		{ID: workflow.EdgeID("edge-audit-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: auditGroup, Key: "audit", TargetNodeID: auditID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Audit {{.Params.next.summary}}."},
		{ID: workflow.EdgeID("edge-done-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: doneGroup, Key: "done", TargetNodeID: done.ID, ContextMode: workflow.ContextModeNewSession},
	} {
		if _, err := store.AddEdge(ctx, edge); err != nil {
			t.Fatalf("AddEdge %s: %v", edge.Key, err)
		}
	}
	return created.ID
}

func createSelectedContextSourceWorkflow(t *testing.T, ctx context.Context, store *Store, contextMode workflow.ContextMode) workflow.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Selected Context Source Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := nodeByKind(t, def, workflow.NodeKindStart)
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	planID := workflow.NodeID("node-plan-" + string(created.ID))
	implementationID := workflow.NodeID("node-implementation-" + string(created.ID))
	acceptanceID := workflow.NodeID("node-acceptance-" + string(created.ID))
	openPRID := workflow.NodeID("node-open-pr-" + string(created.ID))
	for _, node := range []NodeRecord{
		{ID: planID, WorkflowID: created.ID, Key: "plan", Kind: workflow.NodeKindAgent, DisplayName: "Plan", SubagentRole: "coder", PromptTemplate: "Plan.", OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}},
		{ID: implementationID, WorkflowID: created.ID, Key: "implementation", Kind: workflow.NodeKindAgent, DisplayName: "Implementation", SubagentRole: "coder", PromptTemplate: "Implement.", InputFields: []workflow.InputField{{Name: "summary", Description: "Plan summary."}}, OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}},
		{ID: acceptanceID, WorkflowID: created.ID, Key: "acceptance", Kind: workflow.NodeKindAgent, DisplayName: "Acceptance", SubagentRole: "coder", PromptTemplate: "Accept.", InputFields: []workflow.InputField{{Name: "summary", Description: "Implementation summary."}}, OutputFields: []workflow.OutputField{{Name: "decision", Description: "Decision."}}},
		{ID: openPRID, WorkflowID: created.ID, Key: "open_pr", Kind: workflow.NodeKindAgent, DisplayName: "Open PR", SubagentRole: "coder", PromptTemplate: "Open PR {{.Inputs.acceptance_decision}}.", InputFields: []workflow.InputField{{Name: "acceptance_decision", Description: "Acceptance decision."}}, OutputFields: []workflow.OutputField{{Name: "pr_url", Description: "PR URL."}}},
	} {
		if _, err := store.AddNode(ctx, node); err != nil {
			t.Fatalf("AddNode %s: %v", node.Key, err)
		}
	}
	startGroup := workflow.TransitionGroupID("group-start-" + string(created.ID))
	implementGroup := workflow.TransitionGroupID("group-implement-" + string(created.ID))
	acceptGroup := workflow.TransitionGroupID("group-accept-" + string(created.ID))
	openPRGroup := workflow.TransitionGroupID("group-open-pr-" + string(created.ID))
	doneGroup := workflow.TransitionGroupID("group-done-" + string(created.ID))
	for _, group := range []TransitionGroupRecord{
		{ID: startGroup, WorkflowID: created.ID, SourceNodeID: start.ID, TransitionID: "start", DisplayName: "Start"},
		{ID: implementGroup, WorkflowID: created.ID, SourceNodeID: planID, TransitionID: "implement", DisplayName: "Implement"},
		{ID: acceptGroup, WorkflowID: created.ID, SourceNodeID: implementationID, TransitionID: "accept", DisplayName: "Accept"},
		{ID: openPRGroup, WorkflowID: created.ID, SourceNodeID: acceptanceID, TransitionID: "open_pr", DisplayName: "Open PR"},
		{ID: doneGroup, WorkflowID: created.ID, SourceNodeID: openPRID, TransitionID: "done", DisplayName: "Done"},
	} {
		if _, err := store.AddTransitionGroup(ctx, group); err != nil {
			t.Fatalf("AddTransitionGroup %s: %v", group.TransitionID, err)
		}
	}
	for _, edge := range []EdgeRecord{
		{ID: workflow.EdgeID("edge-start-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: startGroup, Key: "start", TargetNodeID: planID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Plan."},
		{ID: workflow.EdgeID("edge-implement-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: implementGroup, Key: "implement", TargetNodeID: implementationID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Implement {{.Params.summary}}.", Parameters: []workflow.Parameter{{Key: "summary", Description: "Plan summary."}}},
		{ID: workflow.EdgeID("edge-accept-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: acceptGroup, Key: "accept", TargetNodeID: acceptanceID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Accept {{.Params.summary}}.", Parameters: []workflow.Parameter{{Key: "summary", Description: "Implementation summary."}}},
		{ID: workflow.EdgeID("edge-open-pr-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: openPRGroup, Key: "open_pr", TargetNodeID: openPRID, ContextMode: contextMode, ContextSource: workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "implementation"}, PromptTemplate: "Open PR {{.Params.acceptance_decision}}.", Parameters: []workflow.Parameter{{Key: "acceptance_decision", Description: "Acceptance decision."}}},
		{ID: workflow.EdgeID("edge-done-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: doneGroup, Key: "done", TargetNodeID: done.ID, ContextMode: workflow.ContextModeNewSession},
	} {
		if _, err := store.AddEdge(ctx, edge); err != nil {
			t.Fatalf("AddEdge %s: %v", edge.Key, err)
		}
	}
	return created.ID
}
