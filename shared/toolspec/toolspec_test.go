package toolspec

import "testing"

func TestParseID(t *testing.T) {
	tests := []struct {
		in   string
		want ID
		ok   bool
	}{
		{in: "shell", want: ToolExecCommand, ok: true},
		{in: "bash", want: ToolExecCommand, ok: true},
		{in: "bash_command", want: ToolExecCommand, ok: true},
		{in: "shell_command", want: ToolExecCommand, ok: true},
		{in: "exec_command", want: ToolExecCommand, ok: true},
		{in: "write_stdin", want: ToolWriteStdin, ok: true},
		{in: "view_image", want: ToolViewImage, ok: true},
		{in: "read_image", want: ToolViewImage, ok: true},
		{in: "patch", want: ToolPatch, ok: true},
		{in: "edit", want: ToolEdit, ok: true},
		{in: "replace", want: ToolEdit, ok: true},
		{in: "write", want: ToolEdit, ok: true},
		{in: "ask_question", want: ToolAskQuestion, ok: true},
		{in: "trigger_handoff", want: ToolTriggerHandoff, ok: true},
		{in: "web_search", want: ToolWebSearch, ok: true},
		{in: "unknown", ok: false},
	}

	for _, tt := range tests {
		got, ok := ParseID(tt.in)
		if ok != tt.ok {
			t.Fatalf("ParseID(%q) ok=%t want %t", tt.in, ok, tt.ok)
		}
		if ok && got != tt.want {
			t.Fatalf("ParseID(%q)=%q want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseConfigIDAndConfigName(t *testing.T) {
	if got, ok := ParseConfigID("shell"); !ok || got != ToolExecCommand {
		t.Fatalf("ParseConfigID(shell) = %q, %t", got, ok)
	}
	if got, ok := ParseConfigID("bash"); ok {
		t.Fatalf("ParseConfigID(bash) unexpectedly resolved to %q", got)
	}
	if got, ok := ParseConfigID("edit"); !ok || got != ToolEdit {
		t.Fatalf("ParseConfigID(edit) = %q, %t", got, ok)
	}
	for _, alias := range []string{"replace", "write"} {
		if got, ok := ParseConfigID(alias); ok {
			t.Fatalf("ParseConfigID(%s) unexpectedly resolved to %q", alias, got)
		}
	}
	if got := ConfigName(ToolExecCommand); got != "shell" {
		t.Fatalf("ConfigName(exec_command) = %q, want shell", got)
	}
}
