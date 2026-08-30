package metadata

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"core/server/metadata/sqlitegen"
	"core/server/session"
	"core/shared/runtimeids"
)

func TestDeleteSessionUsesCurrentRetentionAuthorityAndPreservesHistory(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*testing.T, *sessionDeletionFixture)
		blocked bool
		verify  func(*testing.T, *sessionDeletionFixture)
	}{
		{
			name: "serial Current Node",
			setup: func(t *testing.T, fixture *sessionDeletionFixture) {
				insertTaskCurrentNode(t, fixture.store.db, "task-1", "node-agent", nil)
				fixture.bindCurrentNodeSession(t, nil)
			},
			blocked: true,
		},
		{
			name: "branch Current Node",
			setup: func(t *testing.T, fixture *sessionDeletionFixture) {
				insertTaskActiveFanout(t, fixture.store.db, "task-1")
				insertTaskActiveFanoutBranch(t, fixture.store.db, "task-1", "implementation")
				branch := currentSchemaBranch("implementation")
				insertTaskCurrentNode(t, fixture.store.db, "task-1", "node-agent", branch)
				fixture.bindCurrentNodeSession(t, branch)
			},
			blocked: true,
		},
		{
			name: "pending Approval source",
			setup: func(t *testing.T, fixture *sessionDeletionFixture) {
				fixture.insertPendingApproval(t, fixture.targetSessionID, "")
			},
			blocked: true,
		},
		{
			name: "pending Approval reused target",
			setup: func(t *testing.T, fixture *sessionDeletionFixture) {
				fixture.insertPendingApproval(
					t,
					"",
					fmt.Sprintf(
						`{"target_session":{"kind":"reuse","session_id":%q},"active_source":{"kind":"absent"}}`,
						fixture.targetSessionID,
					),
				)
			},
			blocked: true,
		},
		{
			name: "pending Approval exact active source",
			setup: func(t *testing.T, fixture *sessionDeletionFixture) {
				fixture.insertPendingApproval(
					t,
					"",
					fmt.Sprintf(
						`{"target_session":{"kind":"create"},"active_source":{"kind":"exact","session_id":%q}}`,
						fixture.targetSessionID,
					),
				)
			},
			blocked: true,
		},
		{
			name: "legacy pending Approval snapshot",
			setup: func(t *testing.T, fixture *sessionDeletionFixture) {
				fixture.insertPendingApproval(
					t,
					"",
					fmt.Sprintf(`{"session_id":%q}`, fixture.targetSessionID),
				)
			},
			blocked: true,
		},
		{
			name: "dormant association",
			setup: func(t *testing.T, fixture *sessionDeletionFixture) {
				insertTaskCurrentNode(t, fixture.store.db, "task-1", "node-start", nil)
				fixture.bindSessionToTask(t, fixture.targetSessionID)
				fixture.insertAssociation(t, fixture.targetSessionID, "node-agent", fixture.now)
			},
			verify: func(t *testing.T, fixture *sessionDeletionFixture) {
				_, err := fixture.store.queries.GetLatestSerialTaskSessionAssociationForNode(
					t.Context(),
					sqlitegen.GetLatestSerialTaskSessionAssociationForNodeParams{
						TaskID: sql.NullString{String: "task-1", Valid: true},
						NodeID: workflowGraphSeedID(t, fixture.store.db, "node-agent").(string),
					},
				)
				if !errors.Is(err, sql.ErrNoRows) {
					t.Fatalf("latest association after deletion = %v, want no match", err)
				}
			},
		},
		{
			name: "terminal Task and historical provenance",
			setup: func(t *testing.T, fixture *sessionDeletionFixture) {
				insertTaskCurrentNode(t, fixture.store.db, "task-1", "node-done", nil)
				fixture.bindCurrentNodeSession(t, nil)
				fixture.bindSessionToTask(t, fixture.targetSessionID)
				fixture.bindSessionToTask(t, fixture.survivingSessionID)
				fixture.insertAssociation(t, fixture.targetSessionID, "node-agent", fixture.now)
				fixture.insertAssociation(t, fixture.survivingSessionID, "node-agent", fixture.now+1)
				execSeed(t, fixture.store.db, "Session prompt history", `INSERT INTO session_prompt_history_entries (
    session_id, text, created_at_unix_ms
) VALUES (?, 'prompt', ?)`, fixture.targetSessionID, fixture.now)
				execSeed(t, fixture.store.db, "surviving Session lineage", `UPDATE sessions
SET previous_session_id = ?, parent_agent_session_id = ?
WHERE id = ?`, fixture.targetSessionID, fixture.targetSessionID, fixture.survivingSessionID)
				execSeed(t, fixture.store.db, "Worktree origin", `INSERT INTO worktrees (
    id, workspace_id, canonical_root_path, managed, created_branch,
    origin_session_id, git_metadata_json, created_at_unix_ms, updated_at_unix_ms
) VALUES ('worktree-1', ?, ?, 1, 1, ?, '{}', ?, ?)`,
					fixture.workspaceID,
					filepath.Join(t.TempDir(), "worktree"),
					fixture.targetSessionID,
					fixture.now,
					fixture.now,
				)
			},
			verify: func(t *testing.T, fixture *sessionDeletionFixture) {
				var currentSession sql.NullString
				if err := fixture.store.db.QueryRow(`SELECT session_id
FROM task_current_nodes
WHERE task_id = 'task-1'`).Scan(&currentSession); err != nil {
					t.Fatalf("read surviving Current Node: %v", err)
				}
				if currentSession.Valid {
					t.Fatalf("terminal Current Node Session = %v, want null", currentSession)
				}
				assertMetadataRowCount(t, fixture.store.db, "tasks", "id", "task-1", 1)
				assertMetadataRowCount(t, fixture.store.db, "session_prompt_history_entries", "session_id", fixture.targetSessionID, 0)
				assertMetadataRowCount(t, fixture.store.db, "session_workflow_node_associations", "session_id", fixture.targetSessionID, 0)
				assertMetadataRowCount(t, fixture.store.db, "session_workflow_node_associations", "session_id", fixture.survivingSessionID, 1)

				var previousSessionID, parentAgentSessionID, originSessionID string
				if err := fixture.store.db.QueryRow(`SELECT previous_session_id, parent_agent_session_id
FROM sessions
WHERE id = ?`, fixture.survivingSessionID).Scan(&previousSessionID, &parentAgentSessionID); err != nil {
					t.Fatalf("read surviving lineage: %v", err)
				}
				if err := fixture.store.db.QueryRow(`SELECT origin_session_id
FROM worktrees
WHERE id = 'worktree-1'`).Scan(&originSessionID); err != nil {
					t.Fatalf("read surviving Worktree origin: %v", err)
				}
				if previousSessionID != fixture.targetSessionID ||
					parentAgentSessionID != fixture.targetSessionID ||
					originSessionID != fixture.targetSessionID {
					t.Fatalf(
						"surviving provenance = %q/%q/%q, want %q",
						previousSessionID,
						parentAgentSessionID,
						originSessionID,
						fixture.targetSessionID,
					)
				}
			},
		},
		{
			name: "missing Session",
			setup: func(t *testing.T, fixture *sessionDeletionFixture) {
				fixture.targetSessionID = runtimeids.NewSessionID().String()
			},
			verify: func(t *testing.T, fixture *sessionDeletionFixture) {
				t.Helper()
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSessionDeletionFixture(t)
			test.setup(t, fixture)

			err := fixture.store.DeleteSession(t.Context(), fixture.targetSessionID)
			if test.name == "missing Session" {
				if !errors.Is(err, session.ErrSessionNotFound) {
					t.Fatalf("DeleteSession error = %v, want Session not found", err)
				}
				return
			}
			if test.blocked {
				var inUse *SessionInUseError
				if !errors.As(err, &inUse) || inUse.SessionID != fixture.targetSessionID {
					t.Fatalf("DeleteSession error = %v, want SessionInUseError for %q", err, fixture.targetSessionID)
				}
				assertMetadataRowCount(t, fixture.store.db, "sessions", "id", fixture.targetSessionID, 1)
				return
			}
			if err != nil {
				t.Fatalf("DeleteSession: %v", err)
			}
			assertMetadataRowCount(t, fixture.store.db, "sessions", "id", fixture.targetSessionID, 0)
			if test.verify != nil {
				test.verify(t, fixture)
			}
		})
	}
}

