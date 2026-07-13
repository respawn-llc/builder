package ongoing

import (
	"bytes"
	"strings"
	"testing"

	"core/internal/testharness/pty/analyzer"
)

func TestPTYStableProseUsesTerminalWrapAndResizeDoesNotReplay(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	if _, err := surface.ApplyTerminalMessage(
		committedMessage(userRow("alpha beta gamma delta")),
		FrameInput{Size: Size{Width: 10, Height: 6}},
	); err != nil {
		t.Fatalf("append stable prose: %v", err)
	}

	stream, err := analyzer.NewStream(analyzer.MustDimensions(6, 10))
	if err != nil {
		t.Fatalf("create PTY analyzer: %v", err)
	}
	if err := stream.FeedChunk(analyzer.NewChunk(0, 0, out.Bytes())); err != nil {
		t.Fatalf("feed stable prose: %v", err)
	}
	beforeResize, err := stream.Snapshot()
	if err != nil {
		t.Fatalf("snapshot before resize: %v", err)
	}

	var logical strings.Builder
	var writePositions []analyzer.Position
	for _, transaction := range beforeResize.Operations {
		for _, operation := range analyzer.OperationRecords(transaction) {
			if operation.ChunkIndex != 0 || operation.Write == nil {
				continue
			}
			logical.WriteString(operation.Write.Text())
			writePositions = append(writePositions, operation.Before)
		}
	}
	if got, want := logical.String(), "❯ alpha beta gamma delta"; got != want {
		t.Fatalf("PTY logical write = %q, want %q", got, want)
	}
	softWrapped := false
	for _, position := range writePositions[1:] {
		if position.Col == 0 {
			softWrapped = true
			break
		}
	}
	if !softWrapped {
		t.Fatalf("PTY writes never returned to column zero through terminal soft wrapping: %+v", writePositions)
	}

	out.Reset()
	result, err := surface.Resize(Size{Width: 20, Height: 6}, FrameInput{})
	if err != nil {
		t.Fatalf("resize ongoing surface: %v", err)
	}
	if result.Action != ResultNoop {
		t.Fatalf("resize action = %q, want repaint-only noop", result.Action)
	}
	if err := stream.Resize(analyzer.MustDimensions(6, 20)); err != nil {
		t.Fatalf("resize PTY analyzer: %v", err)
	}
	if err := stream.FeedChunk(analyzer.NewChunk(1, 0, out.Bytes())); err != nil {
		t.Fatalf("feed resize transaction: %v", err)
	}
	afterResize, err := stream.Finish()
	if err != nil {
		t.Fatalf("finish PTY analysis: %v", err)
	}
	for _, transaction := range afterResize.Operations {
		for _, operation := range analyzer.OperationRecords(transaction) {
			if operation.ChunkIndex == 1 && operation.Write != nil {
				t.Fatalf("resize replayed stable history as %q", operation.Write.Text())
			}
		}
	}
}
