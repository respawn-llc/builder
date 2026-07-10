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

	if len(backend.ops) != 1 {
		t.Fatalf("operation count = %d, want 1", len(backend.ops))
	}
	if got := backend.ops[0].Write.Text(); got != "ab" {
		t.Fatalf("merged write = %q, want ab", got)
	}
	if got := len(backend.writeText.bytes); got != 2 {
		t.Fatalf("arena bytes = %d, want 2", got)
	}
	if backend.ops[0].Write.Span != (TextSpan{Start: 0, End: 2}) {
		t.Fatalf("write span = %+v, want [0,2)", backend.ops[0].Write.Span)
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
