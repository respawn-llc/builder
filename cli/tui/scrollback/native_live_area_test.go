package scrollback

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

	if got, want := out.String(), nativeLiveAreaRenderSequence(24, nativeLiveAreaFrame("one", "two")); got != want {
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

	want := nativeLiveAreaRenderSequence(24, nativeLiveAreaFrame("one", "two")) +
		liveAreaErasePhysicalSequence(2, 24) +
		nativeLiveAreaRenderSequence(24, nativeLiveAreaFrame("three"))
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

	want := nativeLiveAreaRenderSequence(24, NativeLiveAreaFrame{
		Lines:  []string{"one", "two"},
		Cursor: NativeLiveAreaCursor{Visible: true, Row: 0, Col: 2},
	})
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

func TestNativeLiveAreaRenderPanicsWhenContentLeavesNoTranscriptRow(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 2, &out, nil)
	defer buffer.close()
	liveArea := newNativeLiveAreaImpl(buffer, 80, 2)

	panicText := capturePanicText(t, func() {
		_ = liveArea.Render(nativeLiveAreaFrame("one", "two"))
	})
	assertPanicContains(t, panicText, "live area content must leave at least one transcript row")
	assertPanicContains(t, panicText, "line_count=2")
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

	want := nativeLiveAreaRenderSequence(24, nativeLiveAreaFrame("live")) +
		liveAreaErasePhysicalSequence(1, 24) +
		stableOutputInsertRowsSequence(1, 24, "stable") +
		nativeLiveAreaRenderSequence(24, nativeLiveAreaFrame("live"))
	if got := out.String(); got != want {
		t.Fatalf("terminal output = %q, want %q", got, want)
	}
}

func TestStableSteerFromTopCursorScrollsThroughBottomBand(t *testing.T) {
	terminal := newNativeLiveAreaTestTerminal(16, 4)
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 16, 4, terminal, nil)
	defer buffer.close()
	liveArea := newNativeLiveAreaImpl(buffer, 16, 4)

	if err := liveArea.Render(nativeLiveAreaFrame("input")); err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	for index := 1; index <= 5; index++ {
		if err := buffer.Steer(fmt.Sprintf("stable-%d", index)); err != nil {
			t.Fatalf("steer %d returned error: %v", index, err)
		}
	}

	if got := terminal.visibleLine(3); got != "input" {
		t.Fatalf("live frame is not anchored at visible bottom, bottom line = %q", got)
	}
	for row, want := range []string{"stable-3", "stable-4", "stable-5"} {
		if got := terminal.visibleLine(row); got != want {
			t.Fatalf("visible stable row %d = %q, want %q", row, got, want)
		}
	}
	if got, want := terminal.historyLines(), []string{"stable-1", "stable-2"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("scrollback history = %#v, want %#v", got, want)
	}
}

func TestStableSteerFullScreenScrollPreservesOldViewportTopLine(t *testing.T) {
	terminal := newNativeLiveAreaTestTerminal(16, 6)
	terminal.history = []string{"c", "d"}
	terminal.screen = []string{"e", "f", "g", "h", "", ""}
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 16, 6, terminal, nil)
	defer buffer.close()
	liveArea := newNativeLiveAreaImpl(buffer, 16, 6)

	if err := liveArea.Render(nativeLiveAreaFrame("input", "status")); err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	if err := buffer.Steer("stable"); err != nil {
		t.Fatalf("steer returned error: %v", err)
	}

	for row, want := range []string{"f", "g", "h", "stable", "input", "status"} {
		if got := terminal.visibleLine(row); got != want {
			t.Fatalf("visible row %d = %q, want %q; screen=%#v history=%#v", row, got, want, terminal.screen, terminal.historyLines())
		}
	}
	if got, want := terminal.historyLines(), []string{"c", "d", "e"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("scrollback history = %#v, want %#v", got, want)
	}
}

