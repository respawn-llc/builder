package metadata

import (
	"context"
	"database/sql"
	"testing"

	queryplantest "core/internal/testharness/databaseseed"
	"core/server/metadata/sqlitegen"
)

func TestTaskSessionQueriesUseMigratedIndexesWithoutTaskHistoryScan(t *testing.T) {
	store, _, _ := newMetadataTestStore(t)

	t.Run("Idle page", func(t *testing.T) {
		recorder := &metadataQueryRecorder{DB: store.db}
		queries := sqlitegen.New(recorder)
		if _, err := queries.ListIdleWorkflowTaskSessions(t.Context(), sqlitegen.ListIdleWorkflowTaskSessionsParams{
			TaskID:                 sql.NullString{String: "task-1", Valid: true},
			ExcludedSessionIdsJson: "[]",
			PageOffset:             0,
			PageLimit:              101,
		}); err != nil {
			t.Fatalf("execute Idle query: %v", err)
		}
		program := queryplantest.Program(t, store.db, recorder.query, recorder.args...)
		queryplantest.RequireProgramOpensIndex(
			t,
			store.db,
			program,
			"sessions_task_activity_idx",
		)
		queryplantest.RequireProgramOpensIndex(
			t,
			store.db,
			program,
			"session_workflow_node_associations_session_recency_idx",
		)
		queryplantest.RequireProgramWithoutSorter(t, program)
	})

	t.Run("active selection", func(t *testing.T) {
		recorder := &metadataQueryRecorder{DB: store.db}
		queries := sqlitegen.New(recorder)
		if _, err := queries.ListActiveWorkflowTaskSessions(t.Context(), sqlitegen.ListActiveWorkflowTaskSessionsParams{
			TaskID:         sql.NullString{String: "task-1", Valid: true},
			SessionIdsJson: `["session-1"]`,
		}); err != nil {
			t.Fatalf("execute active query: %v", err)
		}
		program := queryplantest.Program(t, store.db, recorder.query, recorder.args...)
		queryplantest.RequireProgramContainsOpcode(t, program, queryplantest.OpcodeVFilter)
		queryplantest.RequireProgramOpensIndex(
			t,
			store.db,
			program,
			"sqlite_autoindex_sessions_1",
		)
		queryplantest.RequireProgramOpensIndex(
			t,
			store.db,
			program,
			"session_workflow_node_associations_session_recency_idx",
		)
		queryplantest.RequireProgramDoesNotOpenIndex(
			t,
			store.db,
			program,
			"sessions_task_activity_idx",
		)
		queryplantest.RequireProgramWithoutSorter(t, program)
	})
}

type metadataQueryRecorder struct {
	*sql.DB
	query string
	args  []any
}

func (r *metadataQueryRecorder) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	r.query = query
	r.args = append([]any(nil), args...)
	return r.DB.QueryContext(ctx, query, args...)
}
