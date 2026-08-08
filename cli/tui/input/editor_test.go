package input

import "testing"

func TestEditorInsertAndGraphemeMovement(t *testing.T) {
	editor := NewEditor()
	editor.InsertString("a👍e\u0301")
	if got := editor.Text(); got != "a👍e\u0301" {
		t.Fatalf("text = %q", got)
	}
	if !editor.MoveLeft() {
		t.Fatal("expected move left over combining grapheme")
	}
	if got, want := editor.Cursor(), len("a👍"); got != want {
		t.Fatalf("cursor after combining grapheme = %d, want %d", got, want)
	}
	if !editor.MoveLeft() {
		t.Fatal("expected move left over emoji")
	}
	if got, want := editor.Cursor(), len("a"); got != want {
		t.Fatalf("cursor after emoji = %d, want %d", got, want)
	}
	editor.InsertString("b")
	if got := editor.Text(); got != "ab👍e\u0301" {
		t.Fatalf("insert at cursor text = %q", got)
	}
}
