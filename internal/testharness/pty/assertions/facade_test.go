package assertions_test

import (
	"testing"

	"core/internal/testharness/pty"
)

func TestFacadeAssertionHelpers(t *testing.T) {
	t.Parallel()

	err := pty.NoWritesAbove(analysisWithOps(
		writeOp(0, pty.Region{Top: 1, Bottom: 2, Left: 0, Right: 4}, "ok"),
	), pty.OperationWindow{Start: 0, End: 1}, 1)
	if err != nil {
		t.Fatalf("NoWritesAbove facade: %v", err)
	}

	if err := pty.BlankFrame(pty.Analysis{Screen: pty.NewScreenSnapshot(pty.MustDimensions(2, 4))}); err != nil {
		t.Fatalf("BlankFrame facade: %v", err)
	}
}
