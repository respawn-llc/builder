package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"core/server/processview"
	shelltool "core/server/tools/shell"
	"core/shared/clientui"
	"core/shared/serverapi"
)

type fixedUIProcessClient struct {
	entries []clientui.BackgroundProcess
}

func withUIBackgroundManagerForTest(manager *shelltool.Manager) UIOption {
	return func(m *uiModel) {
		if manager == nil || m.processClientExplicit {
			return
		}
		processes := processview.NewProcessViewService(manager)
		m.processClient = newUIProcessClientWithReads(
			processes,
			processes,
		)
	}
}

type stubProcessViewService struct {
	listResp serverapi.ProcessListResponse
	err      error
}

type stubProcessControlService struct {
	inlineResp serverapi.ProcessInlineOutputResponse
	err        error
	killedReq  serverapi.ProcessKillRequest
	killed     []string
}

func (s *stubProcessViewService) ListProcesses(context.Context, serverapi.ProcessListRequest) (serverapi.ProcessListResponse, error) {
	if s.err != nil {
		return serverapi.ProcessListResponse{}, s.err
	}
	return s.listResp, nil
}

func (s *stubProcessViewService) GetProcess(context.Context, serverapi.ProcessGetRequest) (serverapi.ProcessGetResponse, error) {
	if s.err != nil {
		return serverapi.ProcessGetResponse{}, s.err
	}
	return serverapi.ProcessGetResponse{}, nil
}

func (s *stubProcessControlService) KillProcess(_ context.Context, req serverapi.ProcessKillRequest) (serverapi.ProcessKillResponse, error) {
	if s.err != nil {
		return serverapi.ProcessKillResponse{}, s.err
	}
	s.killedReq = req
	s.killed = append(s.killed, req.ProcessID)
	return serverapi.ProcessKillResponse{}, nil
}

func (s *stubProcessControlService) GetInlineOutput(context.Context, serverapi.ProcessInlineOutputRequest) (serverapi.ProcessInlineOutputResponse, error) {
	if s.err != nil {
		return serverapi.ProcessInlineOutputResponse{}, s.err
	}
	return s.inlineResp, nil
}

func (c fixedUIProcessClient) ListProcesses(context.Context) ([]clientui.BackgroundProcess, error) {
	out := make([]clientui.BackgroundProcess, len(c.entries))
	copy(out, c.entries)
	return out, nil
}

func (fixedUIProcessClient) KillProcess(context.Context, string) error { return nil }

func (fixedUIProcessClient) InlineOutput(context.Context, string, int) (string, string, error) {
	return "", "", nil
}

func TestUIProcessClientProjectsManagerSnapshots(t *testing.T) {
	manager := newFastBackgroundTestManager(t)

	workdir := t.TempDir()
	res, err := manager.Start(context.Background(), shelltool.ExecRequest{
		Command:        []string{"sh", "-c", "printf 'done\n'; sleep 0.05; exit 7"},
		DisplayCommand: "project-test",
		Workdir:        workdir,
		YieldTime:      fastBackgroundTestYield,
	})
	if err != nil {
		t.Fatalf("start background process: %v", err)
	}
	if !res.Backgrounded {
		t.Fatal("expected background process")
	}

	processes := processview.NewProcessViewService(manager)
	client := newUIProcessClientWithReads(
		processes,
		processes,
	)
	waitForTestCondition(t, 2*time.Second, "background process to finish", func() bool {
		entries, err := client.ListProcesses(context.Background())
		if err != nil {
			return false
		}
		for _, entry := range entries {
			if entry.ID == res.SessionID {
				return !entry.Running && entry.ExitCode != nil
			}
		}
		return false
	})

	var projectedExitCode *int
	found := false
	entries, err := client.ListProcesses(context.Background())
	if err != nil {
		t.Fatalf("ListProcesses: %v", err)
	}
	for _, entry := range entries {
		if entry.ID != res.SessionID {
			continue
		}
		found = true
		if entry.Command != "project-test" {
			t.Fatalf("command = %q, want project-test", entry.Command)
		}
		if entry.Workdir != workdir {
			t.Fatalf("workdir = %q, want %q", entry.Workdir, workdir)
		}
		if entry.LogPath == "" {
			t.Fatal("expected projected log path")
		}
		if entry.ExitCode == nil || *entry.ExitCode != 7 {
			t.Fatalf("exit code = %+v, want 7", entry.ExitCode)
		}
		projectedExitCode = entry.ExitCode
		break
	}
	if !found {
		t.Fatalf("expected projected process entry for %s", res.SessionID)
	}

	*projectedExitCode = 0
	entries, err = client.ListProcesses(context.Background())
	if err != nil {
		t.Fatalf("ListProcesses second: %v", err)
	}
	for _, entry := range entries {
		if entry.ID == res.SessionID {
			if entry.ExitCode == nil || *entry.ExitCode != 7 {
				t.Fatalf("expected projected exit code clone to remain 7, got %+v", entry.ExitCode)
			}
			return
		}
	}
	t.Fatalf("expected projected process entry for %s on second read", res.SessionID)
}

