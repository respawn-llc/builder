package postprocess

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"core/internal/testharness/testsetup"
	"core/shared/config"
	"core/shared/sessionenv"
	"core/shared/toolspec"
)

func mustNewRunner(t *testing.T, settings Settings) *Runner {
	t.Helper()
	runner, err := NewRunner(settings)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return runner
}

func testStringPointer(value string) *string {
	copy := value
	return &copy
}

func TestWarningRejectsBlankMessageAndMergesImmutably(t *testing.T) {
	if _, err := NewWarning(" \t\n "); err == nil {
		t.Fatal("expected blank warning construction to fail")
	}

	first, err := NewWarning("first")
	if err != nil {
		t.Fatalf("NewWarning(first): %v", err)
	}
	second, err := NewWarning("second")
	if err != nil {
		t.Fatalf("NewWarning(second): %v", err)
	}
	merged := MergeWarnings(first, second)
	if merged == nil {
		t.Fatal("expected merged warning")
	}
	if got := len(merged.(*warningAggregate).messages); got != 2 {
		t.Fatalf("merged warning count = %d, want 2", got)
	}
	if got := len(first.(*warningAggregate).messages); got != 1 {
		t.Fatalf("first warning count mutated to %d", got)
	}
	if MergeWarnings(nil, nil) != nil {
		t.Fatal("expected absent warnings to remain nil")
	}
}

func TestRunnerBuiltinGoTestSuccessCollapsesToPass(t *testing.T) {
	runner := mustNewRunner(t, Settings{Mode: config.ShellPostprocessingModeBuiltin})
	exitCode := 0
	result, err := runner.Apply(context.Background(), Request{
		ToolName:    toolspec.ToolExecCommand,
		CommandText: "go test ./...",
		ExitCode:    &exitCode,
		Output:      "PASS\nok\texample.com/postprocess\t0.123s\n",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !result.Processed {
		t.Fatal("expected builtin processor to handle successful go test")
	}
	if result.Output != "PASS" {
		t.Fatalf("output = %q, want PASS", result.Output)
	}
}

func TestRunnerBuiltinGoTestPreservesDetailedOutput(t *testing.T) {
	runner := mustNewRunner(t, Settings{Mode: config.ShellPostprocessingModeBuiltin})
	exitCode := 0
	tests := []struct {
		name        string
		commandText string
		parsedArgs  []string
		output      string
	}{
		{
			name:        "benchmark",
			commandText: "go test -bench=. ./...",
			parsedArgs:  []string{"go", "test", "-bench=.", "./..."},
			output:      "PASS\nBenchmarkFoo\t100\t123 ns/op\nok\texample.com/postprocess\t0.123s\n",
		},
		{
			name:        "coverage",
			commandText: "go test -cover ./...",
			parsedArgs:  []string{"go", "test", "-cover", "./..."},
			output:      "PASS\ncoverage: 81.2% of statements\nok\texample.com/postprocess\t0.123s\n",
		},
		{
			name:        "json",
			commandText: "go test -json ./...",
			parsedArgs:  []string{"go", "test", "-json", "./..."},
			output:      "{\"Time\":\"2026-04-23T00:00:00Z\",\"Action\":\"pass\",\"Package\":\"example.com/postprocess\"}\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := runner.Apply(context.Background(), Request{
				ToolName:    toolspec.ToolExecCommand,
				CommandText: tt.commandText,
				ParsedArgs:  tt.parsedArgs,
				CommandName: "go",
				ExitCode:    &exitCode,
				Output:      tt.output,
			})
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if result.Processed {
				t.Fatalf("expected %s output to bypass collapse", tt.name)
			}
			if result.Output != tt.output {
				t.Fatalf("output = %q, want original output", result.Output)
			}
		})
	}
}

