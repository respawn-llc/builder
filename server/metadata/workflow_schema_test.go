package metadata

import (
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"core/shared/runtimeids"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
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
	t.Parallel()
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
		"task_current_nodes",
		"task_active_fanouts",
		"task_active_fanout_branches",
		"task_pending_approvals",
		"task_pending_approval_branches",
		"session_workflow_node_associations",
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
	for _, column := range []string{"prompt_template", "input_fields_json", "output_fields_json"} {
		if columnExists(t, store.db, "workflow_nodes", column) {
			t.Fatalf("workflow_nodes.%s should not exist; Node-owned invocation contracts are deleted", column)
		}
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
	for _, column := range []string{"canceled_at_unix_ms", "cancellation_reason"} {
		if columnExists(t, store.db, "tasks", column) {
			t.Fatalf("tasks.%s should not exist; task cancellation was removed", column)
		}
	}
	for _, column := range []string{"normalized_name", "revision", "color", "sort_order"} {
		if columnExists(t, store.db, "project_labels", column) {
			t.Fatalf("project_labels.%s should not exist", column)
		}
	}
	if !columnExists(t, store.db, "project_labels", "ordinal") {
		t.Fatal("project_labels.ordinal should exist")
	}
	var ordinalNotNull int
	if err := store.db.QueryRow(`
SELECT "notnull"
FROM pragma_table_info('project_labels')
WHERE name = 'ordinal'`).Scan(&ordinalNotNull); err != nil {
		t.Fatalf("inspect project_labels.ordinal: %v", err)
	}
	if ordinalNotNull != 1 {
		t.Fatalf("project_labels.ordinal not-null flag = %d, want 1", ordinalNotNull)
	}
	if !indexExists(t, store.db, "project_labels_project_ordinal_idx") {
		t.Fatal("project_labels_project_ordinal_idx should support per-project order uniqueness")
	}
	if !indexExists(t, store.db, "task_label_assignments_label_task_idx") {
		t.Fatal("task_label_assignments_label_task_idx should support reverse label membership")
	}
	for _, relation := range []string{
		"task_node_placements",
		"task_runs",
		"task_transitions",
		"task_transition_edges",
		"task_node_placement_records",
		"task_run_records",
		"task_transition_records",
		"task_transition_edge_records",
		"workflow_task_status_task_records",
		"workflow_task_status_run_records",
		"workflow_task_status_transition_records",
		"workflow_task_current_run_records",
		"workflow_attention_candidates",
	} {
		if tableExists(t, store.db, relation) || viewExists(t, store.db, relation) {
			t.Fatalf("legacy workflow relation %s should not exist after the hard cutover", relation)
		}
	}
	if !viewExists(t, store.db, "workflow_task_status_records") {
		t.Fatal("workflow_task_status_records should expose canonical Current Node status facts")
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

func TestProjectLabelsOrderMigrationBackfillsExistingCatalog(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 69)
	if err != nil {
		t.Fatalf("open version 69 metadata database: %v", err)
	}
	now := int64(1)
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-label-order-migration', 'Project', ?, ?, '{}')`, now, now)
	execSeed(t, db, "labels", `
INSERT INTO project_labels (id, project_id, name, created_at_unix_ms, updated_at_unix_ms)
VALUES
    ('label-zulu', 'project-label-order-migration', 'Zulu', ?, ?),
    ('label-alpha', 'project-label-order-migration', 'alpha', ?, ?),
    ('label-beta', 'project-label-order-migration', 'Beta', ?, ?)`,
		now, now, now, now, now, now,
	)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 69 database: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated metadata store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	rows, err := store.db.Query(`
SELECT id, ordinal
FROM project_labels
WHERE project_id = 'project-label-order-migration'
ORDER BY ordinal ASC`)
	if err != nil {
		t.Fatalf("query migrated label ordinals: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var got []struct {
		id      string
		ordinal int64
	}
	for rows.Next() {
		var row struct {
			id      string
			ordinal int64
		}
		if err := rows.Scan(&row.id, &row.ordinal); err != nil {
			t.Fatalf("scan migrated label ordinal: %v", err)
		}
		got = append(got, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migrated label ordinals: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("migrated label rows = %+v, want 3 rows", got)
	}
	remaining := map[string]struct{}{
		"label-zulu":  {},
		"label-alpha": {},
		"label-beta":  {},
	}
	for index, row := range got {
		if row.ordinal != int64(index+1) {
			t.Fatalf("migrated row %d ordinal = %d, want %d", index, row.ordinal, index+1)
		}
		if _, ok := remaining[row.id]; !ok {
			t.Fatalf("migrated row %d has unexpected or duplicate ID %q", index, row.id)
		}
		delete(remaining, row.id)
	}
	if len(remaining) != 0 {
		t.Fatalf("migration lost legacy labels: %+v", remaining)
	}
}

func TestTaskSessionAssociationSchemaUsesDirectOwnerAndNaturalKeys(t *testing.T) {
	t.Parallel()
	store, cfg, binding := newMetadataTestStore(t)
	ctx := t.Context()
	now := time.Now().UTC().UnixMilli()
	seedWorkflowGraph(t, store.db, binding.ProjectID, now)
	seedWorkflowTask(t, store, binding.ProjectID, "BLD-1")
	other, err := store.CreateProjectForWorkspace(ctx, t.TempDir(), "Other Project")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace: %v", err)
	}
	seedWorkflowGraphForProject(t, store.db, other.ProjectID, now, "2")
	seedWorkflowTaskWithID(t, store, "task-2", "link-2", 2, "OTH-1", "placement-start-2", "node-start-2")
	sessionID := createMetadataTestSession(t, store, cfg, binding).Meta().SessionID

	assertExactTableColumns(t, store.db, "session_workflow_node_associations", map[string]struct{}{
		"session_id":            {},
		"node_id":               {},
		"transition_branch_key": {},
		"associated_at_unix_ms": {},
	})
	for _, index := range []string{
		"session_workflow_node_associations_serial_unique_idx",
		"session_workflow_node_associations_branch_unique_idx",
	} {
		if !indexExists(t, store.db, index) {
			t.Fatalf("expected session association index %s", index)
		}
	}
	for _, assertion := range []struct {
		table string
		want  int
	}{
		{table: "session_workflow_node_associations", want: 0},
		{table: "task_current_nodes", want: 1},
	} {
		var nodeForeignKeyCount int
		if err := store.db.QueryRow(`
SELECT COUNT(*)
FROM pragma_foreign_key_list(?)
WHERE "from" = 'node_id'
  AND "table" = 'workflow_nodes'`, assertion.table).Scan(&nodeForeignKeyCount); err != nil {
			t.Fatalf("inspect %s.node_id foreign key: %v", assertion.table, err)
		}
		if nodeForeignKeyCount != assertion.want {
			t.Fatalf("%s.node_id workflow_nodes foreign keys = %d, want %d", assertion.table, nodeForeignKeyCount, assertion.want)
		}
	}

	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_TRIGGER, `INSERT INTO session_workflow_node_associations (
    session_id, node_id, transition_branch_key, associated_at_unix_ms
) VALUES (?, 'node-agent', NULL, ?)`, sessionID, now)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_TRIGGER, `UPDATE sessions
SET task_id = 'task-2'
WHERE id = ?`, sessionID)
	if _, err := store.db.Exec(`UPDATE sessions
SET task_id = 'task-1'
WHERE id = ?`, sessionID); err != nil {
		t.Fatalf("bind session to direct task owner: %v", err)
	}

	if _, err := store.db.Exec(`INSERT INTO session_workflow_node_associations (
    session_id, node_id, transition_branch_key, associated_at_unix_ms
) VALUES (?, 'node-agent', NULL, ?)`, sessionID, now); err != nil {
		t.Fatalf("insert serial association: %v", err)
	}
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_UNIQUE, `INSERT INTO session_workflow_node_associations (
    session_id, node_id, transition_branch_key, associated_at_unix_ms
) VALUES (?, 'node-agent', NULL, ?)`, sessionID, now+1)
	for _, branch := range []string{"branch-a", "branch-b"} {
		if _, err := store.db.Exec(`INSERT INTO session_workflow_node_associations (
    session_id, node_id, transition_branch_key, associated_at_unix_ms
) VALUES (?, 'node-agent', ?, ?)`, sessionID, branch, now); err != nil {
			t.Fatalf("insert branch association %q: %v", branch, err)
		}
	}
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_UNIQUE, `INSERT INTO session_workflow_node_associations (
    session_id, node_id, transition_branch_key, associated_at_unix_ms
) VALUES (?, 'node-agent', 'branch-a', ?)`, sessionID, now+1)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_TRIGGER, `INSERT INTO session_workflow_node_associations (
    session_id, node_id, transition_branch_key, associated_at_unix_ms
) VALUES (?, 'node-agent-2', NULL, ?)`, sessionID, now)

	if _, err := store.db.Exec(`UPDATE sessions
SET task_id = NULL
WHERE id = ?`, sessionID); err != nil {
		t.Fatalf("clear session direct task owner: %v", err)
	}
	var associationCount int
	if err := store.db.QueryRow(`SELECT COUNT(*)
FROM session_workflow_node_associations
WHERE session_id = ?`, sessionID).Scan(&associationCount); err != nil {
		t.Fatalf("count cleared session associations: %v", err)
	}
	if associationCount != 0 {
		t.Fatalf("cleared owner left %d session associations, want 0", associationCount)
	}
}

