package app

import (
	"bytes"
	"reflect"
	"testing"

	"core/cli/tui/ongoing"
	"core/shared/clientui"

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

func TestWindowResizeWhileDetailOwnsTerminalRequestsWidthRehydrationOnReturn(t *testing.T) {
	var raw bytes.Buffer
	nativeSurface := ongoing.NewSurface(&raw)
	if _, err := nativeSurface.ApplyTerminalMessage(committedMessageForOngoingResizeTest(), ongoing.FrameInput{
		Size: ongoing.Size{Width: 80, Height: 24},
	}); err != nil {
		t.Fatalf("prime immutable ongoing scrollback: %v", err)
	}
	raw.Reset()
	surface := &ongoingSurfaceSpy{}
	controller := newOngoingTranscriptController(surface, ongoingTestFrameProvider)
	if _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}
	reopenCount := 0
	m := newProjectedStaticUIModel(
		WithUIOngoingSurface(nativeSurface),
		withUIOngoingTranscriptController(controller),
		WithUIOngoingTranscriptReopen(func() { reopenCount++ }),
	)

	if cmd := m.activateSurface(uiSurfaceTranscriptDetail); cmd == nil {
		t.Fatal("expected detail activation command")
	}
	result := m.windowReducer().Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	if !result.handled {
		t.Fatal("window resize was not handled")
	}
	if raw.Len() != 0 {
		t.Fatalf("ongoing surface wrote raw bytes while detail owned terminal: %q", raw.String())
	}
	if !m.pendingOngoingWidthReset {
		t.Fatal("off-surface width change did not mark pending ongoing width rehydration")
	}

	if cmd := m.activateSurface(uiSurfaceOngoingTranscript); cmd == nil {
		t.Fatal("expected ongoing activation command")
	}
	if raw.Len() != 0 {
		t.Fatalf("ongoing surface wrote raw bytes before normal buffer ownership restored: %q", raw.String())
	}
	if _, cmd := m.Update(ongoingNormalBufferOwnedMsg{owned: true}); cmd == nil {
		t.Fatal("post-exit ownership update did not schedule width debounce")
	}
	if raw.Len() != 0 {
		t.Fatalf("ongoing surface wrote raw bytes before width debounce elapsed: %q", raw.String())
	}
	if m.pendingOngoingWidthReset {
		t.Fatal("pending width rehydration marker was not cleared")
	}
	if got, want := surface.callKinds(), []string{"apply"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("transcript controller calls = %v, want %v", got, want)
	}

	token := m.ongoingWidthToken
	if _, cmd := m.Update(ongoingWidthRehydrationDebounceMsg{token: token}); cmd != nil {
		t.Fatalf("debounced width rehydration returned command, want nil")
	}
	if raw.Len() == 0 {
		t.Fatal("expected width scratch reset after debounce")
	}
	if reopenCount != 1 {
		t.Fatalf("reopen count = %d, want 1", reopenCount)
	}
}

func committedMessageForOngoingResizeTest() clientui.TranscriptMessage {
	return clientui.TranscriptMessage{
		Kind: clientui.TranscriptMessageCommittedRow,
		CommittedRow: &clientui.TranscriptCommittedRow{
			Kind: clientui.TranscriptRowUser,
			User: &clientui.TranscriptUserRow{Text: "committed"},
		},
	}
}
