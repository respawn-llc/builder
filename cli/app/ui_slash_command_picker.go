package app

import (
	"context"
	"strings"
	"unicode"

	"core/cli/app/commands"
	"core/cli/app/internal/authui"

	tea "github.com/charmbracelet/bubbletea"
)

const slashCommandPickerLines = 7

type authSlashCommandKind uint8

const (
	authSlashCommandUnknown authSlashCommandKind = iota
	authSlashCommandLogin
	authSlashCommandLogout
)

func authSlashCommandFromName(name string) authSlashCommandKind {
	switch strings.TrimSpace(name) {
	case "login":
		return authSlashCommandLogin
	case "logout":
		return authSlashCommandLogout
	default:
		return authSlashCommandUnknown
	}
}

func (k authSlashCommandKind) commandName() string {
	switch k {
	case authSlashCommandLogin:
		return "login"
	case authSlashCommandLogout:
		return "logout"
	default:
		return ""
	}
}

type slashCommandPickerState struct {
	visible   bool
	matches   []commands.Command
	selection int
	start     int
}

type slashCommandInput struct {
	active       bool
	token        string
	args         string
	argumentMode bool
}

type slashCommandSelection struct {
	input      slashCommandInput
	command    commands.Command
	hasCommand bool
	exact      bool
}

func parseSlashCommandInput(input string) slashCommandInput {
	trimmed := strings.TrimLeftFunc(input, unicode.IsSpace)
	if trimmed == "" || trimmed[0] != '/' {
		return slashCommandInput{}
	}
	payload := strings.TrimLeftFunc(trimmed[1:], unicode.IsSpace)
	if payload == "" {
		return slashCommandInput{active: true}
	}
	spaceIdx := strings.IndexFunc(payload, unicode.IsSpace)
	if spaceIdx < 0 {
		return slashCommandInput{active: true, token: payload}
	}
	return slashCommandInput{
		active:       true,
		token:        payload[:spaceIdx],
		args:         strings.TrimSpace(payload[spaceIdx:]),
		argumentMode: true,
	}
}

func (m *uiModel) refreshSlashCommandFilterFromInputWithAuth(scheduleAuth bool) tea.Cmd {
	parsed := parseSlashCommandInput(m.input)
	if !parsed.active || parsed.argumentMode {
		m.authSlashSessionOpen = false
		m.authSlashLoading = false
		m.slashCommandFilter = ""
		m.slashCommandFilterSet = false
		m.slashCommandSelection = 0
		return nil
	}
	cmd := tea.Cmd(nil)
	if !m.authSlashSessionOpen {
		m.authSlashSessionOpen = true
		m.authSlashGeneration = nextNonZeroToken(m.authSlashGeneration)
	}
	if scheduleAuth {
		cmd = m.requestAuthSlashCommandRefresh()
	}
	normalized := strings.ToLower(strings.TrimSpace(parsed.token))
	if !m.slashCommandFilterSet || m.slashCommandFilter != normalized {
		m.slashCommandSelection = 0
	}
	m.slashCommandFilter = normalized
	m.slashCommandFilterSet = true
	m.clampSlashCommandSelection()
	return cmd
}

func (m *uiModel) currentSlashCommandQuery(token string) string {
	if m.slashCommandFilterSet {
		return m.slashCommandFilter
	}
	return strings.ToLower(strings.TrimSpace(token))
}

func (m *uiModel) currentSlashCommandMatches(token string) []commands.Command {
	if m.commandRegistry == nil {
		return nil
	}
	matches := m.filterSlashCommandMatches(m.commandRegistry.Match(m.currentSlashCommandQuery(token)))
	if len(matches) == 0 {
		return nil
	}
	if m.hasParentSession() {
		return matches
	}
	filtered := make([]commands.Command, 0, len(matches))
	for _, command := range matches {
		if strings.TrimSpace(command.Name) == "back" {
			continue
		}
		filtered = append(filtered, command)
	}
	return filtered
}

func (m *uiModel) filterSlashCommandMatches(matches []commands.Command) []commands.Command {
	if len(matches) == 0 {
		return nil
	}
	filtered := make([]commands.Command, 0, len(matches))
	for _, command := range matches {
		if command.Name == "resume" && !m.resumeCommandAvailable() {
			continue
		}
		if command.Name == "fast" && !m.fastModeAvailable {
			continue
		}
		if command.Name == "copy" && !m.hasAssistantFinalAnswerToCopy() {
			continue
		}
		if isAuthSlashCommand(command.Name) && command.Name != m.authSlashCommand.commandName() {
			continue
		}
		filtered = append(filtered, command)
	}
	return filtered
}

func isAuthSlashCommand(name string) bool {
	switch strings.TrimSpace(name) {
	case "login", "logout":
		return true
	default:
		return false
	}
}

func (m *uiModel) requestAuthSlashCommandRefresh() tea.Cmd {
	if m == nil {
		return nil
	}
	if m.statusConfig.AuthManager == nil {
		m.authSlashCommand = authSlashCommandLogin
		m.authSlashCommandErr = ""
		m.authSlashResolved = m.authSlashGeneration
		m.authSlashLoading = false
		return nil
	}
	if m.authSlashResolved == m.authSlashGeneration || m.authSlashLoading {
		return nil
	}
	m.authSlashToken = nextNonZeroToken(m.authSlashToken)
	token := m.authSlashToken
	generation := m.authSlashGeneration
	loader := m.statusConfig.AuthManager
	m.authSlashLoading = true
	return func() tea.Msg {
		name, err := authui.AuthSlashCommandName(context.Background(), loader)
		return authSlashCommandRefreshedMsg{token: token, generation: generation, name: name, err: err}
	}
}