func TestProjectLabelSchemaEnforcesCatalogIdentityScopeAndCascades(t *testing.T) {
	t.Parallel()
	store, _, binding := newMetadataTestStore(t)
	ctx := t.Context()
	other, err := store.CreateProjectForWorkspace(ctx, t.TempDir(), "Other Project")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace: %v", err)
	}
	now := time.Now().UTC().UnixMilli()

	const (
		projectLabelID = "9e7bab10-773a-4a16-9d4f-4e7bd2321327"
		otherLabelID   = "7af5b40b-10db-4e66-a6eb-8043d758fd90"
		conflictID     = "fd3082ef-cc11-4824-ad3a-2d7863efeb07"
	)
	if _, err := store.db.Exec(
		`INSERT INTO project_labels (id, project_id, name, created_at_unix_ms, updated_at_unix_ms)
VALUES (?, ?, 'Straße', ?, ?)`,
		projectLabelID,
		binding.ProjectID,
		now,
		now,
	); err != nil {
		t.Fatalf("insert project label: %v", err)
	}
	if _, err := store.db.Exec(
		`INSERT INTO project_labels (id, project_id, name, created_at_unix_ms, updated_at_unix_ms)
VALUES (?, ?, 'STRASSE', ?, ?)`,
		conflictID,
		binding.ProjectID,
		now,
		now,
	); err == nil {
		t.Fatal("case-fold-equivalent label insert succeeded within one project")
	}
	if _, err := store.db.Exec(
		`INSERT INTO project_labels (id, project_id, name, created_at_unix_ms, updated_at_unix_ms)
VALUES (?, ?, 'STRASSE', ?, ?)`,
		otherLabelID,
		other.ProjectID,
		now,
		now,
	); err != nil {
		t.Fatalf("insert same folded name in another project: %v", err)
	}
	if _, err := store.db.Exec(
		`UPDATE project_labels SET name = 'straße', updated_at_unix_ms = ? WHERE id = ?`,
		now+1,
		projectLabelID,
	); err != nil {
		t.Fatalf("capitalization-only rename: %v", err)
	}

	seedWorkflowGraph(t, store.db, binding.ProjectID, now)
	seedWorkflowTask(t, store, binding.ProjectID, "BLD-1")
	if _, err := store.db.Exec(
		`INSERT INTO task_label_assignments (task_id, label_id) VALUES ('task-1', ?)`,
		projectLabelID,
	); err != nil {
		t.Fatalf("insert same-project task label assignment: %v", err)
	}
	if _, err := store.db.Exec(
		`INSERT INTO task_label_assignments (task_id, label_id) VALUES ('task-1', ?)`,
		otherLabelID,
	); err == nil {
		t.Fatal("cross-project task label assignment insert succeeded")
	}
	if _, err := store.db.Exec(
		`UPDATE task_label_assignments SET label_id = ? WHERE task_id = 'task-1' AND label_id = ?`,
		otherLabelID,
		projectLabelID,
	); err == nil {
		t.Fatal("cross-project task label assignment update succeeded")
	}
	if _, err := store.db.Exec(
		`UPDATE project_labels SET project_id = ? WHERE id = ?`,
		other.ProjectID,
		projectLabelID,
	); err == nil {
		t.Fatal("moving an assigned label to another project succeeded")
	}

	if _, err := store.db.Exec(`DELETE FROM project_labels WHERE id = ?`, projectLabelID); err != nil {
		t.Fatalf("delete assigned project label: %v", err)
	}
	var assignmentCount int
	if err := store.db.QueryRow(
		`SELECT COUNT(*) FROM task_label_assignments WHERE label_id = ?`,
		projectLabelID,
	).Scan(&assignmentCount); err != nil {
		t.Fatalf("count assignments after label delete: %v", err)
	}
	if assignmentCount != 0 {
		t.Fatalf("assignment count after label delete = %d, want 0", assignmentCount)
	}
}

