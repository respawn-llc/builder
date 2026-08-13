package processview

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/requestmemo"
	"core/server/tools"
	shelltool "core/server/tools/shell"
	"core/shared/serverapi"
	"core/shared/toolspec"
)

const processViewTestWaitTimeout = 10 * time.Second

func TestServiceListProcessesIncludesRunOwnership(t *testing.T) {
	fixture := newProcessViewFixture(t)
	result := fixture.startCommand(t, "call-1", "printf 'working\n'; sleep 30", "run-1", "step-1")
	if result.IsError {
		t.Fatalf("expected successful tool result, got %+v", result)
	}

	waitForProcessSnapshot(t, processViewTestWaitTimeout, func() (shelltool.Snapshot, bool) {
		entries := fixture.manager.List()
		if len(entries) != 1 {
			return shelltool.Snapshot{}, false
		}
		process := entries[0]
		if !process.OutputAvailable || process.OutputRetainedFromBytes != 0 || process.OutputRetainedToBytes <= 0 {
			return shelltool.Snapshot{}, false
		}
		return process, true
	})
	resp, err := fixture.service.ListProcesses(context.Background(), serverapi.ProcessListRequest{OwnerSessionID: "session-1", OwnerRunID: "run-1"})
	if err != nil {
		t.Fatalf("ListProcesses: %v", err)
	}
	if len(resp.Processes) != 1 {
		t.Fatalf("expected one process, got %+v", resp.Processes)
	}
	process := resp.Processes[0]
	if process.OwnerSessionID != "session-1" || process.OwnerRunID != "run-1" || process.OwnerStepID != "step-1" {
		t.Fatalf("unexpected ownership: %+v", process)
	}
	if !process.Backgrounded || !process.Running {
		t.Fatalf("expected backgrounded running process, got %+v", process)
	}
	if !process.OutputAvailable || process.OutputRetainedFromBytes != 0 || process.OutputRetainedToBytes <= 0 {
		t.Fatalf("expected retained output metadata, got %+v", process)
	}

	got, err := fixture.service.GetProcess(context.Background(), serverapi.ProcessGetRequest{ProcessID: process.ID})
	if err != nil {
		t.Fatalf("GetProcess: %v", err)
	}
	if got.Process == nil || got.Process.OwnerRunID != "run-1" || got.Process.OwnerStepID != "step-1" {
		t.Fatalf("unexpected process payload: %+v", got.Process)
	}
	if !got.Process.OutputAvailable || got.Process.OutputRetainedFromBytes != 0 || got.Process.OutputRetainedToBytes < process.OutputRetainedToBytes {
		t.Fatalf("expected retained output metadata from get, got %+v", got.Process)
	}
}

func TestResolveProcessAuthorizationSnapshotsAndProjectsOnce(t *testing.T) {
	source := &countingProcessSource{snapshot: shelltool.Snapshot{
		ID:             "proc-1",
		OwnerSessionID: "session-1",
		OwnerRunID:     "run-1",
		Command:        "sleep 1",
	}}
	service := NewProcessViewService(source)

	candidate, err := service.ResolveProcessAuthorization(t.Context(), "proc-1")
	if err != nil {
		t.Fatalf("ResolveProcessAuthorization: %v", err)
	}
	if source.snapshotCalls != 1 {
		t.Fatalf("Snapshot calls = %d, want 1", source.snapshotCalls)
	}
	if candidate.ProcessID != "proc-1" || candidate.OwnerSessionID != "session-1" {
		t.Fatalf("candidate identity = %+v", candidate)
	}
	if candidate.Process.ID != "proc-1" || candidate.Process.OwnerRunID != "run-1" || candidate.Process.Command != "sleep 1" {
		t.Fatalf("candidate projection = %+v", candidate.Process)
	}
}

type countingProcessSource struct {
	snapshot      shelltool.Snapshot
	snapshotCalls int
}

func (s *countingProcessSource) List() []shelltool.Snapshot { return nil }
func (s *countingProcessSource) Snapshot(id string) (shelltool.Snapshot, error) {
	s.snapshotCalls++
	if id != s.snapshot.ID {
		return shelltool.Snapshot{}, errors.New("unexpected Process ID")
	}
	return s.snapshot, nil
}
func (s *countingProcessSource) Kill(string) error { return nil }
func (s *countingProcessSource) InlineOutput(string, int) (string, string, error) {
	return "", "", nil
}

