package app

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestOngoingWidthRehydrationDebounceTokenRestarts(t *testing.T) {
	m := newProjectedStaticUIModel()
	if cmd := m.scheduleOngoingWidthRehydration(); cmd == nil {
		t.Fatal("first width rehydration schedule returned nil command")
	}
	first := m.ongoingWidthToken
	if cmd := m.scheduleOngoingWidthRehydration(); cmd == nil {
		t.Fatal("second width rehydration schedule returned nil command")
	}
	if m.ongoingWidthToken == first {
		t.Fatalf("width debounce token did not advance: %d", m.ongoingWidthToken)
	}
}

func TestWindowResizeDoesNotWriteOngoingSurfaceWhileDetailOwnsTerminal(t *testing.T) {
	surface := &ongoingSurfaceSpy{}
	controller := newOngoingTranscriptController(surface, ongoingTestFrameProvider)
	m := newProjectedStaticUIModel(withUIOngoingTranscriptController(controller))
	m.activeSurface = uiSurfaceTranscriptDetail

	result := m.windowReducer().Update(tea.WindowSizeMsg{Width: 100, Height: 40})

	if !result.handled {
		t.Fatal("window resize was not handled")
	}
	if m.termWidth != 100 || m.termHeight != 40 || !m.windowSizeKnown {
		t.Fatalf("geometry = %dx%d known=%v, want stored resize", m.termWidth, m.termHeight, m.windowSizeKnown)
	}
	if len(surface.calls) != 0 {
		t.Fatalf("ongoing surface calls while detail active = %v, want none", surface.calls)
	}
}