func TestOpenBackfillsProjectKeysForExistingMetadataDB(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	store, _, binding := newMetadataTestStore(t)
	now := time.Now().UTC().UnixMilli()
	seedWorkflowGraph(t, store.db, binding.ProjectID, now)
	seedWorkflowTask(t, store, binding.ProjectID, "BLD-1")
	workflowID := workflowTestID(t, "1")
	otherWorkflowID := workflowTestID(t, "2")

	execSeed(t, store.db, "other workflow", `INSERT INTO workflows (id, name, version, created_at_unix_ms, updated_at_unix_ms) VALUES (?, 'Other', 1, ?, ?)`, otherWorkflowID, now, now)
	execSeed(t, store.db, "node groups", `INSERT INTO workflow_node_groups (id, workflow_id, group_key, display_name) VALUES ('group-workflow-1', ?, 'impl', 'Implementation'), ('group-other', ?, 'impl', 'Implementation')`, workflowID, otherWorkflowID)

	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_UNIQUE, `INSERT INTO workflow_nodes (id, workflow_id, node_key, kind, display_name) VALUES ('node-second-start', ?, 'second_start', 'start', 'Second Start')`, workflowID)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_CHECK, `INSERT INTO workflow_nodes (id, workflow_id, node_key, kind, display_name) VALUES ('node-invalid-kind', ?, 'bad', 'robot', 'Bad')`, workflowID)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_CHECK, `INSERT INTO workflow_nodes (id, workflow_id, node_key, kind, display_name, completion_mode) VALUES ('node-terminal-completion-mode', ?, 'terminal_mode', 'terminal', 'Terminal Mode', 'tool')`, workflowID)
	execSeed(t, store.db, "script node with path", `INSERT INTO workflow_nodes (id, workflow_id, node_key, kind, display_name, script_path) VALUES ('node-script-valid', ?, 'script_valid', 'script', 'Script Valid', 'scripts/complete')`, workflowID)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_CHECK, `INSERT INTO workflow_nodes (id, workflow_id, node_key, kind, display_name, script_path) VALUES ('node-script-blank-path', ?, 'script_blank', 'script', 'Script Blank', '   ')`, workflowID)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_CHECK, `INSERT INTO workflow_nodes (id, workflow_id, node_key, kind, display_name, script_path) VALUES ('node-agent-script-path', ?, 'agent_script_path', 'agent', 'Agent Script Path', 'scripts/complete')`, workflowID)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_TRIGGER, `INSERT INTO workflow_nodes (id, workflow_id, node_key, kind, display_name, group_id) VALUES ('node-cross-group', ?, 'cross_group', 'agent', 'Cross Group', 'group-other')`, workflowID)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_TRIGGER, `UPDATE workflow_nodes SET group_id = 'group-other' WHERE id = 'node-agent'`)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_CHECK, `INSERT INTO workflow_edges (id, transition_group_id, edge_key, target_node_id, requires_approval, context_mode, input_bindings_json, output_requirements_json) VALUES ('edge-invalid-bool', 'group-start', 'bad_bool', 'node-agent', 2, 'new_session', '{}', '{}')`)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_CHECK, `INSERT INTO workflow_edges (id, transition_group_id, edge_key, target_node_id, requires_approval, context_mode, context_source_kind, context_source_node_key, input_bindings_json, output_requirements_json) VALUES ('edge-invalid-context-source-empty-key', 'group-start', 'bad_context_empty', 'node-agent', 0, 'continue_session', 'selected_node', '', '{}', '{}')`)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_CHECK, `INSERT INTO workflow_edges (id, transition_group_id, edge_key, target_node_id, requires_approval, context_mode, context_source_kind, context_source_node_key, input_bindings_json, output_requirements_json) VALUES ('edge-invalid-context-source-immediate-key', 'group-start', 'bad_context_key', 'node-agent', 0, 'continue_session', 'immediate_source', 'agent', '{}', '{}')`)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_CHECK, `INSERT INTO workflow_edges (id, transition_group_id, edge_key, target_node_id, requires_approval, context_mode, parameters_json, input_bindings_json, output_requirements_json) VALUES ('edge-invalid-parameters-json', 'group-start', 'bad_parameters_json', 'node-agent', 0, 'new_session', '{}', '{}', '{}')`)
	execSeed(t, store.db, "previous target context source edge", `INSERT INTO workflow_edges (id, transition_group_id, edge_key, target_node_id, requires_approval, context_mode, context_source_kind, context_source_node_key, input_bindings_json, output_requirements_json) VALUES ('edge-previous-target-context-source', 'group-start', 'previous_target_context', 'node-agent', 0, 'continue_session', 'previous_target', '', '{}', '{}')`)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_CHECK, `INSERT INTO workflow_edges (id, transition_group_id, edge_key, target_node_id, requires_approval, context_mode, context_source_kind, context_source_node_key, input_bindings_json, output_requirements_json) VALUES ('edge-invalid-context-source-previous-target-key', 'group-start', 'bad_previous_target_context_key', 'node-agent', 0, 'continue_session', 'previous_target', 'agent', '{}', '{}')`)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_CHECK, `INSERT INTO workflows (id, name, version, created_at_unix_ms, updated_at_unix_ms) VALUES ('workflow-bad-time', 'Bad', 1, -1, 1)`)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_CHECK, `INSERT INTO workflows (id, name, version, created_at_unix_ms, updated_at_unix_ms) VALUES ('workflow-bad-rev', 'Bad', 0, 1, 1)`)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_CHECK, `INSERT INTO task_comments (id, task_id, body, author_kind, created_at_unix_ms, updated_at_unix_ms) VALUES ('comment-system-author', 'task-1', 'system note', 'system', 1, 1)`)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_CHECK, `INSERT INTO task_comments (id, task_id, body, author_kind, created_at_unix_ms, updated_at_unix_ms) VALUES ('comment-too-large', 'task-1', ?, 'agent', 1, 1)`, strings.Repeat("a", 262145))
}

