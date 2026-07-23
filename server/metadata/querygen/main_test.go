package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"
)

func TestGeneratedMetadataQueriesAreFresh(t *testing.T) {
	const inputPath = "../querysrc/queries.sql.tmpl"
	const fragmentPath = "../querysrc/task_label_filter.sql.tmpl"
	const outputPath = "../queries.sql"
	input, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatalf("read query template: %v", err)
	}
	fragment, err := os.ReadFile(fragmentPath)
	if err != nil {
		t.Fatalf("read task label filter template: %v", err)
	}
	want, err := generateQueries(input, fragment)
	if err != nil {
		t.Fatalf("generate metadata queries: %v", err)
	}
	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read generated metadata queries: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("generated metadata queries are stale; run go run ./server/metadata/querygen render --input server/metadata/querysrc/queries.sql.tmpl --fragment server/metadata/querysrc/task_label_filter.sql.tmpl --output server/metadata/queries.sql")
	}
}

func TestAnnotateSourceAddsDiagnosticsExactlyOnce(t *testing.T) {
	source := []byte(`package sqlitegen

func (q *Queries) execute(ctx context.Context, value string) error {
	_, err := q.db.ExecContext(ctx, executeQuery, value)
	return err
}

func (q *Queries) read(ctx context.Context, value string) error {
	row := q.db.QueryRowContext(ctx, readQuery, value)
	err := row.Scan(&value)
	return err
}

func (q *Queries) list(ctx context.Context, value string) ([]string, error) {
	rows, err := q.db.QueryContext(ctx, listQuery, value)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []string
	for rows.Next() {
		var item string
		if err := rows.Scan(&item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}
`)
	annotated, err := annotateSource(source)
	if err != nil {
		t.Fatalf("annotate source: %v", err)
	}
	diagnosticCalls := countDiagnosticCalls(t, annotated)
	if diagnosticCalls != 6 {
		t.Fatalf("diagnostic call count = %d, want 6", diagnosticCalls)
	}
	repeated, err := annotateSource(annotated)
	if err != nil {
		t.Fatalf("annotate source twice: %v", err)
	}
	if !bytes.Equal(repeated, annotated) {
		t.Fatal("diagnostic annotation is not idempotent")
	}
}

func TestGeneratedSQLiteQueriesDiagnosticsAreFresh(t *testing.T) {
	const generatedPath = "../sqlitegen/queries.sql.go"
	current, err := os.ReadFile(generatedPath)
	if err != nil {
		t.Fatalf("read generated SQLite queries: %v", err)
	}
	annotated, err := annotateSource(current)
	if err != nil {
		t.Fatalf("annotate generated SQLite queries: %v", err)
	}
	if !bytes.Equal(annotated, current) {
		t.Fatal("generated SQLite query diagnostics are stale; run go generate ./server/metadata/sqlitegen")
	}
}

func countDiagnosticCalls(t *testing.T, source []byte) int {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "queries.sql.go", source, 0)
	if err != nil {
		t.Fatalf("parse annotated source: %v", err)
	}
	count := 0
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		function, ok := call.Fun.(*ast.Ident)
		if ok && function.Name == "recordQueryError" {
			count++
		}
		return true
	})
	return count
}
