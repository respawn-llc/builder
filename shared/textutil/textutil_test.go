package textutil

import (
	"strings"
	"testing"
)

func TestNormalizeCRLF(t *testing.T) {
	if got := strings.ReplaceAll("a\r\nb\r\n", "\r\n", "\n"); got != "a\nb\n" {
		t.Fatalf("unexpected normalization: %q", got)
	}
	if got := strings.ReplaceAll("a\rb", "\r\n", "\n"); got != "a\rb" {
		t.Fatalf("unexpected single-CR normalization: %q", got)
	}
}

func TestSplitLinesCRLF(t *testing.T) {
	got := strings.Split(strings.ReplaceAll("a\r\nb\n", "\r\n", "\n"), "\n")
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "" {
		t.Fatalf("unexpected split result: %#v", got)
	}
}
