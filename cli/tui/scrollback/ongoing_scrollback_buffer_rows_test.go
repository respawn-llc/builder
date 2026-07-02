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
