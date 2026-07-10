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
	"core/shared/clientui"
	"github.com/charmbracelet/x/ansi"
	"github.com/google/uuid"
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
		"\x1b[2;1H\x1b[2K\x1b[3;1H\x1b[2K\x1b[4;1H\x1b[2K" +
		"\x1b[2;1Htool running\x1b[3;1H> prompt\x1b[4;1Hready" +
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
		"\x1b[1;1H\x1b[2K\x1b[2;1H\x1b[2K" +
		"\x1b[?25l"
	if got := out.String(); got != want {
		t.Fatalf("too-short live band bytes = %q, want %q", got, want)
	}
}

func TestRenderAddsAssistantTailOnlyFromSurfaceState(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	if _, err := surface.ApplyTerminalMessage(clientui.TranscriptMessage{
		Kind: clientui.TranscriptMessageAssistantDelta,
		AssistantDelta: &clientui.TranscriptAssistantDelta{
			StreamID: uuid.New(),
			Delta:    "streaming commentary",
		},
	}, FrameInput{Size: Size{Width: 30, Height: 4}}); err != nil {
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
	if got, want := surface.previousBandHeight, 1; got != want {
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
	if got, want := out.String(), "\x1b[r\x1b[?6l\x1b[4;1H\x1b[2K\x1b[4;1Hready\x1b[?25l"; got != want {
		t.Fatalf("height-only repaint bytes = %q, want %q", got, want)
	}
}