type sessionDeletionFixture struct {
	store              *Store
	workspaceID        string
	targetSessionID    string
	survivingSessionID string
	now                int64
}

func newSessionDeletionFixture(t *testing.T) *sessionDeletionFixture {
	t.Helper()
	store, _, binding := newMetadataTestStore(t)
	now := time.Now().UTC().UnixMilli()
	seedWorkflowGraph(t, store.db, binding.ProjectID, now)
	seedWorkflowTask(t, store, binding.ProjectID, "BLD-1")
	targetSessionID := runtimeids.NewSessionID().String()
	survivingSessionID := runtimeids.NewSessionID().String()
	insertSessionDeletionRecord(t, store, binding, targetSessionID, now)
	insertSessionDeletionRecord(t, store, binding, survivingSessionID, now+1)
	return &sessionDeletionFixture{
		store:              store,
		workspaceID:        binding.WorkspaceID,
		targetSessionID:    targetSessionID,
		survivingSessionID: survivingSessionID,
		now:                now,
	}
}

func insertSessionDeletionRecord(t *testing.T, store *Store, binding Binding, sessionID string, now int64) {
	t.Helper()
	execSeed(t, store.db, "Session", `INSERT INTO sessions (
    id, project_id, workspace_id, artifact_relpath, created_at_unix_ms, updated_at_unix_ms, metadata_json
) VALUES (?, ?, ?, ?, ?, ?, '{}')`,
		sessionID,
		binding.ProjectID,
		binding.WorkspaceID,
		filepath.ToSlash(filepath.Join("projects", binding.ProjectID, "sessions", sessionID)),
		now,
		now,
	)
}

