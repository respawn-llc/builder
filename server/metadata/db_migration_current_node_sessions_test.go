package metadata

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"core/server/workflow"

	_ "modernc.org/sqlite"
)

func TestOpenProjectsLegacyWaitingQuestionInterruptsCurrentNode(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-waiting-question-migration', 'Project', ?, ?, '{}')`, now, now)
	seedLegacyWorkflowSession(
		t,
		db,
		"project-waiting-question-migration",
		"workspace-waiting-question-migration",
		"550e8400-e29b-41d4-a716-446655440003",
		now,
	)
	seedWorkflowGraph(t, db, "project-waiting-question-migration", now)
	execSeed(t, db, "task", workflowSeedTaskSQL, "task-waiting-question-migration", "link-1", 1, "ASK-1", now, now)
	execSeed(t, db, "agent placement", workflowSeedPlacementSQL, "placement-waiting-question-migration", "task-waiting-question-migration", "node-agent", now, now)
	execSeed(t, db, "waiting question run", `
INSERT INTO task_runs (
    id, placement_id, session_id, workflow_revision_seen,
    created_at_unix_ms, updated_at_unix_ms, started_at_unix_ms, waiting_ask_id
) VALUES (
    'run-waiting-question-migration',
    'placement-waiting-question-migration',
    '550e8400-e29b-41d4-a716-446655440003',
    1,
    ?, ?, ?, 'ask-precutover'
)`, now, now+3, now+1)
	seedLegacyExecutableCurrentNodeEnteringEdge(t, db, "task-waiting-question-migration", "placement-waiting-question-migration", now)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var schedulingState, interruptionReason string
	if err := store.db.QueryRowContext(t.Context(), `
SELECT scheduling_state, interruption_reason
FROM task_current_nodes
WHERE task_id = 'task-waiting-question-migration'`).Scan(&schedulingState, &interruptionReason); err != nil {
		t.Fatalf("query migrated waiting-question current node: %v", err)
	}
	if schedulingState != "interrupted" || interruptionReason != "server_restart" {
		t.Fatalf("migrated waiting-question current node = scheduling=%q reason=%q", schedulingState, interruptionReason)
	}

}

func seedLegacyWorkflowSession(t *testing.T, db *sql.DB, projectID, workspaceID, sessionID string, now int64) {
	t.Helper()
	execSeed(t, db, "workspace", `
INSERT INTO workspaces (
    id, project_id, canonical_root_path, git_metadata_json, created_at_unix_ms, updated_at_unix_ms
) VALUES (?, ?, ?, '{}', ?, ?)`, workspaceID, projectID, "/"+workspaceID, now, now)
	execSeed(t, db, "session", `
INSERT INTO sessions (
    id, project_id, workspace_id, artifact_relpath, created_at_unix_ms, updated_at_unix_ms
) VALUES (?, ?, ?, ?, ?, ?)`,
		sessionID,
		projectID,
		workspaceID,
		"sessions/"+sessionID,
		now,
		now,
	)
}

func seedLegacyExecutableCurrentNodeEnteringEdge(t *testing.T, db *sql.DB, taskID, targetPlacementID string, now int64) {
	t.Helper()
	sourcePlacementID := "entry-source-" + targetPlacementID
	transitionID := "entry-transition-" + targetPlacementID
	execSeed(t, db, "legacy entry source placement", `
INSERT INTO task_node_placements (
    id, task_id, node_id, state, created_at_unix_ms, updated_at_unix_ms
) VALUES (?, ?, 'node-start', 'completed', ?, ?)`,
		sourcePlacementID,
		taskID,
		now,
		now,
	)
	execSeed(t, db, "legacy entry transition", `
INSERT INTO task_transitions (
    id, task_id, source_placement_id, source_node_key, source_node_display_name,
    transition_id, transition_display_name, workflow_revision_seen, actor, state,
    commentary, output_values_json, created_at_unix_ms, applied_at_unix_ms
) VALUES (?, ?, ?, 'start', 'Start', 'start', 'Start', 1, 'system', 'applied', '', '{}', ?, ?)`,
		transitionID,
		taskID,
		sourcePlacementID,
		now,
		now,
	)
	execSeed(t, db, "legacy entry transition edge", `
INSERT INTO task_transition_edges (
    id, task_transition_id, workflow_edge_id, edge_key,
    target_node_id, target_node_key, target_node_display_name, target_node_kind,
    target_placement_id, state, context_mode, requires_approval,
    input_bindings_json, output_requirements_json, metadata_json
) VALUES (?, ?, 'edge-start-1', 'start', 'node-agent', 'agent', 'Agent', 'agent', ?, 'applied', 'new_session', 0, '[]', '[]', '{}')`,
		"entry-edge-"+targetPlacementID,
		transitionID,
		targetPlacementID,
	)
}

