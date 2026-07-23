package app

import (
	"strings"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"
)

func TestStartupEditorFieldMasksAndRendersPlaceholder(t *testing.T) {
	maskedInput := newSingleLineEditor("secret")
	masked := xansi.Strip(renderStartupEditorField(16, 8, "dark", maskedInput, "› ", true, '*', ""))
	if strings.Contains(masked, "secret") {
		t.Fatalf("masked startup input exposed sensitive text: %q", masked)
	}
	if !strings.Contains(masked, "› ******") {
		t.Fatalf("masked startup input missing mask runes: %q", masked)
	}

	placeholderInput := newSingleLineEditor("")
	placeholder := xansi.Strip(renderStartupEditorField(16, 8, "dark", placeholderInput, "› ", true, 0, "project name"))
	if !strings.Contains(placeholder, "› project name") {
		t.Fatalf("startup input missing placeholder: %q", placeholder)
	}
}

func TestProjectNamePromptUsesRealAltScreenCursorWhenAvailable(t *testing.T) {
	state := newUITerminalCursorState()
	model := newProjectNamePromptModel("alpha beta gamma", "dark")
	model.terminalCursor = state
	model.width = 24
	model.height = 10

	view := model.View()
	placement, ok := state.Snapshot()
	if !ok {
		t.Fatalf("expected real cursor placement for project prompt input, view=%q", view)
	}
	if !placement.AltScreen {
		t.Fatalf("expected alt-screen cursor placement, got %+v", placement)
	}
	if placement.CursorCol >= model.width {
		t.Fatalf("cursor col %d outside width %d", placement.CursorCol, model.width)
	}
	if strings.Contains(view, "\x1b[7") {
		t.Fatal("did not expect soft cursor when real terminal cursor is available")
	}
}
