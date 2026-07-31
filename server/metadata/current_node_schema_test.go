package metadata

import (
	"database/sql"
	"testing"
	"time"

	sqlite3 "modernc.org/sqlite/lib"
)

func TestTaskCurrentNodeSchemaRejectsSecondSerialNode(t *testing.T) {
	t.Parallel()
	store, _ := newTaskCurrentSchemaFixture(t)
	insertTaskCurrentNode(t, store.db, "task-1", "node-start", nil)
	assertTaskCurrentNodeConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_UNIQUE, "task-1", "node-agent", nil)
}

func TestTaskCurrentNodeSchemaRejectsFanoutWhileSerialCurrentNodeExists(t *testing.T) {
	t.Parallel()
	store, _ := newTaskCurrentSchemaFixture(t)
	insertTaskCurrentNode(t, store.db, "task-1", "node-start", nil)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_TRIGGER, `INSERT INTO task_active_fanouts (task_id) VALUES ('task-1')`)
}

func TestTaskCurrentNodeSchemaRejectsSerialNodeWhileFanoutExists(t *testing.T) {
	t.Parallel()
	store, _ := newTaskCurrentSchemaFixture(t)
	insertTaskActiveFanout(t, store.db, "task-1")
	assertTaskCurrentNodeConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_TRIGGER, "task-1", "node-start", nil)
}

func TestTaskCurrentNodeSchemaRejectsParallelNodeWithoutExpectedBranch(t *testing.T) {
	t.Parallel()
	store, _ := newTaskCurrentSchemaFixture(t)
	insertTaskActiveFanout(t, store.db, "task-1")
	assertTaskCurrentNodeConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY, "task-1", "node-agent", currentSchemaBranch("implementation"))
}

func TestTaskCurrentNodeSchemaRejectsSecondParallelNodeInOneBranch(t *testing.T) {
	t.Parallel()
	store, _ := newTaskCurrentSchemaFixture(t)
	branch := currentSchemaBranch("implementation")
	insertTaskActiveFanout(t, store.db, "task-1")
	insertTaskActiveFanoutBranch(t, store.db, "task-1", *branch)
	insertTaskCurrentNode(t, store.db, "task-1", "node-start", branch)
	assertTaskCurrentNodeConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_UNIQUE, "task-1", "node-agent", branch)
}

func TestTaskCurrentNodeSchemaRejectsSecondSerialApprovalForOneSource(t *testing.T) {
	t.Parallel()
	store, now := newTaskCurrentSchemaFixture(t)
	insertTaskCurrentNode(t, store.db, "task-1", "node-start", nil)
	insertTaskPendingApproval(t, store.db, "approval-1", "task-1", "node-start", nil, now)
	assertTaskPendingApprovalConstraint(t, store.db, "approval-2", "task-1", "node-start", nil, now)
}

func TestTaskCurrentNodeSchemaRejectsSecondParallelApprovalForOneSource(t *testing.T) {
	t.Parallel()
	store, now := newTaskCurrentSchemaFixture(t)
	branch := currentSchemaBranch("implementation")
	insertTaskActiveFanout(t, store.db, "task-1")
	insertTaskActiveFanoutBranch(t, store.db, "task-1", *branch)
	insertTaskCurrentNode(t, store.db, "task-1", "node-start", branch)
	insertTaskPendingApproval(t, store.db, "approval-1", "task-1", "node-start", branch, now)
	assertTaskPendingApprovalConstraint(t, store.db, "approval-2", "task-1", "node-start", branch, now)
}

func TestTaskCurrentNodeSchemaRejectsDeletingPendingApprovalSource(t *testing.T) {
	t.Parallel()
	store, now := newTaskCurrentSchemaFixture(t)
	insertTaskCurrentNode(t, store.db, "task-1", "node-start", nil)
	insertTaskPendingApproval(t, store.db, "approval-1", "task-1", "node-start", nil, now)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_TRIGGER, `DELETE FROM task_current_nodes
WHERE task_id = 'task-1'
  AND node_id = 'node-start'
  AND transition_branch_key IS NULL`)
}

func TestTaskCurrentNodeSchemaRejectsFlattenedPriorValueUpdate(t *testing.T) {
	t.Parallel()
	store, _ := newTaskCurrentSchemaFixture(t)
	insertTaskCurrentNode(t, store.db, "task-1", "node-start", nil)
	assertSQLiteConstraint(
		t,
		store.db,
		sqlite3.SQLITE_CONSTRAINT_TRIGGER,
		`UPDATE task_current_nodes
SET prior_node_values_json = '{"shared":{"value":"ambiguous"}}'
WHERE task_id = 'task-1'`,
	)
}

