package shell

import (
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"core/server/tools/shell/postprocess"
	"core/shared/config"
)

func TestExecCommandUsesRuntimeBoundPolicyForForegroundCompletion(t *testing.T) {
	workspace := t.TempDir()
	manager := newManagerWithPostprocessor(t, replacementRunner(t, "BOOTSTRAP"))
	noneRunner := mustPostprocessRunner(t, postprocess.Settings{Mode: config.ShellPostprocessingModeNone})
	runtimeRunner := replacementRunner(t, "RUNTIME")

	noneTool := NewExecCommandToolWithPostprocessor(workspace, 16_000, manager, "none-owner", noneRunner)
	noneResult := callExecCommand(t, noneTool, "none-foreground", map[string]any{
		"cmd":           "printf foreground",
		"shell":         "/bin/sh",
		"login":         false,
		"yield_time_ms": 1_000,
	})
	if noneResult.IsError {
		t.Fatalf("none-mode exec_command error: %s", string(noneResult.Output))
	}
	if got := decodeStringToolOutput(t, noneResult); got != "foreground" {
		t.Fatalf("none-mode foreground output = %q, want foreground", got)
	}

	runtimeTool := NewExecCommandToolWithPostprocessor(workspace, 16_000, manager, "runtime-owner", runtimeRunner)
	runtimeResult := callExecCommand(t, runtimeTool, "hook-foreground", map[string]any{
		"cmd":           "printf foreground",
		"shell":         "/bin/sh",
		"login":         false,
		"yield_time_ms": 1_000,
	})
	if runtimeResult.IsError {
		t.Fatalf("runtime-hook exec_command error: %s", string(runtimeResult.Output))
	}
	if got := decodeStringToolOutput(t, runtimeResult); got != "RUNTIME" {
		t.Fatalf("runtime-hook foreground output = %q, want RUNTIME", got)
	}
}