func TestRunnerBuiltinFileReadAddsTotalLineCountForPartialSed(t *testing.T) {
	path := writeTextFile(t, "example.txt", strings.Join([]string{
		"line 1",
		"line 2",
		"line 3",
		"line 4",
		"line 5",
	}, "\n")+"\n")
	runner := mustNewRunner(t, Settings{Mode: config.ShellPostprocessingModeBuiltin})
	exitCode := 0

	result, err := runner.Apply(context.Background(), Request{
		ToolName:    toolspec.ToolExecCommand,
		CommandText: "sed -n '2,4p' " + shellQuote(path),
		ExitCode:    &exitCode,
		Output:      "line 2\nline 3\nline 4\n",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !result.Processed {
		t.Fatal("expected partial file read to be processed")
	}
	if result.Output != "[Total line count: 5]\nline 2\nline 3\nline 4\n" {
		t.Fatalf("output = %q", result.Output)
	}
}

func TestRunnerBuiltinFileReadHandlesReportedSedRangeShape(t *testing.T) {
	path := writeNestedTextFile(t, filepath.Join("cli", "app", "ui_goal.go"), numberedLines(414))
	runner := mustNewRunner(t, Settings{Mode: config.ShellPostprocessingModeBuiltin})
	exitCode := 0
	output := numberedLines(414)

	result, err := runner.Apply(context.Background(), Request{
		ToolName:    toolspec.ToolExecCommand,
		CommandText: "sed -n '1,430p' " + shellQuote(path),
		ExitCode:    &exitCode,
		Output:      output,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Processed {
		t.Fatal("expected full sed range read to bypass file context marker")
	}
	if result.Output != output {
		t.Fatalf("output = %q", result.Output)
	}
}

func TestRunnerBuiltinFileReadAddsTotalLineCountWhenReportedSedRangeIsPartial(t *testing.T) {
	path := writeNestedTextFile(t, filepath.Join("cli", "app", "ui_goal.go"), numberedLines(431))
	runner := mustNewRunner(t, Settings{Mode: config.ShellPostprocessingModeBuiltin})
	exitCode := 0
	output := numberedLines(430)

	result, err := runner.Apply(context.Background(), Request{
		ToolName:    toolspec.ToolExecCommand,
		CommandText: "sed -n '1,430p' " + shellQuote(path),
		ExitCode:    &exitCode,
		Output:      output,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !result.Processed {
		t.Fatal("expected partial sed range read to include file context marker")
	}
	want := "[Total line count: 431]\n" + output
	if result.Output != want {
		t.Fatalf("output = %q, want %q", result.Output, want)
	}
}

func TestRunnerBuiltinFileReadAddsTotalLineCountForUnknownSedScriptFile(t *testing.T) {
	path := writeTextFile(t, "example.txt", "line 1\nline 2\nline 3\n")
	scriptPath := writeTextFile(t, "script.sed", "1,1p\n")
	runner := mustNewRunner(t, Settings{Mode: config.ShellPostprocessingModeBuiltin})
	exitCode := 0

	result, err := runner.Apply(context.Background(), Request{
		ToolName:    toolspec.ToolExecCommand,
		CommandText: "sed -f " + shellQuote(scriptPath) + " " + shellQuote(path),
		ExitCode:    &exitCode,
		Output:      "line 1\n",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !result.Processed {
		t.Fatal("expected unknown sed script file to be processed conservatively")
	}
	if result.Output != "[Total line count: 3]\nline 1\n" {
		t.Fatalf("output = %q", result.Output)
	}
}

func TestRunnerBuiltinFileReadSkipsSedWhenFullFileIsKnown(t *testing.T) {
	path := writeTextFile(t, "example.txt", "line 1\nline 2\n")
	runner := mustNewRunner(t, Settings{Mode: config.ShellPostprocessingModeBuiltin})
	exitCode := 0
	tests := []struct {
		name    string
		command string
	}{
		{name: "unaddressed print", command: "sed -n p " + shellQuote(path)},
		{name: "range starts at first line and exceeds file length", command: "sed -n '1,430p' " + shellQuote(path)},
		{name: "range starts at first line and ends at last line", command: "sed -n '1,2p' " + shellQuote(path)},
		{name: "range through eof", command: "sed -n '1,$p' " + shellQuote(path)},
		{name: "expression range", command: "sed -n -e '1,430p' " + shellQuote(path)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := runner.Apply(context.Background(), Request{
				ToolName:    toolspec.ToolExecCommand,
				CommandText: tt.command,
				ExitCode:    &exitCode,
				Output:      "line 1\nline 2\n",
			})
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if result.Processed {
				t.Fatal("expected known full-file sed output to bypass file context marker")
			}
			if result.Output != "line 1\nline 2\n" {
				t.Fatalf("output = %q", result.Output)
			}
		})
	}
}

func TestRunnerBuiltinFileReadAddsTotalLineCountForSedRangeEdgeCases(t *testing.T) {
	largePath := writeTextFile(t, "large.txt", numberedLines(431))
	smallPath := writeTextFile(t, "small.txt", "line 1\nline 2\n")
	runner := mustNewRunner(t, Settings{Mode: config.ShellPostprocessingModeBuiltin})
	exitCode := 0
	tests := []struct {
		name      string
		command   string
		output    string
		lineCount int
	}{
		{name: "negated range", command: "sed -n '1,430!p' " + shellQuote(largePath), output: "line 431\n", lineCount: 431},
		{name: "multiple scripts", command: "sed -n -e '1,430p' -e '2,2p' " + shellQuote(smallPath), output: "line 1\nline 2\nline 2\n", lineCount: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := runner.Apply(context.Background(), Request{
				ToolName:    toolspec.ToolExecCommand,
				CommandText: tt.command,
				ExitCode:    &exitCode,
				Output:      tt.output,
			})
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if !result.Processed {
				t.Fatal("expected partial sed read to include file context marker")
			}
			want := fmt.Sprintf("[Total line count: %d]\n%s", tt.lineCount, tt.output)
			if result.Output != want {
				t.Fatalf("output = %q, want %q", result.Output, want)
			}
		})
	}
}

func TestRunnerBuiltinFileReadAddsTotalLineCountForHeadTailAndPowerShell(t *testing.T) {
	// File without trailing newline verifies non-POSIX file handling.
	path := writeTextFile(t, "example.txt", strings.Join([]string{
		"line 1",
		"line 2",
		"line 3",
		"line 4",
		"line 5",
	}, "\n"))
	runner := mustNewRunner(t, Settings{Mode: config.ShellPostprocessingModeBuiltin})
	exitCode := 0
	tests := []struct {
		name    string
		command string
		output  string
	}{
		{name: "head", command: "head -n 2 " + shellQuote(path), output: "line 1\nline 2\n"},
		{name: "tail", command: "tail -2 " + shellQuote(path), output: "line 4\nline 5\n"},
		{name: "powershell head", command: "Get-Content " + shellQuote(path) + " -TotalCount 2", output: "line 1\nline 2\n"},
		{name: "powershell tail", command: "Get-Content " + shellQuote(path) + " -Tail 2", output: "line 4\nline 5\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := runner.Apply(context.Background(), Request{
				ToolName:    toolspec.ToolExecCommand,
				CommandText: tt.command,
				ExitCode:    &exitCode,
				Output:      tt.output,
			})
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if !result.Processed {
				t.Fatal("expected partial file read to be processed")
			}
			want := "[Total line count: 5]\n" + tt.output
			if result.Output != want {
				t.Fatalf("output = %q, want %q", result.Output, want)
			}
		})
	}
}

func TestRunnerBuiltinFileReadSkipsFullHeadTailAndLargeFiles(t *testing.T) {
	smallPath := writeTextFile(t, "small.txt", "line 1\nline 2\n")
	largePath := filepath.Join(t.TempDir(), "large.txt")
	if err := os.WriteFile(largePath, []byte(strings.Repeat("x", 1024*1024+1)), 0o644); err != nil {
		t.Fatalf("write large file: %v", err)
	}
	binaryPath := filepath.Join(t.TempDir(), "binary.txt")
	if err := os.WriteFile(binaryPath, []byte("line 1\x00line 2\n"), 0o644); err != nil {
		t.Fatalf("write binary file: %v", err)
	}
	runner := mustNewRunner(t, Settings{Mode: config.ShellPostprocessingModeBuiltin})
	exitCode := 0
	tests := []struct {
		name    string
		command string
		output  string
	}{
		{name: "head full", command: "head -n 10 " + shellQuote(smallPath), output: "line 1\nline 2\n"},
		{name: "tail full", command: "tail -n 10 " + shellQuote(smallPath), output: "line 1\nline 2\n"},
		{name: "tail negative full", command: "tail -n -5 " + shellQuote(smallPath), output: "line 1\nline 2\n"},
		{name: "tail compact negative full", command: "tail -n-5 " + shellQuote(smallPath), output: "line 1\nline 2\n"},
		{name: "tail long negative full", command: "tail --lines=-5 " + shellQuote(smallPath), output: "line 1\nline 2\n"},
		{name: "tail from first line", command: "tail -n +1 " + shellQuote(smallPath), output: "line 1\nline 2\n"},
		{name: "tail compact from first line", command: "tail -n+1 " + shellQuote(smallPath), output: "line 1\nline 2\n"},
		{name: "tail long from first line", command: "tail --lines=+1 " + shellQuote(smallPath), output: "line 1\nline 2\n"},
		{name: "powershell full", command: "Get-Content " + shellQuote(smallPath) + " -TotalCount 10", output: "line 1\nline 2\n"},
		{name: "large file", command: "head -n 1 " + shellQuote(largePath), output: "x\n"},
		{name: "binary file", command: "head -n 1 " + shellQuote(binaryPath), output: "line 1\n"},
		{name: "head bytes", command: "head -c 3 " + shellQuote(smallPath), output: "lin"},
		{name: "tail bytes", command: "tail --bytes=3 " + shellQuote(smallPath), output: "2\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := runner.Apply(context.Background(), Request{
				ToolName:    toolspec.ToolExecCommand,
				CommandText: tt.command,
				ExitCode:    &exitCode,
				Output:      tt.output,
			})
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if result.Processed {
				t.Fatal("expected file read marker to be skipped")
			}
			if result.Output != tt.output {
				t.Fatalf("output = %q, want %q", result.Output, tt.output)
			}
		})
	}
}

