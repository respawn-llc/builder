package pty

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"core/internal/testharness/pty/analyzer"
)

const maxArtifactBytes = 8 * 1024 * 1024

type artifactDiagnostics struct {
	AssertionError string       `json:"assertion_error,omitempty"`
	ProcessExit    *ProcessExit `json:"process_exit,omitempty"`
	ReadLoopDone   bool         `json:"read_loop_done"`
	Dimensions     Dimensions   `json:"dimensions"`
}

type artifactChunk struct {
	Index  int   `json:"index"`
	At     int64 `json:"at_ns"`
	Offset int64 `json:"offset"`
	Length int   `json:"length"`
}

type artifactOperation struct {
	Sequence    int                 `json:"sequence"`
	Kind        OperationKind       `json:"kind"`
	ChunkIndex  int                 `json:"chunk_index"`
	ByteRange   ByteRange           `json:"byte_range"`
	Before      Position            `json:"before"`
	After       Position            `json:"after"`
	Region      Region              `json:"region"`
	CapturedAt  int64               `json:"captured_at_ns"`
	Write       *TextSpan           `json:"write_span,omitempty"`
	Records     []artifactOperation `json:"records,omitempty"`
	PrivateMode *PrivateModeChange  `json:"private_mode,omitempty"`
}

func WriteArtifacts(dir string, capture Capture, analysis Analysis, assertionErr error) error {
	if len(capture.Raw) > 1*1024*1024 {
		return artifactLimitExceeded(len(capture.Raw))
	}
	if len(analysis.Operations) > 16_384 {
		return artifactLimitExceeded(len(analysis.Operations))
	}
	writeText, err := analyzer.WriteTextArena(analysis)
	if err != nil {
		return fmt.Errorf("validate operation write-text arena: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create artifact directory %s: %w", dir, err)
	}
	budget := artifactBudget{}
	if err := budget.write(filepath.Join(dir, "raw.bin"), capture.Raw); err != nil {
		return fmt.Errorf("write raw artifact: %w", err)
	}
	if err := budget.write(filepath.Join(dir, "escaped.txt"), []byte(strconv.QuoteToASCII(string(capture.Raw)))); err != nil {
		return fmt.Errorf("write escaped artifact: %w", err)
	}
	if err := budget.write(filepath.Join(dir, "write_text.bin"), writeText); err != nil {
		return fmt.Errorf("write operation text artifact: %w", err)
	}
	if err := budget.writeJSON(filepath.Join(dir, "chunks.json"), artifactChunks(capture.Chunks)); err != nil {
		return err
	}
	if err := budget.writeJSON(filepath.Join(dir, "operations.json"), artifactOperations(analysis.Operations)); err != nil {
		return err
	}
	if err := budget.write(filepath.Join(dir, "screen.txt"), []byte(analysis.Screen.RenderText())); err != nil {
		return fmt.Errorf("write screen artifact: %w", err)
	}
	diagnostics := artifactDiagnostics{
		ProcessExit:  capture.ProcessExit,
		ReadLoopDone: capture.ReadLoopDone,
		Dimensions:   capture.Dimensions,
	}
	if assertionErr != nil {
		diagnostics.AssertionError = assertionErr.Error()
	}
	if err := budget.writeJSON(filepath.Join(dir, "diagnostics.json"), diagnostics); err != nil {
		return err
	}
	return nil
}

func artifactChunks(chunks []Chunk) []artifactChunk {
	result := make([]artifactChunk, 0, len(chunks))
	var offset int64
	for _, chunk := range chunks {
		result = append(result, artifactChunk{
			Index: chunk.Index, At: chunk.At.Nanoseconds(), Offset: offset, Length: len(chunk.Payload),
		})
		offset += int64(len(chunk.Payload))
	}
	return result
}

func artifactOperations(operations []Operation) []artifactOperation {
	result := make([]artifactOperation, 0, len(operations))
	for _, operation := range operations {
		item := artifactOperation{
			Sequence: operation.Sequence, Kind: operation.Kind, ChunkIndex: operation.ChunkIndex,
			ByteRange: operation.ByteRange, Before: operation.Before, After: operation.After,
			Region: operation.Region, CapturedAt: operation.CapturedAt.Nanoseconds(), PrivateMode: operation.PrivateMode,
		}
		if operation.Write != nil {
			span := operation.Write.Span
			item.Write = &span
		}
		if len(operation.WriteSegments) > 0 || len(operation.Controls) > 0 {
			item.Records = artifactOperations(analyzer.OperationRecords(operation))
		}
		result = append(result, item)
	}
	return result
}

type artifactBudget struct {
	written int
}

func (b *artifactBudget) write(path string, data []byte) error {
	if len(data) > maxArtifactBytes-b.written {
		return artifactLimitExceeded(b.written + len(data))
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	b.written += len(data)
	return nil
}

func (b *artifactBudget) writeJSON(path string, value any) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	writer := &artifactLimitWriter{file: file, remaining: maxArtifactBytes - b.written}
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	encodeErr := encoder.Encode(value)
	closeErr := file.Close()
	if encodeErr != nil {
		if closeErr != nil {
			return fmt.Errorf("encode artifact %s: %w; close artifact: %v", path, encodeErr, closeErr)
		}
		return fmt.Errorf("encode artifact %s: %w", path, encodeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close artifact %s: %w", path, closeErr)
	}
	b.written += writer.written
	return nil
}

type artifactLimitWriter struct {
	file      *os.File
	remaining int
	written   int
}

func (w *artifactLimitWriter) Write(data []byte) (int, error) {
	if len(data) > w.remaining {
		return 0, artifactLimitExceeded(w.written + len(data))
	}
	written, err := w.file.Write(data)
	w.remaining -= written
	w.written += written
	if err != nil {
		return written, err
	}
	if written != len(data) {
		return written, io.ErrShortWrite
	}
	return written, nil
}

func artifactLimitExceeded(observed int) error {
	return &analyzer.EvidenceLimitExceeded{
		Source:   analyzer.EvidenceSourceArtifacts,
		Limit:    maxArtifactBytes,
		Observed: observed,
	}
}
