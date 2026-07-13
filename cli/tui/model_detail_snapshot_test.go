package tui

import (
	"testing"

	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDetailPresentationSnapshotRestoresSelectionExpansionAndCamera(t *testing.T) {
	model := NewModel()
	next, _ := model.Update(SetViewportSizeMsg{Lines: 2, Width: 80})
	model = next.(Model)
	page := clientui.TranscriptPage{
		Entries: []clientui.TranscriptCommittedRow{
			detailAssistant("one"),
			detailAssistant("two"),
			detailAssistant("three\nline two\nline three\nline four"),
		},
	}
	next, _ = model.Update(SetDetailTranscriptPageMsg{Page: page})
	model = next.(Model)
	next, _ = model.Update(SetModeMsg{Mode: ModeDetail})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = next.(Model)
	snapshot := model.DetailPresentationSnapshot()
	selectedBefore, selectedBeforeOK := model.selectedDetailIndex()
	scrollBefore := model.detailScroll

	next, _ = model.Update(ResetDetailTranscriptMsg{})
	model = next.(Model)
	next, _ = model.Update(SetDetailTranscriptPageMsg{Page: page})
	model = next.(Model)
	next, _ = model.Update(RestoreDetailPresentationMsg{Snapshot: snapshot})
	model = next.(Model)

	selectedAfter, selectedAfterOK := model.selectedDetailIndex()
	if selectedAfterOK != selectedBeforeOK || selectedAfter != selectedBefore {
		t.Fatalf("restored selection = %d/%t, want %d/%t", selectedAfter, selectedAfterOK, selectedBefore, selectedBeforeOK)
	}
	if model.detailScroll != scrollBefore {
		t.Fatalf("restored detail scroll = %d, want %d", model.detailScroll, scrollBefore)
	}
	if _, expanded := model.expanded[selectedAfter]; !expanded {
		t.Fatal("restored selected detail entry is not expanded")
	}
}
