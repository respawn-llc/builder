package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/cli/app/commands"
	"core/shared/clientui"
	authpb "core/shared/protoapi/gen/kent/api/auth"
	"core/shared/textutil"

	tea "github.com/charmbracelet/bubbletea"
)

type deadlineAuthStatusClient struct {
	remaining time.Duration
}

func (c *deadlineAuthStatusClient) GetStatus(ctx context.Context, _ *authpb.GetStatusRequest) (*authpb.Status, error) {
	deadline, ok := ctx.Deadline()
	if ok {
		c.remaining = time.Until(deadline)
	}
	return authStatusResponse(authpb.AuthMethod_AUTH_METHOD_NONE), nil
}

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
	testSetMainInput(m, "/ name")

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected /name command to update the window title")
	}
	if updated.sessionName != "" {
		t.Fatalf("expected / name to behave like /name with empty args, got %q", updated.sessionName)
	}
	if testMainInput(updated) != "" {
		t.Fatalf("expected input cleared after slash command execution, got %q", testMainInput(updated))
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
	if testMainInput(m) != "/goal" {
		t.Fatalf("expected logical slash selection to update input to /goal, got %q", testMainInput(m))
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
	testSetMainInput(m, "/")
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
	testSetMainInput(m, "/re")
	refreshSlashCommandFilterForTest(t, m)

	state := m.slashCommandPicker()
	if !slashPickerContainsCommand(state, "resume") {
		t.Fatalf("expected /resume without another known session, got %+v", slashPickerCommandNames(state))
	}
}

func TestSlashCommandPickerProjectsAuthCommand(t *testing.T) {
	cases := []struct {
		name      string
		method    authpb.AuthMethod
		visible   string
		hidden    string
		wantTyped authSlashCommandKind
	}{
		{name: "no auth", method: authpb.AuthMethod_AUTH_METHOD_NONE, visible: "login", hidden: "logout", wantTyped: authSlashCommandLogin},
		{name: "api key", method: authpb.AuthMethod_AUTH_METHOD_API_KEY, visible: "login", hidden: "logout", wantTyped: authSlashCommandLogin},
		{name: "oauth", method: authpb.AuthMethod_AUTH_METHOD_OAUTH, visible: "logout", hidden: "login", wantTyped: authSlashCommandLogout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &staticAuthStatusClient{response: authStatusResponse(tc.method)}
			m := newProjectedStaticUIModel(WithUIStatusConfig(uiStatusConfig{
				AuthStatus: client,
			}))
			testSetMainInput(m, "/")
			refreshSlashCommandFilterForTest(t, m)

			state := m.slashCommandPicker()
			if !slashPickerContainsCommand(state, tc.visible) {
				t.Fatalf("expected /%s in slash picker, got %+v", tc.visible, slashPickerCommandNames(state))
			}
			if m.authSlashCommand != tc.wantTyped {
				t.Fatalf("typed auth slash command = %v, want %v", m.authSlashCommand, tc.wantTyped)
			}
			if !client.request.SkipSubscriptionUsage {
				t.Fatalf("slash auth request = %+v, want subscription usage skipped", client.request)
			}
			if slashPickerContainsCommand(state, tc.hidden) || slashPickerContainsCommand(state, "fast") {
				t.Fatalf("unexpected gated command in slash picker: %+v", slashPickerCommandNames(state))
			}
		})
	}
}

