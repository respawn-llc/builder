package tui

import (
	"strings"
	"testing"

	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
	xansi "github.com/charmbracelet/x/ansi"
)

func TestDetailModeRendersHydratedCommittedRows(t *testing.T) {
	model := NewModel()
	next, _ := model.Update(ApplyTranscriptMessageMsg{Message: clientui.TranscriptMessage{
		Kind: clientui.TranscriptMessageHydration,
		Hydration: &clientui.TranscriptHydration{CommittedRows: []clientui.TranscriptCommittedRow{
			{Kind: clientui.TranscriptRowUser, User: &clientui.TranscriptUserRow{Text: "hello from user"}},
			{Kind: clientui.TranscriptRowAssistant, Assistant: &clientui.TranscriptAssistantRow{Text: "hello from assistant"}},
		}},
	}})
	model = next.(Model)
	next, _ = model.Update(SetModeMsg{Mode: ModeDetail})
	model = next.(Model)

	view := xansi.Strip(model.View())
	for _, want := range []string{"hello from user", "hello from assistant"} {
		if !strings.Contains(view, want) {
			t.Fatalf("detail view = %q, want %q", view, want)
		}
	}
	if strings.TrimSpace(view) == "" {
		t.Fatal("detail view is blank")
	}
}

func TestDetailModeExpandsSelectedEntry(t *testing.T) {
	model := NewModel()
	next, _ := model.Update(ApplyTranscriptMessageMsg{Message: clientui.TranscriptMessage{
		Kind: clientui.TranscriptMessageCommittedRow,
		CommittedRow: &clientui.TranscriptCommittedRow{
			Kind:      clientui.TranscriptRowAssistant,
			Assistant: &clientui.TranscriptAssistantRow{Text: "line one\nline two\nline three\nline four"},
		},
	}})
	model = next.(Model)
	next, _ = model.Update(SetModeMsg{Mode: ModeDetail})
	model = next.(Model)
	collapsed := xansi.Strip(model.View())
	if strings.Contains(collapsed, "line four") {
		t.Fatalf("collapsed detail view = %q, want three-line preview", collapsed)
	}

	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	expanded := xansi.Strip(model.View())
	if !strings.Contains(expanded, "line four") || !strings.Contains(expanded, "▼") {
		t.Fatalf("expanded detail view = %q, want full selected entry", expanded)
	}
}
