package metadata

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestOpenProjectsLegacyActiveScriptRunToInterruptedCurrentNode(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	runUpdatedAt := now + 9
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-active-script-migration', 'Project', ?, ?, '{}')`, now, now)
	seedLegacyWorkflowSession(
		t,
		db,
		"project-active-script-migration",
		"workspace-active-script-migration",
		"550e8400-e29b-41d4-a716-446655440006",
		now,
	)
	seedWorkflowGraph(t, db, "project-active-script-migration", now)
	execSeed(t, db, "script node", `
UPDATE workflow_nodes
SET kind = 'script', script_path = 'scripts/migrate'
WHERE id = 'node-agent'`)
	execSeed(t, db, "task", workflowSeedTaskSQL, "task-active-script-migration", "link-1", 1, "SCR-1", now, now)
	execSeed(t, db, "script placement", workflowSeedPlacementSQL, "placement-active-script-migration", "task-active-script-migration", "node-agent", now, now)
	execSeed(t, db, "active script run", `
INSERT INTO task_runs (
    id,
    placement_id,
    session_id,
    workflow_revision_seen,
    created_at_unix_ms,
    updated_at_unix_ms
) VALUES (
    'run-active-script-migration',
    'placement-active-script-migration',
    '550e8400-e29b-41d4-a716-446655440006',
    1,
    ?,
    ?
)`, now, runUpdatedAt)
	seedLegacyExecutableCurrentNodeEnteringEdge(t, db, "task-active-script-migration", "placement-active-script-migration", now)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var nodeID, schedulingState, interruptionReason, interruptionDetail string
	var interruptedAt int64
	var sessionID sql.NullString
	if err := store.db.QueryRowContext(t.Context(), `
SELECT
    node_id,
    scheduling_state,
    interruption_reason,
    interruption_detail_json,
    interrupted_at_unix_ms,
    session_id
FROM task_current_nodes
WHERE task_id = 'task-active-script-migration'`).Scan(
		&nodeID,
		&schedulingState,
		&interruptionReason,
		&interruptionDetail,
		&interruptedAt,
		&sessionID,
	); err != nil {
		t.Fatalf("query projected active script current node: %v", err)
	}
	if nodeID != "node-agent" ||
		schedulingState != "interrupted" ||
		interruptionReason != "server_restart" ||
		interruptionDetail != `{"code":"workflow.execution.restarted","fields":{"operation":"recovery"}}` ||
		interruptedAt != runUpdatedAt ||
		sessionID.Valid {
		t.Fatalf(
			"projected active script current node = node=%q scheduling=%q reason=%q detail=%q interrupted_at=%d session=%+v",
			nodeID,
			schedulingState,
			interruptionReason,
			interruptionDetail,
			interruptedAt,
			sessionID,
		)
	}

	var taskID sql.NullString
	if err := store.db.QueryRowContext(t.Context(), `
SELECT task_id
FROM sessions
WHERE id = '550e8400-e29b-41d4-a716-446655440006'`).Scan(&taskID); err != nil {
		t.Fatalf("query projected active script Session owner: %v", err)
	}
	if taskID.Valid {
		t.Fatalf("projected active script Session owner = %q, want workflow-neutral", taskID.String)
	}
	var associationCount int
	if err := store.db.QueryRowContext(t.Context(), `
SELECT COUNT(*)
FROM session_workflow_node_associations
WHERE session_id = '550e8400-e29b-41d4-a716-446655440006'`).Scan(&associationCount); err != nil {
		t.Fatalf("count projected active script Session associations: %v", err)
	}
	if associationCount != 0 {
		t.Fatalf("projected active script Session association count = %d, want 0", associationCount)
	}
}

func TestOpenProjectsPreservesLegacyInterruptedAgentRun(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	interruptedAt := now + 5
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-interrupted-agent-migration', 'Project', ?, ?, '{}')`, now, now)
	seedWorkflowGraph(t, db, "project-interrupted-agent-migration", now)
	execSeed(t, db, "task", workflowSeedTaskSQL, "task-interrupted-agent-migration", "link-1", 1, "INT-1", now, now)
	execSeed(t, db, "agent placement", workflowSeedPlacementSQL, "placement-interrupted-agent-migration", "task-interrupted-agent-migration", "node-agent", now, now)
	execSeed(t, db, "interrupted agent run", `
INSERT INTO task_runs (
    id,
    placement_id,
    workflow_revision_seen,
    created_at_unix_ms,
    updated_at_unix_ms,
    started_at_unix_ms,
    interrupted_at_unix_ms,
    interruption_reason,
    interruption_detail_json
) VALUES (
    'run-interrupted-agent-migration',
    'placement-interrupted-agent-migration',
    1,
    ?,
    ?,
    ?,
    ?,
    'user_interrupt',
    '{"code":"workflow.execution.interrupted","fields":{"operation":"interrupt"}}'
)`, now, now+9, now+1, interruptedAt)
	seedLegacyExecutableCurrentNodeEnteringEdge(t, db, "task-interrupted-agent-migration", "placement-interrupted-agent-migration", now)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var schedulingState, interruptionReason, interruptionDetail string
	var projectedInterruptedAt int64
	if err := store.db.QueryRowContext(t.Context(), `
SELECT scheduling_state, interruption_reason, interruption_detail_json, interrupted_at_unix_ms
FROM task_current_nodes
WHERE task_id = 'task-interrupted-agent-migration'`).Scan(
		&schedulingState,
		&interruptionReason,
		&interruptionDetail,
		&projectedInterruptedAt,
	); err != nil {
		t.Fatalf("query projected interrupted agent current node: %v", err)
	}
	if schedulingState != "interrupted" ||
		interruptionReason != "user_interrupt" ||
		interruptionDetail != `{"code":"workflow.execution.interrupted","fields":{"operation":"interrupt"}}` ||
		projectedInterruptedAt != interruptedAt {
		t.Fatalf(
			"projected interrupted agent current node = scheduling=%q reason=%q detail=%q interrupted_at=%d",
			schedulingState,
			interruptionReason,
			interruptionDetail,
			projectedInterruptedAt,
		)
	}
}

