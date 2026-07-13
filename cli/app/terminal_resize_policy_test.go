package app

import (
	"testing"

	"core/cli/tui/ongoing"
)

func TestAppleTerminalSelectsWidthRehydrationFallback(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("KITTY_WINDOW_ID", "")
	t.Setenv("TERM_PROGRAM", "Apple_Terminal")

	if got := ongoingTerminalResizePolicy(); got != ongoing.TerminalResizeWidthRehydration {
		t.Fatalf("resize policy = %v, want Apple Terminal fallback", got)
	}
}

func TestGhosttySelectsSemanticPromptRepaint(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("KITTY_WINDOW_ID", "")
	t.Setenv("TERM_PROGRAM", "ghostty")

	if got := ongoingTerminalResizePolicy(); got != ongoing.TerminalResizeSemanticPrompt {
		t.Fatalf("resize policy = %v, want Ghostty semantic prompt repaint", got)
	}
}

func TestKittySelectsSemanticPromptRepaint(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("KITTY_WINDOW_ID", "42")
	t.Setenv("TERM_PROGRAM", "")

	if got := ongoingTerminalResizePolicy(); got != ongoing.TerminalResizeSemanticPrompt {
		t.Fatalf("resize policy = %v, want kitty semantic prompt repaint", got)
	}
}

func TestUnknownTerminalSelectsWidthRehydrationFallback(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("KITTY_WINDOW_ID", "")
	t.Setenv("TERM_PROGRAM", "unknown-terminal")

	if got := ongoingTerminalResizePolicy(); got != ongoing.TerminalResizeWidthRehydration {
		t.Fatalf("resize policy = %v, want unknown-terminal fallback", got)
	}
}

func TestTerminalProgramAllowlistMatchingIsExact(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("KITTY_WINDOW_ID", "")
	t.Setenv("TERM_PROGRAM", "Apple_Terminal_preview")

	if got := ongoingTerminalResizePolicy(); got != ongoing.TerminalResizeWidthRehydration {
		t.Fatalf("resize policy = %v, want exact-match fallback", got)
	}
}

func TestTmuxSelectsWidthRehydrationDespiteOuterGhostty(t *testing.T) {
	t.Setenv("TMUX", "/private/tmp/tmux/default,1,0")
	t.Setenv("KITTY_WINDOW_ID", "")
	t.Setenv("TERM_PROGRAM", "ghostty")

	if got := ongoingTerminalResizePolicy(); got != ongoing.TerminalResizeWidthRehydration {
		t.Fatalf("resize policy = %v, want tmux fallback", got)
	}
}
