package shell

import (
	"context"
	"core/internal/testharness/testsetup"
	"core/server/tools"
	"core/shared/config"
	"core/shared/sessionenv"
	"core/shared/toolspec"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func decodeStringToolOutput(t *testing.T, result tools.Result) string {
	t.Helper()
	var out string
	if err := json.Unmarshal(result.Output, &out); err == nil {
		return out
	}
	var wrapped struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(result.Output, &wrapped); err != nil {
		t.Fatalf("decode string output: %v", err)
	}
	return wrapped.Output
}

type shellToolCaller interface {
	Call(context.Context, tools.Call) (tools.Result, error)
}

func callShellTestTool(t *testing.T, tool shellToolCaller, id string, name toolspec.ID, input map[string]any) tools.Result {
	t.Helper()
	rawInput, _ := json.Marshal(input)
	result, err := tool.Call(context.Background(), tools.Call{ID: id, Name: name, Input: rawInput})
	if err != nil {
		t.Fatalf("%s call error: %v", name, err)
	}
	return result
}

func callExecCommand(t *testing.T, tool *ExecCommandTool, id string, input map[string]any) tools.Result {
	t.Helper()
	return callShellTestTool(t, tool, id, toolspec.ToolExecCommand, input)
}

func callWriteStdin(t *testing.T, tool *WriteStdinTool, id string, input map[string]any) tools.Result {
	t.Helper()
	return callShellTestTool(t, tool, id, toolspec.ToolWriteStdin, input)
}

func decodeWriteStdinToolOutput(t *testing.T, result tools.Result) writeStdinOutput {
	t.Helper()
	var output writeStdinOutput
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatalf("decode write_stdin output: %v", err)
	}
	return output
}

func waitForManagerCount(t *testing.T, manager *Manager, want int, timeout time.Duration) {
	t.Helper()
	if testsetup.Until(time.Now().Add(timeout), 25*time.Millisecond, func() bool {
		return manager.Count() == want
	}) {
		return
	}
	t.Fatalf("manager count = %d, want %d", manager.Count(), want)
}

func waitForEntryInteraction(t *testing.T, manager *Manager, id string, timeout time.Duration) {
	t.Helper()
	entry, err := manager.entry(id)
	if err != nil {
		t.Fatalf("background entry %s: %v", id, err)
	}
	testsetup.RequireUntil(t, time.Now().Add(timeout), time.Millisecond, func() bool {
		if !entry.interactMu.TryLock() {
			return true
		}
		entry.interactMu.Unlock()
		return false
	}, "timed out waiting for write_stdin to start interacting with session %s", id)
}

func writeExecutableScript(t *testing.T, contents string) string {
	t.Helper()
	return testsetup.WriteExecutable(t, "hook.sh", contents)
}

func newBackgroundTestManager(t *testing.T) *Manager {
	t.Helper()
	return newShellTestManager(t, 250*time.Millisecond)
}