func TestOpenProjectsRetainsCompletedAgentSessionAssociation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	runUpdatedAt := now + 8
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-completed-session-migration', 'Project', ?, ?, '{}')`, now, now)
	seedLegacyWorkflowSession(
		t,
		db,
		"project-completed-session-migration",
		"workspace-completed-session-migration",
		"550e8400-e29b-41d4-a716-446655440001",
		now,
	)
	seedWorkflowGraph(t, db, "project-completed-session-migration", now)
	execSeed(t, db, "task", workflowSeedTaskSQL, "task-completed-session-migration", "link-1", 1, "SES-1", now, now)
	execSeed(t, db, "terminal placement", workflowSeedPlacementSQL, "placement-completed-session-terminal", "task-completed-session-migration", "node-done", now, now)
	execSeed(t, db, "completed agent placement", `
INSERT INTO task_node_placements (
    id, task_id, node_id, state, created_at_unix_ms, updated_at_unix_ms
) VALUES (
    'placement-completed-session-agent',
    'task-completed-session-migration',
    'node-agent',
    'completed',
    ?,
    ?
)`, now, now+1)
	execSeed(t, db, "completed agent run", `
INSERT INTO task_runs (
    id,
    placement_id,
    session_id,
    workflow_revision_seen,
    created_at_unix_ms,
    updated_at_unix_ms,
    started_at_unix_ms,
    completed_at_unix_ms
) VALUES (
    'run-completed-session-migration',
    'placement-completed-session-agent',
    '550e8400-e29b-41d4-a716-446655440001',
    1,
    ?,
    ?,
    ?,
    ?
)`, now, runUpdatedAt, now+1, now+2)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var taskID, nodeID string
	var associatedAt int64
	if err := store.db.QueryRowContext(t.Context(), `
SELECT session.task_id, association.node_id, association.associated_at_unix_ms
FROM sessions session
JOIN session_workflow_node_associations association ON association.session_id = session.id
WHERE session.id = '550e8400-e29b-41d4-a716-446655440001'`).Scan(&taskID, &nodeID, &associatedAt); err != nil {
		t.Fatalf("query retained completed agent session association: %v", err)
	}
	if taskID != "task-completed-session-migration" || nodeID != "node-agent" || associatedAt != runUpdatedAt {
		t.Fatalf("retained completed agent session association = task=%q node=%q associated_at=%d", taskID, nodeID, associatedAt)
	}

	var historicalCurrentNodeCount int
	if err := store.db.QueryRowContext(t.Context(), `
SELECT COUNT(*)
FROM task_current_nodes
WHERE task_id = 'task-completed-session-migration'
  AND node_id = 'node-agent'`).Scan(&historicalCurrentNodeCount); err != nil {
		t.Fatalf("count projected completed agent current nodes: %v", err)
	}
	if historicalCurrentNodeCount != 0 {
		t.Fatalf("completed agent current node count = %d, want 0", historicalCurrentNodeCount)
	}
}

