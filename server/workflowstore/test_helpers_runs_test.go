package workflowstore

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
)

func addPreviousTargetReworkEdge(t *testing.T, ctx context.Context, store *Store, workflowID workflow.WorkflowID, acceptanceNodeID workflow.NodeID, implementationNodeID workflow.NodeID, requiresApproval bool) {
	t.Helper()
	reworkGroup := workflow.TransitionGroupID("group-previous-target-rework-" + string(workflowID))
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: reworkGroup, WorkflowID: workflowID, SourceNodeID: acceptanceNodeID, TransitionID: "rework", DisplayName: "Rework"}); err != nil {
		t.Fatalf("AddTransitionGroup rework: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{
		ID:                workflow.EdgeID("edge-previous-target-rework-" + string(workflowID)),
		WorkflowID:        workflowID,
		TransitionGroupID: reworkGroup,
		Key:               "rework",
		TargetNodeID:      implementationNodeID,
		ContextMode:       workflow.ContextModeContinueSession,
		ContextSource:     workflow.ContextSource{Kind: workflow.ContextSourcePreviousTarget},
		RequiresApproval:  requiresApproval,
		PromptTemplate:    "Implement {{.Params.summary}}.",
		Parameters:        []workflow.Parameter{{Key: "summary", Description: "Rework summary."}},
	}); err != nil {
		t.Fatalf("AddEdge rework: %v", err)
	}
}

func addOutputFieldToNode(t *testing.T, ctx context.Context, store *Store, workflowID workflow.WorkflowID, node workflow.Node, field workflow.OutputField) {
	t.Helper()
	outputFields := append([]workflow.OutputField(nil), node.OutputFields...)
	outputFields = append(outputFields, field)
	if _, err := store.UpdateNode(ctx, NodeRecord{
		ID:                 node.ID,
		WorkflowID:         workflowID,
		Key:                node.Key,
		Kind:               node.Kind,
		DisplayName:        node.DisplayName,
		GroupID:            node.GroupID,
		SubagentRole:       node.SubagentRole,
		PromptTemplate:     node.PromptTemplate,
		InputFields:        node.InputFields,
		JoinInputProviders: node.JoinInputProviders,
		OutputFields:       outputFields,
	}); err != nil {
		t.Fatalf("UpdateNode %s outputs: %v", node.Key, err)
	}
}

