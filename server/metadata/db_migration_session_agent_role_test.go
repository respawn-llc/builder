package metadata

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestOpenBackfillsLegacyWorkflowSessionAgentRoleAndRotatesContract(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 61)
	if err != nil {
		t.Fatalf("open version 61 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-session-role-migration', 'Project', ?, ?, '{}')`, now, now)
	seedLegacyWorkflowSession(t, db, "project-session-role-migration", "workspace-session-role-migration", "session-latest-role", now)
	seedWorkflowGraph(t, db, "project-session-role-migration", now)
	workflowID := workflowSeedID(t, db, "1")
	execSeed(t, db, "review node", `
INSERT INTO workflow_nodes (id, workflow_id, node_key, kind, display_name, subagent_role)
VALUES ('node-review', ?, 'review', 'agent', 'Review', ' Reviewer ')`, workflowID)
	execSeed(t, db, "default node", `
INSERT INTO workflow_nodes (id, workflow_id, node_key, kind, display_name, subagent_role)
VALUES ('node-default', ?, 'default_agent', 'agent', 'Default Agent', 'default')`, workflowID)
	execSeed(t, db, "blank role node", `
INSERT INTO workflow_nodes (id, workflow_id, node_key, kind, display_name, subagent_role)
VALUES ('node-blank-role', ?, 'blank_role_agent', 'agent', 'Blank Role Agent', '')`, workflowID)
	execSeed(t, db, "agent roles", `
UPDATE workflow_nodes SET subagent_role = 'coder_low' WHERE id = 'node-agent'`)
	execSeed(t, db, "task", workflowSeedTaskSQL, "task-session-role-migration", "link-1", 1, "ROLE-1", now, now)

	for index, fixture := range []struct {
		id           string
		continuation string
		locked       string
		metadata     string
		nodeID       string
		associatedAt int64
	}{
		{id: "session-existing-role", continuation: `{"agent_role":"coder_low","keep":"existing"}`, locked: `{"model":"locked-existing"}`, metadata: `{"prompt_cache_lineage_generation":4,"keep":"existing"}`, nodeID: "node-review", associatedAt: now + 2},
		{id: "session-default-role", continuation: `{"openai_base_url":"https://default.example/v1"}`, locked: `{"model":"locked-default"}`, metadata: `{"keep":"default"}`, nodeID: "node-default", associatedAt: now + 3},
		{id: "session-blank-role", continuation: `{"keep":"blank"}`, locked: `{"model":"locked-blank"}`, metadata: `{"prompt_cache_lineage_generation":9,"keep":"blank"}`, nodeID: "node-blank-role", associatedAt: now + 4},
		{id: "session-unassociated", continuation: `{"keep":"unassociated"}`, locked: `{"model":"locked-unassociated"}`, metadata: `{"prompt_cache_lineage_generation":7}`, nodeID: "", associatedAt: 0},
		{id: "session-non-agent", continuation: `{"keep":"non-agent"}`, locked: `{"model":"locked-non-agent"}`, metadata: `{"prompt_cache_lineage_generation":8}`, nodeID: "node-start", associatedAt: now + 5},
	} {
		seedLegacyWorkflowSession(
			t,
			db,
			"project-session-role-migration",
			"workspace-"+fixture.id,
			fixture.id,
			now+int64(index)+1,
		)
		execSeed(t, db, "session migration fixture", `
UPDATE sessions
SET task_id = 'task-session-role-migration',
    continuation_json = ?,
    locked_json = ?,
    metadata_json = ?
WHERE id = ?`, fixture.continuation, fixture.locked, fixture.metadata, fixture.id)
		if fixture.nodeID != "" {
			execSeed(t, db, "session node association", `
INSERT INTO session_workflow_node_associations (
    session_id, node_id, transition_branch_key, associated_at_unix_ms
) VALUES (?, ?, NULL, ?)`, fixture.id, fixture.nodeID, fixture.associatedAt)
		}
	}
	execSeed(t, db, "latest role session", `
UPDATE sessions
SET task_id = 'task-session-role-migration',
    continuation_json = '{"openai_base_url":"https://legacy.example/v1","keep":"latest"}',
    locked_json = '{"model":"locked-latest"}',
    metadata_json = '{"prompt_cache_lineage_generation":2,"keep":"latest"}'
WHERE id = 'session-latest-role'`)
	execSeed(t, db, "older agent association", `
INSERT INTO session_workflow_node_associations (
    session_id, node_id, transition_branch_key, associated_at_unix_ms
) VALUES ('session-latest-role', 'node-agent', NULL, ?)`, now+5)
	execSeed(t, db, "latest agent association", `
INSERT INTO session_workflow_node_associations (
    session_id, node_id, transition_branch_key, associated_at_unix_ms
) VALUES ('session-latest-role', 'node-review', NULL, ?)`, now+6)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 61 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	assertMigratedSessionJSON(t, store.db, "session-latest-role", map[string]any{
		"continuation": map[string]any{"agent_role": "reviewer", "openai_base_url": "https://legacy.example/v1", "keep": "latest"},
		"locked":       map[string]any{},
		"metadata":     map[string]any{"prompt_cache_lineage_generation": float64(3), "keep": "latest"},
	})
	assertMigratedSessionJSON(t, store.db, "session-default-role", map[string]any{
		"continuation": map[string]any{"openai_base_url": "https://default.example/v1"},
		"locked":       map[string]any{},
		"metadata":     map[string]any{"prompt_cache_lineage_generation": float64(1), "keep": "default"},
	})
	assertMigratedSessionJSON(t, store.db, "session-blank-role", map[string]any{
		"continuation": map[string]any{"keep": "blank"},
		"locked":       map[string]any{},
		"metadata":     map[string]any{"prompt_cache_lineage_generation": float64(10), "keep": "blank"},
	})
	assertMigratedSessionJSON(t, store.db, "session-existing-role", map[string]any{
		"continuation": map[string]any{"agent_role": "coder_low", "keep": "existing"},
		"locked":       map[string]any{"model": "locked-existing"},
		"metadata":     map[string]any{"prompt_cache_lineage_generation": float64(4), "keep": "existing"},
	})
	assertMigratedSessionJSON(t, store.db, "session-unassociated", map[string]any{
		"continuation": map[string]any{"keep": "unassociated"},
		"locked":       map[string]any{"model": "locked-unassociated"},
		"metadata":     map[string]any{"prompt_cache_lineage_generation": float64(7)},
	})
	assertMigratedSessionJSON(t, store.db, "session-non-agent", map[string]any{
		"continuation": map[string]any{"keep": "non-agent"},
		"locked":       map[string]any{"model": "locked-non-agent"},
		"metadata":     map[string]any{"prompt_cache_lineage_generation": float64(8)},
	})
}

func TestOpenRepairsBlankWorkflowSessionAgentRoleFromAppliedMigration(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 63)
	if err != nil {
		t.Fatalf("open version 63 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-blank-role-repair', 'Project', ?, ?, '{}')`, now, now)
	seedLegacyWorkflowSession(t, db, "project-blank-role-repair", "workspace-blank-role-repair", "session-blank-role-repair", now)
	execSeed(t, db, "blank role session", `
UPDATE sessions
SET continuation_json = '{"agent_role":"","keep":"blank"}',
    locked_json = '{"model":"locked-blank"}',
    metadata_json = '{"prompt_cache_lineage_generation":4,"keep":"blank"}'
WHERE id = 'session-blank-role-repair'`)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 63 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open repaired store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	assertMigratedSessionJSON(t, store.db, "session-blank-role-repair", map[string]any{
		"continuation": map[string]any{"keep": "blank"},
		"locked":       map[string]any{},
		"metadata":     map[string]any{"prompt_cache_lineage_generation": float64(5), "keep": "blank"},
	})
}

func TestOpenRepairsNullWorkflowSessionAgentRoleAfterBlankRepair(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 64)
	if err != nil {
		t.Fatalf("open version 64 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-null-role-repair', 'Project', ?, ?, '{}')`, now, now)
	seedLegacyWorkflowSession(t, db, "project-null-role-repair", "workspace-null-role-repair", "session-null-role-repair", now)
	execSeed(t, db, "null role session", `
UPDATE sessions
SET continuation_json = '{"agent_role":null,"keep":"null"}',
    locked_json = '{"model":"locked-null"}',
    metadata_json = '{"prompt_cache_lineage_generation":6,"keep":"null"}'
WHERE id = 'session-null-role-repair'`)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 64 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open repaired store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	assertMigratedSessionJSON(t, store.db, "session-null-role-repair", map[string]any{
		"continuation": map[string]any{"keep": "null"},
		"locked":       map[string]any{},
		"metadata":     map[string]any{"prompt_cache_lineage_generation": float64(7), "keep": "null"},
	})
}

func assertMigratedSessionJSON(t *testing.T, db *sql.DB, sessionID string, want map[string]any) {
	t.Helper()
	var continuationRaw, lockedRaw, metadataRaw string
	if err := db.QueryRow(`
SELECT continuation_json, locked_json, metadata_json
FROM sessions
WHERE id = ?`, sessionID).Scan(&continuationRaw, &lockedRaw, &metadataRaw); err != nil {
		t.Fatalf("query migrated session %q: %v", sessionID, err)
	}
	for key, raw := range map[string]string{
		"continuation": continuationRaw,
		"locked":       lockedRaw,
		"metadata":     metadataRaw,
	} {
		var got map[string]any
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("decode migrated session %q %s: %v", sessionID, key, err)
		}
		if !reflect.DeepEqual(got, want[key].(map[string]any)) {
			t.Fatalf("migrated session %q %s = %#v, want %#v", sessionID, key, got, want[key])
		}
	}
}
