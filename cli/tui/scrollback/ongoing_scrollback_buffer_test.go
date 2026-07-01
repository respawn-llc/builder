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

func TestOngoingScrollbackBufferDiscardAssistantStreamingDropsMutableStreamAndFlushesQueuedSteers(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, &out, nil, WithAssistantStreamPromotion(false))
	defer buffer.close()

	if err := buffer.StreamMarkdownAssistantContent("mutable only"); err != nil {
		t.Fatalf("stream returned error: %v", err)
	}
	queuedErr := make(chan error, 1)
	go func() { queuedErr <- buffer.Steer("committed tool") }()
	waitForQueuedSteers(t, buffer, 1)

	if err := buffer.DiscardAssistantStreaming(); err != nil {
		t.Fatalf("discard returned error: %v", err)
	}
	if err := <-queuedErr; err != nil {
		t.Fatalf("queued steer returned error: %v", err)
	}

	if buffer.AssistantStreaming() {
		t.Fatal("assistant stream still active after discard")
	}
	if got := buffer.AssistantStreamTailLines(); len(got) != 0 {
		t.Fatalf("assistant tail after discard = %q, want empty", got)
	}
	if got, want := out.String(), "committed tool"+terminalLineBreak; got != want {
		t.Fatalf("stable output = %q, want %q", got, want)
	}
}

func nonEmptyAssistantTestRows(rows []string) []string {
	filtered := make([]string, 0, len(rows))
	for _, row := range rows {
		if row != "" {
			filtered = append(filtered, row)
		}
	}
	return filtered
}

func styledAssistantTestRows(source string, firstRow string) []string {
	rows := nonEmptyAssistantTestRows(strings.Split(strings.TrimSuffix(source, "\n"), "\n"))
	if len(rows) > 0 {
		rows[0] = firstRow
	}
	return rows
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
	if err := buffer.FinishAssistantStreaming(); err != nil {
		t.Fatalf("finish returned error: %v", err)
	}

	if got, want := out.String(), "one"+terminalLineBreak+"two"+terminalLineBreak+"three"+terminalLineBreak; got != want {
		t.Fatalf("stable output = %q, want %q", got, want)
	}
}

func TestOngoingScrollbackBufferAssistantStreamPromotesRenderedRows(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(
		context.Background(),
		80,
		24,
		&out,
		nil,
		WithAssistantMarkdownRenderer(func(source string, width int) []string {
			return nonEmptyAssistantTestRows(strings.Split(strings.TrimSuffix(strings.ReplaceAll(source, "**", ""), "\n"), "\n"))
		}),
	)
	defer buffer.close()
	defer buffer.close()

	if err := buffer.StreamMarkdownAssistantContent("**bold**\n\n"); err != nil {
		t.Fatalf("stream first row returned error: %v", err)
	}
	if got := out.String(); got != "" {
		t.Fatalf("completed markdown block promoted before following block started: %q", got)
	}
	if err := buffer.StreamMarkdownAssistantContent("next\n"); err != nil {
		t.Fatalf("stream second row returned error: %v", err)
	}
	if got := out.String(); strings.Contains(got, "**") {
		t.Fatalf("stable output promoted raw markdown markers: %q", got)
	}
	if got, want := out.String(), "bold"+terminalLineBreak; got != want {
		t.Fatalf("stable output = %q, want %q", got, want)
	}
	if got, want := strings.Join(buffer.AssistantStreamTailLines(), ""), "next"; got != want {
		t.Fatalf("tail after promoted first row = %q, want %q", got, want)
	}
}

func TestOngoingScrollbackBufferAssistantStreamHoldsReferenceSensitiveMarkdownBlock(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(
		context.Background(),
		80,
		24,
		&out,
		nil,
		WithAssistantMarkdownRenderer(func(source string, width int) []string {
			if strings.Contains(source, "[ref]:") {
				return nonEmptyAssistantTestRows(strings.Split(strings.TrimSuffix(strings.ReplaceAll(source, "[label][ref]", "linked label"), "\n"), "\n"))
			}
			return nonEmptyAssistantTestRows(strings.Split(strings.TrimSuffix(source, "\n"), "\n"))
		}),
	)
	defer buffer.close()

	if err := buffer.StreamMarkdownAssistantContent("[label][ref]\n\nnext"); err != nil {
		t.Fatalf("stream reference paragraph returned error: %v", err)
	}
	if got := out.String(); got != "" {
		t.Fatalf("reference-sensitive paragraph promoted before possible reference definition: %q", got)
	}
	if err := buffer.StreamMarkdownAssistantContent("\n[ref]: https://example.test\n"); err != nil {
		t.Fatalf("stream reference definition returned error: %v", err)
	}
	if got := out.String(); got != "" {
		t.Fatalf("reference-sensitive paragraph promoted before finish: %q", got)
	}
}

