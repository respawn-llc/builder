package app

import (
	"runtime"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

type uiHelpEntry struct {
	Bindings    []string
	Description string
	Active      func(*uiModel) bool
}

type uiHelpSection struct {
	Title   string
	Entries []uiHelpEntry
}

func (m *uiModel) canToggleHelpWithQuestionMark() bool {
	if m == nil ||
		m.input != "" {
		return false
	}
	switch m.inputMode() {
	case uiInputModeMain, uiInputModeRollbackEdit:
		return true
	default:
		return false
	}
}

func (m *uiModel) statusHelpHint() string {
	if m != nil && m.surface().wantsAltScreen() {
		return "F1 for help"
	}
	if m != nil && m.canToggleHelpWithQuestionMark() {
		return "F1 or ? for help"
	}
	return "F1 for help"
}

func (m *uiModel) canShowHelp() bool {
	return m.surface() == uiSurfaceOngoingTranscript
}

func helpSectionsForGOOS(goos string) []uiHelpSection {
	shortcutLabels := shortcutLabelsForGOOS(goos)
	return []uiHelpSection{
		{
			Title: "Global",
			Entries: []uiHelpEntry{
				{Bindings: []string{shortcutLabels.helpToggleBinding()}, Description: "toggle keyboard help", Active: func(*uiModel) bool { return true }},
				{Bindings: []string{"Ctrl + C"}, Description: "interrupt current run or exit", Active: func(*uiModel) bool { return true }},
				{Bindings: []string{"Shift + Tab / Ctrl + T"}, Description: "toggle transcript mode", Active: uiHelpCanToggleTranscript},
			},
		},
		{
			Title: "Prompt Input",
			Entries: []uiHelpEntry{
				{Bindings: []string{"$ <command>"}, Description: "execute a shell command and show output to the model", Active: uiHelpInMainInput},
				{Bindings: []string{"Enter"}, Description: "submit the current input, selected answer, or flush the next queued item", Active: uiHelpInPromptInput},
				{Bindings: []string{"Tab / Ctrl + Enter"}, Description: "autocomplete a selected slash command, or queue/send the current input", Active: uiHelpInMainInput},
				{Bindings: []string{"↑ / ↓"}, Description: "browse submitted prompts at input boundaries; otherwise move within multiline input", Active: uiHelpInTextEditing},
				{Bindings: []string{"Ctrl/Alt + V/D"}, Description: "paste clipboard image path or text at the cursor", Active: uiHelpInTextEditing},
				{Bindings: []string{"Shift + Enter / Ctrl + J"}, Description: "insert a newline", Active: uiHelpInTextEditing},
				{Bindings: deleteCurrentLineBindingsForGOOS(goos), Description: "delete the current input line", Active: uiHelpInTextEditing},
				{Bindings: []string{"Delete / Ctrl + K/U/W/Y"}, Description: "edit/delete/yank text with shell-style shortcuts", Active: uiHelpInTextEditing},
				{Bindings: []string{"Alt/Ctrl + ←/→"}, Description: "move the cursor by word", Active: uiHelpInTextEditing},
				{Bindings: []string{"Home/End / Ctrl + A/E/End"}, Description: "jump to the line start or end", Active: uiHelpInTextEditing},
			},
		},
		{
			Title: "Rollback Mode",
			Entries: []uiHelpEntry{
				{Bindings: []string{"Esc Esc"}, Description: "open rollback selection from an idle empty prompt", Active: uiHelpCanArmRollback},
				{Bindings: []string{"↑ / ↓"}, Description: "move the rollback selection and load older/newer pages at the edges", Active: func(*uiModel) bool { return true }},
				{Bindings: []string{"PgUp / PgDn"}, Description: "scroll the transcript while selecting a rollback point", Active: func(*uiModel) bool { return true }},
				{Bindings: []string{"Esc"}, Description: "cancel or go back", Active: func(*uiModel) bool { return true }},
			},
		},
	}
}

func deleteCurrentLineBindingsForGOOS(goos string) []string {
	shortcutLabels := shortcutLabelsForGOOS(goos)
	bindings := []string{"Ctrl/" + shortcutLabels.super + " + Backspace"}
	if goos == "darwin" {
		bindings = append(bindings, "Ctrl + U")
	}
	return bindings
}

type uiShortcutLabels struct {
	super string
}

func shortcutLabelsForGOOS(goos string) uiShortcutLabels {
	switch goos {
	case "darwin":
		return uiShortcutLabels{super: "⌘"}
	case "windows":
		return uiShortcutLabels{super: "Win"}
	default:
		return uiShortcutLabels{super: "Super"}
	}
}

func (l uiShortcutLabels) helpToggleBinding() string {
	return "F1 / ? (empty) / Alt/" + l.super + " + /"
}

func uiHelpCanToggleTranscript(m *uiModel) bool {
	switch m.inputMode() {
	case uiInputModeMain, uiInputModeRollbackEdit:
		return true
	default:
		return false
	}
}

func uiHelpInMainInput(m *uiModel) bool {
	return m.inputMode() == uiInputModeMain
}

func uiHelpInPromptInput(m *uiModel) bool {
	if m == nil {
		return false
	}
	switch m.inputMode() {
	case uiInputModeMain, uiInputModeAsk:
		return true
	default:
		return false
	}
}

func uiHelpInTextEditing(m *uiModel) bool {
	switch m.inputMode() {
	case uiInputModeMain, uiInputModeRollbackEdit:
		return true
	case uiInputModeAsk:
		return m.ask.freeform
	default:
		return false
	}
}

func uiHelpCanArmRollback(m *uiModel) bool {
	return m.inputMode() == uiInputModeMain && m.view.Mode() == "ongoing"
}

func helpPaneMaxLines(height, inputLines, queuedLines, pickerLines int) int {
	maxLines := height - inputLines - queuedLines - pickerLines - 2 // reserve chat + status
	if maxLines < 0 {
		return 0
	}
	return maxLines
}

func (l uiViewLayout) renderHelpPane(width, maxLines int, style uiStyles) []string {
	if !l.model.helpVisible || !l.model.canShowHelp() || width < 1 || maxLines < 3 {
		return nil
	}

	palette := uiPalette(l.model.theme)
	activeSectionStyle := lipgloss.NewStyle().Foreground(palette.secondary).Bold(true)
	inactiveSectionStyle := style.meta.Bold(true)
	activeKeyStyle := lipgloss.NewStyle().Foreground(palette.primary).Bold(true)
	inactiveKeyStyle := style.meta
	activeDescStyle := style.input
	inactiveDescStyle := style.meta

	sections := helpSectionsForGOOS(runtime.GOOS)
	keyColumnWidth := helpKeyColumnWidth(sections, width)
	content := make([]string, 0, 32)
	visibleSectionCount := 0

	for _, section := range sections {
		if visibleSectionCount > 0 {
			content = append(content, padRight("", width))
		}
		visibleSectionCount++
		sectionActive := false
		for _, entry := range section.Entries {
			if entry.Active != nil && entry.Active(l.model) {
				sectionActive = true
				break
			}
		}
		sectionStyle := inactiveSectionStyle
		if sectionActive {
			sectionStyle = activeSectionStyle
		}
		content = append(content, sectionStyle.Render(padANSIRight(section.Title, width)))
		for _, entry := range section.Entries {
			entryActive := entry.Active != nil && entry.Active(l.model)
			keyStyle := inactiveKeyStyle
			descStyle := inactiveDescStyle
			if entryActive {
				keyStyle = activeKeyStyle
				descStyle = activeDescStyle
			}
			for _, line := range renderHelpEntryLines(entry, width, keyColumnWidth) {
				keys := ""
				if strings.TrimSpace(line.keys) != "" {
					keys = keyStyle.Render(padRight(line.keys, keyColumnWidth))
				} else {
					keys = padRight("", keyColumnWidth)
				}
				description := descStyle.Render(line.description)
				content = append(content, padANSIRight(keys+" "+description, width))
			}
		}
	}

	maxContentLines := maxLines - 2
	if maxContentLines < len(content) {
		content = content[:maxContentLines]
		if len(content) > 0 {
			content[len(content)-1] = style.meta.Render(padANSIRight("…", width))
		}
	}

	return renderFramedLines(width, content, l.inputBorderStyle())
}

type renderedHelpEntryLine struct {
	keys        string
	description string
}

func renderHelpEntryLines(entry uiHelpEntry, width, keyColumnWidth int) []renderedHelpEntryLine {
	bindings := strings.Join(entry.Bindings, " | ")
	if width <= keyColumnWidth+6 {
		plain := bindings + " " + entry.Description
		wrapped := wrapLine(plain, width)
		out := make([]renderedHelpEntryLine, 0, len(wrapped))
		for _, line := range wrapped {
			out = append(out, renderedHelpEntryLine{description: line})
		}
		return out
	}

	descriptionWidth := width - keyColumnWidth - 1
	keyLines := wrapLine(bindings, keyColumnWidth)
	descriptionLines := wrapLine(entry.Description, descriptionWidth)
	rows := len(keyLines)
	if len(descriptionLines) > rows {
		rows = len(descriptionLines)
	}
	out := make([]renderedHelpEntryLine, 0, rows)
	for i := 0; i < rows; i++ {
		line := renderedHelpEntryLine{}
		if i < len(keyLines) {
			line.keys = keyLines[i]
		}
		if i < len(descriptionLines) {
			line.description = descriptionLines[i]
		}
		out = append(out, line)
	}
	return out
}

func helpKeyColumnWidth(sections []uiHelpSection, width int) int {
	maxWidth := 0
	for _, section := range sections {
		for _, entry := range section.Entries {
			if w := runewidth.StringWidth(strings.Join(entry.Bindings, " | ")); w > maxWidth {
				maxWidth = w
			}
		}
	}
	if maxWidth < 18 {
		maxWidth = 18
	}
	maxAllowed := width / 2
	if maxAllowed < 12 {
		maxAllowed = 12
	}
	if maxWidth > maxAllowed {
		maxWidth = maxAllowed
	}
	return maxWidth
}
