package app

import (
	"core/shared/theme"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestLockedInputEditKeysDismissHelpAndStillNoOp(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 80
	m.termHeight = 24
	m.windowSizeKnown = true
	m.setInputSubmitLocked(true)
	m.setBusy(true)
	m.input = "locked"
	m.layout().syncViewport()

	next, _ := m.Update(customKeyMsg{Kind: customKeyHelp})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	updated = next.(*uiModel)

	if updated.helpVisible {
		t.Fatal("expected any keypress to dismiss help")
	}
	if updated.input != "locked" {
		t.Fatalf("expected locked input unchanged, got %q", updated.input)
	}
}

func slashPickerContainsCommand(state slashCommandPickerState, name string) bool {
	for _, command := range state.matches {
		if command.Name == name {
			return true
		}
	}
	return false
}

func slashPickerCommandNames(state slashCommandPickerState) []string {
	names := make([]string, 0, len(state.matches))
	for _, command := range state.matches {
		names = append(names, command.Name)
	}
	return names
}

func stripANSIAndTrimRight(view string) string {
	stripped := ansi.Strip(view)
	lines := strings.Split(stripped, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return strings.Join(lines, "\n")
}

func stripANSIPreserve(view string) string {
	return ansi.Strip(view)
}

func containsInOrder(text string, parts ...string) bool {
	offset := 0
	for _, part := range parts {
		idx := strings.Index(text[offset:], part)
		if idx < 0 {
			return false
		}
		offset += idx + len(part)
	}
	return true
}

func lineContaining(text, substring string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(ansi.Strip(line), substring) {
			return line
		}
	}
	return ""
}

func themeSelectionBackgroundEscape(themeName string) string {
	hex := strings.TrimPrefix(theme.ResolvePalette(themeName).App.ModeBg.TrueColor, "#")
	var r, g, b int
	if _, err := fmt.Sscanf(hex, "%02x%02x%02x", &r, &g, &b); err != nil {
		return ""
	}
	return fmt.Sprintf("48;2;%d;%d;%d", r, g, b)
}
