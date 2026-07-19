package ongoing

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"core/cli/tui/transcriptrender"
	"core/internal/testharness/pty"
	"core/internal/testharness/pty/analyzer"
	"core/shared/runtimeids"
	"github.com/charmbracelet/x/ansi"
)

func TestRenderPaintsLiveAreaWhenMinimumFits(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)

	_, err := surface.Render(FrameInput{
		Size: Size{Width: 20, Height: 4},
		Sections: []FrameSection{
			{Kind: FrameSectionPendingTools, Lines: []string{"tool running"}},
			{Kind: FrameSectionInput, Lines: []string{"> prompt"}},
			{Kind: FrameSectionStatus, Lines: []string{"ready"}},
		},
	})
	if err != nil {
		t.Fatalf("render live band: %v", err)
	}

	want := "\x1b[r\x1b[?6l" +
		"\x1b]133;C\x1b\\" +
		"\x1b[2;1H\x1b]133;C\x1b\\\x1b[2K" +
		"\x1b[3;1H\x1b]133;C\x1b\\\x1b[2K" +
		"\x1b[4;1H\x1b]133;C\x1b\\\x1b[2K" +
		"\x1b[2;1H\x1b]133;A;redraw=1\x1b\\" +
		"tool running\x1b[3;1H> prompt\x1b[4;1Hready" +
		"\x1b[?25l"
	if got := out.String(); got != want {
		t.Fatalf("live band bytes = %q, want %q", got, want)
	}
}

func TestRenderPaintsStyledLiveAreaThroughThemeEncoder(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)

	_, err := surface.Render(FrameInput{
		Size:  Size{Width: 30, Height: 2},
		Theme: "dark",
		Sections: []FrameSection{{
			Kind: FrameSectionPendingTools,
			StyledLines: []transcriptrender.Line{{Spans: []transcriptrender.Span{
				transcriptrender.SemanticSpan("⢎ ", transcriptrender.StyleRoleToolShell, transcriptrender.SpanAttributeFaint),
				transcriptrender.SemanticSpan(" ", transcriptrender.StyleRoleToolShell, transcriptrender.SpanAttributeFaint),
				transcriptrender.SemanticSpan("go test", transcriptrender.StyleRoleToolShell, transcriptrender.SpanAttributeFaint),
			}}},
		}},
	})
	if err != nil {
		t.Fatalf("render styled live band: %v", err)
	}

	got := out.String()
	if got == "" || got == ansi.Strip(got) {
		t.Fatalf("styled live band output has no ANSI styling: %q", got)
	}
	assertVisibleTextOps(t, parseTerminalOps(got), []string{"⢎  go test"})
}

func TestRenderHidesEntireLiveAreaWhenMinimumDoesNotFit(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	if _, err := surface.Render(FrameInput{
		Size:     Size{Width: 20, Height: 5},
		Sections: []FrameSection{{Kind: FrameSectionInput, Lines: []string{"one", "two", "three"}}},
	}); err != nil {
		t.Fatalf("render initial frame: %v", err)
	}
	out.Reset()

	_, err := surface.Render(FrameInput{
		Size:     Size{Width: 20, Height: 2},
		Sections: []FrameSection{{Kind: FrameSectionInput, Lines: []string{"one", "two", "three"}}},
	})
	if err != nil {
		t.Fatalf("render too-short frame: %v", err)
	}

	want := "\x1b[r\x1b[?6l" +
		"\x1b]133;C\x1b\\" +
		"\x1b[1;1H\x1b]133;C\x1b\\\x1b[2K" +
		"\x1b[2;1H\x1b]133;C\x1b\\\x1b[2K" +
		"\x1b[1;1H\x1b]133;A;redraw=1\x1b\\" +
		"\x1b[?25l"
	if got := out.String(); got != want {
		t.Fatalf("too-short live band bytes = %q, want %q", got, want)
	}
}

