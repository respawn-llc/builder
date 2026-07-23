package app

import (
	"strings"
	"testing"
)

func TestUIRenderFrameRenderRespectsPaddingPolicy(t *testing.T) {
	frame := uiRenderFrame{
		width:      6,
		height:     4,
		chatPanel:  []string{"chat"},
		statusLine: "status",
	}

	withoutPadding := strings.Split(strings.TrimSuffix(frame.renderWithCursorVisibility(true), ansiHideCursor), "\n")
	if len(withoutPadding) != 2 {
		t.Fatalf("expected compact frame without padding, got %d lines", len(withoutPadding))
	}

	frame.padToHeight = true
	withPadding := strings.Split(strings.TrimSuffix(frame.renderWithCursorVisibility(true), ansiHideCursor), "\n")
	if len(withPadding) != 4 {
		t.Fatalf("expected padded frame to fill height, got %d lines", len(withPadding))
	}
}
