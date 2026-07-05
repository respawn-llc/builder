package tui

import (
	"testing"

	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDetailModeRendersHydratedCommittedRows(t *testing.T) {
	model := NewModel()
	next, _ := model.Update(SetDetailTranscriptPageMsg{Page: clientui.TranscriptPage{
		Entries: []clientui.ChatEntry{
			{Role: "user", Text: "hello from user"},
			{Role: "assistant", Text: "hello from assistant"},
		},
	}})
	model = next.(Model)
	next, _ = model.Update(SetModeMsg{Mode: ModeDetail})
	model = next.(Model)

	if model.Mode() != ModeDetail || !model.detailPageLoaded {
		t.Fatalf("detail mode/loaded = %s/%t, want detail/loaded", model.Mode(), model.detailPageLoaded)
	}
	if len(model.detailEntries) != 2 || model.detailEntries[0].Kind != clientui.TranscriptRowUser || model.detailEntries[1].Kind != clientui.TranscriptRowAssistant {
		t.Fatalf("detail entries = %#v, want user then assistant", model.detailEntries)
	}
	if selected, ok := model.selectedDetailIndex(); !ok || selected != 1 {
		t.Fatalf("selected detail entry = %d/%t, want 1/true", selected, ok)
	}
}

func TestDetailModeFiltersHiddenTranscriptEntries(t *testing.T) {
	model := NewModel()
	next, _ := model.Update(SetDetailTranscriptPageMsg{Page: clientui.TranscriptPage{
		Entries: []clientui.ChatEntry{
			{Visibility: clientui.EntryVisibilityOngoing, Role: "user", Text: "visible"},
			{Visibility: clientui.EntryVisibilityHidden, Role: "assistant", Text: "hidden"},
		},
	}})
	model = next.(Model)
	next, _ = model.Update(SetModeMsg{Mode: ModeDetail})
	model = next.(Model)

	if len(model.detailEntries) != 1 || model.detailEntries[0].Kind != clientui.TranscriptRowUser {
		t.Fatalf("detail entries = %#v, want visible user only", model.detailEntries)
	}
	if selected, ok := model.selectedDetailIndex(); !ok || selected != 0 {
		t.Fatalf("selected detail entry = %d/%t, want 0/true", selected, ok)
	}
}

func TestDetailModeCachedRowsPreserveVisibility(t *testing.T) {
	model := NewModel()
	model.detailEntries = []detailEntry{newDetailEntry(clientui.TranscriptCommittedRow{
		Visibility: clientui.EntryVisibilityOngoingCollapsed,
		Kind:       clientui.TranscriptRowNotice,
		Notice: &clientui.TranscriptNoticeRow{
			Severity: clientui.TranscriptNoticeInfo,
			Data:     clientui.TranscriptNoticeData{CompactLabel: "compact notice"},
			Diagnostic: &clientui.TranscriptDiagnosticData{
				Detail: "diagnostic detail",
			},
		},
	})}
	model.detailPageLoaded = true
	model.setSelectedDetailIndex(0)
	next, _ := model.Update(SetModeMsg{Mode: ModeDetail})
	model = next.(Model)

	if len(model.detailEntries) != 1 || model.detailEntries[0].Visibility != clientui.EntryVisibilityOngoingCollapsed {
		t.Fatalf("detail entries = %#v, want preserved ongoing-collapsed visibility", model.detailEntries)
	}
	if selected, ok := model.selectedDetailIndex(); !ok || selected != 0 {
		t.Fatalf("selected detail entry = %d/%t, want 0/true", selected, ok)
	}
}

func TestDetailModeExpandsSelectedEntry(t *testing.T) {
	model := NewModel()
	next, _ := model.Update(SetDetailTranscriptPageMsg{Page: clientui.TranscriptPage{
		Entries: []clientui.ChatEntry{{Role: "assistant", Text: "line one\nline two\nline three\nline four"}},
	}})
	model = next.(Model)
	next, _ = model.Update(SetModeMsg{Mode: ModeDetail})
	model = next.(Model)
	if _, ok := model.expanded[0]; ok {
		t.Fatal("detail entry starts expanded")
	}

	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	if _, ok := model.expanded[0]; !ok {
		t.Fatal("selected detail entry was not expanded")
	}
}