func TestOpenProjectsPreservesLegacyInterruptedScriptRun(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	interruptedAt := now + 6
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-interrupted-script-migration', 'Project', ?, ?, '{}')`, now, now)
	seedWorkflowGraph(t, db, "project-interrupted-script-migration", now)
	execSeed(t, db, "script node", `
UPDATE workflow_nodes
SET kind = 'script', script_path = 'scripts/migrate'
WHERE id = 'node-agent'`)
	execSeed(t, db, "task", workflowSeedTaskSQL, "task-interrupted-script-migration", "link-1", 1, "INT-2", now, now)
	execSeed(t, db, "script placement", workflowSeedPlacementSQL, "placement-interrupted-script-migration", "task-interrupted-script-migration", "node-agent", now, now)
	execSeed(t, db, "interrupted script run", `
INSERT INTO task_runs (
    id,
    placement_id,
    workflow_revision_seen,
    created_at_unix_ms,
    updated_at_unix_ms,
    started_at_unix_ms,
    interrupted_at_unix_ms,
    interruption_reason,
    interruption_detail_json
) VALUES (
    'run-interrupted-script-migration',
    'placement-interrupted-script-migration',
    1,
    ?,
    ?,
    ?,
    ?,
    'script_failure',
    '{"code":"workflow.execution.script_failure","fields":{"operation":"script"}}'
)`, now, now+10, now+1, interruptedAt)
	seedLegacyExecutableCurrentNodeEnteringEdge(t, db, "task-interrupted-script-migration", "placement-interrupted-script-migration", now)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var schedulingState, interruptionReason, interruptionDetail string
	var projectedInterruptedAt int64
	if err := store.db.QueryRowContext(t.Context(), `
SELECT scheduling_state, interruption_reason, interruption_detail_json, interrupted_at_unix_ms
FROM task_current_nodes
WHERE task_id = 'task-interrupted-script-migration'`).Scan(
		&schedulingState,
		&interruptionReason,
		&interruptionDetail,
		&projectedInterruptedAt,
	); err != nil {
		t.Fatalf("query projected interrupted script current node: %v", err)
	}
	if schedulingState != "interrupted" ||
		interruptionReason != "script_failure" ||
		interruptionDetail != `{"code":"workflow.execution.script_failure","fields":{"operation":"script"}}` ||
		projectedInterruptedAt != interruptedAt {
		t.Fatalf(
			"projected interrupted script current node = scheduling=%q reason=%q detail=%q interrupted_at=%d",
			schedulingState,
			interruptionReason,
			interruptionDetail,
			projectedInterruptedAt,
		)
	}
}

