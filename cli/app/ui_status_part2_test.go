package app

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"core/cli/app/internal/status"
	"core/server/auth"
	"core/server/sessionview"
	"core/shared/clientui"
	"core/shared/runtimeids"

	tea "github.com/charmbracelet/bubbletea"
)

func TestStatusLineGitStartupUsesRuntimeWorktreeRootBranch(t *testing.T) {
	processRoot := initStatusLineGitRepo(t, "main")
	workspaceRoot := initStatusLineGitRepo(t, "workspace-branch")
	worktreeRoot := initStatusLineGitRepo(t, "worktree-branch")
	t.Chdir(processRoot)

	runtimeClient := &runtimeControlFakeClient{sessionView: clientui.RuntimeSessionView{
		ExecutionTarget: clientui.SessionExecutionTarget{
			WorkspaceRoot:    workspaceRoot,
			Worktree:         &clientui.SessionExecutionWorktreeTarget{ID: "worktree-1", Root: worktreeRoot},
			EffectiveWorkdir: processRoot,
		},
	}}
	search := newStubUIPathReferenceSearch()
	close(search.events)
	model := newProjectedTestUIModel(
		runtimeClient,
		WithUIPathReferenceSearch(search),
		WithUIStatusConfig(uiStatusConfig{WorkspaceRoot: workspaceRoot}),
	)

	updated := drainStatusLineStartupCommands(t, model, model.Init())
	rendered := stripANSIAndTrimRight(updated.layout().renderStatusLine(120, uiThemeStyles("dark")))
	if !strings.Contains(rendered, "worktree-branch") {
		t.Fatalf("status line did not use runtime worktree branch: %q", rendered)
	}
	for _, unexpected := range []string{"main", "workspace-branch"} {
		if strings.Contains(rendered, unexpected) {
			t.Fatalf("status line used non-authoritative branch %q: %q", unexpected, rendered)
		}
	}
}

func TestTranscriptSessionIdentityUpdatesStatusExecutionTarget(t *testing.T) {
	initial := clientui.SessionExecutionTarget{
		WorkspaceID:           "workspace-1",
		WorkspaceRoot:         "/repo",
		WorkspaceAvailability: clientui.ProjectAvailabilityAvailable,
		CwdRelpath:            ".",
		EffectiveWorkdir:      "/repo",
	}
	entered := clientui.SessionExecutionTarget{
		WorkspaceID:           "workspace-1",
		WorkspaceRoot:         "/repo",
		WorkspaceAvailability: clientui.ProjectAvailabilityAvailable,
		Worktree:              &clientui.SessionExecutionWorktreeTarget{ID: "worktree-1", Root: "/repo/feature"},
		CwdRelpath:            ".",
		EffectiveWorkdir:      "/repo/feature",
	}
	runtimeClient := &sessionRuntimeClient{
		sessionID:   "session-1",
		mainView:    clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{SessionID: "session-1"}},
		hasMainView: true,
	}
	model := newProjectedTestUIModel(runtimeClient, WithUIStatusConfig(uiStatusConfig{
		WorkspaceRoot:   initial.EffectiveWorkdir,
		ExecutionTarget: initial,
	}))
	sessionID, err := runtimeids.ParseSessionID("session-1")
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	if _, err := runtimeClient.admitTranscriptMessageState(clientui.TranscriptMessage{
		Kind: clientui.TranscriptMessageSessionIdentity,
		Payload: clientui.TranscriptPayload{SessionIdentity: &clientui.TranscriptSessionIdentity{
			SessionID:             sessionID,
			ConversationFreshness: clientui.ConversationFreshnessEstablished,
			ExecutionTarget:       &entered,
		}},
	}); err != nil {
		t.Fatalf("admit transcript session identity: %v", err)
	}

	request := model.newStatusRequest(time.Now())
	if request.ExecutionTarget.EffectiveWorkdir != entered.EffectiveWorkdir {
		t.Fatalf("status execution target = %+v, want %q", request.ExecutionTarget, entered.EffectiveWorkdir)
	}
	if request.WorkspaceRoot != entered.EffectiveWorkdir {
		t.Fatalf("status workspace root = %q, want %q", request.WorkspaceRoot, entered.EffectiveWorkdir)
	}
}

func TestStatusSessionNameResolvesFromSessionViews(t *testing.T) {
	persistenceRoot := t.TempDir()
	parentStore := createAuthoritativeAppSession(t, persistenceRoot, "/tmp/work-a")
	if err := parentStore.SetName("incident-root"); err != nil {
		t.Fatalf("set parent name: %v", err)
	}
	sessionViews := sessionview.NewService(sessionview.NewStaticSessionResolver(parentStore), nil, nil)
	got, err := status.Collector{SessionNameReadTimeout: uiRuntimeReadTimeout}.ResolveSessionName(context.Background(), sessionViews, parentStore.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionName: %v", err)
	}
	if got != "incident-root" {
		t.Fatalf("parent session name = %q", got)
	}
}

