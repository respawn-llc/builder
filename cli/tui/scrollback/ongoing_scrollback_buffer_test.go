package scrollback

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	xansi "github.com/charmbracelet/x/ansi"
)

func TestOngoingScrollbackBufferSteerWritesExactLine(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, &out, nil)
	defer buffer.close()

	if err := buffer.Steer("stable line"); err != nil {
		t.Fatalf("steer returned error: %v", err)
	}

	if got, want := out.String(), "stable line"+terminalLineBreak; got != want {
		t.Fatalf("stable output = %q, want %q", got, want)
	}
}

func TestOngoingScrollbackBufferSteerWaitsForStreamingAndFlushesFIFO(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, &out, nil)
	defer buffer.close()

	if err := buffer.StreamMarkdownAssistantContent("stream"); err != nil {
		t.Fatalf("stream returned error: %v", err)
	}

	firstErr := make(chan error, 1)
	secondErr := make(chan error, 1)
	go func() { firstErr <- buffer.Steer(" first") }()
	waitForQueuedSteers(t, buffer, 1)
	go func() { secondErr <- buffer.Steer(" second") }()
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

	if got, want := out.String(), "stream"+terminalLineBreak+" first"+terminalLineBreak+" second"+terminalLineBreak; got != want {
		t.Fatalf("stable output = %q, want %q", got, want)
	}
}

func TestOngoingScrollbackBufferFinishTerminatesOpenAssistantLine(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, &out, nil)
	defer buffer.close()

	if err := buffer.StreamMarkdownAssistantContent("done"); err != nil {
		t.Fatalf("stream returned error: %v", err)
	}
	if err := buffer.FinishAssistantStreaming(); err != nil {
		t.Fatalf("finish returned error: %v", err)
	}

	if got, want := out.String(), "done"+terminalLineBreak; got != want {
		t.Fatalf("stable output = %q, want %q", got, want)
	}
}

func TestOngoingScrollbackBufferAssistantStreamNormalizesLineBreaksForTerminal(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, &out, nil)
	defer buffer.close()

	if err := buffer.StreamMarkdownAssistantContent("one\ntwo\r\nthree"); err != nil {
		t.Fatalf("stream returned error: %v", err)
	}

	if got, want := out.String(), "one"+terminalLineBreak+"two"+terminalLineBreak+"three"; got != want {
		t.Fatalf("stable output = %q, want %q", got, want)
	}
}

func TestOngoingScrollbackBufferTurnEndedWithoutFinishPanicsOnNextStream(t *testing.T) {
	var out bytes.Buffer
	turnEnded := make(chan struct{}, 1)
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, &out, turnEnded)
	defer buffer.close()

	if err := buffer.StreamMarkdownAssistantContent("stream"); err != nil {
		t.Fatalf("stream returned error: %v", err)
	}
	turnEnded <- struct{}{}
	waitForTurnEnded(t, buffer)

	panicText := capturePanicText(t, func() {
		_ = buffer.StreamMarkdownAssistantContent("after")
	})
	assertPanicContains(t, panicText, "streamMarkdownAssistantContent")
	assertPanicContains(t, panicText, "assistant stream continued after model turn ended before finishAssistantStreaming")
	assertPanicContains(t, panicText, "payload_quoted=\"after\"")
	assertPanicContains(t, panicText, "stack:")
}

func TestOngoingScrollbackBufferSteerWidthPanicIncludesDiagnostics(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 3, 24, &out, nil)
	defer buffer.close()

	panicText := capturePanicText(t, func() {
		_ = buffer.Steer("abcd")
	})
	assertPanicContains(t, panicText, "operation=steer")
	assertPanicContains(t, panicText, "line exceeds one visual terminal line")
	assertPanicContains(t, panicText, "terminal_width=3")
	assertPanicContains(t, panicText, "visual_width=4")
	assertPanicContains(t, panicText, "payload_quoted=\"abcd\"")
	assertPanicContains(t, panicText, "payload_raw_hex=61 62 63 64")
	assertPanicContains(t, panicText, "stack:")
}

