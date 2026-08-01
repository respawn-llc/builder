package sqlitegen

import (
	"testing"
)

func TestSessionPageQueriesUseNormalizedCategoryRecencyIndex(t *testing.T) {
	db := openSQLiteFixture(t, ":memory:")
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
CREATE TABLE sessions (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	name TEXT,
	first_prompt_preview TEXT,
	category TEXT,
	launch_visible INTEGER NOT NULL,
	updated_at_unix_ms INTEGER NOT NULL
);
CREATE INDEX sessions_visible_category_recency_idx
	ON sessions(project_id, COALESCE(category, 'main'), updated_at_unix_ms DESC, id DESC)
	WHERE launch_visible <> 0;`); err != nil {
		t.Fatalf("create query-plan fixture: %v", err)
	}

	tests := []struct {
		name  string
		query string
		args  []any
	}{
		{
			name:  "newest",
			query: listNewestSessionPage,
			args:  []any{"project-1", "main", int64(26)},
		},
		{
			name:  "older",
			query: listOlderSessionPage,
			args:  []any{"project-1", "main", int64(1_900_000_000_000), "session-anchor", int64(26)},
		},
		{
			name:  "newer",
			query: listNewerSessionPage,
			args:  []any{"project-1", "main", int64(1_900_000_000_000), "session-anchor", int64(26)},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requireQueryUsesIndexWithoutSort(
				t,
				db,
				test.query,
				"sessions_visible_category_recency_idx",
				test.args...,
			)
		})
	}
}
