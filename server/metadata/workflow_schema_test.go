package metadata

import (
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed testdata/workflow_project_key_backfill.sql
var workflowProjectKeyBackfillSQL string

//go:embed testdata/workflow_schema_node_groups.sql
var workflowSchemaNodeGroupsSQL string

//go:embed testdata/workflow_seed_graph_nodes.sql
var workflowSeedGraphNodesSQL string

//go:embed testdata/workflow_seed_graph_edges.sql
var workflowSeedGraphEdgesSQL string

//go:embed testdata/workflow_seed_task.sql
var workflowSeedTaskSQL string

//go:embed testdata/workflow_seed_placement.sql
var workflowSeedPlacementSQL string

func TestOpenCreatesWorkflowSchemaAndForeignKeys(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	for _, table := range []string{
		"workflows",
		"workflow_nodes",
		"workflow_transition_groups",
		"workflow_edges",
		"project_workflow_links",
		"tasks",
		"task_node_placements",
		"task_runs",
		"task_transitions",
		"task_transition_edges",
		"task_comments",
	} {
		if !tableExists(t, store.db, table) {
			t.Fatalf("expected table %s to exist", table)
		}
	}
	if tableExists(t, store.db, "workflow_events") {
		t.Fatal("workflow_events should not exist; workflow invalidations are process-local live signals")
	}
	if tableExists(t, store.db, "runtime_leases") {
		t.Fatal("runtime_leases should not exist; runtime ownership is process-local run state")
	}
	for _, index := range []string{
		"workspaces_project_idx",
		"workflow_transition_groups_source_transition_idx",
		"tasks_project_short_id_idx",
	} {
		if indexExists(t, store.db, index) {
			t.Fatalf("index %s should not exist; replacement indexes cover its lookup shape", index)
		}
	}
	if columnExists(t, store.db, "workflows", "start_node_id") {
		t.Fatal("workflows.start_node_id should not exist; start node is derived from workflow_nodes.kind")
	}
	if columnExists(t, store.db, "workflow_edges", "source_node_id") {
		t.Fatal("workflow_edges.source_node_id should not exist; source is derived from transition group")
	}
	if columnExists(t, store.db, "workflow_transition_groups", "workflow_id") {
		t.Fatal("workflow_transition_groups.workflow_id should not exist; workflow is derived from source node")
	}
	if columnExists(t, store.db, "workflow_edges", "workflow_id") {
		t.Fatal("workflow_edges.workflow_id should not exist; workflow is derived from transition group source node")
	}
	if !columnExists(t, store.db, "workflow_edges", "prompt_template") {
		t.Fatal("workflow_edges.prompt_template should exist")
	}
	if !columnExists(t, store.db, "workflow_edges", "parameters_json") {
		t.Fatal("workflow_edges.parameters_json should exist")
	}
	if !columnExists(t, store.db, "workflow_transition_groups", "description") {
		t.Fatal("workflow_transition_groups.description should exist")
	}
	for _, table := range []string{
		"workflows",
		"workflow_nodes",
		"workflow_node_groups",
		"workflow_transition_groups",
		"workflow_edges",
	} {
		if columnExists(t, store.db, table, "metadata_json") {
			t.Fatalf("%s.metadata_json should not exist; workflow-definition opaque metadata is not persisted", table)
		}
	}
	if columnExists(t, store.db, "project_workflow_links", "unlinked_at_unix_ms") {
		t.Fatal("project_workflow_links.unlinked_at_unix_ms should not exist; links are active membership rows only")
	}
	if columnExists(t, store.db, "project_workflow_links", "is_default") {
		t.Fatal("project_workflow_links.is_default should not exist; default workflow link is project-owned")
	}
	if !columnExists(t, store.db, "projects", "default_project_workflow_link_id") {
		t.Fatal("projects.default_project_workflow_link_id should exist")
	}
	if !columnExists(t, store.db, "projects", "primary_workspace_id") {
		t.Fatal("projects.primary_workspace_id should exist")
	}
	for _, column := range []string{"display_name", "availability", "is_primary"} {
		if columnExists(t, store.db, "workspaces", column) {
			t.Fatalf("workspaces.%s should not exist; workspace labels/status/default are derived read-model facts", column)
		}
	}
	for _, column := range []string{"display_name", "availability", "is_main"} {
		if columnExists(t, store.db, "worktrees", column) {
			t.Fatalf("worktrees.%s should not exist; worktree labels/status/main are derived read-model facts", column)
		}
	}
	if columnExists(t, store.db, "tasks", "project_id") {
		t.Fatal("tasks.project_id should not exist; task project is derived from project_workflow_link_id")
	}
	if columnExists(t, store.db, "tasks", "workflow_id") {
		t.Fatal("tasks.workflow_id should not exist; task workflow is derived from project_workflow_link_id")
	}
	if !columnExists(t, store.db, "tasks", "source_url") {
		t.Fatal("tasks.source_url should stay as a structured task field")
	}
	if columnExists(t, store.db, "task_runs", "task_id") {
		t.Fatal("task_runs.task_id should not exist; run task is derived from placement_id")
	}
	if columnExists(t, store.db, "task_runs", "node_id") {
		t.Fatal("task_runs.node_id should not exist; run node is derived from placement_id")
	}
	if !viewExists(t, store.db, "task_run_records") {
		t.Fatal("task_run_records view should expose derived run task/node fields")
	}
	if columnExists(t, store.db, "task_transition_edges", "workflow_revision_seen") {
		t.Fatal("task_transition_edges.workflow_revision_seen should not exist; edge revision is derived from its transition")
	}
	if !viewExists(t, store.db, "task_transition_edge_records") {
		t.Fatal("task_transition_edge_records view should expose derived edge workflow revision")
	}
	if columnExists(t, store.db, "task_node_placements", "created_by_transition_id") {
		t.Fatal("task_node_placements.created_by_transition_id should not exist; placement provenance is derived from transition edges")
	}
	if !viewExists(t, store.db, "task_node_placement_records") {
		t.Fatal("task_node_placement_records view should expose derived placement provenance")
	}
	if columnExists(t, store.db, "task_transitions", "source_node_id") {
		t.Fatal("task_transitions.source_node_id should not exist; transition source node is derived from source placement")
	}
	if !viewExists(t, store.db, "task_transition_records") {
		t.Fatal("task_transition_records view should expose derived transition source node")
	}
	if columnExists(t, store.db, "task_transitions", "transition_group_id") {
		t.Fatal("task_transitions.transition_group_id should not exist; transition group is derived from edge snapshots when available")
	}
	for _, column := range []string{"source_run_id", "deleted_at_unix_ms", "metadata_json"} {
		if columnExists(t, store.db, "task_comments", column) {
			t.Fatalf("task_comments.%s should not exist; comments are hard-deleted task notes", column)
		}
	}
	var foreignKeys int
	if err := store.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}
}

