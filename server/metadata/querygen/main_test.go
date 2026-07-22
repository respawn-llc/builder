package main

import (
	"bytes"
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
		t.Fatal("generated metadata queries are stale; run go run ./server/metadata/querygen --input server/metadata/querysrc/queries.sql.tmpl --fragment server/metadata/querysrc/task_label_filter.sql.tmpl --output server/metadata/queries.sql")
	}
}
