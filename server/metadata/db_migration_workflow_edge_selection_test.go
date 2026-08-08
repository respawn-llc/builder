package metadata

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkflowEdgeSelectionMigrationDefaultsModesAndParameterPurposes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 71)
	if err != nil {
		t.Fatalf("open version 71 database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now().UTC().UnixMilli()
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-edge-selection-migration', 'Project', ?, ?, '{}')`, now, now)
	seedWorkflowGraph(t, db, "project-edge-selection-migration", now)
	execSeed(t, db, "legacy edge parameters", `
UPDATE workflow_edges
SET parameters_json = '[{"key":"summary","description":"Summary."}]'
WHERE id = 'edge-done-1'`)

	provider, err := newMetadataMigrationProvider(db)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	if _, err := provider.Up(context.Background()); err != nil {
		t.Fatalf("apply edge selection migration: %v", err)
	}

	var assigneeSelection, thinkingSelection, parametersJSON string
	if err := db.QueryRow(`
SELECT assignee_selection, thinking_selection, parameters_json
FROM workflow_edges
WHERE id = 'edge-done-1'`).Scan(&assigneeSelection, &thinkingSelection, &parametersJSON); err != nil {
		t.Fatalf("read migrated edge: %v", err)
	}
	if assigneeSelection != "configured" || thinkingSelection != "configured" {
		t.Fatalf("migrated edge modes = %q/%q, want configured/configured", assigneeSelection, thinkingSelection)
	}
	var purpose sql.NullString
	if err := db.QueryRow(`SELECT json_extract(?, '$[0].purpose')`, parametersJSON).Scan(&purpose); err != nil {
		t.Fatalf("read migrated parameter purpose: %v", err)
	}
	if !purpose.Valid || purpose.String != "ordinary" {
		t.Fatalf("migrated parameter purpose = %q/%t, want ordinary/present", purpose.String, purpose.Valid)
	}
}