func TestOngoingScrollbackBufferAssistantStreamPromotesClosedFenceFollowedByBlankLine(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(
		context.Background(),
		80,
		24,
		&out,
		nil,
		WithAssistantMarkdownRenderer(func(source string, width int) []string {
			return nonEmptyAssistantTestRows(strings.Split(strings.TrimSuffix(source, "\n"), "\n"))
		}),
	)
	defer buffer.close()

	if err := buffer.StreamMarkdownAssistantContent("```go\nline\n```\n\n"); err != nil {
		t.Fatalf("stream closed fence returned error: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "line"+terminalLineBreak) {
		t.Fatalf("closed fenced code block did not promote at tail: %q", got)
	}
}

func TestOngoingScrollbackBufferAssistantStreamHoldsReferenceBlockBeforeClosedFence(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(
		context.Background(),
		80,
		24,
		&out,
		nil,
		WithAssistantMarkdownRenderer(func(source string, width int) []string {
			return nonEmptyAssistantTestRows(strings.Split(strings.TrimSuffix(source, "\n"), "\n"))
		}),
	)
	defer buffer.close()

	if err := buffer.StreamMarkdownAssistantContent("[label][ref]\n\n```go\nline\n```\n\n"); err != nil {
		t.Fatalf("stream reference paragraph and closed fence returned error: %v", err)
	}
	if got := out.String(); got != "" {
		t.Fatalf("closed fence promoted earlier reference-sensitive block: %q", got)
	}
}

func TestOngoingScrollbackBufferAssistantStreamHoldsReferenceLineImmediatelyBeforeClosedFence(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(
		context.Background(),
		80,
		24,
		&out,
		nil,
		WithAssistantMarkdownRenderer(func(source string, width int) []string {
			return nonEmptyAssistantTestRows(strings.Split(strings.TrimSuffix(source, "\n"), "\n"))
		}),
	)
	defer buffer.close()

	if err := buffer.StreamMarkdownAssistantContent("[label][ref]\n```go\nline\n```\n\n"); err != nil {
		t.Fatalf("stream reference line and immediately closed fence returned error: %v", err)
	}
	if got := out.String(); got != "" {
		t.Fatalf("closed fence promoted earlier reference-sensitive line: %q", got)
	}
}

func TestOngoingScrollbackBufferAssistantStreamHoldsReferenceBlockBeforeActiveTable(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(
		context.Background(),
		80,
		24,
		&out,
		nil,
		WithAssistantMarkdownRenderer(func(source string, width int) []string {
			return nonEmptyAssistantTestRows(strings.Split(strings.TrimSuffix(source, "\n"), "\n"))
		}),
	)
	defer buffer.close()

	if err := buffer.StreamMarkdownAssistantContent("[label][ref]\n\n| a |\n"); err != nil {
		t.Fatalf("stream reference paragraph and active table returned error: %v", err)
	}
	if got := out.String(); got != "" {
		t.Fatalf("active table holdback promoted earlier reference-sensitive block: %q", got)
	}
}

func TestOngoingScrollbackBufferAssistantStreamAllowsEquivalentANSIChurn(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(
		context.Background(),
		80,
		24,
		&out,
		nil,
		WithAssistantMarkdownRenderer(func(source string, width int) []string {
			if strings.Contains(source, "third") {
				return styledAssistantTestRows(source, "\x1b[0;1mrow\x1b[0m")
			}
			return styledAssistantTestRows(source, "\x1b[1mrow\x1b[0m")
		}),
	)

	if err := buffer.StreamMarkdownAssistantContent("row\n\nnext\n"); err != nil {
		t.Fatalf("stream first promotion returned error: %v", err)
	}
	if err := buffer.StreamMarkdownAssistantContent("third\n"); err != nil {
		t.Fatalf("equivalent ANSI churn changed promoted-row key: %v", err)
	}
}

func TestOngoingScrollbackBufferAssistantStreamPanicsWhenPromotedRowStyleChanges(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(
		context.Background(),
		80,
		24,
		&out,
		nil,
		WithAssistantMarkdownRenderer(func(source string, width int) []string {
			if strings.Contains(source, "third") {
				return styledAssistantTestRows(source, "\x1b[1mrow\x1b[0m")
			}
			return styledAssistantTestRows(source, "row")
		}),
	)

	if err := buffer.StreamMarkdownAssistantContent("row\n\nnext\n"); err != nil {
		t.Fatalf("stream first promotion returned error: %v", err)
	}
	panicText := capturePanicText(t, func() {
		_ = buffer.StreamMarkdownAssistantContent("third\n")
	})
	assertPanicContains(t, panicText, "assistant renderer changed an already-promoted stable row")
}

func TestOngoingScrollbackBufferAssistantStreamPanicsWhenPromotedRowFinalPenChanges(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(
		context.Background(),
		80,
		24,
		&out,
		nil,
		WithAssistantMarkdownRenderer(func(source string, width int) []string {
			if strings.Contains(source, "third") {
				return styledAssistantTestRows(source, "row\x1b[31m")
			}
			return styledAssistantTestRows(source, "row\x1b[0m")
		}),
	)

	if err := buffer.StreamMarkdownAssistantContent("row\n\nnext\n"); err != nil {
		t.Fatalf("stream first promotion returned error: %v", err)
	}
	panicText := capturePanicText(t, func() {
		_ = buffer.StreamMarkdownAssistantContent("third\n")
	})
	assertPanicContains(t, panicText, "assistant renderer changed an already-promoted stable row")
}

func TestOngoingScrollbackBufferAssistantStreamPipeHoldbackDoesNotUnpromoteRows(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, &out, nil)
	defer buffer.close()

	if err := buffer.StreamMarkdownAssistantContent("first\nsecond\n"); err != nil {
		t.Fatalf("stream initial rows returned error: %v", err)
	}
	if got, want := out.String(), "first"+terminalLineBreak; got != want {
		t.Fatalf("stable output after initial rows = %q, want %q", got, want)
	}
	if err := buffer.StreamMarkdownAssistantContent("uses | separator\n"); err != nil {
		t.Fatalf("stream pipe row returned error: %v", err)
	}
	if got, want := out.String(), "first"+terminalLineBreak; got != want {
		t.Fatalf("pipe holdback changed stable output = %q, want %q", got, want)
	}
	tail := strings.Join(buffer.AssistantStreamTailLines(), "\n")
	if !strings.Contains(tail, "second") || !strings.Contains(tail, "uses | separator") {
		t.Fatalf("pipe holdback tail skipped unpromoted rows: %q", tail)
	}
}

