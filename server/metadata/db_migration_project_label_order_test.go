package metadata

import (
	"path/filepath"
	"testing"
)

func TestProjectLabelOrderMigrationBackfillsContiguousOrdinals(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 60)
	if err != nil {
		t.Fatalf("open version 60 database: %v", err)
	}
	execSeed(t, db, "project label order migration", `
INSERT INTO projects (
    id,
    display_name,
    created_at_unix_ms,
    updated_at_unix_ms,
    metadata_json,
    project_key,
    next_task_seq
) VALUES ('project-label-order-migration', 'Project', 1, 1, '{}', 'PRJ', 1);
INSERT INTO project_labels (
    id,
    project_id,
    name,
    created_at_unix_ms,
    updated_at_unix_ms
) VALUES
    ('label-zulu', 'project-label-order-migration', 'Zulu', 1, 1),
    ('label-alpha', 'project-label-order-migration', 'alpha', 1, 1),
    ('label-middle', 'project-label-order-migration', 'Middle', 1, 1);
`)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 60 database: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	rows, err := store.DB().Query(`
SELECT id, ordinal
FROM project_labels
WHERE project_id = 'project-label-order-migration'
ORDER BY ordinal ASC
`)
	if err != nil {
		t.Fatalf("read migrated label ordinals: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var got []struct {
		id      string
		ordinal int64
	}
	for rows.Next() {
		var item struct {
			id      string
			ordinal int64
		}
		if err := rows.Scan(&item.id, &item.ordinal); err != nil {
			t.Fatalf("scan migrated label ordinal: %v", err)
		}
		got = append(got, item)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migrated label ordinals: %v", err)
	}
	want := []struct {
		id      string
		ordinal int64
	}{
		{id: "label-alpha", ordinal: 1},
		{id: "label-middle", ordinal: 2},
		{id: "label-zulu", ordinal: 3},
	}
	if len(got) != len(want) {
		t.Fatalf("migrated label count = %d, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("migrated label %d = %+v, want %+v", index, got[index], want[index])
		}
	}

	var uniqueIndexCount int
	if err := store.DB().QueryRow(`
SELECT COUNT(*)
FROM pragma_index_list('project_labels')
WHERE name = 'project_labels_project_ordinal_idx' AND "unique" = 1
`).Scan(&uniqueIndexCount); err != nil {
		t.Fatalf("check project label ordinal index: %v", err)
	}
	if uniqueIndexCount != 1 {
		t.Fatalf("project label ordinal unique index count = %d, want 1", uniqueIndexCount)
	}
	if _, err := store.DB().Exec(`
INSERT INTO project_labels (
    id,
    project_id,
    name,
    ordinal,
    created_at_unix_ms,
    updated_at_unix_ms
) VALUES ('label-duplicate-ordinal', 'project-label-order-migration', 'Duplicate', 1, 2, 2)
`); err == nil {
		t.Fatal("duplicate project label ordinal insert succeeded")
	}
}
