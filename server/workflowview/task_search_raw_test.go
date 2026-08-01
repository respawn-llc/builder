package workflowview

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"core/server/metadata"
	"core/shared/serverapi"

	sqlitedriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func TestTaskSearchRawFTS5UsesMarkerFreeKnownColumnSnippets(t *testing.T) {
	fixture, search := newTaskSearchFixture(t, false)
	task := createTaskSearchTask(
		t,
		fixture,
		"Different title",
		strings.Repeat("prefix ", 40)+"needle "+strings.Repeat("suffix ", 40),
	)
	response, err := search.Search(fixture.ctx, serverapi.TaskSearchRequest{
		Mode:     serverapi.TaskSearchModeFTS5,
		Query:    "body:needle",
		Context:  2,
		PageSize: serverapi.TaskSearchDefaultPageSize,
	})
	if err != nil {
		t.Fatalf("raw Search: %v", err)
	}
	if len(response.Groups) != 1 || response.Groups[0].TaskID != string(task.ID) {
		t.Fatalf("raw search response = %+v", response)
	}
	hits := response.Groups[0].Hits
	if len(hits) != 1 || hits[0].Source.Kind != serverapi.TaskSearchSourceKindBody || hits[0].FTS5 == nil || hits[0].FTS5.Snippet != "eedl" {
		t.Fatalf("raw search hits = %+v", hits)
	}
}

func TestTaskSearchRawFTS5PreservesSourceLocalExpressionSemantics(t *testing.T) {
	fixture, search := newTaskSearchFixture(t, false)
	task := createTaskSearchTask(t, fixture, "needle title", "needle body")
	if _, err := fixture.store.AddComment(fixture.ctx, task.ID, "needle comment", "user", "user-1"); err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	createTaskSearchTask(t, fixture, "alphaone", "betatwo")
	createTaskSearchTask(t, fixture, "ab", "a")

	response, err := search.Search(fixture.ctx, serverapi.TaskSearchRequest{
		Mode:            serverapi.TaskSearchModeFTS5,
		Query:           "needle",
		Context:         serverapi.TaskSearchDefaultContext,
		IncludeComments: true,
		PageSize:        serverapi.TaskSearchDefaultPageSize,
	})
	if err != nil {
		t.Fatalf("raw Search: %v", err)
	}
	if len(response.Groups) != 1 || response.Groups[0].TaskID != string(task.ID) {
		t.Fatalf("raw response = %+v", response)
	}
	hits := response.Groups[0].Hits
	if len(hits) != 3 ||
		hits[0].Source.Kind != serverapi.TaskSearchSourceKindTitle ||
		hits[1].Source.Kind != serverapi.TaskSearchSourceKindBody ||
		hits[2].Source.Kind != serverapi.TaskSearchSourceKindComment {
		t.Fatalf("raw source order = %+v, want title/body/comment", hits)
	}

	withoutComments, err := search.Search(fixture.ctx, serverapi.TaskSearchRequest{
		Mode:     serverapi.TaskSearchModeFTS5,
		Query:    "comment:needle",
		Context:  serverapi.TaskSearchDefaultContext,
		PageSize: serverapi.TaskSearchDefaultPageSize,
	})
	if err != nil {
		t.Fatalf("raw Comment-only Search without inclusion: %v", err)
	}
	if len(withoutComments.Groups) != 0 {
		t.Fatalf("raw Comment-only Search without inclusion = %+v, want no matches", withoutComments)
	}

	splitTerms, err := search.Search(fixture.ctx, serverapi.TaskSearchRequest{
		Mode:     serverapi.TaskSearchModeFTS5,
		Query:    "alphaone betatwo",
		Context:  serverapi.TaskSearchDefaultContext,
		PageSize: serverapi.TaskSearchDefaultPageSize,
	})
	if err != nil {
		t.Fatalf("raw split-term Search: %v", err)
	}
	if len(splitTerms.Groups) != 0 {
		t.Fatalf("raw split-term Search = %+v, want no matches", splitTerms)
	}

	for _, test := range []struct {
		name            string
		query           string
		includeComments bool
		wantKind        serverapi.TaskSearchSourceKind
	}{
		{name: "title column", query: "title:needle", wantKind: serverapi.TaskSearchSourceKindTitle},
		{name: "body column", query: "body:needle", wantKind: serverapi.TaskSearchSourceKindBody},
		{name: "comment column", query: "comment:needle", includeComments: true, wantKind: serverapi.TaskSearchSourceKindComment},
		{name: "body phrase", query: `body:"needle body"`, wantKind: serverapi.TaskSearchSourceKindBody},
	} {
		t.Run(test.name, func(t *testing.T) {
			filtered, err := search.Search(fixture.ctx, serverapi.TaskSearchRequest{
				Mode:            serverapi.TaskSearchModeFTS5,
				Query:           test.query,
				Context:         serverapi.TaskSearchDefaultContext,
				IncludeComments: test.includeComments,
				PageSize:        serverapi.TaskSearchDefaultPageSize,
			})
			if err != nil {
				t.Fatalf("raw Search: %v", err)
			}
			if len(filtered.Groups) != 1 ||
				filtered.Groups[0].TaskID != string(task.ID) ||
				len(filtered.Groups[0].Hits) != 1 ||
				filtered.Groups[0].Hits[0].Source.Kind != test.wantKind {
				t.Fatalf("raw %s response = %+v", test.name, filtered)
			}
		})
	}

	boolean, err := search.Search(fixture.ctx, serverapi.TaskSearchRequest{
		Mode:            serverapi.TaskSearchModeFTS5,
		Query:           "title:needle OR comment:needle",
		Context:         serverapi.TaskSearchDefaultContext,
		IncludeComments: true,
		PageSize:        serverapi.TaskSearchDefaultPageSize,
	})
	if err != nil {
		t.Fatalf("raw boolean Search: %v", err)
	}
	if len(boolean.Groups) != 1 ||
		len(boolean.Groups[0].Hits) != 2 ||
		boolean.Groups[0].Hits[0].Source.Kind != serverapi.TaskSearchSourceKindTitle ||
		boolean.Groups[0].Hits[1].Source.Kind != serverapi.TaskSearchSourceKindComment {
		t.Fatalf("raw boolean response = %+v, want title then Comment", boolean)
	}

	for _, rawTerm := range []string{"a", "ab"} {
		if _, err := search.Search(fixture.ctx, serverapi.TaskSearchRequest{
			Mode:     serverapi.TaskSearchModeFTS5,
			Query:    rawTerm,
			Context:  serverapi.TaskSearchDefaultContext,
			PageSize: serverapi.TaskSearchDefaultPageSize,
		}); err != nil {
			t.Fatalf("short raw term %q was rejected: %v", rawTerm, err)
		}
	}
}

