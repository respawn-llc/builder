package ongoing

import (
	"bytes"
	"strings"
	"testing"

	"core/cli/tui/transcriptrender"
	"core/internal/testharness/pty"
	"core/internal/testharness/pty/analyzer"
	"core/shared/clientui"
	"core/shared/transcript"
)

func TestSurfaceVerticalShrinkMovesVisibleCommittedRowsIntoNativeScrollback(t *testing.T) {
	var output bytes.Buffer
	surface := NewSurface(&output)
	initial := FrameInput{
		Size: Size{Width: 80, Height: 18},
		Sections: []FrameSection{{
			Kind:  FrameSectionStatus,
			Lines: []string{"LIVE"},
		}},
	}
	stream, err := analyzer.NewStream(pty.MustDimensions(18, 80))
	if err != nil {
		t.Fatalf("new terminal stream: %v", err)
	}
	feedFrame := func(operation string) {
		t.Helper()
		if err := stream.Feed(output.Bytes()); err != nil {
			t.Fatalf("%s terminal bytes: %v", operation, err)
		}
		output.Reset()
	}

	if _, err := surface.ApplyTerminalMessage(committedAssistantMessage("IMMUTABLE_01"), initial); err != nil {
		t.Fatalf("append committed transcript: %v", err)
	}
	feedFrame("append committed transcript")
	if _, err := surface.writeFrameTransaction(initial, []string{
		"IMMUTABLE_02",
		"IMMUTABLE_03",
		"IMMUTABLE_04",
		"IMMUTABLE_05",
		"IMMUTABLE_06",
		"IMMUTABLE_07",
		"IMMUTABLE_08",
		"IMMUTABLE_09",
		"IMMUTABLE_10",
		"IMMUTABLE_11",
		"IMMUTABLE_12",
	}); err != nil {
		t.Fatalf("append remaining committed transcript: %v", err)
	}
	feedFrame("append remaining committed transcript")
	beforeShrink, err := stream.ScreenSnapshot()
	if err != nil {
		t.Fatalf("snapshot before shrink: %v", err)
	}
	if err := stream.Resize(pty.MustDimensions(12, 80)); err != nil {
		t.Fatalf("shrink terminal: %v", err)
	}

	shrunk := initial
	shrunk.Size.Height = 12
	if _, err := surface.Resize(shrunk.Size, shrunk); err != nil {
		t.Fatalf("repaint after shrink: %v", err)
	}
	if resizeOutput := output.String(); strings.Contains(resizeOutput, "IMMUTABLE_") {
		t.Fatalf("vertical shrink re-emitted committed transcript: %q", resizeOutput)
	}
	feedFrame("repaint after shrink")
	snapshot, err := stream.ScreenSnapshot()
	if err != nil {
		t.Fatalf("snapshot after shrink: %v", err)
	}

	if !screenContains(snapshot, "IMMUTABLE_07") {
		t.Fatalf(
			"vertical shrink erased committed transcript still visible after terminal resize: before=%q after=%q",
			beforeShrink.RenderText(),
			snapshot.RenderText(),
		)
	}
	if got := screenRow(snapshot, 11); got != "LIVE" {
		t.Fatalf("live band bottom row = %q, want LIVE", got)
	}
}

func TestSurfaceVerticalExpansionMarksOnlyTheLiveBandAsRedrawable(t *testing.T) {
	var output bytes.Buffer
	surface := NewSurface(&output)
	initial := FrameInput{
		Size: Size{Width: 80, Height: 18},
		Sections: []FrameSection{{
			Kind:  FrameSectionStatus,
			Lines: []string{"LIVE"},
		}},
	}
	if _, err := surface.ApplyTerminalMessage(committedAssistantMessage("IMMUTABLE"), initial); err != nil {
		t.Fatalf("append committed transcript: %v", err)
	}
	output.Reset()

	expanded := initial
	expanded.Size.Height = 28
	if _, err := surface.Resize(expanded.Size, expanded); err != nil {
		t.Fatalf("repaint after expansion: %v", err)
	}

	want := "\x1b[28;1H" + redrawableSemanticPromptSequence()
	if !strings.Contains(output.String(), want) {
		t.Fatalf(
			"expanded live band was not marked at its bottom row: output=%q want_sequence=%q",
			output.String(),
			want,
		)
	}
}

