package ongoing

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"core/cli/tui/transcriptrender"
	"core/internal/testharness/pty"
	"core/internal/testharness/pty/analyzer"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/transcript"
)

func TestAssistantFinalizationEqualSourceFlushesUnpromotedTailAndClearsStream(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	streamID := runtimeids.NewAssistantStreamID()
	if _, err := surface.ApplyTerminalMessage(assistantDeltaMessage(streamID, "hello"), FrameInput{Size: Size{Width: 40, Height: 5}}); err != nil {
		t.Fatalf("apply delta: %v", err)
	}
	out.Reset()

	if _, err := surface.ApplyTerminalMessage(committedAssistantMessage(streamID, "hello"), FrameInput{Size: Size{Width: 40, Height: 5}}); err != nil {
		t.Fatalf("finalize equal source: %v", err)
	}

	assertRowStructure(t, immutableAppendedRows(parseTerminalOps(out.String())), []rowKind{
		{separator: true},
		{content: transcriptrender.AssistantSymbol + " hello"},
	})
	if surface.activeAssistant.streamID != nil {
		t.Fatalf("active stream after finalization = %+v, want cleared", surface.activeAssistant)
	}
}

func TestAssistantFinalizationExtensionEmitsOnlyMissingSuffix(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	streamID := runtimeids.NewAssistantStreamID()
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
	streamID := runtimeids.NewAssistantStreamID()
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

	streamID := runtimeids.NewAssistantStreamID()
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
	alphaRow := screenRowIndex(rows, transcriptrender.AssistantSymbol+" alpha")
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
	streamID := runtimeids.NewAssistantStreamID()
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
	if _, err := surface.ApplyTerminalMessage(assistantDeltaMessage(runtimeids.NewAssistantStreamID(), "hello"), FrameInput{Size: Size{Width: 40, Height: 5}}); err != nil {
		t.Fatalf("apply delta: %v", err)
	}

	defer func() {
		if recover() == nil {
			t.Fatal("expected other stream panic")
		}
	}()

	_, _ = surface.ApplyTerminalMessage(committedAssistantMessage(runtimeids.NewAssistantStreamID(), "hello"), FrameInput{Size: Size{Width: 40, Height: 5}})
}