type processViewFixture struct {
	manager *shelltool.Manager
	tool    tools.Handler
	service *ProcessViewService
}

func newProcessViewFixture(t *testing.T) processViewFixture {
	t.Helper()
	manager, err := shelltool.NewManager(shelltool.WithMinimumExecToBgTime(250 * time.Millisecond))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	workspace := t.TempDir()
	tool := shelltool.NewExecCommandTool(workspace, 16_000, manager, "session-1")
	return processViewFixture{manager: manager, tool: tool, service: NewProcessViewService(manager)}
}

func (f processViewFixture) startCommand(t *testing.T, id string, command string, runID string, stepID string) tools.Result {
	t.Helper()
	input, err := json.Marshal(map[string]any{
		"cmd":           command,
		"shell":         "/bin/sh",
		"login":         false,
		"yield_time_ms": 250,
	})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	result, err := f.tool.Call(context.Background(), tools.Call{
		ID:     id,
		Name:   toolspec.ToolExecCommand,
		Input:  input,
		RunID:  runID,
		StepID: stepID,
	})
	if err != nil {
		t.Fatalf("tool call: %v", err)
	}
	return result
}

func TestServiceListProcessesFiltersByOwnerRunID(t *testing.T) {
	fixture := newProcessViewFixture(t)
	for _, runID := range []string{"run-a", "run-b"} {
		fixture.startCommand(t, runID, "sleep 1", runID, runID+"-step")
	}

	waitForProcessCount(t, fixture.manager, 2)

	resp, err := fixture.service.ListProcesses(context.Background(), serverapi.ProcessListRequest{OwnerRunID: "run-b"})
	if err != nil {
		t.Fatalf("ListProcesses: %v", err)
	}
	if len(resp.Processes) != 1 || resp.Processes[0].OwnerRunID != "run-b" {
		t.Fatalf("unexpected filtered processes: %+v", resp.Processes)
	}
}

func TestServiceGetInlineOutputReturnsManagerPreview(t *testing.T) {
	fixture := newProcessViewFixture(t)
	result := fixture.startCommand(t, "call-inline", "printf 'inline-preview\n'; sleep 1", "run-1", "step-1")
	if result.IsError {
		t.Fatalf("expected successful tool result, got %+v", result)
	}

	waitForInlineOutput(t, processViewTestWaitTimeout, func() (serverapi.ProcessInlineOutputResponse, error) {
		return fixture.service.GetInlineOutput(context.Background(), serverapi.ProcessInlineOutputRequest{ProcessID: "1000", MaxChars: 12_000})
	}, func(resp serverapi.ProcessInlineOutputResponse) bool {
		return resp.LogPath != "" && strings.Contains(resp.Output, "inline-preview")
	})
	resp, err := fixture.service.GetInlineOutput(context.Background(), serverapi.ProcessInlineOutputRequest{ProcessID: "1000", MaxChars: 12_000})
	if err != nil {
		t.Fatalf("GetInlineOutput: %v", err)
	}
	if resp.LogPath == "" || !strings.Contains(resp.Output, "inline-preview") {
		t.Fatalf("unexpected inline output response: %+v", resp)
	}
}

func TestServiceKillProcessSignalsManagerEntry(t *testing.T) {
	fixture := newProcessViewFixture(t)
	result := fixture.startCommand(t, "call-kill", "sleep 30", "run-1", "step-1")
	if result.IsError {
		t.Fatalf("expected successful tool result, got %+v", result)
	}

	if _, err := fixture.service.KillProcess(context.Background(), serverapi.ProcessKillRequest{ClientRequestID: "req-kill-1", ProcessID: "1000"}); err != nil {
		t.Fatalf("KillProcess: %v", err)
	}
	waitForProcessKilled(t, fixture.manager, "1000")
}

func TestServiceKillProcessRequiresClientRequestID(t *testing.T) {
	fixture := newProcessViewFixture(t)
	if _, err := fixture.service.KillProcess(context.Background(), serverapi.ProcessKillRequest{ProcessID: "1000"}); err == nil {
		t.Fatal("expected KillProcess to require client_request_id")
	}
}

