package workflowview

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"core/internal/testharness/testsetup"
	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/config"
	"core/shared/serverapi"
)

func newWorkflowViewTestStore(t testing.TB) (*metadata.Store, *workflowstore.Store, metadata.Binding) {
	t.Helper()
	home := t.TempDir()
	workspaceRoot := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KENT_PERSISTENCE_ROOT", filepath.Join(home, ".kent"))
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
	workflowStore, err := workflowstore.New(metadataStore, workflowstore.WithRoleResolver(testsetup.QuestionsEnabled("coder")))
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	return metadataStore, workflowStore, binding
}

func newWorkflowViewTestContextStore(t *testing.T) (context.Context, *metadata.Store, *workflowstore.Store, metadata.Binding) {
	t.Helper()
	store, workflowStore, binding := newWorkflowViewTestStore(t)
	return context.Background(), store, workflowStore, binding
}

func newWorkflowViewTestFixtureWithStore(t *testing.T) (*metadata.Store, *workflowstore.Store, metadata.Binding, *workflowViewTestFixture) {
	t.Helper()
	store, workflowStore, binding := newWorkflowViewTestStore(t)
	view, err := newWorkflowViewTestFixture(store, workflowStore, nil, nil)
	if err != nil {
		t.Fatalf("newWorkflowViewTestFixture: %v", err)
	}
	return store, workflowStore, binding, view
}

func newWorkflowViewTestContextFixture(t *testing.T) (context.Context, *metadata.Store, *workflowstore.Store, metadata.Binding, *workflowViewTestFixture) {
	t.Helper()
	store, workflowStore, binding, view := newWorkflowViewTestFixtureWithStore(t)
	return context.Background(), store, workflowStore, binding, view
}

func runWorkflowViewGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %q: %v\n%s", args, dir, err, output)
	}
	return strings.TrimSpace(string(output))
}

func forceCanceledBacklogPlacementWithoutTerminal(t *testing.T, ctx context.Context, store *metadata.Store, taskID workflow.TaskID, workflowID workflow.WorkflowID) {
	t.Helper()
	var startNodeID string
	if err := store.DB().QueryRowContext(ctx, `
SELECT id
FROM workflow_nodes
WHERE workflow_id = ?
  AND kind = 'start'`, string(workflowID)).Scan(&startNodeID); err != nil {
		t.Fatalf("resolve canceled backlog start node: %v", err)
	}
	if _, err := store.Queries().DeleteTaskNodePlacementsByTask(ctx, string(taskID)); err != nil {
		t.Fatalf("remove canceled task placements: %v", err)
	}
	if err := store.Queries().InsertTaskNodePlacement(ctx, sqlitegen.InsertTaskNodePlacementParams{
		ID:              "placement-canceled-backlog-" + string(taskID),
		TaskID:          string(taskID),
		NodeID:          sql.NullString{String: startNodeID, Valid: strings.TrimSpace(startNodeID) != ""},
		State:           "active",
		CreatedAtUnixMs: 1,
		UpdatedAtUnixMs: 1,
	}); err != nil {
		t.Fatalf("insert canceled backlog placement: %v", err)
	}
}

func requireDoneTransitionApproval(t *testing.T, ctx context.Context, store *metadata.Store, workflowID workflow.WorkflowID) {
	t.Helper()
	if _, err := store.DB().ExecContext(ctx, `
UPDATE workflow_edges
SET requires_approval = 1
WHERE edge_key = 'done'
  AND EXISTS (
      SELECT 1
      FROM workflow_transition_groups tg
      JOIN workflow_nodes source ON source.id = tg.source_node_id
      WHERE tg.id = workflow_edges.transition_group_id
        AND source.workflow_id = ?
  )`, string(workflowID)); err != nil {
		t.Fatalf("require approval: %v", err)
	}
}

