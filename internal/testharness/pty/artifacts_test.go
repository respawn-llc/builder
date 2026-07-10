package pty_test

import (
	"bytes"
	"encoding/json"
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

	for _, name := range []string{"raw.bin", "escaped.txt", "write_text.bin", "chunks.json", "operations.json", "screen.txt", "diagnostics.json"} {
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
	writeText, err := os.ReadFile(filepath.Join(dir, "write_text.bin"))
	if err != nil {
		t.Fatalf("read write text artifact: %v", err)
	}
	if !bytes.Equal(writeText, []byte("old")) {
		t.Fatalf("write text artifact bytes = %#v, want old", writeText)
	}
	operations, err := os.ReadFile(filepath.Join(dir, "operations.json"))
	if err != nil {
		t.Fatalf("read operations artifact: %v", err)
	}
	var encoded []map[string]any
	if err := json.Unmarshal(operations, &encoded); err != nil {
		t.Fatalf("decode operations artifact: %v", err)
	}
	if len(encoded) != 1 {
		t.Fatalf("operation artifact count = %d, want 1", len(encoded))
	}
	if _, exists := encoded[0]["write"]; exists {
		t.Fatalf("operation artifact duplicated write text: %#v", encoded[0])
	}
	if _, exists := encoded[0]["write_span"]; !exists {
		t.Fatalf("operation artifact missing write span: %#v", encoded[0])
	}
}

func TestWriteArtifactsSerializesBatchedRecordsBySpan(t *testing.T) {
	t.Parallel()

	capture, err := pty.NewCapture(
		pty.MustDimensions(2, 8),
		[]pty.Chunk{pty.NewChunk(0, time.Millisecond, []byte("a\x1b[2Jb"))},
	)
	if err != nil {
		t.Fatalf("NewCapture: %v", err)
	}
	analysis, err := pty.Analyze(capture)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	dir := t.TempDir()
	if err := pty.WriteArtifacts(dir, capture, analysis, nil); err != nil {
		t.Fatalf("WriteArtifacts: %v", err)
	}

	encoded, err := os.ReadFile(filepath.Join(dir, "operations.json"))
	if err != nil {
		t.Fatalf("read operations artifact: %v", err)
	}
	var operations []map[string]any
	if err := json.Unmarshal(encoded, &operations); err != nil {
		t.Fatalf("decode operations artifact: %v", err)
	}
	if len(operations) != 1 {
		t.Fatalf("operation count = %d, want 1", len(operations))
	}
	records, ok := operations[0]["records"].([]any)
	if !ok || len(records) != 3 {
		t.Fatalf("records = %#v, want ordered write/erase/write records", operations[0]["records"])
	}
	first, ok := records[0].(map[string]any)
	if !ok || first["kind"] != float64(pty.OperationWrite) {
		t.Fatalf("first record = %#v, want write", records[0])
	}
	second, ok := records[1].(map[string]any)
	if !ok || second["kind"] != float64(pty.OperationErase) {
		t.Fatalf("second record = %#v, want erase", records[1])
	}
	third, ok := records[2].(map[string]any)
	if !ok || third["kind"] != float64(pty.OperationWrite) {
		t.Fatalf("third record = %#v, want write", records[2])
	}
	if bytes.Contains(encoded, []byte(`"write":"`)) {
		t.Fatalf("operations artifact contains duplicated write text: %s", encoded)
	}
}
