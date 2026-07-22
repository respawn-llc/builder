package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
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
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repository root")
		}
		dir = parent
	}
}
