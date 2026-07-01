package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"core/cli/app/commands"
	"core/cli/tui"
	"core/server/auth"
	"core/server/llm"
	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func refreshSlashCommandFilterForTest(t *testing.T, m *uiModel) {
	t.Helper()
	cmd := m.refreshSlashCommandFilterFromInputWithAuth(true)
	for _, msg := range collectCmdMessages(t, cmd) {
		next, _ := m.Update(msg)
		m = next.(*uiModel)
	}
}

func TestSlashCommandEnterIgnoresWhitespaceImmediatelyAfterSlash(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.sessionName = "existing"
	m.input = "/ name"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected /name command to update the window title")
	}
	if updated.sessionName != "" {
		t.Fatalf("expected / name to behave like /name with empty args, got %q", updated.sessionName)
	}
	if updated.input != "" {
		t.Fatalf("expected input cleared after slash command execution, got %q", updated.input)
	}
}

func TestSlashCommandPickerHighlightTracksSelectionAfterViewportScroll(t *testing.T) {
	withTrueColor(t)
	m := newSlashPickerScrollTestModel()

	targetIndex := slashPickerCommandIndex(m.slashCommandPicker(), "goal")
	if targetIndex <= slashCommandPickerLines/2 {
		t.Fatalf("test setup expected /goal past centered viewport threshold, got index %d", targetIndex)
	}
	for step := 0; step < targetIndex; step++ {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = next.(*uiModel)
	}

	state := m.slashCommandPicker()
	if state.start == 0 {
		t.Fatalf("expected slash picker viewport to scroll for /goal, got %+v", state)
	}
	if m.input != "/goal" {
		t.Fatalf("expected logical slash selection to update input to /goal, got %q", m.input)
	}
	assertActivePickerHighlightedSelection(t, m)
}

func TestSlashCommandPickerHighlightTracksSelectionWhenViewportShiftsBothDirections(t *testing.T) {
	withTrueColor(t)
	m := newSlashPickerScrollTestModel()
	assertActivePickerHighlightedSelection(t, m)

	state := m.slashCommandPicker()
	for step := 1; step < len(state.matches); step++ {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = next.(*uiModel)
		assertActivePickerHighlightedSelection(t, m)
	}
	for step := len(state.matches) - 2; step >= 0; step-- {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
		m = next.(*uiModel)
		assertActivePickerHighlightedSelection(t, m)
	}
}

func TestSlashCommandPickerHighlightTracksFilteredVisibleCommands(t *testing.T) {
	withTrueColor(t)
	oauthManager := auth.NewManager(auth.NewMemoryStore(auth.State{
		Scope: auth.ScopeGlobal,
		Method: auth.Method{
			Type: auth.MethodOAuth,
			OAuth: &auth.OAuthMethod{
				AccessToken: "access-token",
				TokenType:   "Bearer",
			},
		},
	}), nil, nil)
	cases := []struct {
		name    string
		opts    []UIOption
		visible []string
		hidden  []string
	}{
		{
			name:    "no auth hides logout fast resume",
			visible: []string{"login"},
			hidden:  []string{"logout", "fast", "resume"},
		},
		{
			name:    "oauth hides login fast resume",
			opts:    []UIOption{WithUIStatusConfig(uiStatusConfig{AuthManager: oauthManager})},
			visible: []string{"logout"},
			hidden:  []string{"login", "fast", "resume"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := append([]UIOption{WithUIHasOtherSessions(true, false)}, tc.opts...)
			m := newProjectedStaticUIModel(opts...)
			m.input = "/"
			refreshSlashCommandFilterForTest(t, m)

			state := m.slashCommandPicker()
			for _, hidden := range tc.hidden {
				if slashPickerContainsCommand(state, hidden) {
					t.Fatalf("did not expect gated /%s in slash picker, got %+v", hidden, slashPickerCommandNames(state))
				}
			}
			for _, visible := range tc.visible {
				if !slashPickerContainsCommand(state, visible) {
					t.Fatalf("expected visible /%s in slash picker, got %+v", visible, slashPickerCommandNames(state))
				}
			}

			assertActivePickerHighlightAcrossVisibleMatches(t, m)
		})
	}
}

