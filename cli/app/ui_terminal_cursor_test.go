package app

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	"core/cli/tui"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
)

func TestTerminalCursorSequencesUseExplicitPlacement(t *testing.T) {
	normal := uiTerminalCursorPlacement{Visible: true, CursorRow: 3, CursorCol: 5, AnchorRow: 9}
	if got, want := terminalCursorRestoreSequence(normal), xansi.CursorDown(6)+"\r"; got != want {
		t.Fatalf("normal restore sequence = %q, want %q", got, want)
	}
	if got, want := terminalCursorPlaceSequence(normal), xansi.ShowCursor+xansi.CursorUp(6)+xansi.CursorForward(5); got != want {
		t.Fatalf("normal place sequence = %q, want %q", got, want)
	}

	alt := uiTerminalCursorPlacement{Visible: true, CursorRow: 4, CursorCol: 7, AnchorRow: 12, AltScreen: true}
	if got, want := terminalCursorRestoreSequence(alt), xansi.CursorPosition(1, 13); got != want {
		t.Fatalf("alt restore sequence = %q, want %q", got, want)
	}
	if got, want := terminalCursorPlaceSequence(alt), xansi.ShowCursor+xansi.CursorPosition(8, 5); got != want {
		t.Fatalf("alt place sequence = %q, want %q", got, want)
	}
}

func TestTerminalCursorPlacementSanitizesNormalBufferRows(t *testing.T) {
	placement := sanitizeTerminalCursorPlacement(uiTerminalCursorPlacement{Visible: true, CursorRow: 8, CursorCol: 2, AnchorRow: 3})
	if placement.AnchorRow != placement.CursorRow {
		t.Fatalf("normal-buffer anchor row = %d, want cursor row %d", placement.AnchorRow, placement.CursorRow)
	}
	if got, want := terminalCursorPlaceSequence(placement), xansi.ShowCursor+xansi.CursorForward(2); got != want {
		t.Fatalf("normal place sequence = %q, want %q", got, want)
	}

	alt := sanitizeTerminalCursorPlacement(uiTerminalCursorPlacement{Visible: true, CursorRow: 8, CursorCol: 2, AnchorRow: 3, AltScreen: true})
	if alt.AnchorRow != 3 {
		t.Fatalf("alt-screen anchor row = %d, want 3", alt.AnchorRow)
	}
}

func TestTerminalCursorWriterRestoresAnchorAroundWrites(t *testing.T) {
	state := newUITerminalCursorState()
	state.Set(uiTerminalCursorPlacement{Visible: true, CursorRow: 2, CursorCol: 4, AnchorRow: 5})

	var out bytes.Buffer
	writer := newUITerminalCursorWriter(&out, state)
	if _, err := writer.Write([]byte("frame")); err != nil {
		t.Fatalf("write: %v", err)
	}
	first := out.String()
	if !strings.HasPrefix(first, "frame") {
		t.Fatalf("first write should not need anchor restore, got %q", first)
	}
	if !strings.HasSuffix(first, xansi.ShowCursor+xansi.CursorUp(3)+xansi.CursorForward(4)) {
		t.Fatalf("first write did not place cursor, got %q", first)
	}

	out.Reset()
	if _, err := writer.Write([]byte("next")); err != nil {
		t.Fatalf("write next: %v", err)
	}
	next := out.String()
	if !strings.HasPrefix(next, xansi.CursorDown(3)+"\rnext") {
		t.Fatalf("next write should restore anchor before payload, got %q", next)
	}
	if !strings.HasSuffix(next, xansi.ShowCursor+xansi.CursorUp(3)+xansi.CursorForward(4)) {
		t.Fatalf("next write did not replace cursor, got %q", next)
	}
}

func TestTerminalCursorWriterPreservesTerminalFileDescriptor(t *testing.T) {
	state := newUITerminalCursorState()
	file := &fakeTerminalCursorFile{fd: 42}

	writer := newUITerminalCursorWriter(file, state)
	terminalFile, ok := writer.(interface{ Fd() uintptr })
	if !ok {
		t.Fatalf("expected cursor writer to preserve Fd for Bubble Tea TTY detection, got %T", writer)
	}
	if got := terminalFile.Fd(); got != 42 {
		t.Fatalf("fd = %d, want 42", got)
	}
}