func TestRenderAddsAssistantTailOnlyFromSurfaceState(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	if _, err := surface.ApplyTerminalMessage(
		assistantDeltaMessage(runtimeids.NewAssistantStreamID(), "streaming commentary"),
		FrameInput{Size: Size{Width: 30, Height: 4}},
	); err != nil {
		t.Fatalf("apply assistant delta: %v", err)
	}
	out.Reset()

	_, err := surface.Render(FrameInput{
		Size:     Size{Width: 30, Height: 4},
		Sections: []FrameSection{{Kind: FrameSectionStatus, Lines: []string{"status"}}},
	})
	if err != nil {
		t.Fatalf("render assistant tail: %v", err)
	}

	assertVisibleTextOps(t, parseTerminalOps(out.String()), []string{"streaming commentary", "status"})
}

func TestRenderShrinksLiveBandBeforeTerminalCoordinateWrites(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)

	_, err := surface.Render(FrameInput{
		Size: Size{Width: 20, Height: 4},
		Sections: []FrameSection{{
			Kind:  FrameSectionInput,
			Lines: []string{"one", "two", "three", "four", "five"},
		}},
	})
	if err != nil {
		t.Fatalf("render oversized live band: %v", err)
	}

	ops := parseTerminalOps(out.String())
	assertCursorAddressRowsAtLeastOne(t, ops)
	assertVisibleTextOps(t, ops, []string{"three", "four", "five"})
}

func TestRenderPlacesTargetedCursorAfterLiveBandShrink(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)

	_, err := surface.Render(FrameInput{
		Size: Size{Width: 20, Height: 4},
		Sections: []FrameSection{
			{Kind: FrameSectionInput, Lines: []string{"one", "two", "three", "four"}},
			{Kind: FrameSectionStatus, Lines: []string{"ready"}},
		},
		Cursor: Cursor{
			Visible: true,
			Row:     4,
			Column:  2,
			Target:  &CursorTarget{SectionKind: FrameSectionInput, Row: 4},
		},
	})
	if err != nil {
		t.Fatalf("render shrink cursor frame: %v", err)
	}

	ops := parseTerminalOps(out.String())
	assertVisibleTextOps(t, ops, []string{"two", "three", "four", "ready"})
	assertCursorAddress(t, ops, 3, 2)
}

func TestCommittedRowsReserveTerminalSpaceBeforeLiveBand(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)

	if _, err := surface.ApplyTerminalMessage(committedMessage(userRow("committed")), FrameInput{
		Size: Size{Width: 40, Height: 3},
		Sections: []FrameSection{
			{Kind: FrameSectionPendingTools, Lines: []string{"tool"}},
			{Kind: FrameSectionInput, Lines: []string{"> prompt"}},
			{Kind: FrameSectionStatus, Lines: []string{"ready"}},
		},
	}); err != nil {
		t.Fatalf("apply committed row: %v", err)
	}

	assertVisibleTextOps(t, parseTerminalOps(out.String()), []string{"❯ committed"})
}

func TestLiveBandGrowthScrollsImmutableRegionBeforeErase(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)

	if _, err := surface.ApplyTerminalMessage(committedMessage(userRow("committed")), FrameInput{
		Size:     Size{Width: 40, Height: 5},
		Sections: []FrameSection{{Kind: FrameSectionStatus, Lines: []string{"ready"}}},
	}); err != nil {
		t.Fatalf("apply committed row: %v", err)
	}
	if !surface.immutableScrollbackProduced() {
		t.Fatal("test setup did not produce immutable scrollback")
	}
	if got, want := surface.retainedBandHeight, 1; got != want {
		t.Fatalf("test setup previous band height = %d, want %d", got, want)
	}
	out.Reset()

	if _, err := surface.Render(FrameInput{
		Size: Size{Width: 40, Height: 5},
		Sections: []FrameSection{
			{Kind: FrameSectionPendingTools, Lines: []string{"tool"}},
			{Kind: FrameSectionInput, Lines: []string{"> prompt"}},
			{Kind: FrameSectionStatus, Lines: []string{"ready"}},
		},
	}); err != nil {
		t.Fatalf("grow live band: %v", err)
	}

	wantPrefix := []terminalOp{
		{kind: terminalOpCSI, value: "\x1b[r"},
		{kind: terminalOpCSI, value: "\x1b[?6l"},
		{kind: terminalOpOSC, value: "\x1b]133;C\x1b\\"},
		{kind: terminalOpCSI, value: "\x1b[1;4r"},
		{kind: terminalOpCSI, value: "\x1b[4;1H"},
		{kind: terminalOpCRLF, value: "\r\n"},
		{kind: terminalOpCRLF, value: "\r\n"},
		{kind: terminalOpCSI, value: "\x1b[r"},
		{kind: terminalOpCSI, value: "\x1b[?6l"},
	}
	assertTerminalPrefix(t, parseTerminalOps(out.String()), wantPrefix)
	assertVisibleTextOps(t, parseTerminalOps(out.String()), []string{"tool", "> prompt", "ready"})
}

