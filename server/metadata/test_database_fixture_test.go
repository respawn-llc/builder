package metadata

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"core/server/metadata/sqlitegen"
)

var metadataTestDatabaseSequence atomic.Uint64

//go:embed testdata/latest_schema.sql
var latestMetadataTestSchema []byte

func openInMemoryMetadataTestStore(t *testing.T, persistenceRoot string) *Store {
	t.Helper()
	db := openLatestMetadataTestDatabase(t)
	store := &Store{
		persistenceRoot: persistenceRoot,
		db:              db,
		queries:         sqlitegen.New(db),
	}
	if err := store.BackfillProjectKeys(context.Background()); err != nil {
		_ = db.Close()
		t.Fatalf("backfill in-memory metadata project keys: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close in-memory metadata store: %v", err)
		}
	})
	return store
}

func openLatestMetadataTestDatabase(t testing.TB) *sql.DB {
	t.Helper()
	db := openEmptyMetadataTestDatabase(t)
	if _, err := db.Exec(string(executableLatestMetadataTestSchema())); err != nil {
		_ = db.Close()
		t.Fatalf("create latest in-memory metadata schema: %v", err)
	}
	db.SetMaxOpenConns(metadataSQLiteConnectionPoolSize)
	db.SetMaxIdleConns(metadataSQLiteConnectionPoolSize)
	return db
}

func executableLatestMetadataTestSchema() []byte {
	lines := bytes.Split(latestMetadataTestSchema, []byte{'\n'})
	executable := make([][]byte, 0, len(lines))
	for _, line := range lines {
		if bytes.HasPrefix(line, []byte("CREATE TABLE 'task_search_fts_")) ||
			bytes.HasPrefix(line, []byte("CREATE TABLE 'task_search_short_id_fts_")) {
			continue
		}
		executable = append(executable, line)
	}
	return bytes.Join(executable, []byte{'\n'})
}

func openEmptyMetadataTestDatabase(t testing.TB) *sql.DB {
	t.Helper()
	if err := registerMetadataSQLiteCollations(); err != nil {
		t.Fatalf("register metadata SQLite extensions: %v", err)
	}
	dsn := fmt.Sprintf(
		"file:kent-metadata-test-%d?mode=memory&cache=shared&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)",
		metadataTestDatabaseSequence.Add(1),
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open in-memory metadata database: %v", err)
	}
	db.SetMaxOpenConns(1)
	return db
}

func TestAllMetadataMigrationsMatchLatestInMemorySchema(t *testing.T) {
	migrated := openEmptyMetadataTestDatabase(t)
	t.Cleanup(func() { _ = migrated.Close() })
	if err := runMigrations(migrated); err != nil {
		t.Fatalf("run full metadata migration chain in memory: %v", err)
	}

	migratedSchema := metadataTestSchema(t, migrated)
	if !bytes.Equal(migratedSchema, latestMetadataTestSchema) {
		t.Fatalf(
			"full migration schema does not match testdata/latest_schema.sql; regenerate it with ./scripts/dump-metadata-schema.sh\nwant_sha256=%x\ngot_sha256=%x",
			sha256Bytes(latestMetadataTestSchema),
			sha256Bytes(migratedSchema),
		)
	}
	var integrity string
	if err := migrated.QueryRowContext(t.Context(), "PRAGMA integrity_check").Scan(&integrity); err != nil {
		t.Fatalf("migration integrity check: %v", err)
	}
	if integrity != "ok" {
		t.Fatalf("migration integrity check = %q, want ok", integrity)
	}
	rows, err := migrated.QueryContext(t.Context(), "PRAGMA foreign_key_check")
	if err != nil {
		t.Fatalf("migration foreign-key check: %v", err)
	}
	if rows.Next() {
		_ = rows.Close()
		t.Fatal("migration foreign-key check reported violations")
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close migration foreign-key check: %v", err)
	}
}

