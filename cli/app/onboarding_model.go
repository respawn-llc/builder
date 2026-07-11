package app

import (
	"errors"
	"fmt"
	"strings"
	"time"

	tuiinput "core/cli/tui/input"
	"core/shared/serverapi"
	"core/shared/theme"

	tea "github.com/charmbracelet/bubbletea"
)

type onboardingFinalizeDoneMsg struct {
	result    onboardingResult
	err       error
	submitted bool
}

type onboardingSpinnerTickMsg struct {
	at time.Time
}

const onboardingToggleAllOptionID = "__toggle_all__"

type onboardingModel struct {
	workflow         onboardingWorkflow
	state            onboardingFlowState
	result           onboardingResult
	finalization     *onboardingFinalization
	width            int
	height           int
	styles           onboardingStyles
	spinnerClock     frameAnimationClock
	spinnerFrame     int
	input            tuiinput.Editor
	inputMask        rune
	inputPlaceholder string
	terminalCursor   *uiTerminalCursorState
	currentScreen    onboardingScreen
	stepIndex        int
	cursor           int
	offset           int
	selection        map[string]bool
	errorText        string
	finalizing       bool
	finalizingLabel  string
	canceled         bool
	terminalErr      error
}

func newOnboardingModel(finalization *onboardingFinalization, state onboardingFlowState) *onboardingModel {
	input := newSingleLineEditor("")
	m := &onboardingModel{
		workflow:     newOnboardingWorkflow(&state),
		state:        state,
		finalization: finalization,
		width:        defaultPickerWidth,
		height:       defaultPickerHeight,
		styles:       newOnboardingStyles(state.theme),
		input:        input,
	}
	m.spinnerClock.Start(uiAnimationNow())
	m.syncScreen(true)
	return m
}

func tickOnboardingSpinner(delay time.Duration) tea.Cmd {
	if delay <= 0 {
		delay = spinnerTickInterval
	}
	return tea.Tick(delay, func(now time.Time) tea.Msg {
		return onboardingSpinnerTickMsg{at: now}
	})
}

func (m *onboardingModel) shouldAnimateSpinner() bool {
	return m.finalizing || m.currentScreen.Kind == onboardingScreenLoading
}

func (m *onboardingModel) activeTheme() string {
	if m.currentScreen.ThemePreview && m.currentScreen.Kind == onboardingScreenChoice && m.cursor >= 0 && m.cursor < len(m.currentScreen.Options) {
		if optionTheme := strings.TrimSpace(m.currentScreen.Options[m.cursor].ID); optionTheme == "light" || optionTheme == "dark" {
			return optionTheme
		}
	}
	return theme.Resolve(m.state.settings.Theme)
}

func (m *onboardingModel) applyActiveThemeStyles() {
	activeTheme := m.activeTheme()
	m.styles = newOnboardingStyles(activeTheme)
}

func (m *onboardingModel) Init() tea.Cmd {
	return nil
}

