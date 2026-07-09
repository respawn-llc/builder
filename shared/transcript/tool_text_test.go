package transcript

import "testing"

func TestSplitInlineMeta(t *testing.T) {
	command, meta := SplitInlineMeta("  echo hi  " + InlineMetaSeparator + "  timeout  ")
	if command != "echo hi" || meta != "timeout" {
		t.Fatalf("SplitInlineMeta = %q, %q; want command/meta", command, meta)
	}

	command, meta = SplitInlineMeta(" echo hi ")
	if command != "echo hi" || meta != "" {
		t.Fatalf("SplitInlineMeta without meta = %q, %q", command, meta)
	}
}

func TestCompactToolCallText(t *testing.T) {
	tests := []struct {
		name string
		meta *ToolCallMeta
		text string
		want string
	}{
		{name: "compact text", meta: &ToolCallMeta{CompactText: "Edited: /tmp/file.go"}, want: "/tmp/file.go"},
		{name: "empty edited compact falls through", meta: &ToolCallMeta{CompactText: "Edited:", PatchSummary: "./a.go +1"}, want: "./a.go +1"},
		{name: "empty edited summary falls through", meta: &ToolCallMeta{PatchSummary: "Edited:", Command: "*** Begin Patch"}, want: "*** Begin Patch"},
		{name: "patch summary", meta: &ToolCallMeta{PatchSummary: "Edited: cli/app"}, want: "cli/app"},
		{name: "command", meta: &ToolCallMeta{Command: "go test ./..."}, want: "go test ./..."},
		{name: "first text line", text: "pwd\n/tmp", want: "pwd"},
		{name: "inline meta removed", text: "pwd" + InlineMetaSeparator + "timeout", want: "pwd"},
		{name: "fallback", want: "tool call"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CompactToolCallText(tt.meta, tt.text); got != tt.want {
				t.Fatalf("CompactToolCallText = %q, want %q", got, tt.want)
			}
		})
	}
}
