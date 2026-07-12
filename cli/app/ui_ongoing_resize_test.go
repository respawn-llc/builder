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

func TestWindowResizeKeepsControllerLiveFrameSections(t *testing.T) {
	var raw bytes.Buffer
	nativeSurface := ongoing.NewSurface(&raw)
	surface := &ongoingSurfaceSpy{}
	m := sizedTestUIModel(newProjectedStaticUIModel(
		WithUIOngoingSurface(nativeSurface),
	), 40, 8)
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