func newShellTestManager(t *testing.T, minimumExecToBackground time.Duration) *Manager {
	t.Helper()
	manager, err := NewManager(WithMinimumExecToBgTime(minimumExecToBackground), WithCloseTimeouts(20*time.Millisecond, 200*time.Millisecond))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

func TestExecCommandSilentSuccessIsTerminalAndUnambiguous(t *testing.T) {
	workspace := t.TempDir()
	manager := newBackgroundTestManager(t)
	execTool := NewExecCommandTool(workspace, 16_000, manager, "")

	result := callExecCommand(t, execTool, "silent-success", map[string]any{
		"cmd":           "true",
		"shell":         "/bin/sh",
		"login":         false,
		"yield_time_ms": 1_000,
	})
	if result.IsError {
		t.Fatalf("unexpected exec_command error: %s", string(result.Output))
	}
	var rendered string
	if err := json.Unmarshal(result.Output, &rendered); err != nil {
		t.Fatalf("exec_command result must be a JSON string: %v", err)
	}
	if rendered == "" {
		t.Fatal("silent terminal completion must render non-empty content")
	}
	if result.PresentationDelta != nil && result.PresentationDelta.MovedToBackground {
		t.Fatalf("silent foreground completion must not be marked backgrounded: %+v", result.PresentationDelta)
	}
	waitForManagerCount(t, manager, 0, time.Second)
}

func TestExecCommandEmptyDefaultWorkdirReturnsManagerErrorWithoutExecuting(t *testing.T) {
	serverCWD := t.TempDir()
	t.Chdir(serverCWD)
	manager := newBackgroundTestManager(t)
	execTool := NewExecCommandTool("", 16_000, manager, "")
	call := tools.Call{
		ID:   "empty-default-workdir",
		Name: toolspec.ToolExecCommand,
	}
	input, err := json.Marshal(map[string]any{
		"cmd":           "touch exec-command-empty-default-side-effect",
		"shell":         "/bin/sh",
		"login":         false,
		"yield_time_ms": 1_000,
	})
	if err != nil {
		t.Fatalf("marshal exec_command input: %v", err)
	}
	call.Input = input

	_, managerErr := manager.Start(context.Background(), ExecRequest{
		Command: []string{"/bin/sh", "-c", "touch exec-command-empty-default-side-effect"},
		Workdir: "",
	})
	if managerErr == nil {
		t.Fatal("expected empty workdir manager error")
	}
	want := tools.ErrorResultWith(call, managerErr.Error(), marshalNoHTMLEscape)

	got, err := execTool.Call(context.Background(), call)
	if err != nil {
		t.Fatalf("exec_command call returned transport error: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("exec_command result = %#v, want %#v", got, want)
	}
	if _, err := os.Stat(filepath.Join(serverCWD, "exec-command-empty-default-side-effect")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty default workdir command side effect = %v, want os.ErrNotExist", err)
	}
	waitForManagerCount(t, manager, 0, time.Second)
}

func TestExecCommandWorkdirValidationErrors(t *testing.T) {
	tests := []struct {
		name, id, workdir, reason string
		prepare                   func(*testing.T, string)
	}{
		{
			name: "deleted default", id: "deleted-default-workdir", reason: missingWorkingDirectoryReason,
			prepare: func(t *testing.T, workspace string) {
				if err := os.RemoveAll(workspace); err != nil {
					t.Fatalf("remove default workdir: %v", err)
				}
			},
		},
		{
			name: "missing relative", id: "missing-relative-workdir",
			workdir: filepath.Join("nested", "..", "missing", ".", "child"), reason: missingWorkingDirectoryReason,
		},
		{
			name: "regular file", id: "file-workdir", workdir: "workdir-file",
			reason: nonDirectoryWorkingDirectoryReason,
			prepare: func(t *testing.T, workspace string) {
				if err := os.WriteFile(filepath.Join(workspace, "workdir-file"), []byte("not a directory"), 0o600); err != nil {
					t.Fatalf("write workdir file: %v", err)
				}
			},
		},
		{
			name: "file ancestor", id: "file-ancestor-workdir",
			workdir: filepath.Join("workdir-file", "child"), reason: missingWorkingDirectoryReason,
			prepare: func(t *testing.T, workspace string) {
				if err := os.WriteFile(filepath.Join(workspace, "workdir-file"), []byte("not a directory"), 0o600); err != nil {
					t.Fatalf("write workdir file: %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			resolved, err := filepath.Abs(ResolveWorkdir(workspace, tt.workdir))
			if err != nil {
				t.Fatalf("normalize workdir: %v", err)
			}
			if tt.prepare != nil {
				tt.prepare(t, workspace)
			}
			sideEffect := filepath.Join(t.TempDir(), "side-effect")
			input := map[string]any{
				"cmd": "touch " + sideEffect, "shell": "/bin/sh", "login": false, "yield_time_ms": 1_000,
			}
			if tt.workdir != "" {
				input["workdir"] = tt.workdir
			}
			rawInput, err := json.Marshal(input)
			if err != nil {
				t.Fatalf("marshal exec_command input: %v", err)
			}
			call := tools.Call{ID: tt.id, Name: toolspec.ToolExecCommand, Input: rawInput}
			manager := newBackgroundTestManager(t)
			got, err := NewExecCommandTool(workspace, 16_000, manager, "").Call(context.Background(), call)
			if err != nil {
				t.Fatalf("exec_command call returned transport error: %v", err)
			}
			wantMessage := strings.Join([]string{resolved, tt.reason, existingWorkingDirectoryHint}, " ")
			want := tools.ErrorResultWith(call, wantMessage, marshalNoHTMLEscape)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("exec_command result = %#v, want %#v", got, want)
			}
			if _, err := os.Stat(sideEffect); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("command side effect = %v, want os.ErrNotExist", err)
			}
			waitForManagerCount(t, manager, 0, time.Second)
		})
	}
}

func envSliceToMap(t *testing.T, in []string) map[string]string {
	t.Helper()
	out := make(map[string]string, len(in))
	for _, entry := range in {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			t.Fatalf("invalid env entry: %q", entry)
		}
		if _, exists := out[key]; exists {
			t.Fatalf("duplicate env key: %s", key)
		}
		out[key] = value
	}
	return out
}

func TestManagerStartEmbedsOwnerSessionIDInProcessEnv(t *testing.T) {
	manager := newBackgroundTestManager(t)
	result, err := manager.Start(context.Background(), ExecRequest{
		Command:        []string{"/bin/sh", "-c", "printf %s \"$" + sessionenv.SessionIDEnv + "\""},
		DisplayCommand: "print kent session id",
		OwnerSessionID: "session-env-123",
		Workdir:        t.TempDir(),
		YieldTime:      time.Second,
		MaxOutputChars: 1000,
	})
	if err != nil {
		t.Fatalf("start command: %v", err)
	}
	if result.Output != "session-env-123" {
		t.Fatalf("output = %q, want session-env-123", result.Output)
	}
	if result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("exit code = %v, want 0", result.ExitCode)
	}
}

func TestEnrichEnvAddsManagedRGConfigPathWhenAvailable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, _, err := config.EnsureManagedRGConfigFile(); err != nil {
		t.Fatalf("ensure managed rg config file: %v", err)
	}

	env := envSliceToMap(t, tools.EnrichShellEnvForSession([]string{"KEEP=1"}, ""))
	want := filepath.Join(home, config.ConfigDirName, "rg.conf")
	if env["RIPGREP_CONFIG_PATH"] != want {
		t.Fatalf("RIPGREP_CONFIG_PATH = %q, want %q", env["RIPGREP_CONFIG_PATH"], want)
	}
}

func TestEnrichEnvKeepsUserRIPGREPConfigPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, _, err := config.EnsureManagedRGConfigFile(); err != nil {
		t.Fatalf("ensure managed rg config file: %v", err)
	}

	env := envSliceToMap(t, tools.EnrichShellEnvForSession([]string{"RIPGREP_CONFIG_PATH=/tmp/user-rg.conf"}, ""))
	if env["RIPGREP_CONFIG_PATH"] != "/tmp/user-rg.conf" {
		t.Fatalf("RIPGREP_CONFIG_PATH = %q, want /tmp/user-rg.conf", env["RIPGREP_CONFIG_PATH"])
	}
}

