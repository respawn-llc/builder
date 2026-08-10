package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"

	"core/server/metadata/querysource"
)

func TestMetadataQuerySourceRendersDeterministically(t *testing.T) {
	renderer := testMetadataQueryRenderer(t)
	first, err := renderer.Render()
	if err != nil {
		t.Fatalf("render metadata queries: %v", err)
	}
	second, err := renderer.Render()
	if err != nil {
		t.Fatalf("render metadata queries again: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("metadata query rendering is not deterministic")
	}
	if len(bytes.TrimSpace(first)) == 0 {
		t.Fatal("rendered metadata queries are empty")
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

func (q *Queries) switchExecute(ctx context.Context, execute bool) error {
	switch {
	case execute:
		_, err := q.db.ExecContext(ctx, switchQuery)
		return err
	default:
		return nil
	}
}
`)
	annotated, err := annotateSource(source)
	if err != nil {
		t.Fatalf("annotate source: %v", err)
	}
	diagnosticCalls := countDiagnosticCalls(t, annotated)
	if diagnosticCalls != 7 {
		t.Fatalf("diagnostic call count = %d, want 7", diagnosticCalls)
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

func TestGeneratedTaskSearchPageDescriptorAdapterIsFresh(t *testing.T) {
	const generatedPath = "../sqlitegen/task_search_page_descriptors_generated.go"
	query, err := testMetadataQueryRenderer(t).RenderTaskSearchPageDescriptors()
	if err != nil {
		t.Fatalf("render task-search page descriptor query: %v", err)
	}
	want, err := generateTaskSearchPageDescriptors(query)
	if err != nil {
		t.Fatalf("generate task-search page descriptor adapter: %v", err)
	}
	got, err := os.ReadFile(generatedPath)
	if err != nil {
		t.Fatalf("read generated task-search page descriptor adapter: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("generated task-search page descriptor adapter is stale; run go generate ./server/metadata/sqlitegen")
	}
}

func TestGeneratedTaskSearchSchemaContractAdapterIsFresh(t *testing.T) {
	const generatedPath = "../sqlitegen/task_search_schema_contract_generated.go"
	query, err := testMetadataQueryRenderer(t).RenderTaskSearchSchemaContract()
	if err != nil {
		t.Fatalf("render task-search schema contract query: %v", err)
	}
	want, err := generateTaskSearchSchemaContract(query)
	if err != nil {
		t.Fatalf("generate task-search schema contract adapter: %v", err)
	}
	got, err := os.ReadFile(generatedPath)
	if err != nil {
		t.Fatalf("read generated task-search schema contract adapter: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("generated task-search schema contract adapter is stale; run go generate ./server/metadata/sqlitegen")
	}
}

func testMetadataQueryRenderer(t testing.TB) querysource.Renderer {
	t.Helper()
	renderer, err := querysource.Load("../querysrc")
	if err != nil {
		t.Fatalf("load metadata query source: %v", err)
	}
	return renderer
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