func TestOngoingScrollbackBufferAssistantStreamFenceHoldbackDoesNotUnpromoteRows(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, &out, nil)
	defer buffer.close()

	if err := buffer.StreamMarkdownAssistantContent("first\nsecond\n"); err != nil {
		t.Fatalf("stream initial rows returned error: %v", err)
	}
	if got, want := out.String(), "first"+terminalLineBreak; got != want {
		t.Fatalf("stable output after initial rows = %q, want %q", got, want)
	}
	if err := buffer.StreamMarkdownAssistantContent("```go\n"); err != nil {
		t.Fatalf("stream fence opener returned error: %v", err)
	}
	if got, want := out.String(), "first"+terminalLineBreak; got != want {
		t.Fatalf("fence holdback changed stable output = %q, want %q", got, want)
	}
	tail := strings.Join(buffer.AssistantStreamTailLines(), "\n")
	if !strings.Contains(tail, "second") || !strings.Contains(tail, "```go") {
		t.Fatalf("fence holdback tail skipped unpromoted rows: %q", tail)
	}
}

func TestOngoingScrollbackBufferAssistantStreamPreservesTabOnlyTail(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(
		context.Background(),
		80,
		24,
		&out,
		nil,
		WithAssistantMarkdownRenderer(func(source string, width int) []string {
			return []string{source}
		}),
	)
	defer buffer.close()

	if err := buffer.StreamMarkdownAssistantContent("\t"); err != nil {
		t.Fatalf("stream tab-only row returned error: %v", err)
	}
	if got, want := strings.Join(buffer.AssistantStreamTailLines(), ""), "\t"; got != want {
		t.Fatalf("tab-only tail = %q, want %q", got, want)
	}
}