func TestTransientLiveBandShrinkRetainsMutableRowsWithoutSyntheticScrollback(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	shortFrame := FrameInput{
		Size:     Size{Width: 40, Height: 8},
		Sections: []FrameSection{{Kind: FrameSectionStatus, Lines: []string{"ready"}}},
	}
	tallFrame := FrameInput{
		Size: Size{Width: 40, Height: 8},
		Sections: []FrameSection{
			{Kind: FrameSectionPicker, Lines: []string{"one", "two", "three", "four"}},
			{Kind: FrameSectionStatus, Lines: []string{"ready"}},
		},
	}
	if _, err := surface.ApplyTerminalMessage(committedMessage(userRow("committed")), shortFrame); err != nil {
		t.Fatalf("append committed row: %v", err)
	}
	if _, err := surface.Render(tallFrame); err != nil {
		t.Fatalf("open transient live frame: %v", err)
	}
	shrinkStart := out.Len()
	if _, err := surface.Render(shortFrame); err != nil {
		t.Fatalf("close transient live frame: %v", err)
	}

	shrinkBytes := out.Bytes()[shrinkStart:]
	ops := parseTerminalOps(string(shrinkBytes))
	if got, want := surface.retainedBandHeight, 5; got != want {
		t.Fatalf("retained mutable height = %d, want %d", got, want)
	}
	if got := countTerminalOp(ops, terminalOpOSC, redrawableSemanticPromptSequence()); got != 1 {
		t.Fatalf("redrawable prompt boundary count = %d, want 1; ops=%+v", got, ops)
	}
	assertCursorAddress(t, ops, shortFrame.Size.Height-5+1, 1)
	assertCursorAddress(t, ops, shortFrame.Size.Height, 1)
	for _, op := range ops {
		if op.kind == terminalOpCRLF {
			t.Fatalf("transient shrink appended immutable rows: ops=%+v", ops)
		}
	}

	capture, err := pty.NewCapture(
		pty.MustDimensions(shortFrame.Size.Height, shortFrame.Size.Width),
		[]pty.Chunk{pty.NewChunk(0, time.Millisecond, out.Bytes())},
	)
	if err != nil {
		t.Fatalf("create transient-shrink capture: %v", err)
	}
	analysis, err := analyzer.Analyze(capture)
	if err != nil {
		t.Fatalf("analyze transient-shrink lifecycle: %v", err)
	}
	rows := strings.Split(analysis.Screen.RenderText(), "\n")
	committedRow := screenRowIndex(rows, "❯ committed")
	readyRow := screenRowIndex(rows, "ready")
	if committedRow < 0 || readyRow < 0 {
		t.Fatalf("committed/live rows missing from terminal screen: %q", rows)
	}
	if readyRow != shortFrame.Size.Height-1 {
		t.Fatalf("live content row = %d, want bottom row %d; screen=%q", readyRow, shortFrame.Size.Height-1, rows)
	}
}

