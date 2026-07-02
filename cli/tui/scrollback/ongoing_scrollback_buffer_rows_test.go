package scrollback

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestSteerCommitsATerminalRow(t *testing.T) {
	var writer bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, &writer, nil)

	if err := buffer.Steer("committed row"); err != nil {
		t.Fatalf("steer failed: %v", err)
	}

	if !strings.HasSuffix(writer.String(), terminalLineBreak) {
		t.Fatalf("committed row did not advance terminal cursor: %q", writer.String())
	}
}

func TestFinishAssistantStreamingCommitsQueuedSteersAsTerminalRows(t *testing.T) {
	var writer bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, &writer, nil)

	if err := buffer.StreamMarkdownAssistantContent("assistant"); err != nil {
		t.Fatalf("stream assistant content failed: %v", err)
	}
	if err := buffer.Steer("queued committed row"); err != nil {
		t.Fatalf("queue committed row failed: %v", err)
	}
	if err := buffer.FinishAssistantStreaming(); err != nil {
		t.Fatalf("finish assistant streaming failed: %v", err)
	}

	if !strings.HasSuffix(writer.String(), terminalLineBreak) {
		t.Fatalf("queued committed row did not advance terminal cursor on finish: %q", writer.String())
	}
}

func TestDiscardAssistantStreamingCommitsQueuedSteersAsTerminalRows(t *testing.T) {
	var writer bytes.Buffer
	buffer := NewOngoingScrollbackBufferImpl(context.Background(), 80, 24, &writer, nil)

	if err := buffer.StreamMarkdownAssistantContent("assistant"); err != nil {
		t.Fatalf("stream assistant content failed: %v", err)
	}
	if err := buffer.Steer("queued committed row"); err != nil {
		t.Fatalf("queue committed row failed: %v", err)
	}
	if err := buffer.DiscardAssistantStreaming(); err != nil {
		t.Fatalf("discard assistant streaming failed: %v", err)
	}

	if !strings.HasSuffix(writer.String(), terminalLineBreak) {
		t.Fatalf("queued committed row did not advance terminal cursor on discard: %q", writer.String())
	}
}
