package workflowstore

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"core/server/metadata"
	"core/server/workflow"
	"core/shared/runtimeids"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

func TestMigratedSerialApprovalFanoutAppliesFrozenTargetBranches(t *testing.T) {
	root := t.TempDir()
	databasePath := filepath.Join(root, "db", "main.sqlite3")
	db := openLegacyCurrentStateMigrationDatabase(t, root, databasePath)
	now := time.Now().UTC().UnixMilli()
	execLegacyMigrationSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-migrated-approval-fanout', 'Project', ?, ?, '{}')`, now, now)
	execLegacyMigrationSeed(t, db, "workflow", `
INSERT INTO workflows (id, name, description, version, created_at_unix_ms, updated_at_unix_ms)
VALUES ('workflow-migrated-approval-fanout', 'Workflow', '', 1, ?, ?)`, now, now)
	execLegacyMigrationSeed(t, db, "workflow nodes", `
INSERT INTO workflow_nodes (
    id, workflow_id, node_key, kind, display_name, subagent_role, output_fields_json
) VALUES
    ('node-start', 'workflow-migrated-approval-fanout', 'backlog', 'start', 'Backlog', '', '[]'),
    ('node-source', 'workflow-migrated-approval-fanout', 'source', 'agent', 'Source', 'coder', '[]'),
    ('node-target-a', 'workflow-migrated-approval-fanout', 'target_a', 'agent', 'Target A', 'coder', '[]'),
    ('node-target-b', 'workflow-migrated-approval-fanout', 'target_b', 'agent', 'Target B', 'coder', '[]'),
    ('node-done', 'workflow-migrated-approval-fanout', 'done', 'terminal', 'Done', '', '[]')`, now, now)
	execLegacyMigrationSeed(t, db, "workflow transition groups", `
INSERT INTO workflow_transition_groups (id, source_node_id, transition_id, display_name)
VALUES
    ('group-start', 'node-start', 'start', 'Start'),
    ('group-fanout', 'node-source', 'approve', 'Approve')`)
	execLegacyMigrationSeed(t, db, "workflow edges", `
INSERT INTO workflow_edges (
    id, transition_group_id, edge_key, target_node_id, requires_approval,
    context_mode, input_bindings_json, output_requirements_json
) VALUES
    ('edge-start', 'group-start', 'start', 'node-source', 0, 'new_session', '[]', '[]'),
    ('edge-fanout-a', 'group-fanout', 'split_a', 'node-target-a', 1, 'new_session', '[]', '[]'),
    ('edge-fanout-b', 'group-fanout', 'split_b', 'node-target-b', 1, 'new_session', '[]', '[]')`)
	execLegacyMigrationSeed(t, db, "workflow link", `
INSERT INTO project_workflow_links (
    id, project_id, workflow_id, created_at_unix_ms, updated_at_unix_ms
) VALUES ('link-migrated-approval-fanout', 'project-migrated-approval-fanout', 'workflow-migrated-approval-fanout', ?, ?)`, now, now)
	execLegacyMigrationSeed(t, db, "default workflow link", `
UPDATE projects
SET default_project_workflow_link_id = 'link-migrated-approval-fanout'
WHERE id = 'project-migrated-approval-fanout'`)
	execLegacyMigrationSeed(t, db, "task", `
INSERT INTO tasks (
    id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id,
    title, body, created_at_unix_ms, updated_at_unix_ms, metadata_json
) VALUES (
    'task-migrated-approval-fanout',
    'link-migrated-approval-fanout',
    1,
    1,
    'MIG-1',
    'Task',
    'Body',
    ?,
    ?,
    '{}'
)`, now, now)
	execLegacyMigrationSeed(t, db, "approval source placement", `
INSERT INTO task_node_placements (
    id, task_id, node_id, state, created_at_unix_ms, updated_at_unix_ms
) VALUES (
    'placement-migrated-approval-fanout',
    'task-migrated-approval-fanout',
    'node-source',
    'waiting_approval',
    ?,
    ?
)`, now, now)
	execLegacyMigrationSeed(t, db, "approval transition", `
INSERT INTO task_transitions (
    id, task_id, source_placement_id, source_node_key, source_node_display_name,
    transition_id, transition_display_name, workflow_revision_seen, actor, state,
    commentary, output_values_json, created_at_unix_ms
) VALUES (
    'transition-migrated-approval-fanout',
    'task-migrated-approval-fanout',
    'placement-migrated-approval-fanout',
    'source',
    'Source',
    'approve',
    'Approve',
    1,
    'agent',
    'pending_approval',
    '',
    '{}',
    ?
)`, now+1)
	for _, target := range []struct {
		id     string
		edgeID string
		key    string
		nodeID string
		name   string
	}{
		{"transition-edge-migrated-a", "edge-fanout-a", "split_a", "node-target-a", "Target A"},
		{"transition-edge-migrated-b", "edge-fanout-b", "split_b", "node-target-b", "Target B"},
	} {
		execLegacyMigrationSeed(t, db, "approval transition edge", `
INSERT INTO task_transition_edges (
    id, task_transition_id, workflow_edge_id, edge_key,
    target_node_id, target_node_key, target_node_display_name, target_node_kind,
    state, context_mode, requires_approval, input_bindings_json, output_requirements_json, metadata_json
) VALUES (?, 'transition-migrated-approval-fanout', ?, ?, ?, ?, ?, 'agent', 'pending', 'new_session', 1, '[]', '[]', '{}')`,
			target.id,
			target.edgeID,
			target.key,
			target.nodeID,
			target.key,
			target.name,
		)
	}
	execLegacyMigrationSeed(t, db, "delete mutable approval graph", `
DELETE FROM workflow_transition_groups
WHERE id = 'group-fanout'`)
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy migration database: %v", err)
	}

	metadataStore, err := metadata.Open(root)
	if err != nil {
		t.Fatalf("migrate legacy approval fanout database: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
	store, err := New(metadataStore)
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}

	approvals, err := store.ListPendingApprovals(t.Context(), workflow.TaskID("task-migrated-approval-fanout"))
	if err != nil {
		t.Fatalf("ListPendingApprovals after migration: %v", err)
	}
	if len(approvals) != 1 || len(approvals[0].Branches) != 2 {
		t.Fatalf("migrated approvals = %+v, want one two-branch approval", approvals)
	}
	approval := approvals[0]
	for _, branch := range approval.Branches {
		targetBranchKey, branchScoped := branch.Target.CurrentNode.Reference.TransitionBranchKey()
		if !branchScoped || targetBranchKey != branch.TransitionBranchKey {
			t.Fatalf("migrated approval branch = %+v, want target scoped to frozen branch key", branch)
		}
	}

	applied, err := store.ApplyPendingApproval(t.Context(), approval.ID)
	if err != nil {
		t.Fatalf("ApplyPendingApproval migrated fanout: %v", err)
	}
	if len(applied.Mutation.Removed) != 1 || len(applied.Mutation.Created) != 2 {
		t.Fatalf("applied migrated fanout = %+v, want source replaced by two targets", applied)
	}
	for _, target := range applied.Mutation.Created {
		if !target.Reference.IsBranchScoped() {
			t.Fatalf("applied migrated target = %+v, want branch-scoped current node", target)
		}
	}
}