func TestOpenProjectsPrefersCompletedSerialPendingApprovalSourceOverActiveTerminal(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 59)
	if err != nil {
		t.Fatalf("open version 59 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-pending-approval-migration', 'Project', ?, ?, '{}')`, now, now)
	seedLegacyWorkflowSession(
		t,
		db,
		"project-pending-approval-migration",
		"workspace-pending-approval-migration",
		"550e8400-e29b-41d4-a716-446655440002",
		now,
	)
	seedWorkflowGraph(t, db, "project-pending-approval-migration", now)
	execSeed(t, db, "task", workflowSeedTaskSQL, "task-pending-approval-migration", "link-1", 1, "APR-1", now, now)
	execSeed(t, db, "completed approval source placement", `
INSERT INTO task_node_placements (
    id, task_id, node_id, state, created_at_unix_ms, updated_at_unix_ms
) VALUES (
    'placement-pending-approval-migration',
    'task-pending-approval-migration',
    'node-agent',
    'completed',
    ?,
    ?
)`, now, now+1)
	execSeed(t, db, "completed approval source run", `
INSERT INTO task_runs (
    id, placement_id, session_id, workflow_revision_seen,
    created_at_unix_ms, updated_at_unix_ms, started_at_unix_ms, completed_at_unix_ms
) VALUES (
    'run-pending-approval-migration',
    'placement-pending-approval-migration',
    '550e8400-e29b-41d4-a716-446655440002',
    1,
    ?, ?, ?, ?
)`, now, now+2, now+1, now+2)
	execSeed(t, db, "pending approval transition", `
INSERT INTO task_transitions (
    id, task_id, source_run_id, source_placement_id,
    source_node_key, source_node_display_name, transition_id, transition_display_name, workflow_revision_seen,
    actor, state, commentary, output_values_json, created_at_unix_ms
) VALUES (
    'transition-pending-approval-migration',
    'task-pending-approval-migration',
    'run-pending-approval-migration',
    'placement-pending-approval-migration',
    'agent',
    'Agent',
    'done',
    'Done',
    1,
    'agent',
    'pending_approval',
    '',
    '{"summary":"done"}',
    ?
)`, now+3)
	execSeed(t, db, "pending approval edge", `
INSERT INTO task_transition_edges (
    id, task_transition_id, workflow_edge_id, edge_key,
    target_node_id, target_node_key, target_node_display_name, target_node_kind,
    state, context_mode, requires_approval, input_bindings_json, output_requirements_json, metadata_json
) VALUES (
    'transition-edge-pending-approval-migration',
    'transition-pending-approval-migration',
    'edge-done-1',
    'done',
    'node-done',
    'done',
    'Done',
    'terminal',
    'pending',
    'new_session',
    1,
    '[{"name":"summary","source":"transition_output","field":"summary"}]',
    '[]',
    '{"context_mode":"new_session","context_source":{"kind":"immediate_source"},"context_resolution_frozen":true,"source_session_id":"550e8400-e29b-41d4-a716-446655440002","node_output_values":{"plan":{"summary":"frozen plan"}}}'
)`)
	execSeed(t, db, "conflicting active terminal placement", workflowSeedPlacementSQL,
		"placement-pending-approval-terminal",
		"task-pending-approval-migration",
		"node-done",
		now+4,
		now+4,
	)
	execSeed(t, db, "delete mutable approval graph", `
DELETE FROM workflow_transition_groups
WHERE id = 'group-done'`)
	seedLegacyExecutableCurrentNodeEnteringEdge(t, db, "task-pending-approval-migration", "placement-pending-approval-migration", now)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 59 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var sourceNodeID, sourceSessionID string
	var sourceSchedulingState sql.NullString
	if err := store.db.QueryRowContext(t.Context(), `
SELECT node_id, session_id, scheduling_state
FROM task_current_nodes
WHERE task_id = 'task-pending-approval-migration'`).Scan(
		&sourceNodeID,
		&sourceSessionID,
		&sourceSchedulingState,
	); err != nil {
		t.Fatalf("query pending approval source current node: %v", err)
	}
	if sourceNodeID != "node-agent" ||
		sourceSessionID != "550e8400-e29b-41d4-a716-446655440002" ||
		sourceSchedulingState.Valid {
		t.Fatalf(
			"pending approval source current node = node=%q session=%q scheduling=%+v",
			sourceNodeID,
			sourceSessionID,
			sourceSchedulingState,
		)
	}

	var (
		approvalID, approvalSourceTaskID, approvalSourceNodeID, approvalSourceSessionID, materializedValues string
		workflowVersion, createdAt                                                                          int64
		transitionWorkflowID, transitionGroupID, transitionSourceNodeID, transitionID, sourceDisplayName    string
		branchKey, targetNodeID, targetEnteredByEdgeID, targetDisplayName, targetInputs, targetPriorValues  string
		targetBranchKey                                                                                     sql.NullString
		targetSessionID, targetSchedulingState                                                              string
		edgeID, edgeKey, edgeTargetNodeID, edgeContextMode, edgeContextSourceKind                           string
		edgeRequiresApproval                                                                                int
		resolutionSessionID                                                                                 string
	)
	if err := store.db.QueryRowContext(t.Context(), `
SELECT
    approval.id,
    approval.source_task_id,
    approval.source_node_id,
    approval.source_session_id,
    approval.workflow_version,
    approval.materialized_values_json,
    approval.created_at_unix_ms,
    json_extract(approval.transition_snapshot_json, '$.workflow_id'),
    json_extract(approval.transition_snapshot_json, '$.id'),
    json_extract(approval.transition_snapshot_json, '$.source_node_id'),
    json_extract(approval.transition_snapshot_json, '$.transition_id'),
    json_extract(approval.transition_snapshot_json, '$.source_display_name'),
    branch.transition_branch_key,
    json_extract(branch.target_snapshot_json, '$.node_id'),
    json_extract(branch.target_snapshot_json, '$.transition_branch_key'),
    json_extract(branch.target_snapshot_json, '$.entered_by_edge_id'),
    json_extract(branch.target_snapshot_json, '$.display_name'),
    json_extract(branch.target_snapshot_json, '$.current_input_values'),
    json_extract(branch.target_snapshot_json, '$.prior_node_values'),
    COALESCE(json_extract(branch.target_snapshot_json, '$.session_id'), ''),
    COALESCE(json_extract(branch.target_snapshot_json, '$.scheduling_state'), ''),
    json_extract(branch.effective_edge_configuration_json, '$.id'),
    json_extract(branch.effective_edge_configuration_json, '$.key'),
    json_extract(branch.effective_edge_configuration_json, '$.target_node_id'),
    json_extract(branch.effective_edge_configuration_json, '$.context_mode'),
    json_extract(branch.effective_edge_configuration_json, '$.context_source.kind'),
    json_extract(branch.effective_edge_configuration_json, '$.requires_approval'),
    COALESCE(json_extract(branch.context_source_resolution_json, '$.session_id'), '')
FROM task_pending_approvals approval
JOIN task_pending_approval_branches branch ON branch.approval_id = approval.id
WHERE approval.source_task_id = 'task-pending-approval-migration'`).Scan(
		&approvalID,
		&approvalSourceTaskID,
		&approvalSourceNodeID,
		&approvalSourceSessionID,
		&workflowVersion,
		&materializedValues,
		&createdAt,
		&transitionWorkflowID,
		&transitionGroupID,
		&transitionSourceNodeID,
		&transitionID,
		&sourceDisplayName,
		&branchKey,
		&targetNodeID,
		&targetBranchKey,
		&targetEnteredByEdgeID,
		&targetDisplayName,
		&targetInputs,
		&targetPriorValues,
		&targetSessionID,
		&targetSchedulingState,
		&edgeID,
		&edgeKey,
		&edgeTargetNodeID,
		&edgeContextMode,
		&edgeContextSourceKind,
		&edgeRequiresApproval,
		&resolutionSessionID,
	); err != nil {
		t.Fatalf("query migrated pending approval snapshot: %v", err)
	}
	if _, err := workflow.ParseApprovalID(approvalID); err != nil {
		t.Fatalf("migrated approval id %q: %v", approvalID, err)
	}
	if approvalSourceTaskID != "task-pending-approval-migration" ||
		approvalSourceNodeID != "node-agent" ||
		approvalSourceSessionID != "550e8400-e29b-41d4-a716-446655440002" ||
		workflowVersion != 1 ||
		materializedValues != `{"summary":"done"}` ||
		createdAt != now+3 ||
		transitionWorkflowID != "workflow-1" ||
		transitionGroupID != "transition-pending-approval-migration" ||
		transitionSourceNodeID != "node-agent" ||
		transitionID != "done" ||
		sourceDisplayName != "Agent" ||
		branchKey != "done" ||
		targetNodeID != "node-done" ||
		targetBranchKey.Valid ||
		targetEnteredByEdgeID != "transition-edge-pending-approval-migration" ||
		targetDisplayName != "Done" ||
		targetInputs != `{"summary":"done"}` ||
		targetPriorValues != `{"plan":{"summary":"frozen plan"}}` ||
		targetSessionID != "" ||
		targetSchedulingState != "" ||
		edgeID != "transition-edge-pending-approval-migration" ||
		edgeKey != "done" ||
		edgeTargetNodeID != "node-done" ||
		edgeContextMode != "new_session" ||
		edgeContextSourceKind != "immediate_source" ||
		edgeRequiresApproval != 1 ||
		resolutionSessionID != "" {
		t.Fatalf(
			"migrated pending approval = id=%q source=%q/%q/%q version=%d values=%q created=%d transition=%q/%q/%q/%q/%q branch=%q target=%q/%+v/%q/%q/%q/%q/%q/%q edge=%q/%q/%q/%q/%q/%d resolution=%q",
			approvalID,
			approvalSourceTaskID,
			approvalSourceNodeID,
			approvalSourceSessionID,
			workflowVersion,
			materializedValues,
			createdAt,
			transitionWorkflowID,
			transitionGroupID,
			transitionSourceNodeID,
			transitionID,
			sourceDisplayName,
			branchKey,
			targetNodeID,
			targetBranchKey,
			targetEnteredByEdgeID,
			targetDisplayName,
			targetInputs,
			targetPriorValues,
			targetSessionID,
			targetSchedulingState,
			edgeID,
			edgeKey,
			edgeTargetNodeID,
			edgeContextMode,
			edgeContextSourceKind,
			edgeRequiresApproval,
			resolutionSessionID,
		)
	}
}

func TestOpenRejectsPendingApprovalWithoutCurrentSource(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-orphan-approval-migration', 'Project', ?, ?, '{}')`, now, now)
	seedWorkflowGraph(t, db, "project-orphan-approval-migration", now)
	execSeed(t, db, "task", workflowSeedTaskSQL, "task-orphan-approval-migration", "link-1", 1, "APR-2", now, now)
	execSeed(t, db, "pending approval transition", `
INSERT INTO task_transitions (
    id, task_id, source_node_key, source_node_display_name,
    transition_id, transition_display_name, workflow_revision_seen, actor, state,
    commentary, output_values_json, created_at_unix_ms
) VALUES (
    'transition-orphan-approval-migration',
    'task-orphan-approval-migration',
    'agent',
    'Agent',
    'done',
    'Done',
    1,
    'agent',
    'pending_approval',
    '',
    '{}',
    ?
)`, now)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	_, err = Open(root)
	if err == nil {
		t.Fatal("Open unexpectedly accepted a pending Approval without a Current Node source")
	}
	if !strings.Contains(err.Error(), "task-orphan-approval-migration") ||
		!strings.Contains(err.Error(), "transition-orphan-approval-migration") {
		t.Fatalf("Open error = %v, want Task and transition diagnostic", err)
	}
}