func TestTaskSchemaAllowsEmptyBodyAndProjectScopedSourceWorkspace(t *testing.T) {
	t.Parallel()
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
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_TRIGGER, `INSERT INTO tasks (id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body, source_workspace_id, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('task-foreign-workspace', 'link-2', 1, 1, 'OTH-1', 'Task', '', ?, ?, ?, '{}')`, source.WorkspaceID, now, now)
}

func TestWorkflowSchemaRejectsCrossProjectTaskLinkFacts(t *testing.T) {
	t.Parallel()
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

	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_TRIGGER, `INSERT INTO tasks (id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body, source_workspace_id, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('task-cross-workspace', 'link-2', 1, 1, 'OTH-1', 'Task', '', (SELECT id FROM workspaces WHERE project_id = ? LIMIT 1), ?, ?, '{}')`, binding.ProjectID, now, now)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_TRIGGER, `INSERT INTO tasks (id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('task-duplicate-seq', 'link-1', 1, 1, 'BLD-1', 'Task', '', ?, ?, '{}')`, now, now)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_TRIGGER, `UPDATE projects SET default_project_workflow_link_id = 'link-2' WHERE id = ?`, binding.ProjectID)
}

func TestProjectPrimaryWorkspaceSchemaRejectsCrossProjectPointer(t *testing.T) {
	t.Parallel()
	store, _, binding := newMetadataTestStore(t)
	ctx := t.Context()
	other, err := store.CreateProjectForWorkspace(ctx, t.TempDir(), "Other Project")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace: %v", err)
	}

	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_TRIGGER, `UPDATE projects SET primary_workspace_id = ? WHERE id = ?`, other.WorkspaceID, binding.ProjectID)
}