func TestRunnerBuiltinFileReadSkipsWhenParsedArgsOmitCommandName(t *testing.T) {
	path := writeTextFile(t, "example.txt", "line 1\nline 2\nline 3\n")
	runner := mustNewRunner(t, Settings{Mode: config.ShellPostprocessingModeBuiltin})
	exitCode := 0

	result, err := runner.Apply(context.Background(), Request{
		ToolName:    toolspec.ToolExecCommand,
		CommandName: "tail",
		ParsedArgs:  []string{"-n", "2", path},
		Workdir:     filepath.Dir(path),
		ExitCode:    &exitCode,
		Output:      "line 2\nline 3\n",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Processed || result.Output != "line 2\nline 3\n" {
		t.Fatalf("expected invalid parsed args contract to skip processing, got processed=%t output=%q", result.Processed, result.Output)
	}
}

func TestRunnerBuiltinFileReadSkipsComposedCommandsAndWholeFileReads(t *testing.T) {
	path := writeTextFile(t, "example.txt", "line 1\nline 2\n")
	runner := mustNewRunner(t, Settings{Mode: config.ShellPostprocessingModeBuiltin})
	exitCode := 0
	tests := []struct {
		name    string
		command string
		output  string
	}{
		{name: "cat", command: "cat " + shellQuote(path), output: "line 1\nline 2\n"},
		{name: "pipeline", command: "nl -ba " + shellQuote(path) + " | sed -n '1,1p'", output: "     1\tline 1\n"},
		{name: "sed transform", command: "sed 's/line/row/' " + shellQuote(path), output: "row 1\nrow 2\n"},
		{name: "awk", command: "awk 'NR>=2 && NR<=3' " + shellQuote(path), output: "line 2\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := runner.Apply(context.Background(), Request{
				ToolName:    toolspec.ToolExecCommand,
				CommandText: tt.command,
				ExitCode:    &exitCode,
				Output:      tt.output,
			})
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if result.Processed {
				t.Fatal("expected file read marker to be skipped")
			}
			if result.Output != tt.output {
				t.Fatalf("output = %q, want %q", result.Output, tt.output)
			}
		})
	}
}

