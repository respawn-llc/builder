package app

import (
	"testing"

	"core/cli/tui/ongoing"
	"core/cli/tui/transcriptrender"
)

func TestTerminalCapabilitiesUseIndependentWhitelists(t *testing.T) {
	tests := []struct {
		name        string
		environment map[string]string
		wantResize  ongoing.TerminalResizePolicy
		wantLinks   transcriptrender.MarkdownLinkPresentation
	}{
		{
			name:        "Ghostty and libghostty",
			environment: map[string]string{"TERM_PROGRAM": "ghostty"},
			wantResize:  ongoing.TerminalResizeSemanticPrompt,
			wantLinks:   transcriptrender.MarkdownLinkLabelOnly,
		},
		{
			name:        "kitty window identity",
			environment: map[string]string{"KITTY_WINDOW_ID": "42"},
			wantResize:  ongoing.TerminalResizeSemanticPrompt,
			wantLinks:   transcriptrender.MarkdownLinkLabelOnly,
		},
		{
			name:        "kitty terminal identity",
			environment: map[string]string{"TERM": "xterm-kitty"},
			wantResize:  ongoing.TerminalResizeWidthRehydration,
			wantLinks:   transcriptrender.MarkdownLinkLabelOnly,
		},
		{
			name: "iTerm version is ignored",
			environment: map[string]string{
				"TERM_PROGRAM":         "iTerm.app",
				"TERM_PROGRAM_VERSION": "0.1",
			},
			wantResize: ongoing.TerminalResizeWidthRehydration,
			wantLinks:  transcriptrender.MarkdownLinkLabelOnly,
		},
		{
			name:        "WezTerm",
			environment: map[string]string{"TERM_PROGRAM": "WezTerm"},
			wantResize:  ongoing.TerminalResizeWidthRehydration,
			wantLinks:   transcriptrender.MarkdownLinkLabelOnly,
		},
		{
			name:        "Alacritty",
			environment: map[string]string{"TERM": "alacritty"},
			wantResize:  ongoing.TerminalResizeWidthRehydration,
			wantLinks:   transcriptrender.MarkdownLinkLabelOnly,
		},
		{
			name:        "Windows Terminal",
			environment: map[string]string{"WT_SESSION": "session"},
			wantResize:  ongoing.TerminalResizeWidthRehydration,
			wantLinks:   transcriptrender.MarkdownLinkLabelOnly,
		},
		{
			name:        "VS Code",
			environment: map[string]string{"TERM_PROGRAM": "vscode"},
			wantResize:  ongoing.TerminalResizeWidthRehydration,
			wantLinks:   transcriptrender.MarkdownLinkLabelOnly,
		},
		{
			name:        "Zed",
			environment: map[string]string{"TERM_PROGRAM": "zed"},
			wantResize:  ongoing.TerminalResizeWidthRehydration,
			wantLinks:   transcriptrender.MarkdownLinkLabelOnly,
		},
		{
			name:        "Apple Terminal fallback",
			environment: map[string]string{"TERM_PROGRAM": "Apple_Terminal"},
			wantResize:  ongoing.TerminalResizeWidthRehydration,
			wantLinks:   transcriptrender.MarkdownLinkLabelAndDestination,
		},
		{
			name:        "unknown terminal fallback",
			environment: map[string]string{"TERM_PROGRAM": "unknown-terminal"},
			wantResize:  ongoing.TerminalResizeWidthRehydration,
			wantLinks:   transcriptrender.MarkdownLinkLabelAndDestination,
		},
		{
			name: "tmux masks supported outer terminal",
			environment: map[string]string{
				"TMUX":         "/private/tmp/tmux/default,1,0",
				"TERM_PROGRAM": "ghostty",
			},
			wantResize: ongoing.TerminalResizeWidthRehydration,
			wantLinks:  transcriptrender.MarkdownLinkLabelAndDestination,
		},
		{
			name: "Zellij masks supported outer terminal",
			environment: map[string]string{
				"ZELLIJ":       "0",
				"TERM_PROGRAM": "ghostty",
			},
			wantResize: ongoing.TerminalResizeSemanticPrompt,
			wantLinks:  transcriptrender.MarkdownLinkLabelAndDestination,
		},
		{
			name: "GNU screen masks supported outer terminal",
			environment: map[string]string{
				"STY":          "1234.screen",
				"TERM_PROGRAM": "ghostty",
			},
			wantResize: ongoing.TerminalResizeSemanticPrompt,
			wantLinks:  transcriptrender.MarkdownLinkLabelAndDestination,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			capabilities := resolveTerminalCapabilities(func(name string) (string, bool) {
				value, present := test.environment[name]
				return value, present
			})
			if capabilities.ResizePolicy != test.wantResize {
				t.Fatalf("resize policy = %v, want %v", capabilities.ResizePolicy, test.wantResize)
			}
			if capabilities.MarkdownLinks != test.wantLinks {
				t.Fatalf("Markdown links = %v, want %v", capabilities.MarkdownLinks, test.wantLinks)
			}
		})
	}
}

func TestTerminalHyperlinkWhitelistMatchingIsExact(t *testing.T) {
	capabilities := resolveTerminalCapabilities(func(name string) (string, bool) {
		if name == "TERM_PROGRAM" {
			return " ghostty ", true
		}
		return "", false
	})
	if capabilities.MarkdownLinks != transcriptrender.MarkdownLinkLabelAndDestination {
		t.Fatalf("Markdown links = %v, want exact-match fallback", capabilities.MarkdownLinks)
	}
	if capabilities.ResizePolicy != ongoing.TerminalResizeWidthRehydration {
		t.Fatalf("resize policy = %v, want exact-match fallback", capabilities.ResizePolicy)
	}
}
