package scrollback

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"
)

func TestNativeLiveAreaRenderWritesPreSplitLines(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, &out, nil)
	defer buffer.close()
	liveArea := newNativeLiveAreaImpl(buffer, 80, 24)

	if err := liveArea.Render(nativeLiveAreaFrame("one", "two")); err != nil {
		t.Fatalf("render returned error: %v", err)
	}

	if got, want := out.String(), "one"+terminalLineBreak+"two"+xansi.HideCursor; got != want {
		t.Fatalf("terminal output = %q, want %q", got, want)
	}
}

func TestNativeLiveAreaRenderErasesPreviousFrameBeforeDrawingNext(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, &out, nil)
	defer buffer.close()
	liveArea := newNativeLiveAreaImpl(buffer, 80, 24)

	if err := liveArea.Render(nativeLiveAreaFrame("one", "two")); err != nil {
		t.Fatalf("first render returned error: %v", err)
	}
	if err := liveArea.Render(nativeLiveAreaFrame("three")); err != nil {
		t.Fatalf("second render returned error: %v", err)
	}

	want := "one" + terminalLineBreak + "two" + xansi.HideCursor + liveAreaEraseSequence(2) + "three" + xansi.HideCursor
	if got := out.String(); got != want {
		t.Fatalf("terminal output = %q, want %q", got, want)
	}
}

func TestNativeLiveAreaRenderSkipsIdenticalFrame(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, &out, nil)
	defer buffer.close()
	liveArea := newNativeLiveAreaImpl(buffer, 80, 24)

	frame := nativeLiveAreaFrame("one", "two")
	if err := liveArea.Render(frame); err != nil {
		t.Fatalf("first render returned error: %v", err)
	}
	firstOutput := out.String()
	if err := liveArea.Render(frame); err != nil {
		t.Fatalf("second render returned error: %v", err)
	}

	if got := out.String(); got != firstOutput {
		t.Fatalf("identical frame changed terminal output from %q to %q", firstOutput, got)
	}
}

func TestNativeLiveAreaRenderPlacesVisibleCursorFromFrame(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, &out, nil)
	defer buffer.close()
	liveArea := newNativeLiveAreaImpl(buffer, 80, 24)

	if err := liveArea.Render(NativeLiveAreaFrame{
		Lines:  []string{"one", "two"},
		Cursor: NativeLiveAreaCursor{Visible: true, Row: 0, Col: 2},
	}); err != nil {
		t.Fatalf("render returned error: %v", err)
	}

	want := "one" + terminalLineBreak + "two" + xansi.ShowCursor + "\r" + xansi.CursorUp(1) + xansi.CursorForward(2)
	if got := out.String(); got != want {
		t.Fatalf("terminal output = %q, want %q", got, want)
	}
}

func TestNativeLiveAreaRenderPanicsWhenCursorRowIsOutsideFrame(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, &out, nil)
	defer buffer.close()
	liveArea := newNativeLiveAreaImpl(buffer, 80, 24)

	panicText := capturePanicText(t, func() {
		_ = liveArea.Render(NativeLiveAreaFrame{
			Lines:  []string{"one"},
			Cursor: NativeLiveAreaCursor{Visible: true, Row: 1, Col: 0},
		})
	})
	assertPanicContains(t, panicText, "live area cursor row is outside submitted frame")
}

func TestNativeLiveAreaRenderPanicsWhenCursorColumnIsOutsideTerminalWidth(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, &out, nil)
	defer buffer.close()
	liveArea := newNativeLiveAreaImpl(buffer, 80, 24)

	panicText := capturePanicText(t, func() {
		_ = liveArea.Render(NativeLiveAreaFrame{
			Lines:  []string{"one"},
			Cursor: NativeLiveAreaCursor{Visible: true, Row: 0, Col: 80},
		})
	})
	assertPanicContains(t, panicText, "live area cursor column is outside terminal width")
}

func TestNativeLiveAreaRenderPanicsForEmptyContent(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, &out, nil)
	defer buffer.close()
	liveArea := newNativeLiveAreaImpl(buffer, 80, 24)

	panicText := capturePanicText(t, func() {
		_ = liveArea.Render(NativeLiveAreaFrame{})
	})
	assertPanicContains(t, panicText, "live area content must not be empty")
}

