package input

import (
	"testing"

	"github.com/rivo/uniseg"
)

func TestFieldRenderReturnsWidthSafeLinesAndCursor(t *testing.T) {
	field := NewField()
	field.Prefix = "› "
	field.Editor.Replace("alpha beta gamma")

	result := field.Render(8)
	if result.Width != 8 {
		t.Fatalf("width = %d, want 8", result.Width)
	}
	if len(result.Lines) != 3 {
		t.Fatalf("lines = %+v, want 3 lines", result.Lines)
	}
	for index, line := range result.Lines {
		if got := uniseg.StringWidth(line); got != 8 {
			t.Fatalf("line %d width = %d, want 8: %q", index, got, line)
		}
	}
	if !result.Cursor.Visible {
		t.Fatal("expected cursor visible")
	}
	if result.Cursor.Row != 2 || result.Cursor.Col != 2 {
		t.Fatalf("cursor = %+v, want row 2 col 2", result.Cursor)
	}
}

func TestFieldRenderTracksCursorViewport(t *testing.T) {
	field := NewField()
	field.Prefix = "› "
	field.MaxLines = 2
	field.Editor.Replace("one two three four")

	result := field.Render(8)
	if len(result.Lines) != 2 {
		t.Fatalf("lines = %+v, want 2 visible lines", result.Lines)
	}
	if result.Cursor.Row != 1 {
		t.Fatalf("cursor row = %d, want last visible row", result.Cursor.Row)
	}
	if result.Lines[0] == "› one   " {
		t.Fatalf("expected viewport to follow cursor, got %+v", result.Lines)
	}
}

func TestFieldRenderFrameOffsetsCursor(t *testing.T) {
	field := NewField()
	field.Framed = true
	field.Prefix = "› "
	field.Editor.Replace("hello")

	result := field.Render(10)
	if len(result.Lines) != 3 {
		t.Fatalf("framed lines = %+v, want 3", result.Lines)
	}
	if result.Cursor.Row != 1 || result.Cursor.Col != uniseg.StringWidth("› hello") {
		t.Fatalf("framed cursor = %+v", result.Cursor)
	}
}

func TestFieldRenderMasksTextAndPreservesCursor(t *testing.T) {
	field := NewField()
	field.Prefix = "key: "
	field.Mask = '*'
	field.Editor.Replace("secret")

	result := field.Render(12)
	if got := result.Lines[0]; got != "key: ****** " {
		t.Fatalf("masked line = %q", got)
	}
	if result.Cursor.Col != len("key: ******") {
		t.Fatalf("cursor col = %d", result.Cursor.Col)
	}
}

func TestFieldRenderMasksGraphemesAndMapsCursorToMaskedText(t *testing.T) {
	field := NewField()
	field.Prefix = "key: "
	field.Mask = '*'
	field.Editor.Replace("a👍e\u0301")
	field.Editor.SetCursor(len("a👍"))

	result := field.Render(10)
	if got := result.Lines[0]; got != "key: ***  " {
		t.Fatalf("masked line = %q", got)
	}
	if result.Cursor.Col != len("key: **") {
		t.Fatalf("cursor col = %d", result.Cursor.Col)
	}
}

func TestFieldRenderPlaceholderDoesNotMoveCursorPastPrompt(t *testing.T) {
	field := NewField()
	field.Prefix = "› "
	field.Placeholder = "type here"

	result := field.Render(12)
	if got := result.Lines[0]; got != "› type here " {
		t.Fatalf("placeholder line = %q", got)
	}
	if result.Cursor.Col != uniseg.StringWidth("› ") {
		t.Fatalf("placeholder cursor col = %d, want %d", result.Cursor.Col, uniseg.StringWidth("› "))
	}
}

func TestFieldVerticalMovementAccountsForPrefixWrapping(t *testing.T) {
	field := NewField()
	field.Prefix = "› "
	field.Editor.Replace("abcdef")

	if !field.MoveUp(4) {
		t.Fatal("expected prefixed field to move up")
	}
	if got, want := field.Editor.Cursor(), len("ab"); got != want {
		t.Fatalf("cursor after move up = %d, want %d", got, want)
	}
}

func TestFieldVerticalMovementAcrossWrappedLines(t *testing.T) {
	field := NewField()
	field.Editor.Replace("abcd efgh ijkl")

	if !field.MoveUp(5) {
		t.Fatal("expected move up")
	}
	if pos := field.Editor.CursorPosition(5); pos.Line != 1 || pos.Col != 4 {
		t.Fatalf("after move up cursor position = %+v, want line 1 col 4", pos)
	}
	if !field.MoveUp(5) {
		t.Fatal("expected second move up")
	}
	if pos := field.Editor.CursorPosition(5); pos.Line != 0 || pos.Col != 4 {
		t.Fatalf("after second move up cursor position = %+v, want line 0 col 4", pos)
	}
	if !field.MoveDown(5) {
		t.Fatal("expected move down")
	}
	if pos := field.Editor.CursorPosition(5); pos.Line != 1 || pos.Col != 4 {
		t.Fatalf("after move down cursor position = %+v, want line 1 col 4", pos)
	}
}

func TestFieldMaskedUnicodeVerticalMovementMapsDisplayToSourceCursor(t *testing.T) {
	field := NewField()
	field.Mask = '*'
	field.Editor.Replace("界界界")

	if !field.MoveUp(1) {
		t.Fatal("expected masked field to move up")
	}
	if got, want := field.Editor.Cursor(), len("界界"); got != want {
		t.Fatalf("cursor after move up = %d, want source byte offset %d", got, want)
	}
}
