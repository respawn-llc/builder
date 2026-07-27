package sqlitegen

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"core/shared/tasksearchtext"
)

func TestListTaskSearchCandidateFeasibilityFiltersCanonicalDocumentsBeforeTaskWinnerSelection(t *testing.T) {
	db := openSQLiteFixture(t, ":memory:")
	t.Cleanup(func() { _ = db.Close() })
	createCanonicalTaskSearchCandidateFeasibilityFixture(t, db)

	matcher, err := tasksearchtext.NewLiteralMatcher("foo", tasksearchtext.LiteralCaseSensitive)
	if err != nil {
		t.Fatalf("NewLiteralMatcher: %v", err)
	}
	params := ListTaskSearchCandidateFeasibilityParams{
		CandidateExpression: sql.NullString{String: matcher.CandidateExpression(), Valid: true},
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
	if taskSearchCandidateFeasibilityTaskID(t, rows[0]) != "task-strong" || rows[0].SourceKind != "title" {
		t.Fatalf("strongest surviving Task row = %+v, want task-strong title", rows[0])
	}
	if taskSearchCandidateFeasibilityTaskID(t, rows[1]) != "task-body" || rows[1].SourceKind != "body" {
		t.Fatalf("false-positive-title Task row = %+v, want task-body body", rows[1])
	}
	for _, row := range rows {
		if taskSearchCandidateFeasibilityTaskID(t, row) == "task-false-positive" {
			t.Fatal("false-positive-only Task survived exact occurrence filtering")
		}
	}

	firstPage, err := New(db).ListTaskSearchCandidateFeasibility(t.Context(), ListTaskSearchCandidateFeasibilityParams{
		CandidateExpression: sql.NullString{String: matcher.CandidateExpression(), Valid: true},
		LiteralQuery:        "foo",
		CaseMode:            int64(tasksearchtext.LiteralCaseSensitive),
		PageSize:            1,
	})
	if err != nil {
		t.Fatalf("load first feasibility page: %v", err)
	}
	if len(firstPage) != 1 || taskSearchCandidateFeasibilityTaskID(t, firstPage[0]) != taskSearchCandidateFeasibilityTaskID(t, rows[0]) {
		t.Fatalf("first feasibility page = %+v, want %+v", firstPage, rows[0])
	}
	secondPage, err := New(db).ListTaskSearchCandidateFeasibility(t.Context(), ListTaskSearchCandidateFeasibilityParams{
		CandidateExpression: sql.NullString{String: matcher.CandidateExpression(), Valid: true},
		LiteralQuery:        "foo",
		CaseMode:            int64(tasksearchtext.LiteralCaseSensitive),
		CursorRank:          sql.NullFloat64{Float64: firstPage[0].WeightedRank, Valid: true},
		CursorTaskID:        sql.NullString{String: taskSearchCandidateFeasibilityTaskID(t, firstPage[0]), Valid: true},
		PageSize:            1,
	})
	if err != nil {
		t.Fatalf("load second feasibility page: %v", err)
	}
	if len(secondPage) != 1 || taskSearchCandidateFeasibilityTaskID(t, secondPage[0]) != taskSearchCandidateFeasibilityTaskID(t, rows[1]) {
		t.Fatalf("second feasibility page = %+v, want %+v", secondPage, rows[1])
	}

	requireTaskSearchCandidateFeasibilityStartsFromFTS(
		t,
		queryProgram(t, db, listTaskSearchCandidateFeasibility, taskSearchCandidateFeasibilityArgs(params)...),
	)
}

func taskSearchCandidateFeasibilityTaskID(t *testing.T, row ListTaskSearchCandidateFeasibilityRow) string {
	t.Helper()
	if !row.TaskID.Valid || row.TaskID.String == "" {
		t.Fatalf("candidate feasibility row has no canonical Task identity: %+v", row)
	}
	return row.TaskID.String
}

func createCanonicalTaskSearchCandidateFeasibilityFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`
CREATE TABLE tasks (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    body TEXT NOT NULL
);
CREATE TABLE task_comments (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL,
    body TEXT NOT NULL,
    created_at_unix_ms INTEGER NOT NULL
);
INSERT INTO tasks (id, title, body) VALUES
    ('task-strong', 'foo', 'foo'),
    ('task-body', 'FOO', 'foo'),
    ('task-false-positive', 'FOO', 'no body match');`); err != nil {
		t.Fatalf("create canonical task-search sources: %v", err)
	}
	migration, err := os.ReadFile(filepath.Join(taskSearchCandidateFeasibilityRepositoryRoot(t), "server", "metadata", "migrations", "00060_task_search_index.up.sql"))
	if err != nil {
		t.Fatalf("read canonical task-search migration: %v", err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatalf("apply canonical task-search migration: %v", err)
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

func taskSearchCandidateFeasibilityRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate candidate feasibility test source")
	}
	for directory := filepath.Dir(sourcePath); ; directory = filepath.Dir(directory) {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("locate repository root containing go.mod")
		}
	}
}
