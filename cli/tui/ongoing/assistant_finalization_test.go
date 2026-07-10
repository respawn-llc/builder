package ongoing

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"core/internal/testharness/pty"
	"core/internal/testharness/pty/analyzer"
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

	assertRowStructure(t, visibleTextRows(parseTerminalOps(out.String())), []rowKind{
		{divider: true},
		{content: "hello", divider: false},
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

func TestAssistantFinalizationAfterClosedParagraphStreamAppendsSuffix(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	streamID := uuid.New()
	if _, err := surface.ApplyTerminalMessage(assistantDeltaMessage(streamID, "roundtrip commentary\n\n"), FrameInput{Size: Size{Width: 80, Height: 24}}); err != nil {
		t.Fatalf("apply initial delta: %v", err)
	}
	out.Reset()

	if _, err := surface.ApplyTerminalMessage(committedAssistantMessage(streamID, "roundtrip commentary\n\nroundtrip complete"), FrameInput{Size: Size{Width: 80, Height: 24}}); err != nil {
		t.Fatalf("finalize extension: %v", err)
	}

	assertVisibleTextOps(t, parseTerminalOps(out.String()), []string{"roundtrip complete"})
}

func TestAssistantFinalizationDoesNotLeaveBlankRowsFromVolatileTail(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	frame := FrameInput{
		Size: Size{Width: 40, Height: 12},
		Sections: []FrameSection{
			{Kind: FrameSectionInput, Lines: []string{"> prompt"}},
			{Kind: FrameSectionStatus, Lines: []string{"ready"}},
		},
	}
	if _, err := surface.ApplyTerminalMessage(committedMessage(toolRow("previous tool")), frame); err != nil {
		t.Fatalf("append previous tool: %v", err)
	}

	streamID := uuid.New()
	source := "```text\nalpha\nbeta\ngamma"
	if _, err := surface.ApplyTerminalMessage(assistantDeltaMessage(streamID, source), frame); err != nil {
		t.Fatalf("stream multiline volatile tail: %v", err)
	}
	if _, err := surface.ApplyTerminalMessage(committedAssistantMessage(streamID, source), frame); err != nil {
		t.Fatalf("finalize multiline volatile tail: %v", err)
	}

	capture, err := pty.NewCapture(
		pty.MustDimensions(frame.Size.Height, frame.Size.Width),
		[]pty.Chunk{pty.NewChunk(0, time.Millisecond, out.Bytes())},
	)
	if err != nil {
		t.Fatalf("create assistant finalization capture: %v", err)
	}
	analysis, err := analyzer.Analyze(capture)
	if err != nil {
		t.Fatalf("analyze assistant finalization: %v", err)
	}
	rows := strings.Split(analysis.Screen.RenderText(), "\n")
	alphaRow := screenRowIndex(rows, "alpha")
	betaRow := screenRowIndex(rows, "beta")
	gammaRow := screenRowIndex(rows, "gamma")
	if alphaRow < 0 || betaRow < 0 || gammaRow < 0 {
		t.Fatalf("assistant rows missing from terminal screen: %q", rows)
	}
	if betaRow != alphaRow+1 || gammaRow != betaRow+1 {
		t.Fatalf(
			"assistant rows = %d, %d, %d, want adjacent rows; screen=%q",
			alphaRow,
			betaRow,
			gammaRow,
			rows,
		)
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

	assertRowStructure(t, visibleTextRows(parseTerminalOps(out.String())), []rowKind{
		{divider: true},
		{content: "Stable paragraph.", divider: false},
		{content: "open tail", divider: false},
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
		{kind: terminalOpCSI, value: "\x1b[3;1H"},
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
			Visibility: clientui.EntryVisibilityOngoing,
			Kind:       clientui.TranscriptRowAssistant,
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