func TestTaskSearchRawSchemaFailuresRemainOperational(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*metadata.Store) error
	}{
		{
			name: "missing FTS",
			mutate: func(store *metadata.Store) error {
				_, err := store.DB().Exec("DROP TABLE task_search_fts")
				return err
			},
		},
		{
			name: "ordinary same-name table",
			mutate: func(store *metadata.Store) error {
				if _, err := store.DB().Exec("DROP TABLE task_search_fts"); err != nil {
					return err
				}
				_, err := store.DB().Exec("CREATE TABLE task_search_fts (title TEXT, body TEXT, comment TEXT)")
				return err
			},
		},
		{
			name: "incomplete content view",
			mutate: func(store *metadata.Store) error {
				if _, err := store.DB().Exec("DROP VIEW task_search_content"); err != nil {
					return err
				}
				_, err := store.DB().Exec("CREATE VIEW task_search_content AS SELECT document_id, NULL AS title, NULL AS body FROM task_search_documents")
				return err
			},
		},
		{
			name: "nonpartial mapping index",
			mutate: func(store *metadata.Store) error {
				if _, err := store.DB().Exec("DROP INDEX task_search_documents_task_title_unique"); err != nil {
					return err
				}
				_, err := store.DB().Exec("CREATE INDEX task_search_documents_task_title_unique ON task_search_documents(task_id)")
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, search := newTaskSearchFixture(t, false)
			createTaskSearchTask(t, fixture, "Schema contract", "needle")
			if err := test.mutate(fixture.metadata); err != nil {
				t.Fatalf("mutate schema: %v", err)
			}
			_, err := search.Search(fixture.ctx, serverapi.TaskSearchRequest{
				Mode:     serverapi.TaskSearchModeFTS5,
				Query:    `"`,
				Context:  serverapi.TaskSearchDefaultContext,
				PageSize: serverapi.TaskSearchDefaultPageSize,
			})
			var searchErr *serverapi.TaskSearchError
			if err == nil || errors.As(err, &searchErr) {
				t.Fatalf("schema failure error = %T %v", err, err)
			}
		})
	}
}

func TestTaskSearchRawFTS5SQLiteErrorsRemainOperational(t *testing.T) {
	fixture, search := newTaskSearchFixture(t, false)
	_, err := search.Search(fixture.ctx, serverapi.TaskSearchRequest{
		Mode:     serverapi.TaskSearchModeFTS5,
		Query:    `"`,
		Context:  serverapi.TaskSearchDefaultContext,
		PageSize: serverapi.TaskSearchDefaultPageSize,
	})
	var searchErr *serverapi.TaskSearchError
	if err == nil || errors.As(err, &searchErr) {
		t.Fatalf("raw FTS5 SQLite error = %T %v, want a generic operational error", err, err)
	}
}