func newSlashPickerScrollTestModel() *uiModel {
	r := commands.NewRegistry()
	registerSlashPickerTestCommand := func(name string) {
		r.RegisterWithOptions(name, "test command "+name, commands.RegisterOptions{PreservePromptHistoryDraft: true}, func(string) commands.Result {
			return commands.Result{Handled: true}
		})
	}
	for _, name := range []string{"aa00", "aa01", "aa02", "aa03", "aa04", "aa05", "aa06", "aa07", "aa08", "goal"} {
		registerSlashPickerTestCommand(name)
	}
	m := newProjectedStaticUIModel(WithUICommandRegistry(r))
	m.input = "/"
	m.refreshSlashCommandFilterFromInputWithAuth(true)
	return m
}

func assertActivePickerHighlightAcrossVisibleMatches(t *testing.T, m *uiModel) {
	t.Helper()
	state := m.activePickerPresentation()
	if !state.visible || len(state.rows) == 0 {
		t.Fatalf("expected visible picker with rows, got %+v", state)
	}
	assertActivePickerHighlightedSelection(t, m)
	for step := 1; step < len(state.rows); step++ {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = next.(*uiModel)
		assertActivePickerHighlightedSelection(t, m)
	}
}

func slashPickerCommandIndex(state slashCommandPickerState, name string) int {
	for idx, command := range state.matches {
		if command.Name == name {
			return idx
		}
	}
	return -1
}

func assertActivePickerHighlightedSelection(t *testing.T, m *uiModel) {
	t.Helper()
	state := m.activePickerPresentation()
	if !state.visible || len(state.rows) == 0 {
		t.Fatalf("expected visible picker with rows, got %+v", state)
	}
	expectedRow := state.selection - state.start
	if expectedRow < 0 || expectedRow >= state.lineCount {
		t.Fatalf("expected visible selected row, got state %+v", state)
	}
	if state.selection < 0 || state.selection >= len(state.rows) {
		t.Fatalf("selected row index out of range for state %+v", state)
	}
}

func TestBuiltInReviewSlashCommandWithWhitespaceAfterSlashDoesNotDuplicateArgs(t *testing.T) {
	r := commands.NewDefaultRegistry()
	m := newProjectedStaticUIModel(WithUICommandRegistry(r))
	m.input = "/ review cli/app"
	if got := r.Execute("/review cli/app"); !got.Handled || !got.SubmitUser {
		t.Fatalf("expected /review command to submit injected user prompt, got %+v", got)
	}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected submission cmd for whitespace-prefixed /review")
	}
	if updated.exitAction != UIActionNone {
		t.Fatalf("expected no session transition for empty-session /review, got %q", updated.exitAction)
	}
	if !updated.isBusy() {
		t.Fatal("expected /review to submit in place for an empty session")
	}
	if updated.nextSessionInitialPrompt != "" {
		t.Fatalf("expected no handoff payload for empty-session /review, got %q", updated.nextSessionInitialPrompt)
	}
	plain := stripANSIAndTrimRight(updated.view.OngoingSnapshot())
	if strings.Contains(plain, "/ review cli/app") {
		t.Fatalf("expected normalized /review prompt content instead of raw command text, got %q", plain)
	}
	if !strings.Contains(plain, "cli/app") {
		t.Fatalf("expected /review args preserved in in-place prompt, got %q", plain)
	}
}

