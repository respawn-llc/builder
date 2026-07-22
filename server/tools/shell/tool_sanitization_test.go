package shell

import (
	"context"
	"strings"
	"testing"
	"time"

	"core/server/tools/shell/postprocess"
	"core/shared/config"
)

func mustPostprocessRunner(t *testing.T, settings postprocess.Settings) *postprocess.Runner {
	t.Helper()
	runner, err := postprocess.NewRunner(settings)
	if err != nil {
		t.Fatalf("new postprocess runner: %v", err)
	}
	return runner
}

func TestNewManagerRejectsNilPostprocessor(t *testing.T) {
	if _, err := NewManager(WithPostprocessor(nil)); err == nil {
		t.Fatal("expected nil shell postprocessor to fail manager construction")
	}
}

func TestExecCommandSanitizesAnsiInDefaultProcessing(t *testing.T) {
	workspace := t.TempDir()
	manager := newBackgroundTestManager(t)
	execTool := NewExecCommandTool(workspace, 16_000, manager, "")

	result := callExecCommand(t, execTool, "ansi-sanitized", map[string]any{
		"cmd":           "printf '\\033[31mred\\033[0m\\rblue\\007'",
		"shell":         "/bin/sh",
		"login":         false,
		"yield_time_ms": 5_000,
	})
	if result.IsError {
		t.Fatalf("unexpected exec_command error: %s", string(result.Output))
	}
	text := decodeStringToolOutput(t, result)
	if text != "red\nblue" {
		t.Fatalf("output = %q, want sanitized text", text)
	}
}

func TestExecCommandRawPreservesAnsi(t *testing.T) {
	workspace := t.TempDir()
	manager := newBackgroundTestManager(t)
	execTool := NewExecCommandTool(workspace, 16_000, manager, "")

	result := callExecCommand(t, execTool, "ansi-raw", map[string]any{
		"cmd":           "printf '\\033[31mred\\033[0m'",
		"shell":         "/bin/sh",
		"login":         false,
		"raw":           true,
		"yield_time_ms": 5_000,
	})
	if result.IsError {
		t.Fatalf("unexpected exec_command error: %s", string(result.Output))
	}
	text := decodeStringToolOutput(t, result)
	if text != "\x1b[31mred\x1b[0m" {
		t.Fatalf("output = %q, want raw ANSI", text)
	}
}

func TestExecCommandPostprocessingNonePreservesAnsi(t *testing.T) {
	workspace := t.TempDir()
	manager, err := NewManager(
		WithMinimumExecToBgTime(250*time.Millisecond),
		WithCloseTimeouts(20*time.Millisecond, 200*time.Millisecond),
		WithPostprocessor(mustPostprocessRunner(t, postprocess.Settings{Mode: config.ShellPostprocessingModeNone})),
	)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	execTool := NewExecCommandTool(workspace, 16_000, manager, "")

	result := callExecCommand(t, execTool, "ansi-none", map[string]any{
		"cmd":           "printf '\\033[31mred\\033[0m'",
		"shell":         "/bin/sh",
		"login":         false,
		"yield_time_ms": 5_000,
	})
	if result.IsError {
		t.Fatalf("unexpected exec_command error: %s", string(result.Output))
	}
	text := decodeStringToolOutput(t, result)
	if text != "\x1b[31mred\x1b[0m" {
		t.Fatalf("output = %q, want raw ANSI", text)
	}
}

func TestRawBackgroundOutputPathsPreserveAnsi(t *testing.T) {
	workspace := t.TempDir()
	manager := newBackgroundTestManager(t)
	execTool := NewExecCommandTool(workspace, 16_000, manager, "")
	stdinTool := NewWriteStdinTool(16_000, manager)

	result := callExecCommand(t, execTool, "raw-bg", map[string]any{
		"cmd":           "printf '\\033[31mhello\\033[0m\\n'; sleep 0.3; printf '\\033[32mdone\\033[0m'",
		"shell":         "/bin/sh",
		"login":         false,
		"raw":           true,
		"yield_time_ms": 250,
	})
	if result.IsError {
		t.Fatalf("unexpected exec_command error: %s", string(result.Output))
	}

	snapshot, err := manager.Snapshot("1000")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if !snapshot.RawOutput {
		t.Fatal("expected raw output snapshot")
	}
	if !strings.Contains(snapshot.RecentOutput, "\x1b[31mhello\x1b[0m") {
		t.Fatalf("recent output lost ANSI: %q", snapshot.RecentOutput)
	}

	sub, err := manager.SubscribeOutput(context.Background(), "1000", 0)
	if err != nil {
		t.Fatalf("SubscribeOutput: %v", err)
	}
	defer func() { _ = sub.Close() }()
	chunk, err := sub.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if !strings.Contains(chunk.Text, "\x1b[31mhello\x1b[0m") {
		t.Fatalf("stream chunk lost ANSI: %q", chunk.Text)
	}

	pollResult := callWriteStdin(t, stdinTool, "raw-bg-poll", map[string]any{
		"session_id":    1000,
		"yield_time_ms": 800,
	})
	if pollResult.IsError {
		t.Fatalf("unexpected write_stdin error: %s", string(pollResult.Output))
	}
	text := decodeStringToolOutput(t, pollResult)
	if !strings.Contains(text, "\x1b[32mdone\x1b[0m") {
		t.Fatalf("poll output lost ANSI: %q", text)
	}
}