func TestTransientLiveBandShrinkToEmptyRetainsSingleMutableBoundary(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	frame := FrameInput{
		Size: Size{Width: 40, Height: 6},
		Sections: []FrameSection{{
			Kind:  FrameSectionPicker,
			Lines: []string{"one", "two", "three"},
		}},
	}
	if _, err := surface.Render(frame); err != nil {
		t.Fatalf("render transient live frame: %v", err)
	}
	out.Reset()

	if _, err := surface.Render(FrameInput{Size: frame.Size}); err != nil {
		t.Fatalf("shrink transient live frame to empty: %v", err)
	}

	ops := parseTerminalOps(out.String())
	if got, want := surface.retainedBandHeight, 3; got != want {
		t.Fatalf("retained mutable height = %d, want %d", got, want)
	}
	if got := countTerminalOp(ops, terminalOpOSC, redrawableSemanticPromptSequence()); got != 1 {
		t.Fatalf("redrawable prompt boundary count = %d, want 1; ops=%+v", got, ops)
	}
	assertCursorAddress(t, ops, frame.Size.Height-3+1, 1)
	if got := visibleTextRows(ops); len(got) != 0 {
		t.Fatalf("empty live content painted visible rows: %q", got)
	}
	for _, op := range ops {
		if op.kind == terminalOpCRLF {
			t.Fatalf("transient empty shrink appended immutable rows: ops=%+v", ops)
		}
	}
}

func TestImmutableRowConsumesOneRetainedMutableRow(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	tallFrame := FrameInput{
		Size: Size{Width: 40, Height: 8},
		Sections: []FrameSection{{
			Kind:  FrameSectionPicker,
			Lines: []string{"one", "two", "three", "four", "five"},
		}},
	}
	shortFrame := FrameInput{
		Size:     tallFrame.Size,
		Sections: []FrameSection{{Kind: FrameSectionStatus, Lines: []string{"working", "ready"}}},
	}
	if _, err := surface.Render(tallFrame); err != nil {
		t.Fatalf("render tall live frame: %v", err)
	}
	if _, err := surface.Render(shortFrame); err != nil {
		t.Fatalf("retain tall live capacity after shrink: %v", err)
	}
	if got, want := surface.retainedBandHeight, 5; got != want {
		t.Fatalf("retained setup height = %d, want %d", got, want)
	}
	out.Reset()

	if _, err := surface.ApplyTerminalMessage(committedMessage(userRow("committed")), shortFrame); err != nil {
		t.Fatalf("append immutable row into retained capacity: %v", err)
	}

	if got, want := surface.retainedBandHeight, 4; got != want {
		t.Fatalf("retained height after one immutable row = %d, want %d", got, want)
	}
	ops := parseTerminalOps(out.String())
	if got := countTerminalKind(ops, terminalOpCRLF); got != 1 {
		t.Fatalf("immutable append CRLF count = %d, want one logical row; ops=%+v", got, ops)
	}
	if got := countTerminalOp(ops, terminalOpOSC, redrawableSemanticPromptSequence()); got != 1 {
		t.Fatalf("redrawable prompt boundary count = %d, want 1; ops=%+v", got, ops)
	}
	assertCursorAddress(t, ops, shortFrame.Size.Height-4+1, 1)
	assertVisibleTextOps(t, ops, []string{"❯ committed", "working", "ready"})
}

func TestImmutableSeparatorConsumesOneRetainedMutableRow(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	frame := FrameInput{
		Size: Size{Width: 40, Height: 8},
		Sections: []FrameSection{{
			Kind:  FrameSectionPicker,
			Lines: []string{"one", "two", "three", "four", "five"},
		}},
	}
	if _, err := surface.ApplyTerminalMessage(committedMessage(userRow("request")), frame); err != nil {
		t.Fatalf("append initial immutable group: %v", err)
	}
	shortFrame := FrameInput{
		Size:     frame.Size,
		Sections: []FrameSection{{Kind: FrameSectionStatus, Lines: []string{"ready"}}},
	}
	if _, err := surface.Render(shortFrame); err != nil {
		t.Fatalf("retain mutable capacity before group change: %v", err)
	}
	if got, want := surface.retainedBandHeight, 5; got != want {
		t.Fatalf("retained setup height = %d, want %d", got, want)
	}
	out.Reset()

	if _, err := surface.ApplyTerminalMessage(committedMessage(toolRow("tool result")), shortFrame); err != nil {
		t.Fatalf("append separator and immutable row: %v", err)
	}

	if got, want := surface.retainedBandHeight, 3; got != want {
		t.Fatalf("retained height after separator and row = %d, want %d", got, want)
	}
	rows := immutableAppendedRows(parseTerminalOps(out.String()))
	assertRowStructure(t, rows, []rowKind{{separator: true}, {content: "• tool result"}})
}

