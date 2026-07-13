package app

import (
	"os"

	"core/cli/tui/ongoing"
)

func ongoingTerminalResizePolicy() ongoing.TerminalResizePolicy {
	// Base OSC 133 parsing is insufficient: the direct terminal layer must
	// clear redrawable prompts before resize reflow. Only Ghostty and kitty
	// provide that contract; multiplexers and unknown terminals use fallback.
	if tmux, insideTmux := os.LookupEnv("TMUX"); insideTmux && tmux != "" {
		return ongoing.TerminalResizeWidthRehydration
	}
	program, _ := os.LookupEnv("TERM_PROGRAM")
	if program == "ghostty" {
		return ongoing.TerminalResizeSemanticPrompt
	}
	if kittyWindowID, insideKitty := os.LookupEnv("KITTY_WINDOW_ID"); insideKitty && kittyWindowID != "" {
		return ongoing.TerminalResizeSemanticPrompt
	}
	return ongoing.TerminalResizeWidthRehydration
}
