package app

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"

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
	if _, err := runtimeClient.admitTranscriptMessageState(clientui.NewTranscriptMessage(0, clientui.NewTranscriptEvent(clientui.TranscriptSessionIdentity{
		SessionID:             sessionID,
		ConversationFreshness: clientui.ConversationFreshnessEstablished,
		ExecutionTarget:       &entered,
	})),
	); err != nil {
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

func TestTranscriptSessionIdentityReplacesConflictingConversationFreshnessCaches(t *testing.T) {
	sessionID, err := runtimeids.ParseSessionID("session-1")
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	runtimeClient := &sessionRuntimeClient{
		sessionID: "session-1",
		mainView: clientui.RuntimeMainView{
			Session: clientui.RuntimeSessionView{
				SessionID:             "session-1",
				ConversationFreshness: clientui.ConversationFreshnessEstablished,
			},
			Status: clientui.RuntimeStatus{
				ConversationFreshness: clientui.ConversationFreshnessEstablished,
			},
		},
		hasMainView: true,
	}
	_, err = runtimeClient.admitTranscriptMessageState(clientui.NewTranscriptMessage(0, clientui.NewTranscriptEvent(
		clientui.TranscriptSessionIdentity{
			SessionID:             sessionID,
			ConversationFreshness: clientui.ConversationFreshnessFresh,
		},
	)))
	if err != nil {
		t.Fatalf("admit transcript session identity: %v", err)
	}
	if runtimeClient.mainView.Session.ConversationFreshness != clientui.ConversationFreshnessFresh {
		t.Fatalf("session-view conversation freshness = %q, want fresh", runtimeClient.mainView.Session.ConversationFreshness)
	}
	if runtimeClient.mainView.Status.ConversationFreshness != clientui.ConversationFreshnessFresh {
		t.Fatalf("status conversation freshness = %q, want fresh", runtimeClient.mainView.Status.ConversationFreshness)
	}
	if got := runtimeClient.Status().ConversationFreshness; got != clientui.ConversationFreshnessFresh {
		t.Fatalf("cached conversation freshness = %q, want fresh", got)
	}
}

func TestStatusRequestCarriesCachedRuntimeAgentRole(t *testing.T) {
	role := "worker"
	runtimeClient := &runtimeControlFakeClient{
		cachedMainView: clientui.RuntimeMainView{
			Session: clientui.RuntimeSessionView{AgentRole: &role},
		},
		hasCachedMainView: true,
	}
	request := newProjectedTestUIModel(runtimeClient).newStatusRequest(time.Now())
	if request.AgentRole == nil || *request.AgentRole != role {
		t.Fatalf("status request agent role = %v, want %q", request.AgentRole, role)
	}
}

func TestStatusRefreshUsesCurrentSessionAgentRoleWhenRuntimeCacheIsCold(t *testing.T) {
	role := "qa_tester"
	sessionViews := stubSessionViewClient{
		getSessionMainView: func(_ context.Context, request serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error) {
			if request.SessionID != "session-1" {
				t.Fatalf("current session view request = %+v, want session-1", request)
			}
			return serverapi.SessionMainViewResponse{
				MainView: clientui.RuntimeMainView{
					Session: clientui.RuntimeSessionView{SessionID: "session-1", AgentRole: &role},
				},
			}, nil
		},
	}
	runtimeClient := &sessionRuntimeClient{
		sessionID: "session-1",
		mainView:  clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{SessionID: "session-1"}},
	}
	model := newProjectedTestUIModel(
		runtimeClient,
		WithUISessionID("session-1"),
		WithUIStatusConfig(uiStatusConfig{WorkspaceRoot: t.TempDir(), SessionViews: sessionViews}),
	)

	messages := collectCmdMessages(t, model.statusRefreshCmd())
	for _, message := range messages {
		base, ok := message.(statusBaseRefreshDoneMsg)
		if !ok {
			continue
		}
		if base.snapshot.AgentRole == nil || *base.snapshot.AgentRole != role {
			t.Fatalf("status agent role = %v, want %q", base.snapshot.AgentRole, role)
		}
		return
	}
	t.Fatal("status refresh did not emit a base snapshot")
}

func TestStatusRefreshCmdSchedulesBaseEnrichmentForProgressiveCollector(t *testing.T) {
	previousSessionID := runtimeids.NewSessionID()
	sessionViews := stubSessionViewClient{
		getSessionMainView: func(_ context.Context, request serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error) {
			if request.SessionID != previousSessionID.String() {
				t.Fatalf("previous session view request = %+v, want %s", request, previousSessionID)
			}
			return serverapi.SessionMainViewResponse{
				MainView: clientui.RuntimeMainView{
					Session: clientui.RuntimeSessionView{
						SessionID:   previousSessionID.String(),
						SessionName: "incident-root",
					},
				},
			}, nil
		},
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
	authStatus := &staticAuthStatusClient{response: authStatusResponse(serverapi.AuthStatusMethodNone)}
	runtimeClient := &statusRefreshRuntimeClient{}
	model := newProjectedStaticUIModel(WithUIStatusConfig(uiStatusConfig{AuthStatus: authStatus}))
	model.engine = runtimeClient

	cmd := model.statusRefreshCmd()
	if cmd == nil {
		t.Fatal("expected status refresh command")
	}
	if runtimeClient.statusCalls != 0 || authStatus.calls != 0 {
		t.Fatalf("status refresh performed eager reads: runtime=%d auth=%d", runtimeClient.statusCalls, authStatus.calls)
	}
	_ = collectCmdMessages(t, cmd)
	if runtimeClient.statusCalls == 0 || authStatus.calls == 0 {
		t.Fatalf("status refresh command reads: runtime=%d auth=%d", runtimeClient.statusCalls, authStatus.calls)
	}
}

type statusRefreshRuntimeClient struct {
	runtimeControlFakeClient
	statusCalls int
}

func (c *statusRefreshRuntimeClient) Status() clientui.RuntimeStatus {
	c.statusCalls++
	return clientui.RuntimeStatus{}
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