func TestOpenBackfillsProjectKeysForExistingMetadataDB(t *testing.T) {
	root := t.TempDir()
	dbPath := root + "/db/main.sqlite3"
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 4)
	if err != nil {
		t.Fatalf("open version 4 db: %v", err)
	}
	execSeed(t, db, "version 4 db", workflowProjectKeyBackfillSQL)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 4 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("Open migrated db: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	keys := projectKeysByID(t, store.db)
	if keys["project-a"] != "BUI" {
		t.Fatalf("project-a key = %q, want BUI", keys["project-a"])
	}
	if keys["project-b"] != "BUI2" {
		t.Fatalf("project-b key = %q, want BUI2", keys["project-b"])
	}
}

func TestProjectKeyValidationCollisionAndMutability(t *testing.T) {
	store, _, binding := newMetadataTestStore(t)
	ctx := t.Context()
	other, err := store.CreateProjectForWorkspace(ctx, t.TempDir(), "Other Project")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace: %v", err)
	}

	if err := store.SetProjectKey(ctx, binding.ProjectID, "bad-key"); !errors.Is(err, ErrInvalidProjectKey) {
		t.Fatalf("expected invalid project key error, got %v", err)
	}
	if err := store.SetProjectKey(ctx, binding.ProjectID, "BLD"); err != nil {
		t.Fatalf("SetProjectKey BLD: %v", err)
	}
	if err := store.SetProjectKey(ctx, other.ProjectID, "BLD"); !errors.Is(err, ErrProjectKeyAlreadyInUse) {
		t.Fatalf("expected project key collision, got %v", err)
	}
	seedWorkflowGraph(t, store.db, binding.ProjectID, time.Now().UTC().UnixMilli())
	seedWorkflowTask(t, store, binding.ProjectID, "BLD-1")

	// The project key is mutable even after tasks exist: the change only affects
	// the prefix applied to future tasks. Existing task short IDs stay frozen.
	if err := store.SetProjectKey(ctx, binding.ProjectID, "NEW"); err != nil {
		t.Fatalf("SetProjectKey after tasks exist: %v", err)
	}
	if got := projectKeysByID(t, store.db)[binding.ProjectID]; got != "NEW" {
		t.Fatalf("project key after rename = %q, want NEW", got)
	}
	if got := taskShortIDByID(t, store.db, "task-1"); got != "BLD-1" {
		t.Fatalf("existing task short id = %q, want frozen BLD-1", got)
	}
	// Collision is still enforced against the renamed key.
	if err := store.SetProjectKey(ctx, other.ProjectID, "NEW"); !errors.Is(err, ErrProjectKeyAlreadyInUse) {
		t.Fatalf("expected project key collision after rename, got %v", err)
	}
	// Re-applying the current value is a no-op.
	if err := store.SetProjectKey(ctx, binding.ProjectID, "NEW"); err != nil {
		t.Fatalf("SetProjectKey same value: %v", err)
	}
}

