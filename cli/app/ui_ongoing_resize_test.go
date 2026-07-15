package app

import (
	"bytes"
	"fmt"
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
	size := m.terminalGeometry.Size()
	if !m.terminalGeometry.IsKnown() || size == nil || size.width != 100 || size.height != 40 {
		t.Fatalf("geometry = %+v, want stored resize", size)
	}
	if len(surface.calls) != 0 {
		t.Fatalf("ongoing surface calls while detail active = %v, want none", surface.calls)
	}
}

func TestWindowResizeWhileDetailOwnsTerminalRepaintsOnReturn(t *testing.T) {
	for _, target := range []ongoing.Size{
		{Width: 100, Height: 24},
		{Width: 80, Height: 30},
	} {
		t.Run(fmt.Sprintf("%dx%d", target.Width, target.Height), func(t *testing.T) {
			var raw bytes.Buffer
			nativeSurface := ongoing.NewSurface(&raw)
			surface := &ongoingSurfaceSpy{}
			m := sizedTestUIModel(newProjectedStaticUIModel(
				WithUIOngoingSurface(nativeSurface),
			), 80, 24)
			controller := newOngoingTranscriptController(surface, m.ongoingFrameInput)
			m.ongoingTranscript = controller
			if _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
				t.Fatalf("accept hydration: %v", err)
			}

			if cmd := m.activateSurface(uiSurfaceTranscriptDetail); cmd == nil {
				t.Fatal("expected detail activation command")
			}
			surface.calls = nil
			raw.Reset()

			result := m.windowReducer().Update(tea.WindowSizeMsg{Width: target.Width, Height: target.Height})
			if !result.handled {
				t.Fatal("window resize was not handled")
			}
			if raw.Len() != 0 {
				t.Fatalf("ongoing surface wrote raw bytes while detail owned terminal: %q", raw.String())
			}
			if !m.pendingOngoingResizeRepaint {
				t.Fatal("off-surface resize did not mark pending ongoing repaint")
			}

			if cmd := m.activateSurface(uiSurfaceOngoingTranscript); cmd == nil {
				t.Fatal("expected ongoing activation command")
			}
			if len(surface.calls) != 0 {
				t.Fatalf("surface calls before ownership restore = %v, want none", surface.calls)
			}

			if _, cmd := m.Update(ongoingNormalBufferOwnedMsg{owned: true}); cmd != nil {
				t.Fatalf("post-exit ownership update returned command, want nil")
			}
			if got, want := surface.callKinds(), []string{"render"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("surface calls after ongoing restore = %v, want %v", got, want)
			}
			frame := surface.calls[len(surface.calls)-1].frame
			if frame.Size != target {
				t.Fatalf("repaint frame size = %+v, want %+v", frame.Size, target)
			}
			if m.pendingOngoingResizeRepaint {
				t.Fatal("pending resize repaint marker was not cleared")
			}
		})
	}
}

func TestAppleTerminalWidthResizeWhileDetailOwnsTerminalRehydratesOnReturn(t *testing.T) {
	var raw bytes.Buffer
	nativeSurface := ongoing.NewSurfaceWithTerminalResizePolicy(
		&raw,
		ongoing.TerminalResizeWidthRehydration,
	)
	if _, err := nativeSurface.ApplyTerminalMessage(
		committedMessageForOngoingResizeTest(),
		ongoing.FrameInput{Size: ongoing.Size{Width: 80, Height: 24}},
	); err != nil {
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
		t.Fatal("off-surface width change did not mark pending width rehydration")
	}

	if cmd := m.activateSurface(uiSurfaceOngoingTranscript); cmd == nil {
		t.Fatal("expected ongoing activation command")
	}
	if _, cmd := m.Update(ongoingNormalBufferOwnedMsg{owned: true}); cmd == nil {
		t.Fatal("ownership restore did not schedule width rehydration")
	}
	if raw.Len() != 0 {
		t.Fatalf("ongoing surface wrote before width debounce elapsed: %q", raw.String())
	}

	token := m.ongoingWidthToken
	if _, cmd := m.Update(ongoingWidthRehydrationDebounceMsg{token: token}); cmd != nil {
		t.Fatalf("debounced width rehydration returned command, want nil")
	}
	if raw.Len() == 0 {
		t.Fatal("width fallback did not reset mutable band")
	}
	if reopenCount != 1 {
		t.Fatalf("reopen count = %d, want 1", reopenCount)
	}
}

func TestWindowResizeKeepsControllerLiveFrameSections(t *testing.T) {
	var raw bytes.Buffer
	nativeSurface := ongoing.NewSurface(&raw)
	surface := &ongoingSurfaceSpy{}
	m := sizedTestUIModel(newProjectedStaticUIModel(
		WithUIOngoingSurface(nativeSurface),
	), 40, 10)
	m.ongoingTranscript = newOngoingTranscriptController(surface, m.ongoingFrameInput)
	if _, err := m.ongoingTranscript.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}
	if _, err := m.ongoingTranscript.Accept(clientui.TranscriptMessage{
		Sequence: 2,
		Kind:     clientui.TranscriptMessagePendingSessionPrompt,
		PendingSessionPrompt: &clientui.TranscriptPendingSessionPrompt{
			ID:    "ask-1",
			State: clientui.TranscriptPromptPending,
			Data:  clientui.TranscriptPendingSessionPromptData{Question: "Approve command?"},
		},
	}); err != nil {
		t.Fatalf("accept pending prompt: %v", err)
	}
	surface.calls = nil

	result := m.windowReducer().Update(tea.WindowSizeMsg{Width: 40, Height: 10})

	if !result.handled {
		t.Fatal("window resize was not handled")
	}
	if got, want := surface.callKinds(), []string{"resize"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("surface calls = %v, want %v", got, want)
	}
	if got, want := surface.lastFrameSectionLines(ongoing.FrameSectionPendingPrompt), []string{"Approve command?"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("prompt section lines = %v, want %v", got, want)
	}
}

func committedMessageForOngoingResizeTest() clientui.TranscriptMessage {
	return clientui.TranscriptMessage{
		Kind: clientui.TranscriptMessageCommittedRow,
		CommittedRow: &clientui.TranscriptCommittedRow{
			Visibility: clientui.EntryVisibilityOngoing,
			Kind:       clientui.TranscriptRowUser,
			User:       &clientui.TranscriptUserRow{Text: "committed"},
		},
	}
}
