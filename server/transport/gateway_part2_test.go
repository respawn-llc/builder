package transport

import (
	"context"
	serverbootstrap "core/server/bootstrap"
	"core/server/core"
	"core/server/metadata"
	"core/server/session"
	shelltool "core/server/tools/shell"
	remoteclient "core/shared/client"
	"core/shared/config"
	connectionpb "core/shared/protoapi/gen/kent/api/connection"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newGatewayTestServerForConfig(t *testing.T, cfg config.App) (*core.Core, *httptest.Server) {
	t.Helper()
	authSupport := newGatewayTestAuthSupport(t, true)
	runtimeSupport, err := serverbootstrap.BuildRuntimeSupport(cfg)
	if err != nil {
		t.Fatalf("BuildRuntimeSupport: %v", err)
	}
	t.Cleanup(func() { _ = runtimeSupport.Background.Close() })
	appCore, err := core.New(cfg, authSupport, runtimeSupport)
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	t.Cleanup(func() { _ = appCore.Close() })
	gateway, err := NewGateway(appCore, gatewayTestIdentity())
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	server := httptest.NewServer(gateway.Handler())
	t.Cleanup(server.Close)
	return appCore, server
}

func resolveGatewayTestConfig(t *testing.T, workspace string) serverbootstrap.ConfigPlan {
	t.Helper()
	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	return resolved
}

func registerGatewayTestBinding(t *testing.T, cfg config.App) metadata.Binding {
	t.Helper()
	binding, err := metadata.RegisterBinding(context.Background(), cfg.PersistenceRoot, cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	return binding
}

func TestGatewayRequiresExplicitWorkspaceSelectionForMultiWorkspaceProject(t *testing.T) {
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	t.Setenv("HOME", home)
	configureGatewayTestServerPort(t)

	resolvedA := resolveGatewayTestConfig(t, workspaceA)
	bindingA := registerGatewayTestBinding(t, resolvedA.Config)
	metadataStore, err := metadata.Open(resolvedA.Config.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	defer func() { _ = metadataStore.Close() }()
	bindingB, err := metadataStore.AttachWorkspaceToProject(context.Background(), bindingA.ProjectID, workspaceB)
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject B: %v", err)
	}

	_, server := newGatewayTestServerForConfig(t, resolvedA.Config)

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	attachResult := attachGatewayProject(t, conn, "attach-project", &connectionpb.AttachProjectRequest{ProjectId: bindingA.ProjectID})
	if attachResult.GetError() == nil {
		t.Fatalf("expected explicit workspace selection error, got %+v", attachResult)
	}

	attachResult = attachGatewayProject(t, conn, "attach-project-explicit", &connectionpb.AttachProjectRequest{
		ProjectId: bindingA.ProjectID,
		Workspace: &connectionpb.AttachProjectRequest_WorkspaceId{WorkspaceId: bindingB.WorkspaceID},
	})
	if attachResult.GetSuccess() == nil {
		t.Fatalf("explicit Project attachment failed: %+v", attachResult.GetError())
	}
	planResp := callGatewaySessionPlan(t, conn, "session-plan", serverapi.SessionPlanRequest{
		Mode:   serverapi.SessionLaunchModeInteractive,
		Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
	})
	target := gatewaySessionExecutionTarget(t, conn, "main-view-after-explicit-workspace", planResp.Plan.SessionID)
	if got, want := target.EffectiveWorkdir, bindingB.CanonicalRoot; got != want {
		t.Fatalf("planned execution workdir = %q, want %q", got, want)
	}
}

func TestGatewayAttachSessionClearsWorkspaceOverrideForLaterPlans(t *testing.T) {
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	t.Setenv("HOME", home)
	configureGatewayTestServerPort(t)

	resolvedB := resolveGatewayTestConfig(t, workspaceB)
	bindingB := registerGatewayTestBinding(t, resolvedB.Config)
	resolvedA := resolveGatewayTestConfig(t, workspaceA)
	metadataStore, err := metadata.Open(resolvedA.Config.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	defer func() { _ = metadataStore.Close() }()
	if _, err := metadataStore.AttachWorkspaceToProject(context.Background(), bindingB.ProjectID, resolvedA.Config.WorkspaceRoot); err != nil {
		t.Fatalf("AttachWorkspaceToProject: %v", err)
	}

	_, server := newGatewayTestServerForConfig(t, resolvedA.Config)

	storeB, err := session.Create(
		filepath.Join(filepath.Join(resolvedA.Config.PersistenceRoot, "projects"), bindingB.ProjectID, "sessions"),
		"workspace-b",
		resolvedB.Config.WorkspaceRoot, sessioncontract.SessionCategoryMain, metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create workspace B: %v", err)
	}
	if err := storeB.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable workspace B: %v", err)
	}

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	attachResult := attachGatewayProject(t, conn, "attach-project", &connectionpb.AttachProjectRequest{
		ProjectId: bindingB.ProjectID,
		Workspace: &connectionpb.AttachProjectRequest_WorkspaceRoot{WorkspaceRoot: resolvedA.Config.WorkspaceRoot},
	})
	if attachResult.GetSuccess() == nil {
		t.Fatalf("Project attachment failed: %+v", attachResult.GetError())
	}
	if result := attachGatewaySession(t, conn, "attach-session", storeB.Meta().SessionID); result.GetSuccess() == nil {
		t.Fatalf("Session attachment failed: %+v", result.GetError())
	}

	planResp := callGatewaySessionPlan(t, conn, "session-plan", serverapi.SessionPlanRequest{
		Mode:   serverapi.SessionLaunchModeInteractive,
		Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
	})
	wantWorkspaceRoot, err := config.CanonicalWorkspaceRoot(resolvedB.Config.WorkspaceRoot)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot B: %v", err)
	}
	target := gatewaySessionExecutionTarget(t, conn, "main-view-after-attach-session", planResp.Plan.SessionID)
	if got, want := target.EffectiveWorkdir, wantWorkspaceRoot; got != want {
		t.Fatalf("planned execution workdir = %q, want %q", got, want)
	}
}

func TestGatewayScopesProcessAPIsToAttachedProject(t *testing.T) {
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	t.Setenv("HOME", home)
	configureGatewayTestServerPort(t)

	resolvedA := resolveGatewayTestConfig(t, workspaceA)
	bindingA := registerGatewayTestBinding(t, resolvedA.Config)
	resolvedB := resolveGatewayTestConfig(t, workspaceB)
	bindingB := registerGatewayTestBinding(t, resolvedB.Config)
	metadataStore, err := metadata.Open(resolvedA.Config.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	defer func() { _ = metadataStore.Close() }()

	appCore, server := newGatewayTestServerForConfig(t, resolvedA.Config)
	appCore.Background().SetMinimumExecToBgTime(time.Millisecond)

	storeA := createGatewayAuthoritativeSession(t, appCore)
	storeB, err := session.Create(
		filepath.Join(filepath.Join(resolvedB.Config.PersistenceRoot, "projects"), bindingB.ProjectID, "sessions"),
		"workspace-b",
		resolvedB.Config.WorkspaceRoot, sessioncontract.SessionCategoryMain, metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create foreign: %v", err)
	}
	if err := storeB.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable foreign: %v", err)
	}

	ownResult, err := appCore.Background().Start(context.Background(), shelltool.ExecRequest{
		Command:        []string{"/bin/sh", "-lc", "printf own\\n; sleep 1"},
		DisplayCommand: "printf own; sleep 1",
		OwnerSessionID: storeA.Meta().SessionID,
		Workdir:        appCore.Config().WorkspaceRoot,
		YieldTime:      time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start own process: %v", err)
	}
	foreignResult, err := appCore.Background().Start(context.Background(), shelltool.ExecRequest{
		Command:        []string{"/bin/sh", "-lc", "printf foreign\\n; sleep 1"},
		DisplayCommand: "printf foreign; sleep 1",
		OwnerSessionID: storeB.Meta().SessionID,
		Workdir:        resolvedB.Config.WorkspaceRoot,
		YieldTime:      time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start foreign process: %v", err)
	}

	remote, err := remoteclient.DialRemoteURLForProject(context.Background(), "ws"+server.URL[len("http"):], bindingA.ProjectID)
	if err != nil {
		t.Fatalf("DialRemoteURLForProject: %v", err)
	}
	defer func() { _ = remote.Close() }()

	listed, err := remote.ListProcesses(context.Background(), serverapi.ProcessListRequest{})
	if err != nil {
		t.Fatalf("ListProcesses: %v", err)
	}
	if len(listed.Processes) != 1 || listed.Processes[0].ID != ownResult.SessionID {
		t.Fatalf("expected only own project process, got %+v", listed.Processes)
	}
	if _, err := remote.GetProcess(context.Background(), serverapi.ProcessGetRequest{ProcessID: foreignResult.SessionID}); err == nil {
		t.Fatal("expected foreign process get to be rejected")
	}
	if _, err := remote.GetInlineOutput(context.Background(), serverapi.ProcessInlineOutputRequest{ProcessID: foreignResult.SessionID, MaxChars: 128}); err == nil {
		t.Fatal("expected foreign process inline output to be rejected")
	}
	if _, err := remote.KillProcess(context.Background(), serverapi.ProcessKillRequest{ClientRequestID: "kill-foreign", ProcessID: foreignResult.SessionID}); err == nil {
		t.Fatal("expected foreign process kill to be rejected")
	}
	if _, err := remote.GetProcess(context.Background(), serverapi.ProcessGetRequest{ProcessID: ownResult.SessionID}); err != nil {
		t.Fatalf("expected own process get to succeed, got %v", err)
	}
	if bindingA.ProjectID == bindingB.ProjectID {
		t.Fatalf("expected distinct project ids, both=%q", bindingA.ProjectID)
	}
}

func TestGatewayRemoteResolveWorktreeCreateTarget(t *testing.T) {
	appCore, server := newGatewayTestServer(t)
	defer server.Close()
	initGatewayGitWorkspace(t, appCore.Config().WorkspaceRoot)

	store := createGatewayAuthoritativeSession(t, appCore)

	remote, err := remoteclient.DialRemoteURLForProject(context.Background(), "ws"+server.URL[len("http"):], appCore.ProjectID())
	if err != nil {
		t.Fatalf("DialRemote: %v", err)
	}
	defer func() { _ = remote.Close() }()

	resp, err := remote.ResolveWorktreeCreateTarget(context.Background(), serverapi.WorktreeCreateTargetResolveRequest{
		SessionID: store.Meta().SessionID,
		Target:    "HEAD",
	})
	if err != nil {
		t.Fatalf("ResolveWorktreeCreateTarget: %v", err)
	}
	if resp.Resolution.Kind != serverapi.WorktreeCreateTargetResolutionKindDetachedRef {
		t.Fatalf("resolution kind = %q, want detached_ref", resp.Resolution.Kind)
	}
	if strings.TrimSpace(resp.Resolution.ResolvedRef) == "" {
		t.Fatalf("expected resolved ref oid, got %+v", resp.Resolution)
	}
}

func initGatewayGitWorkspace(t *testing.T, workspaceRoot string) {
	t.Helper()
	runGatewayGit(t, workspaceRoot, "init", "-b", "main")
	readmePath := filepath.Join(workspaceRoot, "README.md")
	if err := os.WriteFile(readmePath, []byte("gateway test\n"), 0o644); err != nil {
		t.Fatalf("WriteFile README.md: %v", err)
	}
	runGatewayGit(t, workspaceRoot, "add", "README.md")
	runGatewayGit(t, workspaceRoot, "commit", "-m", "init")
}

func runGatewayGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = appendTestGitCommitIdentityEnv(sanitizeTestGitEnv(os.Environ()))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