func TestBusyEnterRunsExactFastCommandEvenWhenPickerHidesIt(t *testing.T) {
	client := &runtimeControlFakeClient{status: clientui.RuntimeStatus{FastModeAvailable: true, FastModeEnabled: true}}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.fastModeAvailable = false
	m.setBusy(true)
	m.activity = uiActivityRunning
	m.input = "/fa"
	if picker := m.slashCommandPicker(); !picker.visible || len(picker.matches) != 0 {
		t.Fatalf("expected picker visible without /fast matches, got %+v", picker)
	}
	m.input = "/fast on"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected transient status command for busy /fast")
	}
	for _, msg := range collectCmdMessages(t, cmd) {
		next, _ = updated.Update(msg)
		updated = next.(*uiModel)
	}
	if len(updated.queued) != 0 {
		t.Fatalf("expected no queued messages, got %+v", updated.queued)
	}
	if len(updated.pendingInjected) != 0 {
		t.Fatalf("expected no pending injected messages, got %+v", updated.pendingInjected)
	}
	if updated.isInputSubmitLocked() {
		t.Fatal("did not expect locked input for busy /fast")
	}
	if updated.input != "" {
		t.Fatalf("expected input cleared for busy /fast, got %q", updated.input)
	}
	if !updated.fastModeEnabled {
		t.Fatal("expected busy /fast to enable fast mode")
	}
	if !client.setFastModeArg {
		t.Fatal("expected runtime client fast mode setter to receive true")
	}
	status := stripANSIAndTrimRight(updated.layout().renderStatusLine(120, uiThemeStyles("dark")))
	if !strings.Contains(status, "Fast mode enabled") {
		t.Fatalf("expected busy /fast success in status line, got %q", status)
	}
}

func TestBusyTabBackWithoutParentShowsLocalErrorAndDoesNotQueue(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.activity = uiActivityRunning
	m.input = "/back"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected transient status command for rejected queued /back")
	}
	if len(updated.queued) != 0 {
		t.Fatalf("expected no queued messages, got %+v", updated.queued)
	}
	if len(updated.pendingInjected) != 0 {
		t.Fatalf("expected no pending injected messages, got %+v", updated.pendingInjected)
	}
	if updated.input != "/back" {
		t.Fatalf("expected input preserved for editing after rejected queued /back, got %q", updated.input)
	}
	if !strings.Contains(updated.transientStatus, "No parent session available") {
		t.Fatalf("expected transient error for rejected queued /back, got %q", updated.transientStatus)
	}
	status := stripANSIAndTrimRight(updated.layout().renderStatusLine(120, uiThemeStyles("dark")))
	if !strings.Contains(status, "No parent session available") {
		t.Fatalf("expected queued /back error in status line, got %q", status)
	}
}

func TestSlashCommandPickerHidesResumeWithoutOtherSessions(t *testing.T) {
	m := newProjectedStaticUIModel(WithUIHasOtherSessions(true, false))
	m.input = "/re"
	refreshSlashCommandFilterForTest(t, m)

	state := m.slashCommandPicker()
	if slashPickerContainsCommand(state, "resume") {
		t.Fatalf("did not expect /resume without other sessions, got %+v", slashPickerCommandNames(state))
	}
}

func TestSlashCommandPickerShowsResumeWhenOtherSessionAvailabilityIsUnknown(t *testing.T) {
	m := newProjectedStaticUIModel(WithUIHasOtherSessions(false, false))
	m.input = "/re"
	refreshSlashCommandFilterForTest(t, m)

	state := m.slashCommandPicker()
	if !slashPickerContainsCommand(state, "resume") {
		t.Fatalf("expected /resume when other session availability is unknown, got %+v", slashPickerCommandNames(state))
	}
}

func TestResumeSlashCommandShowsErrorWithoutOtherSessions(t *testing.T) {
	m := newProjectedStaticUIModel(WithUIHasOtherSessions(true, false))
	m.input = "/resume"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected transient status cmd for unavailable /resume")
	}
	if updated.exitAction != UIActionNone {
		t.Fatalf("did not expect session transition action, got %q", updated.exitAction)
	}
	if updated.input != "" {
		t.Fatalf("expected input cleared for unavailable /resume, got %q", updated.input)
	}
	if !strings.Contains(updated.transientStatus, resumeCommandUnavailableMessage) {
		t.Fatalf("expected unavailable /resume status, got %q", updated.transientStatus)
	}
	status := stripANSIAndTrimRight(updated.layout().renderStatusLine(120, uiThemeStyles("dark")))
	if !strings.Contains(status, resumeCommandUnavailableMessage) {
		t.Fatalf("expected unavailable /resume status line, got %q", status)
	}
}