func (m *onboardingModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch typed := msg.(type) {
	case tea.WindowSizeMsg:
		if typed.Width > 0 {
			m.width = typed.Width
		}
		if typed.Height > 0 {
			m.height = typed.Height
		}
		m.ensureCursorVisible()
		return m, nil
	case onboardingImportDiscoveryDoneMsg:
		m.state.imports = typed.discovery
		m.state.imports.pending = false
		if m.state.imports.skipSkills {
			m.state.skillImport = onboardingImportSelection{Mode: onboardingImportModeNone}
		}
		m.syncScreen(false)
		return m, nil
	case onboardingFinalizeDoneMsg:
		m.finalizing = false
		if typed.err != nil {
			if typed.submitted && onboardingCommittedActivationFailed(typed.err) {
				m.terminalErr = fmt.Errorf("onboarding completed but server activation failed: %w", typed.err)
				return m, tea.Quit
			}
			if typed.submitted && onboardingFinalizationIndeterminate(typed.err) {
				m.terminalErr = typed.err
				return m, tea.Quit
			}
			m.errorText = typed.err.Error()
			m.syncScreen(false)
			return m, nil
		}
		m.result = typed.result
		return m, tea.Quit
	case onboardingSpinnerTickMsg:
		m.spinnerFrame = m.spinnerClock.Frame(typed.at, len(pendingToolSpinner.Frames), spinnerTickInterval)
		if !m.shouldAnimateSpinner() {
			return m, nil
		}
		return m, tickOnboardingSpinner(m.spinnerClock.NextDelay(typed.at, spinnerTickInterval))
	case tea.KeyMsg:
		if m.shouldAnimateSpinner() {
			return m, nil
		}
		switch typed.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			m.canceled = true
			return m, tea.Quit
		case tea.KeyUp:
			if m.currentScreen.Kind == onboardingScreenInput {
				return m, m.scrollContent(-1)
			}
			if cmd := m.moveCursor(-1); cmd != nil {
				return m, m.scrollContent(-1)
			}
			return m, nil
		case tea.KeyDown:
			if m.currentScreen.Kind == onboardingScreenInput {
				return m, m.scrollContent(1)
			}
			if cmd := m.moveCursor(1); cmd != nil {
				return m, m.scrollContent(1)
			}
			return m, nil
		case tea.KeyLeft:
			if !typed.Alt {
				return m.goBack()
			}
		case tea.KeyRight:
			if !typed.Alt {
				return m.submitCurrentScreen()
			}
		case tea.KeyEnter:
			return m.submitCurrentScreen()
		case tea.KeySpace:
			if m.currentScreen.Kind == onboardingScreenMulti {
				return m, m.toggleCurrentSelection()
			}
		case tea.KeyBackspace, tea.KeyCtrlH:
			if m.currentScreen.Kind == onboardingScreenMulti {
				return m, m.toggleCurrentSelection()
			}
		case tea.KeyRunes:
			filtered, _ := stripMouseSGRRunes(typed.Runes)
			if len(filtered) == 1 {
				if (filtered[0] == 'a' || filtered[0] == 'A') && m.currentScreen.Kind == onboardingScreenMulti && screenHasToggleAllOption(m.currentScreen) {
					return m, m.toggleAllSelections()
				}
				if filtered[0] == 'j' && m.currentScreen.Kind != onboardingScreenInput {
					if cmd := m.moveCursor(1); cmd != nil {
						return m, m.scrollContent(1)
					}
					return m, nil
				}
				if filtered[0] == 'k' && m.currentScreen.Kind != onboardingScreenInput {
					if cmd := m.moveCursor(-1); cmd != nil {
						return m, m.scrollContent(-1)
					}
					return m, nil
				}
				if filtered[0] >= '1' && filtered[0] <= '9' && m.currentScreen.Kind != onboardingScreenInput {
					index := int(filtered[0] - '1')
					if index < len(m.currentScreen.Options) {
						m.cursor = index
						m.ensureCursorVisible()
						if m.currentScreen.Kind == onboardingScreenChoice {
							return m.submitCurrentScreen()
						}
						if m.currentScreen.Kind == onboardingScreenMulti {
							return m, m.toggleCurrentSelection()
						}
					}
				}
			}
		}
	}
	if m.currentScreen.Kind == onboardingScreenInput {
		return m, updateSingleLineEditorWithAppKeys(&m.input, msg)
	}
	return m, nil
}

func (m *onboardingModel) submitCurrentScreen() (tea.Model, tea.Cmd) {
	step := m.currentStep()
	if step == nil {
		return m, nil
	}
	m.errorText = ""
	var err error
	switch m.currentScreen.Kind {
	case onboardingScreenChoice:
		if m.cursor < 0 || m.cursor >= len(m.currentScreen.Options) {
			return m, nil
		}
		if step.apply != nil {
			err = step.apply(&m.state, m.currentScreen.Options[m.cursor].ID)
		}
	case onboardingScreenInput:
		if step.apply != nil {
			err = step.apply(&m.state, strings.TrimSpace(m.input.Text()))
		}
	case onboardingScreenMulti:
		if step.applyMultiSelect != nil {
			err = step.applyMultiSelect(&m.state, cloneSelection(m.selection))
		}
	}
	if err != nil {
		m.errorText = err.Error()
		m.currentScreen.ErrorText = m.errorText
		return m, nil
	}
	m.currentScreen.ErrorText = ""
	switch m.state.pendingAction {
	case onboardingPendingActionRestart:
		m.state.pendingAction = onboardingPendingActionNone
		m.stepIndex = 0
		m.syncScreen(true)
		return m, nil
	case onboardingPendingActionWriteDefaults:
		m.state.pendingAction = onboardingPendingActionNone
		m.finalizing = true
		m.finalizingLabel = "Writing default configuration..."
		return m, m.finalizeCmd(true)
	case onboardingPendingActionWriteCustom:
		m.state.pendingAction = onboardingPendingActionNone
		m.finalizing = true
		m.finalizingLabel = "Saving first-time setup..."
		return m, m.finalizeCmd(false)
	default:
		m.stepIndex++
		m.syncScreen(true)
		return m, nil
	}
}