func TestNativeLiveAreaRenderPanicsWhenContentExceedsTerminalHeight(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 2, &out, nil)
	defer buffer.close()
	liveArea := newNativeLiveAreaImpl(buffer, 80, 2)

	panicText := capturePanicText(t, func() {
		_ = liveArea.Render(nativeLiveAreaFrame("one", "two", "three"))
	})
	assertPanicContains(t, panicText, "live area content exceeds terminal height")
	assertPanicContains(t, panicText, "line_count=3")
}

func TestNativeLiveAreaRenderPanicsWhenLineExceedsTerminalWidth(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 3, 24, &out, nil)
	defer buffer.close()
	liveArea := newNativeLiveAreaImpl(buffer, 3, 24)

	panicText := capturePanicText(t, func() {
		_ = liveArea.Render(nativeLiveAreaFrame("abcd"))
	})
	assertPanicContains(t, panicText, "live area line 0 exceeds terminal width")
	assertPanicContains(t, panicText, "terminal_width=3")
}

func TestNativeLiveAreaRenderPanicsWhenLineContainsNewline(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, &out, nil)
	defer buffer.close()
	liveArea := newNativeLiveAreaImpl(buffer, 80, 24)

	panicText := capturePanicText(t, func() {
		_ = liveArea.Render(nativeLiveAreaFrame("one\ntwo"))
	})
	assertPanicContains(t, panicText, "live area line 0 contains CR or LF")
}

func TestNativeLiveAreaConstructorPanicsForMismatchedStableBufferDimensions(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, &out, nil)
	defer buffer.close()

	panicText := capturePanicText(t, func() {
		_ = newNativeLiveAreaImpl(buffer, 79, 24)
	})
	assertPanicContains(t, panicText, "live area terminal dimensions must match stable buffer dimensions")
}

func TestNativeLiveAreaConstructorPanicsWhenAlreadyAttached(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, &out, nil)
	defer buffer.close()
	_ = newNativeLiveAreaImpl(buffer, 80, 24)

	panicText := capturePanicText(t, func() {
		_ = newNativeLiveAreaImpl(buffer, 80, 24)
	})
	assertPanicContains(t, panicText, "live area already attached")
}

func TestStableSteerErasesAndRestoresLiveAreaInOneFrame(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, &out, nil)
	defer buffer.close()
	liveArea := newNativeLiveAreaImpl(buffer, 80, 24)
	if err := liveArea.Render(nativeLiveAreaFrame("live")); err != nil {
		t.Fatalf("render returned error: %v", err)
	}

	if err := buffer.Steer("stable"); err != nil {
		t.Fatalf("steer returned error: %v", err)
	}

	want := "live" + xansi.HideCursor + liveAreaEraseSequence(1) + "stable" + terminalLineBreak + "live" + xansi.HideCursor
	if got := out.String(); got != want {
		t.Fatalf("terminal output = %q, want %q", got, want)
	}
}

func TestStableStreamingKeepsPartialAssistantContentInLiveTailUntilFinish(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, &out, nil)
	defer buffer.close()
	liveArea := newNativeLiveAreaImpl(buffer, 80, 24)
	if err := liveArea.Render(nativeLiveAreaFrame("live")); err != nil {
		t.Fatalf("render returned error: %v", err)
	}

	if err := buffer.StreamMarkdownAssistantContent("he"); err != nil {
		t.Fatalf("stream returned error: %v", err)
	}

	want := "live" + xansi.HideCursor
	if got := out.String(); got != want {
		t.Fatalf("terminal output = %q, want %q", got, want)
	}
	if got, want := strings.Join(buffer.AssistantStreamTailLines(), "|"), "he"; got != want {
		t.Fatalf("assistant stream tail = %q, want %q", got, want)
	}
}

func TestNativeLiveAreaRenderDuringAssistantStreamingKeepsChromeVisibleWithoutLinefeed(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, &out, nil)
	defer buffer.close()
	liveArea := newNativeLiveAreaImpl(buffer, 80, 24)
	if err := liveArea.Render(nativeLiveAreaFrame("old live")); err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	if err := buffer.StreamMarkdownAssistantContent("stream"); err != nil {
		t.Fatalf("stream returned error: %v", err)
	}
	if err := liveArea.Render(nativeLiveAreaFrame("latest live")); err != nil {
		t.Fatalf("render during stream returned error: %v", err)
	}
	wantAfterRender := "old live" + xansi.HideCursor + liveAreaEraseSequence(1) + "latest live" + xansi.HideCursor
	if got := out.String(); got != wantAfterRender {
		t.Fatalf("live render during stream output = %q, want %q", got, wantAfterRender)
	}
	if err := buffer.FinishAssistantStreaming(); err != nil {
		t.Fatalf("finish returned error: %v", err)
	}

	want := wantAfterRender + liveAreaEraseSequence(1) + "stream" + terminalLineBreak + "latest live" + xansi.HideCursor
	if got := out.String(); got != want {
		t.Fatalf("terminal output = %q, want %q", got, want)
	}
}

