package app

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type staticAuthStatusClient struct {
	response serverapi.AuthStatusResponse
	err      error
	calls    int
}

func (c *staticAuthStatusClient) GetAuthStatus(context.Context, serverapi.AuthStatusRequest) (serverapi.AuthStatusResponse, error) {
	c.calls++
	return c.response, c.err
}

func authStatusResponse(method serverapi.AuthStatusMethod) serverapi.AuthStatusResponse {
	facts := serverapi.AuthStatusFacts{
		Method:        method,
		Provider:      serverapi.OpenAIAuthProviderFacts(),
		EnvPreference: serverapi.AuthStatusEnvPreferenceUnspecified,
	}
	switch method {
	case serverapi.AuthStatusMethodAPIKey:
		facts.APIKey = &serverapi.AuthAPIKeyFacts{}
	case serverapi.AuthStatusMethodOAuth:
		facts.OAuth = &serverapi.AuthOAuthFacts{}
	}
	return serverapi.AuthStatusResponse{Resolution: serverapi.KnownAuthStatusResolution(facts, nil)}
}

func newTestSessionPickerModel(t *testing.T, summaries []clientui.SessionSummary, header sessionPickerHeaderInfo) *sessionPickerModel {
	t.Helper()
	model := newUninitializedTestSessionPickerModel(t, summaries, header)
	runSessionPickerCommands(t, model, model.Init())
	return model
}

func newUninitializedTestSessionPickerModel(t *testing.T, summaries []clientui.SessionSummary, header sessionPickerHeaderInfo) *sessionPickerModel {
	t.Helper()
	if summaries == nil {
		summaries = []clientui.SessionSummary{pickerTestSummary(t, "test-session", time.Now().UTC())}
	}
	loader := &recordingSessionPageLoader{responses: func(request serverapi.SessionPageRequest) sessionPageLoadResult {
		response := pickerPageResponse(t, request)
		if request.Category == sessioncontract.SessionCategoryMain {
			response.Sessions = append([]clientui.SessionSummary(nil), summaries...)
		}
		return sessionPageLoadResult{response: response}
	}}
	return newSessionPickerModel(context.Background(), loader, "dark", header)
}

func pickerTestSummary(t *testing.T, raw string, updatedAt time.Time) clientui.SessionSummary {
	t.Helper()
	id, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("ParseSessionID(%q): %v", raw, err)
	}
	return clientui.SessionSummary{
		SessionID: id,
		Category:  sessioncontract.SessionCategoryMain,
		UpdatedAt: updatedAt,
	}
}

func TestSessionPickerScrollsAndSelects(t *testing.T) {
	now := time.Date(2026, time.February, 8, 12, 0, 0, 0, time.UTC)
	summaries := make([]clientui.SessionSummary, 0, 20)
	for i := 0; i < 20; i++ {
		summaries = append(summaries, pickerTestSummary(t, fmt.Sprintf("s-%02d", i), now.Add(-time.Duration(i)*time.Minute)))
	}

	m := newTestSessionPickerModel(t, summaries, sessionPickerHeaderInfo{})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 8})
	m = next.(*sessionPickerModel)
	for i := 0; i < 16; i++ {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = next.(*sessionPickerModel)
	}

	tab := m.tab(sessioncontract.SessionCategoryMain)
	if got := tab.selectedIndex(); got == nil || *got != 16 {
		t.Fatalf("selected index=%v want 16", got)
	}
	if tab.offset == 0 {
		t.Fatalf("offset should advance for scroll")
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*sessionPickerModel)
	open, present := m.result.(sessionPickerOpenResult)
	if !present || open.sessionID != summaries[15].SessionID {
		t.Fatalf("selected=%+v want %s", m.result, summaries[15].SessionID.String())
	}
}

func TestSessionPickerPageKeysMoveByVisibleWindow(t *testing.T) {
	now := time.Date(2026, time.February, 8, 12, 0, 0, 0, time.UTC)
	summaries := make([]clientui.SessionSummary, 0, 20)
	for i := 0; i < 20; i++ {
		summaries = append(summaries, pickerTestSummary(t, fmt.Sprintf("page-%02d", i), now.Add(-time.Duration(i)*time.Minute)))
	}
	model := newTestSessionPickerModel(t, summaries, sessionPickerHeaderInfo{})
	model.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	tab := model.tab(sessioncontract.SessionCategoryMain)
	pageSize := len(model.visibleRowsFromOffset(tab, tab.offset))
	if pageSize < 1 {
		t.Fatal("visible picker page is empty")
	}

	runSessionPickerCommands(t, model, pickerUpdateCommand(t, model, tea.KeyMsg{Type: tea.KeyPgDown}))
	if selected := tab.selectedIndex(); selected == nil || *selected != pageSize {
		t.Fatalf("PgDn selected index = %v, want %d", selected, pageSize)
	}
	runSessionPickerCommands(t, model, pickerUpdateCommand(t, model, tea.KeyMsg{Type: tea.KeyPgUp}))
	if selected := tab.selectedIndex(); selected == nil || *selected != 0 {
		t.Fatalf("PgUp selected index = %v, want 0", selected)
	}
}

