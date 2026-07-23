package transcriptdiag

import (
	"testing"
)

func TestDigestIncludesEveryFact(t *testing.T) {
	if got, wantDifferent := Digest("run-1", "running", "turn"), Digest("run-1", "running", "goal_loop"); got == wantDifferent {
		t.Fatalf("distinct facts must affect digest, got collision %q", got)
	}
}