func TestExplicitUIProcessClientWinsOverBackgroundManagerOptionOrder(t *testing.T) {
	manager := newFastBackgroundTestManager(t)

	explicit := fixedUIProcessClient{entries: []clientui.BackgroundProcess{{ID: "explicit-process"}}}

	first := newProjectedStaticUIModel(
		withUIBackgroundManagerForTest(manager),
		WithUIProcessClient(explicit),
	)
	if got := first.listProcesses(); len(got) != 1 || got[0].ID != "explicit-process" {
		t.Fatalf("expected explicit process client to win when applied last, got %+v", got)
	}

	second := newProjectedStaticUIModel(
		WithUIProcessClient(explicit),
		withUIBackgroundManagerForTest(manager),
	)
	if got := second.listProcesses(); len(got) != 1 || got[0].ID != "explicit-process" {
		t.Fatalf("expected explicit process client to win when applied first, got %+v", got)
	}
}

func TestUIProcessClientUsesSharedReadsWhenAvailable(t *testing.T) {
	reads := &stubProcessViewService{
		listResp: serverapi.ProcessListResponse{Processes: []clientui.BackgroundProcess{{ID: "proc-1", OwnerRunID: "run-1", OwnerStepID: "step-1"}}},
	}
	processClient := newUIProcessClientWithReads(reads, nil)
	got, err := processClient.ListProcesses(context.Background())
	if err != nil {
		t.Fatalf("ListProcesses: %v", err)
	}
	if len(got) != 1 || got[0].ID != "proc-1" || got[0].OwnerRunID != "run-1" || got[0].OwnerStepID != "step-1" {
		t.Fatalf("unexpected loopback process payload: %+v", got)
	}
}

func TestUIProcessClientDoesNotBypassSharedReadBoundaryOnError(t *testing.T) {
	manager := newFastBackgroundTestManager(t)

	workdir := t.TempDir()
	res, err := manager.Start(context.Background(), shelltool.ExecRequest{
		Command:        []string{"sh", "-c", "printf 'done\n'; sleep 0.05; exit 0"},
		DisplayCommand: "fallback-process",
		OwnerSessionID: "session-1",
		OwnerRunID:     "run-1",
		OwnerStepID:    "step-1",
		Workdir:        workdir,
		YieldTime:      fastBackgroundTestYield,
	})
	if err != nil {
		t.Fatalf("start background process: %v", err)
	}
	if !res.Backgrounded {
		t.Fatal("expected background process")
	}

	processClient := newUIProcessClientWithReads(&stubProcessViewService{err: errors.New("boom")}, nil)
	if got, err := processClient.ListProcesses(context.Background()); err == nil || got != nil {
		t.Fatalf("expected shared-read failure to fail closed, got entries=%+v err=%v", got, err)
	}
}

func TestUIProcessClientUsesSharedControlWhenAvailable(t *testing.T) {
	controls := &stubProcessControlService{inlineResp: serverapi.ProcessInlineOutputResponse{Output: "hello", LogPath: "/tmp/proc.log"}}
	processClient := newUIProcessClientWithReads(nil, controls)

	preview, logPath, err := processClient.InlineOutput(context.Background(), "proc-1", 123)
	if err != nil {
		t.Fatalf("InlineOutput: %v", err)
	}
	if preview != "hello" || logPath != "/tmp/proc.log" {
		t.Fatalf("unexpected inline output payload preview=%q logPath=%q", preview, logPath)
	}
	if err := processClient.KillProcess(context.Background(), "proc-1"); err != nil {
		t.Fatalf("KillProcess: %v", err)
	}
	if len(controls.killed) != 1 || controls.killed[0] != "proc-1" {
		t.Fatalf("unexpected killed requests: %+v", controls.killed)
	}
	if strings.TrimSpace(controls.killedReq.ClientRequestID) == "" {
		t.Fatal("expected KillProcess to generate client_request_id")
	}
}

func TestUIProcessClientDoesNotBypassSharedControlBoundaryOnError(t *testing.T) {
	manager := newFastBackgroundTestManager(t)

	workdir := t.TempDir()
	res, err := manager.Start(context.Background(), shelltool.ExecRequest{
		Command:        []string{"sh", "-c", "printf 'fallback-control\n'; sleep 1"},
		DisplayCommand: "fallback-control",
		Workdir:        workdir,
		YieldTime:      fastBackgroundTestYield,
	})
	if err != nil {
		t.Fatalf("start background process: %v", err)
	}
	if !res.Backgrounded {
		t.Fatal("expected background process")
	}

	controlErr := errors.New("shared control boundary failure")
	processClient := newUIProcessClientWithReads(nil, &stubProcessControlService{err: controlErr})
	if _, _, err := processClient.InlineOutput(context.Background(), res.SessionID, 12_000); !errors.Is(err, controlErr) {
		t.Fatalf("expected shared control error from InlineOutput, got %v", err)
	}
	if err := processClient.KillProcess(context.Background(), res.SessionID); !errors.Is(err, controlErr) {
		t.Fatalf("expected shared control error from KillProcess, got %v", err)
	}
	for _, entry := range manager.List() {
		if entry.ID == res.SessionID && entry.KillRequested {
			t.Fatalf("expected manager fallback to stay unused, got %+v", entry)
		}
	}
}
