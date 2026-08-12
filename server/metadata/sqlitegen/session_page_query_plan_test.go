package sqlitegen

import "testing"

func TestSessionPageQueryUsesNormalizedCategoryRecencyIndexWithOffset(t *testing.T) {
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

	requireQueryUsesIndexWithoutSort(
		t,
		db,
		listSessionPage,
		"sessions_visible_category_recency_idx",
		"project-1",
		"main",
		int64(51),
		int64(50),
	)
}
