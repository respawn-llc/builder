package pty_test

import (
	"testing"
	"time"

	"core/internal/testharness/pty"
)

func TestAnalyzeFacadeUsesTerminalAnalyzer(t *testing.T) {
	t.Parallel()

	capture, err := pty.NewCapture(
		pty.MustDimensions(2, 8),
		[]pty.Chunk{pty.NewChunk(0, time.Millisecond, []byte("ok"))},
	)
	if err != nil {
		t.Fatalf("NewCapture: %v", err)
	}

	analysis, err := pty.Analyze(capture)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if got := analysis.Screen.TextInRegion(pty.Region{Top: 0, Bottom: 1, Left: 0, Right: 2}); got != "ok" {
		t.Fatalf("screen text = %q, want %q", got, "ok")
	}
}