func TestResumeSlashCommandAllowsUnknownOtherSessionAvailability(t *testing.T) {
	m := newProjectedStaticUIModel(WithUIHasOtherSessions(false, false))
	m.input = "/resume"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected quit cmd for /resume when availability is unknown")
	}
	updated := next.(*uiModel)
	if updated.exitAction != UIActionResume {
		t.Fatalf("expected UIActionResume, got %q", updated.exitAction)
	}
}

func TestSlashCommandPickerShowsLoginWhenAuthIsMissingOrAPIKey(t *testing.T) {
	cases := []struct {
		name    string
		manager *auth.Manager
	}{
		{name: "missing auth"},
		{
			name: "api key",
			manager: auth.NewManager(auth.NewMemoryStore(auth.State{
				Scope: auth.ScopeGlobal,
				Method: auth.Method{
					Type:   auth.MethodAPIKey,
					APIKey: &auth.APIKeyMethod{Key: "sk-test"},
				},
			}), nil, nil),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newProjectedStaticUIModel(WithUIStatusConfig(uiStatusConfig{AuthManager: tc.manager}))
			m.input = "/"
			refreshSlashCommandFilterForTest(t, m)

			state := m.slashCommandPicker()
			if !slashPickerContainsCommand(state, "login") {
				t.Fatalf("expected /login in slash picker, got %+v", slashPickerCommandNames(state))
			}
			if m.authSlashCommand != authSlashCommandLogin {
				t.Fatalf("expected typed auth slash command login, got %v", m.authSlashCommand)
			}
			if slashPickerContainsCommand(state, "logout") {
				t.Fatalf("did not expect /logout in slash picker, got %+v", slashPickerCommandNames(state))
			}
		})
	}
}

func TestExactHiddenAuthSlashCommandsStillExecute(t *testing.T) {
	cases := []struct {
		name    string
		manager *auth.Manager
		input   string
	}{
		{
			name: "login while oauth shows logout",
			manager: auth.NewManager(auth.NewMemoryStore(auth.State{
				Scope: auth.ScopeGlobal,
				Method: auth.Method{
					Type: auth.MethodOAuth,
					OAuth: &auth.OAuthMethod{
						AccessToken: "access-token",
						TokenType:   "Bearer",
					},
				},
			}), nil, nil),
			input: "/login",
		},
		{
			name: "logout while api key shows login",
			manager: auth.NewManager(auth.NewMemoryStore(auth.State{
				Scope: auth.ScopeGlobal,
				Method: auth.Method{
					Type:   auth.MethodAPIKey,
					APIKey: &auth.APIKeyMethod{Key: "sk-test"},
				},
			}), nil, nil),
			input: "/logout",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newProjectedStaticUIModel(WithUIStatusConfig(uiStatusConfig{AuthManager: tc.manager}))
			m.input = tc.input

			next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			updated := next.(*uiModel)
			if cmd == nil {
				t.Fatalf("expected %s to execute", tc.input)
			}
			if updated.exitAction != UIActionLogout {
				t.Fatalf("expected %s to execute logout/login transition, got %q", tc.input, updated.exitAction)
			}
		})
	}
}

func TestSlashCommandPickerShowsLogoutForOAuthAuth(t *testing.T) {
	manager := auth.NewManager(auth.NewMemoryStore(auth.State{
		Scope: auth.ScopeGlobal,
		Method: auth.Method{
			Type: auth.MethodOAuth,
			OAuth: &auth.OAuthMethod{
				AccessToken: "access-token",
				TokenType:   "Bearer",
			},
		},
	}), nil, nil)
	m := newProjectedStaticUIModel(WithUIStatusConfig(uiStatusConfig{AuthManager: manager}))
	m.input = "/"
	refreshSlashCommandFilterForTest(t, m)

	state := m.slashCommandPicker()
	if !slashPickerContainsCommand(state, "logout") {
		t.Fatalf("expected /logout in slash picker, got %+v", slashPickerCommandNames(state))
	}
	if m.authSlashCommand != authSlashCommandLogout {
		t.Fatalf("expected typed auth slash command logout, got %v", m.authSlashCommand)
	}
	if slashPickerContainsCommand(state, "login") {
		t.Fatalf("did not expect /login in slash picker, got %+v", slashPickerCommandNames(state))
	}
}