func TestTaskSearchKeepsSQLiteLockContentionOperational(t *testing.T) {
	fixture, search := newTaskSearchFixture(t, false)
	createTaskSearchTask(t, fixture, "needle title", "needle body")
	if _, err := fixture.metadata.DB().ExecContext(fixture.ctx, "PRAGMA journal_mode = DELETE"); err != nil {
		t.Fatalf("switch isolated fixture to rollback journaling: %v", err)
	}
	fixture.metadata.DB().SetMaxOpenConns(1)
	fixture.metadata.DB().SetMaxIdleConns(1)
	searchConnection, err := fixture.metadata.DB().Conn(fixture.ctx)
	if err != nil {
		t.Fatalf("acquire search database connection: %v", err)
	}
	if _, err := searchConnection.ExecContext(fixture.ctx, "PRAGMA busy_timeout = 1"); err != nil {
		if closeErr := searchConnection.Close(); closeErr != nil {
			t.Errorf("close search database connection: %v", closeErr)
		}
		t.Fatalf("set search connection busy timeout: %v", err)
	}
	if err := searchConnection.Close(); err != nil {
		t.Fatalf("release configured search database connection: %v", err)
	}

	databasePath := filepath.Join(fixture.metadata.PersistenceRoot(), "db", "main.sqlite3")
	lockURL := url.URL{Scheme: "file", Path: databasePath}
	lockURL.RawQuery = "_pragma=busy_timeout(1)"
	lockDB, err := sql.Open("sqlite", lockURL.String())
	if err != nil {
		t.Fatalf("open lock database connection: %v", err)
	}
	t.Cleanup(func() {
		if err := lockDB.Close(); err != nil {
			t.Errorf("close lock database: %v", err)
		}
	})
	lockConnection, err := lockDB.Conn(fixture.ctx)
	if err != nil {
		t.Fatalf("acquire lock database connection: %v", err)
	}
	t.Cleanup(func() {
		if err := lockConnection.Close(); err != nil {
			t.Errorf("close lock database connection: %v", err)
		}
	})
	if _, err := lockConnection.ExecContext(fixture.ctx, "PRAGMA locking_mode = EXCLUSIVE"); err != nil {
		t.Fatalf("set exclusive locking mode: %v", err)
	}
	if _, err := lockConnection.ExecContext(fixture.ctx, "BEGIN EXCLUSIVE"); err != nil {
		t.Fatalf("acquire exclusive SQLite lock: %v", err)
	}
	t.Cleanup(func() {
		if _, err := lockConnection.ExecContext(context.Background(), "ROLLBACK"); err != nil {
			t.Errorf("release exclusive SQLite lock: %v", err)
		}
	})

	busyCtx, cancel := context.WithTimeout(fixture.ctx, 250*time.Millisecond)
	t.Cleanup(cancel)
	_, err = search.Search(busyCtx, serverapi.TaskSearchRequest{
		Mode:     serverapi.TaskSearchModeFTS5,
		Query:    `"`,
		Context:  serverapi.TaskSearchDefaultContext,
		PageSize: serverapi.TaskSearchDefaultPageSize,
	})
	var searchErr *serverapi.TaskSearchError
	if err == nil || errors.As(err, &searchErr) {
		t.Fatalf("SQLite lock contention error = %T %v, want an operational error instead of a raw FTS5 error", err, err)
	}
	var sqliteErr *sqlitedriver.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != sqlite3.SQLITE_BUSY {
		t.Fatalf("SQLite lock contention error = %T %v, want SQLITE_BUSY", err, err)
	}
}

func TestTaskSearchSearchDoesNotPreflightEntirePersistenceCorpus(t *testing.T) {
	fixture, search := newTaskSearchFixture(t, false)
	healthy := createTaskSearchTask(t, fixture, "Healthy", "healthy needle")
	if _, err := fixture.metadata.DB().Exec(`DROP TRIGGER task_search_task_insert`); err != nil {
		t.Fatalf("drop Task insert trigger: %v", err)
	}
	if _, err := fixture.metadata.DB().Exec(`
CREATE TRIGGER task_search_task_insert
AFTER INSERT ON tasks
BEGIN
    SELECT 1;
END`); err != nil {
		t.Fatalf("create no-op Task insert trigger: %v", err)
	}
	for index := range 64 {
		createTaskSearchTask(t, fixture, fmt.Sprintf("Drift %d", index), "unrelated")
	}
	response, err := search.Search(fixture.ctx, taskSearchRequest("healthy needle"))
	if err != nil {
		t.Fatalf("Search across persistence drift: %v", err)
	}
	if len(response.Groups) != 1 || response.Groups[0].TaskID != string(healthy.ID) {
		t.Fatalf("Search across persistence drift = %+v, want the valid indexed Task", response)
	}
}

func TestTaskSearchRanksEquivalentBodyBeforeComment(t *testing.T) {
	fixture, search := newTaskSearchFixture(t, false)
	bodyTask := createTaskSearchTask(t, fixture, "Body", "needle")
	commentTask := createTaskSearchTask(t, fixture, "Comment", "other")
	if _, err := fixture.store.AddComment(fixture.ctx, commentTask.ID, "needle", "user", "user-1"); err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	response, err := search.Search(fixture.ctx, serverapi.TaskSearchRequest{
		Mode:            serverapi.TaskSearchModeFTS5,
		Query:           "needle",
		Context:         serverapi.TaskSearchDefaultContext,
		IncludeComments: true,
		PageSize:        serverapi.TaskSearchDefaultPageSize,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(response.Groups) != 2 ||
		response.Groups[0].TaskID != string(bodyTask.ID) ||
		response.Groups[1].TaskID != string(commentTask.ID) {
		t.Fatalf("ranked groups = %+v", response.Groups)
	}
}