func createApprovalWorkflow(t *testing.T, ctx context.Context, store *Store) workflow.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Approval Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	workflowID := created.ID
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := nodeByKind(t, def, workflow.NodeKindStart)
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	agentID := workflow.NodeID("node-agent-" + string(workflowID))
	if _, err := store.AddNode(ctx, NodeRecord{ID: agentID, WorkflowID: workflowID, Key: "agent", Kind: workflow.NodeKindAgent, DisplayName: "Agent", SubagentRole: "coder", PromptTemplate: "Do work.", OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}}); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: workflow.TransitionGroupID("group-start-" + string(workflowID)), WorkflowID: workflowID, SourceNodeID: start.ID, TransitionID: "start", DisplayName: "Start"}); err != nil {
		t.Fatalf("AddTransitionGroup start: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: workflow.EdgeID("edge-start-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: workflow.TransitionGroupID("group-start-" + string(workflowID)), Key: "start", TargetNodeID: agentID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Do work."}); err != nil {
		t.Fatalf("AddEdge start: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: workflow.TransitionGroupID("group-done-" + string(workflowID)), WorkflowID: workflowID, SourceNodeID: agentID, TransitionID: "done", DisplayName: "Done"}); err != nil {
		t.Fatalf("AddTransitionGroup done: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: workflow.EdgeID("edge-done-approval-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: workflow.TransitionGroupID("group-done-" + string(workflowID)), Key: "done", TargetNodeID: done.ID, ContextMode: workflow.ContextModeNewSession, RequiresApproval: true}); err != nil {
		t.Fatalf("AddEdge approval done: %v", err)
	}
	return workflowID
}

func createFanoutJoinWorkflow(t *testing.T, ctx context.Context, store *Store) workflow.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Fanout Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	workflowID := created.ID
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := nodeByKind(t, def, workflow.NodeKindStart)
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	planID := workflow.NodeID("node-plan-" + string(workflowID))
	implAID := workflow.NodeID("node-impl-a-" + string(workflowID))
	implBID := workflow.NodeID("node-impl-b-" + string(workflowID))
	joinID := workflow.NodeID("node-join-" + string(workflowID))
	synthID := workflow.NodeID("node-synth-" + string(workflowID))
	joinAEdgeID := workflow.EdgeID("edge-join-a-" + string(workflowID))
	joinBEdgeID := workflow.EdgeID("edge-join-b-" + string(workflowID))
	for _, node := range []NodeRecord{
		{ID: planID, WorkflowID: workflowID, Key: "plan", Kind: workflow.NodeKindAgent, DisplayName: "Plan", SubagentRole: "coder", PromptTemplate: "Plan.", OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}},
		{ID: implAID, WorkflowID: workflowID, Key: "impl_a", Kind: workflow.NodeKindAgent, DisplayName: "Implement A", SubagentRole: "coder", PromptTemplate: "A.", InputFields: []workflow.InputField{{Name: "summary", Description: "Plan summary."}}, OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}},
		{ID: implBID, WorkflowID: workflowID, Key: "impl_b", Kind: workflow.NodeKindAgent, DisplayName: "Implement B", SubagentRole: "coder", PromptTemplate: "B.", InputFields: []workflow.InputField{{Name: "summary", Description: "Plan summary."}}, OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}},
		{ID: joinID, WorkflowID: workflowID, Key: "join", Kind: workflow.NodeKindJoin, DisplayName: "Join", JoinInputProviders: []workflow.JoinInputProvider{{InputName: "joined", ProviderEdgeID: joinAEdgeID}}},
		{ID: synthID, WorkflowID: workflowID, Key: "synth", Kind: workflow.NodeKindAgent, DisplayName: "Synthesize", SubagentRole: "coder", PromptTemplate: "Synthesize {{.Inputs.joined}}.", InputFields: []workflow.InputField{{Name: "joined", Description: "Joined branch summary."}}, OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}},
	} {
		if _, err := store.AddNode(ctx, node); err != nil {
			t.Fatalf("AddNode %s: %v", node.Key, err)
		}
	}
	startGroup := workflow.TransitionGroupID("group-start-" + string(workflowID))
	splitGroup := workflow.TransitionGroupID("group-split-" + string(workflowID))
	joinAGroup := workflow.TransitionGroupID("group-join-a-" + string(workflowID))
	joinBGroup := workflow.TransitionGroupID("group-join-b-" + string(workflowID))
	synthGroup := workflow.TransitionGroupID("group-join-synth-" + string(workflowID))
	doneGroup := workflow.TransitionGroupID("group-synth-done-" + string(workflowID))
	for _, group := range []TransitionGroupRecord{
		{ID: startGroup, WorkflowID: workflowID, SourceNodeID: start.ID, TransitionID: "start", DisplayName: "Start"},
		{ID: splitGroup, WorkflowID: workflowID, SourceNodeID: planID, TransitionID: "split", DisplayName: "Split"},
		{ID: joinAGroup, WorkflowID: workflowID, SourceNodeID: implAID, TransitionID: "join", DisplayName: "Join"},
		{ID: joinBGroup, WorkflowID: workflowID, SourceNodeID: implBID, TransitionID: "join", DisplayName: "Join"},
		{ID: synthGroup, WorkflowID: workflowID, SourceNodeID: joinID, TransitionID: "done", DisplayName: "Done"},
		{ID: doneGroup, WorkflowID: workflowID, SourceNodeID: synthID, TransitionID: "done", DisplayName: "Done"},
	} {
		if _, err := store.AddTransitionGroup(ctx, group); err != nil {
			t.Fatalf("AddTransitionGroup %s: %v", group.TransitionID, err)
		}
	}
	for _, edge := range []EdgeRecord{
		{ID: workflow.EdgeID("edge-start-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: startGroup, Key: "start", TargetNodeID: planID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Plan."},
		{ID: workflow.EdgeID("edge-split-a-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: splitGroup, Key: "split_a", TargetNodeID: implAID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "A {{.Params.summary}}.", Parameters: []workflow.Parameter{{Key: "summary", Description: "Plan summary."}}},
		{ID: workflow.EdgeID("edge-split-b-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: splitGroup, Key: "split_b", TargetNodeID: implBID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "B {{.Params.summary}}.", Parameters: []workflow.Parameter{{Key: "summary", Description: "Plan summary."}}},
		{ID: joinAEdgeID, WorkflowID: workflowID, TransitionGroupID: joinAGroup, Key: "join_a", TargetNodeID: joinID, ContextMode: workflow.ContextModeNewSession, Parameters: []workflow.Parameter{{Key: "joined", Description: "Joined branch summary."}}},
		{ID: joinBEdgeID, WorkflowID: workflowID, TransitionGroupID: joinBGroup, Key: "join_b", TargetNodeID: joinID, ContextMode: workflow.ContextModeNewSession},
		{ID: workflow.EdgeID("edge-join-synth-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: synthGroup, Key: "synth", TargetNodeID: synthID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Synthesize {{.Params.joined}}."},
		{ID: workflow.EdgeID("edge-synth-done-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: doneGroup, Key: "done", TargetNodeID: done.ID, ContextMode: workflow.ContextModeNewSession},
	} {
		if _, err := store.AddEdge(ctx, edge); err != nil {
			t.Fatalf("AddEdge %s: %v", edge.Key, err)
		}
	}
	return workflowID
}

func startFanoutTask(t *testing.T, ctx context.Context, store *Store, projectID string, workflowID workflow.WorkflowID) (TaskRecord, map[workflow.NodeID]workflow.RunID) {
	t.Helper()
	task, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: projectID, WorkflowID: workflowID, Title: "Task", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "split", OutputValues: map[string]string{"summary": "plan"}})
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	branchRunsByNode := map[workflow.NodeID]workflow.RunID{}
	for _, run := range runs {
		if run.ID != started.RunID {
			branchRunsByNode[run.NodeID] = run.ID
		}
	}
	if len(branchRunsByNode) != 2 {
		t.Fatalf("branch runs = %+v, want two branch runs", branchRunsByNode)
	}
	return task, branchRunsByNode
}

func placementParallelIDs(t *testing.T, ctx context.Context, store *Store, placementID workflow.PlacementID) (string, string) {
	t.Helper()
	var batchID sql.NullString
	var branchID sql.NullString
	if err := store.db.QueryRowContext(ctx, `
SELECT parallel_batch_transition_id, parallel_branch_edge_id
FROM task_node_placements
WHERE id = ?`, string(placementID)).Scan(&batchID, &branchID); err != nil {
		t.Fatalf("query placement parallel ids: %v", err)
	}
	return strings.TrimSpace(batchID.String), strings.TrimSpace(branchID.String)
}

func requireApprovalOnWorkflowEdge(t *testing.T, ctx context.Context, store *Store, workflowID workflow.WorkflowID, edgeKey string) {
	t.Helper()
	// Intentional graph fixture helper: toggle requires_approval directly to
	// keep approval-focused tests from reauthoring entire workflow graphs.
	result, err := store.db.ExecContext(ctx, `
UPDATE workflow_edges
SET requires_approval = 1
WHERE edge_key = ?
  AND EXISTS (
      SELECT 1
      FROM workflow_transition_groups tg
      JOIN workflow_nodes source ON source.id = tg.source_node_id
      WHERE tg.id = workflow_edges.transition_group_id
        AND source.workflow_id = ?
  )`, edgeKey, string(workflowID))
	if err != nil {
		t.Fatalf("require approval on edge %s: %v", edgeKey, err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		t.Fatalf("require approval rows for edge %s: %v", edgeKey, err)
	}
	if updated != 1 {
		t.Fatalf("require approval on edge %s updated %d rows", edgeKey, updated)
	}
}

func currentWorkflowRevision(t *testing.T, ctx context.Context, store *Store, workflowID workflow.WorkflowID) int64 {
	t.Helper()
	_, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	return record.Version
}

func hasNode(def workflow.Definition, key string, kind workflow.NodeKind) bool {
	for _, node := range def.Nodes {
		if string(node.Key) == key && node.Kind == kind {
			return true
		}
	}
	return false
}

func nodeByKey(t *testing.T, def workflow.Definition, key string) workflow.Node {
	t.Helper()
	for _, node := range def.Nodes {
		if string(node.Key) == key {
			return node
		}
	}
	t.Fatalf("missing node key %q in %+v", key, def.Nodes)
	return workflow.Node{}
}

func edgeByKey(t *testing.T, def workflow.Definition, key string) workflow.Edge {
	t.Helper()
	for _, edge := range def.Edges {
		if string(edge.Key) == key {
			return edge
		}
	}
	t.Fatalf("missing edge key %q in %+v", key, def.Edges)
	return workflow.Edge{}
}

func runForNode(t *testing.T, ctx context.Context, store *Store, taskID workflow.TaskID, nodeID workflow.NodeID) RunRecord {
	t.Helper()
	runs, err := store.ListRuns(ctx, taskID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	for _, run := range runs {
		if run.NodeID == nodeID {
			return run
		}
	}
	t.Fatalf("run for node %q not found in %+v", nodeID, runs)
	return RunRecord{}
}

func latestRunForNode(t *testing.T, ctx context.Context, store *Store, taskID workflow.TaskID, nodeID workflow.NodeID) RunRecord {
	t.Helper()
	runs, err := store.ListRuns(ctx, taskID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	var latest RunRecord
	for _, run := range runs {
		if run.NodeID == nodeID {
			latest = run
		}
	}
	if latest.ID == "" {
		t.Fatalf("run for node %q not found in %+v", nodeID, runs)
	}
	return latest
}

func insertCompletedRunForNodeAfterTransition(t *testing.T, ctx context.Context, store *Store, taskID workflow.TaskID, nodeID workflow.NodeID, snapshotSourceRunID workflow.RunID, sessionID string, transitionID workflow.TransitionID) workflow.RunID {
	t.Helper()
	var transitionCreatedAt int64
	if err := store.db.QueryRowContext(ctx, `SELECT created_at_unix_ms FROM task_transitions WHERE id = ?`, string(transitionID)).Scan(&transitionCreatedAt); err != nil {
		t.Fatalf("query transition created_at: %v", err)
	}
	var snapshotJSON string
	if err := store.db.QueryRowContext(ctx, `SELECT run_start_snapshot_json FROM task_runs WHERE id = ?`, string(snapshotSourceRunID)).Scan(&snapshotJSON); err != nil {
		t.Fatalf("query source run snapshot: %v", err)
	}
	placementID := prefixedID("placement")
	runID := prefixedID("run")
	completedAt := transitionCreatedAt + 1
	if err := store.queries.InsertTaskNodePlacement(ctx, sqlitegen.InsertTaskNodePlacementParams{ID: placementID, TaskID: string(taskID), NodeID: string(nodeID), State: "completed", CreatedAtUnixMs: completedAt, UpdatedAtUnixMs: completedAt}); err != nil {
		t.Fatalf("InsertTaskNodePlacement competing run: %v", err)
	}
	if err := store.queries.InsertTaskRun(ctx, sqlitegen.InsertTaskRunParams{
		ID:                          runID,
		PlacementID:                 placementID,
		SessionID:                   sql.NullString{String: sessionID, Valid: sessionID != ""},
		RunGeneration:               1,
		WorkflowRevisionSeen:        1,
		AutomationRequestedAtUnixMs: completedAt,
		CreatedAtUnixMs:             completedAt,
		UpdatedAtUnixMs:             completedAt,
		StartedAtUnixMs:             completedAt,
		CompletedAtUnixMs:           completedAt,
		InterruptedAtUnixMs:         0,
		InterruptionDetailJson:      "{}",
		RunStartSnapshotJson:        snapshotJSON,
		MetadataJson:                "{}",
	}); err != nil {
		t.Fatalf("InsertTaskRun competing run: %v", err)
	}
	return workflow.RunID(runID)
}

func insertCompletedRunForNodeInBatch(t *testing.T, ctx context.Context, store *Store, taskID workflow.TaskID, nodeID workflow.NodeID, snapshotSourceRunID workflow.RunID, sessionID string, batchID string, completedAt int64) workflow.RunID {
	t.Helper()
	var snapshotJSON string
	if err := store.db.QueryRowContext(ctx, `SELECT run_start_snapshot_json FROM task_runs WHERE id = ?`, string(snapshotSourceRunID)).Scan(&snapshotJSON); err != nil {
		t.Fatalf("query source run snapshot: %v", err)
	}
	placementID := prefixedID("placement")
	runID := prefixedID("run")
	if err := store.queries.InsertTaskNodePlacement(ctx, sqlitegen.InsertTaskNodePlacementParams{ID: placementID, TaskID: string(taskID), NodeID: string(nodeID), State: "completed", ParallelBatchTransitionID: sql.NullString{String: batchID, Valid: strings.TrimSpace(batchID) != ""}, CreatedAtUnixMs: completedAt, UpdatedAtUnixMs: completedAt}); err != nil {
		t.Fatalf("InsertTaskNodePlacement competing batch run: %v", err)
	}
	if err := store.queries.InsertTaskRun(ctx, sqlitegen.InsertTaskRunParams{
		ID:                          runID,
		PlacementID:                 placementID,
		SessionID:                   sql.NullString{String: sessionID, Valid: sessionID != ""},
		RunGeneration:               1,
		WorkflowRevisionSeen:        1,
		AutomationRequestedAtUnixMs: completedAt,
		CreatedAtUnixMs:             completedAt,
		UpdatedAtUnixMs:             completedAt,
		StartedAtUnixMs:             completedAt,
		CompletedAtUnixMs:           completedAt,
		InterruptedAtUnixMs:         0,
		InterruptionDetailJson:      "{}",
		RunStartSnapshotJson:        snapshotJSON,
		MetadataJson:                "{}",
	}); err != nil {
		t.Fatalf("InsertTaskRun competing batch run: %v", err)
	}
	return workflow.RunID(runID)
}

func assertZeroTaskRows(t *testing.T, store *Store, table string, taskID string) {
	t.Helper()
	queries := map[string]string{
		"task_node_placements": `SELECT COUNT(*) FROM task_node_placements WHERE task_id = ?`,
		"task_transitions":     `SELECT COUNT(*) FROM task_transitions WHERE task_id = ?`,
		"task_comments":        `SELECT COUNT(*) FROM task_comments WHERE task_id = ?`,
	}
	query, ok := queries[table]
	if !ok {
		t.Fatalf("assertZeroTaskRows: unsupported table %q", table)
	}
	var count int
	if err := store.db.QueryRow(query, taskID).Scan(&count); err != nil {
		t.Fatalf("count %s rows for task %s: %v", table, taskID, err)
	}
	if count != 0 {
		t.Fatalf("%s rows for task %s = %d, want 0", table, taskID, count)
	}
}

func taskTransitionIDOtherThan(t *testing.T, ctx context.Context, store *Store, taskID workflow.TaskID, excludedID string) workflow.TransitionID {
	t.Helper()
	var transitionID string
	if err := store.db.QueryRowContext(ctx, `
SELECT id
FROM task_transitions
WHERE task_id = ? AND id != ?
LIMIT 1`, string(taskID), excludedID).Scan(&transitionID); err != nil {
		t.Fatalf("query task transition other than %s: %v", excludedID, err)
	}
	return workflow.TransitionID(transitionID)
}

func nodeByKind(t *testing.T, def workflow.Definition, kind workflow.NodeKind) workflow.Node {
	t.Helper()
	for _, node := range def.Nodes {
		if node.Kind == kind {
			return node
		}
	}
	t.Fatalf("missing node kind %q in %+v", kind, def.Nodes)
	return workflow.Node{}
}