func (fixture *sessionDeletionFixture) bindCurrentNodeSession(t *testing.T, branch *string) {
	t.Helper()
	_, err := fixture.store.db.Exec(`UPDATE task_current_nodes
SET session_id = ?
WHERE task_id = 'task-1'
  AND (
      (transition_branch_key IS NULL AND ? IS NULL)
      OR transition_branch_key = ?
  )`,
		fixture.targetSessionID,
		nullableCurrentNodeSchemaBranch(branch),
		nullableCurrentNodeSchemaBranch(branch),
	)
	if err != nil {
		t.Fatalf("bind Current Node Session: %v", err)
	}
}

func (fixture *sessionDeletionFixture) bindSessionToTask(t *testing.T, sessionID string) {
	t.Helper()
	if _, err := fixture.store.db.Exec(`UPDATE sessions SET task_id = 'task-1' WHERE id = ?`, sessionID); err != nil {
		t.Fatalf("bind Session %s to Task: %v", sessionID, err)
	}
}

func (fixture *sessionDeletionFixture) insertAssociation(t *testing.T, sessionID, nodeID string, associatedAt int64) {
	t.Helper()
	execSeed(t, fixture.store.db, "Session association", `INSERT INTO session_workflow_node_associations (
    session_id, node_id, transition_branch_key, associated_at_unix_ms
) VALUES (?, ?, NULL, ?)`, sessionID, workflowGraphSeedID(t, fixture.store.db, nodeID), associatedAt)
}

func (fixture *sessionDeletionFixture) insertPendingApproval(t *testing.T, sourceSessionID, contextSourceResolutionJSON string) {
	t.Helper()
	insertTaskCurrentNode(t, fixture.store.db, "task-1", "node-agent", nil)
	var sourceSession any
	if sourceSessionID != "" {
		sourceSession = sourceSessionID
	}
	execSeed(t, fixture.store.db, "pending Approval", `INSERT INTO task_pending_approvals (
    id, source_task_id, source_node_id, source_transition_branch_key, source_session_id,
    workflow_version, transition_snapshot_json, materialized_values_json, created_at_unix_ms
) VALUES ('approval-1', 'task-1', ?, NULL, ?, 1, '{}', '{}', ?)`,
		workflowGraphSeedID(t, fixture.store.db, "node-agent"),
		sourceSession,
		fixture.now,
	)
	if contextSourceResolutionJSON == "" {
		return
	}
	execSeed(t, fixture.store.db, "pending Approval branch", `INSERT INTO task_pending_approval_branches (
    approval_id, transition_branch_key, target_snapshot_json,
    effective_edge_configuration_json, context_source_resolution_json
) VALUES (
    'approval-1', 'approved',
    '{"prior_values":{"transition_parameters":{}}}',
    '{}',
    ?
)`, contextSourceResolutionJSON)
}

func assertMetadataRowCount(t *testing.T, db *sql.DB, table, column, value string, want int) {
	t.Helper()
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s = ?", table, column)
	var got int
	if err := db.QueryRow(query, value).Scan(&got); err != nil {
		t.Fatalf("count %s rows: %v", table, err)
	}
	if got != want {
		t.Fatalf("%s rows for %q = %d, want %d", table, value, got, want)
	}
}
