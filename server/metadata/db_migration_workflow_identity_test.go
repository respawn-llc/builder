package metadata

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

const (
	workflowIdentityMigrationPrefixed = "workflow-7e8d24d2-8a98-4dcf-a197-6214db1cb3c0"
	workflowIdentityMigrationBare     = "8d3f0b91-1a10-4d2c-9f77-2e5a4c6b8d90"
)

func TestWorkflowIdentityMigrationConvertsRelationalIDsAndSnapshotsAtomically(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 60)
	if err != nil {
		t.Fatalf("open version 60 database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	seedWorkflowIdentityMigrationFixture(t, db, false)
	sentinel := []byte("{\"event\":\"must remain byte-for-byte unchanged\"}\n")
	eventLogPath := filepath.Join(root, "events.jsonl")
	if err := os.WriteFile(eventLogPath, sentinel, 0o600); err != nil {
		t.Fatalf("write sentinel event log: %v", err)
	}

	provider, err := newMetadataMigrationProvider(db)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	if _, err := provider.Up(context.Background()); err != nil {
		t.Fatalf("apply workflow identity migration: %v", err)
	}

	for _, table := range []string{"workflows", "project_workflow_links", "workflow_node_groups", "workflow_nodes"} {
		var storageClass string
		var length int
		column := "workflow_id"
		if table == "workflows" {
			column = "id"
		}
		err := db.QueryRow(fmt.Sprintf(`SELECT typeof(%s), length(%s) FROM %s LIMIT 1`, column, column, table)).Scan(&storageClass, &length)
		if err != nil {
			t.Fatalf("query %s identity: %v", table, err)
		}
		if storageClass != "blob" || length != 16 {
			t.Fatalf("%s identity = %s/%d, want blob/16", table, storageClass, length)
		}
	}

	expectedPrefixed := uuid.MustParse("7e8d24d2-8a98-4dcf-a197-6214db1cb3c0")
	var workflowBytes []byte
	if err := db.QueryRow(`SELECT id FROM workflows WHERE name = 'Prefixed'`).Scan(&workflowBytes); err != nil {
		t.Fatalf("read converted workflow: %v", err)
	}
	if !bytes.Equal(workflowBytes, expectedPrefixed[:]) {
		t.Fatalf("converted workflow bytes = %x, want %x", workflowBytes, expectedPrefixed[:])
	}

	var transitionSnapshot, edgeSnapshot string
	if err := db.QueryRow(`
		SELECT approval.transition_snapshot_json, branch.effective_edge_configuration_json
		FROM task_pending_approvals approval
		JOIN task_pending_approval_branches branch ON branch.approval_id = approval.id
		WHERE approval.id = 'approval-a'
	`).Scan(&transitionSnapshot, &edgeSnapshot); err != nil {
		t.Fatalf("read converted snapshots: %v", err)
	}
	var transitionIdentity struct {
		WorkflowID string `json:"workflow_id"`
	}
	if err := json.Unmarshal([]byte(transitionSnapshot), &transitionIdentity); err != nil {
		t.Fatalf("decode transition snapshot: %v", err)
	}
	var edgeIdentity struct {
		WorkflowID string `json:"workflow_id"`
	}
	if err := json.Unmarshal([]byte(edgeSnapshot), &edgeIdentity); err != nil {
		t.Fatalf("decode edge snapshot: %v", err)
	}
	const expectedWorkflowID = "7e8d24d2-8a98-4dcf-a197-6214db1cb3c0"
	if transitionIdentity.WorkflowID != expectedWorkflowID || edgeIdentity.WorkflowID != expectedWorkflowID {
		t.Fatalf("migrated snapshot Workflow IDs = %q / %q, want %q", transitionIdentity.WorkflowID, edgeIdentity.WorkflowID, expectedWorkflowID)
	}

	var foreignKeyViolations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&foreignKeyViolations); err != nil {
		t.Fatalf("foreign key check: %v", err)
	}
	if foreignKeyViolations != 0 {
		t.Fatalf("foreign key violations = %d, want 0", foreignKeyViolations)
	}
	var migratedDefaultWorkflowLink sql.NullString
	if err := db.QueryRow(`SELECT default_project_workflow_link_id FROM projects WHERE id = 'project-b'`).Scan(&migratedDefaultWorkflowLink); err != nil {
		t.Fatalf("read migrated default workflow link: %v", err)
	}
	if migratedDefaultWorkflowLink.Valid {
		t.Fatalf("migrated default workflow link = %q, want SQL NULL", migratedDefaultWorkflowLink.String)
	}
	if _, err := db.Exec(`UPDATE projects SET default_project_workflow_link_id = 'link-b' WHERE id = 'project-b'`); err != nil {
		t.Fatalf("set default workflow link before deletion: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM project_workflow_links WHERE id = 'link-b'`); err != nil {
		t.Fatalf("delete default workflow link: %v", err)
	}
	var defaultWorkflowLink sql.NullString
	if err := db.QueryRow(`SELECT default_project_workflow_link_id FROM projects WHERE id = 'project-b'`).Scan(&defaultWorkflowLink); err != nil {
		t.Fatalf("read deleted default workflow link: %v", err)
	}
	if defaultWorkflowLink.Valid {
		t.Fatalf("deleted default workflow link = %q, want SQL NULL", defaultWorkflowLink.String)
	}
	gotSentinel, err := os.ReadFile(eventLogPath)
	if err != nil {
		t.Fatalf("read sentinel event log: %v", err)
	}
	if !bytes.Equal(gotSentinel, sentinel) {
		t.Fatal("workflow identity migration changed the sentinel event log")
	}
}

func TestWorkflowIdentityMigrationRejectsMalformedIdentityWithoutMutation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 60)
	if err != nil {
		t.Fatalf("open version 60 database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	seedWorkflowIdentityMigrationFixture(t, db, true)

	provider, err := newMetadataMigrationProvider(db)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	if _, err := provider.Up(context.Background()); err == nil {
		t.Fatal("malformed Workflow identity migration succeeded")
	}

	var storageClass, workflowID string
	if err := db.QueryRow(`SELECT typeof(id), id FROM workflows WHERE name = 'Prefixed'`).Scan(&storageClass, &workflowID); err != nil {
		t.Fatalf("read unchanged malformed workflow: %v", err)
	}
	if storageClass != "text" || workflowID != "workflow-not-a-uuid" {
		t.Fatalf("malformed workflow after failed migration = %s/%q, want text/%q", storageClass, workflowID, "workflow-not-a-uuid")
	}
	var version int64
	if err := db.QueryRow(`SELECT MAX(version_id) FROM goose_db_version WHERE is_applied = 1`).Scan(&version); err != nil {
		t.Fatalf("read migration version after failure: %v", err)
	}
	if version != 61 {
		t.Fatalf("migration version after failure = %d, want 61", version)
	}
}

func TestOpenRepairsWorkflowIdentityMigrationVersionCollision(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 60)
	if err != nil {
		t.Fatalf("open version 60 database: %v", err)
	}
	seedWorkflowIdentityMigrationFixture(t, db, false)
	provider, err := newMetadataMigrationProvider(db)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	if _, err := provider.UpTo(t.Context(), 61); err != nil {
		t.Fatalf("advance legacy database to version 61: %v", err)
	}
	for _, version := range []int64{62, 63} {
		if _, err := db.Exec(
			`INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, 1)`,
			version,
		); err != nil {
			t.Fatalf("record collided migration version %d: %v", version, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close collided database: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open collided database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	summaries, err := store.ListProjectHomeSummaries(t.Context(), "", 10, 0)
	if err != nil {
		t.Fatalf("list project home summaries after repair: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("project home summary count = %d, want 2", len(summaries))
	}

	var workflowIdentityStorage string
	if err := store.db.QueryRow(`SELECT typeof(id) FROM workflows LIMIT 1`).Scan(&workflowIdentityStorage); err != nil {
		t.Fatalf("read repaired workflow identity storage: %v", err)
	}
	if workflowIdentityStorage != "blob" {
		t.Fatalf("repaired workflow identity storage = %q, want blob", workflowIdentityStorage)
	}
}

func seedWorkflowIdentityMigrationFixture(t *testing.T, db *sql.DB, malformed bool) {
	t.Helper()
	workflowA := workflowIdentityMigrationPrefixed
	if malformed {
		workflowA = "workflow-not-a-uuid"
	}
	const workflowB = workflowIdentityMigrationBare
	statements := []string{
		`INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json, project_key, next_task_seq, default_project_workflow_link_id, primary_workspace_id) VALUES ('project-a', 'Project A', 1, 1, '{}', '', 1, '', '')`,
		`INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json, project_key, next_task_seq, default_project_workflow_link_id, primary_workspace_id) VALUES ('project-b', 'Project B', 1, 1, '{}', '', 1, '', '')`,
		`INSERT INTO workflows (id, name, description, version, execution_target_policy, execution_target_custom_ref, created_at_unix_ms, updated_at_unix_ms) VALUES (?, 'Prefixed', '', 1, 'head', NULL, 1, 1)`,
		`INSERT INTO workflows (id, name, description, version, execution_target_policy, execution_target_custom_ref, created_at_unix_ms, updated_at_unix_ms) VALUES (?, 'Bare', '', 1, 'head', NULL, 1, 1)`,
		`INSERT INTO project_workflow_links (id, project_id, workflow_id, created_at_unix_ms, updated_at_unix_ms) VALUES ('link-a', 'project-a', ?, 1, 1)`,
		`INSERT INTO project_workflow_links (id, project_id, workflow_id, created_at_unix_ms, updated_at_unix_ms) VALUES ('link-b', 'project-b', ?, 1, 1)`,
		`INSERT INTO workflow_node_groups (id, workflow_id, group_key, display_name, sort_order) VALUES ('group-a', ?, 'group_a', 'Group A', 0)`,
		`INSERT INTO workflow_nodes (id, workflow_id, node_key, kind, display_name, subagent_role, prompt_template, output_fields_json, group_id, sort_order, input_fields_json, join_input_providers_json, completion_mode, script_path) VALUES ('node-a', ?, 'start', 'start', 'Start', '', '', '[]', NULL, 0, '[]', '[]', '', NULL)`,
		`INSERT INTO tasks (id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body, source_url, source_workspace_id, managed_worktree_id, execution_target_mode, execution_target_requested_ref, execution_target_resolved_ref, execution_target_commit_oid, execution_target_provenance, created_at_unix_ms, updated_at_unix_ms, metadata_json) VALUES ('task-a', 'link-a', 1, 1, 'PRJ-1', 'Task', '', '', NULL, NULL, NULL, NULL, NULL, NULL, NULL, 1, 1, '{}')`,
		`INSERT INTO task_current_nodes (task_id, node_id, transition_branch_key, current_input_values_json, prior_node_values_json, session_id, scheduling_state, interruption_reason, interruption_detail_json, interrupted_at_unix_ms, entered_by_edge_id) VALUES ('task-a', 'node-a', NULL, '{}', '{}', NULL, NULL, NULL, NULL, NULL, NULL)`,
	}
	args := [][]any{
		nil,
		nil,
		{workflowA},
		{workflowB},
		{workflowA},
		{workflowB},
		{workflowA},
		{workflowA},
		nil,
		nil,
	}
	for index, statement := range statements {
		if _, err := db.Exec(statement, args[index]...); err != nil {
			t.Fatalf("seed statement %d: %v", index, err)
		}
	}
	if malformed {
		return
	}
	transitionSnapshot := `{"workflow_id":"` + workflowA + `","id":"group-a","source_node_id":"node-a","transition_id":"continue","display_name":"Continue","description":"","source_display_name":"Start"}`
	effectiveEdgeSnapshot := `{"workflow_id":"` + workflowA + `","id":"edge-a","key":"continue","transition_group_id":"group-a","target_node_id":"node-a","context_mode":"new_session","context_source":{"kind":"immediate_source"},"requires_approval":false,"prompt_template":"","parameters":[],"input_bindings":[],"output_requirements":[]}`
	if _, err := db.Exec(`INSERT INTO task_pending_approvals (id, source_task_id, source_node_id, source_transition_branch_key, source_session_id, workflow_version, transition_snapshot_json, materialized_values_json, created_at_unix_ms) VALUES ('approval-a', 'task-a', 'node-a', NULL, NULL, 1, ?, '{}', 1)`, transitionSnapshot); err != nil {
		t.Fatalf("seed pending approval: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO task_pending_approval_branches (approval_id, transition_branch_key, target_snapshot_json, effective_edge_configuration_json, context_source_resolution_json) VALUES ('approval-a', 'continue', ?, ?, '{}')`, `{"node_id":"node-a"}`, effectiveEdgeSnapshot); err != nil {
		t.Fatalf("seed pending approval branch: %v", err)
	}
}
