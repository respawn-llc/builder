package tui

import (
	"slices"
	"strings"
	"testing"

	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
	xansi "github.com/charmbracelet/x/ansi"
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

	view := xansi.Strip(model.View())
	lines := trimmedDetailTestLines(view)
	want := []string{"❯ hello from user", "", "▶ hello from assistant"}
	if !slices.Equal(lines, want) {
		t.Fatalf("detail lines = %#v, want %#v", lines, want)
	}
	if strings.TrimSpace(view) == "" {
		t.Fatal("detail view is blank")
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
	collapsedLines := trimmedDetailTestLines(model.View())
	wantCollapsed := []string{"▶ line one", "└ line two", "└ line three"}
	if !slices.Equal(collapsedLines, wantCollapsed) {
		t.Fatalf("collapsed detail lines = %#v, want %#v", collapsedLines, wantCollapsed)
	}

	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	expandedLines := trimmedDetailTestLines(model.View())
	wantExpanded := []string{"▼ line one", "└ line two", "└ line three", "└ line four"}
	if !slices.Equal(expandedLines, wantExpanded) {
		t.Fatalf("expanded detail lines = %#v, want %#v", expandedLines, wantExpanded)
	}
}

func trimmedDetailTestLines(view string) []string {
	lines := strings.Split(xansi.Strip(view), "\n")
	for idx := range lines {
		lines[idx] = strings.TrimRight(lines[idx], " ")
	}
	return lines
}
