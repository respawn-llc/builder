package sqlitegen

import (
	"database/sql"
	"testing"

	"core/shared/tasksearchtext"
)

func TestListTaskSearchPageDescriptorsAllocatesSourceOrdinalsFromOneFTSRelation(t *testing.T) {
	db := openSQLiteFixture(t, ":memory:")
	t.Cleanup(func() { _ = db.Close() })
	createTaskSearchPageDescriptorFixture(t, db)

	rows, err := New(db).ListTaskSearchPageDescriptors(t.Context(), taskSearchPageDescriptorParams(
		"literal",
		"needle",
		"needle",
		int64(tasksearchtext.LiteralCaseInsensitive),
	))
	if err != nil {
		t.Fatalf("ListTaskSearchPageDescriptors literal: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("literal page rows = %+v, want title, two body occurrences, and Comment", rows)
	}
	for index, row := range rows {
		if row.TaskID != "task-1" ||
			row.TotalHitCount != 4 ||
			row.Ordinal != int64(index+1) ||
			row.SourceOrdinal < 1 {
			t.Fatalf("literal descriptor %d = %+v", index, row)
		}
	}
	if rows[0].SourceKind != "title" || rows[0].SourceOrdinal != 1 {
		t.Fatalf("first literal descriptor = %+v, want title source ordinal 1", rows[0])
	}
	if rows[1].SourceKind != "body" || rows[1].SourceOrdinal != 1 ||
		rows[2].SourceKind != "body" || rows[2].SourceOrdinal != 2 {
		t.Fatalf("body literal descriptors = %+v, %+v, want source ordinals 1 and 2", rows[1], rows[2])
	}
	if rows[3].SourceKind != "comment" || rows[3].SourceOrdinal != 1 || !rows[3].CommentID.Valid || rows[3].CommentID.String != "comment-1" {
		t.Fatalf("last literal descriptor = %+v, want Comment source ordinal 1", rows[3])
	}

	rawRows, err := New(db).ListTaskSearchPageDescriptors(t.Context(), taskSearchPageDescriptorParams(
		"fts5",
		"needle",
		"needle",
		int64(tasksearchtext.LiteralCaseInsensitive),
	))
	if err != nil {
		t.Fatalf("ListTaskSearchPageDescriptors raw: %v", err)
	}
	if len(rawRows) != 3 {
		t.Fatalf("raw page rows = %+v, want one descriptor per matching source", rawRows)
	}
	for index, wantKind := range []string{"title", "body", "comment"} {
		if rawRows[index].SourceKind != wantKind ||
			rawRows[index].Ordinal != int64(index+1) ||
			rawRows[index].SourceOrdinal != 1 {
			t.Fatalf("raw descriptor %d = %+v, want %q ordinal/source-ordinal %d/1", index, rawRows[index], wantKind, index+1)
		}
	}
	firstPageParams := taskSearchPageDescriptorParams(
		"fts5",
		"needle",
		"needle",
		int64(tasksearchtext.LiteralCaseInsensitive),
	)
	firstPageParams.LimitRows = 1
	firstPage, err := New(db).ListTaskSearchPageDescriptors(t.Context(), firstPageParams)
	if err != nil {
		t.Fatalf("ListTaskSearchPageDescriptors first raw page: %v", err)
	}
	if len(firstPage) != 1 || firstPage[0].Ordinal != 1 || firstPage[0].SourceKind != "title" {
		t.Fatalf("first raw page = %+v, want title ordinal 1", firstPage)
	}
	nextPageParams := firstPageParams
	nextPageParams.CursorSet = 1
	nextPageParams.CursorOrdinal = firstPage[0].Ordinal
	nextPageParams.CursorWeightedRank = sql.NullFloat64{Float64: firstPage[0].TaskWeightedRank, Valid: true}
	nextPageParams.CursorTaskID = firstPage[0].TaskID
	nextPage, err := New(db).ListTaskSearchPageDescriptors(t.Context(), nextPageParams)
	if err != nil {
		t.Fatalf("ListTaskSearchPageDescriptors raw continuation: %v", err)
	}
	if len(nextPage) != 1 || nextPage[0].Ordinal != 2 || nextPage[0].SourceKind != "body" {
		t.Fatalf("raw continuation = %+v, want body ordinal 2", nextPage)
	}

	instructions := queryProgram(t, db, listTaskSearchPageDescriptors, taskSearchPageDescriptorArgs(taskSearchPageDescriptorParams(
		"fts5",
		"needle",
		"needle",
		int64(tasksearchtext.LiteralCaseInsensitive),
	))...)
	hasFTSFilter := false
	for _, instruction := range instructions {
		if instruction.Opcode == sqliteOpcodeVFilter {
			hasFTSFilter = true
			break
		}
	}
	if !hasFTSFilter {
		t.Fatalf("task-search descriptor query did not invoke a virtual-table filter: %+v", instructions)
	}
}

func taskSearchPageDescriptorParams(mode, candidateExpression, literalQuery string, caseMode int64) ListTaskSearchPageDescriptorsParams {
	return ListTaskSearchPageDescriptorsParams{
		Mode:                      mode,
		CandidateExpression:       candidateExpression,
		LiteralQuery:              literalQuery,
		CaseMode:                  caseMode,
		IncludeComments:           1,
		ProjectIdsJson:            "[]",
		StatusFilterSet:           0,
		StatusKindsJson:           "[]",
		ContextClusters:           20,
		CursorSet:                 0,
		CursorOrdinal:             0,
		CursorWeightedRank:        sql.NullFloat64{},
		CursorTaskID:              "",
		LimitRows:                 100,
		AuthorityObservationsJson: "[]",
		CurrentRunFactsJson:       "[]",
	}
}

func taskSearchPageDescriptorArgs(params ListTaskSearchPageDescriptorsParams) []any {
	return []any{
		params.Mode,
		params.CandidateExpression,
		params.LiteralQuery,
		params.CaseMode,
		params.IncludeComments,
		params.ProjectIdsJson,
		params.StatusFilterSet,
		params.StatusKindsJson,
		params.ContextClusters,
		params.CursorSet,
		params.CursorOrdinal,
		params.CursorWeightedRank,
		params.CursorTaskID,
		params.LimitRows,
		params.AuthorityObservationsJson,
		params.CurrentRunFactsJson,
	}
}

func createTaskSearchPageDescriptorFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`
CREATE TABLE projects (
    id TEXT PRIMARY KEY,
    project_key TEXT NOT NULL
);
CREATE TABLE task_records (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    short_id TEXT NOT NULL,
    workflow_id TEXT NOT NULL,
    title TEXT NOT NULL
);
CREATE TABLE workflow_task_status_records (
    task_id TEXT PRIMARY KEY,
    is_done INTEGER NOT NULL,
    kind TEXT NOT NULL,
    node_ids_json TEXT NOT NULL,
    run_ids_json TEXT NOT NULL,
    attention_types_json TEXT NOT NULL
);
CREATE TABLE task_comments (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL,
    created_at_unix_ms INTEGER NOT NULL
);
CREATE TABLE task_search_documents (
    document_id INTEGER PRIMARY KEY,
    task_id TEXT,
    comment_id TEXT,
    source_kind TEXT NOT NULL
);
CREATE TABLE task_search_content (
    document_id INTEGER PRIMARY KEY,
    title TEXT,
    body TEXT,
    comment TEXT
);
CREATE VIRTUAL TABLE task_search_fts USING fts5(
    title,
    body,
    comment,
    tokenize = 'trigram case_sensitive 0 remove_diacritics 1'
);

INSERT INTO projects (id, project_key) VALUES ('project-1', 'TAS');
INSERT INTO task_records (id, project_id, short_id, workflow_id, title)
VALUES ('task-1', 'project-1', 'TAS-1', 'workflow-1', 'needle title');
INSERT INTO workflow_task_status_records (
    task_id,
    is_done,
    kind,
    node_ids_json,
    run_ids_json,
    attention_types_json
) VALUES ('task-1', 0, 'backlog', '[]', '[]', '[]');
INSERT INTO task_comments (id, task_id, created_at_unix_ms)
VALUES ('comment-1', 'task-1', 1);
INSERT INTO task_search_documents (document_id, task_id, comment_id, source_kind) VALUES
    (1, 'task-1', NULL, 'title'),
    (2, 'task-1', NULL, 'body'),
    (3, NULL, 'comment-1', 'comment');
INSERT INTO task_search_content (document_id, title, body, comment) VALUES
    (1, 'needle title', NULL, NULL),
    (2, NULL, 'needle body needle', NULL),
    (3, NULL, NULL, 'needle comment');
INSERT INTO task_search_fts (rowid, title, body, comment) VALUES
    (1, 'needle title', NULL, NULL),
    (2, NULL, 'needle body needle', NULL),
    (3, NULL, NULL, 'needle comment');`); err != nil {
		t.Fatalf("create task-search descriptor fixture: %v", err)
	}
}