func TestWorkspaceSessionSchemaRejectsCrossProjectReferences(t *testing.T) {
	t.Parallel()
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

	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_TRIGGER, `INSERT INTO sessions (id, project_id, workspace_id, artifact_relpath, created_at_unix_ms, updated_at_unix_ms)
VALUES ('session-cross-workspace', ?, ?, 'projects/project/sessions/session-cross-workspace', ?, ?)`, binding.ProjectID, other.WorkspaceID, now, now)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_TRIGGER, `INSERT INTO sessions (id, project_id, workspace_id, worktree_id, artifact_relpath, created_at_unix_ms, updated_at_unix_ms)
VALUES ('session-cross-worktree', ?, ?, 'worktree-other', 'projects/project/sessions/session-cross-worktree', ?, ?)`, binding.ProjectID, binding.WorkspaceID, now, now)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_TRIGGER, `INSERT INTO sessions (id, project_id, worktree_id, artifact_relpath, created_at_unix_ms, updated_at_unix_ms)
VALUES ('session-worktree-without-workspace', ?, 'worktree-other', 'projects/project/sessions/session-worktree-without-workspace', ?, ?)`, binding.ProjectID, now, now)
	if _, err := store.db.Exec(`INSERT INTO sessions (id, project_id, workspace_id, worktree_id, artifact_relpath, created_at_unix_ms, updated_at_unix_ms)
VALUES ('session-valid-worktree', ?, ?, 'worktree-valid', 'projects/project/sessions/session-valid-worktree', ?, ?)`, binding.ProjectID, binding.WorkspaceID, now, now); err != nil {
		t.Fatalf("valid session worktree should be allowed: %v", err)
	}
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_TRIGGER, `UPDATE sessions SET workspace_id = NULL WHERE id = 'session-valid-worktree'`)
}

