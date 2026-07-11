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
	PrivateMode *PrivateModeChange  `json:"private_mode,omitempty"`
	Source      *artifactRecordKind `json:"source,omitempty"`
}

type artifactRecordKind string

const (
	artifactRecordWriteSegment artifactRecordKind = "write_segment"
	artifactRecordControl      artifactRecordKind = "control"
)

type ArtifactAttachment struct {
	Name string
	Data []byte
}

func WriteArtifacts(dir string, capture Capture, analysis Analysis, assertionErr error) error {
	return WriteArtifactsWithAttachments(dir, capture, analysis, assertionErr, nil)
}

func WriteArtifactsWithAttachments(dir string, capture Capture, analysis Analysis, assertionErr error, attachments []ArtifactAttachment) error {
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
	if err := budget.writeOperations(filepath.Join(dir, "operations.json"), analysis.Operations); err != nil {
		return err
	}
	if err := budget.writeJSON(filepath.Join(dir, "phase-input-dispatches.json"), capture.PhaseInputDispatches); err != nil {
		return err
	}
	if err := budget.writeJSON(filepath.Join(dir, "frame-input-dispatches.json"), capture.FrameInputDispatches); err != nil {
		return err
	}
	if err := budget.write(filepath.Join(dir, "screen.txt"), []byte(analysis.Screen.RenderText())); err != nil {
		return fmt.Errorf("write screen artifact: %w", err)
	}
	if err := budget.writeJSON(filepath.Join(dir, "screen.json"), analysis.Screen); err != nil {
		return err
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
	for _, attachment := range attachments {
		if attachment.Name == "" || filepath.Base(attachment.Name) != attachment.Name {
			return fmt.Errorf("invalid artifact attachment name %q", attachment.Name)
		}
		if err := budget.write(filepath.Join(dir, attachment.Name), attachment.Data); err != nil {
			return fmt.Errorf("write artifact attachment %s: %w", attachment.Name, err)
		}
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

func artifactOperationFor(operation Operation, source *artifactRecordKind) artifactOperation {
	item := artifactOperation{
		Sequence: operation.Sequence, Kind: operation.Kind, ChunkIndex: operation.ChunkIndex,
		ByteRange: operation.ByteRange, Before: operation.Before, After: operation.After,
		Region: operation.Region, CapturedAt: operation.CapturedAt.Nanoseconds(),
		PrivateMode: operation.PrivateMode, Source: source,
	}
	if operation.Write != nil {
		span := operation.Write.Span
		item.Write = &span
	}
	return item
}

func artifactWriteSegmentFor(batch Operation, segment WriteSegment) artifactOperation {
	source := artifactRecordWriteSegment
	span := segment.Write.Span
	return artifactOperation{
		Sequence: batch.Sequence, Kind: OperationWrite, ChunkIndex: segment.ChunkIndex,
		ByteRange: segment.ByteRange, Before: segment.Before, After: segment.After,
		Region: segment.Region, CapturedAt: segment.CapturedAt.Nanoseconds(),
		Write: &span, Source: &source,
	}
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

func (b *artifactBudget) writeOperations(path string, operations []Operation) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	writer := &artifactLimitWriter{file: file, remaining: maxArtifactBytes - b.written}
	writeErr := writeArtifactOperationList(writer, operations)
	closeErr := file.Close()
	if writeErr != nil {
		if closeErr != nil {
			return fmt.Errorf("encode artifact %s: %w; close artifact: %v", path, writeErr, closeErr)
		}
		return fmt.Errorf("encode artifact %s: %w", path, writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close artifact %s: %w", path, closeErr)
	}
	b.written += writer.written
	return nil
}

func writeArtifactOperationList(writer io.Writer, operations []Operation) error {
	if _, err := io.WriteString(writer, "["); err != nil {
		return err
	}
	for index, operation := range operations {
		if index > 0 {
			if _, err := io.WriteString(writer, ","); err != nil {
				return err
			}
		}
		if err := writeArtifactOperation(writer, operation, nil); err != nil {
			return err
		}
	}
	_, err := io.WriteString(writer, "]\n")
	return err
}

func writeArtifactOperation(writer io.Writer, operation Operation, source *artifactRecordKind) error {
	header, err := json.Marshal(artifactOperationFor(operation, source))
	if err != nil {
		return err
	}
	if len(operation.WriteSegments) == 0 && len(operation.Controls) == 0 {
		_, err := writer.Write(header)
		return err
	}
	if _, err := writer.Write(header[:len(header)-1]); err != nil {
		return err
	}
	if _, err := io.WriteString(writer, `,"records":[`); err != nil {
		return err
	}
	if err := writeArtifactBatchRecords(writer, operation); err != nil {
		return err
	}
	_, err = io.WriteString(writer, "]}")
	return err
}

func writeArtifactBatchRecords(writer io.Writer, batch Operation) error {
	segmentIndex, controlIndex := 0, 0
	recordIndex := 0
	for segmentIndex < len(batch.WriteSegments) || controlIndex < len(batch.Controls) {
		if recordIndex > 0 {
			if _, err := io.WriteString(writer, ","); err != nil {
				return err
			}
		}
		if nextBatchRecordIsSegment(batch, segmentIndex, controlIndex) {
			if err := writeArtifactWriteSegment(writer, batch, batch.WriteSegments[segmentIndex]); err != nil {
				return err
			}
			segmentIndex++
		} else {
			source := artifactRecordControl
			if err := writeArtifactOperation(writer, batch.Controls[controlIndex], &source); err != nil {
				return err
			}
			controlIndex++
		}
		recordIndex++
	}
	return nil
}

func nextBatchRecordIsSegment(batch Operation, segmentIndex, controlIndex int) bool {
	if segmentIndex == len(batch.WriteSegments) {
		return false
	}
	if controlIndex == len(batch.Controls) {
		return true
	}
	segment := batch.WriteSegments[segmentIndex].ByteRange
	control := batch.Controls[controlIndex].ByteRange
	if segment.Start != control.Start {
		return segment.Start < control.Start
	}
	return segment.End <= control.End
}

func writeArtifactWriteSegment(writer io.Writer, batch Operation, segment WriteSegment) error {
	encoded, err := json.Marshal(artifactWriteSegmentFor(batch, segment))
	if err != nil {
		return err
	}
	_, err = writer.Write(encoded)
	return err
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
