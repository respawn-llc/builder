package shell

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"core/server/tools"
)

func assertOversizedOutputFailure(t *testing.T, result tools.Result, logPath string) {
	want := fmt.Sprintf(oversizedOutputMessageTemplate, logPath)
	wantBody, _ := json.Marshal(map[string]string{"error": want})
	if !result.IsError || string(result.Output) != string(wantBody) {
		t.Fatalf("failure = %s, want %s", string(result.Output), string(wantBody))
	}
}

func TestExecCommandGuardsOversizedRequestedOutputAfterExecution(t *testing.T) {
	manager := newBackgroundTestManager(t)
	execTool := NewExecCommandTool(t.TempDir(), 16_000, 20, manager, "")
	const commandOutput = "123456789012345678901234567890123456789012345678"
	result := callExecCommand(t, execTool, "oversized-output", map[string]any{
		"cmd": "printf '" + commandOutput + "'; exit 7", "shell": "/bin/sh", "login": false,
		"raw": true, "yield_time_ms": 1_000, "max_output_tokens": 11,
	})
	logPath := filepath.Join(manager.TempDir(), "1000.log")
	assertOversizedOutputFailure(t, result, logPath)
	if log, err := os.ReadFile(logPath); err != nil || string(log) != commandOutput {
		t.Fatalf("retained output = %q, error=%v, want %q", string(log), err, commandOutput)
	}
	if delta := result.PresentationDelta; delta == nil || !delta.RawOutputRequested || delta.ShellExitCode == nil || *delta.ShellExitCode != 7 {
		t.Fatalf("presentation facts = %#v, want raw output and exit code 7", delta)
	}
}
func TestExecCommandOversizedOutputBoundaries(t *testing.T) {
	ptr := func(value int) *int { return &value }
	tests := []struct {
		name, output              string
		maxOutputTokens, exitCode *int
		wantOutput                string
		truncated                 bool
	}{
		{"requested above half with output at half", strings.Repeat("1", 80), ptr(21), nil, strings.Repeat("1", 80), false},
		{"cap equal to half with large output", strings.Repeat("1", 100), ptr(20), nil, "", true},
		{"cap below half with large output", strings.Repeat("1", 100), ptr(19), nil, "", true},
		{"cap omitted above half", strings.Repeat("1", 84), nil, nil, strings.Repeat("1", 84), false},
		{"nonzero output at half", strings.Repeat("1", 59), ptr(21), ptr(7), "", false},
		{"decoded plaintext with JSON escaping", strings.Repeat("\"\\", 40), ptr(21), nil, strings.Repeat("\"\\", 40), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := newBackgroundTestManager(t)
			execTool := NewExecCommandTool(t.TempDir(), 16_000, 40, manager, "")
			input := map[string]any{"cmd": "printf '%s' '" + test.output + "'", "shell": "/bin/sh", "login": false, "yield_time_ms": 1_000}
			if test.maxOutputTokens != nil {
				input["max_output_tokens"] = *test.maxOutputTokens
			}
			if test.exitCode != nil {
				input["cmd"] = input["cmd"].(string) + "; exit 7"
			}
			result := callExecCommand(t, execTool, test.name, input)
			if result.IsError || test.truncated != (result.PresentationDelta != nil && result.PresentationDelta.OutputTruncated) {
				t.Fatalf("result = %#v, want success/truncated=%t", result, test.truncated)
			}
			if test.wantOutput != "" && decodeStringToolOutput(t, result) != test.wantOutput {
				t.Fatalf("output = %q, want %q", decodeStringToolOutput(t, result), test.wantOutput)
			}
			if test.exitCode != nil && (result.PresentationDelta == nil || result.PresentationDelta.ShellExitCode == nil || *result.PresentationDelta.ShellExitCode != *test.exitCode) {
				t.Fatalf("presentation = %#v, want exit code %d", result.PresentationDelta, *test.exitCode)
			}
		})
	}
}
func TestRunningExecCommandGuardsOutputWithoutStoppingProcess(t *testing.T) {
	workspace := t.TempDir()
	manager := newShellTestManager(t, 50*time.Millisecond)
	execTool := NewExecCommandTool(workspace, 16_000, 40, manager, "")
	const commandOutput = "12345678901234567890123456789012345678901234567890123456789012345678901234567890"
	result := callExecCommand(t, execTool, "running-oversized-output", map[string]any{
		"cmd": "printf '" + commandOutput + "'; sleep 0.5", "shell": "/bin/sh", "login": false,
		"raw": true, "yield_time_ms": 50, "max_output_tokens": 21,
	})
	snapshot, err := manager.Snapshot("1000")
	if err != nil || !snapshot.Running {
		t.Fatalf("running snapshot = %+v, error=%v", snapshot, err)
	}
	assertOversizedOutputFailure(t, result, snapshot.LogPath)
	if delta := result.PresentationDelta; delta == nil || !delta.RawOutputRequested || !delta.MovedToBackground || delta.OutputTruncated || delta.ShellExitCode != nil {
		t.Fatalf("running presentation = %#v, want normal running facts", delta)
	}
	if log, err := os.ReadFile(snapshot.LogPath); err != nil || string(log) != commandOutput {
		t.Fatalf("running log = %q, error=%v, want %q", string(log), err, commandOutput)
	}
	poll := callWriteStdin(t, NewWriteStdinTool(16_000, 40, manager), "running-oversized-output-poll", map[string]any{
		"session_id": 1000, "yield_time_ms": 15_000, "max_output_tokens": 19,
	})
	if poll.IsError {
		t.Fatalf("later poll = %s, want independent success", string(poll.Output))
	}
}

