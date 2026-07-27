package sqlitegen

import (
	"database/sql"
	"testing"

	"core/server/tasksearchtext"
)

func TestListTaskSearchCandidateFeasibilityFiltersBeforeTaskWinnerSelection(t *testing.T) {
	db := openSQLiteFixture(t, ":memory:")
	t.Cleanup(func() { _ = db.Close() })
	createTaskSearchCandidateFeasibilityFixture(t, db)

	insertTaskSearchCandidateFeasibilityDocument(t, db, 1, "task-strong", "title", "foo")
	insertTaskSearchCandidateFeasibilityDocument(t, db, 2, "task-strong", "body", "foo")
	insertTaskSearchCandidateFeasibilityDocument(t, db, 3, "task-body", "title", "FOO")
	insertTaskSearchCandidateFeasibilityDocument(t, db, 4, "task-body", "body", "foo")
	insertTaskSearchCandidateFeasibilityDocument(t, db, 5, "task-false-positive", "title", "FOO")

	matcher, err := tasksearchtext.NewLiteralMatcher("foo", tasksearchtext.LiteralCaseSensitive)
	if err != nil {
		t.Fatalf("NewLiteralMatcher: %v", err)
	}
	params := ListTaskSearchCandidateFeasibilityParams{
		CandidateExpression: matcher.CandidateExpression(),
		LiteralQuery:        "foo",
		CaseMode:            int64(tasksearchtext.LiteralCaseSensitive),
		PageSize:            10,
	}
	rows, err := New(db).ListTaskSearchCandidateFeasibility(t.Context(), params)
	if err != nil {
		t.Fatalf("ListTaskSearchCandidateFeasibility: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("candidate feasibility rows = %+v, want two surviving Tasks", rows)
	}
	if rows[0].TaskID != "task-strong" || rows[0].SourceKind != "title" {
		t.Fatalf("strongest surviving Task row = %+v, want task-strong title", rows[0])
	}
	if rows[1].TaskID != "task-body" || rows[1].SourceKind != "body" {
		t.Fatalf("false-positive-title Task row = %+v, want task-body body", rows[1])
	}
	for _, row := range rows {
		if row.TaskID == "task-false-positive" {
			t.Fatal("false-positive-only Task survived exact occurrence filtering")
		}
	}

	firstPage, err := New(db).ListTaskSearchCandidateFeasibility(t.Context(), ListTaskSearchCandidateFeasibilityParams{
		CandidateExpression: matcher.CandidateExpression(),
		LiteralQuery:        "foo",
		CaseMode:            int64(tasksearchtext.LiteralCaseSensitive),
		PageSize:            1,
	})
	if err != nil {
		t.Fatalf("load first feasibility page: %v", err)
	}
	if len(firstPage) != 1 || firstPage[0].TaskID != rows[0].TaskID {
		t.Fatalf("first feasibility page = %+v, want %+v", firstPage, rows[0])
	}
	secondPage, err := New(db).ListTaskSearchCandidateFeasibility(t.Context(), ListTaskSearchCandidateFeasibilityParams{
		CandidateExpression: matcher.CandidateExpression(),
		LiteralQuery:        "foo",
		CaseMode:            int64(tasksearchtext.LiteralCaseSensitive),
		CursorRank:          sql.NullFloat64{Float64: firstPage[0].WeightedRank, Valid: true},
		CursorTaskID:        sql.NullString{String: firstPage[0].TaskID, Valid: true},
		PageSize:            1,
	})
	if err != nil {
		t.Fatalf("load second feasibility page: %v", err)
	}
	if len(secondPage) != 1 || secondPage[0].TaskID != rows[1].TaskID {
		t.Fatalf("second feasibility page = %+v, want %+v", secondPage, rows[1])
	}

	requireTaskSearchCandidateFeasibilityStartsFromFTS(
		t,
		queryProgram(t, db, listTaskSearchCandidateFeasibility, taskSearchCandidateFeasibilityArgs(params)...),
	)
}

func createTaskSearchCandidateFeasibilityFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`
CREATE TABLE task_search_documents (
    document_id INTEGER PRIMARY KEY,
    task_id TEXT NOT NULL,
    source_kind TEXT NOT NULL,
    source_text TEXT NOT NULL
);
CREATE VIRTUAL TABLE task_search_fts
USING fts5(source_text, content = '', tokenize = 'trigram case_sensitive 0 remove_diacritics 1');`); err != nil {
		t.Fatalf("create task-search candidate feasibility fixture: %v", err)
	}
}

func insertTaskSearchCandidateFeasibilityDocument(
	t *testing.T,
	db *sql.DB,
	documentID int64,
	taskID string,
	sourceKind string,
	sourceText string,
) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO task_search_documents (document_id, task_id, source_kind, source_text)
VALUES (?, ?, ?, ?)`,
		documentID,
		taskID,
		sourceKind,
		sourceText,
	); err != nil {
		t.Fatalf("insert candidate feasibility document %d: %v", documentID, err)
	}
	if _, err := db.Exec(
		`INSERT INTO task_search_fts (rowid, source_text) VALUES (?, ?)`,
		documentID,
		sourceText,
	); err != nil {
		t.Fatalf("insert candidate feasibility FTS document %d: %v", documentID, err)
	}
}

func taskSearchCandidateFeasibilityArgs(params ListTaskSearchCandidateFeasibilityParams) []any {
	return []any{
		params.CursorRank,
		params.CursorTaskID,
		params.PageSize,
		params.CandidateExpression,
		params.LiteralQuery,
		params.CaseMode,
	}
}

func requireTaskSearchCandidateFeasibilityStartsFromFTS(t *testing.T, instructions []sqliteInstruction) {
	t.Helper()
	for _, instruction := range instructions {
		switch instruction.Opcode {
		case sqliteOpcodeVFilter:
			return
		case sqliteOpcodeRewind, sqliteOpcodeNext, sqliteOpcodePrev:
			t.Fatalf("candidate feasibility query traversed a relation before FTS filtering: %+v", instructions)
		}
	}
	t.Fatalf("candidate feasibility query did not filter the FTS virtual table: %+v", instructions)
}
