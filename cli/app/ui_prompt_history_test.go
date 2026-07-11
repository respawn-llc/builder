package app

import (
	"strconv"
	"testing"
)

func TestLoadPromptHistoryKeepsOnlyMostRecentHundred(t *testing.T) {
	history := make([]string, 0, 125)
	for i := range 125 {
		history = append(history, promptHistoryEntry(i))
	}

	m := newProjectedStaticUIModel(WithUIPromptHistory(history))

	if got, want := len(m.promptHistory), promptHistoryLimit; got != want {
		t.Fatalf("prompt history length = %d, want %d", got, want)
	}
	if got, want := m.promptHistory[0], promptHistoryEntry(25); got != want {
		t.Fatalf("oldest retained prompt = %q, want %q", got, want)
	}
	if got, want := m.promptHistory[len(m.promptHistory)-1], promptHistoryEntry(124); got != want {
		t.Fatalf("newest retained prompt = %q, want %q", got, want)
	}
}

func TestRememberPromptHistoryLocallyDiscardsOldestPastHundred(t *testing.T) {
	history := make([]string, 0, 100)
	for i := range 100 {
		history = append(history, promptHistoryEntry(i))
	}
	m := newProjectedStaticUIModel(WithUIPromptHistory(history))

	if !m.rememberPromptHistoryLocally(promptHistoryEntry(100)) {
		t.Fatal("remember prompt history returned false")
	}

	if got, want := len(m.promptHistory), promptHistoryLimit; got != want {
		t.Fatalf("prompt history length = %d, want %d", got, want)
	}
	if got, want := m.promptHistory[0], promptHistoryEntry(1); got != want {
		t.Fatalf("oldest retained prompt = %q, want %q", got, want)
	}
	if got, want := m.promptHistory[len(m.promptHistory)-1], promptHistoryEntry(100); got != want {
		t.Fatalf("newest retained prompt = %q, want %q", got, want)
	}
}

func promptHistoryEntry(index int) string {
	return "prompt history entry " + strconv.Itoa(index)
}
