package app

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"core/cli/app/internal/status"
	"core/server/auth"
	"core/shared/client"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type panicAuthStatusClient struct{}

func (panicAuthStatusClient) GetAuthStatus(context.Context, serverapi.AuthStatusRequest) (serverapi.AuthStatusResponse, error) {
	panic("session picker must not call slow auth status")
}

type fastOnlyAuthResolver struct {
	state auth.State
}

func (r fastOnlyAuthResolver) Load(context.Context) (auth.State, error) {
	return r.state, nil
}

func (fastOnlyAuthResolver) CurrentState(context.Context) (auth.State, error) {
	panic("session picker must not resolve current auth state")
}

var _ client.AuthStatusClient = panicAuthStatusClient{}

func TestSessionPickerScrollsAndSelects(t *testing.T) {
	now := time.Date(2026, time.February, 8, 12, 0, 0, 0, time.UTC)
	summaries := make([]clientui.SessionSummary, 0, 20)
	for i := 0; i < 20; i++ {
		summaries = append(summaries, clientui.SessionSummary{
			SessionID: fmt.Sprintf("s-%02d", i),
			UpdatedAt: now.Add(-time.Duration(i) * time.Minute),
		})
	}

	m := newSessionPickerModel(summaries, "dark", sessionPickerHeaderInfo{})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 8})
	m = next.(*sessionPickerModel)
	for i := 0; i < 16; i++ {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = next.(*sessionPickerModel)
	}

	if m.cursor != 16 {
		t.Fatalf("cursor=%d want 16", m.cursor)
	}
	if m.offset == 0 {
		t.Fatalf("offset should advance for scroll")
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*sessionPickerModel)
	if m.result.Session == nil {
		t.Fatal("expected selected session")
	}
	if m.result.Session.SessionID != summaries[15].SessionID {
		t.Fatalf("selected=%s want %s", m.result.Session.SessionID, summaries[15].SessionID)
	}
}

func TestSessionPickerEnterDefaultsToCreateNew(t *testing.T) {
	m := newSessionPickerModel(nil, "dark", sessionPickerHeaderInfo{})
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*sessionPickerModel)
	if !m.result.CreateNew {
		t.Fatal("expected default selection to create a new session")
	}
}

func TestSessionPickerNewHotkeyAndExitKeys(t *testing.T) {
	m := newSessionPickerModel(nil, "dark", sessionPickerHeaderInfo{})

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = next.(*sessionPickerModel)
	if !m.result.CreateNew {
		t.Fatal("expected create-new result")
	}

	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEsc},
		{Type: tea.KeyRunes, Runes: []rune{'q'}},
	} {
		m = newSessionPickerModel([]clientui.SessionSummary{{SessionID: "s-1"}}, "dark", sessionPickerHeaderInfo{})
		next, cmd := m.Update(key)
		m = next.(*sessionPickerModel)
		if cmd != nil {
			t.Fatalf("%v unexpectedly ended the session picker", key)
		}
		if m.cursor != 0 || m.result.CreateNew || m.result.Canceled || m.result.Session != nil {
			t.Fatalf("%v changed picker state: cursor=%d result=%+v", key, m.cursor, m.result)
		}
	}

	m = newSessionPickerModel(nil, "dark", sessionPickerHeaderInfo{})
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = next.(*sessionPickerModel)
	if cmd == nil {
		t.Fatal("expected Ctrl+C to exit the session picker")
	}
	if !m.result.Canceled {
		t.Fatal("expected Ctrl+C cancellation result")
	}
}