func (m *onboardingModel) finalizeCmd(writeDefaults bool) tea.Cmd {
	state := m.state
	return func() tea.Msg {
		request, err := onboardingFinalizeRequest(state, writeDefaults)
		if err != nil {
			return onboardingFinalizeDoneMsg{err: err}
		}
		if m.finalization == nil {
			return onboardingFinalizeDoneMsg{err: errors.New("onboarding finalization is required")}
		}
		return m.finalization.submit(request, writeDefaults, state.settings.Theme)()
	}
}

func onboardingFinalizationIndeterminate(err error) bool {
	var finalizeErr *serverapi.OnboardingFinalizeError
	if errors.As(err, &finalizeErr) {
		return false
	}
	var readinessErr *serverapi.ServerNotReadyError
	return !errors.As(err, &readinessErr)
}

func onboardingCommittedActivationFailed(err error) bool {
	var readinessErr *serverapi.ServerNotReadyError
	if !errors.As(err, &readinessErr) {
		return false
	}
	details, ok := readinessErr.Details.(serverapi.ServerNotReadyDetails)
	return ok && details.OnboardingCompleted
}

func (m *onboardingModel) currentStep() *onboardingStepDefinition {
	steps := m.workflow.visibleSteps(&m.state)
	if len(steps) == 0 {
		return nil
	}
	if m.stepIndex >= len(steps) {
		m.stepIndex = len(steps) - 1
	}
	if m.stepIndex < 0 {
		m.stepIndex = 0
	}
	return &steps[m.stepIndex]
}

func (m *onboardingModel) syncScreen(resetViewport bool) {
	step := m.currentStep()
	if step == nil {
		return
	}
	screen := step.build(&m.state)
	previousID := m.currentScreen.ID
	previousKind := m.currentScreen.Kind
	inputDraft := m.input.Text()
	m.currentScreen = screen
	if resetViewport || previousID != screen.ID {
		m.offset = 0
	}
	if screen.Kind == onboardingScreenInput {
		if !resetViewport && previousID == screen.ID && previousKind == onboardingScreenInput {
			m.input.Replace(strings.NewReplacer("\r", "", "\n", "").Replace(inputDraft))
		} else {
			m.input.Replace(strings.NewReplacer("\r", "", "\n", "").Replace(screen.InputValue))
		}
		m.inputPlaceholder = screen.Placeholder
		if screen.SensitiveInput {
			m.inputMask = '*'
		} else {
			m.inputMask = 0
		}
	}
	if screen.Kind == onboardingScreenMulti {
		m.selection = cloneSelection(screen.Selection)
		if m.selection == nil {
			m.selection = map[string]bool{}
		}
		m.refreshToggleAllOption()
	}
	m.cursor = 0
	if screen.Kind == onboardingScreenChoice {
		for index, option := range screen.Options {
			if option.ID == screen.DefaultOptionID {
				m.cursor = index
				break
			}
		}
	}
	m.ensureCursorVisible()
	if m.errorText != "" && screen.ErrorText == "" {
		m.currentScreen.ErrorText = m.errorText
	}
	if screen.Kind != onboardingScreenLoading {
		m.finalizingLabel = ""
	}
	if screen.Kind != onboardingScreenMulti {
		m.selection = nil
	}
	if screen.Kind == onboardingScreenMulti {
		m.cursor = 0
	}
	if screen.Kind == onboardingScreenInput {
		m.cursor = 0
	}
	if screen.Kind == onboardingScreenChoice && screen.DefaultOptionID == "" && len(screen.Options) > 0 {
		m.cursor = 0
	}
	m.applyActiveThemeStyles()
	if !resetViewport && previousID == screen.ID && screen.Kind == onboardingScreenMulti {
		m.ensureCursorVisible()
	}
	if !resetViewport && previousID == screen.ID && screen.Kind == onboardingScreenChoice {
		m.ensureCursorVisible()
	}
	if previousID != screen.ID {
		m.errorText = ""
	}
	m.ensureCursorVisible()
}