type fakeTerminalCursorFile struct {
	bytes.Buffer
	fd uintptr
}

func (f *fakeTerminalCursorFile) Fd() uintptr {
	return f.fd
}

func (f *fakeTerminalCursorFile) Close() error {
	return nil
}

func TestTerminalCursorWriterRestoresAnchorBeforeAltScreenEnter(t *testing.T) {
	state := newUITerminalCursorState()
	state.Set(uiTerminalCursorPlacement{Visible: true, CursorRow: 4, CursorCol: 6, AnchorRow: 9})

	var out bytes.Buffer
	writer := newUITerminalCursorWriter(&out, state)
	if _, err := writer.Write([]byte("frame")); err != nil {
		t.Fatalf("write frame: %v", err)
	}

	out.Reset()
	if _, err := writer.Write([]byte(xansi.SetModeAltScreenSaveCursor)); err != nil {
		t.Fatalf("write alt-screen enter: %v", err)
	}
	if got, want := out.String(), xansi.CursorDown(5)+"\r"+xansi.SetModeAltScreenSaveCursor; got != want {
		t.Fatalf("alt-screen enter should save renderer anchor, got %q want %q", got, want)
	}

	out.Reset()
	if _, err := writer.Write([]byte("next")); err != nil {
		t.Fatalf("write next: %v", err)
	}
	if strings.HasPrefix(out.String(), xansi.CursorDown(5)+"\r") {
		t.Fatalf("next frame should not restore from pre-alt-screen placement, got %q", out.String())
	}
}

func TestTerminalCursorWriterKeepsStateWhenInvalidatingControlWriteFails(t *testing.T) {
	state := newUITerminalCursorState()
	state.Set(uiTerminalCursorPlacement{Visible: true, CursorRow: 4, CursorCol: 6, AnchorRow: 9})

	var out bytes.Buffer
	writer := newUITerminalCursorWriter(&out, state)
	if _, err := writer.Write([]byte("frame")); err != nil {
		t.Fatalf("write frame: %v", err)
	}

	failing := &failingTerminalCursorWriter{failAfter: 0}
	writer = newUITerminalCursorWriter(failing, state)
	if _, err := writer.Write([]byte(xansi.EraseEntireScreen)); !errors.Is(err, errTerminalCursorTestWrite) {
		t.Fatalf("write clear-screen error = %v, want %v", err, errTerminalCursorTestWrite)
	}
	if !state.hasPlacement() {
		t.Fatal("expected placement state to remain after failed invalidating control write")
	}
}

func TestTerminalCursorWriterKeepsStateWhenPlacementSuffixWriteFails(t *testing.T) {
	state := newUITerminalCursorState()
	state.Set(uiTerminalCursorPlacement{Visible: true, CursorRow: 4, CursorCol: 6, AnchorRow: 9})

	failing := &failingTerminalCursorWriter{failAfter: len("frame")}
	writer := newUITerminalCursorWriter(failing, state)
	if _, err := writer.Write([]byte("frame")); !errors.Is(err, errTerminalCursorTestWrite) {
		t.Fatalf("write frame error = %v, want %v", err, errTerminalCursorTestWrite)
	}
	if state.hasPlacement() {
		t.Fatal("did not expect placement state to commit after failed suffix write")
	}
}

func TestTerminalCursorWriterTreatsEmptyWriteAsNoop(t *testing.T) {
	state := newUITerminalCursorState()
	state.Set(uiTerminalCursorPlacement{Visible: true, CursorRow: 4, CursorCol: 6, AnchorRow: 9})

	var out bytes.Buffer
	writer := newUITerminalCursorWriter(&out, state)
	if _, err := writer.Write([]byte("frame")); err != nil {
		t.Fatalf("write frame: %v", err)
	}

	out.Reset()
	if n, err := writer.Write(nil); n != 0 || err != nil {
		t.Fatalf("empty write = (%d, %v), want (0, nil)", n, err)
	}
	if got := out.String(); got != "" {
		t.Fatalf("empty write should not emit cursor sequences, got %q", got)
	}
	if !state.hasPlacement() {
		t.Fatal("empty write should not mutate placement state")
	}
}