func TestFullHeightLiveBandClearsForImmutableAppend(t *testing.T) {
	for _, height := range []int{1, 3} {
		t.Run(fmt.Sprintf("height-%d", height), func(t *testing.T) {
			var out bytes.Buffer
			surface := NewSurface(&out)
			liveLines := make([]string, height)
			for index := range liveLines {
				liveLines[index] = fmt.Sprintf("live-%d", index+1)
			}
			frame := FrameInput{
				Size:     Size{Width: 40, Height: height},
				Sections: []FrameSection{{Kind: FrameSectionStatus, Lines: liveLines}},
				Cursor:   Cursor{Visible: true, Row: height, Column: 2},
			}
			if _, err := surface.Render(frame); err != nil {
				t.Fatalf("render full live band: %v", err)
			}
			out.Reset()

			if _, err := surface.ApplyTerminalMessage(committedMessage(userRow("committed")), frame); err != nil {
				t.Fatalf("append immutable row in full terminal: %v", err)
			}

			if got, want := surface.retainedBandHeight, height-1; got != want {
				t.Fatalf("retained height after immutable append = %d, want %d", got, want)
			}
			ops := parseTerminalOps(out.String())
			assertVisibleTextOps(t, ops, []string{"❯ committed"})
			if got := countTerminalKind(ops, terminalOpCRLF); got != 1 {
				t.Fatalf("immutable append CRLF count = %d, want 1; ops=%+v", got, ops)
			}
			if got, want := countTerminalOp(ops, terminalOpOSC, redrawableSemanticPromptSequence()), min(1, height-1); got != want {
				t.Fatalf("retained boundary count = %d, want %d; ops=%+v", got, want, ops)
			}
			for _, address := range cursorAddresses(ops) {
				if address.row < 1 {
					t.Fatalf("immutable append emitted invalid cursor row %d: ops=%+v", address.row, ops)
				}
			}
		})
	}
}

func TestImmutableAppendGrowthBeyondRetainedCapacityScrollsOnlyExcess(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	baseFrame := FrameInput{
		Size:     Size{Width: 40, Height: 8},
		Sections: []FrameSection{{Kind: FrameSectionStatus, Lines: []string{"one", "two"}}},
	}
	if _, err := surface.ApplyTerminalMessage(committedMessage(userRow("initial")), baseFrame); err != nil {
		t.Fatalf("append initial immutable row: %v", err)
	}
	out.Reset()
	grownFrame := FrameInput{
		Size: baseFrame.Size,
		Sections: []FrameSection{{
			Kind:  FrameSectionPicker,
			Lines: []string{"one", "two", "three", "four"},
		}},
	}

	if _, err := surface.ApplyTerminalMessage(committedMessage(userRow("next")), grownFrame); err != nil {
		t.Fatalf("append immutable row with live growth: %v", err)
	}

	if got, want := surface.retainedBandHeight, 4; got != want {
		t.Fatalf("retained height after growth = %d, want %d", got, want)
	}
	ops := parseTerminalOps(out.String())
	if got, want := countTerminalKind(ops, terminalOpCRLF), 3; got != want {
		t.Fatalf("CRLF count = %d, want %d (two growth rows plus one immutable row); ops=%+v", got, want, ops)
	}
}

