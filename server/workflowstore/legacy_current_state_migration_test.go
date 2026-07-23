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