func TestOngoingScrollbackBufferSteerNewlinePanics(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, &out, nil)
	defer buffer.close()

	panicText := capturePanicText(t, func() {
		_ = buffer.Steer("line\n")
	})
	assertPanicContains(t, panicText, "line contains CR or LF")
}

func TestOngoingScrollbackBufferFinishWithoutStreamingPanics(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, &out, nil)
	defer buffer.close()

	panicText := capturePanicText(t, func() {
		_ = buffer.FinishAssistantStreaming()
	})
	assertPanicContains(t, panicText, "finishAssistantStreaming called without an active assistant stream")
}

func TestOngoingScrollbackBufferWriteFailuresReturnErrors(t *testing.T) {
	writeErr := errors.New("terminal closed")
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, failingWriter{err: writeErr}, nil)
	defer buffer.close()

	err := buffer.Steer("line")
	if !errors.Is(err, writeErr) {
		t.Fatalf("steer error = %v, want %v", err, writeErr)
	}

	err = buffer.StreamMarkdownAssistantContent("chunk")
	if !errors.Is(err, writeErr) {
		t.Fatalf("stream error = %v, want %v", err, writeErr)
	}
}

func TestOngoingScrollbackBufferFailedFirstStreamDoesNotKeepStreamingState(t *testing.T) {
	writeErr := errors.New("terminal closed")
	writer := &scriptedWriter{errors: []error{writeErr, nil}}
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, writer, nil)
	defer buffer.close()

	if err := buffer.StreamMarkdownAssistantContent("chunk"); !errors.Is(err, writeErr) {
		t.Fatalf("stream error = %v, want %v", err, writeErr)
	}
	if err := buffer.Steer("stable"); err != nil {
		t.Fatalf("steer after failed first stream returned error: %v", err)
	}

	if got, want := strings.Join(writer.Writes(), "|"), "stable"+terminalLineBreak; got != want {
		t.Fatalf("successful writes = %q, want %q", got, want)
	}
}

func TestOngoingScrollbackBufferQueuedFlushKeepsAttemptingAfterWriteFailure(t *testing.T) {
	writeErr := errors.New("first queued write failed")
	writer := &scriptedWriter{errors: []error{nil, nil, writeErr, nil}}
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, writer, nil)
	defer buffer.close()

	if err := buffer.StreamMarkdownAssistantContent("stream"); err != nil {
		t.Fatalf("stream returned error: %v", err)
	}
	firstErr := make(chan error, 1)
	secondErr := make(chan error, 1)
	go func() { firstErr <- buffer.Steer(" first") }()
	waitForQueuedSteers(t, buffer, 1)
	go func() { secondErr <- buffer.Steer(" second") }()
	waitForQueuedSteers(t, buffer, 2)

	if err := <-firstErr; err != nil {
		t.Fatalf("first queued steer returned error before flush: %v", err)
	}
	if err := <-secondErr; err != nil {
		t.Fatalf("second queued steer returned error before flush: %v", err)
	}
	err := buffer.FinishAssistantStreaming()
	if !errors.Is(err, writeErr) {
		t.Fatalf("finish error = %v, want %v", err, writeErr)
	}

	if got, want := strings.Join(writer.Writes(), "|"), "stream|"+terminalLineBreak+"| second"+terminalLineBreak; got != want {
		t.Fatalf("successful writes = %q, want %q", got, want)
	}
}

func TestOngoingScrollbackBufferShortWritesReturnErrors(t *testing.T) {
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, shortWriter{}, nil)
	defer buffer.close()

	err := buffer.Steer("line")
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("steer error = %v, want %v", err, io.ErrShortWrite)
	}
}

