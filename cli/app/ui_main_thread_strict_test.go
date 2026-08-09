package app

import (
	"context"
	"testing"
	"time"

	"core/cli/app/internal/status"
	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

type strictBlockingProbeMsg struct{}

func (strictBlockingProbeMsg) probeUIModel(m *uiModel) {
	m.checkTUIBlockingOperation("test blocking read", "probe")
}

func TestTUIStrictIOPanicsInsideUpdate(t *testing.T) {
	m := newStrictUIModel(nil)

	defer func() {
		if recover() == nil {
			t.Fatal("expected strict-mode panic")
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
	m := newStrictUIModel(nil, WithUIProcessClient(processes))
	m.terminalGeometry = terminalGeometryKnown(100, 14)
	m.openProcessList()
	m.activeSurface = uiSurfaceProcessList

	_ = m.View()

	if processes.listCalls != 0 {
		t.Fatalf("expected View not to list processes, got %d calls", processes.listCalls)
	}
}

type strictRuntimeClient struct {
	clientui.RuntimeClient

	submitQueuedID string
	submitCalls    int
}

func (*strictRuntimeClient) MainView() clientui.RuntimeMainView {
	return clientui.RuntimeMainView{}
}

func (c *strictRuntimeClient) SubmitRuntimeInput(_ context.Context, request clientui.RuntimeSubmitRequest) (clientui.UserTurnSubmission, error) {
	c.submitCalls++
	return clientui.UserTurnSubmission{
		Queued: clientui.QueuedUserMessage{
			ID:   c.submitQueuedID,
			Text: runtimeSubmitInputText(request),
		},
	}, nil
}

func TestTUIStrictIOBusyEnterQueuesInjectedInputAsCommand(t *testing.T) {
	client := &strictRuntimeClient{submitQueuedID: "server-queue-1"}
	m := newStrictUIModel(client)
	m.startupCmds = nil
	setStrictTestRuntimeBusy(t, m)
	m.mainEditor.Replace("queued steering")
	m.mainEditor.SetCursor(len("queued steering"))

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected queue create command")
	}
	if client.submitCalls != 0 {
		t.Fatalf("SubmitRuntimeInput called during Update: %d", client.submitCalls)
	}
	runDraftRecoveryCommands(t, updated, cmd)
	if client.submitCalls != 1 {
		t.Fatalf("SubmitRuntimeInput calls after command = %d, want 1", client.submitCalls)
	}
	if len(updated.pendingInjected) != 1 || updated.pendingInjected[0].ID != "server-queue-1" {
		t.Fatalf("expected server queue item after command, got %+v", updated.pendingInjected)
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
	m := newStrictUIModel(nil,
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
	_ = strictCmdMessages(cmd)
	if collector.baseCalls == 0 {
		t.Fatal("expected CollectBase after executing returned command")
	}
}

type strictWorktreeClient struct {
	apicontract.WorktreeService

	enterCalls int
}

func (c *strictWorktreeClient) EnterWorktree(_ context.Context, request serverapi.WorktreeEnterRequest) (serverapi.WorktreeScheduledAcknowledgement, error) {
	c.enterCalls++
	return serverapi.WorktreeScheduledAcknowledgement{OperationID: request.OperationID}, nil
}

func TestTUIStrictIOWorktreeSwitchRunsAsCommand(t *testing.T) {
	client := &strictWorktreeClient{}
	m := newStrictUIModel(nil, WithUIWorktreeClient(client), WithUISessionID("session-1"))

	_, cmd := m.inputController().handleWorktreeCommand("switch feature")
	if cmd == nil {
		t.Fatal("expected worktree switch command")
	}
	if client.enterCalls != 0 {
		t.Fatalf("worktree client called during Update: enter=%d", client.enterCalls)
	}
	_ = strictCmdMessages(cmd)
	if client.enterCalls != 1 {
		t.Fatalf("expected worktree enter after command, enter=%d", client.enterCalls)
	}
}

func newStrictUIModel(client clientui.RuntimeClient, options ...UIOption) *uiModel {
	defaults := []UIOption{
		WithUISessionID("11111111-1111-4111-8111-111111111111"),
		WithUISessionDraftPersistence(testSessionDraftPersistence{}),
	}
	return NewProjectedUIModel(client, append(defaults, options...)...).(*uiModel)
}

func setStrictTestRuntimeBusy(t *testing.T, m *uiModel) {
	t.Helper()
	runID, err := runtimeids.ParseRunID("00000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatalf("parse run id: %v", err)
	}
	stepID, err := runtimeids.ParseStepID("00000000-0000-4000-8000-000000000002")
	if err != nil {
		t.Fatalf("parse step id: %v", err)
	}
	if err := m.applyRuntimeActivityProjection(clientui.RuntimeActivity{
		State: clientui.RuntimeActivityRunning,
		ActiveStep: &clientui.RuntimeActiveStep{
			RunID: runID, StepID: stepID, ActiveKind: clientui.RuntimeActivityActiveKindUserTurn,
		},
	}); err != nil {
		t.Fatalf("set busy runtime activity: %v", err)
	}
}

func strictCmdMessages(cmd tea.Cmd) []tea.Msg {
	messages := make([]tea.Msg, 0)
	var collectMessage func(tea.Msg)
	var collectCommand func(tea.Cmd)
	collectCommand = func(command tea.Cmd) {
		if command != nil {
			collectMessage(command())
		}
	}
	collectMessage = func(message tea.Msg) {
		if message == nil {
			return
		}
		messages = append(messages, message)
		if batch, ok := message.(tea.BatchMsg); ok {
			for _, nested := range batch {
				collectCommand(nested)
			}
		}
	}
	collectCommand(cmd)
	return messages
}
