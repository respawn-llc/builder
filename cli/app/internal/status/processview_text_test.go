package status

import "testing"

func TestCompactCommandText(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{name: "empty", text: "", want: commandFallback},
		{name: "single line", text: "  git status  ", want: "git status"},
		{name: "first line", text: "git status\nsecond line", want: "git status"},
		{name: "inline meta", text: "git status\x1fmeta", want: "git status"},
		{name: "blank command with meta", text: "\x1fmeta", want: commandFallback},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CompactCommandText(tc.text); got != tc.want {
				t.Fatalf("CompactCommandText()=%q, want %q", got, tc.want)
			}
		})
	}
}
