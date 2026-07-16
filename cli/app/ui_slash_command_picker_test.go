package app

import (
	"context"
	"errors"
	"testing"

	"core/cli/app/commands"
	"core/server/auth"
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

func TestSlashCommandPickerShowsResumeWhenCurrentSessionIsOnlyKnownSession(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.input = "/re"
	refreshSlashCommandFilterForTest(t, m)

	state := m.slashCommandPicker()
	if !slashPickerContainsCommand(state, "resume") {
		t.Fatalf("expected /resume without another known session, got %+v", slashPickerCommandNames(state))
	}
}

func TestSlashCommandPickerProjectsAuthCommand(t *testing.T) {
	cases := []struct {
		name      string
		manager   *auth.Manager
		visible   string
		hidden    string
		wantTyped authSlashCommandKind
	}{
		{name: "missing auth", visible: "login", hidden: "logout", wantTyped: authSlashCommandLogin},
		{
			name: "api key",
			manager: auth.NewManager(auth.NewMemoryStore(auth.State{
				Scope: auth.ScopeGlobal,
				Method: auth.Method{
					Type:   auth.MethodAPIKey,
					APIKey: &auth.APIKeyMethod{Key: "sk-test"},
				},
			}), nil, nil),
			visible:   "login",
			hidden:    "logout",
			wantTyped: authSlashCommandLogin,
		},
		{
			name: "oauth",
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
			visible:   "logout",
			hidden:    "login",
			wantTyped: authSlashCommandLogout,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newProjectedStaticUIModel(WithUIStatusConfig(uiStatusConfig{AuthManager: tc.manager}))
			m.input = "/"
			refreshSlashCommandFilterForTest(t, m)

			state := m.slashCommandPicker()
			if !slashPickerContainsCommand(state, tc.visible) {
				t.Fatalf("expected /%s in slash picker, got %+v", tc.visible, slashPickerCommandNames(state))
			}
			if m.authSlashCommand != tc.wantTyped {
				t.Fatalf("typed auth slash command = %v, want %v", m.authSlashCommand, tc.wantTyped)
			}
			if slashPickerContainsCommand(state, tc.hidden) || slashPickerContainsCommand(state, "fast") {
				t.Fatalf("unexpected gated command in slash picker: %+v", slashPickerCommandNames(state))
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

func TestSlashCommandPickerAlwaysShowsCopyWithoutReadingCachedRuntimeStatus(t *testing.T) {
	client := &runtimeControlFakeClient{
		status: clientui.RuntimeStatus{LastCommittedAssistantFinalAnswer: "done"},
	}
	m := newProjectedTestUIModel(client)
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