func TestWorkflowSchemaConstraints(t *testing.T) {
	store, _, binding := newMetadataTestStore(t)
	now := time.Now().UTC().UnixMilli()
	seedWorkflowGraph(t, store.db, binding.ProjectID, now)
	seedWorkflowTask(t, store, binding.ProjectID, "BLD-1")

	execSeed(t, store.db, "node groups", workflowSchemaNodeGroupsSQL, now, now)

	assertSQLiteConstraint(t, store.db, `INSERT INTO workflow_nodes (id, workflow_id, node_key, kind, display_name, output_fields_json) VALUES ('node-second-start', 'workflow-1', 'second_start', 'start', 'Second Start', '[]')`)
	assertSQLiteConstraint(t, store.db, `INSERT INTO workflow_nodes (id, workflow_id, node_key, kind, display_name, output_fields_json) VALUES ('node-invalid-kind', 'workflow-1', 'bad', 'robot', 'Bad', '[]')`)
	assertSQLiteConstraint(t, store.db, `INSERT INTO workflow_nodes (id, workflow_id, node_key, kind, display_name, completion_mode, output_fields_json) VALUES ('node-terminal-completion-mode', 'workflow-1', 'terminal_mode', 'terminal', 'Terminal Mode', 'tool', '[]')`)
	assertSQLiteConstraint(t, store.db, `INSERT INTO workflow_nodes (id, workflow_id, node_key, kind, display_name, output_fields_json, group_id) VALUES ('node-cross-group', 'workflow-1', 'cross_group', 'agent', 'Cross Group', '[]', 'group-other')`)
	assertSQLiteConstraint(t, store.db, `UPDATE workflow_nodes SET group_id = 'group-other' WHERE id = 'node-agent'`)
	assertSQLiteConstraint(t, store.db, `INSERT INTO workflow_edges (id, transition_group_id, edge_key, target_node_id, requires_approval, context_mode, input_bindings_json, output_requirements_json) VALUES ('edge-invalid-bool', 'group-start', 'bad_bool', 'node-agent', 2, 'new_session', '{}', '{}')`)
	assertSQLiteConstraint(t, store.db, `INSERT INTO workflow_edges (id, transition_group_id, edge_key, target_node_id, requires_approval, context_mode, context_source_kind, context_source_node_key, input_bindings_json, output_requirements_json) VALUES ('edge-invalid-context-source-empty-key', 'group-start', 'bad_context_empty', 'node-agent', 0, 'continue_session', 'selected_node', '', '{}', '{}')`)
	assertSQLiteConstraint(t, store.db, `INSERT INTO workflow_edges (id, transition_group_id, edge_key, target_node_id, requires_approval, context_mode, context_source_kind, context_source_node_key, input_bindings_json, output_requirements_json) VALUES ('edge-invalid-context-source-immediate-key', 'group-start', 'bad_context_key', 'node-agent', 0, 'continue_session', 'immediate_source', 'agent', '{}', '{}')`)
	assertSQLiteConstraint(t, store.db, `INSERT INTO workflow_edges (id, transition_group_id, edge_key, target_node_id, requires_approval, context_mode, parameters_json, input_bindings_json, output_requirements_json) VALUES ('edge-invalid-parameters-json', 'group-start', 'bad_parameters_json', 'node-agent', 0, 'new_session', '{}', '{}', '{}')`)
	execSeed(t, store.db, "previous target context source edge", `INSERT INTO workflow_edges (id, transition_group_id, edge_key, target_node_id, requires_approval, context_mode, context_source_kind, context_source_node_key, input_bindings_json, output_requirements_json) VALUES ('edge-previous-target-context-source', 'group-start', 'previous_target_context', 'node-agent', 0, 'continue_session', 'previous_target', '', '{}', '{}')`)
	assertSQLiteConstraint(t, store.db, `INSERT INTO workflow_edges (id, transition_group_id, edge_key, target_node_id, requires_approval, context_mode, context_source_kind, context_source_node_key, input_bindings_json, output_requirements_json) VALUES ('edge-invalid-context-source-previous-target-key', 'group-start', 'bad_previous_target_context_key', 'node-agent', 0, 'continue_session', 'previous_target', 'agent', '{}', '{}')`)
	assertSQLiteConstraint(t, store.db, `INSERT INTO workflows (id, name, version, created_at_unix_ms, updated_at_unix_ms) VALUES ('workflow-bad-time', 'Bad', 1, -1, 1)`)
	assertSQLiteConstraint(t, store.db, `INSERT INTO workflows (id, name, version, created_at_unix_ms, updated_at_unix_ms) VALUES ('workflow-bad-rev', 'Bad', 0, 1, 1)`)
	assertSQLiteConstraint(t, store.db, `INSERT INTO task_runs (id, placement_id, workflow_revision_seen, effective_completion_mode, created_at_unix_ms, updated_at_unix_ms) VALUES ('run-bad-completion-mode', 'placement-start', 1, 'invalid', 1, 1)`)
	assertSQLiteConstraint(t, store.db, `INSERT INTO task_runs (id, placement_id, workflow_revision_seen, invalid_completion_count, created_at_unix_ms, updated_at_unix_ms) VALUES ('run-bad-counter', 'placement-start', 1, -1, 1, 1)`)
	assertSQLiteConstraint(t, store.db, `INSERT INTO task_comments (id, task_id, body, author_kind, created_at_unix_ms, updated_at_unix_ms) VALUES ('comment-system-author', 'task-1', 'system note', 'system', 1, 1)`)
	assertSQLiteConstraint(t, store.db, `INSERT INTO task_comments (id, task_id, body, author_kind, created_at_unix_ms, updated_at_unix_ms) VALUES ('comment-too-large', 'task-1', ?, 'agent', 1, 1)`, strings.Repeat("a", 262145))
}