func TestTaskPendingApprovalSchemaRejectsFlattenedTargetPriorValues(t *testing.T) {
	t.Parallel()
	store, now := newTaskCurrentSchemaFixture(t)
	insertTaskCurrentNode(t, store.db, "task-1", "node-start", nil)
	insertTaskPendingApproval(t, store.db, "approval-1", "task-1", "node-start", nil, now)
	assertSQLiteConstraint(
		t,
		store.db,
		sqlite3.SQLITE_CONSTRAINT_TRIGGER,
		`INSERT INTO task_pending_approval_branches (
    approval_id,
    transition_branch_key,
    target_snapshot_json,
    effective_edge_configuration_json,
    context_source_resolution_json
) VALUES (
    'approval-1',
    'done',
    '{"prior_node_values":{"shared":{"value":"ambiguous"}}}',
    '{}',
    '{}'
)`,
	)
}

func TestTaskCurrentNodeSchemaAllowsSerialParallelAggregateReplacement(t *testing.T) {
	t.Parallel()
	store, _ := newTaskCurrentSchemaFixture(t)
	insertTaskCurrentNode(t, store.db, "task-1", "node-start", nil)

	toParallel, err := store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin serial-to-parallel transaction: %v", err)
	}
	defer func() { _ = toParallel.Rollback() }()
	if _, err := toParallel.Exec(`DELETE FROM task_current_nodes
WHERE task_id = 'task-1'
  AND transition_branch_key IS NULL`); err != nil {
		t.Fatalf("remove serial current node: %v", err)
	}
	insertTaskActiveFanout(t, toParallel, "task-1")
	insertTaskActiveFanoutBranch(t, toParallel, "task-1", "implementation")
	insertTaskCurrentNode(t, toParallel, "task-1", "node-agent", currentSchemaBranch("implementation"))
	if err := toParallel.Commit(); err != nil {
		t.Fatalf("commit serial-to-parallel transaction: %v", err)
	}

	toSerial, err := store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin parallel-to-serial transaction: %v", err)
	}
	defer func() { _ = toSerial.Rollback() }()
	if _, err := toSerial.Exec(`DELETE FROM task_current_nodes
WHERE task_id = 'task-1'
  AND transition_branch_key = 'implementation'`); err != nil {
		t.Fatalf("remove parallel current node: %v", err)
	}
	if _, err := toSerial.Exec(`DELETE FROM task_active_fanouts WHERE task_id = 'task-1'`); err != nil {
		t.Fatalf("remove active fanout: %v", err)
	}
	insertTaskCurrentNode(t, toSerial, "task-1", "node-start", nil)
	if err := toSerial.Commit(); err != nil {
		t.Fatalf("commit parallel-to-serial transaction: %v", err)
	}
}

func TestTaskCurrentNodeSchemaRejectsSerialUpdateWhileFanoutExists(t *testing.T) {
	t.Parallel()
	store, _ := newTaskCurrentSchemaFixture(t)
	branch := currentSchemaBranch("implementation")
	insertTaskActiveFanout(t, store.db, "task-1")
	insertTaskActiveFanoutBranch(t, store.db, "task-1", *branch)
	insertTaskCurrentNode(t, store.db, "task-1", "node-start", branch)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_TRIGGER, `UPDATE task_current_nodes
SET transition_branch_key = NULL
WHERE task_id = 'task-1'
  AND node_id = 'node-start'
  AND transition_branch_key = 'implementation'`)
}

func TestTaskCurrentNodeSchemaRejectsNodeOutsideTaskWorkflow(t *testing.T) {
	t.Parallel()
	store, _, binding := newMetadataTestStore(t)
	now := time.Now().UTC().UnixMilli()
	seedWorkflowGraph(t, store.db, binding.ProjectID, now)
	seedWorkflowGraphForProject(t, store.db, binding.ProjectID, now, "2")
	seedWorkflowTask(t, store, binding.ProjectID, "BLD-1")
	assertTaskCurrentNodeConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_TRIGGER, "task-1", "node-start-2", nil)
}

func TestTaskCurrentNodeSchemaRejectsFanoutUpdateIntoSerialTask(t *testing.T) {
	t.Parallel()
	store, _, binding := newMetadataTestStore(t)
	now := time.Now().UTC().UnixMilli()
	seedWorkflowGraph(t, store.db, binding.ProjectID, now)
	seedWorkflowGraphForProject(t, store.db, binding.ProjectID, now, "2")
	seedWorkflowTask(t, store, binding.ProjectID, "BLD-1")
	seedWorkflowTaskWithID(t, store, "task-2", "link-2", 2, "BLD-2", "placement-start-2", "node-start-2")

	insertTaskActiveFanout(t, store.db, "task-1")
	insertTaskCurrentNode(t, store.db, "task-2", "node-start-2", nil)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_TRIGGER, `UPDATE task_active_fanouts
SET task_id = 'task-2'
WHERE task_id = 'task-1'`)
}