func TestSessionPickerEnterDefaultsToCreateNew(t *testing.T) {
	m := newTestSessionPickerModel(t, nil, sessionPickerHeaderInfo{})
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*sessionPickerModel)
	if _, ok := m.result.(sessionPickerCreateResult); !ok {
		t.Fatal("expected default selection to create a new session")
	}
}

func TestSessionPickerNewHotkeyAndExitKeys(t *testing.T) {
	m := newTestSessionPickerModel(t, nil, sessionPickerHeaderInfo{})

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = next.(*sessionPickerModel)
	if _, ok := m.result.(sessionPickerCreateResult); !ok {
		t.Fatal("expected create-new result")
	}

	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEsc},
		{Type: tea.KeyRunes, Runes: []rune{'q'}},
	} {
		m = newTestSessionPickerModel(t, []clientui.SessionSummary{pickerTestSummary(t, "s-1", time.Now().UTC())}, sessionPickerHeaderInfo{})
		next, cmd := m.Update(key)
		m = next.(*sessionPickerModel)
		if cmd != nil {
			t.Fatalf("%v unexpectedly ended the session picker", key)
		}
		selected := m.tab(sessioncontract.SessionCategoryMain).selectedIndex()
		if selected == nil || *selected != 0 || m.result != nil {
			t.Fatalf("%v changed picker state: selected=%v result=%+v", key, selected, m.result)
		}
	}

	m = newTestSessionPickerModel(t, nil, sessionPickerHeaderInfo{})
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = next.(*sessionPickerModel)
	if cmd == nil {
		t.Fatal("expected Ctrl+C to exit the session picker")
	}
	if _, ok := m.result.(sessionPickerCancelResult); !ok {
		t.Fatal("expected Ctrl+C cancellation result")
	}
}

func TestSessionPickerIgnoresMouseSGRRunes(t *testing.T) {
	m := newTestSessionPickerModel(t, nil, sessionPickerHeaderInfo{})
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[<64;81;40M[<65;80;39M")})
	m = next.(*sessionPickerModel)
	if m.result != nil {
		t.Fatalf("expected mouse sgr runes ignored, got result=%+v", m.result)
	}
}

func TestSessionPickerHeaderLoadsGitBranchAsync(t *testing.T) {
	repoRoot := initStatusLineGitRepo(t, "picker-branch")
	m := newUninitializedTestSessionPickerModel(t, nil, sessionPickerHeaderInfo{
		Version: "1.2.3",
		StatusRequest: uiStatusRequest{
			WorkspaceRoot: repoRoot,
			ModelName:     "gpt-5",
			ThinkingLevel: "high",
			Settings:      config.Settings{Model: "gpt-5", ThinkingLevel: "high"},
			AuthStatus:    &staticAuthStatusClient{response: authStatusResponse(serverapi.AuthStatusMethodNone)},
		},
	})
	cmd := collectSessionPickerStatusCmd(m.header)
	if cmd == nil {
		t.Fatal("expected async git branch command")
	}

	next, _ := m.Update(cmd())
	updated := next.(*sessionPickerModel)
	plain := stripANSIAndTrimRight(updated.renderHeader())
	for _, want := range []string{"git picker-branch", "No auth · gpt-5 high"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected async status value %q in header, got %q", want, plain)
		}
	}
}

func TestSessionPickerHeaderInitialAsyncPaintUsesOnlyStaticShell(t *testing.T) {
	repoRoot := initStatusLineGitRepo(t, "picker-branch")
	m := newUninitializedTestSessionPickerModel(t, nil, sessionPickerHeaderInfo{
		Version:       "1.2.3",
		ServerAddress: "127.0.0.1:53082",
		StatusRequest: uiStatusRequest{
			WorkspaceRoot: repoRoot,
			ModelName:     "gpt-5",
			ThinkingLevel: "high",
			Settings:      config.Settings{Model: "gpt-5", ThinkingLevel: "high"},
			AuthStatus:    &staticAuthStatusClient{response: authStatusResponse(serverapi.AuthStatusMethodNone)},
		},
	})
	m.width = 80
	cmd := collectSessionPickerStatusCmd(m.header)
	if cmd == nil {
		t.Fatal("expected async status command")
	}

	before := stripANSIAndTrimRight(m.View())
	for _, unexpected := range []string{"git picker-branch", "No auth", "gpt-5 high", repoRoot} {
		if strings.Contains(before, unexpected) {
			t.Fatalf("did not expect async value %q before status arrives, got %q", unexpected, before)
		}
	}
	if height := lipgloss.Height(m.renderHeader()); height != 4 {
		t.Fatalf("initial header height = %d, want static shell height 4", height)
	}
}

func TestSessionPickerHeaderLoadsRemoteAuthStatus(t *testing.T) {
	m := newTestSessionPickerModel(t, nil, sessionPickerHeaderInfo{
		Version: "1.2.3",
		StatusRequest: uiStatusRequest{
			Settings:   config.Settings{Model: "gpt-5"},
			ModelName:  "gpt-5",
			AuthStatus: &staticAuthStatusClient{response: authStatusResponse(serverapi.AuthStatusMethodOAuth)},
		},
	})
	cmd := collectSessionPickerStatusCmd(m.header)
	if cmd == nil {
		t.Fatal("expected async status command")
	}

	next, _ := m.Update(cmd())
	updated := next.(*sessionPickerModel)
	plain := stripANSIAndTrimRight(updated.renderHeader())
	if !strings.Contains(plain, "OpenAI Subscription") {
		t.Fatalf("expected fast auth display in header, got %q", plain)
	}
}

func TestSessionPickerStatusOmitsAbsentModel(t *testing.T) {
	header := sessionPickerHeaderInfo{
		StatusRequest: uiStatusRequest{
			AuthStatus: &staticAuthStatusClient{response: authStatusResponse(serverapi.AuthStatusMethodOAuth)},
		},
	}
	cmd := collectSessionPickerStatusCmd(header)
	if cmd == nil {
		t.Fatal("expected async status command")
	}
	message, ok := cmd().(sessionPickerStatusMsg)
	if !ok {
		t.Fatalf("status message = %T", message)
	}
	if message.auth == nil {
		t.Fatal("auth status is absent")
	}
	if message.model != nil {
		t.Fatalf("absent model projected as %q", *message.model)
	}
}

func TestSessionPickerHeaderLoadsAuthStatusVariants(t *testing.T) {
	tests := []struct {
		name   string
		method serverapi.AuthStatusMethod
		want   string
	}{
		{name: "no auth", method: serverapi.AuthStatusMethodNone, want: "No auth"},
		{name: "api key", method: serverapi.AuthStatusMethodAPIKey, want: "OpenAI API Key"},
		{name: "oauth", method: serverapi.AuthStatusMethodOAuth, want: "OpenAI Subscription"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestSessionPickerModel(t, nil, sessionPickerHeaderInfo{
				Version: "1.2.3",
				StatusRequest: uiStatusRequest{
					Settings:   config.Settings{Model: "gpt-5"},
					ModelName:  "gpt-5",
					AuthStatus: &staticAuthStatusClient{response: authStatusResponse(tt.method)},
				},
			})
			cmd := collectSessionPickerStatusCmd(m.header)
			if cmd == nil {
				t.Fatal("expected async status command")
			}

			next, _ := m.Update(cmd())
			plain := stripANSIAndTrimRight(next.(*sessionPickerModel).renderHeader())
			if !strings.Contains(plain, tt.want) {
				t.Fatalf("expected %q in header, got %q", tt.want, plain)
			}
		})
	}
}

