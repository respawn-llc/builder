package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"core/internal/testharness/testsetup"
)

func TestGeneratedLifecycleOutputIsFresh(t *testing.T) {
	repoRoot := metadataRepoRoot(t)
	inputPath := filepath.Join(repoRoot, "server", "metadata", "lifecycle.sql")
	outputPath := filepath.Join(repoRoot, "server", "metadata", "sqlitelifecyclegen", "lifecycle.sql.go")
	input, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatalf("read lifecycle input: %v", err)
	}
	want, err := generateLifecycleGo(input, "sqlitelifecyclegen")
	if err != nil {
		t.Fatalf("generate lifecycle output: %v", err)
	}
	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read generated lifecycle output: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("generated lifecycle output is stale; run go run ./server/metadata/lifecyclegen --input server/metadata/lifecycle.sql --output server/metadata/sqlitelifecyclegen/lifecycle.sql.go --package sqlitelifecyclegen")
	}
}

func metadataRepoRoot(t *testing.T) string {
	return testsetup.RepositoryRoot(t)
}
