package app

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	"core/cli/tui"
	"core/shared/clientui"
	"core/shared/config"

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

func TestMainUIProgramOptionsPreservesTerminalFileOutput(t *testing.T) {
	state := newUITerminalCursorState()
	file := &fakeTerminalCursorFile{fd: 42}
	options := mainUIProgramOptionsWithOutput(config.Settings{}, state, newUIRendererOutputGateState(), file)
	if len(options) != 3 {
		t.Fatalf("main options length = %d, want filter, focus reporting, and output options", len(options))
	}

	output := newUITerminalCursorWriter(file, state)
	terminalFile, ok := output.(terminalCursorFile)
	if !ok {
		t.Fatalf("expected main options output to preserve terminal file interface, got %T", output)
	}
	if got := terminalFile.Fd(); got != 42 {
		t.Fatalf("fd = %d, want 42", got)
	}
}

func TestTerminalCursorWriterPassesThroughRendererControlWrites(t *testing.T) {
	state := newUITerminalCursorState()
	state.Set(uiTerminalCursorPlacement{Visible: true, CursorRow: 4, CursorCol: 6, AnchorRow: 9})

	var out bytes.Buffer
	writer := newUITerminalCursorWriter(&out, state)
	if _, err := writer.Write([]byte("frame")); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	for _, sequence := range []string{xansi.EraseEntireScreen, xansi.CursorHomePosition} {
		out.Reset()
		if _, err := writer.Write([]byte(sequence)); err != nil {
			t.Fatalf("write control sequence: %v", err)
		}
		if got := out.String(); got != sequence {
			t.Fatalf("control write should pass through unchanged, got %q want %q", got, sequence)
		}
	}
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

type shortTerminalCursorWriter struct {
	limit int
	out   bytes.Buffer
}

func (w *shortTerminalCursorWriter) Write(p []byte) (int, error) {
	if len(p) > w.limit {
		_, _ = w.out.Write(p[:w.limit])
		return w.limit, nil
	}
	_, _ = w.out.Write(p)
	return len(p), nil
}

func (w *shortTerminalCursorWriter) String() string {
	return w.out.String()
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

func TestUITerminalCursorPlacementTracksWrappedOngoingInputAcrossWidthChanges(t *testing.T) {
	state := newUITerminalCursorState()
	m := newProjectedStaticUIModel(WithUITerminalCursorState(state))
	m.termWidth = 24
	m.termHeight = 12
	m.windowSizeKnown = true
	m.input = "alpha beta gamma delta epsilon"
	m.inputCursor = -1
	m.layout().syncViewport()

	view := m.View()
	assertRenderedLinesFitWidth(t, view, m.termWidth)
	placement, ok := state.Snapshot()
	if !ok {
		t.Fatal("expected visible terminal cursor placement")
	}
	if placement.AltScreen {
		t.Fatal("expected ongoing placement to use normal buffer coordinates")
	}
	if placement.CursorCol >= m.termWidth {
		t.Fatalf("cursor col %d outside width %d", placement.CursorCol, m.termWidth)
	}
	if placement.CursorRow < 0 || placement.CursorRow > placement.AnchorRow {
		t.Fatalf("cursor row should be inside rendered frame, got %+v", placement)
	}

	m.termWidth = 16
	m.layout().syncViewport()
	view = m.View()
	assertRenderedLinesFitWidth(t, view, m.termWidth)
	narrow, ok := state.Snapshot()
	if !ok {
		t.Fatal("expected visible terminal cursor placement after width change")
	}
	if narrow.CursorCol >= m.termWidth {
		t.Fatalf("narrow cursor col %d outside width %d", narrow.CursorCol, m.termWidth)
	}
	if narrow == placement {
		t.Fatalf("expected width change to update cursor placement, before=%+v after=%+v", placement, narrow)
	}
}

func TestUITerminalCursorPlacementTracksWrappedAltScreenInputAcrossWidthChanges(t *testing.T) {
	state := newUITerminalCursorState()
	m := newProjectedStaticUIModel(WithUITerminalCursorState(state))
	m.termWidth = 26
	m.termHeight = 12
	m.windowSizeKnown = true
	m.altScreenActive = true
	m.input = "one two three four five six"
	m.inputCursor = -1
	m.layout().syncViewport()

	view := m.View()
	assertRenderedLinesFitWidth(t, view, m.termWidth)
	placement, ok := state.Snapshot()
	if !ok {
		t.Fatal("expected visible terminal cursor placement")
	}
	if !placement.AltScreen {
		t.Fatal("expected alt-screen placement to use absolute coordinates")
	}

	m.termWidth = 18
	m.layout().syncViewport()
	view = m.View()
	assertRenderedLinesFitWidth(t, view, m.termWidth)
	narrow, ok := state.Snapshot()
	if !ok {
		t.Fatal("expected visible terminal cursor placement after alt-screen width change")
	}
	if !narrow.AltScreen {
		t.Fatal("expected alt-screen placement to remain absolute after width change")
	}
	if narrow.CursorCol >= m.termWidth {
		t.Fatalf("narrow cursor col %d outside width %d", narrow.CursorCol, m.termWidth)
	}
	if narrow == placement {
		t.Fatalf("expected alt-screen width change to update cursor placement, before=%+v after=%+v", placement, narrow)
	}
}

func TestMainInputCursorUsesSharedFieldDisplayWidth(t *testing.T) {
	state := newUITerminalCursorState()
	m := newProjectedStaticUIModel(WithUITerminalCursorState(state))
	m.termWidth = 12
	m.termHeight = 10
	m.windowSizeKnown = true
	m.input = "ab👍cd"
	m.inputCursor = 3
	m.layout().syncViewport()

	cursor := m.layout().inputPaneCursor(m.termWidth)
	if !cursor.Visible {
		t.Fatal("expected main input cursor")
	}
	if cursor.Row != framedInputContentCursorRow(0) || cursor.Col != 6 {
		t.Fatalf("cursor = %+v, want framed content row %d col 6", cursor, framedInputContentCursorRow(0))
	}
	view := m.View()
	assertRenderedLinesFitWidth(t, view, m.termWidth)
	if !strings.Contains(xansi.Strip(view), "› ab👍cd") {
		t.Fatalf("expected input text rendered through shared field, got %q", view)
	}
}

func TestAskInputCursorUsesSharedFieldDisplayWidth(t *testing.T) {
	state := newUITerminalCursorState()
	m := newProjectedStaticUIModel(WithUITerminalCursorState(state))
	m.termWidth = 12
	m.termHeight = 10
	m.windowSizeKnown = true
	reply := make(chan askReply, 1)
	testSetActiveAsk(m, &askEvent{req: clientui.PendingPromptEvent{Question: "Question?"}, reply: reply})
	m.ask.input = "ab👍cd"
	m.ask.inputCursor = 3
	m.layout().syncViewport()

	cursor := m.layout().inputPaneCursor(m.termWidth)
	if !cursor.Visible {
		t.Fatal("expected ask input cursor")
	}
	if cursor.Row != framedInputContentCursorRow(1) || cursor.Col != 6 {
		t.Fatalf("cursor = %+v, want framed content row %d col 6", cursor, framedInputContentCursorRow(1))
	}
	view := m.View()
	assertRenderedLinesFitWidth(t, view, m.termWidth)
	if !strings.Contains(xansi.Strip(view), "› ab👍cd") {
		t.Fatalf("expected ask input text rendered through shared field, got %q", view)
	}
}

func TestViewDoesNotAppendHideCursorWhenRealTerminalCursorVisible(t *testing.T) {
	state := newUITerminalCursorState()
	m := newProjectedStaticUIModel(WithUITerminalCursorState(state))
	m.termWidth = 24
	m.termHeight = 10
	m.windowSizeKnown = true
	m.input = "visible cursor"
	m.layout().syncViewport()

	view := m.View()
	assertRenderedLinesFitWidth(t, view, m.termWidth)
	if strings.Contains(view, ansiHideCursor) {
		t.Fatalf("did not expect view to hide terminal cursor when real cursor is active: %q", view)
	}
	if _, ok := state.Snapshot(); !ok {
		t.Fatal("expected real cursor placement")
	}
}

func TestRealCursorFrameChangesWhenOnlyInputSpacesMoveCursor(t *testing.T) {
	state := newUITerminalCursorState()
	m := newProjectedStaticUIModel(WithUITerminalCursorState(state))
	m.termWidth = 24
	m.termHeight = 10
	m.windowSizeKnown = true
	m.layout().syncViewport()

	emptyView := m.View()
	m.input = " "
	m.inputCursor = -1
	m.layout().syncViewport()
	spaceView := m.View()
	if emptyView == spaceView {
		t.Fatal("expected real-cursor frame to change when only trailing spaces move cursor")
	}
	placement, ok := state.Snapshot()
	if !ok {
		t.Fatal("expected real cursor placement")
	}
	if got, want := placement.CursorCol, lipgloss.Width("›  "); got != want {
		t.Fatalf("cursor col after typed space = %d, want %d", got, want)
	}
}

func TestRealCursorFrameChangesAfterTypingEachSpace(t *testing.T) {
	state := newUITerminalCursorState()
	model := tea.Model(newProjectedStaticUIModel(WithUITerminalCursorState(state)))
	m := model.(*uiModel)
	m.termWidth = 24
	m.termHeight = 10
	m.windowSizeKnown = true
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
	m.termWidth = 24
	m.termHeight = 10
	m.windowSizeKnown = true
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
	m.termWidth = 24
	m.termHeight = 10
	m.windowSizeKnown = true
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