func TestTaskManagedWorktreeSchemaRejectsCrossWorkspaceReferences(t *testing.T) {
	t.Parallel()
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
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_TRIGGER, `UPDATE tasks SET source_workspace_id = NULL WHERE id = 'task-valid-worktree'`)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_TRIGGER, `INSERT INTO tasks (id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body, source_workspace_id, managed_worktree_id, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('task-cross-worktree', 'link-1', 1, 2, 'BLD-2', 'Task', '', ?, 'worktree-other', ?, ?, '{}')`, binding.WorkspaceID, now, now)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_TRIGGER, `INSERT INTO tasks (id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body, managed_worktree_id, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('task-missing-source-workspace', 'link-1', 1, 3, 'BLD-3', 'Task', '', 'worktree-valid', ?, ?, '{}')`, now, now)
}

func TestWorkflowExecutionTargetSchemaConstraints(t *testing.T) {
	t.Parallel()
	store, _, binding := newMetadataTestStore(t)
	now := time.Now().UTC().UnixMilli()
	seedWorkflowGraph(t, store.db, binding.ProjectID, now)
	workflowID := workflowTestID(t, "1")
	worktreeRoot := t.TempDir()
	execSeed(t, store.db, "task target worktree", `INSERT INTO worktrees (
    id, workspace_id, canonical_root_path, managed, created_branch, origin_session_id, git_metadata_json, created_at_unix_ms, updated_at_unix_ms
) VALUES ('worktree-target', ?, ?, 1, 1, '', '{"head_oid":"legacy-oid","branch_ref":"refs/heads/BLD-1"}', ?, ?)`, binding.WorkspaceID, worktreeRoot, now, now)

	for _, column := range []string{
		"execution_target_policy",
		"execution_target_custom_ref",
	} {
		if !columnExists(t, store.db, "workflows", column) {
			t.Fatalf("workflows.%s should exist", column)
		}
	}
	for _, column := range []string{
		"execution_target_mode",
		"execution_target_requested_ref",
		"execution_target_resolved_ref",
		"execution_target_commit_oid",
		"execution_target_provenance",
	} {
		if !columnExists(t, store.db, "tasks", column) {
			t.Fatalf("tasks.%s should exist", column)
		}
	}
	if !columnExists(t, store.db, "worktrees", "creation_base_commit_oid") {
		t.Fatal("worktrees.creation_base_commit_oid should exist")
	}

	var policy string
	if err := store.db.QueryRow(`SELECT execution_target_policy FROM workflows WHERE id = ?`, workflowID).Scan(&policy); err != nil {
		t.Fatalf("read default workflow target policy: %v", err)
	}
	if policy != "ask_on_first_execution" {
		t.Fatalf("workflow target policy = %q, want ask_on_first_execution", policy)
	}
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_CHECK, `UPDATE workflows SET execution_target_policy = 'unknown' WHERE id = ?`, workflowID)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_CHECK, `UPDATE workflows SET execution_target_policy = 'head', execution_target_custom_ref = 'refs/heads/main' WHERE id = ?`, workflowID)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_CHECK, `UPDATE workflows SET execution_target_policy = 'custom_ref', execution_target_custom_ref = ' ' WHERE id = ?`, workflowID)
	if _, err := store.db.Exec(`UPDATE workflows SET execution_target_policy = 'custom_ref', execution_target_custom_ref = NULL WHERE id = ?`, workflowID); err != nil {
		t.Fatalf("incomplete custom-ref workflow draft should persist: %v", err)
	}

	insertExecutionTargetSchemaTask(t, store.db, "task-unlocked-provisional", 1, "BLD-1", binding.WorkspaceID, stringPointerForSchemaTest("worktree-target"), nil, nil, nil, nil, nil, now)
	insertExecutionTargetSchemaTask(t, store.db, "task-none", 2, "BLD-2", binding.WorkspaceID, nil, stringPointerForSchemaTest("none"), nil, nil, nil, stringPointerForSchemaTest("resolved"), now)
	insertExecutionTargetSchemaTask(t, store.db, "task-managed-missing-relation", 3, "BLD-3", binding.WorkspaceID, nil, stringPointerForSchemaTest("head"), stringPointerForSchemaTest("HEAD"), nil, stringPointerForSchemaTest("commit-1"), stringPointerForSchemaTest("resolved"), now)
	insertExecutionTargetSchemaTask(t, store.db, "task-default-branch", 4, "BLD-4", binding.WorkspaceID, nil, stringPointerForSchemaTest("default_branch"), stringPointerForSchemaTest("refs/remotes/origin/HEAD"), stringPointerForSchemaTest("refs/remotes/origin/main"), stringPointerForSchemaTest("commit-4"), stringPointerForSchemaTest("resolved"), now)
	insertExecutionTargetSchemaTask(t, store.db, "task-custom-ref", 5, "BLD-5", binding.WorkspaceID, nil, stringPointerForSchemaTest("custom_ref"), stringPointerForSchemaTest("release/v1"), stringPointerForSchemaTest("refs/tags/release/v1"), stringPointerForSchemaTest("commit-5"), stringPointerForSchemaTest("resolved"), now)

	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_CHECK, `INSERT INTO tasks (
    id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body, source_workspace_id,
    execution_target_mode, execution_target_commit_oid, created_at_unix_ms, updated_at_unix_ms, metadata_json
) VALUES ('task-invalid-mixed', 'link-1', 1, 6, 'BLD-6', 'Task', '', ?, NULL, 'commit-1', ?, ?, '{}')`, binding.WorkspaceID, now, now)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_CHECK, `INSERT INTO tasks (
    id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body, source_workspace_id,
    managed_worktree_id, execution_target_mode, execution_target_provenance, created_at_unix_ms, updated_at_unix_ms, metadata_json
) VALUES ('task-invalid-none-worktree', 'link-1', 1, 7, 'BLD-7', 'Task', '', ?, 'worktree-target', 'none', 'resolved', ?, ?, '{}')`, binding.WorkspaceID, now, now)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_CHECK, `INSERT INTO tasks (
    id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body, source_workspace_id,
    execution_target_mode, execution_target_requested_ref, execution_target_provenance, created_at_unix_ms, updated_at_unix_ms, metadata_json
) VALUES ('task-invalid-managed-facts', 'link-1', 1, 8, 'BLD-8', 'Task', '', ?, 'head', 'HEAD', 'resolved', ?, ?, '{}')`, binding.WorkspaceID, now, now)

	insertExecutionTargetSchemaTask(t, store.db, "task-locked-deleted-worktree", 9, "BLD-9", binding.WorkspaceID, stringPointerForSchemaTest("worktree-target"), stringPointerForSchemaTest("head"), stringPointerForSchemaTest("HEAD"), stringPointerForSchemaTest("refs/heads/BLD-9"), stringPointerForSchemaTest("commit-9"), stringPointerForSchemaTest("resolved"), now)
	if _, err := store.db.Exec(`DELETE FROM worktrees WHERE id = 'worktree-target'`); err != nil {
		t.Fatalf("delete locked managed worktree: %v", err)
	}
	var deletedRelation sql.NullString
	var deletedMode string
	if err := store.db.QueryRow(`SELECT managed_worktree_id, execution_target_mode FROM tasks WHERE id = 'task-locked-deleted-worktree'`).Scan(&deletedRelation, &deletedMode); err != nil {
		t.Fatalf("read locked target after worktree delete: %v", err)
	}
	if deletedRelation.Valid || deletedMode != "head" {
		t.Fatalf("locked target after worktree delete = relation=%+v mode=%q, want null/head", deletedRelation, deletedMode)
	}
}

