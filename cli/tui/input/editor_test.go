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

func TestEditorDeleteBackwardAndForwardUseGraphemeClusters(t *testing.T) {
	editor := NewEditor()
	editor.Replace("a👍e\u0301b")
	editor.SetCursor(len("a👍e\u0301"))
	if !editor.DeleteBackward() {
		t.Fatal("expected delete backward")
	}
	if got := editor.Text(); got != "a👍b" {
		t.Fatalf("delete backward text = %q", got)
	}
	if !editor.DeleteBackward() {
		t.Fatal("expected delete backward emoji")
	}
	if got := editor.Text(); got != "ab" {
		t.Fatalf("delete emoji text = %q", got)
	}
	editor.SetCursor(0)
	if !editor.DeleteForward() {
		t.Fatal("expected delete forward")
	}
	if got := editor.Text(); got != "b" {
		t.Fatalf("delete forward text = %q", got)
	}
}

func TestEditorWordMovementAndDelete(t *testing.T) {
	editor := NewEditor()
	editor.Replace("alpha beta/gamma")
	editor.MoveWordLeft()
	if got, want := editor.Cursor(), len("alpha beta/"); got != want {
		t.Fatalf("move word left cursor = %d, want %d", got, want)
	}
	editor.MoveWordLeft()
	if got, want := editor.Cursor(), len("alpha beta"); got != want {
		t.Fatalf("second move word left cursor = %d, want %d", got, want)
	}
	editor.MoveWordLeft()
	if got, want := editor.Cursor(), len("alpha "); got != want {
		t.Fatalf("third move word left cursor = %d, want %d", got, want)
	}
	editor.DeleteForwardWord()
	if got := editor.Text(); got != "alpha /gamma" {
		t.Fatalf("delete forward word text = %q", got)
	}
	editor.DeleteBackwardWord()
	if got := editor.Text(); got != "/gamma" {
		t.Fatalf("delete backward word text = %q", got)
	}
}

func TestEditorKillAndYank(t *testing.T) {
	editor := NewEditor()
	editor.Replace("one two\nthree")
	editor.SetCursor(len("one "))
	if !editor.KillToLineEnd() {
		t.Fatal("expected kill to line end")
	}
	if got := editor.Text(); got != "one \nthree" {
		t.Fatalf("kill to end text = %q", got)
	}
	editor.SetCursor(len(editor.Text()))
	if !editor.Yank() {
		t.Fatal("expected yank")
	}
	if got := editor.Text(); got != "one \nthreetwo" {
		t.Fatalf("yank text = %q", got)
	}
	if !editor.KillToLineStart() {
		t.Fatal("expected kill to line start")
	}
	if got := editor.Text(); got != "one \n" {
		t.Fatalf("kill to line start text = %q", got)
	}
}

func TestEditorDeleteCurrentLine(t *testing.T) {
	editor := NewEditor()
	editor.Replace("top\ncurrent\nbottom")
	editor.SetCursor(len("top\ncur"))
	if !editor.DeleteCurrentLine() {
		t.Fatal("expected delete current line")
	}
	if got := editor.Text(); got != "top\nbottom" {
		t.Fatalf("text after delete = %q", got)
	}
	if got, want := editor.Cursor(), len("top\n"); got != want {
		t.Fatalf("cursor after delete = %d, want %d", got, want)
	}

	editor.Replace("top\nlast")
	editor.SetCursor(len(editor.Text()))
	if !editor.DeleteCurrentLine() {
		t.Fatal("expected delete final line")
	}
	if got := editor.Text(); got != "top" {
		t.Fatalf("text after final delete = %q", got)
	}
	if got, want := editor.Cursor(), len("top"); got != want {
		t.Fatalf("cursor after final delete = %d, want %d", got, want)
	}
}

func TestEditorSetCursorClampsInsideGrapheme(t *testing.T) {
	editor := NewEditor()
	editor.Replace("e\u0301x")
	editor.SetCursor(len("e"))
	if got := editor.Cursor(); got != 0 && got != len("e\u0301") {
		t.Fatalf("cursor should clamp to grapheme boundary, got %d", got)
	}
}

func TestEditorSnapshotRestoresTextAndCursorWithoutReplacingKillBuffer(t *testing.T) {
	editor := NewEditor()
	editor.Replace("界x")
	editor.SetCursor(len("界"))
	snapshot := editor.Snapshot()

	editor.Replace("temporary")
	editor.SetKillBuffer("retained")
	editor.Restore(snapshot)

	if got, want := editor.Text(), "界x"; got != want {
		t.Fatalf("restored text = %q, want %q", got, want)
	}
	if got, want := editor.Cursor(), len("界"); got != want {
		t.Fatalf("restored cursor = %d, want %d", got, want)
	}
	if got, want := editor.KillBuffer(), "retained"; got != want {
		t.Fatalf("kill buffer = %q, want %q", got, want)
	}
}
