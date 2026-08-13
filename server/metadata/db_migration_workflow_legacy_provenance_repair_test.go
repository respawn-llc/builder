package metadata

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"core/shared/runtimeids"
)

func TestWorkflowLegacyProvenanceRepairMigrationRepairsV25CurrentNodeDuringFullUpgrade(t *testing.T) {
	t.Parallel()
	const (
		projectID       = "project-v25-provenance-repair"
		taskID          = "task-v25-provenance-repair"
		sourceSessionID = "session-v25-source"
		targetSessionID = "session-v25-target"
		targetNodeID    = "node-v25-target"
		groupID         = "group-v25-repair"
		edgeID          = "edge-v25-repair"
	)
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 80)
	if err != nil {
		t.Fatalf("open version 80 metadata database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now().UTC().UnixMilli()
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES (?, 'v2.5 provenance repair', ?, ?, '{}')`, projectID, now, now)
	seedWorkflowGraph(t, db, projectID, now)
	workflowID := workflowSeedID(t, db, "1")
	execSeed(t, db, "v2.5 target Node", `
INSERT INTO workflow_nodes (
    id, workflow_id, node_key, kind, display_name, subagent_role, completion_mode
) VALUES (?, ?, 'v25_target', 'agent', 'v2.5 target', 'default', 'tool')`,
		targetNodeID,
		workflowID,
	)
	execSeed(t, db, "v2.5 Transition", `
INSERT INTO workflow_transition_groups (id, source_node_id, transition_id, display_name)
VALUES (?, 'node-agent', 'repair', 'Repair')`, groupID)
	execSeed(t, db, "v2.5 Edge", `
INSERT INTO workflow_edges (
    id, transition_group_id, edge_key, target_node_id, context_mode,
    context_source_kind, assignee_selection, thinking_selection
) VALUES (?, ?, 'repair', ?, 'continue_session', 'previous_target_or_new', 'configured', 'configured')`,
		edgeID,
		groupID,
		targetNodeID,
	)
	execSeed(t, db, "Task", workflowSeedTaskSQL, taskID, "link-1", 1, "V25-1", now, now)
	for _, session := range []struct {
		id           string
		nodeID       string
		associatedAt int64
	}{
		{id: sourceSessionID, nodeID: "node-agent", associatedAt: now + 1},
		{id: targetSessionID, nodeID: targetNodeID, associatedAt: now + 2},
	} {
		seedLegacyWorkflowSession(t, db, projectID, "workspace-"+session.id, session.id, now)
		execSeed(t, db, "Session owner", `UPDATE sessions SET task_id = ? WHERE id = ?`, taskID, session.id)
		execSeed(t, db, "v2.5 association", `
INSERT INTO session_workflow_node_associations (
    session_id, node_id, transition_branch_key, associated_at_unix_ms
) VALUES (?, ?, NULL, ?)`,
			session.id,
			session.nodeID,
			session.associatedAt,
		)
	}
	execSeed(t, db, "v2.5 Current Node", `
INSERT INTO task_current_nodes (
    task_id, node_id, transition_branch_key, current_input_values_json,
    prior_node_values_json, session_id, scheduling_state, entered_by_edge_id,
    effective_assignee, assignee_origin
) VALUES (?, ?, NULL, '{}', '{"transition_parameters":{}}', ?, 'ready', ?, 'default', 'configured_fallback')`,
		taskID,
		targetNodeID,
		targetSessionID,
		edgeID,
	)
	provider, err := newMetadataMigrationProvider(db)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	if _, err := provider.UpTo(t.Context(), 85); err != nil {
		t.Fatalf("upgrade v2.5 Workflow state through provenance repair: %v", err)
	}
	var sourceKind, repairedSource string
	var legacyMaterialized int64
	if err := db.QueryRow(`
SELECT continuation_source_kind, continuation_source_session_id, legacy_materialized
FROM task_current_nodes
WHERE task_id = ?`, taskID).Scan(&sourceKind, &repairedSource, &legacyMaterialized); err != nil {
		t.Fatalf("read fully upgraded Current Node: %v", err)
	}
	if sourceKind != "exact" || repairedSource != sourceSessionID || legacyMaterialized != 0 {
		t.Fatalf(
			"fully upgraded Current Node = kind %q source %q legacy %d; want exact v2.5 source",
			sourceKind,
			repairedSource,
			legacyMaterialized,
		)
	}
}

func TestWorkflowLegacyProvenanceRepairMigrationInfersUniqueLatestEnteringSource(t *testing.T) {
	t.Parallel()
	fixture := seedWorkflowLegacyProvenanceRepairFixture(t, []legacyProvenanceSource{
		{sessionID: "session-source-older", associatedAtOffset: 1},
		{sessionID: "session-source-latest", associatedAtOffset: 2},
	}, 3)

	fixture.migrate(t)

	var sourceKind, sourceSessionID string
	var legacyMaterialized int64
	if err := fixture.db.QueryRow(`
SELECT continuation_source_kind, continuation_source_session_id, legacy_materialized
FROM task_current_nodes
WHERE task_id = ?`, fixture.taskID).Scan(
		&sourceKind,
		&sourceSessionID,
		&legacyMaterialized,
	); err != nil {
		t.Fatalf("read repaired Current Node: %v", err)
	}
	if sourceKind != "exact" || sourceSessionID != "session-source-latest" || legacyMaterialized != 0 {
		t.Fatalf(
			"repaired Current Node = kind %q Session %q legacy %d; want exact latest source",
			sourceKind,
			sourceSessionID,
			legacyMaterialized,
		)
	}
	var status, associationSource string
	if err := fixture.db.QueryRow(`
SELECT association_status, source_session_id
FROM session_workflow_node_associations
WHERE task_id = ? AND session_id = ? AND node_id = ?`,
		fixture.taskID,
		fixture.targetSessionID,
		fixture.targetNodeID,
	).Scan(&status, &associationSource); err != nil {
		t.Fatalf("read repaired retained association: %v", err)
	}
	if status != "current" || associationSource != "session-source-latest" {
		t.Fatalf(
			"repaired retained association = status %q source %q; want current latest source",
			status,
			associationSource,
		)
	}
}

func TestWorkflowLegacyProvenanceRepairMigrationLeavesTimestampTieUnresolved(t *testing.T) {
	t.Parallel()
	fixture := seedWorkflowLegacyProvenanceRepairFixture(t, []legacyProvenanceSource{
		{sessionID: "session-source-a", associatedAtOffset: 2},
		{sessionID: "session-source-b", associatedAtOffset: 2},
	}, 3)

	fixture.migrate(t)

	var sourceKind, sourceSessionID sql.NullString
	var legacyMaterialized int64
	if err := fixture.db.QueryRow(`
SELECT continuation_source_kind, continuation_source_session_id, legacy_materialized
FROM task_current_nodes
WHERE task_id = ?`, fixture.taskID).Scan(
		&sourceKind,
		&sourceSessionID,
		&legacyMaterialized,
	); err != nil {
		t.Fatalf("read unresolved Current Node: %v", err)
	}
	if sourceKind.Valid || sourceSessionID.Valid || legacyMaterialized != 1 {
		t.Fatalf(
			"tied Current Node = kind %v Session %v legacy %d; want unresolved legacy",
			sourceKind,
			sourceSessionID,
			legacyMaterialized,
		)
	}
	var status string
	var associationSource sql.NullString
	if err := fixture.db.QueryRow(`
SELECT association_status, source_session_id
FROM session_workflow_node_associations
WHERE task_id = ? AND session_id = ? AND node_id = ?`,
		fixture.taskID,
		fixture.targetSessionID,
		fixture.targetNodeID,
	).Scan(&status, &associationSource); err != nil {
		t.Fatalf("read unresolved retained association: %v", err)
	}
	if status != "historical" || associationSource.Valid {
		t.Fatalf(
			"tied retained association = status %q source %v; want unchanged historical",
			status,
			associationSource,
		)
	}
}

func TestWorkflowLegacyProvenanceRepairMigrationRejectsSourceAfterRetainedTarget(t *testing.T) {
	t.Parallel()
	fixture := seedWorkflowLegacyProvenanceRepairFixture(t, []legacyProvenanceSource{
		{sessionID: "session-source-future", associatedAtOffset: 4},
	}, 3)

	fixture.migrate(t)

	var legacyMaterialized int64
	if err := fixture.db.QueryRow(`
SELECT legacy_materialized
FROM task_current_nodes
WHERE task_id = ?`, fixture.taskID).Scan(&legacyMaterialized); err != nil {
		t.Fatalf("read future-source Current Node: %v", err)
	}
	if legacyMaterialized != 1 {
		t.Fatalf("future-source Current Node legacy = %d, want unresolved legacy", legacyMaterialized)
	}
}

func TestWorkflowLegacyProvenanceRepairMigrationUsesBoundAgentSessionForNewSessionEntry(t *testing.T) {
	t.Parallel()
	fixture := seedWorkflowLegacyProvenanceRepairFixture(t, []legacyProvenanceSource{
		{sessionID: "session-new-entry-source", associatedAtOffset: 1},
	}, 2)
	execSeed(t, fixture.db, "mark entering Edge new Session", `
UPDATE workflow_edges
SET context_mode = 'new_session',
    context_source_kind = 'immediate_source'
WHERE id = (
    SELECT entered_by_edge_id
    FROM task_current_nodes
    WHERE task_id = ?
)`, fixture.taskID)

	fixture.migrate(t)

	var sourceKind, sourceSessionID string
	var legacyMaterialized int64
	if err := fixture.db.QueryRow(`
SELECT continuation_source_kind, continuation_source_session_id, legacy_materialized
FROM task_current_nodes
WHERE task_id = ?`, fixture.taskID).Scan(
		&sourceKind,
		&sourceSessionID,
		&legacyMaterialized,
	); err != nil {
		t.Fatalf("read repaired new-session Current Node: %v", err)
	}
	if sourceKind != "exact" ||
		sourceSessionID != fixture.targetSessionID ||
		legacyMaterialized != 0 {
		t.Fatalf(
			"new-session Current Node = kind %q Session %q legacy %d; want exact self Session %q",
			sourceKind,
			sourceSessionID,
			legacyMaterialized,
			fixture.targetSessionID,
		)
	}
}

func TestWorkflowLegacyProvenanceRepairMigrationRepairsOnlyFanoutBranchesWithBranchLocalProof(t *testing.T) {
	t.Parallel()
	fixture := seedWorkflowLegacyProvenanceRepairFixture(t, []legacyProvenanceSource{
		{sessionID: "session-fanout-source", associatedAtOffset: 1},
	}, 2)
	execSeed(t, fixture.db, "remove serial repair Current Node", `
DELETE FROM task_current_nodes WHERE task_id = ?`, fixture.taskID)
	execSeed(t, fixture.db, "scope retained association to fanout branch", `
UPDATE session_workflow_node_associations
SET transition_branch_key = 'branch-a'
WHERE task_id = ? AND session_id = ? AND node_id = ?`,
		fixture.taskID,
		fixture.targetSessionID,
		fixture.targetNodeID,
	)
	execSeed(t, fixture.db, "fanout sibling Edge", `
INSERT INTO workflow_edges (
    id, transition_group_id, edge_key, target_node_id, context_mode, context_source_kind,
    assignee_selection, thinking_selection
)
SELECT ?, edge.transition_group_id, 'repair-b', ?, 'new_session', 'immediate_source',
       'configured', 'configured'
FROM workflow_edges edge
WHERE edge.target_node_id = ? AND edge.edge_key = 'repair'`,
		legacyProvenanceGraphID(t),
		workflowGraphSeedID(t, fixture.db, "node-done"),
		fixture.targetNodeID,
	)
	deferredEdgeID := legacyProvenanceGraphID(t)
	execSeed(t, fixture.db, "deferred-self Fan-Out Edge", `
INSERT INTO workflow_edges (
    id, transition_group_id, edge_key, target_node_id, context_mode, context_source_kind,
    assignee_selection, thinking_selection
)
SELECT ?, edge.transition_group_id, 'repair-c', ?, 'new_session', 'immediate_source',
       'configured', 'configured'
FROM workflow_edges edge
WHERE edge.target_node_id = ? AND edge.edge_key = 'repair'`,
		deferredEdgeID,
		fixture.targetNodeID,
		fixture.targetNodeID,
	)
	execSeed(t, fixture.db, "active Fan-Out", `
INSERT INTO task_active_fanouts (task_id) VALUES (?)`, fixture.taskID)
	execSeed(t, fixture.db, "active Fan-Out branches", `
INSERT INTO task_active_fanout_branches (
    task_id, transition_branch_key, arrival_state, arrival_values_json,
    continuation_source_kind, continuation_source_session_id, legacy_materialized
) VALUES
    (?, 'branch-a', 'pending', NULL, NULL, NULL, 1),
    (?, 'branch-b', 'arrived', '{}', NULL, NULL, 1),
    (?, 'branch-c', 'pending', NULL, NULL, NULL, 1)`,
		fixture.taskID,
		fixture.taskID,
		fixture.taskID,
	)
	execSeed(t, fixture.db, "legacy branch Current Node", `
INSERT INTO task_current_nodes (
    task_id, node_id, transition_branch_key, current_input_values_json,
    prior_node_values_json, session_id, scheduling_state, entered_by_edge_id,
    effective_assignee, assignee_origin, continuation_source_kind,
    continuation_source_session_id, legacy_materialized
) SELECT
    ?, ?, 'branch-a', '{}', '{"transition_parameters":{}}', ?, 'ready',
    edge.id, 'default', 'configured_fallback', NULL, NULL, 1
FROM workflow_edges edge
WHERE edge.target_node_id = ? AND edge.edge_key = 'repair'`,
		fixture.taskID,
		fixture.targetNodeID,
		fixture.targetSessionID,
		fixture.targetNodeID,
	)
	execSeed(t, fixture.db, "deferred-self branch Current Node", `
INSERT INTO task_current_nodes (
    task_id, node_id, transition_branch_key, current_input_values_json,
    prior_node_values_json, session_id, scheduling_state, entered_by_edge_id,
    effective_assignee, assignee_origin, continuation_source_kind,
    continuation_source_session_id, legacy_materialized
) VALUES (
    ?, ?, 'branch-c', '{}', '{"transition_parameters":{}}', NULL, 'ready', ?,
    'default', 'configured_fallback', 'deferred_self', NULL, 0
)`,
		fixture.taskID,
		fixture.targetNodeID,
		deferredEdgeID,
	)

	fixture.migrate(t)

	rows, err := fixture.db.Query(`
SELECT transition_branch_key, continuation_source_kind, continuation_source_session_id, legacy_materialized
FROM task_active_fanout_branches
WHERE task_id = ?
ORDER BY transition_branch_key`, fixture.taskID)
	if err != nil {
		t.Fatalf("read repaired Fan-Out branches: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var count int
	for rows.Next() {
		var branchKey string
		var sourceKind, sourceSessionID sql.NullString
		var legacyMaterialized int64
		if err := rows.Scan(&branchKey, &sourceKind, &sourceSessionID, &legacyMaterialized); err != nil {
			t.Fatalf("scan repaired Fan-Out branch: %v", err)
		}
		switch branchKey {
		case "branch-a":
			if !sourceKind.Valid || sourceKind.String != "exact" ||
				!sourceSessionID.Valid || sourceSessionID.String != "session-fanout-source" ||
				legacyMaterialized != 0 {
				t.Fatalf(
					"repaired Fan-Out branch %q = kind %v Session %v legacy %d; want branch-local proof",
					branchKey,
					sourceKind,
					sourceSessionID,
					legacyMaterialized,
				)
			}
		case "branch-b":
			if sourceKind.Valid || sourceSessionID.Valid || legacyMaterialized != 1 {
				t.Fatalf(
					"unproved Fan-Out branch %q = kind %v Session %v legacy %d; want unresolved legacy",
					branchKey,
					sourceKind,
					sourceSessionID,
					legacyMaterialized,
				)
			}
		case "branch-c":
			if !sourceKind.Valid || sourceKind.String != "deferred_self" ||
				sourceSessionID.Valid || legacyMaterialized != 0 {
				t.Fatalf(
					"deferred-self Fan-Out branch %q = kind %v Session %v legacy %d; want deferred self",
					branchKey,
					sourceKind,
					sourceSessionID,
					legacyMaterialized,
				)
			}
		default:
			t.Fatalf("unexpected Fan-Out branch %q", branchKey)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate repaired Fan-Out branches: %v", err)
	}
	if count != 3 {
		t.Fatalf("Fan-Out branch count = %d, want 3", count)
	}
}

func TestWorkflowLegacyProvenanceRepairMigrationRepairsPendingApprovalFromFrozenSourceSession(t *testing.T) {
	t.Parallel()
	const (
		projectID  = "project-legacy-approval-repair"
		taskID     = "task-legacy-approval-repair"
		sessionID  = "session-legacy-approval-source"
		approvalID = "approval-legacy-source"
	)
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 84)
	if err != nil {
		t.Fatalf("open version 84 metadata database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now().UTC().UnixMilli()
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES (?, 'Legacy Approval repair', ?, ?, '{}')`, projectID, now, now)
	seedWorkflowGraph(t, db, projectID, now)
	execSeed(t, db, "Task", workflowSeedTaskSQL, taskID, "link-1", 1, "APPROVAL-REPAIR-1", now, now)
	seedLegacyWorkflowSession(t, db, projectID, "workspace-legacy-approval-source", sessionID, now)
	execSeed(t, db, "source Session owner", `UPDATE sessions SET task_id = ? WHERE id = ?`, taskID, sessionID)
	sourceNodeID := workflowGraphSeedID(t, db, "node-agent")
	execSeed(t, db, "source association", `
INSERT INTO session_workflow_node_associations (
    task_id, session_id, node_id, transition_branch_key,
    association_status, source_session_id, associated_at_unix_ms
) VALUES (?, ?, ?, NULL, 'current', ?, ?)`,
		taskID,
		sessionID,
		sourceNodeID,
		sessionID,
		now,
	)
	execSeed(t, db, "source Current Node", `
INSERT INTO task_current_nodes (
    task_id, node_id, transition_branch_key, current_input_values_json,
    prior_node_values_json, session_id, scheduling_state, entered_by_edge_id,
    effective_assignee, assignee_origin, continuation_source_kind,
    continuation_source_session_id, legacy_materialized
) VALUES (?, ?, NULL, '{}', '{"transition_parameters":{}}', ?, 'ready', ?,
          'default', 'configured_fallback', 'exact', ?, 0)`,
		taskID,
		sourceNodeID,
		sessionID,
		workflowGraphSeedID(t, db, "edge-start-1"),
		sessionID,
	)
	execSeed(t, db, "pending Approval", `
INSERT INTO task_pending_approvals (
    id, source_task_id, source_node_id, source_transition_branch_key,
    source_session_id, workflow_version, transition_snapshot_json,
    materialized_values_json, created_at_unix_ms
) VALUES (?, ?, ?, NULL, ?, 1, '{}', '{}', ?)`,
		approvalID,
		taskID,
		sourceNodeID,
		sessionID,
		now,
	)
	execSeed(t, db, "pending Approval branch", `
INSERT INTO task_pending_approval_branches (
    approval_id, transition_branch_key, target_snapshot_json,
    effective_edge_configuration_json, context_source_resolution_json
) VALUES (
    ?,
    'target',
    json_object(
        'node_id', 'node-agent',
        'display_name', 'Agent',
        'current_input_values', json('{}'),
        'prior_values', json('{"transition_parameters":{}}'),
        'session_id', ?
    ),
    json_object(
        'id', 'edge-done-1',
        'transition_group_id', 'group-done',
        'target_node_id', 'node-agent',
        'context_mode', 'continue_session'
    ),
    json_object(
        'target_session', json_object('kind', 'create'),
        'active_source', json_object('kind', 'legacy')
    )
)`, approvalID, sessionID)

	provider, err := newMetadataMigrationProvider(db)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	if _, err := provider.UpTo(t.Context(), 85); err != nil {
		t.Fatalf("apply Workflow legacy Approval repair migration: %v", err)
	}
	var sourceKind, repairedSessionID string
	if err := db.QueryRow(`
SELECT
    json_extract(context_source_resolution_json, '$.active_source.kind'),
    json_extract(context_source_resolution_json, '$.active_source.session_id')
FROM task_pending_approval_branches
WHERE approval_id = ?`, approvalID).Scan(&sourceKind, &repairedSessionID); err != nil {
		t.Fatalf("read repaired pending Approval: %v", err)
	}
	if sourceKind != "exact" || repairedSessionID != sessionID {
		t.Fatalf(
			"repaired pending Approval source = kind %q Session %q; want frozen exact source",
			sourceKind,
			repairedSessionID,
		)
	}
}