func TestPendingToolCommitDoesNotLeaveBlankRowBetweenConsecutiveTools(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	baseFrame := FrameInput{
		Size: Size{Width: 40, Height: 10},
		Sections: []FrameSection{
			{Kind: FrameSectionInput, Lines: []string{"> prompt"}},
			{Kind: FrameSectionStatus, Lines: []string{"ready"}},
		},
	}
	pendingFrame := baseFrame
	pendingFrame.Sections = append(
		[]FrameSection{{Kind: FrameSectionPendingTools, Lines: []string{"tool running"}}},
		baseFrame.Sections...,
	)

	if _, err := surface.ApplyTerminalMessage(committedMessage(userRow("request")), baseFrame); err != nil {
		t.Fatalf("append initial user row: %v", err)
	}
	if _, err := surface.Render(pendingFrame); err != nil {
		t.Fatalf("render first pending tool: %v", err)
	}
	if _, err := surface.ApplyTerminalMessage(committedMessage(toolRow("tool one")), baseFrame); err != nil {
		t.Fatalf("commit first tool: %v", err)
	}
	if _, err := surface.Render(pendingFrame); err != nil {
		t.Fatalf("render second pending tool: %v", err)
	}
	if _, err := surface.ApplyTerminalMessage(committedMessage(toolRow("tool two")), baseFrame); err != nil {
		t.Fatalf("commit second tool: %v", err)
	}

	capture, err := pty.NewCapture(
		pty.MustDimensions(baseFrame.Size.Height, baseFrame.Size.Width),
		[]pty.Chunk{pty.NewChunk(0, time.Millisecond, out.Bytes())},
	)
	if err != nil {
		t.Fatalf("create pending-tool lifecycle capture: %v", err)
	}
	analysis, err := analyzer.Analyze(capture)
	if err != nil {
		t.Fatalf("analyze pending-tool lifecycle: %v", err)
	}
	rows := strings.Split(analysis.Screen.RenderText(), "\n")
	firstToolRow := screenRowIndex(rows, "• tool one")
	secondToolRow := screenRowIndex(rows, "• tool two")
	if firstToolRow < 0 || secondToolRow < 0 {
		t.Fatalf("tool rows missing from terminal screen: %q", rows)
	}
	if secondToolRow != firstToolRow+1 {
		t.Fatalf(
			"consecutive tool rows = %d and %d, want adjacent rows; screen=%q",
			firstToolRow,
			secondToolRow,
			rows,
		)
	}
}

func countTerminalOp(ops []terminalOp, kind terminalOpKind, value string) int {
	count := 0
	for _, op := range ops {
		if op.kind == kind && op.value == value {
			count++
		}
	}
	return count
}

func countTerminalKind(ops []terminalOp, kind terminalOpKind) int {
	count := 0
	for _, op := range ops {
		if op.kind == kind {
			count++
		}
	}
	return count
}

func screenRowIndex(rows []string, want string) int {
	for index, row := range rows {
		if strings.TrimSpace(row) == want {
			return index
		}
	}
	return -1
}

func TestInitialLiveBandRenderDoesNotScrollBlankScreen(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)

	if _, err := surface.Render(FrameInput{
		Size: Size{Width: 40, Height: 5},
		Sections: []FrameSection{
			{Kind: FrameSectionInput, Lines: []string{"> prompt"}},
			{Kind: FrameSectionStatus, Lines: []string{"ready"}},
		},
	}); err != nil {
		t.Fatalf("render initial live band: %v", err)
	}

	ops := parseTerminalOps(out.String())
	for _, op := range ops {
		if op.kind == terminalOpCRLF {
			t.Fatalf("initial blank live render scrolled terminal: ops=%+v", ops)
		}
	}
}

