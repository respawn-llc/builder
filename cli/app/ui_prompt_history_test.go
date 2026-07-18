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

func TestPromptHistoryRestoresAnEmptyDraft(t *testing.T) {
	m := newProjectedStaticUIModel(WithUIPromptHistory([]string{"previous prompt"}))
	testSetMainInputAtRuneCursor(m, "", 0)

	if !m.navigatePromptHistoryUp() {
		t.Fatal("expected history navigation to select the previous prompt")
	}
	if got, want := testMainInput(m), "previous prompt"; got != want {
		t.Fatalf("history input = %q, want %q", got, want)
	}
	if !m.navigatePromptHistoryDown() {
		t.Fatal("expected history navigation to restore the empty draft")
	}
	if got := testMainInput(m); got != "" {
		t.Fatalf("restored draft = %q, want empty", got)
	}
	if got := m.mainEditor.Cursor(); got != 0 {
		t.Fatalf("restored empty draft cursor = %d, want 0", got)
	}
	if m.promptHistorySelectionActive() || m.hasPromptHistoryDraft() {
		t.Fatalf("history state remained active after restoring empty draft: selection=%v draft=%#v", m.promptHistorySelection, m.promptHistoryDraft)
	}
}

func TestPromptHistoryDraftRestoresUnicodeCursorWithoutReplacingKillBuffer(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.mainEditor.Replace("界x")
	m.mainEditor.SetCursor(len("界"))
	snapshot := m.mainEditor.Snapshot()
	m.promptHistoryDraft = &snapshot
	m.mainEditor.Replace("temporary")
	m.mainEditor.SetKillBuffer("retained")

	m.restorePromptHistoryDraft()
	if got, want := testMainInput(m), "界x"; got != want {
		t.Fatalf("restored draft = %q, want %q", got, want)
	}
	if got, want := m.mainEditor.Cursor(), len("界"); got != want {
		t.Fatalf("restored byte cursor = %d, want %d", got, want)
	}
	if got, want := m.mainEditor.KillBuffer(), "retained"; got != want {
		t.Fatalf("restored kill buffer = %q, want live value %q", got, want)
	}
}

func promptHistoryEntry(index int) string {
	return "prompt history entry " + strconv.Itoa(index)
}
