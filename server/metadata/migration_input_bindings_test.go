package metadata

import (
	"database/sql/driver"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDecodeMigrationInputBindingsRequiresArray(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantCount int
		wantError bool
	}{
		{name: "empty array", raw: `[]`},
		{
			name:      "binding array",
			raw:       `[{"name":"summary","source":"transition_output","field":"summary"}]`,
			wantCount: 1,
		},
		{name: "malformed", raw: `[`, wantError: true},
		{name: "scalar", raw: `7`, wantError: true},
		{name: "empty object", raw: `{}`, wantError: true},
		{name: "non-empty object", raw: `{"name":"summary"}`, wantError: true},
		{name: "null", raw: `null`, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bindings, err := decodeMigrationInputBindings(test.raw)
			if test.wantError {
				if err == nil {
					t.Fatalf("decodeMigrationInputBindings accepted %s", test.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeMigrationInputBindings: %v", err)
			}
			if len(bindings) != test.wantCount {
				t.Fatalf("binding count = %d, want %d", len(bindings), test.wantCount)
			}
		})
	}
}

func TestMigrationCurrentInputValuesRejectsInvalidBindingValues(t *testing.T) {
	_, err := migrationCurrentInputValues(nil, []driver.Value{
		"task-1",
		"node-1",
		"",
		`[{"name":"summary","source":"unsupported","field":"summary"}]`,
		`{}`,
		"",
		"TASK-1",
		"Task",
		"Body",
		"",
	})
	if err == nil {
		t.Fatal("migrationCurrentInputValues accepted an unsupported binding source")
	}
	if !strings.Contains(err.Error(), "unsupported binding source") {
		t.Fatalf("migrationCurrentInputValues error = %v", err)
	}
}

func TestMigration60RequiresCanonicalInputBindingArrays(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantError bool
	}{
		{name: "array", raw: `[]`},
		{name: "normalized empty object", raw: `{}`},
		{name: "malformed", raw: `[`, wantError: true},
		{name: "scalar", raw: `7`, wantError: true},
		{name: "non-empty object", raw: `{"name":"summary"}`, wantError: true},
		{name: "invalid binding values", raw: `[{"name":"summary","source":"unsupported","field":"summary"}]`, wantError: true},
		{name: "null", raw: `null`, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			dbPath := filepath.Join(root, "db", "main.sqlite3")
			db, err := openDatabaseAtVersionForTest(t, root, dbPath, 59)
			if err != nil {
				t.Fatalf("open version 59 db: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			now := time.Now().UTC().UnixMilli()
			execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-input-binding-migration', 'Project', ?, ?, '{}')`, now, now)
			seedWorkflowGraph(t, db, "project-input-binding-migration", now)
			execSeed(t, db, "task", workflowSeedTaskSQL, "task-input-binding-migration", "link-1", 1, "INP-1", now, now)
			execSeed(
				t,
				db,
				"agent placement",
				workflowSeedPlacementSQL,
				"placement-input-binding-migration",
				"task-input-binding-migration",
				"node-agent",
				now,
				now,
			)
			seedLegacyExecutableCurrentNodeEnteringEdge(t, db, "task-input-binding-migration", "placement-input-binding-migration", now)
			if _, err := db.ExecContext(t.Context(), `PRAGMA ignore_check_constraints = ON`); err != nil {
				t.Fatalf("ignore historical input-binding checks: %v", err)
			}
			if _, err := db.ExecContext(t.Context(), `
UPDATE task_transition_edges
SET input_bindings_json = ?
WHERE id = 'entry-edge-placement-input-binding-migration'`, test.raw); err != nil {
				t.Fatalf("seed historical input bindings: %v", err)
			}

			provider, err := newMetadataMigrationProvider(db)
			if err != nil {
				t.Fatalf("create metadata migration provider: %v", err)
			}
			_, err = provider.UpTo(t.Context(), 60)
			if test.wantError {
				if err == nil {
					t.Fatalf("migration 60 accepted input bindings %s", test.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("apply migration 60: %v", err)
			}

			var persistedBindings string
			if err := db.QueryRowContext(t.Context(), `
SELECT input_bindings_json
FROM workflow_edges
WHERE id = 'edge-start-1'`).Scan(&persistedBindings); err != nil {
				t.Fatalf("query migrated input bindings: %v", err)
			}
			if persistedBindings != `[]` {
				t.Fatalf("persisted input bindings = %q, want canonical []", persistedBindings)
			}
			var currentInputValues string
			if err := db.QueryRowContext(t.Context(), `
SELECT current_input_values_json
FROM task_current_nodes
WHERE task_id = 'task-input-binding-migration'`).Scan(&currentInputValues); err != nil {
				t.Fatalf("query migrated current input values: %v", err)
			}
			if currentInputValues != `{}` {
				t.Fatalf("current input values = %q, want {}", currentInputValues)
			}
		})
	}
}

func TestMigration82NormalizesExistingEmptyObjectInputBindings(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 80)
	if err != nil {
		t.Fatalf("open version 80 db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now().UTC().UnixMilli()
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-input-binding-normalization', 'Project', ?, ?, '{}')`, now, now)
	seedWorkflowGraph(t, db, "project-input-binding-normalization", now)
	edgeID := workflowGraphSeedID(t, db, "edge-start-1")
	if _, err := db.ExecContext(t.Context(), `
UPDATE workflow_edges
SET input_bindings_json = '{}'
WHERE id = ?`, edgeID); err != nil {
		t.Fatalf("seed existing empty-object input bindings: %v", err)
	}

	provider, err := newMetadataMigrationProvider(db)
	if err != nil {
		t.Fatalf("create metadata migration provider: %v", err)
	}
	if _, err := provider.UpTo(t.Context(), 82); err != nil {
		t.Fatalf("apply migration 82: %v", err)
	}

	var persistedBindings string
	if err := db.QueryRowContext(t.Context(), `
SELECT input_bindings_json
FROM workflow_edges
WHERE id = ?`, edgeID).Scan(&persistedBindings); err != nil {
		t.Fatalf("query normalized input bindings: %v", err)
	}
	if persistedBindings != `[]` {
		t.Fatalf("persisted input bindings = %q, want canonical []", persistedBindings)
	}
}