func TestOngoingScrollbackBufferAssistantStreamSplitsStyledRowsWithoutBreakingEscapes(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 6, 24, &out, nil)
	defer buffer.close()

	if err := buffer.StreamMarkdownAssistantContent("\x1b[31mhello world\x1b[0m"); err != nil {
		t.Fatalf("stream returned error: %v", err)
	}
	if err := buffer.FinishAssistantStreaming(); err != nil {
		t.Fatalf("finish returned error: %v", err)
	}

	rows := strings.Split(strings.TrimSuffix(out.String(), terminalLineBreak), terminalLineBreak)
	if got, want := len(rows), 2; got != want {
		t.Fatalf("row count = %d, want %d: %q", got, want, out.String())
	}
	if got, want := xansi.Strip(rows[0]), "hello "; got != want {
		t.Fatalf("row 0 stripped = %q, want %q; raw output %q", got, want, out.String())
	}
	if got, want := xansi.Strip(rows[1]), "world"; got != want {
		t.Fatalf("row 1 stripped = %q, want %q; raw output %q", got, want, out.String())
	}
	if !strings.Contains(rows[1], "\x1b[31mworld") {
		t.Fatalf("row 1 does not carry active SGR style into tail: %q", rows[1])
	}
	for index, row := range rows {
		if strings.Contains(row, "\x1b[") && !strings.Contains(row, "m") {
			t.Fatalf("row %d split an ANSI control sequence: %q", index, row)
		}
	}
}

func TestOngoingScrollbackBufferAssistantStreamTailPreservesWhitespace(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, &out, nil)
	defer buffer.close()

	if err := buffer.StreamMarkdownAssistantContent("    "); err != nil {
		t.Fatalf("stream returned error: %v", err)
	}

	if got, want := strings.Join(buffer.AssistantStreamTailLines(), "|"), "    "; got != want {
		t.Fatalf("tail lines = %q, want %q", got, want)
	}
}

