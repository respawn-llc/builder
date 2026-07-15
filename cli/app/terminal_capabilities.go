package app

import (
	"os"

	"core/cli/tui/ongoing"
	"core/cli/tui/transcriptrender"
)

type terminalCapabilities struct {
	ResizePolicy  ongoing.TerminalResizePolicy
	MarkdownLinks transcriptrender.MarkdownLinkPresentation
}

func currentTerminalCapabilities() terminalCapabilities {
	return resolveTerminalCapabilities(os.LookupEnv)
}

func resolveTerminalCapabilities(lookup func(string) (string, bool)) terminalCapabilities {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	capabilities := terminalCapabilities{
		ResizePolicy:  ongoing.TerminalResizeWidthRehydration,
		MarkdownLinks: transcriptrender.MarkdownLinkLabelAndDestination,
	}

	tmux := lookupTerminalEnvironment(lookup, "TMUX")
	termProgram := lookupTerminalEnvironment(lookup, "TERM_PROGRAM")
	kittyWindowID := lookupTerminalEnvironment(lookup, "KITTY_WINDOW_ID")
	if !tmux.NonEmpty() {
		switch {
		case termProgram.Equals("ghostty"), kittyWindowID.NonEmpty():
			capabilities.ResizePolicy = ongoing.TerminalResizeSemanticPrompt
		}
	}

	if tmux.NonEmpty() ||
		lookupTerminalEnvironment(lookup, "ZELLIJ").NonEmpty() ||
		lookupTerminalEnvironment(lookup, "STY").NonEmpty() {
		return capabilities
	}

	if whitelistedTerminalProgram(termProgram) {
		capabilities.MarkdownLinks = transcriptrender.MarkdownLinkLabelOnly
		return capabilities
	}
	term := lookupTerminalEnvironment(lookup, "TERM")
	if kittyWindowID.NonEmpty() || term.Equals("xterm-kitty") {
		capabilities.MarkdownLinks = transcriptrender.MarkdownLinkLabelOnly
		return capabilities
	}
	if term.Equals("alacritty") ||
		lookupTerminalEnvironment(lookup, "WT_SESSION").NonEmpty() {
		capabilities.MarkdownLinks = transcriptrender.MarkdownLinkLabelOnly
	}
	return capabilities
}

type terminalEnvironment struct {
	value   string
	present bool
}

func lookupTerminalEnvironment(
	lookup func(string) (string, bool),
	name string,
) terminalEnvironment {
	value, present := lookup(name)
	return terminalEnvironment{value: value, present: present}
}

func (e terminalEnvironment) NonEmpty() bool {
	return e.present && e.value != ""
}

func (e terminalEnvironment) Equals(value string) bool {
	return e.present && e.value == value
}

func whitelistedTerminalProgram(program terminalEnvironment) bool {
	if !program.present {
		return false
	}
	switch program.value {
	case "ghostty", "iTerm.app", "WezTerm", "Alacritty", "vscode", "zed":
		return true
	default:
		return false
	}
}
