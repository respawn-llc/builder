package transcriptrender

import "testing"

func TestTerminalTextSanitizersDropControls(t *testing.T) {
	source := "one\t\atwo\r\nthree"
	if got, want := TerminalSafePlainText(source), "one two\nthree"; got != want {
		t.Fatalf("TerminalSafePlainText()=%q want %q", got, want)
	}
	if got, want := TerminalSafeSingleLine(source), "one two three"; got != want {
		t.Fatalf("TerminalSafeSingleLine()=%q want %q", got, want)
	}
}

func TestTerminalSafePrintableLinesPreservesOnlyPrintableGraphemesAndLines(t *testing.T) {
	source := "ok\a\r\b\n👩‍💻\xff"
	if got, want := TerminalSafePrintableLines(source), "ok\n👩‍💻"; got != want {
		t.Fatalf("TerminalSafePrintableLines()=%q want %q", got, want)
	}
}