func TestSlashCommandPickerHidesAuthCommandsWhenAuthStateCannotLoad(t *testing.T) {
	manager := auth.NewManager(errorAuthStore{err: errors.New("permission denied")}, nil, nil)
	m := newProjectedStaticUIModel(WithUIStatusConfig(uiStatusConfig{AuthManager: manager}))
	m.input = "/"
	refreshSlashCommandFilterForTest(t, m)

	state := m.slashCommandPicker()
	if slashPickerContainsCommand(state, "login") || slashPickerContainsCommand(state, "logout") {
		t.Fatalf("did not expect auth commands when auth state cannot load, got %+v", slashPickerCommandNames(state))
	}
	if m.authSlashCommandErr == "" {
		t.Fatal("expected auth slash command error to be recorded")
	}
	if m.authSlashCommand != authSlashCommandUnknown {
		t.Fatalf("expected typed auth slash command unknown on load error, got %v", m.authSlashCommand)
	}
}

func TestSlashCommandPickerRefreshesAuthStateAfterModelInit(t *testing.T) {
	store := auth.NewMemoryStore(auth.State{
		Scope: auth.ScopeGlobal,
		Method: auth.Method{
			Type:   auth.MethodAPIKey,
			APIKey: &auth.APIKeyMethod{Key: "sk-test"},
		},
	})
	manager := auth.NewManager(store, nil, nil)
	m := newProjectedStaticUIModel(WithUIStatusConfig(uiStatusConfig{AuthManager: manager}))

	if err := store.Save(context.Background(), auth.State{
		Scope: auth.ScopeGlobal,
		Method: auth.Method{
			Type: auth.MethodOAuth,
			OAuth: &auth.OAuthMethod{
				AccessToken: "access-token",
				TokenType:   "Bearer",
			},
		},
	}); err != nil {
		t.Fatalf("update auth store: %v", err)
	}

	m.input = "/"
	refreshSlashCommandFilterForTest(t, m)
	state := m.slashCommandPicker()
	if !slashPickerContainsCommand(state, "logout") {
		t.Fatalf("expected refreshed /logout in slash picker, got %+v", slashPickerCommandNames(state))
	}
	if slashPickerContainsCommand(state, "login") {
		t.Fatalf("did not expect stale /login after auth refresh, got %+v", slashPickerCommandNames(state))
	}
}

func TestSlashCommandPickerLoadsAuthStateOncePerSlashSession(t *testing.T) {
	store := &countingAuthStore{state: auth.State{
		Scope: auth.ScopeGlobal,
		Method: auth.Method{
			Type: auth.MethodOAuth,
			OAuth: &auth.OAuthMethod{
				AccessToken: "access-token",
				TokenType:   "Bearer",
			},
		},
	}}
	manager := auth.NewManager(store, nil, nil)
	m := newProjectedStaticUIModel(WithUIStatusConfig(uiStatusConfig{AuthManager: manager}))
	loadsAfterInit := store.loads

	for _, input := range []string{"/", "/l", "/lo"} {
		m.input = input
		refreshSlashCommandFilterForTest(t, m)
	}
	if got := store.loads - loadsAfterInit; got != 1 {
		t.Fatalf("expected one auth load while editing one slash session, got %d", got)
	}

	m.input = "ordinary prompt"
	m.refreshSlashCommandFilterFromInputWithAuth(true)
	m.input = "/"
	refreshSlashCommandFilterForTest(t, m)
	if got := store.loads - loadsAfterInit; got != 2 {
		t.Fatalf("expected auth load after starting a new slash session, got %d", got)
	}
}

