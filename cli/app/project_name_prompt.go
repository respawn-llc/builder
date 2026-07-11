package app

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"core/cli/tui"
	tuiinput "core/cli/tui/input"
	sharedtheme "core/shared/theme"

	"charm.land/glamour/v2"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type projectNamePromptModel struct {
	width          int
	height         int
	theme          string
	headerMD       *glamour.TermRenderer
	input          tuiinput.Editor
	terminalCursor *uiTerminalCursorState
	error          string
	result         string
	canceled       bool
}

func newProjectNamePromptModel(defaultName string, theme string) *projectNamePromptModel {
	input := newSingleLineEditor(defaultName)
	return &projectNamePromptModel{
		width:    defaultPickerWidth,
		height:   defaultPickerHeight,
		theme:    theme,
		headerMD: newStartupMarkdownRendererWithWordWrap(theme, 0),
		input:    input,
	}
}

func (m *projectNamePromptModel) Init() tea.Cmd { return nil }

func (m *projectNamePromptModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		case tea.KeyEnter:
			value := strings.TrimSpace(m.input.Text())
			if value == "" {
				m.error = "project name is required"
				return m, nil
			}
			m.result = value
			return m, tea.Quit
		case tea.KeyEsc, tea.KeyCtrlC:
			m.canceled = true
			return m, tea.Quit
		}
	}
	return m, updateSingleLineEditorWithAppKeys(&m.input, msg)
}

func (m *projectNamePromptModel) View() string {
	var out strings.Builder
	out.WriteString(m.renderHeader())
	out.WriteString("\n\n")
	out.WriteString(tui.ApplyThemeStyleIntents("Enter a project name. Press Enter to create the project.", m.theme, tui.ThemeForeground))
	out.WriteString("\n\n")
	out.WriteString(renderStartupEditorField(m.width, m.height, m.theme, m.input, "› ", m.terminalCursor == nil, 0, ""))
	if trimmed := strings.TrimSpace(m.error); trimmed != "" {
		out.WriteString("\n\n")
		out.WriteString(lipgloss.NewStyle().Foreground(sharedtheme.DefaultPalette().Status.Error.Adaptive()).Bold(true).Render(truncateQueuedMessageLine(trimmed, m.width)))
	}
	m.updateTerminalCursor()
	return out.String()
}

func (m *projectNamePromptModel) updateTerminalCursor() {
	if m.terminalCursor == nil {
		return
	}
	headerLines := strings.Split(m.renderHeader(), "\n")
	cursor := renderSingleLineEditor(max(1, m.width), inputContentLineLimit(m.height), m.input, "› ", true, 0, "").Cursor
	if !cursor.Visible {
		m.terminalCursor.Clear()
		return
	}
	m.terminalCursor.Set(uiTerminalCursorPlacement{
		Visible:   true,
		CursorRow: len(headerLines) + 4 + cursor.Row,
		CursorCol: cursor.Col,
		AnchorRow: max(0, m.height-1),
		AltScreen: true,
	})
}

func (m *projectNamePromptModel) renderHeader() string {
	if m.headerMD != nil {
		rendered, err := m.headerMD.Render(projectNamePromptHeaderMarkdown)
		if err == nil {
			return tui.ApplyThemeStyleIntents(trimRenderedHeaderInset(rendered), m.theme, tui.ThemeForeground)
		}
	}
	return lipgloss.NewStyle().Foreground(uiPalette(m.theme).primary).Bold(true).Render(projectNamePromptHeaderFallback)
}

func runProjectNamePrompt(defaultName string, theme string) (string, error) {
	model := newProjectNamePromptModel(defaultName, theme)
	terminalCursor := newUITerminalCursorState()
	model.terminalCursor = terminalCursor
	program := tea.NewProgram(model, tea.WithAltScreen(), tea.WithOutput(newUITerminalCursorWriter(os.Stdout, terminalCursor)))
	finalModel, err := program.Run()
	if err != nil {
		return "", err
	}
	finalized, ok := finalModel.(*projectNamePromptModel)
	if !ok {
		return "", fmt.Errorf("unexpected project name prompt model type %T", finalModel)
	}
	if finalized.canceled {
		return "", errors.New("startup canceled by user")
	}
	return strings.TrimSpace(finalized.result), nil
}

func renderStartupEditorField(width int, height int, theme string, input tuiinput.Editor, prefix string, renderCursor bool, mask rune, placeholder string) string {
	contentWidth := width
	if contentWidth < 1 {
		contentWidth = 1
	}
	lineStyle := lipgloss.NewStyle().Foreground(uiPalette(theme).foreground)
	borderStyle := lipgloss.NewStyle().Foreground(uiPalette(theme).primary)
	return strings.Join(renderSingleLineEditorFramedSoftCursorLines(contentWidth, inputContentLineLimit(height), input, prefix, renderCursor, lineStyle, borderStyle, mask, placeholder), "\n")
}
