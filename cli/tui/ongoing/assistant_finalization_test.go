package ongoing

import (
	"bytes"
	"strings"
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

	if got := out.String(); !strings.Contains(got, "hello") {
		t.Fatalf("finalization wrote %q, want flushed final tail", got)
	}
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

	if got := out.String(); !strings.Contains(got, "more") {
		t.Fatalf("extension finalization bytes = %q, want missing suffix", got)
	}
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

	if got := out.String(); !strings.Contains(got, "\x1b[3;1H\x1b[2K") {
		t.Fatalf("abort repaint bytes = %q, want mutable erase", got)
	}
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
