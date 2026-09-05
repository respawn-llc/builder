package metadata

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSessionContextBudgetCutoverPreservesOtherSessionFacts(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 88)
	if err != nil {
		t.Fatal(err)
	}
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms)
VALUES ('context-budget-project', 'Project', 1000, 1000)`)
	locked := map[string]any{
		"model":             "gpt-5.6-sol",
		"context_window":    float64(272_000),
		"context_percent":   float64(95),
		"system_prompt":     "preserved system prompt",
		"has_system_prompt": true,
		"reviewer_prompt":   "preserved reviewer prompt",
		"enabled_tools":     []any{"exec_command"},
		"provider_contract": map[string]any{"provider_id": "chatgpt-codex"},
		"locked_at":         "2026-08-20T21:05:48.594065Z",
	}
	encoded, err := json.Marshal(locked)
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		id     string
		locked string
	}{
		{id: "context-budget-session", locked: string(encoded)},
		{id: "unlocked-session", locked: "{}"},
	} {
		execSeed(t, db, "session", `
INSERT INTO sessions (
    id, project_id, artifact_relpath, name, input_draft, locked_json,
    continuation_json, usage_state_json, metadata_json,
    last_sequence, created_at_unix_ms, updated_at_unix_ms
) VALUES (?, 'context-budget-project', ?, 'evals', 'unsent message', ?,
    '{"agent_role":"pm"}', '{"input_tokens":123456}', '{"conversation_established":true}',
    1234, 1000, 2000)`,
			fixture.id, "sessions/"+fixture.id, fixture.locked)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenAtPath(root, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	delete(locked, "context_window")
	delete(locked, "context_percent")
	for _, id := range []string{"context-budget-session", "unlocked-session"} {
		var encoded, name, draft, continuation, usage, metadata string
		var sequence, created, updated int64
		err := store.DB().QueryRowContext(t.Context(), `
SELECT locked_json, name, input_draft, continuation_json, usage_state_json,
    metadata_json, last_sequence, created_at_unix_ms, updated_at_unix_ms
FROM sessions WHERE id = ?`, id).Scan(
			&encoded, &name, &draft, &continuation, &usage, &metadata, &sequence, &created, &updated,
		)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(encoded), &got); err != nil {
			t.Fatal(err)
		}
		want := locked
		if id == "unlocked-session" {
			want = map[string]any{}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("migrated contract = %+v, want %+v", got, want)
		}
		if name != "evals" || draft != "unsent message" ||
			continuation != `{"agent_role":"pm"}` || usage != `{"input_tokens":123456}` ||
			metadata != `{"conversation_established":true}` ||
			sequence != 1234 || created != 1000 || updated != 2000 {
			t.Fatalf("migration changed unrelated Session facts for %s", id)
		}
	}
}