func TestExactHiddenAuthSlashCommandsStillExecute(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{name: "login", input: "/login"},
		{name: "logout", input: "/logout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newProjectedStaticUIModel()
			testSetMainInput(m, tc.input)

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
	m := newProjectedStaticUIModel(WithUIStatusConfig(uiStatusConfig{
		AuthStatus: &staticAuthStatusClient{err: errors.New("permission denied")},
	}))
	testSetMainInput(m, "/")
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
	client := &staticAuthStatusClient{response: authStatusResponse(authpb.AuthMethod_AUTH_METHOD_API_KEY)}
	m := newProjectedStaticUIModel(WithUIStatusConfig(uiStatusConfig{AuthStatus: client}))
	client.response = authStatusResponse(authpb.AuthMethod_AUTH_METHOD_OAUTH)

	testSetMainInput(m, "/")
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
	client := &staticAuthStatusClient{response: authStatusResponse(authpb.AuthMethod_AUTH_METHOD_OAUTH)}
	m := newProjectedStaticUIModel(WithUIStatusConfig(uiStatusConfig{AuthStatus: client}))
	callsAfterInit := client.calls

	for _, input := range []string{"/", "/l", "/lo"} {
		testSetMainInput(m, input)
		refreshSlashCommandFilterForTest(t, m)
	}
	if got := client.calls - callsAfterInit; got != 1 {
		t.Fatalf("expected one auth request while editing one slash session, got %d", got)
	}

	testSetMainInput(m, "ordinary prompt")
	m.refreshSlashCommandFilterFromInputWithAuth(true)
	testSetMainInput(m, "/")
	refreshSlashCommandFilterForTest(t, m)
	if got := client.calls - callsAfterInit; got != 2 {
		t.Fatalf("expected auth request after starting a new slash session, got %d", got)
	}
}

func TestSlashCommandPickerTypingSlashDefersAuthLoadToCommand(t *testing.T) {
	client := &staticAuthStatusClient{response: authStatusResponse(authpb.AuthMethod_AUTH_METHOD_OAUTH)}
	m := newProjectedStaticUIModel(WithUIStatusConfig(uiStatusConfig{AuthStatus: client}))
	callsAfterInit := client.calls

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected auth slash refresh command")
	}
	if got := client.calls - callsAfterInit; got != 0 {
		t.Fatalf("expected no auth request during Update, got %d", got)
	}
	for _, msg := range collectCmdMessages(t, cmd) {
		next, _ = updated.Update(msg)
		updated = next.(*uiModel)
	}
	if got := client.calls - callsAfterInit; got != 1 {
		t.Fatalf("expected auth request after command executes, got %d", got)
	}
	if state := updated.slashCommandPicker(); !slashPickerContainsCommand(state, "logout") {
		t.Fatalf("expected /logout after async auth refresh, got %+v", slashPickerCommandNames(state))
	}
}

func TestSlashCommandPickerAuthRefreshSingleFlightsAfterScheduledCommand(t *testing.T) {
	client := &staticAuthStatusClient{response: authStatusResponse(authpb.AuthMethod_AUTH_METHOD_OAUTH)}
	m := newProjectedStaticUIModel(WithUIStatusConfig(uiStatusConfig{AuthStatus: client}))
	m.replaceMainInputAtEnd("/")
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
	testSetMainInput(m, "/lo")
	secondCmd := m.refreshSlashCommandFilterFromInputWithAuth(true)
	if secondCmd != nil {
		t.Fatal("did not expect concurrent auth slash refresh while first is loading")
	}
	if client.calls != 0 {
		t.Fatalf("expected no auth request before command executes, got %d", client.calls)
	}
	for _, msg := range collectCmdMessages(t, cmd) {
		next, _ := m.Update(msg)
		m = next.(*uiModel)
	}
	if client.calls != 1 {
		t.Fatalf("expected one auth request after command executes, got %d", client.calls)
	}
	if state := m.slashCommandPicker(); !slashPickerContainsCommand(state, "logout") {
		t.Fatalf("expected /logout after rescheduled auth refresh, got %+v", slashPickerCommandNames(state))
	}
}

func TestSlashCommandPickerAuthRefreshUsesBoundedStatusTimeout(t *testing.T) {
	client := &deadlineAuthStatusClient{}
	m := newProjectedStaticUIModel(WithUIStatusConfig(uiStatusConfig{AuthStatus: client}))
	m.replaceMainInputAtEnd("/")
	cmd := m.refreshSlashCommandFilterFromInputWithAuth(true)
	if cmd == nil {
		t.Fatal("expected auth slash refresh command")
	}
	_ = cmd()
	if client.remaining <= 0 || client.remaining > statusRefreshTimeout {
		t.Fatalf("auth slash refresh timeout = %s, want bounded by %s", client.remaining, statusRefreshTimeout)
	}
}

func TestSlashCommandPickerAlwaysShowsCopyWithoutReadingCachedRuntimeStatus(t *testing.T) {
	client := &runtimeControlFakeClient{
		status: clientui.RuntimeStatus{LastCommittedAssistantFinalAnswer: textutil.Value("done")},
	}
	m := newProjectedTestUIModel(client)
	testSetMainInput(m, "/co")
	m.refreshSlashCommandFilterFromInputWithAuth(true)

	state := m.slashCommandPicker()
	if !slashPickerContainsCommand(state, "copy") {
		t.Fatalf("expected /copy from cached runtime status, got %+v", slashPickerCommandNames(state))
	}
	if client.refreshMainViewCalls != 0 {
		t.Fatalf("slash picker refreshed runtime status %d times, want 0", client.refreshMainViewCalls)
	}
}
