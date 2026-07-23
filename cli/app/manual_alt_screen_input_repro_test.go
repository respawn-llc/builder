package app

import (
	"os"
	"strings"
	"testing"

	tuiinput "core/cli/tui/input"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestManualAltScreenInputCursorRepro(t *testing.T) {
	if os.Getenv("KENT_MANUAL_ALT_INPUT_REPRO") != "1" {
		t.Skip("set KENT_MANUAL_ALT_INPUT_REPRO=1 and run from a real terminal")
	}
	state := newUITerminalCursorState()
	model := &manualAltScreenInputReproModel{
		width:          80,
		height:         24,
		editor:         newSingleLineEditor("alpha beta gamma"),
		terminalCursor: state,
	}
	program := tea.NewProgram(model, tea.WithAltScreen(), tea.WithOutput(newUITerminalCursorWriter(os.Stdout, state)))
	if _, err := program.Run(); err != nil {
		t.Fatalf("manual alt-screen input repro failed: %v", err)
	}
}

type manualAltScreenInputReproModel struct {
	width          int
	height         int
	editor         tuiinput.Editor
	terminalCursor *uiTerminalCursorState
}

func (m *manualAltScreenInputReproModel) Init() tea.Cmd { return nil }

func (m *manualAltScreenInputReproModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch typed := msg.(type) {
	case tea.WindowSizeMsg:
		if typed.Width > 0 {
			m.width = typed.Width
		}
		if typed.Height > 0 {
			m.height = typed.Height
		}
		return m, nil
	case tea.KeyMsg:
		switch typed.Type {
		case tea.KeyEsc, tea.KeyCtrlC:
			return m, tea.Quit
		case tea.KeyEnter:
			m.editor.Replace(strings.NewReplacer("\r", "", "\n", "").Replace(""))
			return m, nil
		}
		return m, updateSingleLineEditorWithAppKeys(&m.editor, msg)
	default:
		return m, nil
	}
}

func (m *manualAltScreenInputReproModel) View() string {
	width := max(1, m.width)
	titleStyle := lipgloss.NewStyle().Bold(true)
	helpStyle := lipgloss.NewStyle().Faint(true)
	inputStyle := lipgloss.NewStyle()
	lines := []string{
		titleStyle.Render("Manual alt-screen input cursor repro"),
		"",
		"Type, paste, move with arrows/alt-arrows, delete words/current line, resize the terminal.",
		"Expected: native terminal cursor tracks the input; no reverse-video soft cursor while active.",
		"",
	}
	renderedInput := renderSingleLineEditor(width, 3, m.editor, "› ", true, 0, "type here")
	for _, line := range renderedInput.Lines {
		lines = append(lines, inputStyle.Render(line))
	}
	lines = append(lines, "", helpStyle.Render("Enter clears | Esc/Ctrl-C exits"))
	m.updateTerminalCursor(renderedInput.Cursor)
	return strings.Join(lines, "\n")
}

func (m *manualAltScreenInputReproModel) updateTerminalCursor(cursor tuiinput.FieldCursor) {
	if m.terminalCursor == nil {
		return
	}
	if !cursor.Visible {
		m.terminalCursor.Clear()
		return
	}
	m.terminalCursor.Set(uiTerminalCursorPlacement{
		Visible:   true,
		CursorRow: 5 + cursor.Row,
		CursorCol: cursor.Col,
		AnchorRow: max(0, m.height-1),
		AltScreen: true,
	})
}
