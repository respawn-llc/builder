package ongoing

import (
	"bytes"
	"testing"

	"core/shared/clientui"
	"github.com/google/uuid"
)

func TestAssistantFinalizationEqualSourceFlushesUnpromotedTailAndClearsStream(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	streamID := uuid.New()
	if _, err := surface.ApplyTerminalMessage(assistantDeltaMessage(streamID, "hello"), FrameInput{Size: Size{Width: 40, Height: 5}}); err != nil {
		t.Fatalf("apply delta: %v", err)
	}
	out.Reset()

	if _, err := surface.ApplyTerminalMessage(committedAssistantMessage(streamID, "hello"), FrameInput{Size: Size{Width: 40, Height: 5}}); err != nil {
		t.Fatalf("finalize equal source: %v", err)
	}

	assertVisibleTextOps(t, parseTerminalOps(out.String()), []string{
		"────────────── assistant ───────────────",
		"hello",
	})
	if surface.activeAssistant.streamID != nil {
		t.Fatalf("active stream after finalization = %+v, want cleared", surface.activeAssistant)
	}
}

func TestAssistantFinalizationExtensionEmitsOnlyMissingSuffix(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	streamID := uuid.New()
	if _, err := surface.ApplyTerminalMessage(assistantDeltaMessage(streamID, "hello\n\n"), FrameInput{Size: Size{Width: 40, Height: 5}}); err != nil {
		t.Fatalf("apply initial delta: %v", err)
	}
	out.Reset()

	if _, err := surface.ApplyTerminalMessage(committedAssistantMessage(streamID, "hello\n\nmore\n\n"), FrameInput{Size: Size{Width: 40, Height: 5}}); err != nil {
		t.Fatalf("finalize extension: %v", err)
	}

	assertVisibleTextOps(t, parseTerminalOps(out.String()), []string{"more"})
}

func TestAssistantFinalizationMismatchPanics(t *testing.T) {
	surface := NewSurface(&bytes.Buffer{})
	streamID := uuid.New()
	if _, err := surface.ApplyTerminalMessage(assistantDeltaMessage(streamID, "hello"), FrameInput{Size: Size{Width: 40, Height: 5}}); err != nil {
		t.Fatalf("apply delta: %v", err)
	}

	defer func() {
		if recover() == nil {
			t.Fatal("expected mismatch panic")
		}
	}()

	_, _ = surface.ApplyTerminalMessage(committedAssistantMessage(streamID, "goodbye"), FrameInput{Size: Size{Width: 40, Height: 5}})
}

func TestAssistantFinalizationOtherStreamPanics(t *testing.T) {
	surface := NewSurface(&bytes.Buffer{})
	if _, err := surface.ApplyTerminalMessage(assistantDeltaMessage(uuid.New(), "hello"), FrameInput{Size: Size{Width: 40, Height: 5}}); err != nil {
		t.Fatalf("apply delta: %v", err)
	}

	defer func() {
		if recover() == nil {
			t.Fatal("expected other stream panic")
		}
	}()

	_, _ = surface.ApplyTerminalMessage(committedAssistantMessage(uuid.New(), "hello"), FrameInput{Size: Size{Width: 40, Height: 5}})
}

func TestHydrationRestoresActiveAssistantStreamAndFinalizes(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	streamID := uuid.New()

	if _, err := surface.ApplyTerminalMessage(clientui.TranscriptMessage{
		Kind: clientui.TranscriptMessageHydration,
		Hydration: &clientui.TranscriptHydration{
			ActiveAssistantStream: &clientui.TranscriptAssistantStream{
				StreamID: streamID,
				Text:     "Stable paragraph.\n\nopen tail",
			},
		},
	}, FrameInput{Size: Size{Width: 40, Height: 5}}); err != nil {
		t.Fatalf("apply hydration: %v", err)
	}

	assertVisibleTextOps(t, parseTerminalOps(out.String()), []string{
		"────────────── assistant ───────────────",
		"Stable paragraph.",
		"open tail",
	})
	out.Reset()

	if _, err := surface.ApplyTerminalMessage(committedAssistantMessage(streamID, "Stable paragraph.\n\nopen tail done"), FrameInput{Size: Size{Width: 40, Height: 5}}); err != nil {
		t.Fatalf("finalize hydrated stream: %v", err)
	}

	assertVisibleTextOps(t, parseTerminalOps(out.String()), []string{"open tail done"})
	if surface.activeAssistant.streamID != nil {
		t.Fatalf("active stream after hydrated finalization = %+v, want cleared", surface.activeAssistant)
	}
}

func TestAssistantAbortClearsVolatileTailWithoutImmutableAppend(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	streamID := uuid.New()
	if _, err := surface.ApplyTerminalMessage(assistantDeltaMessage(streamID, "volatile tail"), FrameInput{Size: Size{Width: 40, Height: 3}}); err != nil {
		t.Fatalf("apply delta: %v", err)
	}
	if _, err := surface.Render(FrameInput{Size: Size{Width: 40, Height: 3}}); err != nil {
		t.Fatalf("render tail: %v", err)
	}
	out.Reset()

	if _, err := surface.ApplyTerminalMessage(clientui.TranscriptMessage{
		Kind: clientui.TranscriptMessageAssistantStreamAbort,
		AssistantStreamAbort: &clientui.TranscriptAssistantStreamAbort{
			StreamID: streamID,
		},
	}, FrameInput{Size: Size{Width: 40, Height: 3}}); err != nil {
		t.Fatalf("abort stream: %v", err)
	}

	assertTerminalPrefix(t, parseTerminalOps(out.String()), []terminalOp{
		{kind: terminalOpCSI, value: "\x1b[r"},
		{kind: terminalOpCSI, value: "\x1b[?6l"},
		{kind: terminalOpCSI, value: "\x1b[2;1H"},
		{kind: terminalOpCSI, value: "\x1b[2K"},
	})
}

func assistantDeltaMessage(streamID uuid.UUID, delta string) clientui.TranscriptMessage {
	return clientui.TranscriptMessage{
		Kind: clientui.TranscriptMessageAssistantDelta,
		AssistantDelta: &clientui.TranscriptAssistantDelta{
			StreamID: streamID,
			Delta:    delta,
		},
	}
}

func committedAssistantMessage(streamID uuid.UUID, text string) clientui.TranscriptMessage {
	return clientui.TranscriptMessage{
		Kind: clientui.TranscriptMessageCommittedRow,
		CommittedRow: &clientui.TranscriptCommittedRow{
			Kind: clientui.TranscriptRowAssistant,
			Assistant: &clientui.TranscriptAssistantRow{
				StreamID: &streamID,
				Text:     text,
			},
		},
	}
}

func nonZeroStreamID(t *testing.T) uuid.UUID {
	t.Helper()
	return uuid.New()
}
