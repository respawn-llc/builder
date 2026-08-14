package main

import (
	"path/filepath"
	"testing"
)

func TestMetadataOwnershipLintAcceptsOwnedFixture(t *testing.T) {
	diagnostics, err := lintRepository(filepath.Join("testdata", "valid"))
	if err != nil {
		t.Fatal(err)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v, want none", diagnostics)
	}
}

func TestMetadataOwnershipLintRejectsUnownedFixture(t *testing.T) {
	diagnostics, err := lintRepository(filepath.Join("testdata", "invalid"))
	if err != nil {
		t.Fatal(err)
	}
	want := []diagnostic{
		{
			Rule:     "generated-query-without-operation-boundary",
			Path:     "server/metadata/sqlitegen/broken.go",
			Function: "Broken",
			Detail:   "beforeOperation",
		},
		{
			Rule:     "generated-query-without-operation-boundary",
			Path:     "server/metadata/sqlitegen/broken.go",
			Function: "Broken",
			Detail:   "completeOperation",
		},
		{
			Rule:     "invalid-generated-query-constructor",
			Path:     "server/workflowstore/store.go",
			Function: "Leaky",
			Detail:   "New",
		},
		{
			Rule:     "invalid-generated-query-constructor",
			Path:     "server/workflowstore/store.go",
			Function: "Leaky",
			Detail:   "NewRaw",
		},
		{
			Rule:     "raw-database-call",
			Path:     "server/workflowstore/store.go",
			Function: "Leaky",
			Detail:   "QueryContext",
		},
		{
			Rule:     "transaction-query-constructor-outside-owner",
			Path:     "server/workflowstore/store.go",
			Function: "Leaky",
		},
		{
			Rule:     "transaction-without-settlement",
			Path:     "server/workflowstore/store.go",
			Function: "Leaky",
		},
	}
	if len(diagnostics) != len(want) {
		t.Fatalf("diagnostics = %+v, want %+v", diagnostics, want)
	}
	for index := range want {
		if diagnostics[index] != want[index] {
			t.Fatalf("diagnostic %d = %+v, want %+v", index, diagnostics[index], want[index])
		}
	}
}
