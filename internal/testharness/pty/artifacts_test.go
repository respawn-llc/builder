package pty_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/internal/testharness/pty"
	"core/internal/testharness/pty/analyzer"
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

	for _, name := range []string{
		"raw.bin",
		"escaped.txt",
		"write_text.bin",
		"chunks.json",
		"operations.json",
		"phase-input-dispatches.json",
		"frame-input-dispatches.json",
		"screen.txt",
		"screen.json",
		"diagnostics.json",
	} {
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

func TestWriteArtifactsSerializesBatchedRecordsLosslesslyBySpan(t *testing.T) {
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
	if !ok || first["source"] != "write_segment" || first["kind"] != float64(pty.OperationWrite) {
		t.Fatalf("first record = %#v, want write segment", records[0])
	}
	if _, exists := first["write_span"]; !exists {
		t.Fatalf("first record has no write span: %#v", first)
	}
	second, ok := records[1].(map[string]any)
	if !ok || second["source"] != "control" || second["kind"] != float64(pty.OperationErase) {
		t.Fatalf("second record = %#v, want erase control", records[1])
	}
	third, ok := records[2].(map[string]any)
	if !ok || third["source"] != "write_segment" || third["kind"] != float64(pty.OperationWrite) {
		t.Fatalf("third record = %#v, want write segment", records[2])
	}
	if bytes.Contains(encoded, []byte(`"write":"`)) {
		t.Fatalf("operations artifact contains duplicated write text: %s", encoded)
	}
}

func TestWriteArtifactsRejectsMaximumBatchWithoutMaterializingRecordProjection(t *testing.T) {
	capture, err := pty.NewCapture(pty.MustDimensions(1, 1), nil)
	if err != nil {
		t.Fatalf("NewCapture: %v", err)
	}
	controls := make([]pty.Operation, 100_000)
	for index := range controls {
		controls[index] = pty.Operation{
			Sequence: index,
			Kind:     pty.OperationErase,
			ByteRange: pty.ByteRange{
				Start: int64(index),
				End:   int64(index + 1),
			},
		}
	}
	analysis := pty.Analysis{
		Dimensions: capture.Dimensions,
		Operations: []pty.Operation{{Kind: pty.OperationErase, Controls: controls}},
		Screen:     pty.NewScreenSnapshot(capture.Dimensions),
	}
	err = pty.WriteArtifacts(t.TempDir(), capture, analysis, nil)
	var overflow *pty.EvidenceLimitExceeded
	if !errors.As(err, &overflow) {
		t.Fatalf("WriteArtifacts error = %T %v, want EvidenceLimitExceeded", err, err)
	}
	if overflow.Source != analyzer.EvidenceSourceArtifacts {
		t.Fatalf("overflow source = %s, want artifacts", overflow.Source)
	}
}

func TestWriteArtifactsIncludesBoundedAttachmentsInDirectoryBudget(t *testing.T) {
	dir := t.TempDir()
	capture, err := pty.NewCapture(pty.MustDimensions(2, 8), []pty.Chunk{
		pty.NewChunk(0, 0, []byte("evidence")),
	})
	if err != nil {
		t.Fatalf("NewCapture: %v", err)
	}
	analysis, err := pty.Analyze(capture)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if err := pty.WriteArtifactsWithAttachments(dir, capture, analysis, nil, []pty.ArtifactAttachment{
		{Name: "server.stderr.log", Data: []byte("server diagnostic")},
		{Name: "model.json", Data: []byte(`{"route":"responses"}`)},
	}); err != nil {
		t.Fatalf("WriteArtifactsWithAttachments: %v", err)
	}
	for _, name := range []string{"server.stderr.log", "model.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("attachment %s: %v", name, err)
		}
	}
	if err := pty.WriteArtifactsWithAttachments(dir, capture, analysis, nil, []pty.ArtifactAttachment{{Name: "../escape", Data: []byte("no")}}); err == nil {
		t.Fatal("WriteArtifactsWithAttachments accepted path-traversal attachment")
	}
}
