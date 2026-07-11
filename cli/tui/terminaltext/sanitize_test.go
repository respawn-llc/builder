package terminaltext

import "testing"

func TestPlainAndSingleLineDropControls(t *testing.T) {
	source := "one\t\atwo\r\nthree"
	if got, want := Plain(source), "one two\nthree"; got != want {
		t.Fatalf("Plain()=%q want %q", got, want)
	}
	if got, want := SingleLine(source), "one two three"; got != want {
		t.Fatalf("SingleLine()=%q want %q", got, want)
	}
}

func TestPrintableLinesPreservesOnlyPrintableGraphemesAndLines(t *testing.T) {
	source := "ok\a\r\b\n👩‍💻\xff"
	if got, want := PrintableLines(source), "ok\n👩‍💻"; got != want {
		t.Fatalf("PrintableLines()=%q want %q", got, want)
	}
}
