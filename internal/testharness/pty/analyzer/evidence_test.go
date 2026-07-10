package analyzer_test

import (
	"bytes"
	"errors"
	"testing"

	"core/internal/testharness/pty"
	"core/internal/testharness/pty/analyzer"
)

func TestCaptureAssemblerCoalescesEquivalentFragmentationAtResizeBarrier(t *testing.T) {
	t.Parallel()

	build := func(fragments [][]byte, resizeBefore int) analyzer.Capture {
		t.Helper()
		assembler, err := analyzer.NewCaptureAssembler(pty.MustDimensions(2, 8))
		if err != nil {
			t.Fatalf("NewCaptureAssembler: %v", err)
		}
		for index, fragment := range fragments {
			if index == resizeBefore {
				if err := assembler.Resize(pty.MustDimensions(3, 8)); err != nil {
					t.Fatalf("Resize: %v", err)
				}
			}
			if err := assembler.Append(fragment); err != nil {
				t.Fatalf("Append: %v", err)
			}
		}
		capture, err := assembler.Capture()
		if err != nil {
			t.Fatalf("Capture: %v", err)
		}
		return capture
	}

	split := build([][]byte{[]byte("\x1b"), []byte("["), []byte("?25"), []byte("hhello!")}, 3)
	coalesced := build([][]byte{[]byte("\x1b[?25"), []byte("hhello!")}, 1)
	if !bytes.Equal(split.Raw, coalesced.Raw) {
		t.Fatalf("raw evidence differs: split=%q coalesced=%q", split.Raw, coalesced.Raw)
	}
	if len(split.Resizes) != 1 || len(coalesced.Resizes) != 1 || split.Resizes[0].Offset != coalesced.Resizes[0].Offset {
		t.Fatalf("resize evidence differs: split=%#v coalesced=%#v", split.Resizes, coalesced.Resizes)
	}
}

func TestCaptureAssemblerRejectsPTYEvidenceOverflowWithPrefixAndTail(t *testing.T) {
	t.Parallel()

	assembler, err := analyzer.NewCaptureAssembler(pty.MustDimensions(1, 1))
	if err != nil {
		t.Fatalf("NewCaptureAssembler: %v", err)
	}
	payload := bytes.Repeat([]byte("x"), 1*1024*1024+1)
	err = assembler.Append(payload)
	var overflow *analyzer.EvidenceLimitExceeded
	if !errors.As(err, &overflow) {
		t.Fatalf("Append error = %T %v, want EvidenceLimitExceeded", err, err)
	}
	if overflow.Source != analyzer.EvidenceSourcePTY || overflow.Limit != 1*1024*1024 || overflow.Observed != len(payload) {
		t.Fatalf("overflow = %+v", overflow)
	}
	if len(overflow.Prefix) != 32*1024 || len(overflow.Tail) != 32*1024 {
		t.Fatalf("overflow excerpts = prefix:%d tail:%d", len(overflow.Prefix), len(overflow.Tail))
	}
}
