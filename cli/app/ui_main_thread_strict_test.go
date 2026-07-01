package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"core/cli/app/internal/status"
	"core/shared/clientui"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

type strictBlockingProbeMsg struct{}

func (strictBlockingProbeMsg) probeUIModel(m *uiModel) {
	m.checkTUIBlockingOperation("test blocking read", "probe")
}

func TestTUIStrictIOPanicsInsideUpdate(t *testing.T) {
	m := newProjectedStaticUIModel()

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("expected strict-mode panic")
		}
		if !strings.Contains(recovered.(string), "TUI main-thread I/O violation during Update") {
			t.Fatalf("unexpected panic: %v", recovered)
		}
	}()

	_, _ = m.Update(strictBlockingProbeMsg{})
}

type countingProcessClient struct {
	listCalls int
}

func (c *countingProcessClient) ListProcesses(context.Context) ([]clientui.BackgroundProcess, error) {
	c.listCalls++
	return []clientui.BackgroundProcess{{ID: "proc-1", Running: true, State: "running"}}, nil
}

func (*countingProcessClient) KillProcess(context.Context, string) error {
	return nil
}

func (*countingProcessClient) InlineOutput(context.Context, string, int) (string, string, error) {
	return "", "", nil
}

func TestTUIStrictIOViewDoesNotFetchProcessesForStatusOrOverlay(t *testing.T) {
	processes := &countingProcessClient{}
	m := newProjectedStaticUIModel(
		WithUIProcessClient(processes),
		WithUIDebug(true),
	)
	m.termWidth = 100
	m.termHeight = 14
	m.windowSizeKnown = true
	m.openProcessList()
	m.activeSurface = uiSurfaceProcessList

	_ = m.View()

	if processes.listCalls != 0 {
		t.Fatalf("expected View not to list processes, got %d calls", processes.listCalls)
	}
}

func TestTUIStrictIOBusyEnterQueuesInjectedInputAsCommand(t *testing.T) {
	client := &runtimeControlFakeClient{queueUserMessageID: "server-queue-1"}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents(), WithUIDebug(true))
	m.startupCmds = nil
	m.setBusy(true)
	m.input = "queued steering"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected queue create command")
	}
	if client.queueUserMessageCalls != 0 {
		t.Fatalf("QueueUserMessage called during Update: %d", client.queueUserMessageCalls)
	}
	updated = applyFirstInjectedQueueCreateDoneForTest(t, updated, cmd)
	if client.queueUserMessageCalls != 1 {
		t.Fatalf("QueueUserMessage calls after command = %d, want 1", client.queueUserMessageCalls)
	}
	if len(updated.pendingInjected) != 1 || updated.pendingInjected[0].ID != "server-queue-1" {
		t.Fatalf("expected server queue item after command, got %+v", updated.pendingInjected)
	}
	if len(updated.promptHistory) != 1 || updated.promptHistory[0] != "queued steering" {
		t.Fatalf("prompt history = %+v, want accepted queued prompt", updated.promptHistory)
	}
}

func TestTUIStrictIOCompactDoneChecksQueuedRuntimeWorkAsCommand(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents(), WithUIDebug(true))
	m.startupCmds = nil
	m.setBusy(true)
	m.setCompacting(true)
	m.activity = uiActivityRunning

	next, cmd := m.Update(compactDoneMsg{})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected queued-work check command")
	}
	if client.hasQueuedUserWorkCalls != 0 {
		t.Fatalf("HasQueuedUserWork called during Update: %d", client.hasQueuedUserWorkCalls)
	}
	_, _ = applyQueuedRuntimeWorkCheckForTest(t, updated, cmd)
	if client.hasQueuedUserWorkCalls != 1 {
		t.Fatalf("HasQueuedUserWork calls after command = %d, want 1", client.hasQueuedUserWorkCalls)
	}
}

