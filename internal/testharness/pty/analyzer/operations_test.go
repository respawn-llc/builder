package analyzer

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v3/vt"
)

func TestRecordPutMergesContiguousSpansWithoutCopyingPriorText(t *testing.T) {
	backend := newTracingBackend(MustDimensions(1, 8))
	backend.beginByte(Chunk{}, 0)
	backend.recordPut(Position{Row: 0, Col: 0}, Cell{Content: "a"})
	backend.beginByte(Chunk{}, 1)
	backend.recordPut(Position{Row: 0, Col: 1}, Cell{Content: "b"})

	operations := backend.operations()
	if len(operations) != 1 {
		t.Fatalf("operation count = %d, want 1", len(operations))
	}
	if got := operations[0].Write.Text(); got != "ab" {
		t.Fatalf("merged write = %q, want ab", got)
	}
	if got := len(backend.writeText.bytes); got != 2 {
		t.Fatalf("arena bytes = %d, want 2", got)
	}
	if operations[0].Write.Span != (TextSpan{Start: 0, End: 2}) {
		t.Fatalf("write span = %+v, want [0,2)", operations[0].Write.Span)
	}
}

func TestWriteTextArenaRejectsAggregateOverflow(t *testing.T) {
	arena := newDefaultWriteTextArena()
	if _, err := arena.append(strings.Repeat("x", maxOperationTextBytes)); err != nil {
		t.Fatalf("append limit-sized payload: %v", err)
	}
	if _, err := arena.append("x"); err == nil {
		t.Fatal("append beyond limit succeeded")
	} else {
		var overflow *EvidenceLimitExceeded
		if !errors.As(err, &overflow) {
			t.Fatalf("append error = %T %v, want EvidenceLimitExceeded", err, err)
		}
		if overflow.Source != EvidenceSourceOperationText || overflow.Observed != maxOperationTextBytes+1 {
			t.Fatalf("overflow = %+v", overflow)
		}
	}
}

func TestIdenticalRedrawAfterEraseRemainsSemanticOperation(t *testing.T) {
	capture, err := NewCapture(MustDimensions(2, 8), []Chunk{
		NewChunk(0, 0, []byte("x\x1b[2Jx")),
	})
	if err != nil {
		t.Fatalf("NewCapture: %v", err)
	}
	analysis, err := Analyze(capture)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	writes := 0
	for _, operation := range analysis.Operations {
		if operation.Kind == OperationWrite {
			for _, segment := range operation.WriteSegments {
				if segment.Write.Text() == "x" {
					writes++
				}
			}
		}
	}
	if writes != 2 {
		t.Fatalf("identical redraw writes = %d, want 2; operations=%#v", writes, analysis.Operations)
	}
}

func TestIdenticalRedrawAfterTerminalResetRemainsSemanticOperation(t *testing.T) {
	capture, err := NewCapture(MustDimensions(2, 8), []Chunk{
		NewChunk(0, 0, []byte("x\x1bcx")),
	})
	if err != nil {
		t.Fatalf("NewCapture: %v", err)
	}
	analysis, err := Analyze(capture)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	writes := 0
	for _, operation := range analysis.Operations {
		if operation.Kind == OperationWrite && operation.Write != nil && operation.Write.Text() == "x" {
			writes++
		}
	}
	if writes != 2 {
		t.Fatalf("post-reset redraw writes = %d, want 2; operations=%#v", writes, analysis.Operations)
	}
}

func TestOperationRecordsPreservesWriteControlOrderWithinBatch(t *testing.T) {
	capture, err := NewCapture(MustDimensions(2, 8), []Chunk{
		NewChunk(0, 0, []byte("a\x1b[2Jb")),
	})
	if err != nil {
		t.Fatalf("NewCapture: %v", err)
	}
	analysis, err := Analyze(capture)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(analysis.Operations) != 1 {
		t.Fatalf("top-level operation count = %d, want 1", len(analysis.Operations))
	}

	records := OperationRecords(analysis.Operations[0])
	if len(records) != 3 {
		t.Fatalf("record count = %d, want 3; records=%#v", len(records), records)
	}
	if records[0].Kind != OperationWrite || records[0].Write.Text() != "a" {
		t.Fatalf("first record = %#v, want write a", records[0])
	}
	if records[1].Kind != OperationErase {
		t.Fatalf("second record = %#v, want erase", records[1])
	}
	if records[2].Kind != OperationWrite || records[2].Write.Text() != "b" {
		t.Fatalf("third record = %#v, want write b", records[2])
	}
}

func TestWriteBatchRejectsCombinedSegmentAndControlOverflow(t *testing.T) {
	backend := newTracingBackend(MustDimensions(1, 1))
	backend.writeBatch = &writeBatch{
		segments: make([]WriteSegment, maxWriteBatchSegments),
	}
	backend.beginByte(Chunk{}, 0)

	if backend.appendOperation(Operation{Kind: OperationErase}) {
		t.Fatal("append control beyond combined batch limit succeeded")
	}
	var overflow *EvidenceLimitExceeded
	if !errors.As(backend.error(), &overflow) {
		t.Fatalf("append error = %T %v, want EvidenceLimitExceeded", backend.error(), backend.error())
	}
	if overflow.Source != EvidenceSourceOperations || overflow.Limit != maxWriteBatchSegments || overflow.Observed != maxWriteBatchSegments+1 {
		t.Fatalf("overflow = %+v", overflow)
	}
}

