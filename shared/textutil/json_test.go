package textutil

import "testing"

func TestCompactNoHTMLEscapeNormalizesPersistedHTMLEscapes(t *testing.T) {
	raw := []byte(`{"cmd":"git diff --cached \u0026\u0026 git diff","tag":"\u003cok\u003e"}`)
	got := CompactNoHTMLEscape(raw)
	want := `{"cmd":"git diff --cached && git diff","tag":"<ok>"}`
	if got != want {
		t.Fatalf("CompactNoHTMLEscape() = %q, want %q", got, want)
	}
}

func TestCompactNoHTMLEscapePreservesLiteralBackslashUText(t *testing.T) {
	raw := []byte(`{"literal":"\\u0026","cmd":"echo \u0026"}`)
	got := CompactNoHTMLEscape(raw)
	want := `{"literal":"\\u0026","cmd":"echo &"}`
	if got != want {
		t.Fatalf("CompactNoHTMLEscape() = %q, want %q", got, want)
	}
}

func TestCompactNoHTMLEscapePreservesObjectKeyOrder(t *testing.T) {
	raw := []byte(`{"z":1,"a":{"b":"c \u0026 d"},"m":2}`)
	got := CompactNoHTMLEscape(raw)
	want := `{"z":1,"a":{"b":"c & d"},"m":2}`
	if got != want {
		t.Fatalf("CompactNoHTMLEscape() = %q, want %q", got, want)
	}
}