func TestBackgroundProcessKeepsCapturedHookAcrossLaterStartsPollingAndCompletion(t *testing.T) {
	workspace := t.TempDir()
	manager := newManagerWithPostprocessor(t, replacementRunner(t, "BOOTSTRAP"))
	runnerA := replacementRunner(t, "RUNTIME_A")
	runnerB := replacementRunner(t, "RUNTIME_B")
	toolA := NewExecCommandToolWithPostprocessor(workspace, 16_000, manager, "owner-a", runnerA)
	toolB := NewExecCommandToolWithPostprocessor(workspace, 16_000, manager, "owner-b", runnerB)
	pollTool := NewWriteStdinTool(16_000, manager)

	startA := callExecCommand(t, toolA, "a-background", map[string]any{
		"cmd":           "printf early; sleep 0.45; printf late",
		"shell":         "/bin/sh",
		"login":         false,
		"yield_time_ms": 250,
	})
	if startA.IsError {
		t.Fatalf("runtime A background start error: %s", string(startA.Output))
	}
	if got := decodeStringToolOutput(t, startA); !strings.Contains(got, "RUNTIME_A") {
		t.Fatalf("runtime A transition output = %q, want captured hook output", got)
	}
	snapshots := manager.List()
	if len(snapshots) != 1 {
		t.Fatalf("background process count = %d, want 1", len(snapshots))
	}
	processA, err := strconv.Atoi(snapshots[0].ID)
	if err != nil {
		t.Fatalf("runtime A process ID: %v", err)
	}

	foregroundB := callExecCommand(t, toolB, "b-foreground", map[string]any{
		"cmd":           "printf foreground-b",
		"shell":         "/bin/sh",
		"login":         false,
		"yield_time_ms": 1_000,
	})
	if foregroundB.IsError {
		t.Fatalf("runtime B foreground error: %s", string(foregroundB.Output))
	}
	if got := decodeStringToolOutput(t, foregroundB); got != "RUNTIME_B" {
		t.Fatalf("runtime B foreground output = %q, want RUNTIME_B", got)
	}

	pollA := callWriteStdin(t, pollTool, "a-poll", map[string]any{
		"session_id":    processA,
		"yield_time_ms": 1_000,
	})
	if pollA.IsError {
		t.Fatalf("runtime A polling error: %s", string(pollA.Output))
	}
	if got := decodeWriteStdinToolOutput(t, pollA).Output; !strings.Contains(got, "RUNTIME_A") {
		t.Fatalf("runtime A polling output = %q, want captured hook output", got)
	}
	waitForManagerCount(t, manager, 0, time.Second)

	events := make(chan Event, 1)
	manager.SetEventHandler(func(event Event) {
		if event.Type != EventCompleted && event.Type != EventKilled {
			return
		}
		select {
		case events <- event:
		default:
		}
	})
	autoA := callExecCommand(t, toolA, "a-auto", map[string]any{
		"cmd":           "sleep 0.35; printf automatic",
		"shell":         "/bin/sh",
		"login":         false,
		"yield_time_ms": 250,
	})
	if autoA.IsError {
		t.Fatalf("runtime A automatic completion start error: %s", string(autoA.Output))
	}

	select {
	case event := <-events:
		if event.completion == nil {
			t.Fatalf("automatic completion event has no finalized output: %+v", event)
		}
		if got := event.completion.output.Content(); got != "RUNTIME_A" {
			t.Fatalf("automatic completion output = %q, want RUNTIME_A", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for runtime A completion event")
	}
	waitForManagerCount(t, manager, 0, time.Second)
}

func TestRawBypassesCapturedPolicyInForegroundBackgroundAndPolling(t *testing.T) {
	workspace := t.TempDir()
	manager := newManagerWithPostprocessor(t, replacementRunner(t, "BOOTSTRAP"))
	tool := NewExecCommandToolWithPostprocessor(workspace, 16_000, manager, "raw-owner", replacementRunner(t, "RUNTIME"))
	pollTool := NewWriteStdinTool(16_000, manager)

	foreground := callExecCommand(t, tool, "raw-foreground", map[string]any{
		"cmd":           "printf '\\033[31mforeground\\033[0m'",
		"shell":         "/bin/sh",
		"login":         false,
		"raw":           true,
		"yield_time_ms": 1_000,
	})
	if foreground.IsError {
		t.Fatalf("raw foreground error: %s", string(foreground.Output))
	}
	if got := decodeStringToolOutput(t, foreground); got != "\x1b[31mforeground\x1b[0m" {
		t.Fatalf("raw foreground output = %q, want original ANSI", got)
	}

	background := callExecCommand(t, tool, "raw-background", map[string]any{
		"cmd":           "printf '\\033[31mearly\\033[0m'; sleep 0.4; printf '\\033[32mlate\\033[0m'",
		"shell":         "/bin/sh",
		"login":         false,
		"raw":           true,
		"yield_time_ms": 250,
	})
	if background.IsError {
		t.Fatalf("raw background error: %s", string(background.Output))
	}
	if got := decodeStringToolOutput(t, background); !strings.Contains(got, "\x1b[31mearly\x1b[0m") {
		t.Fatalf("raw background transition output = %q, want original ANSI", got)
	}
	snapshots := manager.List()
	if len(snapshots) != 1 {
		t.Fatalf("raw background process count = %d, want 1", len(snapshots))
	}
	processID, err := strconv.Atoi(snapshots[0].ID)
	if err != nil {
		t.Fatalf("raw background process ID: %v", err)
	}
	poll := callWriteStdin(t, pollTool, "raw-poll", map[string]any{
		"session_id":    processID,
		"yield_time_ms": 1_000,
	})
	if poll.IsError {
		t.Fatalf("raw polling error: %s", string(poll.Output))
	}
	if got := decodeWriteStdinToolOutput(t, poll).Output; !strings.Contains(got, "\x1b[32mlate\x1b[0m") {
		t.Fatalf("raw polling output = %q, want original ANSI", got)
	}
	waitForManagerCount(t, manager, 0, time.Second)
}

func TestSharedManagerKeepsGlobalLifecycleAcrossCapturedPolicies(t *testing.T) {
	workspace := t.TempDir()
	manager := newManagerWithPostprocessor(t, replacementRunner(t, "BOOTSTRAP"))
	toolA := NewExecCommandToolWithPostprocessor(workspace, 16_000, manager, "owner-a", replacementRunner(t, "RUNTIME_A"))
	toolB := NewExecCommandToolWithPostprocessor(workspace, 16_000, manager, "owner-b", replacementRunner(t, "RUNTIME_B"))
	pollTool := NewWriteStdinTool(16_000, manager)
	events := make(chan Event, 4)
	manager.SetEventHandler(func(event Event) {
		if event.Type != EventCompleted && event.Type != EventKilled {
			return
		}
		events <- event
	})

	for name, tool := range map[string]*ExecCommandTool{"RUNTIME_A": toolA, "RUNTIME_B": toolB} {
		result := callExecCommand(t, tool, "start-"+name, map[string]any{
			"cmd":           "printf started; sleep 30",
			"shell":         "/bin/sh",
			"login":         false,
			"yield_time_ms": 250,
		})
		if result.IsError {
			t.Fatalf("%s background start error: %s", name, string(result.Output))
		}
		if got := decodeStringToolOutput(t, result); !strings.Contains(got, name) {
			t.Errorf("%s transition output = %q, want captured policy", name, got)
		}
	}

	snapshots := manager.List()
	if len(snapshots) != 2 {
		t.Fatalf("global process list count = %d, want 2", len(snapshots))
	}
	ids := make([]int, 0, len(snapshots))
	idByOwner := make(map[string]int, len(snapshots))
	for _, snapshot := range snapshots {
		id, err := strconv.Atoi(snapshot.ID)
		if err != nil {
			t.Fatalf("global process ID %q: %v", snapshot.ID, err)
		}
		ids = append(ids, id)
		idByOwner[snapshot.OwnerSessionID] = id
	}
	sort.Ints(ids)
	if ids[1] != ids[0]+1 {
		t.Fatalf("global process IDs = %v, want one shared sequence", ids)
	}

	pollA := callWriteStdin(t, pollTool, "cross-runtime-poll", map[string]any{
		"session_id":    idByOwner["owner-a"],
		"yield_time_ms": 250,
	})
	if pollA.IsError {
		t.Fatalf("cross-runtime polling error: %s", string(pollA.Output))
	}
	if got := decodeWriteStdinToolOutput(t, pollA).Output; !strings.Contains(got, "RUNTIME_A") {
		t.Errorf("cross-runtime polling output = %q, want process A captured policy", got)
	}

	if err := manager.Close(); err != nil {
		t.Fatalf("close shared manager: %v", err)
	}
	seenOwners := map[string]bool{}
	for range snapshots {
		select {
		case event := <-events:
			seenOwners[event.Snapshot.OwnerSessionID] = true
			if event.completion == nil {
				t.Errorf("owner %q completion event has no output", event.Snapshot.OwnerSessionID)
				continue
			}
			want := "RUNTIME_A"
			if event.Snapshot.OwnerSessionID == "owner-b" {
				want = "RUNTIME_B"
			}
			if got := event.completion.output.Content(); got != want {
				t.Errorf("owner %q completion output = %q, want %q", event.Snapshot.OwnerSessionID, got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for globally routed completion events")
		}
	}
	if !seenOwners["owner-a"] || !seenOwners["owner-b"] {
		t.Fatalf("completion event owners = %#v, want both process owners", seenOwners)
	}
	if manager.Count() != 0 {
		t.Fatalf("manager count after shutdown = %d, want 0", manager.Count())
	}
}

func replacementRunner(t *testing.T, replacement string) *postprocess.Runner {
	t.Helper()
	hookPath := writeExecutableScript(t, "#!/bin/sh\nprintf '{\"processed\":true,\"replaced_output\":\""+replacement+"\"}'\n")
	return mustPostprocessRunner(t, postprocess.Settings{
		Mode:     config.ShellPostprocessingModeUser,
		HookPath: &hookPath,
	})
}

func newManagerWithPostprocessor(t *testing.T, runner *postprocess.Runner) *Manager {
	t.Helper()
	manager, err := NewManager(
		WithMinimumExecToBgTime(250*time.Millisecond),
		WithCloseTimeouts(20*time.Millisecond, 200*time.Millisecond),
		WithPostprocessor(runner),
	)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}