func TestStatusRefreshCmdSchedulesBaseEnrichmentForProgressiveCollector(t *testing.T) {
	persistenceRoot := t.TempDir()
	parentStore := createAuthoritativeAppSession(t, persistenceRoot, "/tmp/work-a")
	if err := parentStore.SetName("incident-root"); err != nil {
		t.Fatalf("set parent name: %v", err)
	}
	sessionViews := sessionview.NewService(sessionview.NewStaticSessionResolver(parentStore), nil, nil)
	previousSessionID, err := runtimeids.ParseSessionID(parentStore.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse previous session id: %v", err)
	}
	collector := &stubProgressiveStatusCollector{base: uiStatusSnapshot{PreviousSessionID: &previousSessionID}}
	model := newProjectedStaticUIModel(
		WithUIStatusConfig(uiStatusConfig{SessionViews: sessionViews}),
		WithUIStatusCollector(collector),
	)
	cmd := model.statusRefreshCmd()
	if cmd == nil {
		t.Fatal("expected progressive status refresh to schedule base enrichment")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) == 0 {
		t.Fatalf("status refresh message = %T, want non-empty tea.BatchMsg", cmd())
	}
	baseMsg, ok := batch[0]().(statusBaseRefreshDoneMsg)
	if !ok {
		t.Fatalf("batched message type = %T, want statusBaseRefreshDoneMsg", batch[0]())
	}
	if baseMsg.snapshot.PreviousSessionName != "incident-root" {
		t.Fatalf("previous session name = %q", baseMsg.snapshot.PreviousSessionName)
	}
}

func TestStatusRefreshDefersRuntimeAndAuthReadsToCommands(t *testing.T) {
	store := &countingAuthStore{}
	manager := auth.NewManager(store, nil, nil)
	runtimeClient := &statusRefreshRuntimeClient{}
	model := newProjectedStaticUIModel(WithUIStatusConfig(uiStatusConfig{AuthManager: manager}))
	model.engine = runtimeClient

	cmd := model.statusRefreshCmd()
	if cmd == nil {
		t.Fatal("expected status refresh command")
	}
	if runtimeClient.statusCalls != 0 || store.loads != 0 {
		t.Fatalf("status refresh performed eager reads: runtime=%d auth=%d", runtimeClient.statusCalls, store.loads)
	}
	_ = collectCmdMessages(t, cmd)
	if runtimeClient.statusCalls == 0 || store.loads == 0 {
		t.Fatalf("status refresh command reads: runtime=%d auth=%d", runtimeClient.statusCalls, store.loads)
	}
}

func TestStatusCollectorPrefersWorkspaceRootForWorkdir(t *testing.T) {
	workspaceRoot := t.TempDir()
	snapshot, err := (defaultUIStatusCollector{}).Collect(context.Background(), newStatusRequestForTest(withStatusWorkspaceRoot(workspaceRoot)))
	if err != nil {
		t.Fatalf("collect status: %v", err)
	}
	if snapshot.Workdir != workspaceRoot || snapshot.Git.Visible {
		t.Fatalf("snapshot = workdir %q, git visible %t", snapshot.Workdir, snapshot.Git.Visible)
	}
}

type statusRefreshRuntimeClient struct {
	runtimeControlFakeClient
	statusCalls int
}

func (c *statusRefreshRuntimeClient) Status() clientui.RuntimeStatus {
	c.statusCalls++
	parentSessionID, err := runtimeids.ParseSessionID("parent-session")
	if err != nil {
		panic(err)
	}
	return clientui.RuntimeStatus{
		PreviousSessionID:         &parentSessionID,
		NavigationTargetSessionID: &parentSessionID,
	}
}

func initStatusLineGitRepo(t *testing.T, branch string) string {
	t.Helper()
	repoRoot := t.TempDir()
	cmd := exec.Command("git", "-C", repoRoot, "init", "-b", branch)
	cmd.Env = sanitizedGitEnv(os.Environ())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init -b %s: %v (%s)", branch, err, out)
	}
	return repoRoot
}

func drainStatusLineStartupCommands(t *testing.T, model *uiModel, cmd tea.Cmd) *uiModel {
	t.Helper()
	if cmd == nil {
		return model
	}
	msg := cmd()
	switch typed := msg.(type) {
	case nil:
		return model
	case tea.BatchMsg:
		for _, child := range typed {
			model = drainStatusLineStartupCommands(t, model, child)
		}
		return model
	default:
		next, nextCmd := model.Update(msg)
		updated, ok := next.(*uiModel)
		if !ok {
			t.Fatalf("unexpected model type %T", next)
		}
		return drainStatusLineStartupCommands(t, updated, nextCmd)
	}
}
