package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/protocol"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
)

const (
	workspaceChangePromptHeaderFallback = "Workspace changed"
)

type workspaceChangePromptResult struct {
	Rebind bool
}

type sessionWorkspaceRetargetContext struct {
	workspaceRoot string
	theme         string
}

type sessionWorkspaceRetargetContextProvider interface {
	workspaceRetargetContext() *sessionWorkspaceRetargetContext
}

func newSessionWorkspaceRetargetContext(workspaceRoot string, theme string) (*sessionWorkspaceRetargetContext, error) {
	normalizedRoot := normalizeWorkspaceChangeDisplayRoot(workspaceRoot)
	if normalizedRoot == "" {
		return nil, errors.New("workspace retarget root is required")
	}
	return &sessionWorkspaceRetargetContext{
		workspaceRoot: normalizedRoot,
		theme:         strings.TrimSpace(theme),
	}, nil
}

func sessionWorkspaceRetargetContextFromBinding(binding protocol.ProjectAttachment, theme string) *sessionWorkspaceRetargetContext {
	return &sessionWorkspaceRetargetContext{
		workspaceRoot: normalizeWorkspaceChangeDisplayRoot(binding.WorkspaceRoot),
		theme:         strings.TrimSpace(theme),
	}
}

func resolveSessionWorkspaceRetargetContext(
	ctx context.Context,
	projectViews apicontract.ProjectViewService,
	projectID string,
	workspaceID string,
	theme string,
) (*sessionWorkspaceRetargetContext, error) {
	if projectViews == nil {
		return nil, errors.New("project view client is required for workspace retarget context")
	}
	trimmedProjectID := strings.TrimSpace(projectID)
	if trimmedProjectID == "" {
		return nil, errors.New("project id is required for workspace retarget context")
	}
	trimmedWorkspaceID := strings.TrimSpace(workspaceID)
	if trimmedWorkspaceID == "" {
		return nil, errors.New("workspace id is required for workspace retarget context")
	}
	overview, err := projectViews.GetProjectOverview(ctx, serverapi.ProjectGetOverviewRequest{ProjectID: trimmedProjectID})
	if err != nil {
		return nil, err
	}
	for _, workspace := range overview.Overview.Workspaces {
		if workspace.WorkspaceID == trimmedWorkspaceID {
			return newSessionWorkspaceRetargetContext(workspace.RootPath, theme)
		}
	}
	return nil, fmt.Errorf("workspace %q is not attached to project %q", trimmedWorkspaceID, trimmedProjectID)
}

type workspaceChangePromptModel struct {
	width        int
	height       int
	theme        string
	selectedRoot string
	currentRoot  string
	cursor       int
	result       workspaceChangePromptResult
	done         bool
}

func inspectPickedSessionWorkspaceChange(
	server sessionWorkspaceChangeServer,
	sessionID string,
	executionTarget clientui.SessionExecutionTarget,
) (*sessionPickerWorkspaceChange, error) {
	if server == nil {
		return nil, errors.New("session server is required")
	}
	if strings.TrimSpace(sessionID) == "" {
		return nil, errors.New("session id is required")
	}
	executionTarget = clientui.NormalizeSessionExecutionTarget(executionTarget)
	if executionTarget.WorkspaceAvailability != clientui.ProjectAvailabilityAvailable {
		return nil, nil
	}
	contextProvider, ok := server.(sessionWorkspaceRetargetContextProvider)
	if !ok {
		return nil, errors.New("workspace retarget context provider is required")
	}
	retargetContext := contextProvider.workspaceRetargetContext()
	if retargetContext == nil {
		return nil, errors.New("workspace retarget context is required")
	}
	currentRoot := normalizeWorkspaceChangeDisplayRoot(retargetContext.workspaceRoot)
	selectedRoot := normalizeWorkspaceChangeDisplayRoot(executionTarget.WorkspaceRoot)
	if comparableWorkspaceChangeRoot(currentRoot) == "" {
		return nil, errors.New("current workspace root is required")
	}
	if comparableWorkspaceChangeRoot(selectedRoot) == "" {
		return nil, errors.New("selected session workspace root is required")
	}
	if comparableWorkspaceChangeRoot(currentRoot) == comparableWorkspaceChangeRoot(selectedRoot) {
		return nil, nil
	}
	return &sessionPickerWorkspaceChange{
		selectedRoot: selectedRoot,
		currentRoot:  currentRoot,
	}, nil
}

func retargetInteractiveSessionWorkspace(ctx context.Context, server sessionLifecycleClientProvider, sessionID string, workspaceRoot string) error {
	if server == nil || server.SessionLifecycleClient() == nil {
		return errors.New("session lifecycle client is required")
	}
	trimmedSessionID := strings.TrimSpace(sessionID)
	if trimmedSessionID == "" {
		return errors.New("session id is required")
	}
	trimmedWorkspaceRoot := strings.TrimSpace(workspaceRoot)
	if trimmedWorkspaceRoot == "" {
		return errors.New("workspace root is required")
	}
	_, err := server.SessionLifecycleClient().RetargetSessionWorkspace(ctx, serverapi.SessionRetargetWorkspaceRequest{ClientRequestID: uuid.NewString(), SessionID: trimmedSessionID, WorkspaceRoot: trimmedWorkspaceRoot})
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
					m.done = true
					return m, tea.Quit
				case 'n', 'q':
					m.result = workspaceChangePromptResult{}
					m.done = true
					return m, tea.Quit
				}
			}
		case tea.KeyEnter:
			m.result = workspaceChangePromptResult{Rebind: m.cursor == 0}
			m.done = true
			return m, tea.Quit
		case tea.KeyEsc, tea.KeyCtrlC:
			m.result = workspaceChangePromptResult{}
			m.done = true
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