var errTerminalCursorTestWrite = errors.New("terminal cursor test write failed")

type failingTerminalCursorWriter struct {
	written   int
	failAfter int
}

func (w *failingTerminalCursorWriter) Write(p []byte) (int, error) {
	if w.written >= w.failAfter {
		return 0, errTerminalCursorTestWrite
	}
	remaining := w.failAfter - w.written
	if len(p) > remaining {
		w.written += remaining
		return remaining, errTerminalCursorTestWrite
	}
	w.written += len(p)
	return len(p), nil
}

func TestTerminalCursorWriterDoesNotRestoreFromStalePlacementAfterClearScreen(t *testing.T) {
	state := newUITerminalCursorState()
	state.Set(uiTerminalCursorPlacement{Visible: true, CursorRow: 4, CursorCol: 6, AnchorRow: 9})

	var out bytes.Buffer
	writer := newUITerminalCursorWriter(&out, state)
	if _, err := writer.Write([]byte("frame")); err != nil {
		t.Fatalf("write frame: %v", err)
	}

	out.Reset()
	if _, err := writer.Write([]byte(xansi.EraseEntireScreen)); err != nil {
		t.Fatalf("write clear screen: %v", err)
	}
	if _, err := writer.Write([]byte(xansi.CursorHomePosition)); err != nil {
		t.Fatalf("write cursor home: %v", err)
	}
	if got, want := out.String(), xansi.EraseEntireScreen+xansi.CursorHomePosition; got != want {
		t.Fatalf("clear screen should not append terminal cursor placement, got %q want %q", got, want)
	}

	out.Reset()
	if _, err := writer.Write([]byte("next")); err != nil {
		t.Fatalf("write next: %v", err)
	}
	got := out.String()
	if strings.HasPrefix(got, xansi.CursorDown(5)+"\r") {
		t.Fatalf("next frame should not restore from stale pre-clear cursor placement, got %q", got)
	}
	if got != "next"+xansi.ShowCursor+xansi.CursorUp(5)+xansi.CursorForward(6) {
		t.Fatalf("next frame = %q", got)
	}
}

func TestTerminalCursorWriterDoesNotRepositionAfterStop(t *testing.T) {
	state := newUITerminalCursorState()
	state.Set(uiTerminalCursorPlacement{Visible: true, CursorRow: 4, CursorCol: 6, AnchorRow: 9})

	var out bytes.Buffer
	writer := newUITerminalCursorWriter(&out, state)
	if _, err := writer.Write([]byte("frame")); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	state.Stop()
	out.Reset()
	payload := "\x1b[?2004l" + xansi.ShowCursor
	if _, err := writer.Write([]byte(payload)); err != nil {
		t.Fatalf("write cleanup: %v", err)
	}
	if got := out.String(); got != payload {
		t.Fatalf("cleanup write should pass through after cursor stop, got %q want %q", got, payload)
	}
}

func TestUITerminalCursorPlacementTracksWrappedInputAcrossWidthChanges(t *testing.T) {
	tests := []struct {
		name        string
		altScreen   bool
		input       string
		wideWidth   int
		narrowWidth int
	}{
		{name: "ongoing", input: "alpha beta gamma delta epsilon", wideWidth: 24, narrowWidth: 16},
		{name: "alt screen", altScreen: true, input: "one two three four five six", wideWidth: 26, narrowWidth: 18},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := newUITerminalCursorState()
			model := newProjectedStaticUIModel(WithUITerminalCursorState(state))
			model.terminalGeometry = terminalGeometryKnown(test.wideWidth, 12)
			model.altScreenActive = test.altScreen
			model.input = test.input
			model.inputCursor = -1
			model.layout().syncViewport()
			size := model.terminalGeometry.Size()
			if size == nil {
				t.Fatal("expected known terminal geometry")
			}

			assertRenderedLinesFitWidth(t, model.View(), size.width)
			wide, ok := state.Snapshot()
			if !ok || wide.AltScreen != test.altScreen || wide.CursorCol >= size.width {
				t.Fatalf("wide placement = %+v, visible=%t", wide, ok)
			}
			if !test.altScreen && (wide.CursorRow < 0 || wide.CursorRow > wide.AnchorRow) {
				t.Fatalf("normal-buffer cursor row outside rendered frame: %+v", wide)
			}

			model.terminalGeometry = terminalGeometryKnown(test.narrowWidth, size.height)
			model.layout().syncViewport()
			size = model.terminalGeometry.Size()
			assertRenderedLinesFitWidth(t, model.View(), size.width)
			narrow, ok := state.Snapshot()
			if !ok || narrow.AltScreen != test.altScreen || narrow.CursorCol >= size.width {
				t.Fatalf("narrow placement = %+v, visible=%t", narrow, ok)
			}
			if narrow == wide {
				t.Fatalf("width change did not update placement: before=%+v after=%+v", wide, narrow)
			}
		})
	}
}