func TestSlashCommandPickerTypingSlashDefersAuthLoadToCommand(t *testing.T) {
	store := &countingAuthStore{state: auth.State{
		Scope: auth.ScopeGlobal,
		Method: auth.Method{
			Type: auth.MethodOAuth,
			OAuth: &auth.OAuthMethod{
				AccessToken: "access-token",
				TokenType:   "Bearer",
			},
		},
	}}
	manager := auth.NewManager(store, nil, nil)
	m := newProjectedStaticUIModel(WithUIStatusConfig(uiStatusConfig{AuthManager: manager}))
	loadsAfterInit := store.loads

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected auth slash refresh command")
	}
	if got := store.loads - loadsAfterInit; got != 0 {
		t.Fatalf("expected no auth load during Update, got %d", got)
	}
	for _, msg := range collectCmdMessages(t, cmd) {
		next, _ = updated.Update(msg)
		updated = next.(*uiModel)
	}
	if got := store.loads - loadsAfterInit; got != 1 {
		t.Fatalf("expected auth load after command executes, got %d", got)
	}
	if state := updated.slashCommandPicker(); !slashPickerContainsCommand(state, "logout") {
		t.Fatalf("expected /logout after async auth refresh, got %+v", slashPickerCommandNames(state))
	}
}

func TestSlashCommandPickerAuthRefreshSingleFlightsAfterScheduledCommand(t *testing.T) {
	store := &countingAuthStore{state: auth.State{
		Scope: auth.ScopeGlobal,
		Method: auth.Method{
			Type: auth.MethodOAuth,
			OAuth: &auth.OAuthMethod{
				AccessToken: "access-token",
				TokenType:   "Bearer",
			},
		},
	}}
	manager := auth.NewManager(store, nil, nil)
	m := newProjectedStaticUIModel(WithUIStatusConfig(uiStatusConfig{AuthManager: manager}))
	m.replaceMainInput("/", -1)
	if m.authSlashLoading {
		t.Fatal("replaceMainInput must not mark an unscheduled auth refresh in flight")
	}

	cmd := m.refreshSlashCommandFilterFromInputWithAuth(true)
	if cmd == nil {
		t.Fatal("expected auth slash refresh command after state-only input replacement")
	}
	if !m.authSlashLoading {
		t.Fatal("expected scheduled auth slash refresh to be marked loading")
	}
	m.input = "/lo"
	secondCmd := m.refreshSlashCommandFilterFromInputWithAuth(true)
	if secondCmd != nil {
		t.Fatal("did not expect concurrent auth slash refresh while first is loading")
	}
	if store.loads != 0 {
		t.Fatalf("expected no auth load before command executes, got %d", store.loads)
	}
	for _, msg := range collectCmdMessages(t, cmd) {
		next, _ := m.Update(msg)
		m = next.(*uiModel)
	}
	if store.loads != 1 {
		t.Fatalf("expected one auth load after command executes, got %d", store.loads)
	}
	if state := m.slashCommandPicker(); !slashPickerContainsCommand(state, "logout") {
		t.Fatalf("expected /logout after rescheduled auth refresh, got %+v", slashPickerCommandNames(state))
	}
}

type errorAuthStore struct {
	err error
}

func (s errorAuthStore) Load(context.Context) (auth.State, error) {
	return auth.State{}, s.err
}

func (s errorAuthStore) Save(context.Context, auth.State) error {
	return nil
}

type countingAuthStore struct {
	state auth.State
	loads int
}

func (s *countingAuthStore) Load(context.Context) (auth.State, error) {
	s.loads++
	return s.state, nil
}

func (s *countingAuthStore) Save(_ context.Context, state auth.State) error {
	s.state = state
	return nil
}

func TestSlashCommandPickerShowsCopyOnlyWhenFinalAnswerIsAvailable(t *testing.T) {
	hidden := newProjectedStaticUIModel()
	hidden.input = "/co"
	hidden.refreshSlashCommandFilterFromInputWithAuth(true)
	if state := hidden.slashCommandPicker(); slashPickerContainsCommand(state, "copy") {
		t.Fatalf("did not expect /copy without a final answer, got %+v", slashPickerCommandNames(state))
	}

	visible := newProjectedStaticUIModel()
	visible.transcriptEntries = []tui.TranscriptEntry{{Role: "assistant", Text: "done", Phase: llm.MessagePhaseFinal}}
	visible.input = "/co"
	visible.refreshSlashCommandFilterFromInputWithAuth(true)
	state := visible.slashCommandPicker()
	if !state.visible {
		t.Fatal("expected slash picker visible")
	}
	if !slashPickerContainsCommand(state, "copy") {
		t.Fatalf("expected /copy in slash picker, got %+v", slashPickerCommandNames(state))
	}
}

