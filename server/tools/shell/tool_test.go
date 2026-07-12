package shell

import (
	"context"
	"core/server/tools"
	"core/server/tools/shell/postprocess"
	"core/shared/config"
	"core/shared/sessionenv"
	"core/shared/toolspec"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if manager.Count() == want {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("manager count = %d, want %d", manager.Count(), want)
}

func waitForEntryInteraction(t *testing.T, manager *Manager, id string, timeout time.Duration) {
	t.Helper()
	entry, err := manager.entry(id)
	if err != nil {
		t.Fatalf("background entry %s: %v", id, err)
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !entry.interactMu.TryLock() {
			return
		}
		entry.interactMu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for write_stdin to start interacting with session %s", id)
}

func writeExecutableScript(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hook.sh")
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return path
}

func newBackgroundTestManager(t *testing.T) *Manager {
	t.Helper()
	manager, err := NewManager(WithMinimumExecToBgTime(250*time.Millisecond), WithCloseTimeouts(20*time.Millisecond, 200*time.Millisecond))
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

func TestEnrichEnvOverridesNonInteractiveDefaults(t *testing.T) {
	env := envSliceToMap(t, tools.EnrichShellEnvForSession([]string{
		"TERM=xterm-256color",
		"AGENT=other",
		"GIT_EDITOR=vim",
		"PAGER=less",
		"NO_COLOR=0",
		"DOCKER_CLI_HINTS=true",
		"BUILDKIT_PROGRESS=auto",
		"COMPOSE_PROGRESS=auto",
		"COMPOSE_ANSI=always",
		"npm_config_progress=true",
		"YARN_ENABLE_PROGRESS_BARS=true",
		"KEEP=1",
	}, ""))

	if env["TERM"] != "dumb" {
		t.Fatalf("TERM = %q, want dumb", env["TERM"])
	}
	if env["AGENT"] != "kent" {
		t.Fatalf("AGENT = %q, want kent", env["AGENT"])
	}
	if env["GIT_EDITOR"] != ":" {
		t.Fatalf("GIT_EDITOR = %q, want :", env["GIT_EDITOR"])
	}
	if env["PAGER"] != "cat" {
		t.Fatalf("PAGER = %q, want cat", env["PAGER"])
	}
	if env["NO_COLOR"] != "1" {
		t.Fatalf("NO_COLOR = %q, want 1", env["NO_COLOR"])
	}
	if env["GIT_TERMINAL_PROMPT"] != "0" {
		t.Fatalf("GIT_TERMINAL_PROMPT = %q, want 0", env["GIT_TERMINAL_PROMPT"])
	}
	if env["DOCKER_CLI_HINTS"] != "false" {
		t.Fatalf("DOCKER_CLI_HINTS = %q, want false", env["DOCKER_CLI_HINTS"])
	}
	if env["BUILDKIT_PROGRESS"] != "plain" {
		t.Fatalf("BUILDKIT_PROGRESS = %q, want plain", env["BUILDKIT_PROGRESS"])
	}
	if env["COMPOSE_PROGRESS"] != "plain" {
		t.Fatalf("COMPOSE_PROGRESS = %q, want plain", env["COMPOSE_PROGRESS"])
	}
	if env["COMPOSE_ANSI"] != "never" {
		t.Fatalf("COMPOSE_ANSI = %q, want never", env["COMPOSE_ANSI"])
	}
	if env["npm_config_progress"] != "false" {
		t.Fatalf("npm_config_progress = %q, want false", env["npm_config_progress"])
	}
	if env["YARN_ENABLE_PROGRESS_BARS"] != "false" {
		t.Fatalf("YARN_ENABLE_PROGRESS_BARS = %q, want false", env["YARN_ENABLE_PROGRESS_BARS"])
	}
	if env["KEEP"] != "1" {
		t.Fatalf("KEEP = %q, want 1", env["KEEP"])
	}
}

func TestEnrichEnvForSessionEmbedsOwnerSessionID(t *testing.T) {
	env := envSliceToMap(t, tools.EnrichShellEnvForSession([]string{
		"KENT_SESSION_ID=stale",
		"KEEP=1",
	}, "session-abc"))

	if env[sessionenv.SessionIDEnv] != "session-abc" {
		t.Fatalf("KENT_SESSION_ID = %q, want session-abc", env[sessionenv.SessionIDEnv])
	}
	if env["KEEP"] != "1" {
		t.Fatalf("KEEP = %q, want 1", env["KEEP"])
	}
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

func TestSanitizeOutputStripsANSIAndControlSequences(t *testing.T) {
	in := "\x1b[31mred\x1b[0m\r\nline2\a\b\tok\rline3"
	out := postprocess.SanitizeOutput(in)

	if strings.Contains(out, "\x1b[") {
		t.Fatalf("output still contains ANSI escape: %q", out)
	}
	if strings.ContainsAny(out, "\a\b\r") {
		t.Fatalf("output still contains control chars: %q", out)
	}
	if !strings.Contains(out, "red\nline2\tok\nline3") {
		t.Fatalf("sanitized output mismatch: %q", out)
	}
}

func TestTruncateBannerUsesByteWording(t *testing.T) {
	in := strings.Repeat("a", headTailSize+headTailSize+10)
	out, truncated, removed := truncateWithTemplate(in, 100, truncationBannerTemplate)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if removed <= 0 {
		t.Fatalf("expected positive removed bytes, got %d", removed)
	}
	if !strings.Contains(out, "omitted ") || !strings.Contains(out, " bytes.") {
		t.Fatalf("expected byte-based truncation banner, output = %q", out)
	}
}

func TestTruncateWhitespaceOnlyOutputDoesNotCreateBanner(t *testing.T) {
	output, truncated, removed := truncateWithTemplate(strings.Repeat(" ", 4096), 80, truncationBannerTemplate)
	if output != "" || truncated || removed != 0 {
		t.Fatalf("whitespace-only output truncation = (%q, %t, %d), want empty untruncated output", output, truncated, removed)
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
	if len(preview) > 200 || !strings.Contains(preview, "Omitted") {
		t.Fatalf("expected truncated recent output fallback, got len=%d preview=%q", len(preview), preview)
	}
}

func TestManagerSubscribeOutputRejectsInvalidOffset(t *testing.T) {
	manager := newBackgroundTestManager(t)
	if _, err := manager.SubscribeOutput(context.Background(), "proc-1", -1); err == nil {
		t.Fatal("expected invalid offset error")
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

func TestTruncateBackgroundOutputBannerReferencesLogFile(t *testing.T) {
	in := strings.Repeat("a", headTailSize+headTailSize+10)
	out, truncated, removed := truncateWithTemplate(in, 100, backgroundTruncationBannerTemplate)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if removed <= 0 {
		t.Fatalf("expected positive removed bytes, got %d", removed)
	}
	if !strings.Contains(out, "Omitted ") || !strings.Contains(out, "read log file for details") {
		t.Fatalf("expected background truncation banner to point to the log file, output = %q", out)
	}
	if strings.Contains(out, "Consider using more targeted commands") {
		t.Fatalf("did not expect foreground truncation guidance in background output, got %q", out)
	}
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

func TestExecCommandMovesToBackgroundAndPollsToCompletion(t *testing.T) {
	workspace := t.TempDir()
	manager := newBackgroundTestManager(t)
	execTool := NewExecCommandTool(workspace, 16_000, manager, "")
	pollTool := NewWriteStdinTool(16_000, manager)

	result := callExecCommand(t, execTool, "bg-1", map[string]any{
		"cmd":           "sleep 0.3; echo done; sleep 0.3",
		"shell":         "/bin/sh",
		"login":         false,
		"yield_time_ms": 250,
	})
	if result.IsError {
		t.Fatalf("unexpected exec_command error: %s", string(result.Output))
	}
	if manager.Count() != 1 {
		t.Fatalf("manager count = %d, want 1", manager.Count())
	}
	if result.PresentationDelta == nil || !result.PresentationDelta.MovedToBackground {
		t.Fatalf("expected backgrounded presentation delta, got %+v", result.PresentationDelta)
	}
	snapshots := manager.List()
	if len(snapshots) != 1 {
		t.Fatalf("background snapshot count = %d, want 1", len(snapshots))
	}
	if _, err := manager.Snapshot(snapshots[0].ID); err != nil {
		t.Fatalf("background session must be pollable: %v", err)
	}

	pollResult := callWriteStdin(t, pollTool, "bg-2", map[string]any{
		"session_id":    1000,
		"yield_time_ms": 800,
	})
	if pollResult.IsError {
		t.Fatalf("unexpected write_stdin error: %s", string(pollResult.Output))
	}
	pollOutput := decodeWriteStdinToolOutput(t, pollResult)
	if pollOutput.BackgroundSessionID != 1000 {
		t.Fatalf("background session ID = %d, want 1000", pollOutput.BackgroundSessionID)
	}
	if pollOutput.BackgroundRunning {
		t.Fatal("expected completed background process")
	}
	if !pollOutput.Backgrounded {
		t.Fatal("expected terminal polling result to retain background lifecycle")
	}
	if pollOutput.BackgroundExitCode == nil || *pollOutput.BackgroundExitCode != 0 {
		t.Fatalf("background exit code = %v, want 0", pollOutput.BackgroundExitCode)
	}
	if pollOutput.Output == "" {
		t.Fatal("completed polling output must be non-empty")
	}
	waitForManagerCount(t, manager, 0, time.Second)
}

func TestWriteStdinPollingPreservesTerminalLifecycleForAllCompletionShapes(t *testing.T) {
	tests := []struct {
		name         string
		command      string
		wantExitCode int
	}{
		{name: "zero silent", command: "sleep 0.3", wantExitCode: 0},
		{name: "zero with output", command: "sleep 0.3; printf visible", wantExitCode: 0},
		{name: "non-zero silent", command: "sleep 0.3; exit 7", wantExitCode: 7},
		{name: "non-zero with output", command: "sleep 0.3; printf visible; exit 7", wantExitCode: 7},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			manager := newBackgroundTestManager(t)
			execTool := NewExecCommandTool(workspace, 16_000, manager, "")
			pollTool := NewWriteStdinTool(16_000, manager)

			start := callExecCommand(t, execTool, "poll-start", map[string]any{
				"cmd":           tt.command,
				"shell":         "/bin/sh",
				"login":         false,
				"yield_time_ms": 250,
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
				"yield_time_ms": 800,
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
	go func() {
		sessionID, err := strconv.Atoi(result.SessionID)
		if err != nil {
			t.Errorf("parse session id: %v", err)
			done <- tools.Result{}
			return
		}
		pollInput, _ := json.Marshal(map[string]any{
			"session_id":    sessionID,
			"yield_time_ms": 5_000,
		})
		pollResult, err := pollTool.Call(ctx, tools.Call{ID: "cancel-poll", Name: toolspec.ToolWriteStdin, Input: pollInput})
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
		if !strings.Contains(pollResult.Summary, "Canceled polling by user, process active") {
			t.Fatalf("expected active-process cancellation summary, got %q", pollResult.Summary)
		}
		if strings.Contains(pollResult.Summary, "context canceled") {
			t.Fatalf("did not expect raw context cancellation summary, got %q", pollResult.Summary)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for canceled write_stdin")
	}
	if snapshot, err := manager.Snapshot(result.SessionID); err != nil || !snapshot.Running {
		t.Fatalf("expected process to remain active after polling cancellation, snapshot=%+v err=%v", snapshot, err)
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

func TestExecCommandExportsAgentEnv(t *testing.T) {
	workspace := t.TempDir()
	manager := newBackgroundTestManager(t)
	execTool := NewExecCommandTool(workspace, 16_000, manager, "")

	result := callExecCommand(t, execTool, "agent-env", map[string]any{
		"cmd":           "printf '%s' \"$AGENT\"",
		"shell":         "/bin/sh",
		"login":         false,
		"yield_time_ms": 1_000,
	})
	if result.IsError {
		t.Fatalf("unexpected exec_command error: %s", string(result.Output))
	}
	if got := decodeStringToolOutput(t, result); !strings.Contains(got, "kent") {
		t.Fatalf("expected AGENT=kent in shell output, got %q", got)
	}
}

func TestExecCommandBackgroundProcessExportsAgentEnv(t *testing.T) {
	workspace := t.TempDir()
	manager := newBackgroundTestManager(t)
	execTool := NewExecCommandTool(workspace, 16_000, manager, "")
	pollTool := NewWriteStdinTool(16_000, manager)

	result := callExecCommand(t, execTool, "agent-env-bg-start", map[string]any{
		"cmd":           "sleep 0.35; printf '%s' \"$AGENT\"",
		"shell":         "/bin/sh",
		"login":         false,
		"yield_time_ms": 250,
	})
	if result.IsError {
		t.Fatalf("unexpected exec_command error: %s", string(result.Output))
	}
	if result.PresentationDelta == nil || !result.PresentationDelta.MovedToBackground {
		t.Fatalf("expected background transition, got %+v", result.PresentationDelta)
	}

	pollResult := callWriteStdin(t, pollTool, "agent-env-bg-poll", map[string]any{
		"session_id":    1000,
		"yield_time_ms": 800,
	})
	if pollResult.IsError {
		t.Fatalf("unexpected write_stdin error: %s", string(pollResult.Output))
	}
	if got := decodeStringToolOutput(t, pollResult); !strings.Contains(got, "kent") {
		t.Fatalf("expected AGENT=kent in background shell output, got %q", got)
	}
}

func TestExecCommandAppliesUserHookOutput(t *testing.T) {
	workspace := t.TempDir()
	hookPath := writeExecutableScript(t, "#!/bin/sh\nif [ \"$AGENT\" != kent ]; then printf '{\"processed\":true,\"replaced_output\":\"MISSING_AGENT\"}'; exit 0; fi\nprintf '{\"processed\":true,\"replaced_output\":\"HOOKED\"}\n'")
	manager, err := NewManager(
		WithMinimumExecToBgTime(250*time.Millisecond),
		WithCloseTimeouts(20*time.Millisecond, 200*time.Millisecond),
		WithPostprocessor(postprocess.NewRunner(postprocess.Settings{Mode: config.ShellPostprocessingModeUser, HookPath: hookPath})),
	)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	execTool := NewExecCommandTool(workspace, 16_000, manager, "")

	result := callExecCommand(t, execTool, "hooked", map[string]any{
		"cmd":           "printf raw",
		"shell":         "/bin/sh",
		"login":         false,
		"yield_time_ms": 5_000,
	})
	if result.IsError {
		t.Fatalf("unexpected exec_command error: %s", string(result.Output))
	}
	if got := decodeStringToolOutput(t, result); got != "HOOKED" {
		t.Fatalf("output = %q, want HOOKED", got)
	}
}

func TestExecCommandFileReadPostprocessorHandlesDirectCommandOnly(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "example.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	manager := newBackgroundTestManager(t)
	execTool := NewExecCommandTool(workspace, 16_000, manager, "")

	directResult := callExecCommand(t, execTool, "file-read-direct", map[string]any{
		"cmd":           "sed -n '1,1p' " + shellSingleQuote(path),
		"shell":         "/bin/sh",
		"login":         false,
		"yield_time_ms": 1_000,
	})
	if directResult.IsError {
		t.Fatalf("unexpected direct exec_command error: %s", string(directResult.Output))
	}
	if got := decodeStringToolOutput(t, directResult); got != "[Total line count: 2]\nalpha" {
		t.Fatalf("direct output = %q", got)
	}

	pipelineResult := callExecCommand(t, execTool, "file-read-pipeline", map[string]any{
		"cmd":           "nl -ba " + shellSingleQuote(path) + " | sed -n '1,1p'",
		"shell":         "/bin/sh",
		"login":         false,
		"yield_time_ms": 1_000,
	})
	if pipelineResult.IsError {
		t.Fatalf("unexpected pipeline exec_command error: %s", string(pipelineResult.Output))
	}
	pipelineOutput := decodeStringToolOutput(t, pipelineResult)
	if strings.Contains(pipelineOutput, "[Total line count:") {
		t.Fatalf("pipeline output should not include file-read context marker, got %q", pipelineOutput)
	}
	if !strings.Contains(pipelineOutput, "alpha") {
		t.Fatalf("pipeline output missing command output, got %q", pipelineOutput)
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
	exitCode := 7
	presentation := projectExecResult(ExecResult{ExitCode: &exitCode, Output: "bad"})
	if presentation.kind != execPresentationForegroundCompleted || presentation.exitCode != exitCode || !presentation.output.HasVisibleContent() {
		t.Fatalf("non-zero presentation = %+v", presentation)
	}
}

func TestWriteStdinWarnsAndRetriesWhenFullLogReadFails(t *testing.T) {
	workspace := t.TempDir()
	manager := newBackgroundTestManager(t)
	pollTool := NewWriteStdinTool(16_000, manager)

	result, err := manager.Start(context.Background(), ExecRequest{
		Command:        []string{"sh", "-c", "sleep 0.35; printf done"},
		DisplayCommand: "delayed-done",
		Workdir:        workspace,
		YieldTime:      250 * time.Millisecond,
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

	time.Sleep(500 * time.Millisecond)
	if err := os.Rename(logPath, backupPath); err != nil {
		t.Fatalf("rename log away: %v", err)
	}

	pollInput := map[string]any{
		"session_id":    sessionID,
		"yield_time_ms": 20,
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

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func TestExecCommandClampsShortYieldTimeSilently(t *testing.T) {
	const requestedYield = 20
	const commandDelay = 100 * time.Millisecond
	// Keep the clamped foreground window far above commandDelay. This test verifies
	// clamping behavior, not scheduler precision under full-suite/pre-push load.
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
		"yield_time_ms": requestedYield,
	})
	if result.IsError {
		t.Fatalf("unexpected exec_command error: %s", string(result.Output))
	}
	text := decodeStringToolOutput(t, result)
	if strings.Contains(text, "Warning: yield_time_ms below the minimum exec-to-background time") {
		t.Fatalf("did not expect clamp warning, got %q", text)
	}
	if !strings.Contains(text, "done") {
		t.Fatalf("expected command output, got %q", text)
	}
	if manager.Count() != 0 {
		t.Fatalf("manager count = %d, want 0", manager.Count())
	}
}

func TestNormalizeExecYieldTimeDoesNotCapConfiguredMinimum(t *testing.T) {
	manager, err := NewManager(WithMinimumExecToBgTime(45 * time.Second))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	yieldTime := manager.normalizeExecYieldTime(250 * time.Millisecond)
	if yieldTime != 45*time.Second {
		t.Fatalf("yield time = %s, want %s", yieldTime, 45*time.Second)
	}

	yieldTime = manager.normalizeExecYieldTime(50 * time.Second)
	if yieldTime != 50*time.Second {
		t.Fatalf("yield time = %s, want %s", yieldTime, 50*time.Second)
	}

	yieldTime = manager.normalizeExecYieldTime(0)
	if yieldTime != 45*time.Second {
		t.Fatalf("yield time = %s, want %s for zero input", yieldTime, 45*time.Second)
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
	manager := newBackgroundTestManager(t)
	execTool := NewExecCommandTool(workspace, 16_000, manager, "")
	pollTool := NewWriteStdinTool(16_000, manager)

	result := callExecCommand(t, execTool, "poll-duration-exec", map[string]any{
		"cmd":           "sleep 0.8",
		"shell":         "/bin/sh",
		"login":         false,
		"yield_time_ms": 250,
	})
	if result.IsError {
		t.Fatalf("unexpected exec_command error: %s", string(result.Output))
	}

	pollInput := map[string]any{
		"session_id":        1000,
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

func TestExecCommandForegroundTruncationUsesForegroundBanner(t *testing.T) {
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
	text := decodeStringToolOutput(t, result)
	if !strings.Contains(text, "Output is very large, omitted ") {
		t.Fatalf("expected foreground truncation banner, got %q", text)
	}
	if strings.Contains(text, "read log file for details") {
		t.Fatalf("did not expect background truncation guidance in foreground output, got %q", text)
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
	manager := newBackgroundTestManager(t)
	execTool := NewExecCommandTool(workspace, 16_000, manager, "")
	stdinTool := NewWriteStdinTool(16_000, manager)

	result := callExecCommand(t, execTool, "raw-tty-1", map[string]any{
		"cmd":           "read line; printf '\\033[31m%s\\033[0m' \"$line\"",
		"shell":         "/bin/sh",
		"login":         false,
		"raw":           true,
		"tty":           true,
		"yield_time_ms": 250,
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
	manager := newBackgroundTestManager(t)
	execTool := NewExecCommandTool(workspace, 16_000, manager, "")
	stdinTool := NewWriteStdinTool(16_000, manager)

	result := callExecCommand(t, execTool, "tty-1", map[string]any{
		"cmd":           "read line; echo $line",
		"shell":         "/bin/sh",
		"login":         false,
		"tty":           true,
		"yield_time_ms": 250,
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
		"yield_time_ms": 800,
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

func TestWriteStdinUsesBackgroundTruncationBannerOnCompletion(t *testing.T) {
	workspace := t.TempDir()
	manager := newBackgroundTestManager(t)
	execTool := NewExecCommandTool(workspace, 16_000, manager, "")
	stdinTool := NewWriteStdinTool(16_000, manager)

	result := callExecCommand(t, execTool, "tty-trunc-1", map[string]any{
		"cmd":           "read line; printf '%s' \"$line\"",
		"shell":         "/bin/sh",
		"login":         false,
		"tty":           true,
		"yield_time_ms": 250,
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
	stdinText := decodeStringToolOutput(t, stdinResult)
	if !strings.Contains(stdinText, "Omitted ") {
		t.Fatalf("expected background truncation banner, got %q", stdinText)
	}
	if !strings.Contains(stdinText, "read log file for details") {
		t.Fatalf("expected background truncation banner to reference the log file, got %q", stdinText)
	}
	if strings.Contains(stdinText, "Consider using more targeted commands") {
		t.Fatalf("did not expect foreground truncation guidance in background output, got %q", stdinText)
	}
	if stdinResult.PresentationDelta == nil || !stdinResult.PresentationDelta.OutputTruncated {
		t.Fatalf("expected write_stdin truncation presentation delta, got %+v", stdinResult.PresentationDelta)
	}
	waitForManagerCount(t, manager, 0, 3*time.Second)
}

func TestWriteStdinPreservesBackgroundSummaryTruncationMetadata(t *testing.T) {
	workspace := t.TempDir()
	manager := newBackgroundTestManager(t)
	execTool := NewExecCommandTool(workspace, 16_000, manager, "")
	stdinTool := NewWriteStdinTool(16_000, manager)

	result := callExecCommand(t, execTool, "tty-summary-trunc-1", map[string]any{
		"cmd":           "read line; head -c 2200000 /dev/zero | tr '\\0' x",
		"shell":         "/bin/sh",
		"login":         false,
		"tty":           true,
		"yield_time_ms": 250,
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