func TestInputCursorsUseSharedFieldDisplayWidth(t *testing.T) {
	for _, test := range []struct {
		name      string
		ask       bool
		cursorRow int
	}{
		{name: "main", cursorRow: framedInputContentCursorRow(0)},
		{name: "ask", ask: true, cursorRow: framedInputContentCursorRow(1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := newUITerminalCursorState()
			model := newProjectedStaticUIModel(WithUITerminalCursorState(state))
			model.terminalGeometry = terminalGeometryKnown(12, 10)
			if test.ask {
				event := testQuestionAskEvent("ask-1", "Question?")
				testSetActiveAsk(model, &event)
				model.ask.input = "ab👍cd"
				model.ask.inputCursor = 3
			} else {
				model.input = "ab👍cd"
				model.inputCursor = 3
			}
			model.layout().syncViewport()
			size := model.terminalGeometry.Size()
			if size == nil {
				t.Fatal("expected known terminal geometry")
			}

			cursor := model.layout().inputPaneCursor(size.width)
			if !cursor.Visible || cursor.Row != test.cursorRow || cursor.Col != 6 {
				t.Fatalf("cursor = %+v, want visible row %d col 6", cursor, test.cursorRow)
			}
			view := model.View()
			assertRenderedLinesFitWidth(t, view, size.width)
			if !strings.Contains(xansi.Strip(view), "› ab👍cd") {
				t.Fatalf("shared input field missing from view %q", view)
			}
		})
	}
}

func TestViewDoesNotAppendHideCursorWhenRealTerminalCursorVisible(t *testing.T) {
	state := newUITerminalCursorState()
	m := newProjectedStaticUIModel(WithUITerminalCursorState(state))
	m.terminalGeometry = terminalGeometryKnown(24, 10)
	m.input = "visible cursor"
	m.layout().syncViewport()
	size := m.terminalGeometry.Size()
	if size == nil {
		t.Fatal("expected known terminal geometry")
	}

	view := m.View()
	assertRenderedLinesFitWidth(t, view, size.width)
	if strings.Contains(view, ansiHideCursor) {
		t.Fatalf("did not expect view to hide terminal cursor when real cursor is active: %q", view)
	}
	if _, ok := state.Snapshot(); !ok {
		t.Fatal("expected real cursor placement")
	}
}

func TestRealCursorFrameChangesAfterTypingEachSpace(t *testing.T) {
	state := newUITerminalCursorState()
	model := tea.Model(newProjectedStaticUIModel(WithUITerminalCursorState(state)))
	m := model.(*uiModel)
	m.terminalGeometry = terminalGeometryKnown(24, 10)
	m.layout().syncViewport()
	previous := m.View()

	for i := range 3 {
		next, _ := model.Update(tea.KeyMsg{Type: tea.KeySpace})
		model = next
		updated := model.(*uiModel)
		updated.layout().syncViewport()
		current := updated.View()
		if current == previous {
			t.Fatalf("view did not change after typing space %d", i+1)
		}
		placement, ok := state.Snapshot()
		if !ok {
			t.Fatalf("expected real cursor placement after typing space %d", i+1)
		}
		if got, want := placement.CursorCol, lipgloss.Width("› ")+i+1; got != want {
			t.Fatalf("cursor col after typing space %d = %d, want %d", i+1, got, want)
		}
		previous = current
	}
	if got, want := model.(*uiModel).input, "   "; got != want {
		t.Fatalf("input = %q, want %q", got, want)
	}
}

