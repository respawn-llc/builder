package sqlitegen

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"core/shared/tasksearchtext"

	sqlitedriver "modernc.org/sqlite"
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
	nextPageParams.CursorOrdinal = sql.NullInt64{Int64: firstPage[0].Ordinal, Valid: true}
	nextPageParams.CursorWeightedRank = sql.NullFloat64{Float64: firstPage[0].TaskWeightedRank, Valid: true}
	nextPageParams.CursorTaskID = sql.NullString{String: firstPage[0].TaskID, Valid: true}
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

func TestListTaskSearchPageDescriptorsFiltersProjectAndStatusBeforeAllocatingNewestCommentsAndKeysets(t *testing.T) {
	db := openSQLiteFixture(t, ":memory:")
	t.Cleanup(func() { _ = db.Close() })
	createTaskSearchPageDescriptorFilteringFixture(t, db)

	params := taskSearchPageDescriptorParams(
		"fts5",
		"needle",
		"needle",
		int64(tasksearchtext.LiteralCaseInsensitive),
	)
	params.ProjectIdsJson = sql.NullString{String: `["project-a"]`, Valid: true}
	params.StatusKindsJson = sql.NullString{String: `["done"]`, Valid: true}
	rows, err := New(db).ListTaskSearchPageDescriptors(t.Context(), params)
	if err != nil {
		t.Fatalf("ListTaskSearchPageDescriptors scoped raw: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("scoped raw descriptors = %+v, want body plus two Comments", rows)
	}
	for index, want := range []struct {
		sourceKind    string
		commentID     string
		ordinal       int64
		sourceOrdinal int64
	}{
		{sourceKind: "body", ordinal: 1, sourceOrdinal: 1},
		{sourceKind: "comment", commentID: "comment-new", ordinal: 2, sourceOrdinal: 1},
		{sourceKind: "comment", commentID: "comment-old", ordinal: 3, sourceOrdinal: 1},
	} {
		got := rows[index]
		if got.TaskID != "task-a" ||
			got.SourceKind != want.sourceKind ||
			got.Ordinal != want.ordinal ||
			got.SourceOrdinal != want.sourceOrdinal ||
			got.TotalHitCount != 3 {
			t.Fatalf("scoped descriptor %d = %+v, want task-a %+v", index, got, want)
		}
		if want.commentID == "" {
			if got.CommentID.Valid {
				t.Fatalf("body descriptor comment identity = %+v, want absent", got.CommentID)
			}
		} else if !got.CommentID.Valid || got.CommentID.String != want.commentID {
			t.Fatalf("Comment descriptor %d = %+v, want %q", index, got.CommentID, want.commentID)
		}
	}

	params.IncludeComments = 0
	withoutComments, err := New(db).ListTaskSearchPageDescriptors(t.Context(), params)
	if err != nil {
		t.Fatalf("ListTaskSearchPageDescriptors without Comments: %v", err)
	}
	if len(withoutComments) != 1 ||
		withoutComments[0].TaskID != "task-a" ||
		withoutComments[0].SourceKind != "body" ||
		withoutComments[0].TotalHitCount != 1 {
		t.Fatalf("Comment-excluded descriptors = %+v, want task-a body only", withoutComments)
	}

	params.IncludeComments = 1
	params.ProjectIdsJson = sql.NullString{String: `["project-b"]`, Valid: true}
	projectB, err := New(db).ListTaskSearchPageDescriptors(t.Context(), params)
	if err != nil {
		t.Fatalf("ListTaskSearchPageDescriptors project-b: %v", err)
	}
	if len(projectB) != 1 ||
		projectB[0].TaskID != "task-c" ||
		projectB[0].SourceKind != "body" ||
		projectB[0].TotalHitCount != 1 {
		t.Fatalf("project-b descriptors = %+v, want task-c body only", projectB)
	}

	params.IncludeComments = 0
	params.ProjectIdsJson = sql.NullString{}
	params.LimitRows = 1
	first, err := New(db).ListTaskSearchPageDescriptors(t.Context(), params)
	if err != nil {
		t.Fatalf("ListTaskSearchPageDescriptors keyset first page: %v", err)
	}
	if len(first) != 1 || first[0].TaskID != "task-a" || first[0].Ordinal != 1 {
		t.Fatalf("keyset first page = %+v, want task-a ordinal 1", first)
	}
	params.CursorOrdinal = sql.NullInt64{Int64: first[0].Ordinal, Valid: true}
	params.CursorWeightedRank = sql.NullFloat64{Float64: first[0].TaskWeightedRank, Valid: true}
	params.CursorTaskID = sql.NullString{String: first[0].TaskID, Valid: true}
	second, err := New(db).ListTaskSearchPageDescriptors(t.Context(), params)
	if err != nil {
		t.Fatalf("ListTaskSearchPageDescriptors keyset second page: %v", err)
	}
	if len(second) != 1 || second[0].TaskID != "task-c" || second[0].Ordinal != 1 {
		t.Fatalf("keyset second page = %+v, want task-c ordinal 1", second)
	}
	if second[0].TaskWeightedRank != first[0].TaskWeightedRank {
		t.Fatalf(
			"same-source Task ranks = %f and %f, want exact tie resolved by Task ID",
			first[0].TaskWeightedRank,
			second[0].TaskWeightedRank,
		)
	}
}

func TestListTaskSearchPageDescriptorsOrdersCommentsByWeightedRankBeforeRecency(t *testing.T) {
	db := openSQLiteFixture(t, ":memory:")
	t.Cleanup(func() { _ = db.Close() })
	createTaskSearchPageDescriptorFilteringFixture(t, db)
	weakerNewComment := "needle " + strings.Repeat("filler ", 256)
	if _, err := db.Exec(`
UPDATE task_search_content
SET comment = CASE document_id
    WHEN 2 THEN 'needle'
    WHEN 3 THEN ?
END
WHERE document_id IN (2, 3);
UPDATE task_search_fts
SET comment = CASE rowid
    WHEN 2 THEN 'needle'
    WHEN 3 THEN ?
END
WHERE rowid IN (2, 3);`, weakerNewComment, weakerNewComment); err != nil {
		t.Fatalf("set unequal Comment relevance fixture: %v", err)
	}

	for _, mode := range []string{"literal", "fts5"} {
		t.Run(mode, func(t *testing.T) {
			params := taskSearchPageDescriptorParams(
				mode,
				"needle",
				"needle",
				int64(tasksearchtext.LiteralCaseInsensitive),
			)
			params.ProjectIdsJson = sql.NullString{String: `["project-a"]`, Valid: true}
			params.StatusKindsJson = sql.NullString{String: `["done"]`, Valid: true}
			rows, err := New(db).ListTaskSearchPageDescriptors(t.Context(), params)
			if err != nil {
				t.Fatalf("ListTaskSearchPageDescriptors: %v", err)
			}
			commentIDs := make([]string, 0, 2)
			for _, row := range rows {
				if row.SourceKind == "comment" {
					if !row.CommentID.Valid {
						t.Fatalf("Comment descriptor missing identity: %+v", row)
					}
					commentIDs = append(commentIDs, row.CommentID.String)
				}
			}
			if len(commentIDs) != 2 || commentIDs[0] != "comment-old" || commentIDs[1] != "comment-new" {
				t.Fatalf(
					"Comment order = %v, want older stronger match before newer weaker match",
					commentIDs,
				)
			}
		})
	}
}

func TestListTaskSearchPageDescriptorsKeepsSelectiveStatusPageWorkNearLinear(t *testing.T) {
	type measurement struct {
		taskCount int
		cacheHits int
	}
	for _, mode := range []string{"literal", "fts5"} {
		t.Run(mode, func(t *testing.T) {
			measurements := make([]measurement, 0, 2)
			for _, taskCount := range []int{128, 256} {
				t.Run(fmt.Sprintf("%d_tasks", taskCount), func(t *testing.T) {
					db := openSQLiteFixture(t, ":memory:")
					t.Cleanup(func() { _ = db.Close() })
					db.SetMaxOpenConns(1)
					createTaskSearchHighCardinalityFixture(t, db, taskCount)

					params := taskSearchPageDescriptorParams(
						mode,
						"needle",
						"needle",
						int64(tasksearchtext.LiteralCaseInsensitive),
					)
					params.IncludeComments = 0
					params.StatusKindsJson = sql.NullString{String: `["done"]`, Valid: true}
					params.LimitRows = 1
					rows, cacheHits := executeTaskSearchDescriptorPageWithCacheMeasurement(t, db, params)
					if len(rows) != 1 ||
						rows[0].TaskID != "task-0000" ||
						rows[0].Ordinal != 1 ||
						rows[0].TotalHitCount != 1 {
						t.Fatalf("selective status page = %+v, want one bounded done Task descriptor", rows)
					}
					if cacheHits <= 0 {
						t.Fatal("SQLite cache-hit instrumentation observed no ranked-page work")
					}
					measurements = append(measurements, measurement{taskCount: taskCount, cacheHits: cacheHits})
				})
			}
			small, large := measurements[0], measurements[1]
			if large.cacheHits > small.cacheHits*3+1024 {
				t.Fatalf(
					"descriptor work grew superlinearly across selective status pages: %d tasks=%d cache hits, %d tasks=%d cache hits",
					small.taskCount,
					small.cacheHits,
					large.taskCount,
					large.cacheHits,
				)
			}
		})
	}
}

func taskSearchPageDescriptorParams(mode, candidateExpression, literalQuery string, caseMode int64) ListTaskSearchPageDescriptorsParams {
	return ListTaskSearchPageDescriptorsParams{
		Mode:                mode,
		CandidateExpression: candidateExpression,
		LiteralQuery:        literalQuery,
		CaseMode:            caseMode,
		IncludeComments:     1,
		ProjectIdsJson:      sql.NullString{},
		StatusKindsJson:     sql.NullString{},
		ContextClusters:     20,
		CursorOrdinal:       sql.NullInt64{},
		CursorWeightedRank:  sql.NullFloat64{},
		CursorTaskID:        sql.NullString{},
		LimitRows:           100,
		LiveTaskStatesJson:  "[]",
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
		params.StatusKindsJson,
		params.ContextClusters,
		params.CursorOrdinal,
		params.CursorWeightedRank,
		params.CursorTaskID,
		params.LimitRows,
		params.LiveTaskStatesJson,
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
    primary_status_rank INTEGER NOT NULL,
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
    primary_status_rank,
    attention_types_json
) VALUES ('task-1', 0, 'backlog', '[]', 7, '[]');
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

func createTaskSearchPageDescriptorFilteringFixture(t *testing.T, db *sql.DB) {
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
    primary_status_rank INTEGER NOT NULL,
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

INSERT INTO projects (id, project_key) VALUES
    ('project-a', 'AAA'),
    ('project-b', 'BBB');
INSERT INTO task_records (id, project_id, short_id, workflow_id, title) VALUES
    ('task-a', 'project-a', 'AAA-1', 'workflow-1', 'Task A'),
    ('task-b', 'project-a', 'AAA-2', 'workflow-1', 'Task B'),
    ('task-c', 'project-b', 'BBB-1', 'workflow-1', 'Task C');
INSERT INTO workflow_task_status_records (
    task_id,
    is_done,
    kind,
    node_ids_json,
    primary_status_rank,
    attention_types_json
) VALUES
    ('task-a', 1, 'done', '[]', 1, '[]'),
    ('task-b', 0, 'backlog', '[]', 7, '[]'),
    ('task-c', 1, 'done', '[]', 1, '[]');
INSERT INTO task_comments (id, task_id, created_at_unix_ms) VALUES
    ('comment-old', 'task-a', 1),
    ('comment-new', 'task-a', 2);
INSERT INTO task_search_documents (document_id, task_id, comment_id, source_kind) VALUES
    (1, 'task-a', NULL, 'body'),
    (2, NULL, 'comment-old', 'comment'),
    (3, NULL, 'comment-new', 'comment'),
    (4, 'task-b', NULL, 'body'),
    (5, 'task-c', NULL, 'body');
INSERT INTO task_search_content (document_id, title, body, comment) VALUES
    (1, NULL, 'needle', NULL),
    (2, NULL, NULL, 'needle'),
    (3, NULL, NULL, 'needle'),
    (4, NULL, 'needle', NULL),
    (5, NULL, 'needle', NULL);
INSERT INTO task_search_fts (rowid, title, body, comment) VALUES
    (1, NULL, 'needle', NULL),
    (2, NULL, NULL, 'needle'),
    (3, NULL, NULL, 'needle'),
    (4, NULL, 'needle', NULL),
    (5, NULL, 'needle', NULL);`); err != nil {
		t.Fatalf("create task-search descriptor filtering fixture: %v", err)
	}
}

func createTaskSearchHighCardinalityFixture(t *testing.T, db *sql.DB, taskCount int) {
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
    primary_status_rank INTEGER NOT NULL,
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
INSERT INTO projects (id, project_key) VALUES ('project-1', 'HIG');`); err != nil {
		t.Fatalf("create high-cardinality task-search schema: %v", err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin high-cardinality task-search fixture transaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	insertTask, err := tx.PrepareContext(t.Context(), `
INSERT INTO task_records (id, project_id, short_id, workflow_id, title)
VALUES (?, 'project-1', ?, 'workflow-1', ?)`)
	if err != nil {
		t.Fatalf("prepare high-cardinality Task insertion: %v", err)
	}
	defer insertTask.Close()
	insertStatus, err := tx.PrepareContext(t.Context(), `
INSERT INTO workflow_task_status_records (
    task_id,
    is_done,
    kind,
    node_ids_json,
    primary_status_rank,
    attention_types_json
) VALUES (?, ?, ?, '[]', ?, '[]')`)
	if err != nil {
		t.Fatalf("prepare high-cardinality status insertion: %v", err)
	}
	defer insertStatus.Close()
	insertDocument, err := tx.PrepareContext(t.Context(), `
INSERT INTO task_search_documents (document_id, task_id, comment_id, source_kind)
VALUES (?, ?, NULL, 'body')`)
	if err != nil {
		t.Fatalf("prepare high-cardinality document insertion: %v", err)
	}
	defer insertDocument.Close()
	insertContent, err := tx.PrepareContext(t.Context(), `
INSERT INTO task_search_content (document_id, title, body, comment)
VALUES (?, NULL, 'needle', NULL)`)
	if err != nil {
		t.Fatalf("prepare high-cardinality content insertion: %v", err)
	}
	defer insertContent.Close()
	insertFTS, err := tx.PrepareContext(t.Context(), `
INSERT INTO task_search_fts (rowid, title, body, comment)
VALUES (?, NULL, 'needle', NULL)`)
	if err != nil {
		t.Fatalf("prepare high-cardinality FTS insertion: %v", err)
	}
	defer insertFTS.Close()
	for index := range taskCount {
		taskID := fmt.Sprintf("task-%04d", index)
		shortID := fmt.Sprintf("HIG-%d", index+1)
		if _, err := insertTask.ExecContext(t.Context(), taskID, shortID, taskID); err != nil {
			t.Fatalf("insert high-cardinality Task %d: %v", index, err)
		}
		isDone, status := 0, "backlog"
		if index == 0 {
			isDone, status = 1, "done"
		}
		primaryStatusRank := 7
		if isDone != 0 {
			primaryStatusRank = 1
		}
		if _, err := insertStatus.ExecContext(t.Context(), taskID, isDone, status, primaryStatusRank); err != nil {
			t.Fatalf("insert high-cardinality Task status %d: %v", index, err)
		}
		documentID := index + 1
		if _, err := insertDocument.ExecContext(t.Context(), documentID, taskID); err != nil {
			t.Fatalf("insert high-cardinality Task document %d: %v", index, err)
		}
		if _, err := insertContent.ExecContext(t.Context(), documentID); err != nil {
			t.Fatalf("insert high-cardinality Task content %d: %v", index, err)
		}
		if _, err := insertFTS.ExecContext(t.Context(), documentID); err != nil {
			t.Fatalf("insert high-cardinality Task FTS document %d: %v", index, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit high-cardinality task-search fixture: %v", err)
	}
}

func executeTaskSearchDescriptorPageWithCacheMeasurement(
	t *testing.T,
	db *sql.DB,
	params ListTaskSearchPageDescriptorsParams,
) ([]ListTaskSearchPageDescriptorsRow, int) {
	t.Helper()
	connection, err := db.Conn(t.Context())
	if err != nil {
		t.Fatalf("acquire descriptor measurement connection: %v", err)
	}
	defer connection.Close()
	cacheHits := func(reset bool) int {
		var hits int
		err := connection.Raw(func(driverConnection any) error {
			status, ok := driverConnection.(sqlitedriver.DBStatus)
			if !ok {
				return fmt.Errorf("sqlite driver connection %T does not expose DB status", driverConnection)
			}
			var statusErr error
			hits, _, statusErr = status.Status(sqlitedriver.DBStatusCacheHit, reset)
			return statusErr
		})
		if err != nil {
			t.Fatalf("read SQLite cache-hit status: %v", err)
		}
		return hits
	}
	cacheHits(true)
	rows, err := connection.QueryContext(t.Context(), listTaskSearchPageDescriptors, taskSearchPageDescriptorArgs(params)...)
	if err != nil {
		t.Fatalf("execute descriptor page: %v", err)
	}
	defer closeQueryRows(t, rows)
	var descriptors []ListTaskSearchPageDescriptorsRow
	for rows.Next() {
		var descriptor ListTaskSearchPageDescriptorsRow
		if err := rows.Scan(
			&descriptor.TaskID,
			&descriptor.ProjectID,
			&descriptor.ProjectKey,
			&descriptor.ShortID,
			&descriptor.WorkflowID,
			&descriptor.TaskTitle,
			&descriptor.IsDone,
			&descriptor.StatusKind,
			&descriptor.NodeIdsJson,
			&descriptor.AttentionTypesJson,
			&descriptor.DocumentID,
			&descriptor.SourceKind,
			&descriptor.CommentID,
			&descriptor.TotalHitCount,
			&descriptor.Ordinal,
			&descriptor.SourceOrdinal,
			&descriptor.TaskWeightedRank,
			&descriptor.RawSnippet,
		); err != nil {
			t.Fatalf("scan descriptor page: %v", err)
		}
		descriptors = append(descriptors, descriptor)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate descriptor page: %v", err)
	}
	return descriptors, cacheHits(false)
}
