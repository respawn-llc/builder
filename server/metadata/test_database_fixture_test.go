package metadata

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
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
	if err := runMigrations(db); err != nil {
		_ = db.Close()
		t.Fatalf("migrate in-memory metadata schema: %v", err)
	}
	db.SetMaxOpenConns(metadataSQLiteConnectionPoolSize)
	db.SetMaxIdleConns(metadataSQLiteConnectionPoolSize)
	return db
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

	_ = metadataTestSchema(t, migrated)
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