func TestResolveCurrentSessionStartContextUsesMigratedDirectOwnership(t *testing.T) {
	root := t.TempDir()
	databasePath := filepath.Join(root, "db", "main.sqlite3")
	db := openLegacyCurrentStateMigrationDatabase(t, root, databasePath)
	now := time.Now().UTC().UnixMilli()
	const (
		projectID  = "project-migrated-session-context"
		workflowID = "workflow-migrated-session-context"
		taskID     = "task-migrated-session-context"
		sessionID  = "550e8400-e29b-41d4-a716-446655440090"
		runID      = "run-migrated-session-context"
	)
	snapshotJSON, err := workflow.MarshalString(runStartSnapshot{
		WorkflowID:           workflow.WorkflowID(workflowID),
		WorkflowRevisionSeen: 1,
		Node: nodeContractSnapshot{
			ID:           "node-agent",
			Key:          "agent",
			DisplayName:  "Agent",
			Kind:         workflow.NodeKindAgent,
			SubagentRole: "coder",
		},
	})
	if err != nil {
		t.Fatalf("marshal legacy run snapshot: %v", err)
	}
	execLegacyMigrationSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES (?, 'Project', ?, ?, '{}')`, projectID, now, now)
	execLegacyMigrationSeed(t, db, "workspace", `
INSERT INTO workspaces (
    id, project_id, canonical_root_path, git_metadata_json, created_at_unix_ms, updated_at_unix_ms
) VALUES ('workspace-migrated-session-context', ?, '/workspace-migrated-session-context', '{}', ?, ?)`,
		projectID,
		now,
		now,
	)
	execLegacyMigrationSeed(t, db, "session", `
INSERT INTO sessions (
    id, project_id, workspace_id, artifact_relpath, created_at_unix_ms, updated_at_unix_ms, metadata_json
) VALUES (?, ?, 'workspace-migrated-session-context', ?, ?, ?, json_object(
    'workflow_session',
    json_object(
        'run_id', 'stale-run-id',
        'task_id', 'stale-task-id',
        'workflow_id', 'stale-workflow-id'
    )
))`,
		sessionID,
		projectID,
		"sessions/"+sessionID,
		now,
		now,
	)
	execLegacyMigrationSeed(t, db, "workflow", `
INSERT INTO workflows (id, name, description, version, created_at_unix_ms, updated_at_unix_ms)
VALUES (?, 'Workflow', '', 1, ?, ?)`, workflowID, now, now)
	execLegacyMigrationSeed(t, db, "workflow nodes", `
INSERT INTO workflow_nodes (
    id, workflow_id, node_key, kind, display_name, subagent_role, output_fields_json
) VALUES
    ('node-start', ?, 'backlog', 'start', 'Backlog', '', '[]'),
    ('node-agent', ?, 'agent', 'agent', 'Agent', 'coder', '[]'),
    ('node-done', ?, 'done', 'terminal', 'Done', '', '[]')`,
		workflowID,
		workflowID,
		workflowID,
	)
	execLegacyMigrationSeed(t, db, "workflow link", `
INSERT INTO project_workflow_links (
    id, project_id, workflow_id, created_at_unix_ms, updated_at_unix_ms
) VALUES ('link-migrated-session-context', ?, ?, ?, ?)`, projectID, workflowID, now, now)
	execLegacyMigrationSeed(t, db, "default workflow link", `
UPDATE projects
SET default_project_workflow_link_id = 'link-migrated-session-context'
WHERE id = ?`, projectID)
	execLegacyMigrationSeed(t, db, "task", `
INSERT INTO tasks (
    id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id,
    title, body, created_at_unix_ms, updated_at_unix_ms, metadata_json
) VALUES (?, 'link-migrated-session-context', 1, 1, 'MIG-2', 'Task', 'Body', ?, ?, '{}')`,
		taskID,
		now,
		now,
	)
	execLegacyMigrationSeed(t, db, "agent placement", `
INSERT INTO task_node_placements (
    id, task_id, node_id, state, created_at_unix_ms, updated_at_unix_ms
) VALUES ('placement-migrated-session-context', ?, 'node-agent', 'active', ?, ?)`,
		taskID,
		now,
		now+1,
	)
	execLegacyMigrationSeed(t, db, "agent run", `
INSERT INTO task_runs (
    id, placement_id, session_id, workflow_revision_seen,
    created_at_unix_ms, updated_at_unix_ms, started_at_unix_ms,
    run_start_snapshot_json
) VALUES (?, 'placement-migrated-session-context', ?, 1, ?, ?, ?, ?)`,
		runID,
		sessionID,
		now,
		now+2,
		now+1,
		snapshotJSON,
	)
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy migration database: %v", err)
	}

	metadataStore, err := metadata.Open(root)
	if err != nil {
		t.Fatalf("migrate legacy session context database: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
	store, err := New(metadataStore)
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	parsedSessionID, err := runtimeids.ParseSessionID(sessionID)
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	workflowOwned, err := metadataStore.SessionHasWorkflowTask(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("SessionHasWorkflowTask: %v", err)
	}
	if !workflowOwned {
		t.Fatal("migrated session direct task ownership was not retained")
	}

	input, err := store.ResolveCurrentSessionStartContext(t.Context(), parsedSessionID)
	if err != nil {
		t.Fatalf("ResolveCurrentSessionStartContext: %v", err)
	}
	if input.Run.ID != workflow.RunID(runID) || input.Task.ID != workflow.TaskID(taskID) || input.Node.ID != workflow.NodeID("node-agent") {
		t.Fatalf("resolved migrated context = run=%q task=%q node=%q, want direct current ownership", input.Run.ID, input.Task.ID, input.Node.ID)
	}
}

func openLegacyCurrentStateMigrationDatabase(t *testing.T, root string, databasePath string) *sql.DB {
	t.Helper()
	bootstrap, err := metadata.Open(t.TempDir())
	if err != nil {
		t.Fatalf("initialize metadata SQLite extensions: %v", err)
	}
	if err := bootstrap.Close(); err != nil {
		t.Fatalf("close metadata SQLite extension bootstrap: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o755); err != nil {
		t.Fatalf("create migration database directory: %v", err)
	}
	db, err := sql.Open("sqlite", "file:"+databasePath+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open legacy migration database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	migrations, err := legacyCurrentStateMigrationFiles()
	if err != nil {
		t.Fatalf("read metadata migrations: %v", err)
	}
	provider, err := goose.NewProvider(
		goose.DialectSQLite3,
		db,
		migrations,
		goose.WithLogger(goose.NopLogger()),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	if _, err := provider.UpTo(context.Background(), 58); err != nil {
		t.Fatalf("apply migrations through version 58: %v", err)
	}
	return db
}

func legacyCurrentStateMigrationFiles() (fs.FS, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return nil, os.ErrNotExist
	}
	migrationFiles := os.DirFS(filepath.Join(filepath.Dir(file), "..", "metadata", "migrations"))
	entries, err := fs.ReadDir(migrationFiles, ".")
	if err != nil {
		return nil, err
	}
	upMigrations := make(fstest.MapFS)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".up.sql") {
			continue
		}
		contents, err := fs.ReadFile(migrationFiles, entry.Name())
		if err != nil {
			return nil, err
		}
		upMigrations[entry.Name()] = &fstest.MapFile{Data: contents}
	}
	return upMigrations, nil
}

func execLegacyMigrationSeed(t *testing.T, db *sql.DB, label string, statement string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), statement, args...); err != nil {
		t.Fatalf("seed %s: %v", label, err)
	}
}