func TestSessionPickerIgnoresMouseSGRRunes(t *testing.T) {
	m := newSessionPickerModel(nil, "dark", sessionPickerHeaderInfo{})
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[<64;81;40M[<65;80;39M")})
	m = next.(*sessionPickerModel)
	if m.result.CreateNew || m.result.Canceled || m.result.Session != nil {
		t.Fatalf("expected mouse sgr runes ignored, got result=%+v", m.result)
	}
}

func TestSessionPickerHeaderLoadsGitBranchAsync(t *testing.T) {
	repoRoot := initStatusLineGitRepo(t, "picker-branch")
	m := newSessionPickerModel(nil, "dark", sessionPickerHeaderInfo{
		Version: "1.2.3",
		StatusRequest: uiStatusRequest{
			WorkspaceRoot: repoRoot,
			ModelName:     "gpt-5",
			ThinkingLevel: "high",
			Settings:      config.Settings{Model: "gpt-5", ThinkingLevel: "high"},
		},
	})
	cmd := m.Init()
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
	m := newSessionPickerModel(nil, "dark", sessionPickerHeaderInfo{
		Version:       "1.2.3",
		OwnsServer:    true,
		ServerAddress: "127.0.0.1:53082",
		StatusRequest: uiStatusRequest{
			WorkspaceRoot: repoRoot,
			ModelName:     "gpt-5",
			ThinkingLevel: "high",
			Settings:      config.Settings{Model: "gpt-5", ThinkingLevel: "high"},
		},
	})
	m.width = 80
	cmd := m.Init()
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

func TestSessionPickerHeaderLoadsFastAuthStateOnly(t *testing.T) {
	m := newSessionPickerModel(nil, "dark", sessionPickerHeaderInfo{
		Version: "1.2.3",
		StatusRequest: uiStatusRequest{
			Settings:   config.Settings{Model: "gpt-5"},
			ModelName:  "gpt-5",
			AuthStatus: panicAuthStatusClient{},
		},
		AuthManager: fastOnlyAuthResolver{state: auth.State{
			Method: auth.Method{Type: auth.MethodOAuth, OAuth: &auth.OAuthMethod{Email: "user@example.com"}},
		}},
	})
	cmd := m.Init()
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

func TestSessionPickerHeaderLoadsFastAuthStateVariants(t *testing.T) {
	tests := []struct {
		name  string
		state auth.State
		want  string
	}{
		{name: "no auth", state: auth.EmptyState(), want: "No auth"},
		{name: "api key", state: auth.State{Method: auth.Method{Type: auth.MethodAPIKey, APIKey: &auth.APIKeyMethod{Key: "sk-test-1234"}}}, want: "OpenAI API Key"},
		{name: "oauth", state: auth.State{Method: auth.Method{Type: auth.MethodOAuth, OAuth: &auth.OAuthMethod{Email: "user@example.com"}}}, want: "OpenAI Subscription"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newSessionPickerModel(nil, "dark", sessionPickerHeaderInfo{
				Version: "1.2.3",
				StatusRequest: uiStatusRequest{
					Settings:   config.Settings{Model: "gpt-5"},
					ModelName:  "gpt-5",
					AuthStatus: panicAuthStatusClient{},
				},
				AuthManager: fastOnlyAuthResolver{state: tt.state},
			})
			cmd := m.Init()
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
	m := newSessionPickerModel(nil, "dark", sessionPickerHeaderInfo{
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
	m := newSessionPickerModel(nil, "dark", sessionPickerHeaderInfo{
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

func TestSessionPickerAuthLabelExamples(t *testing.T) {
	tests := []struct {
		name string
		info uiStatusAuthInfo
		want string
	}{
		{name: "no auth", info: uiStatusAuthInfo{Summary: "No Auth", Visible: true, Method: auth.MethodNone}, want: "No auth"},
		{name: "api key", info: uiStatusAuthInfo{Summary: "API Key ...1234", Visible: true, Method: auth.MethodAPIKey, Provider: "openai"}, want: "OpenAI API Key"},
		{name: "subscription", info: uiStatusAuthInfo{Summary: "user@example.com", Visible: true, Method: auth.MethodOAuth, Provider: "chatgpt-codex"}, want: "OpenAI Subscription"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := status.AuthDisplayLabel(tt.info); got != tt.want {
				t.Fatalf("auth label = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSessionPickerHeaderTinyWidthKeepsRowsVisible(t *testing.T) {
	m := newSessionPickerModel([]clientui.SessionSummary{{SessionID: "s-1", UpdatedAt: time.Now()}}, "dark", sessionPickerHeaderInfo{
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
