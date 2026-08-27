package shell

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"core/server/tools"
)

func assertOversizedOutputFailure(t *testing.T, result tools.Result, logPath string) {
	t.Helper()
	message := fmt.Sprintf(oversizedOutputMessageTemplate, logPath)
	want, _ := json.Marshal(map[string]string{"error": message})
	if !result.IsError || string(result.Output) != string(want) {
		t.Fatalf("failure = %s, want %s", result.Output, want)
	}
}

func assertGuardedPresentation(t *testing.T, result tools.Result, raw bool, backgrounded bool, exitCode *int) {
	t.Helper()
	delta := result.PresentationDelta
	if !result.IsError || delta == nil || delta.RawOutputRequested != raw ||
		delta.MovedToBackground != backgrounded {
		t.Fatalf("result = %+v, want guarded result with presentation facts", result)
	}
	if exitCode == nil && delta.ShellExitCode != nil ||
		exitCode != nil && (delta.ShellExitCode == nil || *delta.ShellExitCode != *exitCode) {
		t.Fatalf("exit code presentation = %+v, want %v", delta.ShellExitCode, exitCode)
	}
}

func TestExecCommandGuardPreservesOutputPathPresentationAndLog(t *testing.T) {
	manager := newBackgroundTestManager(t)
	tool := NewExecCommandTool(t.TempDir(), 16_000, 20, manager, "")
	const output = "123456789012345678901234567890123456789012345678"
	result := callExecCommand(t, tool, "guarded", map[string]any{
		"cmd": "printf '" + output + "'; exit 7", "shell": "/bin/sh", "login": false,
		"raw": true, "yield_time_ms": 1_000, "max_output_tokens": 11,
	})
	entries, err := os.ReadDir(manager.TempDir())
	if err != nil || len(entries) != 1 {
		t.Fatalf("retained log entries = %v, error=%v", entries, err)
	}
	logPath := filepath.Join(manager.TempDir(), entries[0].Name())
	assertOversizedOutputFailure(t, result, logPath)
	if got := decodeStringToolOutput(t, result); got != "" {
		t.Fatalf("guarded output = %q, want omitted command output", got)
	}
	if log, err := os.ReadFile(logPath); err != nil || string(log) != output {
		t.Fatalf("retained output = %q, error=%v", log, err)
	}
	exitCode := 7
	assertGuardedPresentation(t, result, true, false, &exitCode)
}