func TestHydrationRestoresActiveAssistantStreamAndFinalizes(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	streamID := runtimeids.NewAssistantStreamID()

	if _, err := surface.ApplyTerminalMessage(clientui.TranscriptMessage{
		Kind: clientui.TranscriptMessageHydration,
		Payload: clientui.TranscriptPayload{Hydration: &clientui.TranscriptHydration{
			ActiveAssistant: &clientui.TranscriptAssistantStream{
				StepID:   assistantFinalizationStepID(),
				StreamID: streamID,
				Text:     "Stable paragraph.\n\nopen tail",
				Phase:    transcript.AssistantPhaseCommentary,
			},
		}},
	}, FrameInput{Size: Size{Width: 40, Height: 5}}); err != nil {
		t.Fatalf("apply hydration: %v", err)
	}

	assertRowStructure(t, immutableAppendedRows(parseTerminalOps(out.String())), []rowKind{
		{separator: true},
		{content: transcriptrender.AssistantSymbol + " Stable paragraph."},
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
	streamID := runtimeids.NewAssistantStreamID()
	if _, err := surface.ApplyTerminalMessage(assistantDeltaMessage(streamID, "volatile tail"), FrameInput{Size: Size{Width: 40, Height: 3}}); err != nil {
		t.Fatalf("apply delta: %v", err)
	}
	if _, err := surface.Render(FrameInput{Size: Size{Width: 40, Height: 3}}); err != nil {
		t.Fatalf("render tail: %v", err)
	}
	out.Reset()

	if _, err := surface.ApplyTerminalMessage(clientui.TranscriptMessage{
		Kind: clientui.TranscriptMessageAssistantStreamAbort,
		Payload: clientui.TranscriptPayload{AssistantStreamAbort: &clientui.TranscriptAssistantStreamAbort{
			StepID:   assistantFinalizationStepID(),
			StreamID: streamID,
			Reason:   clientui.AssistantStreamAbortSuperseded,
		}},
	}, FrameInput{Size: Size{Width: 40, Height: 3}}); err != nil {
		t.Fatalf("abort stream: %v", err)
	}

	assertTerminalPrefix(t, parseTerminalOps(out.String()), []terminalOp{
		{kind: terminalOpCSI, value: "\x1b[r"},
		{kind: terminalOpCSI, value: "\x1b[?6l"},
		{kind: terminalOpOSC, value: "\x1b]133;C\x1b\\"},
		{kind: terminalOpCSI, value: "\x1b[3;1H"},
		{kind: terminalOpOSC, value: "\x1b]133;C\x1b\\"},
		{kind: terminalOpCSI, value: "\x1b[2K"},
	})
}

func TestNoopFinalStreamNeverPromotesIntoImmutableScrollback(t *testing.T) {
	surface := NewSurface(&bytes.Buffer{})
	streamID := runtimeids.NewAssistantStreamID()
	message := assistantDeltaMessage(streamID, "NO_OP\n\n")
	message.Payload.AssistantDelta.Phase = transcript.AssistantPhaseFinal

	if _, err := surface.ApplyTerminalMessage(message, FrameInput{Size: Size{Width: 40, Height: 5}}); err != nil {
		t.Fatalf("apply no-op final delta: %v", err)
	}
	if got := surface.activeAssistant.promotedSourceBoundary; got != 0 {
		t.Fatalf("promoted source boundary = %d, want no immutable no-op promotion", got)
	}
}

func TestNoopFinalSegmentAfterCommentaryNeverPromotesIntoImmutableScrollback(t *testing.T) {
	surface := NewSurface(&bytes.Buffer{})
	streamID := runtimeids.NewAssistantStreamID()
	commentary := assistantDeltaMessage(streamID, "commentary\n\n")
	commentary.Payload.AssistantDelta.Phase = transcript.AssistantPhaseCommentary
	if _, err := surface.ApplyTerminalMessage(commentary, FrameInput{Size: Size{Width: 40, Height: 5}}); err != nil {
		t.Fatalf("apply commentary delta: %v", err)
	}
	promotedBeforeFinal := surface.activeAssistant.promotedSourceBoundary

	final := assistantDeltaMessage(streamID, "NO_OP\n\n")
	final.Payload.AssistantDelta.Phase = transcript.AssistantPhaseFinal
	if _, err := surface.ApplyTerminalMessage(final, FrameInput{Size: Size{Width: 40, Height: 5}}); err != nil {
		t.Fatalf("apply no-op final delta after commentary: %v", err)
	}
	if got := surface.activeAssistant.promotedSourceBoundary; got != promotedBeforeFinal {
		t.Fatalf("promoted source boundary = %d, want unchanged %d", got, promotedBeforeFinal)
	}
}

func assistantDeltaMessage(streamID runtimeids.AssistantStreamID, delta string) clientui.TranscriptMessage {
	return clientui.TranscriptMessage{
		Kind: clientui.TranscriptMessageAssistantDelta,
		Payload: clientui.TranscriptPayload{AssistantDelta: &clientui.TranscriptAssistantDelta{
			StepID:   assistantFinalizationStepID(),
			StreamID: streamID,
			Delta:    delta,
			Phase:    transcript.AssistantPhaseCommentary,
		}},
	}
}

func committedAssistantMessage(streamID runtimeids.AssistantStreamID, text string) clientui.TranscriptMessage {
	return clientui.TranscriptMessage{
		Kind: clientui.TranscriptMessageCommittedRow,
		Payload: clientui.TranscriptPayload{CommittedRow: &clientui.TranscriptCommittedRow{
			Visibility: clientui.EntryVisibilityOngoing,
			Integrity:  transcript.RowIntegrityValid,
			Kind:       clientui.TranscriptRowAssistant,
			Assistant: &clientui.TranscriptAssistantRow{
				StepID:   assistantFinalizationStepID(),
				StreamID: &streamID,
				Text:     text,
				Phase:    transcript.AssistantPhaseFinal,
			},
		}},
	}
}

func nonZeroStreamID(t *testing.T) runtimeids.AssistantStreamID {
	t.Helper()
	return runtimeids.NewAssistantStreamID()
}

func assistantFinalizationStepID() runtimeids.StepID {
	stepID, err := runtimeids.ParseStepID("22222222-2222-4222-8222-222222222222")
	if err != nil {
		panic(err)
	}
	return stepID
}