func (m *onboardingModel) moveCursor(delta int) tea.Cmd {
	limit := len(m.currentScreen.Options)
	if limit == 0 {
		return onboardingBellCmd()
	}
	previous := m.cursor
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= limit {
		m.cursor = limit - 1
	}
	m.ensureCursorVisible()
	if previous == m.cursor {
		return onboardingBellCmd()
	}
	m.applyActiveThemeStyles()
	return nil
}

func (m *onboardingModel) scrollContent(delta int) tea.Cmd {
	content := m.buildContent(max(1, m.width))
	maxOffset := len(content.lines) - m.contentHeight()
	if maxOffset < 0 {
		maxOffset = 0
	}
	previous := m.offset
	m.offset += delta
	if m.offset < 0 {
		m.offset = 0
	}
	if m.offset > maxOffset {
		m.offset = maxOffset
	}
	if previous == m.offset {
		return onboardingBellCmd()
	}
	return nil
}

func (m *onboardingModel) goBack() (tea.Model, tea.Cmd) {
	if m.stepIndex <= 0 {
		return m, onboardingBellCmd()
	}
	m.stepIndex--
	m.syncScreen(true)
	return m, nil
}

func (m *onboardingModel) toggleCurrentSelection() tea.Cmd {
	if m.cursor < 0 || m.cursor >= len(m.currentScreen.Options) {
		return onboardingBellCmd()
	}
	id := m.currentScreen.Options[m.cursor].ID
	if id == onboardingToggleAllOptionID {
		return m.toggleAllSelections()
	}
	m.selection[id] = !m.selection[id]
	m.refreshToggleAllOption()
	return nil
}

func (m *onboardingModel) toggleAllSelections() tea.Cmd {
	if !screenHasToggleAllOption(m.currentScreen) {
		return onboardingBellCmd()
	}
	setEnabled := !allSelectableOptionsEnabled(m.currentScreen.Options, m.selection)
	for _, option := range m.currentScreen.Options {
		if option.ID == onboardingToggleAllOptionID {
			continue
		}
		m.selection[option.ID] = setEnabled
	}
	m.refreshToggleAllOption()
	return nil
}

func (m *onboardingModel) refreshToggleAllOption() {
	allEnabled := allSelectableOptionsEnabled(m.currentScreen.Options, m.selection)
	m.selection[onboardingToggleAllOptionID] = allEnabled
	for index, option := range m.currentScreen.Options {
		if option.ID != onboardingToggleAllOptionID {
			continue
		}
		m.currentScreen.Options[index].Title = toggleAllOptionTitle(m.currentScreen.Options, m.selection)
		return
	}
}

func (m *onboardingModel) ensureCursorVisible() {
	content := m.buildContent(max(1, m.width))
	maxOffset := len(content.lines) - m.contentHeight()
	if maxOffset < 0 {
		maxOffset = 0
	}
	if m.offset < 0 {
		m.offset = 0
	}
	if m.offset > maxOffset {
		m.offset = maxOffset
	}
	if content.cursorRow < 0 {
		return
	}
	contentHeight := m.contentHeight()
	if content.cursorRow < m.offset {
		m.offset = content.cursorRow
	}
	if content.cursorRow >= m.offset+contentHeight {
		m.offset = content.cursorRow - contentHeight + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
	if m.offset > maxOffset {
		m.offset = maxOffset
	}
}

func screenHasToggleAllOption(screen onboardingScreen) bool {
	for _, option := range screen.Options {
		if option.ID == onboardingToggleAllOptionID {
			return true
		}
	}
	return false
}

func toggleAllOptionTitle(options []onboardingOption, selection map[string]bool) string {
	if allSelectableOptionsEnabled(options, selection) {
		return "Disable all"
	}
	return "Enable all"
}

func allSelectableOptionsEnabled(options []onboardingOption, selection map[string]bool) bool {
	hasSelectable := false
	for _, option := range options {
		if option.ID == onboardingToggleAllOptionID {
			continue
		}
		hasSelectable = true
		if !selection[option.ID] {
			return false
		}
	}
	return hasSelectable
}

func onboardingBellCmd() tea.Cmd {
	return func() tea.Msg {
		fmt.Print("\a")
		return nil
	}
}