func TestTaskSchemaAllowsEmptyBodyAndProjectScopedSourceWorkspace(t *testing.T) {
	store, _, binding := newMetadataTestStore(t)
	ctx := t.Context()
	other, err := store.CreateProjectForWorkspace(ctx, t.TempDir(), "Other Project")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace: %v", err)
	}
	source, err := store.AttachWorkspaceToProject(ctx, binding.ProjectID, t.TempDir())
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject source: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	seedWorkflowGraph(t, store.db, binding.ProjectID, now)
	seedWorkflowGraphForProject(t, store.db, other.ProjectID, now, "2")

	if _, err := store.db.Exec(`INSERT INTO tasks (id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body, source_workspace_id, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('task-empty-body', 'link-1', 1, 1, 'BLD-1', 'Task', '', ?, ?, ?, '{}')`, source.WorkspaceID, now, now); err != nil {
		t.Fatalf("empty task body with source workspace should be allowed: %v", err)
	}
	assertSQLiteConstraint(t, store.db, `INSERT INTO tasks (id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body, source_workspace_id, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('task-foreign-workspace', 'link-2', 1, 1, 'OTH-1', 'Task', '', ?, ?, ?, '{}')`, source.WorkspaceID, now, now)
}

func TestWorkflowSchemaRejectsCrossProjectTaskLinkFacts(t *testing.T) {
	store, _, binding := newMetadataTestStore(t)
	ctx := t.Context()
	other, err := store.CreateProjectForWorkspace(ctx, t.TempDir(), "Other Project")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	seedWorkflowGraph(t, store.db, binding.ProjectID, now)
	seedWorkflowGraphForProject(t, store.db, other.ProjectID, now, "2")
	seedWorkflowTask(t, store, binding.ProjectID, "BLD-1")

	assertSQLiteConstraint(t, store.db, `INSERT INTO tasks (id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body, source_workspace_id, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('task-cross-workspace', 'link-2', 1, 1, 'OTH-1', 'Task', '', (SELECT id FROM workspaces WHERE project_id = ? LIMIT 1), ?, ?, '{}')`, binding.ProjectID, now, now)
	assertSQLiteConstraint(t, store.db, `INSERT INTO tasks (id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('task-duplicate-seq', 'link-1', 1, 1, 'BLD-1', 'Task', '', ?, ?, '{}')`, now, now)
	assertSQLiteConstraint(t, store.db, `UPDATE projects SET default_project_workflow_link_id = 'link-2' WHERE id = ?`, binding.ProjectID)
}

func TestProjectPrimaryWorkspaceSchemaRejectsCrossProjectPointer(t *testing.T) {
	store, _, binding := newMetadataTestStore(t)
	ctx := t.Context()
	other, err := store.CreateProjectForWorkspace(ctx, t.TempDir(), "Other Project")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace: %v", err)
	}

	assertSQLiteConstraint(t, store.db, `UPDATE projects SET primary_workspace_id = ? WHERE id = ?`, other.WorkspaceID, binding.ProjectID)
}

