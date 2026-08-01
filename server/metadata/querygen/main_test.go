package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

func TestGeneratedMetadataQueriesAreFresh(t *testing.T) {
	const inputPath = "../querysrc/queries.sql.tmpl"
	const fragmentPath = "../querysrc/task_label_filter.sql.tmpl"
	const statusFragmentPath = "../querysrc/task_status_projection.sql.tmpl"
	const outputPath = "../queries.sql"
	input, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatalf("read query template: %v", err)
	}
	fragment, err := os.ReadFile(fragmentPath)
	if err != nil {
		t.Fatalf("read task label filter template: %v", err)
	}
	statusFragment, err := os.ReadFile(statusFragmentPath)
	if err != nil {
		t.Fatalf("read task status projection template: %v", err)
	}
	want, err := generateQueries(input, fragment, statusFragment)
	if err != nil {
		t.Fatalf("generate metadata queries: %v", err)
	}
	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read generated metadata queries: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("generated metadata queries are stale; run go run ./server/metadata/querygen render --input server/metadata/querysrc/queries.sql.tmpl --fragment server/metadata/querysrc/task_label_filter.sql.tmpl --status-fragment server/metadata/querysrc/task_status_projection.sql.tmpl --output server/metadata/queries.sql")
	}
}

func TestTaskStatusProjectionFragmentIsRenderedForListSearchAndBatch(t *testing.T) {
	source, err := os.ReadFile("../queries.sql")
	if err != nil {
		t.Fatalf("read generated metadata queries: %v", err)
	}
	rendered := string(source)
	if got := strings.Count(rendered, "effective_status AS ("); got != 2 {
		t.Fatalf("generated metadata status fragment count = %d, want List and batch selectors", got)
	}
	if !strings.Contains(rendered, "ListWorkflowTaskStatusProjectionByTasks") {
		t.Fatal("generated metadata queries omit the Board/Detail status batch selector")
	}
	descriptors, err := os.ReadFile("../sqlitegen/task_search_page_descriptors_generated.go")
	if err != nil {
		t.Fatalf("read generated task-search descriptors: %v", err)
	}
	if !strings.Contains(string(descriptors), "has_waiting_approval") {
		t.Fatal("generated Task Search selector omits live approval status input")
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
	const inputPath = "../querysrc/queries.sql.tmpl"
	const fragmentPath = "../querysrc/task_label_filter.sql.tmpl"
	const statusFragmentPath = "../querysrc/task_status_projection.sql.tmpl"
	const generatedPath = "../sqlitegen/task_search_page_descriptors_generated.go"
	input, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatalf("read query template: %v", err)
	}
	fragment, err := os.ReadFile(fragmentPath)
	if err != nil {
		t.Fatalf("read task label filter template: %v", err)
	}
	statusFragment, err := os.ReadFile(statusFragmentPath)
	if err != nil {
		t.Fatalf("read task status projection template: %v", err)
	}
	query, err := renderTaskSearchPageDescriptors(input, fragment, statusFragment)
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
	const inputPath = "../querysrc/queries.sql.tmpl"
	const fragmentPath = "../querysrc/task_label_filter.sql.tmpl"
	const statusFragmentPath = "../querysrc/task_status_projection.sql.tmpl"
	const generatedPath = "../sqlitegen/task_search_schema_contract_generated.go"
	input, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatalf("read query template: %v", err)
	}
	fragment, err := os.ReadFile(fragmentPath)
	if err != nil {
		t.Fatalf("read task label filter template: %v", err)
	}
	statusFragment, err := os.ReadFile(statusFragmentPath)
	if err != nil {
		t.Fatalf("read task status projection template: %v", err)
	}
	query, err := renderTaskSearchSchemaContract(input, fragment, statusFragment)
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