func (m *uiModel) applyAuthSlashCommandRefreshed(msg authSlashCommandRefreshedMsg) {
	if m == nil || msg.token != m.authSlashToken || msg.generation != m.authSlashGeneration {
		return
	}
	m.authSlashLoading = false
	m.authSlashResolved = msg.generation
	if msg.err != nil {
		m.authSlashCommand = authSlashCommandUnknown
		m.authSlashCommandErr = msg.err.Error()
		m.clampSlashCommandSelection()
		return
	}
	m.authSlashCommand = authSlashCommandFromName(msg.name)
	m.authSlashCommandErr = ""
	m.clampSlashCommandSelection()
}

func (m *uiModel) resumeCommandAvailable() bool {
	if !m.hasOtherSessionsKnown {
		return true
	}
	return m.hasOtherSessions
}

func (m *uiModel) hasParentSession() bool {
	return strings.TrimSpace(m.cachedRuntimeStatus().ParentSessionID) != ""
}

func (m *uiModel) clampSlashCommandSelection() {
	if m.commandRegistry == nil {
		m.slashCommandSelection = 0
		return
	}
	matches := m.filterSlashCommandMatches(m.commandRegistry.Match(m.slashCommandFilter))
	if len(matches) == 0 {
		m.slashCommandSelection = 0
		return
	}
	m.slashCommandSelection = clampSlashPickerIndex(m.slashCommandSelection, 0, len(matches)-1)
}

func (m *uiModel) slashCommandPicker() slashCommandPickerState {
	if m.rollback.isSelecting() {
		return slashCommandPickerState{}
	}
	if m.shouldSuppressSlashCommandPicker() {
		return slashCommandPickerState{}
	}
	parsed := parseSlashCommandInput(m.input)
	if !parsed.active || parsed.argumentMode ||
		m.ask.hasCurrent() {
		return slashCommandPickerState{}
	}
	matches := m.currentSlashCommandMatches(parsed.token)
	selection := 0
	if len(matches) > 0 {
		selection = clampSlashPickerIndex(m.slashCommandSelection, 0, len(matches)-1)
	}
	start := 0
	if len(matches) > slashCommandPickerLines {
		start = selection - (slashCommandPickerLines / 2)
		maxStart := len(matches) - slashCommandPickerLines
		if start < 0 {
			start = 0
		}
		if start > maxStart {
			start = maxStart
		}
	}
	return slashCommandPickerState{
		visible:   true,
		matches:   matches,
		selection: selection,
		start:     start,
	}
}

func (m *uiModel) resolveSlashCommandSelection(input string) slashCommandSelection {
	parsed := parseSlashCommandInput(input)
	selection := slashCommandSelection{input: parsed}
	if !parsed.active || m.commandRegistry == nil {
		return selection
	}
	if parsed.argumentMode {
		command, ok := m.commandRegistry.Command(input)
		if !ok {
			return selection
		}
		selection.command = command
		selection.hasCommand = true
		selection.exact = true
		return selection
	}
	exactCommandText := "/" + strings.ToLower(strings.TrimSpace(parsed.token))
	if command, ok := m.commandRegistry.Command(exactCommandText); ok {
		selection.command = command
		selection.hasCommand = true
		selection.exact = true
		return selection
	}
	matches := m.currentSlashCommandMatches(parsed.token)
	if len(matches) == 0 {
		return selection
	}
	selected := matches[clampSlashPickerIndex(m.slashCommandSelection, 0, len(matches)-1)]
	selection.command = selected
	selection.hasCommand = true
	selection.exact = selected.Name == strings.ToLower(strings.TrimSpace(parsed.token))
	return selection
}

func (s slashCommandSelection) commandText() string {
	if !s.hasCommand {
		return ""
	}
	commandText := "/" + s.command.Name
	if !s.input.argumentMode || strings.TrimSpace(s.input.args) == "" {
		return commandText
	}
	return commandText + " " + strings.TrimSpace(s.input.args)
}

func (s slashCommandSelection) autocompleteText() string {
	if !s.hasCommand {
		return ""
	}
	return "/" + s.command.Name + " "
}

func (s slashCommandSelection) shouldAutocomplete() bool {
	return s.hasCommand && !s.input.argumentMode && !s.exact
}

func (m *uiModel) navigateSlashCommandPicker(delta int) bool {
	if m.shouldSuppressSlashCommandPicker() {
		return false
	}
	state := m.slashCommandPicker()
	if !state.visible || len(state.matches) == 0 {
		return false
	}
	nextSelection := clampSlashPickerIndex(state.selection+delta, 0, len(state.matches)-1)
	m.slashCommandSelection = nextSelection
	m.mainInputDraftToken = nextNonZeroToken(m.mainInputDraftToken)
	m.input = "/" + state.matches[nextSelection].Name
	m.inputCursor = -1
	m.refreshPathReferenceFromInput()
	return true
}

func clampSlashPickerIndex(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}
