package app

import (
	"bytes"
	"strings"
	"testing"
)

func TestInteractiveAuthPromptWritesLabelAndTrimsLineEnding(t *testing.T) {
	var out bytes.Buffer
	interactor := &interactiveAuthInteractor{
		stdin:  strings.NewReader("code-123\r\n"),
		stderr: &out,
	}

	got, err := interactor.prompt("Paste code: ")
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if got != "code-123" {
		t.Fatalf("prompt value = %q", got)
	}
	if out.String() != "Paste code: " {
		t.Fatalf("prompt label = %q", out.String())
	}
}
