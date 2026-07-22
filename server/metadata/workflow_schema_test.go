package metadata

import (
	"database/sql"
	_ "embed"
	"errors"
	"strings"
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
		"project_labels",
		"task_label_assignments",
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
	for _, column := range []string{"normalized_name", "revision", "color", "sort_order"} {
		if columnExists(t, store.db, "project_labels", column) {
			t.Fatalf("project_labels.%s should not exist", column)
		}
	}
	if !indexExists(t, store.db, "task_label_assignments_label_task_idx") {
		t.Fatal("task_label_assignments_label_task_idx should support reverse label membership")
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
	for _, view := range []string{
		"workflow_task_status_task_records",
		"workflow_task_status_run_records",
		"workflow_task_status_transition_records",
		"workflow_task_current_run_records",
		"workflow_task_status_records",
	} {
		if !viewExists(t, store.db, view) {
			t.Fatalf("%s view should expose canonical task status facts", view)
		}
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

// The project key is mutable even after tasks exist: the change only affects
// the prefix applied to future tasks. Existing task short IDs stay frozen.

// Collision is still enforced against the renamed key.

// Re-applying the current value is a no-op.

func insertExecutionTargetSchemaTask(t *testing.T, db *sql.DB, id string, taskSeq int, shortID string, sourceWorkspaceID string, managedWorktreeID *string, mode *string, requestedRef *string, resolvedRef *string, commitOID *string, provenance *string, now int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO tasks (
    id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body, source_workspace_id, managed_worktree_id,
    execution_target_mode, execution_target_requested_ref, execution_target_resolved_ref, execution_target_commit_oid, execution_target_provenance,
    created_at_unix_ms, updated_at_unix_ms, metadata_json
) VALUES (?, 'link-1', 1, ?, ?, 'Task', '', ?, ?, ?, ?, ?, ?, ?, ?, ?, '{}')`,
		id, taskSeq, shortID, sourceWorkspaceID, managedWorktreeID, mode, requestedRef, resolvedRef, commitOID, provenance, now, now,
	); err != nil {
		t.Fatalf("insert execution target task %s: %v", id, err)
	}
}

func stringPointerForSchemaTest(value string) *string {
	return &value
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
