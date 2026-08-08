package transcriptrender

import (
	"testing"

	"core/shared/toolspec"
	"core/shared/transcript"
)

func TestWebSearchDisplayTextPreservesQueryQuotes(t *testing.T) {
	query := `site:ghostty.org/docs/config/reference "cmd" keybind Ghostty`
	text, ok := webSearchDisplayText(toolMeta{
		ToolCallMeta: transcript.ToolCallMeta{
			ToolName: string(toolspec.ToolWebSearch),
			Command:  query,
		},
	})
	if !ok {
		t.Fatal("web search display text not recognized")
	}

	want := webSearchDisplayPrefix + `"` + query + `"`
	if text != want {
		t.Fatalf("web search display text = %q, want unescaped query quotes", text)
	}
}