func TestDropSessionLastSequenceMigrationPreservesSessionRows(t *testing.T) {
	db := openEmptyMetadataTestDatabase(t)
	t.Cleanup(func() { _ = db.Close() })
	provider, err := newMetadataMigrationProvider(db)
	if err != nil {
		t.Fatalf("create metadata migration provider: %v", err)
	}
	if _, err := provider.UpTo(t.Context(), 85); err != nil {
		t.Fatalf("migrate metadata schema to version 85: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), `
		INSERT INTO projects (
			id,
			display_name,
			created_at_unix_ms,
			updated_at_unix_ms,
			metadata_json,
			project_key,
			next_task_seq,
			default_project_workflow_link_id,
			primary_workspace_id
		) VALUES ('project-last-sequence', 'Project', 1, 1, '{}', 'SEQ', 1, NULL, '')
	`); err != nil {
		t.Fatalf("seed Project: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), `
		INSERT INTO sessions (
			id,
			project_id,
			artifact_relpath,
			name,
			first_prompt_preview,
			input_draft,
			category,
			created_at_unix_ms,
			updated_at_unix_ms,
			last_sequence,
			model_request_count,
			launch_visible,
			cwd_relpath,
			continuation_json,
			locked_json,
			usage_state_json,
			metadata_json
		) VALUES (
			'session-last-sequence',
			'project-last-sequence',
			'projects/project-last-sequence/sessions/session-last-sequence',
			'Session',
			'preserved preview',
			'',
			'main',
			1,
			2,
			42,
			3,
			1,
			'.',
			'{}',
			'{}',
			'{}',
			'{}'
		)
	`); err != nil {
		t.Fatalf("seed Session: %v", err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("apply Session sequence-authority migration: %v", err)
	}
	var (
		sessionID          string
		firstPromptPreview string
	)
	if err := db.QueryRowContext(
		t.Context(),
		`SELECT id, first_prompt_preview FROM sessions WHERE id = 'session-last-sequence'`,
	).Scan(&sessionID, &firstPromptPreview); err != nil {
		t.Fatalf("read migrated Session: %v", err)
	}
	if sessionID != "session-last-sequence" || firstPromptPreview != "preserved preview" {
		t.Fatalf("migrated Session = %q/%q", sessionID, firstPromptPreview)
	}
	rows, err := db.QueryContext(t.Context(), `PRAGMA table_info(sessions)`)
	if err != nil {
		t.Fatalf("inspect migrated Session schema: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultSQL sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultSQL, &primaryKey); err != nil {
			t.Fatalf("scan Session column: %v", err)
		}
		if name == "last_sequence" {
			t.Fatal("last_sequence remains in migrated Session schema")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate Session columns: %v", err)
	}
}

func TestMetadataTestsDoNotOpenFileBackedDatabases(t *testing.T) {
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve metadata test source path")
	}
	metadataDirectory := filepath.Dir(sourcePath)
	err := filepath.WalkDir(metadataDirectory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		fileSet := token.NewFileSet()
		file, err := parser.ParseFile(fileSet, path, nil, 0)
		if err != nil {
			return fmt.Errorf("parse metadata test file %s: %w", path, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if allowedMetadataTestDatabaseOpen(file, call) {
				return true
			}
			if name, forbidden := forbiddenMetadataTestDatabaseOpen(file, call); forbidden {
				position := fileSet.Position(call.Pos())
				t.Errorf("%s calls %s; metadata tests must use an in-memory SQLite fixture", position, name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func allowedMetadataTestDatabaseOpen(file *ast.File, call *ast.CallExpr) bool {
	position := call.Pos()
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil || position < function.Body.Pos() || position > function.Body.End() {
			continue
		}
		return function.Name.Name == "openEmptyMetadataTestDatabase" ||
			function.Name.Name == "openGraphEntityIDSQLiteTestDatabase" ||
			function.Name.Name == "openSQLiteFixture" ||
			function.Name.Name == "TestGeneratedSingleRowNoRowsDiagnosticsArePolicyScoped"
	}
	return false
}

func forbiddenMetadataTestDatabaseOpen(file *ast.File, call *ast.CallExpr) (string, bool) {
	switch function := call.Fun.(type) {
	case *ast.Ident:
		if function.Name == "Open" || function.Name == "OpenAtPath" {
			return function.Name, true
		}
	case *ast.SelectorExpr:
		packageName, ok := function.X.(*ast.Ident)
		if !ok {
			return "", false
		}
		if function.Sel.Name == "OpenAtPath" {
			return packageName.Name + ".OpenAtPath", true
		}
		if function.Sel.Name != "Open" {
			return "", false
		}
		for _, importSpec := range file.Imports {
			importPath := strings.Trim(importSpec.Path.Value, `"`)
			importName := filepath.Base(importPath)
			if importSpec.Name != nil {
				importName = importSpec.Name.Name
			}
			if importName != packageName.Name {
				continue
			}
			if importPath == "database/sql" || importPath == "core/server/metadata" {
				return packageName.Name + ".Open", true
			}
		}
	}
	return "", false
}

func metadataTestSchema(t testing.TB, db *sql.DB) []byte {
	t.Helper()
	rows, err := db.Query(`
SELECT type, name, CAST(sql AS TEXT)
FROM sqlite_schema
WHERE sql IS NOT NULL
  AND name != 'sqlite_sequence'
ORDER BY
  CASE type
    WHEN 'table' THEN 0
    WHEN 'view' THEN 1
    WHEN 'index' THEN 2
    WHEN 'trigger' THEN 3
  END,
  name`)
	if err != nil {
		t.Fatalf("query metadata test schema: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var schema bytes.Buffer
	for rows.Next() {
		var kind, name, ddl string
		if err := rows.Scan(&kind, &name, &ddl); err != nil {
			t.Fatalf("scan metadata test schema: %v", err)
		}
		if schema.Len() > 0 {
			schema.WriteByte('\n')
		}
		schema.WriteString(ddl)
		schema.WriteString(";\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate metadata test schema: %v", err)
	}
	return schema.Bytes()
}

func sha256Bytes(value []byte) [32]byte {
	return sha256.Sum256(value)
}