func TestWorkspaceSessionSchemaRejectsCrossProjectReferences(t *testing.T) {
	store, _, binding := newMetadataTestStore(t)
	ctx := t.Context()
	other, err := store.CreateProjectForWorkspace(ctx, t.TempDir(), "Other Project")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace: %v", err)
	}
	otherWorktreeRoot := t.TempDir()
	if err := store.UpsertWorktreeRecord(ctx, WorktreeRecord{
		ID:              "worktree-other",
		WorkspaceID:     other.WorkspaceID,
		CanonicalRoot:   otherWorktreeRoot,
		DisplayName:     "other",
		Availability:    "available",
		GitMetadataJSON: "{}",
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord other: %v", err)
	}
	if err := store.UpsertWorktreeRecord(ctx, WorktreeRecord{
		ID:              "worktree-valid",
		WorkspaceID:     binding.WorkspaceID,
		CanonicalRoot:   t.TempDir(),
		DisplayName:     "valid",
		Availability:    "available",
		GitMetadataJSON: "{}",
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord valid: %v", err)
	}
	now := time.Now().UTC().UnixMilli()

	assertSQLiteConstraint(t, store.db, `INSERT INTO sessions (id, project_id, workspace_id, artifact_relpath, created_at_unix_ms, updated_at_unix_ms)
VALUES ('session-cross-workspace', ?, ?, 'projects/project/sessions/session-cross-workspace', ?, ?)`, binding.ProjectID, other.WorkspaceID, now, now)
	assertSQLiteConstraint(t, store.db, `INSERT INTO sessions (id, project_id, workspace_id, worktree_id, artifact_relpath, created_at_unix_ms, updated_at_unix_ms)
VALUES ('session-cross-worktree', ?, ?, 'worktree-other', 'projects/project/sessions/session-cross-worktree', ?, ?)`, binding.ProjectID, binding.WorkspaceID, now, now)
	assertSQLiteConstraint(t, store.db, `INSERT INTO sessions (id, project_id, worktree_id, artifact_relpath, created_at_unix_ms, updated_at_unix_ms)
VALUES ('session-worktree-without-workspace', ?, 'worktree-other', 'projects/project/sessions/session-worktree-without-workspace', ?, ?)`, binding.ProjectID, now, now)
	if _, err := store.db.Exec(`INSERT INTO sessions (id, project_id, workspace_id, worktree_id, artifact_relpath, created_at_unix_ms, updated_at_unix_ms)
VALUES ('session-valid-worktree', ?, ?, 'worktree-valid', 'projects/project/sessions/session-valid-worktree', ?, ?)`, binding.ProjectID, binding.WorkspaceID, now, now); err != nil {
		t.Fatalf("valid session worktree should be allowed: %v", err)
	}
	assertSQLiteConstraint(t, store.db, `UPDATE sessions SET workspace_id = NULL WHERE id = 'session-valid-worktree'`)
}

func TestTaskManagedWorktreeSchemaRejectsCrossWorkspaceReferences(t *testing.T) {
	store, _, binding := newMetadataTestStore(t)
	ctx := t.Context()
	other, err := store.CreateProjectForWorkspace(ctx, t.TempDir(), "Other Project")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace: %v", err)
	}
	if err := store.UpsertWorktreeRecord(ctx, WorktreeRecord{
		ID:              "worktree-valid",
		WorkspaceID:     binding.WorkspaceID,
		CanonicalRoot:   t.TempDir(),
		DisplayName:     "valid",
		Availability:    "available",
		GitMetadataJSON: "{}",
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord valid: %v", err)
	}
	if err := store.UpsertWorktreeRecord(ctx, WorktreeRecord{
		ID:              "worktree-other",
		WorkspaceID:     other.WorkspaceID,
		CanonicalRoot:   t.TempDir(),
		DisplayName:     "other",
		Availability:    "available",
		GitMetadataJSON: "{}",
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord other: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	seedWorkflowGraph(t, store.db, binding.ProjectID, now)

	if _, err := store.db.Exec(`INSERT INTO tasks (id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body, source_workspace_id, managed_worktree_id, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('task-valid-worktree', 'link-1', 1, 1, 'BLD-1', 'Task', '', ?, 'worktree-valid', ?, ?, '{}')`, binding.WorkspaceID, now, now); err != nil {
		t.Fatalf("valid managed worktree should be allowed: %v", err)
	}
	assertSQLiteConstraint(t, store.db, `UPDATE tasks SET source_workspace_id = NULL WHERE id = 'task-valid-worktree'`)
	assertSQLiteConstraint(t, store.db, `INSERT INTO tasks (id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body, source_workspace_id, managed_worktree_id, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('task-cross-worktree', 'link-1', 1, 2, 'BLD-2', 'Task', '', ?, 'worktree-other', ?, ?, '{}')`, binding.WorkspaceID, now, now)
	assertSQLiteConstraint(t, store.db, `INSERT INTO tasks (id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body, managed_worktree_id, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('task-missing-source-workspace', 'link-1', 1, 3, 'BLD-3', 'Task', '', 'worktree-valid', ?, ?, '{}')`, now, now)
}

func TestWorkflowRuntimeSchemaRejectsCrossWorkflowPlacementsAndRuns(t *testing.T) {
	store, _, binding := newMetadataTestStore(t)
	now := time.Now().UTC().UnixMilli()
	seedWorkflowGraph(t, store.db, binding.ProjectID, now)
	seedWorkflowGraphForProject(t, store.db, binding.ProjectID, now, "2")
	seedWorkflowTask(t, store, binding.ProjectID, "BLD-1")
	seedWorkflowTaskWithID(t, store, "task-2", "link-1", 2, "BLD-2", "placement-start-2", "node-start")

	assertSQLiteConstraint(t, store.db, `INSERT INTO task_node_placements (id, task_id, node_id, state, created_at_unix_ms, updated_at_unix_ms)
VALUES ('placement-cross-workflow', 'task-1', 'node-agent-2', 'active', ?, ?)`, now, now)
	assertSQLiteConstraint(t, store.db, `UPDATE task_node_placements SET node_id = 'node-agent-2' WHERE id = 'placement-start'`)
	execSeed(t, store.db, "derived task run", `INSERT INTO task_runs (id, placement_id, workflow_revision_seen, created_at_unix_ms, updated_at_unix_ms)
VALUES ('run-derived', 'placement-start', 1, ?, ?)`, now, now)
	var taskID string
	var nodeID string
	if err := store.db.QueryRow(`SELECT task_id, node_id FROM task_run_records WHERE id = 'run-derived'`).Scan(&taskID, &nodeID); err != nil {
		t.Fatalf("query derived task run: %v", err)
	}
	if taskID != "task-1" || nodeID != "node-start" {
		t.Fatalf("derived task run = task %q node %q, want task-1/node-start", taskID, nodeID)
	}
}

func TestWorkflowRuntimeSchemaRejectsCrossTaskTransitionsAndEdges(t *testing.T) {
	store, _, binding := newMetadataTestStore(t)
	now := time.Now().UTC().UnixMilli()
	seedWorkflowGraph(t, store.db, binding.ProjectID, now)
	seedWorkflowGraphForProject(t, store.db, binding.ProjectID, now, "2")
	seedWorkflowTask(t, store, binding.ProjectID, "BLD-1")
	seedWorkflowTaskWithID(t, store, "task-2", "link-1", 2, "BLD-2", "placement-start-2", "node-start")
	execSeed(t, store.db, "task-2 run", `INSERT INTO task_runs (id, placement_id, workflow_revision_seen, created_at_unix_ms, updated_at_unix_ms)
VALUES ('run-2', 'placement-start-2', 1, ?, ?)`, now, now)

	assertSQLiteConstraint(t, store.db, `INSERT INTO task_transitions (id, task_id, source_run_id, source_placement_id, transition_id, workflow_revision_seen, actor, state, output_values_json, created_at_unix_ms)
VALUES ('transition-cross-run', 'task-1', 'run-2', 'placement-start', 'start', 1, 'system', 'applied', '{}', ?)`, now)
	assertSQLiteConstraint(t, store.db, `INSERT INTO task_transitions (id, task_id, source_placement_id, transition_id, workflow_revision_seen, actor, state, output_values_json, created_at_unix_ms)
VALUES ('transition-cross-placement', 'task-1', 'placement-start-2', 'start', 1, 'system', 'applied', '{}', ?)`, now)
	execSeed(t, store.db, "valid transition", `INSERT INTO task_transitions (id, task_id, source_placement_id, transition_id, workflow_revision_seen, actor, state, output_values_json, created_at_unix_ms)
VALUES ('transition-valid', 'task-1', 'placement-start', 'start', 1, 'system', 'applied', '{}', ?)`, now)
	var sourceNodeID string
	if err := store.db.QueryRow(`SELECT source_node_id FROM task_transition_records WHERE id = 'transition-valid'`).Scan(&sourceNodeID); err != nil {
		t.Fatalf("query derived transition source node: %v", err)
	}
	if sourceNodeID != "node-start" {
		t.Fatalf("derived transition source node = %q, want node-start", sourceNodeID)
	}
	execSeed(t, store.db, "valid transition edge", `INSERT INTO task_transition_edges (id, task_transition_id, workflow_edge_id, edge_key, target_node_id, state, input_bindings_json, output_requirements_json)
VALUES ('transition-edge-valid', 'transition-valid', 'edge-start-1', 'start', 'node-agent', 'pending', '[]', '[]')`)
	var transitionGroupID string
	if err := store.db.QueryRow(`SELECT transition_group_id FROM task_transition_records WHERE id = 'transition-valid'`).Scan(&transitionGroupID); err != nil {
		t.Fatalf("query derived transition group: %v", err)
	}
	if transitionGroupID != "group-start" {
		t.Fatalf("derived transition group = %q, want group-start", transitionGroupID)
	}
	var edgeRevision int64
	if err := store.db.QueryRow(`SELECT workflow_revision_seen FROM task_transition_edge_records WHERE id = 'transition-edge-valid'`).Scan(&edgeRevision); err != nil {
		t.Fatalf("query derived transition edge revision: %v", err)
	}
	if edgeRevision != 1 {
		t.Fatalf("derived transition edge revision = %d, want 1", edgeRevision)
	}
	assertSQLiteConstraint(t, store.db, `INSERT INTO task_transition_edges (id, task_transition_id, edge_key, target_node_id, target_placement_id, state, input_bindings_json, output_requirements_json)
VALUES ('transition-edge-cross-task', 'transition-valid', 'bad', 'node-start', 'placement-start-2', 'applied', '[]', '[]')`)
	assertSQLiteConstraint(t, store.db, `INSERT INTO task_transition_edges (id, task_transition_id, edge_key, target_node_id, state, input_bindings_json, output_requirements_json)
VALUES ('transition-edge-cross-node', 'transition-valid', 'bad', 'node-agent-2', 'pending', '[]', '[]')`)
	assertSQLiteConstraint(t, store.db, `INSERT INTO task_transition_edges (id, task_transition_id, workflow_edge_id, edge_key, target_node_id, state, input_bindings_json, output_requirements_json)
VALUES ('transition-edge-cross-workflow-edge', 'transition-valid', 'edge-start-2', 'bad', 'node-agent', 'pending', '[]', '[]')`)
	assertSQLiteConstraint(t, store.db, `INSERT INTO task_transition_edges (id, task_transition_id, workflow_edge_id, edge_key, target_node_id, state, input_bindings_json, output_requirements_json)
VALUES ('transition-edge-object-inputs', 'transition-valid', 'edge-start-1', 'bad', 'node-agent', 'pending', '{}', '[]')`)
	assertSQLiteConstraint(t, store.db, `INSERT INTO task_transition_edges (id, task_transition_id, workflow_edge_id, edge_key, target_node_id, state, input_bindings_json, output_requirements_json)
VALUES ('transition-edge-object-outputs', 'transition-valid', 'edge-start-1', 'bad', 'node-agent', 'pending', '[]', '{}')`)
}

func TestTaskShortIDUniquenessIsProjectScoped(t *testing.T) {
	store, _, binding := newMetadataTestStore(t)
	ctx := t.Context()
	other, err := store.CreateProjectForWorkspace(ctx, t.TempDir(), "Other Project")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	seedWorkflowGraph(t, store.db, binding.ProjectID, now)
	seedWorkflowGraphForProject(t, store.db, other.ProjectID, now, "2")
	seedWorkflowTask(t, store, binding.ProjectID, "BLD-1")
	assertSQLiteConstraint(t, store.db, `INSERT INTO tasks (id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('task-dup', 'link-1', 1, 2, 'BLD-1', 'Task', 'Body', ?, ?, '{}')`, now, now)
	if _, err := store.db.Exec(`INSERT INTO tasks (id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('task-other', 'link-2', 1, 1, 'BLD-1', 'Task', 'Body', ?, ?, '{}')`, now, now); err != nil {
		t.Fatalf("same short id in another project should be allowed: %v", err)
	}
}

func TestTaskSequenceAllocationIsAtomic(t *testing.T) {
	store, _, binding := newMetadataTestStore(t)
	ctx := t.Context()
	if err := store.SetProjectKey(ctx, binding.ProjectID, "BLD"); err != nil {
		t.Fatalf("SetProjectKey: %v", err)
	}

	const workers = 12
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	seqs := make(chan int64, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key, seq, err := store.AllocateProjectTaskSequence(ctx, binding.ProjectID)
			if err != nil {
				errs <- err
				return
			}
			if key != "BLD" {
				errs <- fmt.Errorf("project key = %q, want BLD", key)
				return
			}
			seqs <- seq
		}()
	}
	wg.Wait()
	close(errs)
	close(seqs)
	for err := range errs {
		t.Fatalf("AllocateProjectTaskSequence: %v", err)
	}
	seen := map[int64]bool{}
	for seq := range seqs {
		if seen[seq] {
			t.Fatalf("duplicate sequence %d", seq)
		}
		seen[seq] = true
	}
	for seq := int64(1); seq <= workers; seq++ {
		if !seen[seq] {
			t.Fatalf("missing sequence %d in %+v", seq, seen)
		}
	}
}

func TestCircularTransitionPlacementReferencesUseNullableDomainValidatedPath(t *testing.T) {
	store, _, binding := newMetadataTestStore(t)
	now := time.Now().UTC().UnixMilli()
	seedWorkflowGraph(t, store.db, binding.ProjectID, now)
	seedWorkflowTask(t, store, binding.ProjectID, "BLD-1")

	if _, err := store.db.Exec(`INSERT INTO task_transitions (id, task_id, source_placement_id, transition_id, workflow_revision_seen, actor, state, output_values_json, created_at_unix_ms)
VALUES ('transition-1', 'task-1', 'placement-start', 'start', 1, 'system', 'applied', '{}', ?)`, now); err != nil {
		t.Fatalf("insert transition referencing existing placement: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO task_node_placements (id, task_id, node_id, state, created_at_unix_ms, updated_at_unix_ms)
VALUES ('placement-agent', 'task-1', 'node-agent', 'active', ?, ?)`, now, now); err != nil {
		t.Fatalf("insert placement before transition edge: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO task_transition_edges (id, task_transition_id, workflow_edge_id, edge_key, target_node_id, target_placement_id, state, input_bindings_json, output_requirements_json)
VALUES ('transition-edge-1', 'transition-1', 'edge-start-1', 'start', 'node-agent', 'placement-agent', 'applied', '[]', '[]')`); err != nil {
		t.Fatalf("insert transition edge referencing placement: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE task_transitions SET applied_at_unix_ms = ? WHERE id = 'transition-1'`, now); err != nil {
		t.Fatalf("update transition after circular insert path: %v", err)
	}
}

func tableExists(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	var name string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		t.Fatalf("query table %s: %v", table, err)
	}
	return name == table
}

func viewExists(t *testing.T, db *sql.DB, view string) bool {
	t.Helper()
	var name string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'view' AND name = ?`, view).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		t.Fatalf("query view %s: %v", view, err)
	}
	return name == view
}

func indexExists(t *testing.T, db *sql.DB, index string) bool {
	t.Helper()
	var name string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		t.Fatalf("query index %s: %v", index, err)
	}
	return name == index
}

func columnExists(t *testing.T, db *sql.DB, table string, column string) bool {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatalf("table_info %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid int
		var name string
		var typ string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan table_info %s: %v", table, err)
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info %s: %v", table, err)
	}
	return false
}

func projectKeysByID(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT id, project_key FROM projects ORDER BY id`)
	if err != nil {
		t.Fatalf("query project keys: %v", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var id string
		var key string
		if err := rows.Scan(&id, &key); err != nil {
			t.Fatalf("scan project key: %v", err)
		}
		out[id] = key
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate project keys: %v", err)
	}
	return out
}

func taskShortIDByID(t *testing.T, db *sql.DB, taskID string) string {
	t.Helper()
	var shortID string
	if err := db.QueryRow(`SELECT short_id FROM tasks WHERE id = ?`, taskID).Scan(&shortID); err != nil {
		t.Fatalf("query task short id %q: %v", taskID, err)
	}
	return shortID
}

func assertSQLiteConstraint(t *testing.T, db *sql.DB, statement string, args ...any) {
	t.Helper()
	_, err := db.Exec(statement, args...)
	if err == nil {
		t.Fatalf("expected SQLite constraint failure for %s", statement)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "constraint") {
		t.Fatalf("expected constraint failure, got %v", err)
	}
}

func seedWorkflowGraph(t *testing.T, db *sql.DB, projectID string, now int64) {
	t.Helper()
	seedWorkflowGraphForProject(t, db, projectID, now, "1")
}

func seedWorkflowGraphForProject(t *testing.T, db *sql.DB, projectID string, now int64, suffix string) {
	t.Helper()
	workflowID := "workflow-" + suffix
	startID := "node-start-" + suffix
	agentID := "node-agent-" + suffix
	doneID := "node-done-" + suffix
	startGroupID := "group-start-" + suffix
	doneGroupID := "group-done-" + suffix
	if suffix == "1" {
		startID = "node-start"
		agentID = "node-agent"
		doneID = "node-done"
		startGroupID = "group-start"
		doneGroupID = "group-done"
	}
	execSeed(t, db, "workflow", `INSERT INTO workflows (id, name, description, version, created_at_unix_ms, updated_at_unix_ms)
VALUES (?, 'Workflow', '', 1, ?, ?)`, workflowID, now, now)
	execSeed(t, db, "nodes", workflowSeedGraphNodesSQL, startID, workflowID, agentID, workflowID, doneID, workflowID)
	execSeed(t, db, "transition groups", `INSERT INTO workflow_transition_groups (id, source_node_id, transition_id, display_name)
VALUES (?, ?, 'start', 'Start'),
       (?, ?, 'done', 'Done')`, startGroupID, startID, doneGroupID, agentID)
	execSeed(t, db, "edges", workflowSeedGraphEdgesSQL, "edge-start-"+suffix, startGroupID, agentID, "edge-done-"+suffix, doneGroupID, doneID)
	linkID := "link-" + suffix
	execSeed(t, db, "project workflow link", `INSERT INTO project_workflow_links (id, project_id, workflow_id, created_at_unix_ms, updated_at_unix_ms)
VALUES (?, ?, ?, ?, ?)`, linkID, projectID, workflowID, now, now)
	execSeed(t, db, "project default workflow link", `UPDATE projects SET default_project_workflow_link_id = ? WHERE id = ?`, linkID, projectID)
}

func execSeed(t *testing.T, db *sql.DB, label string, statement string, args ...any) {
	t.Helper()
	if _, err := db.Exec(statement, args...); err != nil {
		t.Fatalf("seed %s: %v", label, err)
	}
}

func seedWorkflowTask(t *testing.T, store *Store, projectID string, shortID string) {
	t.Helper()
	seedWorkflowTaskWithID(t, store, "task-1", "link-1", 1, shortID, "placement-start", "node-start")
}

func seedWorkflowTaskWithID(t *testing.T, store *Store, taskID string, linkID string, taskSeq int64, shortID string, placementID string, nodeID string) {
	t.Helper()
	now := time.Now().UTC().UnixMilli()
	execSeed(t, store.db, "workflow task", workflowSeedTaskSQL, taskID, linkID, taskSeq, shortID, now, now)
	execSeed(t, store.db, "workflow placement", workflowSeedPlacementSQL, placementID, taskID, nodeID, now, now)
}
