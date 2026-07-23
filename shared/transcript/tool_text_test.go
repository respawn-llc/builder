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
		{name: "compact text", meta: &ToolCallMeta{CompactText: "/tmp/file.go"}, want: "/tmp/file.go"},
		{name: "patch summary", meta: &ToolCallMeta{PatchSummary: "cli/app"}, want: "cli/app"},
		{name: "command", meta: &ToolCallMeta{Command: "go test ./..."}, want: "go test ./..."},
		{name: "first text line", text: "pwd\n/tmp", want: "pwd"},
		{name: "inline meta removed", text: "pwd" + InlineMetaSeparator + "timeout", want: "pwd"},
		{name: "fallback", want: "tool call"},
		{name: "patch skips tool name fallback", meta: &ToolCallMeta{ToolName: "patch"}, want: "tool call"},
		{name: "edit alias replace skips tool name fallback", meta: &ToolCallMeta{ToolName: "replace"}, want: "tool call"},
		{name: "edit alias write skips tool name fallback", meta: &ToolCallMeta{ToolName: "write"}, want: "tool call"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CompactToolCallText(tt.meta, tt.text); got != tt.want {
				t.Fatalf("CompactToolCallText = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDetailedToolCallText(t *testing.T) {
	meta := &ToolCallMeta{
		ToolName:    "exec_command",
		Command:     "go test ./...",
		CompactText: "run tests",
	}
	if got := DetailedToolCallText(meta, "raw output"); got != "go test ./..." {
		t.Fatalf("DetailedToolCallText = %q, want command before output", got)
	}

	patchMeta := &ToolCallMeta{
		ToolName:    "patch",
		PatchDetail: "cli/tui/model.go\n- old\n+ new",
		Command:     "fallback command",
	}
	if got := DetailedToolCallText(patchMeta, "raw patch output"); got != "cli/tui/model.go\n- old\n+ new" {
		t.Fatalf("DetailedToolCallText patch = %q, want full patch detail", got)
	}
}
