package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"core/cli/app/internal/authui"

	tea "github.com/charmbracelet/bubbletea"
	ansi "github.com/charmbracelet/x/ansi"
)

func TestAuthCallbackPageInvalidPasteShowsTransientErrorAndStaysOpen(t *testing.T) {
	m := newAuthCallbackPageModel(authCallbackPageData{Theme: "dark"})
	m.complete = func(context.Context, string) (authui.AuthMethod, error) {
		return authui.AuthMethod{}, errors.New("oauth callback is missing code")
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("bad")})
	m = next.(*authCallbackPageModel)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*authCallbackPageModel)
	if cmd == nil {
		t.Fatal("expected completion command")
	}
	msg := cmd()
	next, _ = m.Update(msg)
	m = next.(*authCallbackPageModel)
	if m.result.Method.Type != "" {
		t.Fatalf("expected invalid paste to stay on page, result=%+v", m.result)
	}
	if !strings.Contains(ansi.Strip(m.View()), "Invalid callback: oauth callback is missing code") {
		t.Fatalf("expected transient error in view, got %q", ansi.Strip(m.View()))
	}
}

func TestAuthCallbackPageBrowserWaitErrorShowsErrorAndStaysOpen(t *testing.T) {
	m := newAuthCallbackPageModel(authCallbackPageData{Theme: "dark"})
	next, cmd := m.Update(authCallbackPageBrowserDoneMsg{err: errors.New("listener timed out")})
	m = next.(*authCallbackPageModel)
	if cmd == nil {
		t.Fatal("expected transient error command")
	}
	if m.result.Err != nil || m.result.Canceled {
		t.Fatalf("expected browser wait failure to keep page open, result=%+v", m.result)
	}
	if !strings.Contains(ansi.Strip(m.View()), "Browser callback failed: listener timed out. Paste the callback URL or code.") {
		t.Fatalf("expected transient wait error in view, got %q", ansi.Strip(m.View()))
	}
}

func TestAuthCallbackPageEscCancels(t *testing.T) {
	m := newAuthCallbackPageModel(authCallbackPageData{Theme: "dark"})
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(*authCallbackPageModel)
	if !m.result.Canceled {
		t.Fatalf("expected Esc to cancel, got %+v", m.result)
	}
}
