package metadata

import (
	"context"
	"database/sql"
	"slices"
	"testing"

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
		plan := metadataQueryPlan(t, store.db, recorder.query, recorder.args...)
		requireMetadataQueryPlanDetail(
			t,
			plan,
			"SEARCH session USING INDEX sessions_task_activity_idx (task_id=?)",
		)
		requireMetadataQueryPlanDetail(
			t,
			plan,
			"SEARCH candidate USING COVERING INDEX session_workflow_node_associations_session_recency_idx (session_id=?)",
		)
		rejectMetadataQueryPlanDetail(t, plan, "USE TEMP B-TREE FOR ORDER BY")
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
		plan := metadataQueryPlan(t, store.db, recorder.query, recorder.args...)
		requireMetadataQueryPlanDetail(t, plan, "SCAN active VIRTUAL TABLE INDEX 1:")
		requireMetadataQueryPlanDetail(
			t,
			plan,
			"SEARCH session USING INDEX sqlite_autoindex_sessions_1 (id=?)",
		)
		requireMetadataQueryPlanDetail(
			t,
			plan,
			"SEARCH candidate USING COVERING INDEX session_workflow_node_associations_session_recency_idx (session_id=?)",
		)
		rejectMetadataQueryPlanDetail(
			t,
			plan,
			"SEARCH session USING INDEX sessions_task_activity_idx (task_id=?)",
		)
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

func metadataQueryPlan(t *testing.T, db *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("explain query plan: %v", err)
	}
	defer rows.Close()

	var details []string
	for rows.Next() {
		var id, parent, unused int64
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate query plan: %v", err)
	}
	return details
}

func requireMetadataQueryPlanDetail(t *testing.T, details []string, want string) {
	t.Helper()
	if !slices.Contains(details, want) {
		t.Fatalf("query plan = %v, want detail %q", details, want)
	}
}

func rejectMetadataQueryPlanDetail(t *testing.T, details []string, unwanted string) {
	t.Helper()
	if slices.Contains(details, unwanted) {
		t.Fatalf("query plan = %v, reject detail %q", details, unwanted)
	}
}