func TestServiceKillProcessHonorsCanceledContext(t *testing.T) {
	source := &stubKillProcessSource{}
	svc := NewProcessViewService(source)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.KillProcess(ctx, serverapi.ProcessKillRequest{ClientRequestID: "req-kill-1", ProcessID: "1000"}); err != context.Canceled {
		t.Fatalf("KillProcess error = %v, want context canceled", err)
	}
	if source.killCalls != 0 {
		t.Fatalf("kill call count = %d, want 0", source.killCalls)
	}
}

func TestServiceKillProcessDedupesSuccessfulRetry(t *testing.T) {
	source := &stubKillProcessSource{}
	svc := NewProcessViewService(source)
	req := serverapi.ProcessKillRequest{ClientRequestID: "req-kill-1", ProcessID: "1000"}

	if _, err := svc.KillProcess(context.Background(), req); err != nil {
		t.Fatalf("KillProcess first: %v", err)
	}
	source.killErr = context.DeadlineExceeded
	if _, err := svc.KillProcess(context.Background(), req); err != nil {
		t.Fatalf("KillProcess replay: %v", err)
	}
	if source.killCalls != 1 {
		t.Fatalf("kill call count = %d, want 1", source.killCalls)
	}
}

func TestServiceKillProcessRejectsRequestIDPayloadMismatch(t *testing.T) {
	source := &stubKillProcessSource{}
	svc := NewProcessViewService(source)

	if _, err := svc.KillProcess(context.Background(), serverapi.ProcessKillRequest{ClientRequestID: "req-kill-1", ProcessID: "1000"}); err != nil {
		t.Fatalf("KillProcess first: %v", err)
	}
	if _, err := svc.KillProcess(context.Background(), serverapi.ProcessKillRequest{ClientRequestID: "req-kill-1", ProcessID: "2000"}); !errors.Is(err, requestmemo.ErrClientRequestIDReused) {
		t.Fatalf("KillProcess mismatch error = %v, want reused with different parameters", err)
	}
	if source.killCalls != 1 {
		t.Fatalf("kill call count = %d, want 1", source.killCalls)
	}
}

type stubKillProcessSource struct {
	killCalls int
	killErr   error
}

func (s *stubKillProcessSource) List() []shelltool.Snapshot { return nil }

func (s *stubKillProcessSource) Snapshot(string) (shelltool.Snapshot, error) {
	return shelltool.Snapshot{}, nil
}

func (s *stubKillProcessSource) Kill(string) error {
	s.killCalls++
	return s.killErr
}

func (s *stubKillProcessSource) InlineOutput(string, int) (string, string, error) {
	return "", "", nil
}

func waitForProcessCount(t *testing.T, manager *shelltool.Manager, count int) {
	t.Helper()
	testsetup.RequireUntil(t, time.Now().Add(processViewTestWaitTimeout), 10*time.Millisecond, func() bool {
		return len(manager.List()) >= count
	}, "timed out waiting for %d processes", count)
}

func waitForProcessKilled(t *testing.T, manager *shelltool.Manager, id string) {
	t.Helper()
	testsetup.RequireUntil(t, time.Now().Add(processViewTestWaitTimeout), 10*time.Millisecond, func() bool {
		for _, entry := range manager.List() {
			if entry.ID == id && (entry.KillRequested || !entry.Running) {
				return true
			}
		}
		return false
	}, "timed out waiting for process %s to be kill-requested", id)
}

func waitForProcessSnapshot(t *testing.T, timeout time.Duration, check func() (shelltool.Snapshot, bool)) shelltool.Snapshot {
	t.Helper()
	var snapshot shelltool.Snapshot
	testsetup.RequireUntil(t, time.Now().Add(timeout), 10*time.Millisecond, func() bool {
		var ok bool
		snapshot, ok = check()
		return ok
	}, "timed out waiting for process snapshot condition")
	return snapshot
}

func waitForInlineOutput(t *testing.T, timeout time.Duration, call func() (serverapi.ProcessInlineOutputResponse, error), match func(serverapi.ProcessInlineOutputResponse) bool) serverapi.ProcessInlineOutputResponse {
	t.Helper()
	var resp serverapi.ProcessInlineOutputResponse
	testsetup.RequireUntil(t, time.Now().Add(timeout), 10*time.Millisecond, func() bool {
		var err error
		resp, err = call()
		return err == nil && match(resp)
	}, "timed out waiting for inline output")
	return resp
}
