package app

import (
	"context"
	"core/shared/serverapi"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
)

const (
	workspaceChangePromptHeaderFallback = "Workspace changed"
)

var runWorkspaceChangePromptFlow = runWorkspaceChangePrompt

type sessionWorkspaceChangeAction int

const (
	sessionWorkspaceChangeProceed sessionWorkspaceChangeAction = iota
	sessionWorkspaceChangePickAgain
)

type workspaceChangePromptResult struct {
	Rebind bool
}

type workspaceChangePromptModel struct {
	width        int
	height       int
	theme        string
	selectedRoot string
	currentRoot  string
	cursor       int
	result       workspaceChangePromptResult
}

func maybeHandlePickedSessionWorkspaceChange(ctx context.Context, server sessionWorkspaceChangeServer, sessionID string, selectedWorkspaceRoot string) (sessionWorkspaceChangeAction, error) {
	if server == nil {
		return sessionWorkspaceChangeProceed, errors.New("embedded server is required")
	}
	if strings.TrimSpace(sessionID) == "" {
		return sessionWorkspaceChangeProceed, errors.New("session id is required")
	}
	currentRoot := normalizeWorkspaceChangeDisplayRoot(server.Config().WorkspaceRoot)
	selectedRoot := normalizeWorkspaceChangeDisplayRoot(selectedWorkspaceRoot)
	if comparableWorkspaceChangeRoot(currentRoot) == "" || comparableWorkspaceChangeRoot(selectedRoot) == "" || comparableWorkspaceChangeRoot(currentRoot) == comparableWorkspaceChangeRoot(selectedRoot) {
		return sessionWorkspaceChangeProceed, nil
	}
	result, err := runWorkspaceChangePromptFlow(selectedRoot, currentRoot, server.Config().Settings.Theme)
	if err != nil {
		return sessionWorkspaceChangeProceed, err
	}
	if !result.Rebind {
		return sessionWorkspaceChangePickAgain, nil
	}
	if err := retargetInteractiveSessionWorkspace(ctx, server, sessionID); err != nil {
		return sessionWorkspaceChangeProceed, err
	}
	return sessionWorkspaceChangeProceed, nil
}

func retargetInteractiveSessionWorkspace(ctx context.Context, server sessionWorkspaceChangeServer, sessionID string) error {
	if server == nil || server.SessionLifecycleClient() == nil {
		return errors.New("session lifecycle client is required")
	}
	trimmedSessionID := strings.TrimSpace(sessionID)
	if trimmedSessionID == "" {
		return errors.New("session id is required")
	}
	workspaceRoot := strings.TrimSpace(server.Config().WorkspaceRoot)
	if workspaceRoot == "" {
		return errors.New("workspace root is required")
	}
	_, err := server.SessionLifecycleClient().RetargetSessionWorkspace(ctx, serverapi.SessionRetargetWorkspaceRequest{ClientRequestID: uuid.NewString(), SessionID: trimmedSessionID, WorkspaceRoot: workspaceRoot})
	return err
}

func normalizeWorkspaceChangeDisplayRoot(root string) string {
	trimmed := strings.TrimSpace(root)
	if trimmed == "" {
		return ""
	}
	return filepath.Clean(trimmed)
}

func comparableWorkspaceChangeRoot(root string) string {
	trimmed := strings.TrimSpace(root)
	if trimmed == "" {
		return ""
	}
	absRoot := trimmed
	if !filepath.IsAbs(absRoot) {
		if resolved, err := filepath.Abs(absRoot); err == nil {
			absRoot = resolved
		}
	}
	if canonical, err := filepath.EvalSymlinks(absRoot); err == nil {
		return filepath.Clean(canonical)
	}
	return filepath.Clean(absRoot)
}

func newWorkspaceChangePromptModel(selectedRoot string, currentRoot string, theme string) *workspaceChangePromptModel {
	return &workspaceChangePromptModel{
		width:        defaultPickerWidth,
		height:       defaultPickerHeight,
		theme:        theme,
		selectedRoot: strings.TrimSpace(selectedRoot),
		currentRoot:  strings.TrimSpace(currentRoot),
		cursor:       1,
	}
}

func (m *workspaceChangePromptModel) Init() tea.Cmd { return nil }

func (m *workspaceChangePromptModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		case tea.KeyUp:
			m.moveCursor(-1)
		case tea.KeyDown:
			m.moveCursor(1)
		case tea.KeyRunes:
			filtered, _ := stripMouseSGRRunes(typed.Runes)
			if len(filtered) == 1 {
				switch filtered[0] {
				case 'k':
					m.moveCursor(-1)
				case 'j':
					m.moveCursor(1)
				case 'y':
					m.result = workspaceChangePromptResult{Rebind: true}
					return m, tea.Quit
				case 'n', 'q':
					m.result = workspaceChangePromptResult{}
					return m, tea.Quit
				}
			}
		case tea.KeyEnter:
			m.result = workspaceChangePromptResult{Rebind: m.cursor == 0}
			return m, tea.Quit
		case tea.KeyEsc, tea.KeyCtrlC:
			m.result = workspaceChangePromptResult{}
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *workspaceChangePromptModel) View() string {
	return renderStartupFullScreenPrompt(startupFullScreenPromptSpec{
		Width:           m.width,
		Height:          m.height,
		Title:           renderStartupPlainTitle(workspaceChangePromptHeaderFallback, m.theme),
		Theme:           m.theme,
		Lines:           m.promptLines(),
		Footer:          "↑/↓ pick | enter confirm | esc return to picker",
		MinContentLines: 3,
	})
}

func (m *workspaceChangePromptModel) moveCursor(delta int) {
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor > 1 {
		m.cursor = 1
	}
}

func (m *workspaceChangePromptModel) promptLines() []askPromptLine {
	return []askPromptLine{
		{Text: fmt.Sprintf("This session started in %q but Kent's current is %q. Continue in new location?", m.selectedRoot, m.currentRoot), Kind: askPromptLineKindQuestion},
		{Text: "", Kind: askPromptLineKindQuestion},
		{Text: fmt.Sprintf("%d. %s", 1, "Yes"), Kind: askPromptLineKindOption, Selected: m.cursor == 0},
		{Text: fmt.Sprintf("%d. %s", 2, "No"), Kind: askPromptLineKindOption, Selected: m.cursor == 1},
	}
}

func runWorkspaceChangePrompt(selectedRoot string, currentRoot string, theme string) (workspaceChangePromptResult, error) {
	model := newWorkspaceChangePromptModel(selectedRoot, currentRoot, theme)
	program := tea.NewProgram(model, tea.WithAltScreen())
	finalModel, err := program.Run()
	if err != nil {
		return workspaceChangePromptResult{}, err
	}
	finalized, ok := finalModel.(*workspaceChangePromptModel)
	if !ok {
		return workspaceChangePromptResult{}, fmt.Errorf("unexpected workspace change prompt model type %T", finalModel)
	}
	return finalized.result, nil
}
