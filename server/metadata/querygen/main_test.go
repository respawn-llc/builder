package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"

	"core/server/metadata"
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

func TestAnnotateSourceAddsOperationOwnershipExactlyOnce(t *testing.T) {
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
	beforeCalls, completeCalls := countOperationOwnershipCalls(t, annotated)
	if beforeCalls != 4 || completeCalls != 4 {
		t.Fatalf("operation ownership calls = before %d, complete %d; want 4 each", beforeCalls, completeCalls)
	}
	repeated, err := annotateSource(annotated)
	if err != nil {
		t.Fatalf("annotate source twice: %v", err)
	}
	if !bytes.Equal(repeated, annotated) {
		t.Fatal("operation ownership annotation is not idempotent")
	}
}

func TestGeneratedSQLiteQueriesOperationOwnershipIsFresh(t *testing.T) {
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
		t.Fatal("generated SQLite query operation ownership is stale; run go generate ./server/metadata/sqlitegen")
	}
}

func TestGeneratedSQLiteDBAdapterIsFresh(t *testing.T) {
	const generatedPath = "../sqlitegen/db.go"
	current, err := os.ReadFile(generatedPath)
	if err != nil {
		t.Fatalf("read generated SQLite DB adapter: %v", err)
	}
	temp := t.TempDir() + "/db.go"
	if err := generateQueriesDB(temp); err != nil {
		t.Fatalf("generate SQLite DB adapter: %v", err)
	}
	want, err := os.ReadFile(temp)
	if err != nil {
		t.Fatalf("read expected SQLite DB adapter: %v", err)
	}
	if !bytes.Equal(current, want) {
		t.Fatal("generated SQLite DB adapter is stale; run go generate ./server/metadata/sqlitegen")
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

func TestGeneratedWorkflowSessionDependencyInvalidationAdapterIsFresh(t *testing.T) {
	const generatedPath = "../sqlitegen/workflow_session_dependency_invalidation_generated.go"
	query, err := testMetadataQueryRenderer(t).RenderWorkflowSessionDependencyInvalidation()
	if err != nil {
		t.Fatalf("render Workflow Session dependency invalidation query: %v", err)
	}
	want, err := generateWorkflowSessionDependencyInvalidation(query)
	if err != nil {
		t.Fatalf("generate Workflow Session dependency invalidation adapter: %v", err)
	}
	got, err := os.ReadFile(generatedPath)
	if err != nil {
		t.Fatalf("read generated Workflow Session dependency invalidation adapter: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("generated Workflow Session dependency invalidation adapter is stale; run go generate ./server/metadata/sqlitegen")
	}
}

func testMetadataQueryRenderer(t testing.TB) metadata.QuerySourceRenderer {
	t.Helper()
	renderer, err := metadata.LoadQuerySourceRenderer("../querysrc")
	if err != nil {
		t.Fatalf("load metadata query source: %v", err)
	}
	return renderer
}

func countOperationOwnershipCalls(t *testing.T, source []byte) (int, int) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "queries.sql.go", source, 0)
	if err != nil {
		t.Fatalf("parse annotated source: %v", err)
	}
	before := 0
	complete := 0
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		function, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch function.Sel.Name {
		case "beforeOperation":
			before++
		case "completeOperation":
			complete++
		}
		return true
	})
	return before, complete
}
