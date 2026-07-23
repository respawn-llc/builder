package llm

import "testing"

func TestReconciledCompletedAssistantTextPreservesStreamedTrailingWhitespace(t *testing.T) {
	const streamed = "completed answer    "
	const completed = "completed answer"

	if got := reconciledCompletedAssistantText(streamed, completed); got != streamed {
		t.Fatalf("reconciled completed text = %q, want streamed text %q", got, streamed)
	}
}