func TestOngoingScrollbackBufferCloseDropsQueuedSteers(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, &out, nil)

	if err := buffer.StreamMarkdownAssistantContent("stream"); err != nil {
		t.Fatalf("stream returned error: %v", err)
	}
	if err := buffer.Steer("queued"); err != nil {
		t.Fatalf("queued steer returned error before close: %v", err)
	}
	waitForQueuedSteers(t, buffer, 1)

	buffer.close()

	if got, want := out.String(), "stream"; got != want {
		t.Fatalf("close should not flush queued steer, output = %q, want %q", got, want)
	}
}

func TestOngoingScrollbackBufferHoldoffBuffersAssistantStreamingAndQueuedSteers(t *testing.T) {
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

	if err := buffer.StreamMarkdownAssistantContent("he"); err != nil {
		t.Fatalf("stream he returned error: %v", err)
	}
	if err := buffer.Steer(" queued"); err != nil {
		t.Fatalf("queued steer returned error: %v", err)
	}
	if err := buffer.StreamMarkdownAssistantContent("llo"); err != nil {
		t.Fatalf("stream llo returned error: %v", err)
	}
	if err := buffer.FinishAssistantStreaming(); err != nil {
		t.Fatalf("finish returned error: %v", err)
	}
	if got := out.String(); got != "" {
		t.Fatalf("holdoff wrote while normal buffer unavailable: %q", got)
	}

	available = true
	if err := buffer.flushHoldoff(); err != nil {
		t.Fatalf("flush holdoff returned error: %v", err)
	}
	if got, want := out.String(), "hello"+terminalLineBreak+" queued"+terminalLineBreak; got != want {
		t.Fatalf("held output = %q, want %q", got, want)
	}
}

func TestOngoingScrollbackBufferHoldoffFlushReportsDelayedErrorsToListener(t *testing.T) {
	writeErr := errors.New("held write failed")
	writer := &scriptedWriter{errors: []error{writeErr, nil}}
	available := false
	var delayed []error
	buffer := NewOngoingScrollbackBufferImpl(
		context.Background(),
		80,
		24,
		writer,
		nil,
		WithNormalBufferAvailability(func() bool { return available }),
		WithDelayedWriteErrorListener(func(err error) {
			delayed = append(delayed, err)
		}),
	)
	defer buffer.close()

	if err := buffer.Steer("first"); err != nil {
		t.Fatalf("held first steer returned error: %v", err)
	}
	if err := buffer.Steer("second"); err != nil {
		t.Fatalf("held second steer returned error: %v", err)
	}

	available = true
	err := buffer.flushHoldoff()
	if !errors.Is(err, writeErr) {
		t.Fatalf("flush error = %v, want %v", err, writeErr)
	}
	if len(delayed) != 1 || !errors.Is(delayed[0], writeErr) {
		t.Fatalf("delayed listener errors = %v, want %v", delayed, writeErr)
	}
	if got, want := strings.Join(writer.Writes(), "|"), "second"+terminalLineBreak; got != want {
		t.Fatalf("successful held writes = %q, want %q", got, want)
	}
}

func TestOngoingScrollbackBufferHoldoffFlushRendersLatestPendingLiveFrame(t *testing.T) {
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
	liveArea := NewNativeLiveAreaImpl(buffer, 80, 24)

	if err := liveArea.Render(nativeLiveAreaFrame("old live")); err != nil {
		t.Fatalf("old live render returned error: %v", err)
	}
	if err := buffer.Steer("held stable"); err != nil {
		t.Fatalf("held steer returned error: %v", err)
	}
	if err := liveArea.Render(nativeLiveAreaFrame("latest live")); err != nil {
		t.Fatalf("latest live render returned error: %v", err)
	}
	if got := out.String(); got != "" {
		t.Fatalf("holdoff wrote before normal buffer was available: %q", got)
	}

	available = true
	if err := buffer.flushHoldoff(); err != nil {
		t.Fatalf("flush holdoff returned error: %v", err)
	}

	if got, want := out.String(), "held stable"+terminalLineBreak+"latest live"+xansi.HideCursor; got != want {
		t.Fatalf("held stable/latest live output = %q, want %q", got, want)
	}
}

