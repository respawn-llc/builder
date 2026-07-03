package pty_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/internal/testharness/pty"
)

func TestWriteArtifactsForAssertionFailure(t *testing.T) {
	t.Parallel()

	capture, err := pty.NewCapture(
		pty.MustDimensions(2, 8),
		[]pty.Chunk{pty.NewChunk(0, time.Millisecond, []byte("old"))},
	)
	if err != nil {
		t.Fatalf("NewCapture: %v", err)
	}
	analysis, err := pty.Analyze(capture)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	assertionErr := pty.NoWritesAbove(analysis, pty.OperationWindow{Start: 0, End: len(analysis.Operations)}, 1)
	if assertionErr == nil {
		t.Fatalf("NoWritesAbove succeeded, want forced assertion failure")
	}

	dir := t.TempDir()
	if err := pty.WriteArtifacts(dir, capture, analysis, assertionErr); err != nil {
		t.Fatalf("WriteArtifacts: %v", err)
	}

	for _, name := range []string{"raw.bin", "escaped.txt", "chunks.json", "operations.json", "screen.txt", "diagnostics.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("artifact %s missing: %v", name, err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "raw.bin"))
	if err != nil {
		t.Fatalf("read raw artifact: %v", err)
	}
	if !bytes.Equal(raw, capture.Raw) {
		t.Fatalf("raw artifact bytes = %#v, want %#v", raw, capture.Raw)
	}
}