func TestExecCommandGuardBoundariesAndOrdinaryTruncation(t *testing.T) {
	ptr := func(value int) *int { return &value }
	tests := []struct {
		name, output string
		cap          *int
		full         bool
		truncated    bool
	}{
		{"output at half", strings.Repeat("1", 80), ptr(21), true, false},
		{"cap equal to half", strings.Repeat("1", 100), ptr(20), false, true},
		{"cap below half", strings.Repeat("1", 100), ptr(19), false, true},
		{"cap omitted", strings.Repeat("1", 84), nil, true, false},
		{"decoded escaping", strings.Repeat(`"\`, 40), ptr(21), true, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := newBackgroundTestManager(t)
			input := map[string]any{
				"cmd": "printf '%s' '" + test.output + "'", "shell": "/bin/sh",
				"login": false, "yield_time_ms": 1_000,
			}
			if test.cap != nil {
				input["max_output_tokens"] = *test.cap
			}
			result := callExecCommand(t, NewExecCommandTool(t.TempDir(), 16_000, 40, manager, ""), test.name, input)
			if result.IsError {
				t.Fatalf("unexpected error: %s", result.Output)
			}
			got := decodeStringToolOutput(t, result)
			if test.full != (got == test.output) {
				t.Fatalf("output = %q, want full=%t", got, test.full)
			}
			if test.truncated != (result.PresentationDelta != nil && result.PresentationDelta.OutputTruncated) {
				t.Fatalf("presentation = %+v, want truncated=%t", result.PresentationDelta, test.truncated)
			}
		})
	}
}

func TestRunningExecCommandGuardPreservesLifecycleAndIndependentPoll(t *testing.T) {
	manager := newShellTestManager(t, 50*time.Millisecond)
	tool := NewExecCommandTool(t.TempDir(), 16_000, 40, manager, "")
	const output = "12345678901234567890123456789012345678901234567890123456789012345678901234567890"
	result := callExecCommand(t, tool, "running", map[string]any{
		"cmd": "printf '" + output + "'; sleep 0.5", "shell": "/bin/sh", "login": false,
		"raw": true, "yield_time_ms": 50, "max_output_tokens": 21,
	})
	snapshot, err := manager.Snapshot("1000")
	if err != nil || !snapshot.Running {
		t.Fatalf("running snapshot = %+v, error=%v", snapshot, err)
	}
	assertOversizedOutputFailure(t, result, snapshot.LogPath)
	assertGuardedPresentation(t, result, true, true, nil)
	if log, err := os.ReadFile(snapshot.LogPath); err != nil || string(log) != output {
		t.Fatalf("retained output = %q, error=%v", log, err)
	}
	if poll := callWriteStdin(t, NewWriteStdinTool(16_000, 40, manager), "later", map[string]any{
		"session_id": 1000, "yield_time_ms": 15_000, "max_output_tokens": 19,
	}); poll.IsError {
		t.Fatalf("independent poll = %s", poll.Output)
	}
}

func TestWriteStdinGuardPreservesRunningCompletedEscapedAndIndependentPolls(t *testing.T) {
	const plain = "12345678901234567890123456789012345678901234567890123456789012345678901234567890"
	tests := []struct {
		name, command, output, chars string
		running, later               bool
	}{
		{"running", "read line; printf '" + plain + "'; sleep 0.8", plain, "go\n", true, true},
		{"completed", "sleep 0.1; printf '" + plain + "'", plain, "", false, false},
		{"escaped", "read line; printf '%s' '" + strings.Repeat(`"\`, 20) + "'; sleep 0.8", strings.Repeat(`"\`, 20), "go\n", true, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := newShellTestManager(t, 50*time.Millisecond)
			backgrounded := make(chan Snapshot, 1)
			manager.SetEventHandler(func(event Event) bool {
				if event.Type == EventBackgrounded {
					backgrounded <- event.Snapshot
				}
				return true
			})
			start := callExecCommand(t, NewExecCommandTool(t.TempDir(), 16_000, 40, manager, ""), "start", map[string]any{
				"cmd": test.command, "shell": "/bin/sh", "login": false, "tty": true, "yield_time_ms": 50,
			})
			if start.IsError {
				t.Fatalf("start = %s", start.Output)
			}
			snapshot := <-backgrounded
			sessionID, err := strconv.Atoi(snapshot.ID)
			if err != nil {
				t.Fatalf("session ID: %v", err)
			}
			input := map[string]any{"session_id": sessionID, "yield_time_ms": 15_000, "max_output_tokens": 21}
			if test.chars != "" {
				input["chars"], input["yield_time_ms"] = test.chars, 50
			}
			result := callWriteStdin(t, NewWriteStdinTool(16_000, 40, manager), "poll", input)
			assertOversizedOutputFailure(t, result, snapshot.LogPath)
			if !test.running && result.BackgroundSessionID == nil {
				t.Fatal("completed guarded poll lost its background completion identity")
			}
			if test.running {
				current, err := manager.Snapshot(snapshot.ID)
				if err != nil || !current.Running {
					t.Fatalf("guarded snapshot = %+v, error=%v", current, err)
				}
			}
			if test.later && callWriteStdin(t, NewWriteStdinTool(16_000, 40, manager), "later", map[string]any{
				"session_id": sessionID, "yield_time_ms": 15_000, "max_output_tokens": 19,
			}).IsError {
				t.Fatal("later independent poll returned an error")
			}
			if log, err := os.ReadFile(snapshot.LogPath); err != nil || string(log) != test.output {
				t.Fatalf("retained output = %q, error=%v", log, err)
			}
		})
	}
}