func TestLegacyResizeDoesNotMarkMutableBandAsRedrawable(t *testing.T) {
	var output bytes.Buffer
	surface := NewSurfaceWithOptions(&output, SurfaceOptions{
		TerminalResize: TerminalResizeTmuxWidthRehydration,
		MarkdownLinks:  transcriptrender.MarkdownLinkLabelOnly,
	})
	initial := FrameInput{
		Size: Size{Width: 80, Height: 18},
		Sections: []FrameSection{{
			Kind:  FrameSectionStatus,
			Lines: []string{"OLD_LIVE_TOP", "OLD_LIVE_BOTTOM"},
		}},
	}
	if _, err := surface.ApplyTerminalMessage(committedAssistantMessage("IMMUTABLE"), initial); err != nil {
		t.Fatalf("render legacy transcript and live band: %v", err)
	}
	output.Reset()
	expanded := FrameInput{
		Size: Size{Width: 80, Height: 28},
		Sections: []FrameSection{{
			Kind:  FrameSectionStatus,
			Lines: []string{"NEW_LIVE_TOP", "NEW_LIVE_BOTTOM"},
		}},
	}
	if _, err := surface.Resize(expanded.Size, expanded); err != nil {
		t.Fatalf("resize legacy live band: %v", err)
	}

	resizeOutput := output.String()
	if strings.Contains(resizeOutput, redrawableSemanticPromptSequence()) {
		t.Fatalf("legacy resize emitted redrawable semantic prompt: %q", resizeOutput)
	}
	if strings.Contains(resizeOutput, "OLD_LIVE") {
		t.Fatalf("legacy resize replayed stale mutable content: %q", resizeOutput)
	}
	if !strings.Contains(resizeOutput, "NEW_LIVE_TOP") || !strings.Contains(resizeOutput, "NEW_LIVE_BOTTOM") {
		t.Fatalf("legacy resize did not repaint current mutable content: %q", resizeOutput)
	}
	if wantScroll := "\x1b[1;26r\x1b[26;1H"; !strings.Contains(resizeOutput, wantScroll) {
		t.Fatalf(
			"legacy expansion did not move immutable region into native scrollback: output=%q want_sequence=%q",
			resizeOutput,
			wantScroll,
		)
	}
}

func TestScratchHydrationResetErasesExpandedBottomBand(t *testing.T) {
	var output bytes.Buffer
	surface := NewSurfaceWithOptions(&output, SurfaceOptions{
		TerminalResize: TerminalResizeWidthRehydration,
		MarkdownLinks:  transcriptrender.MarkdownLinkLabelOnly,
	})
	initial := FrameInput{
		Size: Size{Width: 80, Height: 18},
		Sections: []FrameSection{{
			Kind:  FrameSectionStatus,
			Lines: []string{"LIVE_TOP", "LIVE_BOTTOM"},
		}},
	}
	if _, err := surface.ApplyTerminalMessage(committedAssistantMessage("IMMUTABLE"), initial); err != nil {
		t.Fatalf("render legacy transcript and live band: %v", err)
	}
	output.Reset()

	expanded := initial
	expanded.Size.Height = 28
	if _, err := surface.ResetForScratchHydration(RehydrateReasonWidthChange, expanded); err != nil {
		t.Fatalf("reset after expansion: %v", err)
	}

	resetOutput := output.String()
	if want := "\x1b[27;1H" + semanticOutputSequence() + "\x1b[2K"; !strings.Contains(resetOutput, want) {
		t.Fatalf("scratch reset did not erase expanded bottom band: output=%q want_sequence=%q", resetOutput, want)
	}
	if strings.Contains(resetOutput, "\x1b[17;1H"+semanticOutputSequence()+"\x1b[2K") {
		t.Fatalf("scratch reset erased expanded immutable rows: %q", resetOutput)
	}
}

func committedAssistantMessage(text string) clientui.TranscriptMessage {
	return clientui.TranscriptMessage{
		Kind: clientui.TranscriptMessageCommittedRow,
		Payload: clientui.TranscriptPayload{
			CommittedRow: &clientui.TranscriptCommittedRow{
				Visibility: transcript.EntryVisibilityOngoing,
				Integrity:  transcript.RowIntegrityValid,
				Kind:       clientui.TranscriptRowAssistant,
				Assistant: &clientui.TranscriptAssistantRow{
					Text:  text,
					Phase: transcript.AssistantPhaseFinal,
				},
			},
		},
	}
}

func screenContains(snapshot pty.ScreenSnapshot, want string) bool {
	for row := range snapshot.Cells {
		if screenRow(snapshot, row) == want {
			return true
		}
	}
	return false
}

func screenRow(snapshot pty.ScreenSnapshot, row int) string {
	var text strings.Builder
	for _, cell := range snapshot.Cells[row] {
		text.WriteString(cell.Content)
	}
	return strings.TrimRight(text.String(), " ")
}