func TestAssistantStreamAppendErasesStreamChromeWithoutAddingLinefeed(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 6, 24, &out, nil)
	defer buffer.close()
	liveArea := newNativeLiveAreaImpl(buffer, 6, 24)
	if err := liveArea.Render(nativeLiveAreaFrame("old")); err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	if err := buffer.StreamMarkdownAssistantContent("hello"); err != nil {
		t.Fatalf("first stream returned error: %v", err)
	}
	if err := liveArea.Render(nativeLiveAreaFrame("input", "hello")); err != nil {
		t.Fatalf("render during stream returned error: %v", err)
	}
	if err := buffer.StreamMarkdownAssistantContent(" world"); err != nil {
		t.Fatalf("second stream returned error: %v", err)
	}

	want := "old" + xansi.HideCursor +
		liveAreaEraseSequence(1) + "input" + terminalLineBreak + "hello" + xansi.HideCursor +
		liveAreaEraseSequence(2) + "hello " + terminalLineBreak
	if got := out.String(); got != want {
		t.Fatalf("terminal output = %q, want %q", got, want)
	}
	if got, want := strings.Join(buffer.AssistantStreamTailLines(), "|"), "world"; got != want {
		t.Fatalf("assistant stream tail = %q, want %q", got, want)
	}
	if err := liveArea.Render(nativeLiveAreaFrame("input", "world")); err != nil {
		t.Fatalf("render latest tail returned error: %v", err)
	}
	wantAfterRender := want + "input" + terminalLineBreak + "world" + xansi.HideCursor
	if got := out.String(); got != wantAfterRender {
		t.Fatalf("terminal output after live tail render = %q, want %q", got, wantAfterRender)
	}
}

func TestAssistantStreamAppendErasesMultilineStreamChromeFromBottom(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 6, 24, &out, nil)
	defer buffer.close()
	liveArea := newNativeLiveAreaImpl(buffer, 6, 24)
	if err := liveArea.Render(nativeLiveAreaFrame("old 1", "old 2", "old 3")); err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	if err := buffer.StreamMarkdownAssistantContent("hello"); err != nil {
		t.Fatalf("first stream returned error: %v", err)
	}
	if err := liveArea.Render(nativeLiveAreaFrame("new 1", "new 2", "hello")); err != nil {
		t.Fatalf("render during stream returned error: %v", err)
	}
	if err := buffer.StreamMarkdownAssistantContent(" world"); err != nil {
		t.Fatalf("second stream returned error: %v", err)
	}

	want := "old 1" + terminalLineBreak + "old 2" + terminalLineBreak + "old 3" + xansi.HideCursor +
		liveAreaEraseSequence(3) +
		"new 1" + terminalLineBreak + "new 2" + terminalLineBreak + "hello" + xansi.HideCursor +
		liveAreaEraseSequence(3) + "hello " + terminalLineBreak
	if got := out.String(); got != want {
		t.Fatalf("terminal output = %q, want %q", got, want)
	}
	if got, want := strings.Join(buffer.AssistantStreamTailLines(), "|"), "world"; got != want {
		t.Fatalf("assistant stream tail = %q, want %q", got, want)
	}
}

