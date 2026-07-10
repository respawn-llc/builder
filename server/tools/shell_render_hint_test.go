package tools

import (
	"core/shared/toolspec"
	"core/shared/transcript"
	"encoding/json"
	"testing"
)

func TestDetectShellRenderHintRecognizesSimpleFileViewCommands(t *testing.T) {
	ctx := ToolCallContext{DefaultShellPath: "/bin/zsh", GOOS: "darwin"}
	tests := []struct {
		name    string
		command string
		path    string
	}{
		{name: "cat", command: "cat cli/tui/model.go", path: "cli/tui/model.go"},
		{name: "cat with double dash", command: "cat -- cli/tui/model.go", path: "cli/tui/model.go"},
		{name: "nl", command: "nl cli/tui/model.go", path: "cli/tui/model.go"},
		{name: "nl -ba", command: "nl -ba cli/tui/model.go", path: "cli/tui/model.go"},
		{name: "sed range", command: "sed -n '1,120p' cli/tui/model.go", path: "cli/tui/model.go"},
		{name: "command suffix", command: "cat.exe cli/tui/model.go", path: "cli/tui/model.go"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hint := detectShellRenderHint(ctx, toolspec.ToolExecCommand, json.RawMessage(`{"command":`+jsonQuoted(tc.command)+`}`), tc.command)
			if hint == nil {
				t.Fatalf("expected render hint for command %q", tc.command)
			}
			if hint.Kind != transcript.ToolRenderKindSource {
				t.Fatalf("expected source hint, got %+v", hint)
			}
			if hint.Path != tc.path {
				t.Fatalf("unexpected path: got %q want %q", hint.Path, tc.path)
			}
			if !hint.ResultOnly {
				t.Fatalf("expected result-only highlight mode for command %q", tc.command)
			}
		})
	}
}

func TestDetectShellRenderHintDefaultsToShellForGeneralCommands(t *testing.T) {
	ctx := ToolCallContext{DefaultShellPath: "/bin/zsh", GOOS: "darwin"}
	commands := []string{
		"./gradlew -p apps/respawn detektFormat > docs/tmp/build-triage-2026-03-15/detektFormat.log 2>&1",
		"git status --short",
	}

	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			hint := detectShellRenderHint(ctx, toolspec.ToolExecCommand, json.RawMessage(`{"command":`+jsonQuoted(command)+`}`), command)
			if hint == nil {
				t.Fatalf("expected shell render hint for command %q", command)
			}
			if hint.Kind != transcript.ToolRenderKindShell {
				t.Fatalf("expected shell hint, got %+v", hint)
			}
			if hint.ShellDialect != transcript.ToolShellDialectPosix {
				t.Fatalf("expected posix shell dialect, got %+v", hint)
			}
		})
	}
}

func TestDetectShellRenderHintUsesExplicitWindowsExecShell(t *testing.T) {
	tests := []struct {
		name    string
		raw     json.RawMessage
		command string
		want    transcript.ToolShellDialect
	}{
		{name: "pwsh.exe", raw: json.RawMessage(`{"cmd":"Get-ChildItem","shell":"pwsh.exe"}`), command: "Get-ChildItem", want: transcript.ToolShellDialectPowerShell},
		{name: "powershell.exe", raw: json.RawMessage(`{"cmd":"Get-ChildItem","shell":"powershell.exe"}`), command: "Get-ChildItem", want: transcript.ToolShellDialectPowerShell},
		{name: "cmd.exe", raw: json.RawMessage(`{"cmd":"dir","shell":"cmd.exe"}`), command: "dir", want: transcript.ToolShellDialectWindowsCommand},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hint := detectShellRenderHint(
				ToolCallContext{DefaultShellPath: "/bin/zsh", GOOS: "windows"},
				toolspec.ToolExecCommand,
				tc.raw,
				tc.command,
			)
			if hint == nil || hint.Kind != transcript.ToolRenderKindShell {
				t.Fatalf("expected shell render hint, got %+v", hint)
			}
			if hint.ShellDialect != tc.want {
				t.Fatalf("expected %s dialect, got %+v", tc.want, hint)
			}
		})
	}
}

func TestDetectShellRenderHintFallsBackToWindowsCommandDialect(t *testing.T) {
	command := `copy /y C:\Users\nek\src.txt C:\Temp\dst.txt`
	hint := detectShellRenderHint(
		ToolCallContext{GOOS: "windows"},
		toolspec.ToolExecCommand,
		json.RawMessage(`{"command":`+jsonQuoted(command)+`}`),
		command,
	)
	if hint == nil || hint.Kind != transcript.ToolRenderKindShell {
		t.Fatalf("expected shell render hint, got %+v", hint)
	}
	if hint.ShellDialect != transcript.ToolShellDialectWindowsCommand {
		t.Fatalf("expected windows command dialect, got %+v", hint)
	}
}

func TestDetectShellRenderHintRejectsComplexOrAmbiguousCommands(t *testing.T) {
	ctx := ToolCallContext{DefaultShellPath: "/bin/zsh", GOOS: "darwin"}
	tests := []string{
		"cat",
		"cat cli/tui/model.go | sed -n '1,10p'",
		"cat cli/tui/model.go && echo done",
		`cat "$FILE"`,
		"nl -w2 cli/tui/model.go",
		"sed -n '1,10d' cli/tui/model.go",
		"sed -n '1,10p' cli/tui/model.go extra",
	}

	for _, command := range tests {
		t.Run(command, func(t *testing.T) {
			hint := detectShellRenderHint(ctx, toolspec.ToolExecCommand, json.RawMessage(`{"command":`+jsonQuoted(command)+`}`), command)
			if hint == nil {
				t.Fatalf("expected fallback shell hint for command %q", command)
			}
			if hint.Kind != transcript.ToolRenderKindShell {
				t.Fatalf("expected shell fallback hint, got %+v", hint)
			}
			if hint.ShellDialect != transcript.ToolShellDialectPosix {
				t.Fatalf("expected posix shell dialect, got %+v", hint)
			}
		})
	}
}

func TestParseSedFileArgAcceptsOnlyPrintRangeExpressions(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantPath string
		wantOK   bool
	}{
		{name: "single line", args: []string{"sed", "-n", "1p", "file.go"}, wantPath: "file.go", wantOK: true},
		{name: "line range", args: []string{"sed", "-n", "1,120p", "file.go"}, wantPath: "file.go", wantOK: true},
		{name: "leading zeros", args: []string{"sed", "-n", "001,002p", "--", "file.go"}, wantPath: "file.go", wantOK: true},
		{name: "missing start", args: []string{"sed", "-n", ",2p", "file.go"}},
		{name: "missing end", args: []string{"sed", "-n", "1,p", "file.go"}},
		{name: "multiple commas", args: []string{"sed", "-n", "1,2,3p", "file.go"}},
		{name: "missing print suffix", args: []string{"sed", "-n", "1,2", "file.go"}},
		{name: "extra print suffix", args: []string{"sed", "-n", "1,2pp", "file.go"}},
		{name: "non ascii digits", args: []string{"sed", "-n", "１,２p", "file.go"}},
		{name: "negative line", args: []string{"sed", "-n", "-1p", "file.go"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path, ok := parseSedFileArg(tc.args)
			if ok != tc.wantOK || path != tc.wantPath {
				t.Fatalf("parseSedFileArg(%q) = %q/%t, want %q/%t", tc.args, path, ok, tc.wantPath, tc.wantOK)
			}
		})
	}
}

func jsonQuoted(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