func TestStableSteerScrollsAboveMultiLineLiveBand(t *testing.T) {
	terminal := newNativeLiveAreaTestTerminal(16, 5)
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 16, 5, terminal, nil)
	defer buffer.close()
	liveArea := newNativeLiveAreaImpl(buffer, 16, 5)

	if err := liveArea.Render(nativeLiveAreaFrame("live-1", "live-2", "live-3")); err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	for index := 1; index <= 5; index++ {
		if err := buffer.Steer(fmt.Sprintf("stable-%d", index)); err != nil {
			t.Fatalf("steer %d returned error: %v", index, err)
		}
	}

	for row, want := range []string{"stable-4", "stable-5", "live-1", "live-2", "live-3"} {
		if got := terminal.visibleLine(row); got != want {
			t.Fatalf("visible row %d = %q, want %q; screen=%#v history=%#v", row, got, want, terminal.screen, terminal.historyLines())
		}
	}
	if got, want := terminal.historyLines(), []string{"stable-1", "stable-2", "stable-3"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("scrollback history = %#v, want %#v", got, want)
	}
}

func TestNormalBufferPreparationPrecedesFirstNativeWrite(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(
		context.Background(),
		80,
		24,
		&out,
		nil,
		WithNormalBufferPreparation(),
	)
	defer buffer.close()
	liveArea := newNativeLiveAreaImpl(buffer, 80, 24)

	if err := liveArea.Render(nativeLiveAreaFrame("live")); err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	if err := buffer.Steer("stable"); err != nil {
		t.Fatalf("steer returned error: %v", err)
	}

	want := normalBufferPreparationSequence() +
		nativeLiveAreaRenderSequence(24, nativeLiveAreaFrame("live")) +
		liveAreaErasePhysicalSequence(1, 24) +
		stableOutputInsertRowsSequence(1, 24, "stable") +
		nativeLiveAreaRenderSequence(24, nativeLiveAreaFrame("live"))
	if got := out.String(); got != want {
		t.Fatalf("terminal output = %q, want %q", got, want)
	}
}

func TestNormalBufferInvalidationForcesUnchangedLiveAreaRepaint(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(
		context.Background(),
		80,
		24,
		&out,
		nil,
		WithNormalBufferPreparation(),
	)
	defer buffer.close()
	liveArea := newNativeLiveAreaImpl(buffer, 80, 24)

	if err := liveArea.Render(nativeLiveAreaFrame("live")); err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	out.Reset()
	InvalidateNormalBufferPreparation(buffer)
	if err := liveArea.Render(nativeLiveAreaFrame("live")); err != nil {
		t.Fatalf("unchanged render after invalidation returned error: %v", err)
	}

	got := out.String()
	prepIndex := strings.Index(got, normalBufferPreparationSequence())
	eraseIndex := strings.Index(got, liveAreaEraseSequence(1))
	liveIndex := strings.Index(got, "live")
	hideIndex := strings.Index(got, xansi.HideCursor)
	if prepIndex < 0 || eraseIndex < 0 || liveIndex < 0 || hideIndex < 0 || !(prepIndex < eraseIndex && eraseIndex < liveIndex && liveIndex < hideIndex) {
		t.Fatalf("repaint output order is invalid: %q", got)
	}
}