func TestOngoingScrollbackBufferDelayedFlushFailureDoesNotDropCurrentSteer(t *testing.T) {
	writeErr := errors.New("held write failed")
	writer := &scriptedWriter{errors: []error{writeErr, nil}}
	available := false
	var delayed []error
	buffer := NewOngoingScrollbackBufferImpl(
		context.Background(),
		80,
		24,
		writer,
		nil,
		WithNormalBufferAvailability(func() bool { return available }),
		WithDelayedWriteErrorListener(func(err error) {
			delayed = append(delayed, err)
		}),
	)
	defer buffer.close()

	if err := buffer.Steer("held"); err != nil {
		t.Fatalf("held steer returned error: %v", err)
	}

	available = true
	if err := buffer.Steer("current"); err != nil {
		t.Fatalf("current steer returned error: %v", err)
	}

	if len(delayed) != 1 || !errors.Is(delayed[0], writeErr) {
		t.Fatalf("delayed listener errors = %v, want %v", delayed, writeErr)
	}
	if got, want := strings.Join(writer.Writes(), "|"), "current"+terminalLineBreak; got != want {
		t.Fatalf("successful writes = %q, want %q", got, want)
	}
}

func TestOngoingScrollbackBufferClosedWritesReturnErrors(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, &out, nil)
	buffer.close()

	if err := buffer.Steer("line"); !errors.Is(err, errOngoingScrollbackBufferClosed) {
		t.Fatalf("steer after close error = %v, want closed buffer error", err)
	}
	if err := buffer.StreamMarkdownAssistantContent("chunk"); !errors.Is(err, errOngoingScrollbackBufferClosed) {
		t.Fatalf("stream after close error = %v, want closed buffer error", err)
	}
	if err := buffer.FinishAssistantStreaming(); !errors.Is(err, errOngoingScrollbackBufferClosed) {
		t.Fatalf("finish after close error = %v, want closed buffer error", err)
	}
}

func TestOngoingScrollbackBufferConstructorPanicsForInvalidDimensions(t *testing.T) {
	panicText := capturePanicText(t, func() {
		_ = NewOngoingScrollbackBufferImpl(context.Background(), 0, 24, io.Discard, nil)
	})
	assertPanicContains(t, panicText, "terminal dimensions must be positive")
	assertPanicContains(t, panicText, "terminal_width=0")
}

func waitForQueuedSteers(t *testing.T, buffer *OngoingScrollbackBufferImpl, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		buffer.mu.Lock()
		got := len(buffer.queuedSteers)
		buffer.mu.Unlock()
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	buffer.mu.Lock()
	got := len(buffer.queuedSteers)
	buffer.mu.Unlock()
	t.Fatalf("queued steers = %d, want %d", got, want)
}

func waitForTurnEnded(t *testing.T, buffer *OngoingScrollbackBufferImpl) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if buffer.turnEndedDuringActiveFlow.Load() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("turn-ended marker was not set")
}

func capturePanicText(t *testing.T, fn func()) (panicText string) {
	t.Helper()
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("expected panic")
		}
		panicText = recovered.(string)
	}()
	fn()
	return ""
}

func assertPanicContains(t *testing.T, panicText string, want string) {
	t.Helper()
	if !strings.Contains(panicText, want) {
		t.Fatalf("panic text missing %q:\n%s", want, panicText)
	}
}

type failingWriter struct {
	err error
}

func (w failingWriter) Write([]byte) (int, error) {
	return 0, w.err
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) {
	return len(p) - 1, nil
}

type scriptedWriter struct {
	errors []error
	writes []string
}

func (w *scriptedWriter) Write(p []byte) (int, error) {
	if len(w.errors) > 0 {
		err := w.errors[0]
		w.errors = w.errors[1:]
		if err != nil {
			return 0, err
		}
	}
	w.writes = append(w.writes, string(p))
	return len(p), nil
}

func (w *scriptedWriter) Writes() []string {
	return append([]string(nil), w.writes...)
}