func TestEstablishedZeroBandTerminalGrowthDoesNotInventMutableGeometry(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	initial := FrameInput{Size: Size{Width: 40, Height: 5}}
	if _, err := surface.ApplyTerminalMessage(committedMessage(userRow("committed")), initial); err != nil {
		t.Fatalf("append committed row with zero live band: %v", err)
	}
	if surface.lastPaintedSize == nil || surface.retainedBandHeight != 0 {
		t.Fatalf("zero-band setup geometry = size %+v retained %d", surface.lastPaintedSize, surface.retainedBandHeight)
	}
	out.Reset()

	if _, err := surface.Resize(Size{Width: 40, Height: 8}, FrameInput{}); err != nil {
		t.Fatalf("grow zero-band terminal: %v", err)
	}

	if got := surface.retainedBandHeight; got != 0 {
		t.Fatalf("terminal growth invented retained height %d", got)
	}
	ops := parseTerminalOps(out.String())
	if countTerminalOp(ops, terminalOpOSC, redrawableSemanticPromptSequence()) != 0 {
		t.Fatalf("zero-band growth emitted a redrawable prompt boundary: ops=%+v", ops)
	}
	for _, op := range ops {
		if op.kind == terminalOpCRLF || (op.kind == terminalOpCSI && op.value == "\x1b[2K") {
			t.Fatalf("zero-band growth erased or scrolled terminal rows: ops=%+v", ops)
		}
	}
	out.Reset()

	grown := FrameInput{Size: Size{Width: 40, Height: 8}}
	if _, err := surface.ApplyTerminalMessage(committedMessage(userRow("next")), grown); err != nil {
		t.Fatalf("append immutable row after zero-band growth: %v", err)
	}
	if got := surface.retainedBandHeight; got != 0 {
		t.Fatalf("zero-band immutable append invented retained height %d", got)
	}
	ops = parseTerminalOps(out.String())
	if got := countTerminalKind(ops, terminalOpCRLF); got != 1 {
		t.Fatalf("zero-band immutable append CRLF count = %d, want 1; ops=%+v", got, ops)
	}
	if countTerminalOp(ops, terminalOpOSC, redrawableSemanticPromptSequence()) != 0 {
		t.Fatalf("zero-band immutable append emitted a redrawable boundary: ops=%+v", ops)
	}
	for _, op := range ops {
		if op.kind == terminalOpCSI && op.value == "\x1b[2K" {
			t.Fatalf("zero-band immutable append erased invented mutable rows: ops=%+v", ops)
		}
	}
}

func assertCursorAddress(t *testing.T, ops []terminalOp, wantRow int, wantColumn int) {
	t.Helper()
	for _, address := range cursorAddresses(ops) {
		if address.row == wantRow && address.column == wantColumn {
			return
		}
	}
	t.Fatalf("cursor address %d,%d not found in ops %+v", wantRow, wantColumn, ops)
}

func assertCursorAddressRowsAtLeastOne(t *testing.T, ops []terminalOp) {
	t.Helper()
	for _, address := range cursorAddresses(ops) {
		if address.row < 1 {
			t.Fatalf("cursor address row = %d, want >= 1", address.row)
		}
	}
}

type cursorAddress struct {
	row    int
	column int
}

func cursorAddresses(ops []terminalOp) []cursorAddress {
	var out []cursorAddress
	for _, op := range ops {
		if op.kind != terminalOpCSI {
			continue
		}
		var row int
		var column int
		if _, err := fmt.Sscanf(op.value, "\x1b[%d;%dH", &row, &column); err == nil {
			out = append(out, cursorAddress{row: row, column: column})
		}
	}
	return out
}

func TestHeightOnlyResizeRepaintsWithoutRehydration(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	if _, err := surface.Render(FrameInput{
		Size:     Size{Width: 20, Height: 5},
		Sections: []FrameSection{{Kind: FrameSectionStatus, Lines: []string{"ready"}}},
	}); err != nil {
		t.Fatalf("render initial frame: %v", err)
	}
	out.Reset()

	result, err := surface.Resize(Size{Width: 20, Height: 4}, FrameInput{
		Sections: []FrameSection{{Kind: FrameSectionStatus, Lines: []string{"ready"}}},
	})
	if err != nil {
		t.Fatalf("height-only resize: %v", err)
	}
	if result.Action != ResultNoop {
		t.Fatalf("resize action = %q, want noop", result.Action)
	}
	if got, want := out.String(), "\x1b[r\x1b[?6l\x1b]133;C\x1b\\\x1b[4;1H\x1b]133;C\x1b\\\x1b[2K\x1b[4;1H\x1b]133;A;redraw=1\x1b\\ready\x1b[?25l"; got != want {
		t.Fatalf("height-only repaint bytes = %q, want %q", got, want)
	}
}