func TestRunnerRawBypassesBuiltinProcessing(t *testing.T) {
	runner := mustNewRunner(t, Settings{Mode: config.ShellPostprocessingModeBuiltin})
	exitCode := 0
	result, err := runner.Apply(context.Background(), Request{
		ToolName:    toolspec.ToolExecCommand,
		CommandText: "go test ./...",
		ExitCode:    &exitCode,
		Raw:         true,
		Output:      "raw go test output",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Processed {
		t.Fatal("expected raw request to bypass postprocessing")
	}
	if result.Output != "raw go test output" {
		t.Fatalf("output = %q, want raw output", result.Output)
	}
}

func TestRunnerAdvisesForEveryProvenGitWorktreeInvocation(t *testing.T) {
	runner := mustNewRunner(t, Settings{Mode: config.ShellPostprocessingModeBuiltin})
	for _, command := range []string{
		`printf ready; command -- git worktree list`,
		`git -C /repo worktree list`,
		`git -c core.quotePath=false worktree list`,
		`git --git-dir /repo/.git worktree list`,
		`git --git-dir=/repo/.git worktree list`,
	} {
		result, err := runner.Apply(context.Background(), Request{
			ToolName:    toolspec.ToolExecCommand,
			CommandText: command,
			Output:      "ready",
		})
		if err != nil {
			t.Fatalf("Apply(%q): %v", command, err)
		}
		if result.Warning == nil {
			t.Fatalf("expected git worktree advisory for %q", command)
		}
	}
}

func TestRunnerUserModeDoesNotRunGitWorktreeAdvisory(t *testing.T) {
	runner := mustNewRunner(t, Settings{Mode: config.ShellPostprocessingModeUser})
	runner.hookProcessor = testProcessor{id: "user-hook", fn: func(envelope Envelope) (Decision, error) {
		return Skip(envelope), nil
	}}
	result, err := runner.Apply(context.Background(), Request{
		ToolName:    toolspec.ToolExecCommand,
		CommandText: `git worktree list`,
		Output:      "unchanged",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Warning != nil {
		t.Fatalf("unexpected built-in advisory in user mode: %s", result.Warning.Text())
	}
}

func TestRunnerDoesNotAdviseForTextOrDynamicGitWorktreeWords(t *testing.T) {
	runner := mustNewRunner(t, Settings{Mode: config.ShellPostprocessingModeBuiltin})
	for _, command := range []string{
		`printf 'git worktree list'`,
		`git "$subcommand" list`,
	} {
		result, err := runner.Apply(context.Background(), Request{
			ToolName:    toolspec.ToolExecCommand,
			CommandText: command,
			Output:      "unchanged",
		})
		if err != nil {
			t.Fatalf("Apply(%q): %v", command, err)
		}
		if result.Warning != nil {
			t.Fatalf("unexpected advisory for %q: %s", command, result.Warning.Text())
		}
	}
}

func TestRunnerAggregateProcessorRequiresStandaloneInvocation(t *testing.T) {
	runner := mustNewRunner(t, Settings{Mode: config.ShellPostprocessingModeBuiltin})
	exitCode := 0
	output := "PASS\nok\texample.com/postprocess\t0.123s\ndone\n"
	result, err := runner.Apply(context.Background(), Request{
		ToolName:    toolspec.ToolExecCommand,
		CommandText: `go test ./... && printf done`,
		ExitCode:    &exitCode,
		Output:      output,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Output != output {
		t.Fatalf("output = %q, want attributable aggregate output unchanged", result.Output)
	}
}

func TestRunnerUserHookReplacesOutput(t *testing.T) {
	hookPath := writeHookScript(t, "#!/bin/sh\nprintf '{\"processed\":true,\"replaced_output\":\"HOOKED\"}\n'")
	runner := mustNewRunner(t, Settings{Mode: config.ShellPostprocessingModeUser, HookPath: testStringPointer(hookPath)})
	result, err := runner.Apply(context.Background(), Request{
		ToolName:    toolspec.ToolExecCommand,
		CommandText: "printf hi",
		Output:      "hi",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !result.Processed {
		t.Fatal("expected user hook to mark output processed")
	}
	if result.Output != "HOOKED" {
		t.Fatalf("output = %q, want HOOKED", result.Output)
	}
}

func TestRunnerUserHookInheritsOwnerSessionID(t *testing.T) {
	hookPath := writeHookScript(t, `#!/bin/sh
printf '{"processed":true,"replaced_output":"%s"}' "$`+sessionenv.SessionIDEnv+`"
`)
	runner := mustNewRunner(t, Settings{Mode: config.ShellPostprocessingModeUser, HookPath: testStringPointer(hookPath)})
	result, err := runner.Apply(context.Background(), Request{
		ToolName:       toolspec.ToolExecCommand,
		CommandText:    "printf hi",
		OwnerSessionID: "session-hook-123",
		Output:         "hi",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !result.Processed {
		t.Fatal("expected user hook to mark output processed")
	}
	if result.Output != "session-hook-123" {
		t.Fatalf("output = %q, want session-hook-123", result.Output)
	}
}

func TestRunnerAllModeFallsBackToBuiltinWhenHookFails(t *testing.T) {
	hookPath := writeHookScript(t, "#!/bin/sh\nprintf 'not-json\n'")
	runner := mustNewRunner(t, Settings{Mode: config.ShellPostprocessingModeAll, HookPath: testStringPointer(hookPath)})
	exitCode := 0
	result, err := runner.Apply(context.Background(), Request{
		ToolName:    toolspec.ToolExecCommand,
		CommandText: "go test ./...",
		ExitCode:    &exitCode,
		Output:      "PASS\nok\texample.com/postprocess\t0.123s\n",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Output != "PASS" {
		t.Fatalf("output = %q, want PASS", result.Output)
	}
	if !result.Processed {
		t.Fatal("expected builtin fallback to remain processed")
	}
}

func TestRunnerUserModeBrokenHookFallsBackToOriginal(t *testing.T) {
	hookPath := writeHookScript(t, "#!/bin/sh\nprintf 'not-json\n'")
	runner := mustNewRunner(t, Settings{Mode: config.ShellPostprocessingModeUser, HookPath: testStringPointer(hookPath)})
	result, err := runner.Apply(context.Background(), Request{
		ToolName:    toolspec.ToolExecCommand,
		CommandText: "printf hi",
		Output:      "hi",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Processed {
		t.Fatal("expected broken user hook to fall back to original output")
	}
	if result.Output != "hi" {
		t.Fatalf("output = %q, want hi", result.Output)
	}
}

func TestRunnerUserHookCancellationPropagates(t *testing.T) {
	hookPath := writeHookScript(t, "#!/bin/sh\nsleep 5\n")
	runner := mustNewRunner(t, Settings{Mode: config.ShellPostprocessingModeUser, HookPath: testStringPointer(hookPath)})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := runner.Apply(ctx, Request{
		ToolName:    toolspec.ToolExecCommand,
		CommandText: "printf hi",
		Output:      "hi",
	})
	if err == nil {
		t.Fatal("expected canceled context to propagate")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want errors.Is(..., context.Canceled)", err)
	}
}

func TestRunnerUserHookFailureWarningTruncatesStderr(t *testing.T) {
	hookPath := writeHookScript(t, "#!/bin/sh\ni=0\nwhile [ \"$i\" -lt 5000 ]; do\n  printf 'xxxxxxxxxx' 1>&2\n  i=$((i + 1))\ndone\nexit 1\n")
	runner := mustNewRunner(t, Settings{Mode: config.ShellPostprocessingModeUser, HookPath: testStringPointer(hookPath)})
	result, err := runner.Apply(context.Background(), Request{
		ToolName:    toolspec.ToolExecCommand,
		CommandText: "printf hi",
		Output:      "hi",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Warning == nil {
		t.Fatal("expected hook failure warning")
	}
	if len(result.Warning.(*warningAggregate).messages) != 1 {
		t.Fatalf("warning count = %d, want 1", len(result.Warning.(*warningAggregate).messages))
	}
	if len(result.Warning.Text()) > maxHookOutputBytes+512 {
		t.Fatalf("expected bounded warning length, got %d", len(result.Warning.Text()))
	}
}

func writeHookScript(t *testing.T, contents string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("hook tests require POSIX shell")
	}
	return testsetup.WriteExecutable(t, "hook.sh", contents)
}

func writeTextFile(t *testing.T, name string, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write text file: %v", err)
	}
	return path
}

func writeNestedTextFile(t *testing.T, name string, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent dirs: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write text file: %v", err)
	}
	return path
}

func numberedLines(count int) string {
	lines := make([]string, 0, count)
	for i := 1; i <= count; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	return strings.Join(lines, "\n") + "\n"
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func TestRunnerAllModeAccumulatesWarnings(t *testing.T) {
	missingHookPath := filepath.Join(t.TempDir(), "missing-hook")
	runner := mustNewRunner(t, Settings{Mode: config.ShellPostprocessingModeAll, HookPath: testStringPointer(missingHookPath)})
	runner.processors = []Processor{warningProcessor{}}
	result, err := runner.Apply(context.Background(), Request{
		ToolName:    toolspec.ToolExecCommand,
		CommandText: "printf hi",
		Output:      "hi",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Warning == nil {
		t.Fatal("expected accumulated warnings")
	}
	if got := len(result.Warning.(*warningAggregate).messages); got != 2 {
		t.Fatalf("warning count = %d, want 2", got)
	}
}

type warningProcessor struct{}

func (warningProcessor) ID() string { return "test/warning" }

func (warningProcessor) Process(_ context.Context, envelope Envelope) (Decision, error) {
	warning, err := NewWarning("builtin warning")
	if err != nil {
		return Decision{}, err
	}
	return Decision{Action: ActionSkip, Next: envelope, Warning: warning}, nil
}