func TestTUIStrictIORuntimeControlSlashRunsAsCommand(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents(), WithUIDebug(true))
	m.startupCmds = nil

	handled, _, cmd := m.inputController().handleEnteredSlashCommandInput("/name New Name")
	if !handled {
		t.Fatal("expected /session slash command to be handled")
	}
	if cmd == nil {
		t.Fatal("expected runtime-control command")
	}
	if client.setSessionNameArg != "" {
		t.Fatalf("SetSessionName called during Update with %q", client.setSessionNameArg)
	}
	msgs := collectCmdMessages(t, cmd)
	if client.setSessionNameArg != "New Name" {
		t.Fatalf("SetSessionName after command = %q, want New Name; msgs=%+v", client.setSessionNameArg, msgs)
	}
}

type strictCountingStatusCollector struct {
	baseCalls int
}

func (c *strictCountingStatusCollector) Collect(context.Context, uiStatusRequest) (uiStatusSnapshot, error) {
	return uiStatusSnapshot{}, nil
}

func (c *strictCountingStatusCollector) CollectBase(req uiStatusRequest) uiStatusSnapshot {
	c.baseCalls++
	return status.Snapshot{CollectedAt: req.CurrentTime}
}

func (c *strictCountingStatusCollector) CollectAuth(context.Context, uiStatusRequest, uiStatusSnapshot) uiStatusAuthStageResult {
	return uiStatusAuthStageResult{}
}

func (c *strictCountingStatusCollector) CollectGit(context.Context, uiStatusRequest, uiStatusSnapshot) uiStatusGitStageResult {
	return uiStatusGitStageResult{}
}

func (c *strictCountingStatusCollector) CollectEnvironment(context.Context, uiStatusRequest, uiStatusSnapshot) uiStatusEnvironmentStageResult {
	return uiStatusEnvironmentStageResult{}
}

func TestTUIStrictIOStatusOpenDefersCollectorBaseToCommand(t *testing.T) {
	collector := &strictCountingStatusCollector{}
	repository := status.NewMemoryRepository()
	request := populateStatusRequestCacheKeys(uiStatusRequest{WorkspaceRoot: t.TempDir(), CurrentTime: time.Now()})
	repository.StoreGit(request.CacheKeys.Git, uiStatusGitStageResult{Git: uiStatusGitInfo{Visible: true, Branch: "cached"}}, time.Now())
	m := newProjectedStaticUIModel(
		WithUIDebug(true),
		WithUIStatusCollector(collector),
		WithUIStatusRepository(repository),
		WithUIStatusConfig(uiStatusConfig{WorkspaceRoot: request.WorkspaceRoot}),
	)

	cmd := m.inputController().startStatusFlowCmd()
	if cmd == nil {
		t.Fatal("expected status refresh command")
	}
	if collector.baseCalls != 0 {
		t.Fatalf("CollectBase called before command: %d", collector.baseCalls)
	}
	_ = collectCmdMessages(t, cmd)
	if collector.baseCalls == 0 {
		t.Fatal("expected CollectBase after executing returned command")
	}
}

func TestTUIStrictIOWorktreeSwitchRunsAsCommand(t *testing.T) {
	resp := testMainWorktreeListResponse()
	resp.Worktrees = append(resp.Worktrees, serverapi.WorktreeView{
		WorktreeID: "wt-feature", DisplayName: "feature", CanonicalRoot: "/repo-feature", BranchName: "feature",
	})
	client := &worktreeCommandTestClient{listResp: resp}
	m := newWorktreeTestModel(t, client, WithUIDebug(true))

	_, cmd := m.inputController().handleWorktreeCommand("switch feature")
	if cmd == nil {
		t.Fatal("expected worktree switch command")
	}
	if len(client.listRequests) != 0 || len(client.switchRequests) != 0 {
		t.Fatalf("worktree client called before command: list=%d switch=%d", len(client.listRequests), len(client.switchRequests))
	}
	_ = collectCmdMessages(t, cmd)
	if len(client.listRequests) == 0 || len(client.switchRequests) == 0 {
		t.Fatalf("expected worktree list/switch after command, list=%d switch=%d", len(client.listRequests), len(client.switchRequests))
	}
}