func TestWorkflowLegacyProvenanceRepairMigrationUsesSelectedNodeAssociationInsteadOfApprovalHeaderOrTarget(t *testing.T) {
	t.Parallel()
	fixture := seedWorkflowLegacyProvenanceRepairFixture(t, []legacyProvenanceSource{
		{sessionID: "session-approval-header-only", associatedAtOffset: 1},
	}, 2)
	execSeed(t, fixture.db, "use retained target as Approval source", `
UPDATE task_current_nodes
SET continuation_source_kind = 'exact',
    continuation_source_session_id = 'session-approval-header-only',
    legacy_materialized = 0
WHERE task_id = ?`, fixture.taskID)
	execSeed(t, fixture.db, "pending Approval", `
INSERT INTO task_pending_approvals (
    id, source_task_id, source_node_id, source_transition_branch_key,
    source_session_id, workflow_version, transition_snapshot_json,
    materialized_values_json, created_at_unix_ms
) VALUES (
    'approval-header-only', ?, ?, NULL,
    ?, 1, '{}', '{}', ?
)`,
		fixture.taskID,
		fixture.targetNodeID,
		fixture.targetSessionID,
		time.Now().UTC().UnixMilli(),
	)
	execSeed(t, fixture.db, "pending Approval branch", `
INSERT INTO task_pending_approval_branches (
    approval_id, transition_branch_key, target_snapshot_json,
    effective_edge_configuration_json, context_source_resolution_json
) VALUES (
    'approval-header-only',
    'target',
    json_object(
        'node_id', 'node-agent',
        'display_name', 'Agent',
        'current_input_values', json('{}'),
        'prior_values', json('{"transition_parameters":{}}'),
        'session_id', ?
    ),
    json_object(
        'id', 'edge-done-1',
        'transition_group_id', 'group-done',
        'target_node_id', 'node-agent',
        'context_mode', 'continue_session',
        'context_source', json_object('kind', 'selected_node', 'node_key', 'agent')
    ),
    json_object(
        'target_session', json_object('kind', 'create'),
        'active_source', json_object('kind', 'legacy')
    )
)`, fixture.targetSessionID)

	fixture.migrate(t)

	var sourceKind, sourceSessionID string
	if err := fixture.db.QueryRow(`
SELECT
    json_extract(context_source_resolution_json, '$.active_source.kind'),
    json_extract(context_source_resolution_json, '$.active_source.session_id')
FROM task_pending_approval_branches
WHERE approval_id = 'approval-header-only'`).Scan(&sourceKind, &sourceSessionID); err != nil {
		t.Fatalf("read selected-node pending Approval: %v", err)
	}
	if sourceKind != "exact" || sourceSessionID != "session-approval-header-only" {
		t.Fatalf(
			"selected-node pending Approval source = kind %q Session %q; want selected-node Session",
			sourceKind,
			sourceSessionID,
		)
	}
}

