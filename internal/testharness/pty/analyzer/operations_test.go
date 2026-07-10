package analyzer

import (
	"errors"
	"strings"
	"testing"
)

func TestRecordPutMergesContiguousSpansWithoutCopyingPriorText(t *testing.T) {
	backend := newTracingBackend(MustDimensions(1, 8))
	backend.beginByte(Chunk{}, 0)
	backend.recordPut(Position{Row: 0, Col: 0}, "a")
	backend.beginByte(Chunk{}, 1)
	backend.recordPut(Position{Row: 0, Col: 1}, "b")

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
		if operation.Kind == OperationWrite && operation.Write != nil && operation.Write.Text() == "x" {
			writes++
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