func TestTaskCurrentNodeSchemaUsesNaturalReferencesAndLeanFanoutStorage(t *testing.T) {
	t.Parallel()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	for _, table := range []string{
		"task_current_nodes",
		"task_active_fanouts",
		"task_active_fanout_branches",
		"task_pending_approval_branches",
	} {
		if columnExists(t, store.db, table, "id") {
			t.Fatalf("%s.id should not exist; the relation has a natural identity", table)
		}
	}
	assertExactTableColumns(t, store.db, "task_active_fanouts", map[string]struct{}{
		"task_id": {},
	})
	assertExactTableColumns(t, store.db, "task_active_fanout_branches", map[string]struct{}{
		"task_id":               {},
		"transition_branch_key": {},
		"arrival_state":         {},
		"arrival_values_json":   {},
	})
}

type currentNodeSchemaSQLExecutor interface {
	Exec(string, ...any) (sql.Result, error)
}

const insertTaskCurrentNodeSQL = `INSERT INTO task_current_nodes (
    task_id,
    node_id,
    transition_branch_key,
    current_input_values_json,
    prior_node_values_json
) VALUES (?, ?, ?, '{}', '{"transition_parameters":{}}')`

const insertTaskPendingApprovalSQL = `INSERT INTO task_pending_approvals (
    id,
    source_task_id,
    source_node_id,
    source_transition_branch_key,
    workflow_version,
    transition_snapshot_json,
    materialized_values_json,
    created_at_unix_ms
) VALUES (?, ?, ?, ?, 1, '{}', '{}', ?)`

func newTaskCurrentSchemaFixture(t *testing.T) (*Store, int64) {
	t.Helper()
	store, _, binding := newMetadataTestStore(t)
	now := time.Now().UTC().UnixMilli()
	seedWorkflowGraph(t, store.db, binding.ProjectID, now)
	seedWorkflowTask(t, store, binding.ProjectID, "BLD-1")
	return store, now
}

func currentSchemaBranch(value string) *string {
	return &value
}

func insertTaskCurrentNode(t *testing.T, executor currentNodeSchemaSQLExecutor, taskID, nodeID string, branch *string) {
	t.Helper()
	if _, err := executor.Exec(insertTaskCurrentNodeSQL, taskCurrentNodeArgs(taskID, nodeID, branch)...); err != nil {
		t.Fatalf("insert current node %s/%s: %v", taskID, nodeID, err)
	}
}

func assertTaskCurrentNodeConstraint(t *testing.T, db *sql.DB, wantCode int, taskID, nodeID string, branch *string) {
	t.Helper()
	assertSQLiteConstraint(t, db, wantCode, insertTaskCurrentNodeSQL, taskCurrentNodeArgs(taskID, nodeID, branch)...)
}

func taskCurrentNodeArgs(taskID, nodeID string, branch *string) []any {
	return []any{taskID, nodeID, nullableCurrentNodeSchemaBranch(branch)}
}

func insertTaskActiveFanout(t *testing.T, executor currentNodeSchemaSQLExecutor, taskID string) {
	t.Helper()
	if _, err := executor.Exec(`INSERT INTO task_active_fanouts (task_id) VALUES (?)`, taskID); err != nil {
		t.Fatalf("insert active fanout for %s: %v", taskID, err)
	}
}

func insertTaskActiveFanoutBranch(t *testing.T, executor currentNodeSchemaSQLExecutor, taskID, branch string) {
	t.Helper()
	if _, err := executor.Exec(`INSERT INTO task_active_fanout_branches (
    task_id,
    transition_branch_key,
    arrival_state
) VALUES (?, ?, 'pending')`, taskID, branch); err != nil {
		t.Fatalf("insert active fanout branch %s/%s: %v", taskID, branch, err)
	}
}

func insertTaskPendingApproval(t *testing.T, executor currentNodeSchemaSQLExecutor, approvalID, taskID, nodeID string, branch *string, now int64) {
	t.Helper()
	if _, err := executor.Exec(insertTaskPendingApprovalSQL, taskPendingApprovalArgs(approvalID, taskID, nodeID, branch, now)...); err != nil {
		t.Fatalf("insert pending approval %s: %v", approvalID, err)
	}
}

func assertTaskPendingApprovalConstraint(t *testing.T, db *sql.DB, approvalID, taskID, nodeID string, branch *string, now int64) {
	t.Helper()
	assertSQLiteConstraint(t, db, sqlite3.SQLITE_CONSTRAINT_UNIQUE, insertTaskPendingApprovalSQL, taskPendingApprovalArgs(approvalID, taskID, nodeID, branch, now)...)
}

func taskPendingApprovalArgs(approvalID, taskID, nodeID string, branch *string, now int64) []any {
	return []any{approvalID, taskID, nodeID, nullableCurrentNodeSchemaBranch(branch), now}
}

func nullableCurrentNodeSchemaBranch(branch *string) any {
	if branch != nil {
		return *branch
	}
	return nil
}
