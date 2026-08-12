package sqlitegen_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
)

func TestTaskSessionQueriesUseBoundedMigratedIndexes(t *testing.T) {
	db := openMigratedTaskSessionDatabase(t)

	t.Run("active IDs drive primary-key point seeks", func(t *testing.T) {
		recorder := &queryRecorder{DB: db}
		queries := sqlitegen.New(recorder)
		if _, err := queries.ListActiveWorkflowTaskSessions(t.Context(), sqlitegen.ListActiveWorkflowTaskSessionsParams{
			TaskID:         sql.NullString{String: "task-1", Valid: true},
			SessionIdsJson: `["session-1"]`,
		}); err != nil {
			t.Fatalf("execute active Task Session query: %v", err)
		}

		program := sqlitegen.QueryProgram(t, db, recorder.query, recorder.args...)
		sqlitegen.RequireProgramVirtualTableDrivesIndexPointSeeks(t, db, program, "sqlite_autoindex_sessions_1")
		sqlitegen.RequireProgramIndexSeekStopsAfterFirstRow(
			t,
			db,
			program,
			"session_workflow_node_associations_session_recency_idx",
		)
	})

	t.Run("Idle page seeks Task prefix without sorting", func(t *testing.T) {
		recorder := &queryRecorder{DB: db}
		queries := sqlitegen.New(recorder)
		if _, err := queries.ListIdleWorkflowTaskSessions(t.Context(), sqlitegen.ListIdleWorkflowTaskSessionsParams{
			TaskID:                 sql.NullString{String: "task-1", Valid: true},
			ExcludedSessionIdsJson: "[]",
			PageOffset:             0,
			PageLimit:              101,
		}); err != nil {
			t.Fatalf("execute Idle Task Session query: %v", err)
		}

		program := sqlitegen.QueryProgram(t, db, recorder.query, recorder.args...)
		sqlitegen.RequireProgramIndexSeekWithoutSorter(t, db, program, "sessions_task_activity_idx")
		sqlitegen.RequireProgramIndexSeekStopsAfterFirstRow(
			t,
			db,
			program,
			"session_workflow_node_associations_session_recency_idx",
		)
	})
}

func openMigratedTaskSessionDatabase(t *testing.T) *sql.DB {
	t.Helper()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	store, err := metadata.OpenAtPath(root, dbPath)
	if err != nil {
		t.Fatalf("open migrated metadata store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close migrated metadata store: %v", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("reopen migrated metadata database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close migrated metadata database: %v", err)
		}
	})
	return db
}

type queryRecorder struct {
	*sql.DB
	query string
	args  []any
}

func (r *queryRecorder) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	r.query = query
	r.args = append([]any(nil), args...)
	return r.DB.QueryContext(ctx, query, args...)
}