func TestSessionPickerHeaderReflowsMainInfoWhenNarrow(t *testing.T) {
	m := newTestSessionPickerModel(t, nil, sessionPickerHeaderInfo{
		Version:       "1.2.3",
		CWD:           "~/very/long/repository/path",
		Branch:        "main",
		Model:         "gpt-5.1-ultra high",
		Auth:          "OpenAI API Key",
		ServerAddress: "127.0.0.1:53082",
	})
	m.width = 24

	plain := stripANSIAndTrimRight(m.renderHeader())
	if strings.Contains(plain, "git main · ~/very/long/repository/path") || strings.Contains(plain, "OpenAI API Key · gpt-5.1-ultra high") {
		t.Fatalf("expected narrow header to reflow main info, got %q", plain)
	}
	for _, want := range []string{
		"Kent v1.2.3",
		"git main",
		"…",
		"OpenAI API Key",
		"gpt-5.1-ultra high",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected narrow header to contain %q, got %q", want, plain)
		}
	}
}

func TestSessionPickerHeaderRendersMissingRemoteAddressFallback(t *testing.T) {
	m := newTestSessionPickerModel(t, nil, sessionPickerHeaderInfo{
		Version: "1.2.3",
		CWD:     "~/repo",
		Auth:    "No auth",
		Model:   "gpt-5 high",
	})
	m.width = 80

	plain := stripANSIAndTrimRight(m.renderHeader())
	if !strings.Contains(plain, "Server") {
		t.Fatalf("expected remote server fallback copy, got %q", plain)
	}
}

func TestSessionPickerHeaderTinyWidthKeepsRowsVisible(t *testing.T) {
	m := newTestSessionPickerModel(t, []clientui.SessionSummary{pickerTestSummary(t, "s-1", time.Now().UTC())}, sessionPickerHeaderInfo{
		Version:       "1.2.3",
		CWD:           "~/very/long/path/to/repo",
		Branch:        "feature/very-long-branch",
		Model:         "gpt-5.1-ultra high",
		ServerAddress: "127.0.0.1:53082",
	})
	m.width = 8
	m.height = 8

	if got := m.visibleLineBudget(); got < 1 {
		t.Fatalf("visible line budget = %d, want at least 1", got)
	}
	view := stripANSIAndTrimRight(m.View())
	if !strings.Contains(view, "◈") {
		t.Fatalf("expected at least selected row visible at tiny width, got %q", view)
	}
}