func createWorkflowViewValidWorkflow(t testing.TB, ctx context.Context, store *workflowstore.Store) workflow.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: "Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := workflowViewNodeByKind(t, def, workflow.NodeKindStart)
	done := workflowViewNodeByKind(t, def, workflow.NodeKindTerminal)
	agentID := workflow.NodeID("node-agent-" + string(created.ID))
	if _, err := store.AddNode(ctx, workflowstore.NodeRecord{ID: agentID, WorkflowID: created.ID, Key: "agent", Kind: workflow.NodeKindAgent, DisplayName: "Agent", SubagentRole: "coder"}); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, workflowstore.TransitionGroupRecord{ID: workflow.TransitionGroupID("group-start-" + string(created.ID)), WorkflowID: created.ID, SourceNodeID: workflow.NodeIDOf(start), TransitionID: "start", DisplayName: "Start"}); err != nil {
		t.Fatalf("AddTransitionGroup start: %v", err)
	}
	if _, err := store.AddEdge(ctx, workflowstore.EdgeRecord{ID: workflow.EdgeID("edge-start-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: workflow.TransitionGroupID("group-start-" + string(created.ID)), Key: "start", TargetNodeID: agentID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Do work."}); err != nil {
		t.Fatalf("AddEdge start: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, workflowstore.TransitionGroupRecord{ID: workflow.TransitionGroupID("group-done-" + string(created.ID)), WorkflowID: created.ID, SourceNodeID: agentID, TransitionID: "done", DisplayName: "Done"}); err != nil {
		t.Fatalf("AddTransitionGroup done: %v", err)
	}
	if _, err := store.AddEdge(ctx, workflowstore.EdgeRecord{ID: workflow.EdgeID("edge-done-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: workflow.TransitionGroupID("group-done-" + string(created.ID)), Key: "done", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession}); err != nil {
		t.Fatalf("AddEdge done: %v", err)
	}
	return created.ID
}

func createWorkflowViewFanoutWorkflow(t *testing.T, ctx context.Context, store *workflowstore.Store) workflow.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: "Fanout Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := workflowViewNodeByKind(t, def, workflow.NodeKindStart)
	done := workflowViewNodeByKind(t, def, workflow.NodeKindTerminal)
	planID := workflow.NodeID("node-plan-" + string(created.ID))
	implAID := workflow.NodeID("node-impl-a-" + string(created.ID))
	implBID := workflow.NodeID("node-impl-b-" + string(created.ID))
	implCID := workflow.NodeID("node-impl-c-" + string(created.ID))
	joinID := workflow.NodeID("node-join-" + string(created.ID))
	synthID := workflow.NodeID("node-synth-" + string(created.ID))
	for _, node := range []workflowstore.NodeRecord{
		{ID: planID, WorkflowID: created.ID, Key: "plan", Kind: workflow.NodeKindAgent, DisplayName: "Plan", SubagentRole: "coder"},
		{ID: implAID, WorkflowID: created.ID, Key: "impl_a", Kind: workflow.NodeKindAgent, DisplayName: "Implement A", SubagentRole: "coder"},
		{ID: implBID, WorkflowID: created.ID, Key: "impl_b", Kind: workflow.NodeKindAgent, DisplayName: "Implement B", SubagentRole: "coder"},
		{ID: implCID, WorkflowID: created.ID, Key: "impl_c", Kind: workflow.NodeKindAgent, DisplayName: "Implement C", SubagentRole: "coder"},
		{ID: joinID, WorkflowID: created.ID, Key: "join", Kind: workflow.NodeKindJoin, DisplayName: "Join"},
		{ID: synthID, WorkflowID: created.ID, Key: "synth", Kind: workflow.NodeKindAgent, DisplayName: "Synthesize", SubagentRole: "coder"},
	} {
		if _, err := store.AddNode(ctx, node); err != nil {
			t.Fatalf("AddNode %s: %v", node.Key, err)
		}
	}
	startGroup := workflow.TransitionGroupID("group-start-" + string(created.ID))
	splitGroup := workflow.TransitionGroupID("group-split-" + string(created.ID))
	joinAGroup := workflow.TransitionGroupID("group-join-a-" + string(created.ID))
	joinBGroup := workflow.TransitionGroupID("group-join-b-" + string(created.ID))
	joinCGroup := workflow.TransitionGroupID("group-join-c-" + string(created.ID))
	synthGroup := workflow.TransitionGroupID("group-join-synth-" + string(created.ID))
	doneGroup := workflow.TransitionGroupID("group-synth-done-" + string(created.ID))
	for _, group := range []workflowstore.TransitionGroupRecord{
		{ID: startGroup, WorkflowID: created.ID, SourceNodeID: workflow.NodeIDOf(start), TransitionID: "start", DisplayName: "Start"},
		{ID: splitGroup, WorkflowID: created.ID, SourceNodeID: planID, TransitionID: "split", DisplayName: "Split"},
		{ID: joinAGroup, WorkflowID: created.ID, SourceNodeID: implAID, TransitionID: "join", DisplayName: "Join"},
		{ID: joinBGroup, WorkflowID: created.ID, SourceNodeID: implBID, TransitionID: "join", DisplayName: "Join"},
		{ID: joinCGroup, WorkflowID: created.ID, SourceNodeID: implCID, TransitionID: "join", DisplayName: "Join"},
		{ID: synthGroup, WorkflowID: created.ID, SourceNodeID: joinID, TransitionID: "done", DisplayName: "Done"},
		{ID: doneGroup, WorkflowID: created.ID, SourceNodeID: synthID, TransitionID: "done", DisplayName: "Done"},
	} {
		if _, err := store.AddTransitionGroup(ctx, group); err != nil {
			t.Fatalf("AddTransitionGroup %s: %v", group.TransitionID, err)
		}
	}
	for _, edge := range []workflowstore.EdgeRecord{
		{ID: workflow.EdgeID("edge-start-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: startGroup, Key: "start", TargetNodeID: planID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Plan."},
		{ID: workflow.EdgeID("edge-split-a-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: splitGroup, Key: "split_a", TargetNodeID: implAID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Implement A.", Parameters: []workflow.Parameter{{Key: "summary", Description: "Plan summary."}}},
		{ID: workflow.EdgeID("edge-split-b-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: splitGroup, Key: "split_b", TargetNodeID: implBID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Implement B.", Parameters: []workflow.Parameter{{Key: "summary", Description: "Plan summary."}}},
		{ID: workflow.EdgeID("edge-split-c-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: splitGroup, Key: "split_c", TargetNodeID: implCID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Implement C.", Parameters: []workflow.Parameter{{Key: "summary", Description: "Plan summary."}}},
		{ID: workflow.EdgeID("edge-join-a-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: joinAGroup, Key: "join_a", TargetNodeID: joinID, ContextMode: workflow.ContextModeNewSession, Parameters: []workflow.Parameter{{Key: "summary", Description: "Implementation summary."}}},
		{ID: workflow.EdgeID("edge-join-b-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: joinBGroup, Key: "join_b", TargetNodeID: joinID, ContextMode: workflow.ContextModeNewSession},
		{ID: workflow.EdgeID("edge-join-c-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: joinCGroup, Key: "join_c", TargetNodeID: joinID, ContextMode: workflow.ContextModeNewSession},
		{ID: workflow.EdgeID("edge-join-synth-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: synthGroup, Key: "synth", TargetNodeID: synthID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Synthesize."},
		{ID: workflow.EdgeID("edge-synth-done-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: doneGroup, Key: "done", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession},
	} {
		if _, err := store.AddEdge(ctx, edge); err != nil {
			t.Fatalf("AddEdge %s: %v", edge.Key, err)
		}
	}
	return created.ID
}

func workflowViewNodeByKind(t testing.TB, def workflow.Definition, kind workflow.NodeKind) workflow.Node {
	t.Helper()
	for _, node := range def.Nodes {
		if node.Kind() == kind {
			return node
		}
	}
	t.Fatalf("missing node kind %q in %+v", kind, def.Nodes)
	return nil
}

func workflowViewNodeByKey(t *testing.T, def workflow.Definition, key string) workflow.Node {
	t.Helper()
	for _, node := range def.Nodes {
		if workflow.NodeKey(node) == workflow.ModelKey(key) {
			return node
		}
	}
	t.Fatalf("missing workflow node key %q in %+v", key, def.Nodes)
	return nil
}

func workflowViewColumnByKind(t *testing.T, board serverapi.WorkflowBoard, kind workflow.NodeKind) serverapi.WorkflowBoardColumn {
	t.Helper()
	for _, column := range board.Columns {
		if column.Node.Kind == string(kind) {
			return column
		}
	}
	t.Fatalf("missing board column kind %q in %+v", kind, board.Columns)
	return serverapi.WorkflowBoardColumn{}
}

func workflowViewColumnByKey(t *testing.T, board serverapi.WorkflowBoard, key string) serverapi.WorkflowBoardColumn {
	t.Helper()
	for _, column := range board.Columns {
		if column.Node.Key == key {
			return column
		}
	}
	t.Fatalf("missing board column key %q in %+v", key, board.Columns)
	return serverapi.WorkflowBoardColumn{}
}

func workflowViewBoardColumnKeys(columns []serverapi.WorkflowBoardColumn) []string {
	keys := make([]string, 0, len(columns))
	for _, column := range columns {
		keys = append(keys, column.Node.Key)
	}
	return keys
}

func workflowViewBoardCardIDs(cards []serverapi.WorkflowBoardTaskCard) []string {
	ids := make([]string, 0, len(cards))
	for _, card := range cards {
		ids = append(ids, card.TaskID)
	}
	return ids
}

type boardNodeCardsTokenFixture struct {
	Version         int                          `json:"version"`
	ProjectID       string                       `json:"project_id"`
	WorkflowID      string                       `json:"workflow_id"`
	NodeID          string                       `json:"node_id"`
	LabelFilter     workflowTaskLabelFilterFacts `json:"label_filter"`
	UpdatedAtUnixMs int64                        `json:"updated_at_unix_ms"`
	TaskID          string                       `json:"task_id"`
	Direction       string                       `json:"direction"`
}

func mutateBoardNodeCardsToken(t *testing.T, token *string, mutate func(*boardNodeCardsTokenFixture)) string {
	t.Helper()
	if token == nil {
		t.Fatal("page token is required")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(*token)
	if err != nil {
		t.Fatalf("decode page token: %v", err)
	}
	var payload boardNodeCardsTokenFixture
	if err := json.Unmarshal(decoded, &payload); err != nil {
		t.Fatalf("unmarshal page token: %v", err)
	}
	mutate(&payload)
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal page token: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(encoded)
}

type staticTranscriptProvider struct {
	entries map[string][]PendingQuestionTranscriptEntry
}

type staticPendingPromptSource map[string][]PendingPromptSnapshot

func (s staticPendingPromptSource) ListPendingPrompts(sessionID string) ([]PendingPromptSnapshot, error) {
	return append([]PendingPromptSnapshot(nil), s[strings.TrimSpace(sessionID)]...), nil
}

func (p staticTranscriptProvider) SessionNewestActiveSegmentQuestions(_ context.Context, sessionID string) ([]PendingQuestionTranscriptEntry, error) {
	return append([]PendingQuestionTranscriptEntry(nil), p.entries[strings.TrimSpace(sessionID)]...), nil
}

func transcriptEntriesWithAsk(askID string, question string) []PendingQuestionTranscriptEntry {
	return []PendingQuestionTranscriptEntry{askTranscriptEntry(askID, question, nil, nil)}
}

func attentionPointerEquals[T comparable](value *T, want T) bool {
	return value != nil && *value == want
}

func transcriptEntriesWithAskOptions(askID string, question string, suggestions []string, recommended int) []PendingQuestionTranscriptEntry {
	return []PendingQuestionTranscriptEntry{askTranscriptEntry(askID, question, suggestions, &recommended)}
}

func askTranscriptEntry(askID string, question string, suggestions []string, recommended *int) PendingQuestionTranscriptEntry {
	return PendingQuestionTranscriptEntry{
		AskID:                  askID,
		Question:               question,
		Suggestions:            suggestions,
		RecommendedOptionIndex: recommended,
	}
}