func TestWorkflowLegacyProvenanceRepairMigrationUsesRepairedSourceForRetainedTargetApproval(t *testing.T) {
	t.Parallel()
	fixture := seedWorkflowLegacyProvenanceRepairFixture(t, []legacyProvenanceSource{
		{sessionID: "session-retained-approval-source", associatedAtOffset: 1},
	}, 2)
	execSeed(t, fixture.db, "retained-source pending Approval", `
INSERT INTO task_pending_approvals (
    id, source_task_id, source_node_id, source_transition_branch_key,
    source_session_id, workflow_version, transition_snapshot_json,
    materialized_values_json, created_at_unix_ms
) VALUES (
    'approval-retained-source', ?, ?, NULL,
    ?, 1, '{}', '{}', ?
)`,
		fixture.taskID,
		fixture.targetNodeID,
		fixture.targetSessionID,
		time.Now().UTC().UnixMilli(),
	)
	execSeed(t, fixture.db, "retained-source pending Approval branch", `
INSERT INTO task_pending_approval_branches (
    approval_id, transition_branch_key, target_snapshot_json,
    effective_edge_configuration_json, context_source_resolution_json
) VALUES (
    'approval-retained-source',
    'target',
    json_object(
        'node_id', 'node-agent',
        'display_name', 'Agent',
        'current_input_values', json('{}'),
        'prior_values', json('{"transition_parameters":{}}'),
        'session_id', ?
    ),
    json_object(
        'id', 'edge-done-1',
        'transition_group_id', 'group-done',
        'target_node_id', 'node-agent',
        'context_mode', 'continue_session',
        'context_source', json_object('kind', 'previous_target')
    ),
    json_object(
        'target_session', json_object('kind', 'reuse', 'session_id', ?),
        'active_source', json_object('kind', 'legacy')
    )
)`,
		fixture.targetSessionID,
		fixture.targetSessionID,
	)

	fixture.migrate(t)

	var sourceKind, sourceSessionID string
	if err := fixture.db.QueryRow(`
SELECT
    json_extract(context_source_resolution_json, '$.active_source.kind'),
    json_extract(context_source_resolution_json, '$.active_source.session_id')
FROM task_pending_approval_branches
WHERE approval_id = 'approval-retained-source'`).Scan(&sourceKind, &sourceSessionID); err != nil {
		t.Fatalf("read retained-source pending Approval: %v", err)
	}
	if sourceKind != "exact" || sourceSessionID != "session-retained-approval-source" {
		t.Fatalf(
			"retained-source pending Approval = kind %q Session %q; want repaired source Session",
			sourceKind,
			sourceSessionID,
		)
	}
}

