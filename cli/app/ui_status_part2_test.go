package app

import (
	"context"
	"core/cli/app/internal/status"
	"core/server/auth"
	"core/server/sessionview"
	"core/shared/client"
	"core/shared/clientui"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestStatusLineGitStartupRefreshCachesBranch(t *testing.T) {
	repoRoot := initStatusLineGitRepo(t, "statusline-branch")
	search := newStubUIPathReferenceSearch()
	close(search.events)
	m := newProjectedTestUIModel(
		nil,
		closedProjectedRuntimeEvents(),
		closedAskEvents(),
		WithUIPathReferenceSearch(search),
		WithUIStatusConfig(uiStatusConfig{WorkspaceRoot: repoRoot}),
	)

	updated := drainStatusLineStartupCommands(t, m, m.Init())
	status := stripANSIAndTrimRight(updated.layout().renderStatusLine(120, uiThemeStyles("dark")))
	if !strings.Contains(status, "statusline-branch") {
		t.Fatalf("expected startup git branch in status line, got %q", status)
	}
}

func TestStatusLineGitStartupUsesRuntimeWorktreeRootBranch(t *testing.T) {
	processRoot := initStatusLineGitRepo(t, "main")
	workspaceRoot := initStatusLineGitRepo(t, "workspace-branch")
	worktreeRoot := initStatusLineGitRepo(t, "worktree-branch")
	t.Chdir(processRoot)

	runtimeClient := &runtimeControlFakeClient{sessionView: clientui.RuntimeSessionView{
		ExecutionTarget: clientui.SessionExecutionTarget{
			WorkspaceRoot:    workspaceRoot,
			WorktreeRoot:     worktreeRoot,
			EffectiveWorkdir: processRoot,
		},
	}}
	search := newStubUIPathReferenceSearch()
	close(search.events)
	m := newProjectedTestUIModel(
		runtimeClient,
		closedProjectedRuntimeEvents(),
		closedAskEvents(),
		WithUIPathReferenceSearch(search),
		WithUIStatusConfig(uiStatusConfig{WorkspaceRoot: workspaceRoot}),
	)

	updated := drainStatusLineStartupCommands(t, m, m.Init())
	status := stripANSIAndTrimRight(updated.layout().renderStatusLine(120, uiThemeStyles("dark")))
	if !strings.Contains(status, "worktree-branch") {
		t.Fatalf("expected worktree git branch in status line, got %q", status)
	}
	for _, unexpected := range []string{"main", "workspace-branch"} {
		if strings.Contains(status, unexpected) {
			t.Fatalf("did not expect %q branch in status line, got %q", unexpected, status)
		}
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

func drainStatusLineStartupCommands(t *testing.T, m *uiModel, cmd tea.Cmd) *uiModel {
	t.Helper()
	if cmd == nil {
		return m
	}
	msg := cmd()
	switch typed := msg.(type) {
	case nil:
		return m
	case tea.BatchMsg:
		for _, child := range typed {
			m = drainStatusLineStartupCommands(t, m, child)
		}
		return m
	default:
		next, nextCmd := m.Update(msg)
		updated, ok := next.(*uiModel)
		if !ok {
			t.Fatalf("unexpected model type %T", next)
		}
		return drainStatusLineStartupCommands(t, updated, nextCmd)
	}
}

func TestStatusParentSessionNameResolvesFromSessionViews(t *testing.T) {
	persistenceRoot := t.TempDir()
	parentStore := createAuthoritativeAppSession(t, persistenceRoot, "/tmp/work-a")
	if err := parentStore.SetName("incident-root"); err != nil {
		t.Fatalf("set parent name: %v", err)
	}
	sessionViews := client.NewLoopbackSessionViewClient(sessionview.NewService(sessionview.NewStaticSessionResolver(parentStore), nil, nil))
	got, warning := status.Collector{ParentSessionReadTimeout: uiRuntimeReadTimeout}.ParentSessionName(context.Background(), sessionViews, parentStore.Meta().SessionID)
	if warning != "" {
		t.Fatalf("unexpected warning: %q", warning)
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
	sessionViews := client.NewLoopbackSessionViewClient(sessionview.NewService(sessionview.NewStaticSessionResolver(parentStore), nil, nil))
	collector := &stubProgressiveStatusCollector{base: uiStatusSnapshot{ParentSessionID: parentStore.Meta().SessionID}}
	m := newProjectedStaticUIModel(
		WithUIStatusConfig(uiStatusConfig{SessionViews: sessionViews}),
		WithUIStatusCollector(collector),
	)
	cmd := m.statusRefreshCmd()
	if cmd == nil {
		t.Fatal("expected progressive status refresh to schedule base enrichment")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("message type = %T, want tea.BatchMsg", cmd())
	}
	if len(batch) == 0 {
		t.Fatal("expected at least one batched status command")
	}
	baseMsg, ok := batch[0]().(statusBaseRefreshDoneMsg)
	if !ok {
		t.Fatalf("batched message type = %T, want statusBaseRefreshDoneMsg", batch[0]())
	}
	if baseMsg.snapshot.ParentSessionName != "incident-root" {
		t.Fatalf("parent session name = %q, want incident-root", baseMsg.snapshot.ParentSessionName)
	}
}

func TestStatusRefreshDefersRuntimeAndAuthReadsToCommands(t *testing.T) {
	store := &countingAuthStore{}
	manager := auth.NewManager(store, nil, nil)
	runtimeClient := &statusRefreshRuntimeClient{}
	m := newProjectedStaticUIModel(WithUIStatusConfig(uiStatusConfig{AuthManager: manager}))
	m.engine = runtimeClient

	cmd := m.statusRefreshCmd()
	if cmd == nil {
		t.Fatal("expected status refresh command")
	}
	if runtimeClient.statusCalls != 0 {
		t.Fatalf("expected no runtime status read before command executes, got %d", runtimeClient.statusCalls)
	}
	if store.loads != 0 {
		t.Fatalf("expected no auth load before command executes, got %d", store.loads)
	}

	_ = collectCmdMessages(t, cmd)
	if runtimeClient.statusCalls == 0 {
		t.Fatal("expected runtime status read after command executes")
	}
	if store.loads == 0 {
		t.Fatal("expected auth load after command executes")
	}
}

type statusRefreshRuntimeClient struct {
	runtimeControlFakeClient
	statusCalls int
}

func (c *statusRefreshRuntimeClient) Status() clientui.RuntimeStatus {
	c.statusCalls++
	return clientui.RuntimeStatus{ParentSessionID: "parent-session"}
}

func TestStatusVisibleAuthSummarySuppressesGenericSubscriptionWhenPlanPresent(t *testing.T) {
	if got := statusVisibleAuthSummary(uiStatusAuthInfo{Summary: "Subscription", Visible: true}, uiStatusSubscriptionInfo{Summary: "Pro subscription"}); got != "" {
		t.Fatalf("visible auth summary = %q", got)
	}
	if got := statusVisibleAuthSummary(uiStatusAuthInfo{Summary: "user@example.com", Visible: true}, uiStatusSubscriptionInfo{Summary: "Pro subscription"}); got != "user@example.com" {
		t.Fatalf("visible auth summary = %q", got)
	}
	if got := statusVisibleAuthSummary(uiStatusAuthInfo{Summary: "No Auth", Visible: true}, uiStatusSubscriptionInfo{Summary: "Pro subscription"}); got != "No Auth" {
		t.Fatalf("visible auth summary = %q", got)
	}
}

func TestStatusSubscriptionResetMetaIncludesRelativeDuration(t *testing.T) {
	now := time.Date(2026, time.March, 24, 20, 0, 0, 0, time.UTC)
	resetAt := now.Add(49*time.Hour + 3*time.Minute)
	got := statusSubscriptionResetMeta(resetAt, now)
	if !strings.Contains(got, "in 2d1h3m") {
		t.Fatalf("reset meta = %q", got)
	}
	if !strings.Contains(got, "at ") {
		t.Fatalf("expected local timestamp in reset meta, got %q", got)
	}
}

func TestStatusCollectorPrefersWorkspaceRootForWorkdir(t *testing.T) {
	workspaceRoot := t.TempDir()
	collector := defaultUIStatusCollector{}
	snapshot, err := collector.Collect(context.Background(), newStatusRequestForTest(withStatusWorkspaceRoot(workspaceRoot)))
	if err != nil {
		t.Fatalf("collect status: %v", err)
	}
	if snapshot.Workdir != workspaceRoot {
		t.Fatalf("workdir = %q, want %q", snapshot.Workdir, workspaceRoot)
	}
	if snapshot.Git.Visible {
		t.Fatal("expected non-git temp directory to hide git section")
	}
}