func TestHoldoffFlushPreparesBeforePendingLiveFrameRender(t *testing.T) {
	var out bytes.Buffer
	available := false
	buffer := NewOngoingScrollbackBufferImpl(
		context.Background(),
		80,
		24,
		&out,
		nil,
		WithNormalBufferAvailability(func() bool { return available }),
		WithNormalBufferPreparation(),
	)
	defer buffer.close()
	liveArea := newNativeLiveAreaImpl(buffer, 80, 24)

	if err := liveArea.Render(nativeLiveAreaFrame("held live")); err != nil {
		t.Fatalf("held render returned error: %v", err)
	}
	if got := out.String(); got != "" {
		t.Fatalf("held render wrote while normal buffer unavailable: %q", got)
	}
	available = true
	if err := buffer.flushHoldoff(); err != nil {
		t.Fatalf("flush holdoff returned error: %v", err)
	}

	got := out.String()
	prepIndex := strings.Index(got, normalBufferPreparationSequence())
	liveIndex := strings.Index(got, "held live")
	hideIndex := strings.Index(got, xansi.HideCursor)
	if prepIndex < 0 || liveIndex < 0 || hideIndex < 0 || !(prepIndex < liveIndex && liveIndex < hideIndex) {
		t.Fatalf("held live repaint output order is invalid: %q", got)
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

	want := nativeLiveAreaRenderSequence(24, nativeLiveAreaFrame("live"))
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
	wantAfterRender := nativeLiveAreaRenderSequence(24, nativeLiveAreaFrame("old live")) +
		liveAreaErasePhysicalSequence(1, 24) +
		nativeLiveAreaRenderSequence(24, nativeLiveAreaFrame("latest live"))
	if got := out.String(); got != wantAfterRender {
		t.Fatalf("live render during stream output = %q, want %q", got, wantAfterRender)
	}
	if err := buffer.FinishAssistantStreaming(); err != nil {
		t.Fatalf("finish returned error: %v", err)
	}

	want := wantAfterRender +
		liveAreaErasePhysicalSequence(1, 24) +
		stableOutputInsertRowsSequence(1, 24, "stream") +
		nativeLiveAreaRenderSequence(24, nativeLiveAreaFrame("latest live"))
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
	if err := buffer.StreamMarkdownAssistantContent(" world\nnext\n"); err != nil {
		t.Fatalf("second stream returned error: %v", err)
	}

	want := nativeLiveAreaRenderSequence(24, nativeLiveAreaFrame("old")) +
		liveAreaErasePhysicalSequence(1, 24) +
		nativeLiveAreaRenderSequence(24, nativeLiveAreaFrame("input", "hello")) +
		liveAreaErasePhysicalSequence(2, 24) +
		stableOutputInsertRowsSequence(2, 24, "hello ", "world")
	if got := out.String(); got != want {
		t.Fatalf("terminal output = %q, want %q", got, want)
	}
	if got, want := strings.Join(buffer.AssistantStreamTailLines(), "|"), "next"; got != want {
		t.Fatalf("assistant stream tail = %q, want %q", got, want)
	}
	if err := liveArea.Render(nativeLiveAreaFrame("input", "next")); err != nil {
		t.Fatalf("render latest tail returned error: %v", err)
	}
	wantAfterRender := want + nativeLiveAreaRenderSequence(24, nativeLiveAreaFrame("input", "next"))
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
	if err := buffer.StreamMarkdownAssistantContent(" world\nnext\n"); err != nil {
		t.Fatalf("second stream returned error: %v", err)
	}

	want := nativeLiveAreaRenderSequence(24, nativeLiveAreaFrame("old 1", "old 2", "old 3")) +
		liveAreaErasePhysicalSequence(3, 24) +
		nativeLiveAreaRenderSequence(24, nativeLiveAreaFrame("new 1", "new 2", "hello")) +
		liveAreaErasePhysicalSequence(3, 24) +
		stableOutputInsertRowsSequence(3, 24, "hello ", "world")
	if got := out.String(); got != want {
		t.Fatalf("terminal output = %q, want %q", got, want)
	}
	if got, want := strings.Join(buffer.AssistantStreamTailLines(), "|"), "next"; got != want {
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
	wantAfterFlush := nativeLiveAreaRenderSequence(24, nativeLiveAreaFrame("old live"))
	if got := out.String(); got != wantAfterFlush {
		t.Fatalf("holdoff flush during stream output = %q, want %q", got, wantAfterFlush)
	}
	if err := buffer.FinishAssistantStreaming(); err != nil {
		t.Fatalf("finish returned error: %v", err)
	}
	wantAfterFinish := wantAfterFlush +
		liveAreaErasePhysicalSequence(1, 24) +
		stableOutputInsertRowsSequence(1, 24, "he") +
		nativeLiveAreaRenderSequence(24, nativeLiveAreaFrame("latest live"))
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

	want := nativeLiveAreaRenderSequence(24, nativeLiveAreaFrame("live")) +
		liveAreaErasePhysicalSequence(1, 24) +
		stableOutputInsertRowsSequence(1, 24, "stream", "first", "second") +
		nativeLiveAreaRenderSequence(24, nativeLiveAreaFrame("live"))
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

	if got, want := strings.Join(writer.Writes(), "|"), nativeLiveAreaRenderSequence(24, nativeLiveAreaFrame("live")); got != want {
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

	if got, want := strings.Join(writer.Writes(), "|"), nativeLiveAreaRenderSequence(24, nativeLiveAreaFrame("live"))+"|"+liveAreaErasePhysicalSequence(1, 24)+"|"+nativeLiveAreaRenderSequence(24, nativeLiveAreaFrame("live")); got != want {
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

	if got, want := strings.Join(writer.Writes(), "|"), "stable"+terminalLineBreak+"|"+nativeLiveAreaRenderSequence(24, nativeLiveAreaFrame("desired")); got != want {
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
	want := nativeLiveAreaRenderSequence(24, NativeLiveAreaFrame{
		Lines:  []string{"new"},
		Cursor: NativeLiveAreaCursor{Visible: true, Row: 0, Col: 2},
	})
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

	want := nativeLiveAreaRenderSequence(24, frame) +
		liveAreaErasePhysicalSequence(2, 24) +
		stableOutputInsertRowsSequence(2, 24, "stable") +
		nativeLiveAreaRenderSequence(24, frame)
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

	want := nativeLiveAreaRenderSequence(24, frame) +
		liveAreaErasePhysicalSequence(3, 24)
	if got := out.String(); got != want {
		t.Fatalf("terminal output = %q, want %q", got, want)
	}
}

func nativeLiveAreaFrame(lines ...string) NativeLiveAreaFrame {
	return NativeLiveAreaFrame{Lines: lines}
}

func nativeLiveAreaRenderSequence(terminalHeight int, frame NativeLiveAreaFrame) string {
	return liveAreaBottomAnchorSequence(len(frame.Lines), terminalHeight) +
		strings.Join(frame.Lines, terminalLineBreak) +
		liveAreaCursorPlacementSequence(frame.Cursor, len(frame.Lines))
}

func stableOutputInsertRowsSequence(liveRows int, terminalHeight int, rows ...string) string {
	var out strings.Builder
	for _, row := range rows {
		out.WriteString(stableOutputInsertRowSequence(row, liveRows, terminalHeight))
	}
	return out.String()
}

type nativeLiveAreaTestTerminal struct {
	width        int
	height       int
	row          int
	col          int
	topMargin    int
	bottomMargin int
	screen       []string
	history      []string
}

func newNativeLiveAreaTestTerminal(width int, height int) *nativeLiveAreaTestTerminal {
	screen := make([]string, height)
	return &nativeLiveAreaTestTerminal{width: width, height: height, bottomMargin: height - 1, screen: screen}
}

func (t *nativeLiveAreaTestTerminal) Write(payload []byte) (int, error) {
	for index := 0; index < len(payload); index++ {
		if payload[index] == '\x1b' {
			next := t.applyEscape(payload, index)
			if next > index {
				index = next
			}
			continue
		}
		switch payload[index] {
		case '\r':
			t.col = 0
		case '\n':
			t.lineFeed()
		default:
			t.putByte(payload[index])
		}
	}
	return len(payload), nil
}

func (t *nativeLiveAreaTestTerminal) visibleLine(row int) string {
	if row < 0 || row >= len(t.screen) {
		return ""
	}
	return strings.TrimRight(t.screen[row], " ")
}

func (t *nativeLiveAreaTestTerminal) historyLines() []string {
	out := make([]string, 0, len(t.history))
	for _, line := range t.history {
		if trimmed := strings.TrimRight(line, " "); strings.TrimSpace(trimmed) != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func (t *nativeLiveAreaTestTerminal) applyEscape(payload []byte, start int) int {
	if start+1 >= len(payload) {
		return start
	}
	switch payload[start+1] {
	case '7', '8':
		return start + 1
	case '[':
	default:
		return start
	}
	end := start + 2
	for end < len(payload) && !isNativeLiveAreaTestFinalByte(payload[end]) {
		end++
	}
	if end >= len(payload) {
		return start
	}
	body := string(payload[start+2 : end])
	final := payload[end]
	switch final {
	case 'A':
		t.row -= nativeLiveAreaTestCSIParam(body, 1)
		if t.row < 0 {
			t.row = 0
		}
	case 'B':
		t.row += nativeLiveAreaTestCSIParam(body, 1)
		if t.row >= t.height {
			t.row = t.height - 1
		}
	case 'H', 'f':
		col, row := nativeLiveAreaTestCursorPosition(body)
		t.col = col
		t.row = row
	case 'K':
		if nativeLiveAreaTestCSIParam(body, 0) == 2 && t.row >= 0 && t.row < len(t.screen) {
			t.screen[t.row] = ""
		}
	case 'r':
		top, bottom := nativeLiveAreaTestMargins(body, t.height)
		t.topMargin = top
		t.bottomMargin = bottom
		t.row = top
		t.col = 0
	case 'h', 'l':
	}
	return end
}

func (t *nativeLiveAreaTestTerminal) putByte(value byte) {
	if t.row < 0 || t.row >= t.height || t.col < 0 || t.col >= t.width {
		return
	}
	line := t.screen[t.row]
	if len(line) < t.col {
		line += strings.Repeat(" ", t.col-len(line))
	}
	if len(line) == t.col {
		line += string(value)
	} else {
		line = line[:t.col] + string(value) + line[t.col+1:]
	}
	t.screen[t.row] = line
	t.col++
	if t.col >= t.width {
		t.col = t.width - 1
	}
}

func (t *nativeLiveAreaTestTerminal) lineFeed() {
	if t.row < t.bottomMargin {
		t.row++
		return
	}
	t.scrollUp()
}

func (t *nativeLiveAreaTestTerminal) scrollUp() {
	if t.topMargin < 0 || t.bottomMargin >= t.height || t.topMargin > t.bottomMargin {
		return
	}
	if t.topMargin == 0 && t.bottomMargin == t.height-1 {
		t.history = append(t.history, t.screen[0])
	}
	for row := t.topMargin; row < t.bottomMargin; row++ {
		t.screen[row] = t.screen[row+1]
	}
	t.screen[t.bottomMargin] = ""
}

func isNativeLiveAreaTestFinalByte(value byte) bool {
	return value >= 0x40 && value <= 0x7e
}

func nativeLiveAreaTestCSIParam(body string, fallback int) int {
	body = strings.TrimPrefix(body, "?")
	if body == "" {
		return fallback
	}
	var value int
	if _, err := fmt.Sscanf(body, "%d", &value); err != nil {
		return fallback
	}
	return value
}

func nativeLiveAreaTestCursorPosition(body string) (int, int) {
	var row int
	var col int
	if _, err := fmt.Sscanf(body, "%d;%d", &row, &col); err != nil {
		return 0, 0
	}
	return max(col-1, 0), max(row-1, 0)
}

func nativeLiveAreaTestMargins(body string, height int) (int, int) {
	if body == "" {
		return 0, height - 1
	}
	var top int
	var bottom int
	if _, err := fmt.Sscanf(body, "%d;%d", &top, &bottom); err != nil {
		return 0, height - 1
	}
	top = max(top-1, 0)
	bottom = max(bottom-1, 0)
	if bottom >= height {
		bottom = height - 1
	}
	if top > bottom {
		return 0, height - 1
	}
	return top, bottom
}
