package clientui

import "testing"

func TestChatEntryPhaseFinalAnswerContract(t *testing.T) {
	entry := ChatEntry{Role: "assistant", Phase: ChatEntryPhaseFinalAnswer, Text: "done"}
	if entry.Phase != "final_answer" {
		t.Fatalf("final-answer phase = %q, want final_answer", entry.Phase)
	}
}