func TestManagerSubscribeOutputStreamsTailAndEndsAtEOF(t *testing.T) {
	manager := newBackgroundTestManager(t)
	workspace := t.TempDir()

	result, err := manager.Start(context.Background(), ExecRequest{
		Command:        []string{"sh", "-c", "printf 'hello\\n'; sleep 0.3; printf 'world\\n'"},
		DisplayCommand: "tail-test",
		Workdir:        workspace,
		YieldTime:      250 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !result.Backgrounded {
		t.Fatalf("expected backgrounded process, got %+v", result)
	}

	sub, err := manager.SubscribeOutput(context.Background(), result.SessionID, 0)
	if err != nil {
		t.Fatalf("SubscribeOutput: %v", err)
	}
	defer func() { _ = sub.Close() }()

	first, err := sub.Next(context.Background())
	if err != nil {
		t.Fatalf("first Next: %v", err)
	}
	if first.ProcessID != result.SessionID || !strings.Contains(first.Text, "hello") {
		t.Fatalf("unexpected first chunk: %+v", first)
	}

	second, err := sub.Next(context.Background())
	if err != nil {
		t.Fatalf("second Next: %v", err)
	}
	if second.OffsetBytes <= first.OffsetBytes || second.NextOffsetBytes <= second.OffsetBytes || !strings.Contains(second.Text, "world") {
		t.Fatalf("unexpected second chunk: %+v", second)
	}

	if _, err := sub.Next(context.Background()); err != io.EOF {
		t.Fatalf("expected EOF after process exit, got %v", err)
	}

	tailSub, err := manager.SubscribeOutput(context.Background(), result.SessionID, second.NextOffsetBytes)
	if err != nil {
		t.Fatalf("SubscribeOutput from tail: %v", err)
	}
	defer func() { _ = tailSub.Close() }()
	if _, err := tailSub.Next(context.Background()); err != io.EOF {
		t.Fatalf("expected EOF for tail subscription at end, got %v", err)
	}
}

func TestManagerSubscribeOutputReceivesSingleLineWhileProcessKeepsRunning(t *testing.T) {
	manager := newBackgroundTestManager(t)
	workspace := t.TempDir()

	result, err := manager.Start(context.Background(), ExecRequest{
		Command:        []string{"sh", "-c", "printf 'ready\\n'; sleep 1"},
		DisplayCommand: "single-line-running",
		Workdir:        workspace,
		YieldTime:      250 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !result.Backgrounded {
		t.Fatalf("expected backgrounded process, got %+v", result)
	}
	defer func() { _ = manager.Kill(result.SessionID) }()

	sub, err := manager.SubscribeOutput(context.Background(), result.SessionID, 0)
	if err != nil {
		t.Fatalf("SubscribeOutput: %v", err)
	}
	defer func() { _ = sub.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	chunk, err := sub.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if !strings.Contains(chunk.Text, "ready") {
		t.Fatalf("expected ready output, got %+v", chunk)
	}
	snapshot, err := manager.Snapshot(result.SessionID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !snapshot.Running {
		t.Fatalf("expected process to still be running, got %+v", snapshot)
	}
}

func TestManagerInlineOutputUsesRecentOutputBeforeLogFlush(t *testing.T) {
	manager := newBackgroundTestManager(t)
	workspace := t.TempDir()

	result, err := manager.Start(context.Background(), ExecRequest{
		Command:        []string{"sh", "-c", "printf 'inline-ready\\n'; sleep 1"},
		DisplayCommand: "inline-recent",
		Workdir:        workspace,
		YieldTime:      250 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !result.Backgrounded {
		t.Fatalf("expected backgrounded process, got %+v", result)
	}
	defer func() { _ = manager.Kill(result.SessionID) }()

	preview, _, err := manager.InlineOutput(result.SessionID, 1024)
	if err != nil {
		t.Fatalf("InlineOutput: %v", err)
	}
	if !strings.Contains(preview, "inline-ready") {
		t.Fatalf("expected recent output fallback, got %q", preview)
	}
}

func TestManagerInlineOutputTruncatesRecentOutputFallback(t *testing.T) {
	manager := newBackgroundTestManager(t)
	workspace := t.TempDir()

	result, err := manager.Start(context.Background(), ExecRequest{
		Command:        []string{"sh", "-c", "printf '%0500d\\n' 1; sleep 1"},
		DisplayCommand: "inline-recent-truncated",
		Workdir:        workspace,
		YieldTime:      250 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !result.Backgrounded {
		t.Fatalf("expected backgrounded process, got %+v", result)
	}
	defer func() { _ = manager.Kill(result.SessionID) }()

	preview, _, err := manager.InlineOutput(result.SessionID, 80)
	if err != nil {
		t.Fatalf("InlineOutput: %v", err)
	}
	if len(preview) > 200 {
		t.Fatalf("expected truncated recent output fallback, got len=%d preview=%q", len(preview), preview)
	}
}

func TestManagerSubscribeOutputRejectsUnknownProcess(t *testing.T) {
	manager := newBackgroundTestManager(t)
	if _, err := manager.SubscribeOutput(context.Background(), "missing", 0); err == nil {
		t.Fatal("expected unknown process error")
	}
}

func TestManagerSubscribeOutputCloseUnblocksNext(t *testing.T) {
	manager := newBackgroundTestManager(t)
	workspace := t.TempDir()

	result, err := manager.Start(context.Background(), ExecRequest{
		Command:        []string{"sh", "-c", "sleep 1"},
		DisplayCommand: "tail-close-test",
		Workdir:        workspace,
		YieldTime:      250 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !result.Backgrounded {
		t.Fatalf("expected backgrounded process, got %+v", result)
	}

	sub, err := manager.SubscribeOutput(context.Background(), result.SessionID, 0)
	if err != nil {
		t.Fatalf("SubscribeOutput: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := sub.Next(context.Background())
		done <- err
	}()

	time.Sleep(20 * time.Millisecond)
	if err := sub.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-done:
		if err != io.EOF {
			t.Fatalf("expected EOF after Close, got %v", err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("timed out waiting for Next to unblock after Close")
	}
	_ = manager.Kill(result.SessionID)
}

func TestTruncateDoesNotDuplicateWholeOutputWhenShorterThanHeadTailWindow(t *testing.T) {
	in := strings.Repeat("x", 543)
	out, truncated, removed := truncateWithTemplate(in, 80, truncationBannerTemplate)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if removed <= 0 {
		t.Fatalf("expected positive removed bytes, got %d", removed)
	}
	if strings.Contains(out, "omitted -") {
		t.Fatalf("did not expect negative omitted bytes, got %q", out)
	}
	if strings.Count(out, in) > 0 {
		t.Fatalf("did not expect full input duplicated in output, got %q", out)
	}
	headLen, tailLen := truncationSegmentLengths(len(in), 80)
	wantMax := headLen + tailLen + len(fmt.Sprintf(truncationBannerTemplate, removed))
	if got := len(out); got > wantMax {
		t.Fatalf("expected bounded truncated output <= %d bytes, got %d", wantMax, got)
	}
	if len(out) >= len(in) {
		t.Fatalf("expected truncated output smaller than input, got out=%d in=%d", len(out), len(in))
	}
}

func TestWriteStdinPollingPreservesTerminalLifecycleForAllCompletionShapes(t *testing.T) {
	tests := []struct {
		name         string
		command      string
		wantExitCode int
		yieldTimeMS  int
	}{
		{name: "zero silent", command: "sleep 0.15", wantExitCode: 0, yieldTimeMS: 15_000},
		{name: "zero with output", command: "sleep 0.15; printf visible", wantExitCode: 0, yieldTimeMS: 15_000},
		{name: "non-zero silent", command: "sleep 0.15; exit 7", wantExitCode: 7, yieldTimeMS: 15_000},
		{name: "non-zero with output", command: "sleep 0.15; printf visible; exit 7", wantExitCode: 7, yieldTimeMS: 15_000},
		{name: "maximum output poll wait", command: "sleep 0.15", wantExitCode: 0, yieldTimeMS: 86_400_000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			manager := newShellTestManager(t, 50*time.Millisecond)
			execTool := NewExecCommandTool(workspace, 16_000, manager, "")
			pollTool := NewWriteStdinTool(16_000, manager)

			start := callExecCommand(t, execTool, "poll-start", map[string]any{
				"cmd":           tt.command,
				"shell":         "/bin/sh",
				"login":         false,
				"yield_time_ms": 50,
			})
			if start.IsError {
				t.Fatalf("unexpected exec_command error: %s", string(start.Output))
			}
			if start.PresentationDelta == nil || !start.PresentationDelta.MovedToBackground {
				t.Fatalf("expected background transition, got %+v", start.PresentationDelta)
			}
			snapshots := manager.List()
			if len(snapshots) != 1 {
				t.Fatalf("background snapshot count = %d, want 1", len(snapshots))
			}
			sessionID, err := strconv.Atoi(snapshots[0].ID)
			if err != nil {
				t.Fatalf("background session ID must be numeric: %v", err)
			}

			poll := callWriteStdin(t, pollTool, "poll-complete", map[string]any{
				"session_id":    sessionID,
				"yield_time_ms": tt.yieldTimeMS,
			})
			if poll.IsError {
				t.Fatalf("unexpected write_stdin error: %s", string(poll.Output))
			}
			output := decodeWriteStdinToolOutput(t, poll)
			if output.BackgroundSessionID != sessionID {
				t.Fatalf("background session ID = %d, want %d", output.BackgroundSessionID, sessionID)
			}
			if output.BackgroundRunning {
				t.Fatal("expected terminal polling response")
			}
			if !output.Backgrounded {
				t.Fatal("expected terminal polling response to preserve backgrounded lifecycle")
			}
			if output.BackgroundExitCode == nil || *output.BackgroundExitCode != tt.wantExitCode {
				t.Fatalf("background exit code = %v, want %d", output.BackgroundExitCode, tt.wantExitCode)
			}
			if output.Output == "" {
				t.Fatal("terminal polling response must contain non-empty presentation")
			}
			waitForManagerCount(t, manager, 0, time.Second)
		})
	}
}

func TestWriteStdinRejectsShortTimedOutputPolls(t *testing.T) {
	workspace := t.TempDir()
	manager := newShellTestManager(t, 50*time.Millisecond)
	execTool := NewExecCommandTool(workspace, 16_000, manager, "")
	pollTool := NewWriteStdinTool(16_000, manager)

	start := callExecCommand(t, execTool, "short-poll-start", map[string]any{
		"cmd":           "sleep 0.15",
		"shell":         "/bin/sh",
		"login":         false,
		"yield_time_ms": 50,
	})
	if start.IsError {
		t.Fatalf("unexpected exec_command error: %s", string(start.Output))
	}

	for _, test := range []struct {
		name        string
		yieldTimeMS int
		chars       string
	}{
		{name: "below minimum", yieldTimeMS: 14_999},
		{name: "zero", yieldTimeMS: 0},
		{name: "negative", yieldTimeMS: -1},
		{name: "above maximum", yieldTimeMS: 86_400_001},
	} {
		input := map[string]any{
			"session_id":    1000,
			"yield_time_ms": test.yieldTimeMS,
		}
		if test.chars != "" {
			input["chars"] = test.chars
		}
		rejected := callWriteStdin(t, pollTool, "short-poll-rejected", input)
		if !rejected.IsError {
			t.Fatalf("expected %s request to fail, got %+v", test.name, rejected)
		}
		var envelope struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rejected.Output, &envelope); err != nil {
			t.Fatalf("decode rejected write_stdin output: %v", err)
		}
		if envelope.Error == "" {
			t.Fatal("expected rejected write_stdin error value")
		}
		if rejected.Summary == nil || *rejected.Summary == "" {
			t.Fatal("expected rejected write_stdin summary")
		}
		if *rejected.Summary != envelope.Error {
			t.Fatalf("rejected summary = %q, want error value %q", *rejected.Summary, envelope.Error)
		}
	}

	accepted := callWriteStdin(t, pollTool, "short-poll-accepted", map[string]any{
		"session_id": 1000,
	})
	if accepted.IsError {
		t.Fatalf("expected process to remain usable after rejection: %s", string(accepted.Output))
	}
	waitForManagerCount(t, manager, 0, time.Second)
}

func TestWriteStdinCancellationReportsActiveProcess(t *testing.T) {
	workspace := t.TempDir()
	manager := newBackgroundTestManager(t)
	pollTool := NewWriteStdinTool(16_000, manager)

	result, err := manager.Start(context.Background(), ExecRequest{
		Command:        []string{"sh", "-c", "sleep 2"},
		DisplayCommand: "sleep 2",
		Workdir:        workspace,
		YieldTime:      250 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start background process: %v", err)
	}
	if !result.Backgrounded {
		t.Fatalf("expected backgrounded process, got %+v", result)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan tools.Result, 1)
	sessionID, err := strconv.Atoi(result.SessionID)
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	pollInput, _ := json.Marshal(map[string]any{
		"session_id":    sessionID,
		"yield_time_ms": 15_000,
	})
	pollCall := tools.Call{ID: "cancel-poll", Name: toolspec.ToolWriteStdin, Input: pollInput}
	go func() {
		pollResult, err := pollTool.Call(ctx, pollCall)
		if err != nil {
			t.Errorf("write_stdin call returned transport error: %v", err)
		}
		done <- pollResult
	}()

	waitForEntryInteraction(t, manager, result.SessionID, time.Second)
	cancel()

	select {
	case pollResult := <-done:
		if !pollResult.IsError {
			t.Fatalf("expected write_stdin error result, got %+v", pollResult)
		}
		pollErr := &PollingCanceledError{SessionID: result.SessionID, Active: true}
		want := tools.ErrorResultWith(pollCall, formatToolCallErrorDecoration("write_stdin", pollErr.Error()), marshalNoHTMLEscape)
		if !reflect.DeepEqual(pollResult, want) {
			t.Fatalf("write_stdin result = %#v, want %#v", pollResult, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for canceled write_stdin")
	}
	if snapshot, err := manager.Snapshot(result.SessionID); err != nil || !snapshot.Running {
		t.Fatalf("expected process to remain active after polling cancellation, snapshot=%+v err=%v", snapshot, err)
	}
}

func TestExecCommandCancellationReturnsUndecoratedBaseError(t *testing.T) {
	workspace := t.TempDir()
	readyMarker := filepath.Join(workspace, "exec-command-cancellation-ready")
	manager := newBackgroundTestManager(t)
	execTool := NewExecCommandTool(workspace, 16_000, manager, "")
	call := tools.Call{
		ID:   "exec-command-cancellation",
		Name: toolspec.ToolExecCommand,
	}
	var err error
	call.Input, err = json.Marshal(map[string]any{
		"cmd":           "touch " + readyMarker + "; while :; do :; done",
		"shell":         "/bin/sh",
		"login":         false,
		"yield_time_ms": 15_000,
	})
	if err != nil {
		t.Fatalf("marshal exec_command input: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type callResult struct {
		result tools.Result
		err    error
	}
	done := make(chan callResult, 1)
	go func() {
		result, err := execTool.Call(ctx, call)
		done <- callResult{result: result, err: err}
	}()

	testsetup.RequireUntil(t, time.Now().Add(time.Second), time.Millisecond, func() bool {
		_, err := os.Stat(readyMarker)
		return err == nil
	}, "timed out waiting for exec_command readiness marker")
	cancel()

	select {
	case completed := <-done:
		if completed.err != nil {
			t.Fatalf("exec_command call returned transport error: %v", completed.err)
		}
		want := tools.ErrorResultWith(call, canceledByUserMessage, marshalNoHTMLEscape)
		if !reflect.DeepEqual(completed.result, want) {
			t.Fatalf("exec_command result = %#v, want %#v", completed.result, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for canceled exec_command")
	}
	waitForManagerCount(t, manager, 0, time.Second)
}

func TestExecCommandReturnsClosedManagerErrorUnchanged(t *testing.T) {
	workspace := t.TempDir()
	manager := newBackgroundTestManager(t)
	if err := manager.Close(); err != nil {
		t.Fatalf("close manager: %v", err)
	}
	execTool := NewExecCommandTool(workspace, 16_000, manager, "")
	call := tools.Call{
		ID:   "closed-manager",
		Name: toolspec.ToolExecCommand,
	}
	input, err := json.Marshal(map[string]any{
		"cmd":           "true",
		"shell":         "/bin/sh",
		"login":         false,
		"yield_time_ms": 1_000,
	})
	if err != nil {
		t.Fatalf("marshal exec_command input: %v", err)
	}
	call.Input = input
	_, managerErr := manager.Start(context.Background(), ExecRequest{
		Command: []string{"/bin/sh", "-c", "true"},
		Workdir: workspace,
	})
	if managerErr == nil {
		t.Fatal("expected closed manager error")
	}
	want := tools.ErrorResultWith(call, managerErr.Error(), marshalNoHTMLEscape)

	got, err := execTool.Call(context.Background(), call)
	if err != nil {
		t.Fatalf("exec_command call returned transport error: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("exec_command result = %#v, want %#v", got, want)
	}
}

func TestManagerWriteStdinCancellationPreservesContextCanceled(t *testing.T) {
	workspace := t.TempDir()
	manager := newBackgroundTestManager(t)

	result, err := manager.Start(context.Background(), ExecRequest{
		Command:        []string{"sh", "-c", "sleep 2"},
		DisplayCommand: "sleep 2",
		Workdir:        workspace,
		YieldTime:      250 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start background process: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = manager.WriteStdin(ctx, WriteRequest{SessionID: result.SessionID, YieldTime: 5 * time.Second})
	if err == nil {
		t.Fatal("expected canceled polling error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected errors.Is(..., context.Canceled), got %v", err)
	}
	var pollErr *PollingCanceledError
	if !errors.As(err, &pollErr) {
		t.Fatalf("expected PollingCanceledError, got %T %v", err, err)
	}
	if !pollErr.Active {
		t.Fatalf("expected active process metadata, got %+v", pollErr)
	}
}

func TestExecCommandReportsNonZeroExitCode(t *testing.T) {
	workspace := t.TempDir()
	manager := newBackgroundTestManager(t)
	execTool := NewExecCommandTool(workspace, 16_000, manager, "")

	result := callExecCommand(t, execTool, "nonzero-1", map[string]any{
		"cmd":           "printf 'bad\\n'; exit 7",
		"shell":         "/bin/sh",
		"login":         false,
		"yield_time_ms": 1_000,
	})
	if result.IsError {
		t.Fatalf("unexpected exec_command error: %s", string(result.Output))
	}
	var output string
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatalf("exec_command result must be a JSON string: %v", err)
	}
	if output == "" {
		t.Fatal("non-zero completion with output must render non-empty content")
	}
	if result.PresentationDelta == nil ||
		result.PresentationDelta.ShellExitCode == nil ||
		*result.PresentationDelta.ShellExitCode != 7 {
		t.Fatalf("non-zero shell presentation delta = %+v, want typed exit code 7", result.PresentationDelta)
	}
}

func TestWriteStdinWarnsAndRetriesWhenFullLogReadFails(t *testing.T) {
	workspace := t.TempDir()
	manager := newShellTestManager(t, 50*time.Millisecond)
	pollTool := NewWriteStdinTool(16_000, manager)

	result, err := manager.Start(context.Background(), ExecRequest{
		Command:        []string{"sh", "-c", "sleep 0.15; printf done"},
		DisplayCommand: "delayed-done",
		Workdir:        workspace,
		YieldTime:      50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !result.Backgrounded {
		t.Fatalf("expected backgrounded result, got %+v", result)
	}
	logPath := result.OutputPath
	backupPath := logPath + ".bak"
	sessionID, err := strconv.Atoi(result.SessionID)
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}

	testsetup.RequireUntil(t, time.Now().Add(time.Second), 5*time.Millisecond, func() bool {
		snapshot, snapshotErr := manager.Snapshot(result.SessionID)
		info, statErr := os.Stat(logPath)
		return snapshotErr == nil && !snapshot.Running && statErr == nil && info.Size() > 0
	}, "timed out waiting for completed process output")
	if err := os.Rename(logPath, backupPath); err != nil {
		t.Fatalf("rename log away: %v", err)
	}

	pollInput := map[string]any{
		"session_id": sessionID,
	}
	first := callWriteStdin(t, pollTool, "log-missing-1", pollInput)
	if first.IsError {
		t.Fatalf("unexpected first write_stdin error: %s", string(first.Output))
	}
	firstText := decodeStringToolOutput(t, first)
	if !strings.Contains(firstText, "failed to read full output log") {
		t.Fatalf("expected full-log warning, got %q", firstText)
	}

	if err := os.Rename(backupPath, logPath); err != nil {
		t.Fatalf("restore log: %v", err)
	}
	second := callWriteStdin(t, pollTool, "log-missing-2", pollInput)
	if second.IsError {
		t.Fatalf("unexpected second write_stdin error: %s", string(second.Output))
	}
	secondText := decodeStringToolOutput(t, second)
	if strings.Contains(secondText, "failed to read full output log") {
		t.Fatalf("did not expect warning after log restored, got %q", secondText)
	}
	if !strings.Contains(secondText, "done") {
		t.Fatalf("expected restored full output, got %q", secondText)
	}
}

func TestExecCommandClampsShortYieldTime(t *testing.T) {
	const commandDelay = 100 * time.Millisecond
	const clampedForegroundWindow = 2 * time.Second

	workspace := t.TempDir()
	manager, err := NewManager(WithMinimumExecToBgTime(clampedForegroundWindow))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	execTool := NewExecCommandTool(workspace, 16_000, manager, "")

	result := callExecCommand(t, execTool, "clamp-1", map[string]any{
		"cmd":           fmt.Sprintf("sleep %.1f; echo done", commandDelay.Seconds()),
		"shell":         "/bin/sh",
		"login":         false,
		"yield_time_ms": 20,
	})
	if result.IsError {
		t.Fatalf("unexpected exec_command error: %s", string(result.Output))
	}
	if text := decodeStringToolOutput(t, result); !strings.Contains(text, "done") {
		t.Fatalf("expected command output, got %q", text)
	}
	if manager.Count() != 0 {
		t.Fatalf("manager count = %d, want 0", manager.Count())
	}
}

func TestNormalizeWriteYieldTimeDoesNotCapLongPolls(t *testing.T) {
	yieldTime := normalizeWriteYieldTime(5*time.Minute, defaultWriteYieldTime)
	if yieldTime != 5*time.Minute {
		t.Fatalf("yield time = %s, want %s", yieldTime, 5*time.Minute)
	}

	yieldTime = normalizeWriteYieldTime(100*time.Millisecond, defaultWriteYieldTime)
	if yieldTime != minWriteYieldTime {
		t.Fatalf("yield time = %s, want %s for short input", yieldTime, minWriteYieldTime)
	}

	yieldTime = normalizeWriteYieldTime(0, defaultWriteYieldTime)
	if yieldTime != defaultWriteYieldTime {
		t.Fatalf("yield time = %s, want %s for zero input", yieldTime, defaultWriteYieldTime)
	}
}

func TestWriteStdinPollHonorsRequestedDuration(t *testing.T) {
	workspace := t.TempDir()
	manager := newShellTestManager(t, 50*time.Millisecond)
	execTool := NewExecCommandTool(workspace, 16_000, manager, "")
	pollTool := NewWriteStdinTool(16_000, manager)

	result := callExecCommand(t, execTool, "poll-duration-exec", map[string]any{
		"cmd":           "read line; sleep 0.6",
		"shell":         "/bin/sh",
		"login":         false,
		"tty":           true,
		"yield_time_ms": 50,
	})
	if result.IsError {
		t.Fatalf("unexpected exec_command error: %s", string(result.Output))
	}

	pollInput := map[string]any{
		"session_id":        1000,
		"chars":             "\n",
		"yield_time_ms":     300,
		"max_output_tokens": 32,
	}
	start := time.Now()
	pollResult := callWriteStdin(t, pollTool, "poll-duration-poll", pollInput)
	elapsed := time.Since(start)
	if pollResult.IsError {
		t.Fatalf("unexpected write_stdin error: %s", string(pollResult.Output))
	}
	if elapsed < 250*time.Millisecond {
		t.Fatalf("poll returned too early: %s", elapsed)
	}
	if elapsed > time.Second {
		t.Fatalf("poll took too long: %s", elapsed)
	}

	var payload writeStdinOutput
	if err := json.Unmarshal(pollResult.Output, &payload); err != nil {
		t.Fatalf("decode write_stdin output: %v", err)
	}
	if !payload.BackgroundRunning {
		t.Fatalf("expected session to still be running after requested poll window, got %+v", payload)
	}
	if !payload.Backgrounded {
		t.Fatalf("expected session to remain backgrounded, got %+v", payload)
	}
	waitForManagerCount(t, manager, 0, 2*time.Second)
}

func TestWriteStdinWhitespaceInputRemainsInputAtLongWaits(t *testing.T) {
	tests := []struct {
		name        string
		yieldTimeMS int
	}{
		{name: "above output poll maximum", yieldTimeMS: 86_400_001},
		{name: "maximum integer", yieldTimeMS: math.MaxInt},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			manager := newShellTestManager(t, 50*time.Millisecond)
			execTool := NewExecCommandTool(workspace, 16_000, manager, "")
			stdinTool := NewWriteStdinTool(16_000, manager)

			start := callExecCommand(t, execTool, "whitespace-input-start", map[string]any{
				"cmd":           "read line; sleep 0.6",
				"shell":         "/bin/sh",
				"login":         false,
				"tty":           true,
				"yield_time_ms": 50,
			})
			if start.IsError {
				t.Fatalf("unexpected exec_command error: %s", string(start.Output))
			}

			started := time.Now()
			result := callWriteStdin(t, stdinTool, "whitespace-input", map[string]any{
				"session_id":    1000,
				"chars":         "\n",
				"yield_time_ms": tt.yieldTimeMS,
			})
			elapsed := time.Since(started)
			if result.IsError {
				t.Fatalf("unexpected write_stdin error: %s", string(result.Output))
			}
			if elapsed < 500*time.Millisecond {
				t.Fatalf("write_stdin returned before post-input delay: %s", elapsed)
			}
			if elapsed > 2*time.Second {
				t.Fatalf("write_stdin took too long: %s", elapsed)
			}
			output := decodeWriteStdinToolOutput(t, result)
			if output.BackgroundRunning || !output.Backgrounded ||
				output.BackgroundExitCode == nil || *output.BackgroundExitCode != 0 {
				t.Fatalf("completed whitespace input output = %+v", output)
			}
			waitForManagerCount(t, manager, 0, time.Second)
		})
	}
}

func TestExecCommandForegroundTruncationSetsPresentationMetadata(t *testing.T) {
	workspace := t.TempDir()
	manager := newBackgroundTestManager(t)
	execTool := NewExecCommandTool(workspace, 16_000, manager, "")

	result := callExecCommand(t, execTool, "fg-trunc-1", map[string]any{
		"cmd":               "i=0; while [ $i -lt 400 ]; do printf x; i=$((i+1)); done",
		"shell":             "/bin/sh",
		"login":             false,
		"yield_time_ms":     2_000,
		"max_output_tokens": 10,
	})
	if result.IsError {
		t.Fatalf("unexpected exec_command error: %s", string(result.Output))
	}
	if result.PresentationDelta == nil || !result.PresentationDelta.OutputTruncated {
		t.Fatalf("expected foreground truncation presentation delta, got %+v", result.PresentationDelta)
	}
	if manager.Count() != 0 {
		t.Fatalf("manager count = %d, want 0", manager.Count())
	}
}

func TestExecCommandRawOutputAddsPresentationMetadata(t *testing.T) {
	workspace := t.TempDir()
	manager := newBackgroundTestManager(t)
	execTool := NewExecCommandTool(workspace, 16_000, manager, "")

	result := callExecCommand(t, execTool, "raw-presentation-1", map[string]any{
		"cmd":           "printf raw",
		"shell":         "/bin/sh",
		"login":         false,
		"raw":           true,
		"yield_time_ms": 2_000,
	})
	if result.IsError {
		t.Fatalf("unexpected exec_command error: %s", string(result.Output))
	}
	if result.PresentationDelta == nil || !result.PresentationDelta.RawOutputRequested || result.PresentationDelta.OutputTruncated {
		t.Fatalf("expected raw output presentation delta without truncation, got %+v", result.PresentationDelta)
	}
}

func TestWriteStdinRawSessionAddsPresentationMetadata(t *testing.T) {
	workspace := t.TempDir()
	manager := newShellTestManager(t, 50*time.Millisecond)
	execTool := NewExecCommandTool(workspace, 16_000, manager, "")
	stdinTool := NewWriteStdinTool(16_000, manager)

	result := callExecCommand(t, execTool, "raw-tty-1", map[string]any{
		"cmd":           "read line; printf '\\033[31m%s\\033[0m' \"$line\"",
		"shell":         "/bin/sh",
		"login":         false,
		"raw":           true,
		"tty":           true,
		"yield_time_ms": 50,
	})
	if result.IsError {
		t.Fatalf("unexpected exec_command error: %s", string(result.Output))
	}

	stdinResult := callWriteStdin(t, stdinTool, "raw-tty-2", map[string]any{
		"session_id":    1000,
		"chars":         "raw app\n",
		"yield_time_ms": 2_000,
	})
	if stdinResult.IsError {
		t.Fatalf("unexpected write_stdin error: %s", string(stdinResult.Output))
	}
	if stdinResult.PresentationDelta == nil || !stdinResult.PresentationDelta.RawOutputRequested || stdinResult.PresentationDelta.OutputTruncated {
		t.Fatalf("expected raw write_stdin presentation delta without truncation, got %+v", stdinResult.PresentationDelta)
	}
	waitForManagerCount(t, manager, 0, time.Second)
}

func TestWriteStdinSendsInputToInteractiveProcess(t *testing.T) {
	workspace := t.TempDir()
	manager := newShellTestManager(t, 50*time.Millisecond)
	execTool := NewExecCommandTool(workspace, 16_000, manager, "")
	stdinTool := NewWriteStdinTool(16_000, manager)

	result := callExecCommand(t, execTool, "tty-1", map[string]any{
		"cmd":           "read line; echo $line",
		"shell":         "/bin/sh",
		"login":         false,
		"tty":           true,
		"yield_time_ms": 50,
	})
	if result.IsError {
		t.Fatalf("unexpected exec_command error: %s", string(result.Output))
	}
	if manager.Count() != 1 {
		t.Fatalf("manager count = %d, want 1", manager.Count())
	}

	stdinResult := callWriteStdin(t, stdinTool, "tty-2", map[string]any{
		"session_id":    1000,
		"chars":         "hello app\n",
		"yield_time_ms": 86_400_001,
	})
	if stdinResult.IsError {
		t.Fatalf("unexpected write_stdin error: %s", string(stdinResult.Output))
	}
	stdinOutput := decodeWriteStdinToolOutput(t, stdinResult)
	if stdinOutput.BackgroundSessionID != 1000 || stdinOutput.BackgroundRunning || !stdinOutput.Backgrounded ||
		stdinOutput.BackgroundExitCode == nil || *stdinOutput.BackgroundExitCode != 0 || stdinOutput.Output == "" {
		t.Fatalf("completed interactive write_stdin output = %+v", stdinOutput)
	}
	waitForManagerCount(t, manager, 0, time.Second)
}

func TestWriteStdinCompletionTruncationSetsPresentationMetadata(t *testing.T) {
	workspace := t.TempDir()
	manager := newShellTestManager(t, 50*time.Millisecond)
	execTool := NewExecCommandTool(workspace, 16_000, manager, "")
	stdinTool := NewWriteStdinTool(16_000, manager)

	result := callExecCommand(t, execTool, "tty-trunc-1", map[string]any{
		"cmd":           "read line; printf '%s' \"$line\"",
		"shell":         "/bin/sh",
		"login":         false,
		"tty":           true,
		"yield_time_ms": 50,
	})
	if result.IsError {
		t.Fatalf("unexpected exec_command error: %s", string(result.Output))
	}

	stdinResult := callWriteStdin(t, stdinTool, "tty-trunc-2", map[string]any{
		"session_id":        1000,
		"chars":             strings.Repeat("x", 400) + "\n",
		"yield_time_ms":     2_000,
		"max_output_tokens": 10,
	})
	if stdinResult.IsError {
		t.Fatalf("unexpected write_stdin error: %s", string(stdinResult.Output))
	}
	if stdinResult.PresentationDelta == nil || !stdinResult.PresentationDelta.OutputTruncated {
		t.Fatalf("expected write_stdin truncation presentation delta, got %+v", stdinResult.PresentationDelta)
	}
	waitForManagerCount(t, manager, 0, 3*time.Second)
}

func TestWriteStdinPreservesBackgroundSummaryTruncationMetadata(t *testing.T) {
	workspace := t.TempDir()
	manager := newShellTestManager(t, 50*time.Millisecond)
	execTool := NewExecCommandTool(workspace, 16_000, manager, "")
	stdinTool := NewWriteStdinTool(16_000, manager)

	result := callExecCommand(t, execTool, "tty-summary-trunc-1", map[string]any{
		"cmd":           "read line; head -c 2200000 /dev/zero | tr '\\0' x",
		"shell":         "/bin/sh",
		"login":         false,
		"tty":           true,
		"yield_time_ms": 50,
	})
	if result.IsError {
		t.Fatalf("unexpected exec_command error: %s", string(result.Output))
	}

	stdinResult := callWriteStdin(t, stdinTool, "tty-summary-trunc-2", map[string]any{
		"session_id":        1000,
		"chars":             "go\n",
		"yield_time_ms":     5_000,
		"max_output_tokens": 10,
	})
	if stdinResult.IsError {
		t.Fatalf("unexpected write_stdin error: %s", string(stdinResult.Output))
	}
	if stdinResult.PresentationDelta == nil || !stdinResult.PresentationDelta.OutputTruncated {
		t.Fatalf("expected source truncation presentation delta, got %+v", stdinResult.PresentationDelta)
	}
	waitForManagerCount(t, manager, 0, 3*time.Second)
}
