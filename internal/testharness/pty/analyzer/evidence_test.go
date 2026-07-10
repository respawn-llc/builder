package analyzer_test

import (
	"bytes"
	"errors"
	"math/rand/v2"
	"reflect"
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

func TestCaptureAssemblerIsInvariantToRandomReadFragmentation(t *testing.T) {
	t.Parallel()

	payload := []byte("a\x1b[?1049hhello\x1b[2J\x1b[?25hworld")
	resizeOffset := len([]byte("a\x1b[?1049hhello"))
	build := func(fragments [][]byte) (pty.Capture, pty.Analysis) {
		t.Helper()
		assembler, err := analyzer.NewCaptureAssembler(pty.MustDimensions(3, 8))
		if err != nil {
			t.Fatalf("NewCaptureAssembler: %v", err)
		}
		observed := 0
		for _, fragment := range fragments {
			if observed == resizeOffset {
				if err := assembler.Resize(pty.MustDimensions(4, 8)); err != nil {
					t.Fatalf("Resize: %v", err)
				}
			}
			if err := assembler.Append(fragment); err != nil {
				t.Fatalf("Append: %v", err)
			}
			observed += len(fragment)
		}
		if observed == resizeOffset {
			if err := assembler.Resize(pty.MustDimensions(4, 8)); err != nil {
				t.Fatalf("final Resize: %v", err)
			}
		}
		capture, err := assembler.Capture()
		if err != nil {
			t.Fatalf("Capture: %v", err)
		}
		analysis, err := pty.Analyze(capture)
		if err != nil {
			t.Fatalf("Analyze: %v", err)
		}
		return capture, analysis
	}

	coalesced, coalescedAnalysis := build([][]byte{payload[:resizeOffset], payload[resizeOffset:]})
	for seed := uint64(0); seed < 8; seed++ {
		random := rand.New(rand.NewPCG(seed, seed+1))
		fragments := make([][]byte, 0)
		for start := 0; start < len(payload); {
			end := start + 1 + random.IntN(4)
			if start < resizeOffset && end > resizeOffset {
				end = resizeOffset
			}
			if end > len(payload) {
				end = len(payload)
			}
			fragments = append(fragments, payload[start:end])
			start = end
		}
		capture, analysis := build(fragments)
		if !bytes.Equal(capture.Raw, coalesced.Raw) || !reflect.DeepEqual(capture.Resizes, coalesced.Resizes) {
			t.Fatalf("seed=%d capture differs: got=%#v want=%#v", seed, capture, coalesced)
		}
		if !reflect.DeepEqual(analysis.Screen, coalescedAnalysis.Screen) ||
			!reflect.DeepEqual(analysis.PrivateModeChanges, coalescedAnalysis.PrivateModeChanges) ||
			!reflect.DeepEqual(operationView(analysis), operationView(coalescedAnalysis)) {
			t.Fatalf("seed=%d analysis differs: got=%#v want=%#v", seed, analysis, coalescedAnalysis)
		}
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

func TestCaptureAssemblerRejectsEvidenceBlockOverflow(t *testing.T) {
	t.Parallel()

	assembler, err := analyzer.NewCaptureAssembler(pty.MustDimensions(1, 1))
	if err != nil {
		t.Fatalf("NewCaptureAssembler: %v", err)
	}
	for index := 0; index < 320; index++ {
		if err := assembler.Append([]byte("x")); err != nil {
			t.Fatalf("Append %d: %v", index, err)
		}
		if err := assembler.Resize(pty.MustDimensions(1, 1)); err != nil {
			t.Fatalf("Resize %d: %v", index, err)
		}
	}
	if err := assembler.Append([]byte("x")); err != nil {
		t.Fatalf("Append after block limit: %v", err)
	}
	if _, err := assembler.Capture(); err == nil {
		t.Fatal("Capture after block limit succeeded")
	}
}