func TestRealCursorFrameMarkerNotRenderedWithoutRealCursor(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.terminalGeometry = terminalGeometryKnown(24, 10)
	m.input = " "
	m.layout().syncViewport()

	view := m.View()
	if strings.Contains(view, realCursorFrameMarker(1)) {
		t.Fatalf("did not expect real cursor frame marker without terminal cursor: %q", view)
	}
	if !strings.Contains(view, ansiHideCursor) {
		t.Fatalf("expected soft-cursor fallback frame to hide terminal cursor: %q", view)
	}
}

func TestRealCursorFrameMarkerNotRenderedInDetailMode(t *testing.T) {
	state := newUITerminalCursorState()
	m := newProjectedStaticUIModel(WithUITerminalCursorState(state))
	m.terminalGeometry = terminalGeometryKnown(24, 10)
	m.forwardToView(tui.SetModeMsg{Mode: tui.ModeDetail})
	m.layout().syncViewport()

	view := m.View()
	if strings.Contains(view, realCursorFrameMarker(1)) {
		t.Fatalf("did not expect real cursor frame marker in detail mode: %q", view)
	}
}

func TestTerminalCursorPlacementAccountsForTailTrimmedStatusLine(t *testing.T) {
	state := newUITerminalCursorState()
	m := newProjectedStaticUIModel(WithUITerminalCursorState(state))
	layout := m.layout()
	frame := uiRenderFrame{
		width:      12,
		height:     3,
		chatPanel:  []string{"chat 1", "chat 2", "chat 3"},
		inputPane:  []string{"input 1", "input 2"},
		statusLine: "status",
		tailOnly:   true,
		inputCursor: uiInputFieldCursor{
			Visible: true,
			Row:     0,
			Col:     4,
		},
	}

	view := layout.renderFrame(frame)
	if strings.Contains(view, ansiHideCursor) {
		t.Fatalf("did not expect hidden cursor in real-cursor frame: %q", view)
	}
	placement, ok := state.Snapshot()
	if !ok {
		t.Fatal("expected visible terminal cursor placement")
	}
	if placement.CursorRow != 0 {
		t.Fatalf("cursor row = %d, want 0 after tail trim with status line", placement.CursorRow)
	}
	if placement.AnchorRow != 2 {
		t.Fatalf("anchor row = %d, want 2", placement.AnchorRow)
	}
	if placement.CursorCol != 4 {
		t.Fatalf("cursor col = %d, want 4", placement.CursorCol)
	}
	lines := strings.Split(view, "\n")
	strippedLines := make([]string, 0, len(lines))
	for _, line := range lines {
		strippedLines = append(strippedLines, xansi.Strip(line))
	}
	if got, want := strippedLines, []string{"input 1", "input 2", "status"}; !slices.Equal(got, want) {
		t.Fatalf("rendered lines = %#v, want %#v", got, want)
	}
}

func TestSoftCursorOverlayPreservesColumnAfterTrimmedTrailingSpaces(t *testing.T) {
	rendered := overlayCursorOnLine("› abc", 7, 10, lipgloss.NewStyle().Reverse(true))
	if !strings.HasPrefix(rendered, "› abc  ") {
		t.Fatalf("expected cursor overlay to preserve target column, got %q", rendered)
	}
}

func TestSharedFieldRenderingPreservesExplicitTrailingSpaces(t *testing.T) {
	rendered := renderEditableInputField(10, 1, uiEditableInputRenderSpec{
		Prefix:       "› ",
		Text:         "abc  ",
		CursorIndex:  -1,
		RenderCursor: true,
	})
	if got, want := rendered.Lines[0], "› abc     "; got != want {
		t.Fatalf("line = %q, want %q", got, want)
	}
	if rendered.Cursor.Col != 7 {
		t.Fatalf("cursor col = %d, want 7", rendered.Cursor.Col)
	}
}

func assertRenderedLinesFitWidth(t *testing.T, view string, width int) {
	t.Helper()
	for index, line := range strings.Split(strings.TrimSuffix(view, ansiHideCursor), "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Fatalf("rendered line %d width = %d, want <= %d: %q", index, got, width, line)
		}
	}
}