type legacyProvenanceSource struct {
	sessionID          string
	associatedAtOffset int64
}

type legacyProvenanceRepairFixture struct {
	db              *sql.DB
	taskID          string
	targetNodeID    []byte
	targetSessionID string
}

func seedWorkflowLegacyProvenanceRepairFixture(
	t *testing.T,
	sources []legacyProvenanceSource,
	targetAssociatedAtOffset int64,
) legacyProvenanceRepairFixture {
	t.Helper()
	const (
		projectID       = "project-legacy-provenance-repair"
		taskID          = "task-legacy-provenance-repair"
		targetSessionID = "session-target"
	)
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 84)
	if err != nil {
		t.Fatalf("open version 84 metadata database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now().UTC().UnixMilli()
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES (?, 'Legacy provenance repair', ?, ?, '{}')`, projectID, now, now)
	seedWorkflowGraph(t, db, projectID, now)
	workflowID := workflowSeedID(t, db, "1")
	targetNodeID := legacyProvenanceGraphID(t)
	groupID := legacyProvenanceGraphID(t)
	edgeID := legacyProvenanceGraphID(t)
	sourceNodeID := workflowGraphSeedID(t, db, "node-agent")
	execSeed(t, db, "repair target Node", `
INSERT INTO workflow_nodes (id, workflow_id, node_key, kind, display_name, subagent_role, completion_mode)
VALUES (?, ?, 'repair_target', 'agent', 'Repair target', 'default', 'tool')`,
		targetNodeID,
		workflowID,
	)
	execSeed(t, db, "repair Transition", `
INSERT INTO workflow_transition_groups (id, source_node_id, transition_id, display_name)
VALUES (?, ?, 'repair', 'Repair')`,
		groupID,
		sourceNodeID,
	)
	execSeed(t, db, "repair Edge", `
INSERT INTO workflow_edges (
    id, transition_group_id, edge_key, target_node_id, context_mode, context_source_kind,
    assignee_selection, thinking_selection
) VALUES (?, ?, 'repair', ?, 'continue_session', 'previous_target_or_new', 'configured', 'configured')`,
		edgeID,
		groupID,
		targetNodeID,
	)
	execSeed(t, db, "Task", workflowSeedTaskSQL, taskID, "link-1", 1, "REPAIR-1", now, now)
	for _, source := range sources {
		seedLegacyWorkflowSession(t, db, projectID, "workspace-"+source.sessionID, source.sessionID, now)
		execSeed(t, db, "source Session owner", `UPDATE sessions SET task_id = ? WHERE id = ?`, taskID, source.sessionID)
		execSeed(t, db, "source association", `
INSERT INTO session_workflow_node_associations (
    task_id, session_id, node_id, transition_branch_key,
    association_status, source_session_id, associated_at_unix_ms
) VALUES (?, ?, ?, NULL, 'historical', NULL, ?)`,
			taskID,
			source.sessionID,
			sourceNodeID,
			now+source.associatedAtOffset,
		)
	}
	seedLegacyWorkflowSession(t, db, projectID, "workspace-target", targetSessionID, now)
	execSeed(t, db, "target Session owner", `UPDATE sessions SET task_id = ? WHERE id = ?`, taskID, targetSessionID)
	execSeed(t, db, "target association", `
INSERT INTO session_workflow_node_associations (
    task_id, session_id, node_id, transition_branch_key,
    association_status, source_session_id, associated_at_unix_ms
) VALUES (?, ?, ?, NULL, 'historical', NULL, ?)`,
		taskID,
		targetSessionID,
		targetNodeID,
		now+targetAssociatedAtOffset,
	)
	execSeed(t, db, "legacy Current Node", `
INSERT INTO task_current_nodes (
    task_id, node_id, transition_branch_key, current_input_values_json,
    prior_node_values_json, session_id, scheduling_state, entered_by_edge_id,
    effective_assignee, assignee_origin, continuation_source_kind,
    continuation_source_session_id, legacy_materialized
) VALUES (?, ?, NULL, '{}', '{"transition_parameters":{}}', ?, 'ready', ?, 'default', 'configured_fallback', NULL, NULL, 1)`,
		taskID,
		targetNodeID,
		targetSessionID,
		edgeID,
	)
	return legacyProvenanceRepairFixture{
		db:              db,
		taskID:          taskID,
		targetNodeID:    targetNodeID,
		targetSessionID: targetSessionID,
	}
}

func (f legacyProvenanceRepairFixture) migrate(t *testing.T) {
	t.Helper()
	provider, err := newMetadataMigrationProvider(f.db)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	if _, err := provider.UpTo(t.Context(), 85); err != nil {
		t.Fatalf("apply Workflow legacy provenance repair migration: %v", err)
	}
}

func legacyProvenanceGraphID(t *testing.T) []byte {
	t.Helper()
	raw, err := runtimeids.GraphEntityIDBlob(runtimeids.NewGraphEntityID())
	if err != nil {
		t.Fatalf("encode Workflow graph fixture identity: %v", err)
	}
	return raw
}
