package app

import (
	"strconv"
	"testing"

	"core/cli/tui/ongoing"
	"core/shared/serverapi"
)

func TestLoadPromptHistoryKeepsOnlyNewestContractTailInRelease(t *testing.T) {
	history := make([]string, 0, serverapi.SessionPromptHistoryMaxEntries+25)
	for i := range serverapi.SessionPromptHistoryMaxEntries + 25 {
		history = append(history, promptHistoryEntry(i))
	}

	m := newProjectedStaticUIModel(WithUIDebug(false), WithUIPromptHistory(history))

	if got, want := len(m.promptHistory), serverapi.SessionPromptHistoryMaxEntries; got != want {
		t.Fatalf("prompt history length = %d, want %d", got, want)
	}
	if got, want := m.promptHistory[0], promptHistoryEntry(25); got != want {
		t.Fatalf("oldest retained prompt = %q, want %q", got, want)
	}
	if got, want := m.promptHistory[len(m.promptHistory)-1], promptHistoryEntry(serverapi.SessionPromptHistoryMaxEntries+24); got != want {
		t.Fatalf("newest retained prompt = %q, want %q", got, want)
	}
}

func TestPromptHistoryOptionDoesNotPopulateRuntimeHistoryBeforeFinalization(t *testing.T) {
	history := make([]string, 0, serverapi.SessionPromptHistoryMaxEntries+25)
	for i := range serverapi.SessionPromptHistoryMaxEntries + 25 {
		history = append(history, promptHistoryEntry(i))
	}
	construction := newUIModelConstruction(nil)

	WithUIPromptHistory(history)(construction)

	if got := len(construction.promptHistory); got != 0 {
		t.Fatalf("runtime prompt history length before finalization = %d, want 0", got)
	}
	if got := len(construction.initialPromptHistory); got != len(history) {
		t.Fatalf("staged prompt history length = %d, want %d", got, len(history))
	}
	if &construction.initialPromptHistory[0] != &history[0] {
		t.Fatal("staged prompt history copied the source payload")
	}

	m := construction.finalize()
	if got, want := len(m.promptHistory), serverapi.SessionPromptHistoryMaxEntries; got != want {
		t.Fatalf("final prompt history length = %d, want %d", got, want)
	}
	if construction.initialPromptHistory != nil {
		t.Fatal("construction retained initial prompt history after finalization")
	}
}

func TestLoadPromptHistoryPanicsWithDeveloperDiagnosticWhenServerExceedsContractInDebug(t *testing.T) {
	history := make([]string, 0, serverapi.SessionPromptHistoryMaxEntries+1)
	for i := range serverapi.SessionPromptHistoryMaxEntries + 1 {
		history = append(history, promptHistoryEntry(i))
	}

	for _, test := range []struct {
		name    string
		options []UIOption
	}{
		{
			name: "debug before history",
			options: []UIOption{
				WithUIDebug(true),
				WithUIPromptHistory(history),
			},
		},
		{
			name: "history before debug",
			options: []UIOption{
				WithUIPromptHistory(history),
				WithUIDebug(true),
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			recovered := capturePanic(func() {
				_ = newProjectedStaticUIModel(test.options...)
			})
			developerErr, ok := recovered.(ongoing.DeveloperError)
			if !ok {
				t.Fatalf("panic = %T, want ongoing.DeveloperError", recovered)
			}
			if developerErr.Operation != "load_prompt_history" {
				t.Fatalf("developer-error operation = %q", developerErr.Operation)
			}
			if developerErr.Reason == "" {
				t.Fatal("developer error omitted reason")
			}
			if got := developerErr.Facts["actual_count"]; got != serverapi.SessionPromptHistoryMaxEntries+1 {
				t.Fatalf("actual-count diagnostic = %#v", got)
			}
			if got := developerErr.Facts["maximum_count"]; got != serverapi.SessionPromptHistoryMaxEntries {
				t.Fatalf("maximum-count diagnostic = %#v", got)
			}
			if developerErr.Stack == "" {
				t.Fatal("developer error omitted stack trace")
			}
		})
	}
}

func TestRememberPromptHistoryLocallyDiscardsOldestPastHundred(t *testing.T) {
	history := make([]string, 0, serverapi.SessionPromptHistoryMaxEntries)
	for i := range serverapi.SessionPromptHistoryMaxEntries {
		history = append(history, promptHistoryEntry(i))
	}
	m := newProjectedStaticUIModel(WithUIDebug(true), WithUIPromptHistory(history))

	if !m.rememberPromptHistoryLocally(promptHistoryEntry(serverapi.SessionPromptHistoryMaxEntries)) {
		t.Fatal("remember prompt history returned false")
	}

	if got, want := len(m.promptHistory), serverapi.SessionPromptHistoryMaxEntries; got != want {
		t.Fatalf("prompt history length = %d, want %d", got, want)
	}
	if got, want := m.promptHistory[0], promptHistoryEntry(1); got != want {
		t.Fatalf("oldest retained prompt = %q, want %q", got, want)
	}
	if got, want := m.promptHistory[len(m.promptHistory)-1], promptHistoryEntry(serverapi.SessionPromptHistoryMaxEntries); got != want {
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