func TestNativeLiveAreaHoldoffFlushDuringAssistantStreamingDefersLiveRestore(t *testing.T) {
	var out bytes.Buffer
	available := true
	buffer := NewOngoingScrollbackBufferImpl(
		context.Background(),
		80,
		24,
		&out,
		nil,
		WithNormalBufferAvailability(func() bool { return available }),
	)
	defer buffer.close()
	liveArea := newNativeLiveAreaImpl(buffer, 80, 24)
	if err := liveArea.Render(nativeLiveAreaFrame("old live")); err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	available = false
	if err := buffer.StreamMarkdownAssistantContent("he"); err != nil {
		t.Fatalf("held stream returned error: %v", err)
	}
	if err := liveArea.Render(nativeLiveAreaFrame("latest live")); err != nil {
		t.Fatalf("held live render returned error: %v", err)
	}

	available = true
	if err := buffer.flushHoldoff(); err != nil {
		t.Fatalf("flush holdoff returned error: %v", err)
	}
	wantAfterFlush := "old live" + xansi.HideCursor
	if got := out.String(); got != wantAfterFlush {
		t.Fatalf("holdoff flush during stream output = %q, want %q", got, wantAfterFlush)
	}
	if err := buffer.FinishAssistantStreaming(); err != nil {
		t.Fatalf("finish returned error: %v", err)
	}
	wantAfterFinish := wantAfterFlush + liveAreaEraseSequence(1) + "he" + terminalLineBreak + "latest live" + xansi.HideCursor
	if got := out.String(); got != wantAfterFinish {
		t.Fatalf("finish output = %q, want %q", got, wantAfterFinish)
	}
}

func TestQueuedSteeringFlushErasesOnceAndRestoresOnce(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, &out, nil)
	defer buffer.close()
	liveArea := newNativeLiveAreaImpl(buffer, 80, 24)
	if err := liveArea.Render(nativeLiveAreaFrame("live")); err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	if err := buffer.StreamMarkdownAssistantContent("stream"); err != nil {
		t.Fatalf("stream returned error: %v", err)
	}
	firstErr := make(chan error, 1)
	secondErr := make(chan error, 1)
	go func() { firstErr <- buffer.Steer("first") }()
	waitForQueuedSteers(t, buffer, 1)
	go func() { secondErr <- buffer.Steer("second") }()
	waitForQueuedSteers(t, buffer, 2)

	if err := buffer.FinishAssistantStreaming(); err != nil {
		t.Fatalf("finish returned error: %v", err)
	}
	if err := <-firstErr; err != nil {
		t.Fatalf("first steer returned error: %v", err)
	}
	if err := <-secondErr; err != nil {
		t.Fatalf("second steer returned error: %v", err)
	}

	want := "live" +
		xansi.HideCursor + liveAreaEraseSequence(1) + "stream" + terminalLineBreak +
		"first" + terminalLineBreak + "second" + terminalLineBreak + "live" + xansi.HideCursor
	if got := out.String(); got != want {
		t.Fatalf("terminal output = %q, want %q", got, want)
	}
}

func TestStableWriteSkipsWhenLiveEraseFails(t *testing.T) {
	eraseErr := errors.New("erase failed")
	writer := &scriptedWriter{errors: []error{nil, eraseErr}}
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, writer, nil)
	defer buffer.close()
	liveArea := newNativeLiveAreaImpl(buffer, 80, 24)
	if err := liveArea.Render(nativeLiveAreaFrame("live")); err != nil {
		t.Fatalf("render returned error: %v", err)
	}

	err := buffer.Steer("stable")
	if !errors.Is(err, eraseErr) {
		t.Fatalf("steer error = %v, want %v", err, eraseErr)
	}

	if got, want := strings.Join(writer.Writes(), "|"), "live"+xansi.HideCursor; got != want {
		t.Fatalf("successful writes = %q, want %q", got, want)
	}
}

func TestStableWriteFailureStillAttemptsLiveRestore(t *testing.T) {
	stableErr := errors.New("stable failed")
	writer := &scriptedWriter{errors: []error{nil, nil, stableErr, nil}}
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, writer, nil)
	defer buffer.close()
	liveArea := newNativeLiveAreaImpl(buffer, 80, 24)
	if err := liveArea.Render(nativeLiveAreaFrame("live")); err != nil {
		t.Fatalf("render returned error: %v", err)
	}

	err := buffer.Steer("stable")
	if !errors.Is(err, stableErr) {
		t.Fatalf("steer error = %v, want %v", err, stableErr)
	}

	if got, want := strings.Join(writer.Writes(), "|"), "live"+xansi.HideCursor+"|"+liveAreaEraseSequence(1)+"|live"+xansi.HideCursor; got != want {
		t.Fatalf("successful writes = %q, want %q", got, want)
	}
}