func TestWorktreeCreationBaseCommitOIDIsImmutable(t *testing.T) {
	t.Parallel()
	store, _, binding := newMetadataTestStore(t)
	now := time.Now().UTC().UnixMilli()
	execSeed(t, store.db, "worktree with creation base", `INSERT INTO worktrees (
    id, workspace_id, canonical_root_path, managed, created_branch, origin_session_id, git_metadata_json, creation_base_commit_oid, created_at_unix_ms, updated_at_unix_ms
) VALUES ('worktree-base', ?, ?, 1, 1, '', '{}', 'commit-created', ?, ?)`, binding.WorkspaceID, t.TempDir(), now, now)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_TRIGGER, `UPDATE worktrees SET creation_base_commit_oid = 'commit-mutated' WHERE id = 'worktree-base'`)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_TRIGGER, `UPDATE worktrees SET creation_base_commit_oid = NULL WHERE id = 'worktree-base'`)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_TRIGGER, `INSERT INTO worktrees (
    id, workspace_id, canonical_root_path, managed, created_branch, origin_session_id, git_metadata_json, creation_base_commit_oid, created_at_unix_ms, updated_at_unix_ms
) SELECT 'worktree-base-conflict', workspace_id, canonical_root_path, managed, created_branch, origin_session_id, git_metadata_json, 'commit-conflict', ?, ?
FROM worktrees
WHERE id = 'worktree-base'`, now, now)
}

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

