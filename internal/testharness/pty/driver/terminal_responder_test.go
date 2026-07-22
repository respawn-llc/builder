package driver

import (
	"bytes"
	"testing"
)

func TestTerminalResponderAnswersFragmentedCapabilityQueries(t *testing.T) {
	t.Parallel()

	var responder terminalResponder
	var replies [][]byte
	for _, payload := range [][]byte{
		[]byte("\x1b]11;"),
		[]byte("?\x1b"),
		[]byte("\\\x1b["),
		[]byte("6n"),
	} {
		replies = append(replies, responder.Feed(payload)...)
	}
	if len(replies) != 2 {
		t.Fatalf("reply count = %d, want 2", len(replies))
	}
	if !bytes.Equal(replies[0], []byte("\x1b]11;rgb:0000/0000/0000\x1b\\")) {
		t.Fatalf("background reply = %q", replies[0])
	}
	if !bytes.Equal(replies[1], []byte("\x1b[1;1R")) {
		t.Fatalf("cursor reply = %q", replies[1])
	}
}

func TestTerminalResponderIgnoresUnsupportedControlSequences(t *testing.T) {
	t.Parallel()

	var responder terminalResponder
	if replies := responder.Feed([]byte("\x1b[5n\x1b]10;?\a")); len(replies) != 0 {
		t.Fatalf("replies = %#v, want none", replies)
	}
}