func TestLiveAreaRenderFailureStoresDesiredContentForLaterStableRestore(t *testing.T) {
	renderErr := errors.New("render failed")
	writer := &scriptedWriter{errors: []error{renderErr, nil, nil, nil}}
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, writer, nil)
	defer buffer.close()
	liveArea := newNativeLiveAreaImpl(buffer, 80, 24)

	err := liveArea.Render(nativeLiveAreaFrame("desired"))
	if !errors.Is(err, renderErr) {
		t.Fatalf("render error = %v, want %v", err, renderErr)
	}
	if err := buffer.Steer("stable"); err != nil {
		t.Fatalf("steer returned error: %v", err)
	}

	if got, want := strings.Join(writer.Writes(), "|"), "stable"+terminalLineBreak+"|desired"+xansi.HideCursor; got != want {
		t.Fatalf("successful writes = %q, want %q", got, want)
	}
}

func TestNativeLiveAreaHoldoffStoresLatestFrameUntilFlush(t *testing.T) {
	var out bytes.Buffer
	available := false
	buffer := NewOngoingScrollbackBufferImpl(
		context.Background(),
		80,
		24,
		&out,
		nil,
		WithNormalBufferAvailability(func() bool { return available }),
	)
	defer buffer.close()
	liveArea := newNativeLiveAreaImpl(buffer, 80, 24)

	if err := liveArea.Render(nativeLiveAreaFrame("old")); err != nil {
		t.Fatalf("old render returned error: %v", err)
	}
	if err := liveArea.Render(NativeLiveAreaFrame{
		Lines:  []string{"new"},
		Cursor: NativeLiveAreaCursor{Visible: true, Row: 0, Col: 2},
	}); err != nil {
		t.Fatalf("new render returned error: %v", err)
	}
	if got := out.String(); got != "" {
		t.Fatalf("holdoff wrote live frame while normal buffer unavailable: %q", got)
	}

	available = true
	if err := buffer.flushHoldoff(); err != nil {
		t.Fatalf("flush holdoff returned error: %v", err)
	}
	want := "new" + xansi.ShowCursor + "\r" + xansi.CursorForward(2)
	if got := out.String(); got != want {
		t.Fatalf("held live output = %q, want %q", got, want)
	}
}

func TestStableWriteAfterCursorPlacementRestoresAnchorBeforeErasingLiveArea(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, &out, nil)
	defer buffer.close()
	liveArea := newNativeLiveAreaImpl(buffer, 80, 24)
	frame := NativeLiveAreaFrame{
		Lines:  []string{"one", "two"},
		Cursor: NativeLiveAreaCursor{Visible: true, Row: 0, Col: 2},
	}
	if err := liveArea.Render(frame); err != nil {
		t.Fatalf("render returned error: %v", err)
	}

	if err := buffer.Steer("stable"); err != nil {
		t.Fatalf("steer returned error: %v", err)
	}

	placeCursor := xansi.ShowCursor + "\r" + xansi.CursorUp(1) + xansi.CursorForward(2)
	restoreAnchor := xansi.CursorDown(1) + "\r"
	want := "one" + terminalLineBreak + "two" + placeCursor +
		restoreAnchor + liveAreaEraseSequence(2) + "stable" + terminalLineBreak +
		"one" + terminalLineBreak + "two" + placeCursor
	if got := out.String(); got != want {
		t.Fatalf("terminal output = %q, want %q", got, want)
	}
}

func TestCloseErasesRenderedLiveFrameBeforeReleasingOwnership(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, &out, nil)
	liveArea := newNativeLiveAreaImpl(buffer, 80, 24)
	frame := NativeLiveAreaFrame{
		Lines:  []string{"old top", "old input", "old bottom"},
		Cursor: NativeLiveAreaCursor{Visible: true, Row: 1, Col: 4},
	}
	if err := liveArea.Render(frame); err != nil {
		t.Fatalf("render returned error: %v", err)
	}

	buffer.close()

	placeCursor := xansi.ShowCursor + "\r" + xansi.CursorUp(1) + xansi.CursorForward(4)
	restoreAnchor := xansi.CursorDown(1) + "\r"
	want := "old top" + terminalLineBreak + "old input" + terminalLineBreak + "old bottom" + placeCursor +
		restoreAnchor + liveAreaEraseSequence(3)
	if got := out.String(); got != want {
		t.Fatalf("terminal output = %q, want %q", got, want)
	}
}

func nativeLiveAreaFrame(lines ...string) NativeLiveAreaFrame {
	return NativeLiveAreaFrame{Lines: lines}
}