func startOutputGuardWriteScenario(t *testing.T, command string) (*Manager, *WriteStdinTool, Snapshot) {
	manager := newShellTestManager(t, 50*time.Millisecond)
	start := callExecCommand(t, NewExecCommandTool(t.TempDir(), 16_000, 40, manager, ""), "write-start", map[string]any{
		"cmd": command, "shell": "/bin/sh", "login": false, "tty": true, "yield_time_ms": 50,
	})
	if start.IsError || manager.Count() != 1 {
		t.Fatalf("start result = %s, process count = %d", string(start.Output), manager.Count())
	}
	snapshot, err := manager.Snapshot("1000")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return manager, NewWriteStdinTool(16_000, 40, manager), snapshot
}

func TestWriteStdinGuardsRunningCompletedAndEscapedOutput(t *testing.T) {
	const plain = "12345678901234567890123456789012345678901234567890123456789012345678901234567890"
	tests := []struct {
		name, command, output, chars string
		running, later               bool
	}{
		{"running", "read line; printf '" + plain + "'; sleep 0.5", plain, "go\n", true, true},
		{"completed", "sleep 0.1; printf '" + plain + "'", plain, "", false, false},
		{"escaped wrapper", "read line; printf '%s' '" + strings.Repeat("\"\\", 20) + "'; sleep 0.5", strings.Repeat("\"\\", 20), "go\n", true, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, pollTool, snapshot := startOutputGuardWriteScenario(t, test.command)
			input := map[string]any{"session_id": 1000, "yield_time_ms": 15_000, "max_output_tokens": 21}
			if test.chars != "" {
				input["chars"], input["yield_time_ms"] = test.chars, 50
			}
			result := callWriteStdin(t, pollTool, "write-guard", input)
			assertOversizedOutputFailure(t, result, snapshot.LogPath)
			if test.running {
				current, err := manager.Snapshot(snapshot.ID)
				if err != nil || !current.Running {
					t.Fatalf("guarded snapshot = %+v, error=%v", current, err)
				}
			} else if result.PresentationDelta == nil || result.PresentationDelta.ShellExitCode == nil || *result.PresentationDelta.ShellExitCode != 0 {
				t.Fatalf("completed presentation = %#v, want exit code 0", result.PresentationDelta)
			}
			if log, err := os.ReadFile(snapshot.LogPath); err != nil || string(log) != test.output {
				t.Fatalf("retained log = %q, error=%v, want %q", string(log), err, test.output)
			}
			if test.later && callWriteStdin(t, pollTool, "write-later", map[string]any{"session_id": 1000, "yield_time_ms": 15_000, "max_output_tokens": 19}).IsError {
				t.Fatal("later poll = error, want independent success")
			}
		})
	}
}