func TestOngoingScrollbackBufferAssistantStreamDoesNotExposeStyleOnlyTail(t *testing.T) {
	var out bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(
		context.Background(),
		6,
		24,
		&out,
		nil,
		WithAssistantMarkdownRenderer(func(source string, width int) []string {
			return []string{"\x1b[31m"}
		}),
	)
	defer buffer.close()

	if err := buffer.StreamMarkdownAssistantContent("style"); err != nil {
		t.Fatalf("stream returned error: %v", err)
	}
	if got := buffer.AssistantStreamTailLines(); len(got) != 0 {
		t.Fatalf("style-only tail lines = %q, want empty", got)
	}
	if err := buffer.FinishAssistantStreaming(); err != nil {
		t.Fatalf("finish returned error: %v", err)
	}
	if got := out.String(); got != "" {
		t.Fatalf("style-only stream wrote stable output: %q", got)
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

func TestNormalBufferPreparationSequencePreservesCursorAcrossModeResets(t *testing.T) {
	sequence := normalBufferPreparationSequence()
	if !strings.Contains(sequence, xansi.SaveCursor) || !strings.Contains(sequence, xansi.RestoreCursor) {
		t.Fatalf("normal buffer preparation sequence must save/restore cursor, got %q", sequence)
	}
	if strings.Index(sequence, xansi.SaveCursor) > strings.Index(sequence, "\x1b[?6l") ||
		strings.Index(sequence, xansi.RestoreCursor) < strings.Index(sequence, "\x1b[r") {
		t.Fatalf("normal buffer preparation sequence does not wrap mode resets with cursor restore, got %q", sequence)
	}
}

func TestOngoingScrollbackBufferNormalBufferPreparationCanBeInvalidated(t *testing.T) {
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

	if err := buffer.Steer("first stable"); err != nil {
		t.Fatalf("first steer returned error: %v", err)
	}
	out.Reset()
	buffer.invalidateNormalBufferPreparation()
	if err := buffer.Steer("second stable"); err != nil {
		t.Fatalf("second steer returned error: %v", err)
	}

	if got, want := out.String(), normalBufferPreparationSequence()+"second stable"+terminalLineBreak; got != want {
		t.Fatalf("reprepared output = %q, want %q", got, want)
	}
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

	err = buffer.StreamMarkdownAssistantContent(strings.Repeat("x", 81) + "\nnext\n")
	if !errors.Is(err, writeErr) {
		t.Fatalf("stream error = %v, want %v", err, writeErr)
	}
}

func TestOngoingScrollbackBufferFailedFirstStreamDoesNotKeepStreamingState(t *testing.T) {
	writeErr := errors.New("terminal closed")
	writer := &scriptedWriter{errors: []error{writeErr, nil}}
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, writer, nil)
	defer buffer.close()

	if err := buffer.StreamMarkdownAssistantContent(strings.Repeat("x", 81) + "\nnext\n"); !errors.Is(err, writeErr) {
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
	writer := &scriptedWriter{errors: []error{writeErr, nil, nil}}
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

	if got, want := strings.Join(writer.Writes(), "|"), " first"+terminalLineBreak+"| second"+terminalLineBreak; got != want {
		t.Fatalf("successful writes = %q, want %q", got, want)
	}
}

func TestOngoingScrollbackBufferQueuedWriteFailureDoesNotPoisonNextStream(t *testing.T) {
	writeErr := errors.New("queued write failed")
	writer := &scriptedWriter{errors: []error{nil, writeErr, nil}}
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, writer, nil)
	defer buffer.close()

	if err := buffer.StreamMarkdownAssistantContent("done"); err != nil {
		t.Fatalf("stream returned error: %v", err)
	}
	firstQueuedErr := make(chan error, 1)
	go func() { firstQueuedErr <- buffer.Steer(" queued") }()
	waitForQueuedSteers(t, buffer, 1)
	if err := <-firstQueuedErr; err != nil {
		t.Fatalf("queued steer returned error before flush: %v", err)
	}

	err := buffer.FinishAssistantStreaming()
	if !errors.Is(err, writeErr) {
		t.Fatalf("finish error = %v, want %v", err, writeErr)
	}
	if err := buffer.StreamMarkdownAssistantContent("next"); err != nil {
		t.Fatalf("second stream returned error: %v", err)
	}
	if err := buffer.FinishAssistantStreaming(); err != nil {
		t.Fatalf("second finish returned error: %v", err)
	}

	if got, want := strings.Join(writer.Writes(), "|"), "done"+terminalLineBreak+"|next"+terminalLineBreak; got != want {
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

	if got, want := out.String(), ""; got != want {
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

func TestOngoingScrollbackBufferHoldoffFlushPreparesNormalBufferBeforeHeldWrites(t *testing.T) {
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

	if err := buffer.Steer("held stable"); err != nil {
		t.Fatalf("held steer returned error: %v", err)
	}
	available = true
	if err := buffer.flushHoldoff(); err != nil {
		t.Fatalf("flush holdoff returned error: %v", err)
	}

	if got, want := out.String(), normalBufferPreparationSequence()+"held stable"+terminalLineBreak; got != want {
		t.Fatalf("held prepared output = %q, want %q", got, want)
	}
}

func TestOngoingScrollbackBufferHeldStreamFlushDoesNotInterleaveLiveFrameBeforeTail(t *testing.T) {
	var out bytes.Buffer
	available := true
	buffer := NewOngoingScrollbackBufferImpl(
		context.Background(),
		6,
		24,
		&out,
		nil,
		WithNormalBufferAvailability(func() bool { return available }),
	)
	defer buffer.close()
	liveArea := newNativeLiveAreaImpl(buffer, 6, 24)

	if err := liveArea.Render(nativeLiveAreaFrame("input")); err != nil {
		t.Fatalf("initial live render returned error: %v", err)
	}
	available = false
	if err := buffer.StreamMarkdownAssistantContent("hello world"); err != nil {
		t.Fatalf("held stream returned error: %v", err)
	}
	if err := buffer.FinishAssistantStreaming(); err != nil {
		t.Fatalf("held finish returned error: %v", err)
	}

	available = true
	if err := buffer.flushHoldoff(); err != nil {
		t.Fatalf("flush holdoff returned error: %v", err)
	}

	want := nativeLiveAreaRenderSequence(24, nativeLiveAreaFrame("input")) +
		liveAreaErasePhysicalSequence(1, 24) +
		stableOutputInsertRowsSequence(1, 24, "hello ", "world") +
		nativeLiveAreaRenderSequence(24, nativeLiveAreaFrame("input"))
	if got := out.String(); got != want {
		t.Fatalf("held stream output = %q, want %q", got, want)
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
	liveArea := newNativeLiveAreaImpl(buffer, 80, 24)

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

	if got, want := out.String(), "held stable"+terminalLineBreak+nativeLiveAreaRenderSequence(24, nativeLiveAreaFrame("latest live")); got != want {
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