func TestSlashCommandPickerUsesCachedRuntimeStatusForCopy(t *testing.T) {
	client := &runtimeControlFakeClient{
		status: clientui.RuntimeStatus{LastCommittedAssistantFinalAnswer: "done"},
	}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.input = "/co"
	m.refreshSlashCommandFilterFromInputWithAuth(true)

	state := m.slashCommandPicker()
	if !slashPickerContainsCommand(state, "copy") {
		t.Fatalf("expected /copy from cached runtime status, got %+v", slashPickerCommandNames(state))
	}
	if client.refreshMainViewCalls != 0 {
		t.Fatalf("slash picker refreshed runtime status %d times, want 0", client.refreshMainViewCalls)
	}
}

func TestRollbackEditHidesSlashCommandPicker(t *testing.T) {
	m := newProjectedStaticUIModel()
	testSetRollbackEditing(m, 0, 1)
	m.input = "/sta"
	m.refreshSlashCommandFilterFromInputWithAuth(true)

	state := m.slashCommandPicker()
	if state.visible {
		t.Fatalf("did not expect slash picker visible while editing, got %+v", state)
	}
}

func TestRollbackEditRejectsSlashCommandSubmitAndAutocomplete(t *testing.T) {
	m := newProjectedStaticUIModel()
	testSetRollbackEditing(m, 0, 1)
	m.input = "/status"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected transient status command for blocked edit-mode slash command")
	}
	if updated.isBusy() {
		t.Fatal("did not expect slash command to submit while editing")
	}
	if updated.status.open {
		t.Fatal("did not expect /status to open while editing")
	}
	if updated.input != "/status" {
		t.Fatalf("expected blocked slash command to remain editable, got %q", updated.input)
	}
	if updated.transientStatus != slashCommandEditModeError {
		t.Fatalf("expected edit-mode slash error, got %q", updated.transientStatus)
	}

	updated.input = "/sta"
	next, cmd = updated.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated = next.(*uiModel)
	if updated.input != "/sta" {
		t.Fatalf("expected blocked slash autocomplete to preserve input, got %q", updated.input)
	}
	if updated.transientStatus != slashCommandEditModeError {
		t.Fatalf("expected edit-mode slash autocomplete error, got %q", updated.transientStatus)
	}
	status := stripANSIAndTrimRight(updated.layout().renderStatusLine(120, uiThemeStyles("dark")))
	if !strings.Contains(status, slashCommandEditModeError) {
		t.Fatalf("expected edit-mode slash error in status line, got %q", status)
	}
}

func TestRollbackEditRejectsUnknownSlashInputWithoutSubmittingPrompt(t *testing.T) {
	m := newProjectedStaticUIModel()
	testSetRollbackEditing(m, 0, 1)
	m.input = "/nope"
	before := stripANSIAndTrimRight(m.view.OngoingSnapshot())

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected transient status command for blocked unknown slash in edit mode")
	}
	if updated.isBusy() {
		t.Fatal("did not expect unknown slash to submit while editing")
	}
	if len(updated.queued) != 0 {
		t.Fatalf("did not expect queued messages, got %+v", updated.queued)
	}
	if updated.exitAction != UIActionNone {
		t.Fatalf("did not expect session transition action, got %q", updated.exitAction)
	}
	if updated.input != "/nope" {
		t.Fatalf("expected blocked unknown slash to remain editable, got %q", updated.input)
	}
	if updated.transientStatus != slashCommandEditModeError {
		t.Fatalf("expected edit-mode slash error, got %q", updated.transientStatus)
	}
	after := stripANSIAndTrimRight(updated.view.OngoingSnapshot())
	if after != before {
		t.Fatalf("did not expect blocked unknown slash to alter transcript, before=%q after=%q", before, after)
	}
}