func TestMaximumPrintableEvidenceUsesOneBoundedWriteTextArena(t *testing.T) {
	payload := strings.Repeat("x", maxOperationTextBytes)
	capture, err := NewCapture(MustDimensions(maxTerminalRows, maxTerminalCols), []Chunk{
		NewChunk(0, 0, []byte(payload)),
	})
	if err != nil {
		t.Fatalf("NewCapture: %v", err)
	}
	analysis, err := Analyze(capture)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(analysis.Operations) != 1 {
		t.Fatalf("top-level operation count = %d, want 1", len(analysis.Operations))
	}

	arena, err := WriteTextArena(analysis)
	if err != nil {
		t.Fatalf("WriteTextArena: %v", err)
	}
	if len(arena) != maxOperationTextBytes {
		t.Fatalf("write-text arena bytes = %d, want %d", len(arena), maxOperationTextBytes)
	}

	records := OperationRecords(analysis.Operations[0])
	if len(records) == 0 {
		t.Fatal("write batch has no records")
	}
	for _, record := range records {
		if record.Kind != OperationWrite || record.Write == nil {
			t.Fatalf("record = %#v, want write", record)
		}
		if record.Write.arena == nil {
			t.Fatalf("record write payload has no arena: %#v", record.Write)
		}
		if record.Write.arena != analysis.Operations[0].Write.arena {
			t.Fatal("write record does not reference the shared analysis arena")
		}
	}
}

func TestOperationBudgetPreservesOrderedTailAfterCircularWrap(t *testing.T) {
	budget := newOperationBudget()
	payload := make([]byte, evidenceExcerptSize+17)
	for index := range payload {
		payload[index] = byte(index)
		budget.observeByte(payload[index])
	}

	if got, want := budget.prefixBytes(), payload[:evidenceExcerptSize]; !bytes.Equal(got, want) {
		t.Fatalf("prefix = %v, want %v", got, want)
	}
	if got, want := budget.tailBytes(), payload[len(payload)-evidenceExcerptSize:]; !bytes.Equal(got, want) {
		t.Fatalf("tail = %v, want %v", got, want)
	}
}

func TestTracingBackendBlitPreservesOverlappingCells(t *testing.T) {
	t.Run("moves down without copying a source row twice", func(t *testing.T) {
		backend := newTracingBackend(MustDimensions(3, 1))
		backend.cells[0][0].Content = "a"
		backend.cells[1][0].Content = "b"
		backend.cells[2][0].Content = "c"

		backend.Blit(vt.Coord{X: 0, Y: 0}, vt.Coord{X: 0, Y: 1}, vt.Coord{X: 1, Y: 2})

		if got := []string{backend.cells[0][0].Content, backend.cells[1][0].Content, backend.cells[2][0].Content}; !slices.Equal(got, []string{"a", "a", "b"}) {
			t.Fatalf("cells = %q, want [a a b]", got)
		}
	})
	t.Run("moves right without copying a source cell twice", func(t *testing.T) {
		backend := newTracingBackend(MustDimensions(1, 4))
		for index, value := range []string{"a", "b", "c", "d"} {
			backend.cells[0][index].Content = value
		}

		backend.Blit(vt.Coord{X: 0, Y: 0}, vt.Coord{X: 1, Y: 0}, vt.Coord{X: 3, Y: 1})

		if got := []string{backend.cells[0][0].Content, backend.cells[0][1].Content, backend.cells[0][2].Content, backend.cells[0][3].Content}; !slices.Equal(got, []string{"a", "a", "b", "c"}) {
			t.Fatalf("cells = %q, want [a a b c]", got)
		}
	})
}

func TestReplayCheckpointScreensUsesOneCaptureTimeline(t *testing.T) {
	capture, err := NewCapture(MustDimensions(1, 4), []Chunk{
		NewChunk(0, 0, []byte("ab")),
	})
	if err != nil {
		t.Fatalf("NewCapture: %v", err)
	}
	screens, err := ReplayCheckpointScreens(capture, []ReplayCheckpoint{
		{ByteOffset: 1},
		{ByteOffset: 2},
	})
	if err != nil {
		t.Fatalf("ReplayCheckpointScreens: %v", err)
	}
	if got := screens[0].TextInRegion(Region{Top: 0, Bottom: 1, Left: 0, Right: 2}); got != "a" {
		t.Fatalf("first checkpoint = %q, want a", got)
	}
	if got := screens[1].TextInRegion(Region{Top: 0, Bottom: 1, Left: 0, Right: 2}); got != "ab" {
		t.Fatalf("second checkpoint = %q, want ab", got)
	}
}