func TestTaskShortIDUniquenessIsProjectScoped(t *testing.T) {
	t.Parallel()
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
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_TRIGGER, `INSERT INTO tasks (id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('task-dup', 'link-1', 1, 2, 'BLD-1', 'Task', 'Body', ?, ?, '{}')`, now, now)
	if _, err := store.db.Exec(`INSERT INTO tasks (id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('task-other', 'link-2', 1, 1, 'BLD-1', 'Task', 'Body', ?, ?, '{}')`, now, now); err != nil {
		t.Fatalf("same short id in another project should be allowed: %v", err)
	}
}

func TestTaskSequenceAllocationIsAtomic(t *testing.T) {
	t.Parallel()
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
	_, exists := tableColumns(t, db, table)[column]
	return exists
}

func assertExactTableColumns(t *testing.T, db *sql.DB, table string, want map[string]struct{}) {
	t.Helper()
	got := tableColumns(t, db, table)
	if len(got) != len(want) {
		t.Fatalf("%s columns = %v, want %v", table, got, want)
	}
	for column := range want {
		if _, exists := got[column]; !exists {
			t.Fatalf("%s is missing expected column %q: %v", table, column, got)
		}
	}
}

func tableColumns(t *testing.T, db *sql.DB, table string) map[string]struct{} {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatalf("table_info %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	columns := map[string]struct{}{}
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
		columns[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info %s: %v", table, err)
	}
	return columns
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

func assertSQLiteConstraint(t *testing.T, db *sql.DB, wantCode int, statement string, args ...any) {
	t.Helper()
	_, err := db.Exec(statement, args...)
	if err == nil {
		t.Fatalf("expected SQLite constraint failure for %s", statement)
	}
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		t.Fatalf("expected SQLite error for %s, got %T: %v", statement, err, err)
	}
	if got := sqliteErr.Code() & 0xff; got != sqlite3.SQLITE_CONSTRAINT {
		t.Fatalf("SQLite primary result code for %s = %d, want %d; full code = %d", statement, got, sqlite3.SQLITE_CONSTRAINT, sqliteErr.Code())
	}
	if got := sqliteErr.Code(); got != wantCode {
		t.Fatalf("SQLite extended result code for %s = %d, want %d", statement, got, wantCode)
	}
}

func seedWorkflowGraph(t *testing.T, db *sql.DB, projectID string, now int64) {
	t.Helper()
	seedWorkflowGraphForProject(t, db, projectID, now, "1")
}

func seedWorkflowGraphForProject(t *testing.T, db *sql.DB, projectID string, now int64, suffix string) {
	t.Helper()
	workflowID := workflowSeedID(t, db, suffix)
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

func workflowSeedID(t *testing.T, db *sql.DB, suffix string) any {
	t.Helper()
	workflowID := workflowTestID(t, suffix)
	rows, err := db.Query(`PRAGMA table_info(workflows)`)
	if err != nil {
		t.Fatalf("inspect workflow identity storage: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var sequence, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&sequence, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan workflow identity storage: %v", err)
		}
		if name != "id" {
			continue
		}
		switch columnType {
		case "TEXT":
			return "workflow-" + workflowID.String()
		case "BLOB":
			return workflowID
		default:
			t.Fatalf("workflows.id storage type = %q, want TEXT or BLOB", columnType)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate workflow identity storage: %v", err)
	}
	t.Fatal("workflows.id schema column is missing")
	return nil
}

func workflowTestID(t *testing.T, suffix string) runtimeids.WorkflowID {
	t.Helper()
	raw, found := map[string]string{
		"1": "550e8400-e29b-41d4-a716-446655440001",
		"2": "550e8400-e29b-41d4-a716-446655440002",
	}[suffix]
	if !found {
		t.Fatalf("unknown workflow fixture suffix %q", suffix)
	}
	workflowID, err := runtimeids.ParseWorkflowID(raw)
	if err != nil {
		t.Fatalf("parse workflow fixture ID %q: %v", raw, err)
	}
	return workflowID
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

func seedWorkflowTaskWithID(t *testing.T, store *Store, taskID string, linkID string, taskSeq int64, shortID string, _ string, _ string) {
	t.Helper()
	now := time.Now().UTC().UnixMilli()
	execSeed(t, store.db, "workflow task", workflowSeedTaskSQL, taskID, linkID, taskSeq, shortID, now, now)
}