func TestOpenBackfillsExecutionTargetsForEveryLegacyTaskWithUsableRecordedWorktreeHead(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 52)
	if err != nil {
		t.Fatalf("open version 52 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	execSeed(t, db, "legacy project", `INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms)
VALUES ('project-legacy-target', 'Legacy target project', ?, ?)`, now, now)
	execSeed(t, db, "legacy workspace", `INSERT INTO workspaces (id, project_id, canonical_root_path, git_metadata_json, created_at_unix_ms, updated_at_unix_ms)
VALUES ('workspace-legacy-target', 'project-legacy-target', ?, '{}', ?, ?)`, t.TempDir(), now, now)
	seedWorkflowGraph(t, db, "project-legacy-target", now)
	execSeed(t, db, "legacy managed worktrees", `INSERT INTO worktrees (
    id, workspace_id, canonical_root_path, managed, created_branch, origin_session_id, git_metadata_json, created_at_unix_ms, updated_at_unix_ms
) VALUES
    ('worktree-legacy-valid', 'workspace-legacy-target', ?, 1, 1, '', '{"head_oid":"observed-commit","branch_ref":"refs/heads/BLD-1"}', ?, ?),
    ('worktree-legacy-invalid', 'workspace-legacy-target', ?, 1, 1, '', '{"branch_ref":"refs/heads/BLD-3"}', ?, ?)`,
		t.TempDir(), now, now, t.TempDir(), now, now,
	)
	for _, task := range []struct {
		id          string
		taskSeq     int
		shortID     string
		worktreeID  string
		placementID string
		nodeID      string
		runID       string
	}{
		{id: "task-legacy-executed", taskSeq: 1, shortID: "BLD-1", worktreeID: "worktree-legacy-valid", placementID: "placement-legacy-executed", nodeID: "node-agent", runID: "run-legacy-executed"},
		{id: "task-legacy-backlog", taskSeq: 2, shortID: "BLD-2", worktreeID: "worktree-legacy-valid", placementID: "placement-legacy-backlog", nodeID: "node-start"},
		{id: "task-legacy-invalid-oid", taskSeq: 3, shortID: "BLD-3", worktreeID: "worktree-legacy-invalid", placementID: "placement-legacy-invalid", nodeID: "node-agent", runID: "run-legacy-invalid"},
	} {
		execSeed(t, db, "legacy task", `INSERT INTO tasks (
    id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body, source_workspace_id, managed_worktree_id,
    created_at_unix_ms, updated_at_unix_ms, metadata_json
) VALUES (?, 'link-1', 1, ?, ?, 'Legacy task', '', 'workspace-legacy-target', ?, ?, ?, '{}')`,
			task.id, task.taskSeq, task.shortID, task.worktreeID, now, now,
		)
		execSeed(t, db, "legacy placement", `INSERT INTO task_node_placements (id, task_id, node_id, state, created_at_unix_ms, updated_at_unix_ms)
VALUES (?, ?, ?, 'active', ?, ?)`, task.placementID, task.id, task.nodeID, now, now)
		if task.runID != "" {
			execSeed(t, db, "legacy executable run", `INSERT INTO task_runs (id, placement_id, workflow_revision_seen, created_at_unix_ms, updated_at_unix_ms)
VALUES (?, ?, 1, ?, ?)`, task.runID, task.placementID, now, now)
		}
		if task.nodeID == "node-agent" {
			seedLegacyExecutableCurrentNodeEnteringEdge(t, db, task.id, task.placementID, now)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close version 52 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var policy string
	if err := store.db.QueryRow(`SELECT execution_target_policy FROM workflows WHERE id = ?`, workflowTestID(t, "1")).Scan(&policy); err != nil {
		t.Fatalf("read migrated workflow policy: %v", err)
	}
	if policy != "head" {
		t.Fatalf("migrated workflow policy = %q, want head", policy)
	}

	type targetRow struct {
		mode       sql.NullString
		requested  sql.NullString
		resolved   sql.NullString
		commitOID  sql.NullString
		provenance sql.NullString
		worktreeID sql.NullString
	}
	readTarget := func(taskID string) targetRow {
		t.Helper()
		var row targetRow
		if err := store.db.QueryRow(`SELECT
    execution_target_mode,
    execution_target_requested_ref,
    execution_target_resolved_ref,
    execution_target_commit_oid,
    execution_target_provenance,
    managed_worktree_id
FROM tasks
WHERE id = ?`, taskID).Scan(&row.mode, &row.requested, &row.resolved, &row.commitOID, &row.provenance, &row.worktreeID); err != nil {
			t.Fatalf("read migrated task target %s: %v", taskID, err)
		}
		return row
	}

	executed := readTarget("task-legacy-executed")
	if !executed.mode.Valid || executed.mode.String != "head" ||
		!executed.requested.Valid || executed.requested.String != "HEAD" ||
		!executed.resolved.Valid || executed.resolved.String != "refs/heads/BLD-1" ||
		!executed.commitOID.Valid || executed.commitOID.String != "observed-commit" ||
		!executed.provenance.Valid || executed.provenance.String != "legacy_observed" ||
		!executed.worktreeID.Valid || executed.worktreeID.String != "worktree-legacy-valid" {
		t.Fatalf("executed legacy target = %+v, want observed head target", executed)
	}
	backlog := readTarget("task-legacy-backlog")
	if !backlog.mode.Valid || backlog.mode.String != "head" ||
		!backlog.requested.Valid || backlog.requested.String != "HEAD" ||
		!backlog.resolved.Valid || backlog.resolved.String != "refs/heads/BLD-1" ||
		!backlog.commitOID.Valid || backlog.commitOID.String != "observed-commit" ||
		!backlog.provenance.Valid || backlog.provenance.String != "legacy_observed" ||
		!backlog.worktreeID.Valid || backlog.worktreeID.String != "worktree-legacy-valid" {
		t.Fatalf("backlog legacy target = %+v, want observed head target", backlog)
	}
	invalid := readTarget("task-legacy-invalid-oid")
	if invalid.mode.Valid || invalid.requested.Valid || invalid.resolved.Valid || invalid.commitOID.Valid || invalid.provenance.Valid {
		t.Fatalf("invalid migrated task target = %+v, want all snapshot facts null", invalid)
	}
	if !invalid.worktreeID.Valid {
		t.Fatal("invalid migrated task lost provisional managed worktree relation")
	}
}
