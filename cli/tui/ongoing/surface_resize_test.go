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

func TestTmuxResizeDoesNotMarkMutableBandAsRedrawable(t *testing.T) {
	var output bytes.Buffer
	surface := NewSurfaceWithOptions(&output, SurfaceOptions{
		TerminalResize: TerminalResizeTmuxWidthRehydration,
		MarkdownLinks:  transcriptrender.MarkdownLinkLabelOnly,
	})
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
	feedFrame("render legacy transcript and live band")
	if err := stream.Resize(pty.MustDimensions(28, 80)); err != nil {
		t.Fatalf("expand terminal: %v", err)
	}
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
	feedFrame("resize legacy live band")
	snapshot, err := stream.ScreenSnapshot()
	if err != nil {
		t.Fatalf("snapshot after tmux expansion: %v", err)
	}
	if screenContains(snapshot, "OLD_LIVE_TOP") || screenContains(snapshot, "OLD_LIVE_BOTTOM") {
		t.Fatalf("tmux expansion retained stale mutable rows: %q", snapshot.RenderText())
	}
	if got := screenRow(snapshot, 26); got != "NEW_LIVE_TOP" {
		t.Fatalf("tmux expansion live band first row = %q, want NEW_LIVE_TOP", got)
	}
	if got := screenRow(snapshot, 27); got != "NEW_LIVE_BOTTOM" {
		t.Fatalf("tmux expansion live band bottom row = %q, want NEW_LIVE_BOTTOM", got)
	}
}

func TestDirectTerminalExpansionErasesPreviousMutableBand(t *testing.T) {
	var output bytes.Buffer
	surface := NewSurfaceWithOptions(&output, SurfaceOptions{
		TerminalResize: TerminalResizeWidthRehydration,
		MarkdownLinks:  transcriptrender.MarkdownLinkLabelOnly,
	})
	initial := FrameInput{
		Size: Size{Width: 80, Height: 18},
		Sections: []FrameSection{{
			Kind:  FrameSectionStatus,
			Lines: []string{"OLD_LIVE_TOP", "OLD_LIVE_BOTTOM"},
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

	if _, err := surface.ApplyTerminalMessage(committedAssistantMessage("IMMUTABLE"), initial); err != nil {
		t.Fatalf("render direct-terminal transcript and live band: %v", err)
	}
	feedFrame("render direct-terminal transcript and live band")
	if err := stream.Resize(pty.MustDimensions(28, 80)); err != nil {
		t.Fatalf("expand terminal: %v", err)
	}
	expanded := FrameInput{
		Size: Size{Width: 80, Height: 28},
		Sections: []FrameSection{{
			Kind:  FrameSectionStatus,
			Lines: []string{"NEW_LIVE_TOP", "NEW_LIVE_BOTTOM"},
		}},
	}
	if _, err := surface.Resize(expanded.Size, expanded); err != nil {
		t.Fatalf("resize direct-terminal live band: %v", err)
	}
	feedFrame("resize direct-terminal live band")
	snapshot, err := stream.ScreenSnapshot()
	if err != nil {
		t.Fatalf("snapshot after expansion: %v", err)
	}

	if screenContains(snapshot, "OLD_LIVE_TOP") || screenContains(snapshot, "OLD_LIVE_BOTTOM") {
		t.Fatalf("direct-terminal expansion retained stale mutable rows: %q", snapshot.RenderText())
	}
	if got := screenRow(snapshot, 26); got != "NEW_LIVE_TOP" {
		t.Fatalf("expanded live band first row = %q, want NEW_LIVE_TOP", got)
	}
	if got := screenRow(snapshot, 27); got != "NEW_LIVE_BOTTOM" {
		t.Fatalf("expanded live band bottom row = %q, want NEW_LIVE_BOTTOM", got)
	}
}

func TestScratchHydrationResetErasesExpandedBottomBand(t *testing.T) {
	var output bytes.Buffer
	surface := NewSurfaceWithOptions(&output, SurfaceOptions{
		TerminalResize: TerminalResizeWidthRehydration,
		MarkdownLinks:  transcriptrender.MarkdownLinkLabelOnly,
	})
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
	feedFrame("render direct-terminal transcript and live band")
	if err := stream.Resize(pty.MustDimensions(28, 81)); err != nil {
		t.Fatalf("resize terminal: %v", err)
	}

	expanded := initial
	expanded.Size = Size{Width: 81, Height: 28}
	if _, err := surface.ResetForScratchHydration(RehydrateReasonWidthChange, expanded); err != nil {
		t.Fatalf("reset after expansion: %v", err)
	}
	feedFrame("reset after expansion")

	snapshot, err := stream.ScreenSnapshot()
	if err != nil {
		t.Fatalf("snapshot after scratch reset: %v", err)
	}
	if screenContains(snapshot, "LIVE_TOP") || screenContains(snapshot, "LIVE_BOTTOM") {
		t.Fatalf("scratch reset retained pre-resize mutable rows: %q", snapshot.RenderText())
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
