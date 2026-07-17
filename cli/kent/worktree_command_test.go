package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"core/internal/testharness/testsetup"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
)

func TestWorktreeStatusUsesShellSessionFromNestedWorkspaceDirectory(t *testing.T) {
	workspace := t.TempDir()
	testsetup.InitializeGitRepository(t, workspace)
	configureWorktreeCommandWorkspaceServer(t, workspace)
	_, binding, sess := newBindingCommandSession(t, workspace)
	if err := sess.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	cleanup := startBindingCommandServer(t, workspace)
	defer cleanup()
	disableWorktreeCommandLocalSocket(t, workspace)
	nested := filepath.Join(workspace, "pkg")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatalf("Mkdir nested cwd: %v", err)
	}
	t.Chdir(nested)
	t.Setenv("KENT_SESSION_ID", sess.Meta().SessionID)

	status := decodeWorktreeCommandJSON[serverapi.WorktreeStatusResponse](
		t,
		runSuccessfulWorktreeCommand(t, "worktree", "status", "--json"),
	)
	if status.Target.WorkspaceID != binding.WorkspaceID {
		t.Fatalf("target workspace id = %q, want %q", status.Target.WorkspaceID, binding.WorkspaceID)
	}
	if status.Worktree.RecordedRoot != binding.CanonicalRoot {
		t.Fatalf("recorded root = %q, want %q", status.Worktree.RecordedRoot, binding.CanonicalRoot)
	}
}

func TestWorktreeListUsesWorkspaceWithoutShellSession(t *testing.T) {
	workspace := t.TempDir()
	testsetup.InitializeGitRepository(t, workspace)
	_, binding, _ := newBindingCommandSession(t, workspace)
	configureWorktreeCommandWorkspaceServer(t, workspace)
	cleanup := startBindingCommandServer(t, workspace)
	defer cleanup()
	disableWorktreeCommandLocalSocket(t, workspace)
	nested := filepath.Join(workspace, "pkg")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatalf("Mkdir nested cwd: %v", err)
	}
	t.Chdir(nested)
	t.Setenv("KENT_SESSION_ID", "")

	response := decodeWorktreeCommandJSON[serverapi.WorktreeWorkspaceListResponse](
		t,
		runSuccessfulWorktreeCommand(t, "worktree", "list", "--json"),
	)
	if response.WorkspaceID != binding.WorkspaceID || len(response.Worktrees) != 1 {
		t.Fatalf("workspace list = %+v", response)
	}
	if response.Worktrees[0].Projection.IsCurrent {
		t.Fatalf("workspace projection = %+v, want markerless", response.Worktrees[0].Projection)
	}
}

func TestWorktreeListUsesSessionProjectionWhenSessionIsPresent(t *testing.T) {
	remote := &worktreeCommandTestRemote{
		list: serverapi.WorktreeListResponse{
			Target: clientui.SessionExecutionTarget{WorkspaceID: "workspace-1", WorkspaceRoot: "/repo"},
			Worktrees: []serverapi.WorktreeListEntry{{
				Topology: serverapi.WorktreeTopologyEntry{
					Variant: serverapi.WorktreeTopologyVariantExternal,
					External: &serverapi.WorktreeExternalFacts{Git: serverapi.WorktreeGitFacts{
						CanonicalRoot: "/repo",
						HeadObject:    "deadbeef",
						IsMain:        true,
						PathAvailable: true,
					}},
				},
				Projection: serverapi.WorktreeListProjection{Selector: "main", IsCurrent: true},
			}},
		},
	}
	replaceWorktreeCommandRemote(t, remote)
	t.Setenv("KENT_SESSION_ID", "shell-session")

	output := runSuccessfulWorktreeCommand(t, "worktree", "list", "--json")
	if remote.listRequest == nil || remote.listRequest.SessionID != "shell-session" {
		t.Fatalf("session list request = %+v", remote.listRequest)
	}
	response := decodeWorktreeCommandJSON[serverapi.WorktreeListResponse](t, output)
	if len(response.Worktrees) != 1 || !response.Worktrees[0].Projection.IsCurrent {
		t.Fatalf("session list = %+v, want current projection", response)
	}
}

func TestWorktreeListUsesExplicitSessionProjectionOutsideShell(t *testing.T) {
	remote := &worktreeCommandTestRemote{
		list: serverapi.WorktreeListResponse{
			Target: clientui.SessionExecutionTarget{WorkspaceID: "workspace-1", WorkspaceRoot: "/repo"},
		},
	}
	replaceWorktreeCommandRemote(t, remote)
	t.Setenv("KENT_SESSION_ID", "")

	runSuccessfulWorktreeCommand(t, "worktree", "list", "--session", "explicit-session")
	if remote.listRequest == nil || remote.listRequest.SessionID != "explicit-session" {
		t.Fatalf("session list request = %+v", remote.listRequest)
	}
}

func runSuccessfulWorktreeCommand(t *testing.T, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if exitCode := rootCommand(args, strings.NewReader(""), &stdout, &stderr); exitCode != 0 {
		t.Fatalf("%v exit=%d stderr=%s", args, exitCode, stderr.String())
	}
	return stdout.String()
}

func decodeWorktreeCommandJSON[T any](t *testing.T, output string) T {
	t.Helper()
	var value T
	if err := json.Unmarshal([]byte(output), &value); err != nil {
		t.Fatalf("decode worktree command JSON: %v", err)
	}
	return value
}

func disableWorktreeCommandLocalSocket(t *testing.T, workspace string) {
	t.Helper()
	cfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load workspace: %v", err)
	}
	socketPath, ok, err := config.ServerLocalRPCSocketPath(cfg)
	if err != nil {
		t.Fatalf("ServerLocalRPCSocketPath: %v", err)
	}
	if !ok {
		return
	}
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove local RPC socket: %v", err)
	}
}

func configureWorktreeCommandWorkspaceServer(t *testing.T, workspace string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	reserveBindingCommandTestPort(t, listener)
	configDir := filepath.Join(workspace, config.ConfigDirName)
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("Mkdir workspace config: %v", err)
	}
	settings := fmt.Sprintf(
		"server_host = \"127.0.0.1\"\nserver_port = %d\n",
		listener.Addr().(*net.TCPAddr).Port,
	)
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(settings), 0o644); err != nil {
		t.Fatalf("write workspace config: %v", err)
	}
}

type worktreeCommandTestRemote struct {
	statusRequest *serverapi.WorktreeStatusRequest
	status        serverapi.WorktreeStatusResponse
	listRequest   *serverapi.WorktreeListRequest
	list          serverapi.WorktreeListResponse
	resolve       serverapi.WorktreeCreateTargetResolveResponse
	createRequest *serverapi.WorktreeCreateRequest
	create        serverapi.WorktreeCreateResponse
	enterRequest  *serverapi.WorktreeEnterRequest
	leaveRequest  *serverapi.WorktreeLeaveRequest
	deleteRequest *serverapi.WorktreeDeleteRequest
	deleteResult  serverapi.WorktreeDeleteResult
}

func (r *worktreeCommandTestRemote) GetWorktreeStatus(_ context.Context, req serverapi.WorktreeStatusRequest) (serverapi.WorktreeStatusResponse, error) {
	request := req
	r.statusRequest = &request
	return r.status, nil
}

func (r *worktreeCommandTestRemote) ListWorktrees(_ context.Context, req serverapi.WorktreeListRequest) (serverapi.WorktreeListResponse, error) {
	request := req
	r.listRequest = &request
	return r.list, nil
}

func (r *worktreeCommandTestRemote) ResolveWorktreeCreateTarget(_ context.Context, _ serverapi.WorktreeCreateTargetResolveRequest) (serverapi.WorktreeCreateTargetResolveResponse, error) {
	return r.resolve, nil
}

func (r *worktreeCommandTestRemote) CreateWorktree(_ context.Context, req serverapi.WorktreeCreateRequest) (serverapi.WorktreeCreateResponse, error) {
	request := req
	r.createRequest = &request
	return r.create, nil
}

func (r *worktreeCommandTestRemote) EnterWorktree(_ context.Context, req serverapi.WorktreeEnterRequest) (serverapi.WorktreeScheduledAcknowledgement, error) {
	request := req
	r.enterRequest = &request
	return serverapi.WorktreeScheduledAcknowledgement{OperationID: req.OperationID}, nil
}

func (r *worktreeCommandTestRemote) LeaveWorktree(_ context.Context, req serverapi.WorktreeLeaveRequest) (serverapi.WorktreeScheduledAcknowledgement, error) {
	request := req
	r.leaveRequest = &request
	return serverapi.WorktreeScheduledAcknowledgement{OperationID: req.OperationID}, nil
}

func (r *worktreeCommandTestRemote) DeleteWorktree(_ context.Context, req serverapi.WorktreeDeleteRequest) (serverapi.WorktreeDeleteResult, error) {
	request := req
	r.deleteRequest = &request
	return r.deleteResult, nil
}

func (*worktreeCommandTestRemote) Close() error { return nil }

func replaceWorktreeCommandRemote(t *testing.T, remote *worktreeCommandTestRemote) {
	t.Helper()
	previous := worktreeCommandRemoteOpener
	worktreeCommandRemoteOpener = func(context.Context, string) (worktreeCommandRemote, error) {
		return remote, nil
	}
	t.Cleanup(func() { worktreeCommandRemoteOpener = previous })
}

func TestWorktreeStatusUsesShellSessionAndJSONHasNoSelector(t *testing.T) {
	remote := &worktreeCommandTestRemote{status: serverapi.WorktreeStatusResponse{
		Target:   clientui.SessionExecutionTarget{WorkspaceID: "workspace", WorkspaceRoot: "/repo"},
		Worktree: serverapi.WorktreeStatusTarget{RecordedRoot: "/repo"},
	}}
	replaceWorktreeCommandRemote(t, remote)
	t.Setenv("KENT_SESSION_ID", "shell-session")
	output := runSuccessfulWorktreeCommand(t, "worktree", "status", "--json", "--session", "ignored")
	if remote.statusRequest == nil || remote.statusRequest.SessionID != "shell-session" {
		t.Fatalf("status request = %+v", remote.statusRequest)
	}
	payload := decodeWorktreeCommandJSON[struct {
		Worktree map[string]json.RawMessage `json:"worktree"`
	}](t, output)
	if _, exists := payload.Worktree["selector"]; exists {
		t.Fatalf("status JSON exposed selector: %+v", payload.Worktree)
	}
}

func TestWorktreeEnterAndLeaveReturnScheduledAcknowledgements(t *testing.T) {
	remote := &worktreeCommandTestRemote{}
	replaceWorktreeCommandRemote(t, remote)
	t.Setenv("KENT_SESSION_ID", "shell-session")
	for _, args := range [][]string{
		{"worktree", "enter", "--json", "feature/a"},
		{"worktree", "leave", "--json"},
	} {
		acknowledgement := decodeWorktreeCommandJSON[serverapi.WorktreeScheduledAcknowledgement](
			t,
			runSuccessfulWorktreeCommand(t, args...),
		)
		if err := acknowledgement.OperationID.Validate(); err != nil {
			t.Fatalf("%v acknowledgement = %+v: %v", args, acknowledgement, err)
		}
	}
	if remote.enterRequest == nil || remote.enterRequest.SessionID != "shell-session" || remote.enterRequest.Selector != "feature/a" {
		t.Fatalf("enter request = %+v", remote.enterRequest)
	}
	if remote.leaveRequest == nil || remote.leaveRequest.SessionID != "shell-session" {
		t.Fatalf("leave request = %+v", remote.leaveRequest)
	}
}

func TestWorktreeCreateDoesNotEnterAndReturnsCreatedSelector(t *testing.T) {
	remote := &worktreeCommandTestRemote{
		resolve: serverapi.WorktreeCreateTargetResolveResponse{
			Resolution: serverapi.WorktreeCreateTargetResolution{
				Input: "feature/a",
				Kind:  serverapi.WorktreeCreateTargetResolutionKindNewBranch,
			},
		},
		create: serverapi.WorktreeCreateResponse{
			Worktree: serverapi.WorktreeListEntry{
				Topology: serverapi.WorktreeTopologyEntry{
					Variant: serverapi.WorktreeTopologyVariantRegistered,
					Registered: &serverapi.WorktreeRegisteredFacts{
						Git: serverapi.WorktreeGitFacts{
							CanonicalRoot: "/wt/a",
							HeadObject:    "deadbeef",
							PathAvailable: true,
						},
						Kent: serverapi.WorktreeKentFacts{
							WorktreeID:    "worktree-a",
							CanonicalRoot: "/wt/a",
							DisplayName:   "a",
							Managed:       true,
						},
					},
				},
				Projection: serverapi.WorktreeListProjection{Selector: "worktree-a"},
			},
		},
	}
	replaceWorktreeCommandRemote(t, remote)
	t.Setenv("KENT_SESSION_ID", "shell-session")
	output := runSuccessfulWorktreeCommand(t, "worktree", "create", "--json", "--base", "main", "feature/a")
	if remote.createRequest == nil || remote.createRequest.SessionID != "shell-session" || !remote.createRequest.CreateBranch || remote.createRequest.BaseRef != "main" {
		t.Fatalf("create request = %+v", remote.createRequest)
	}
	if remote.enterRequest != nil {
		t.Fatalf("create unexpectedly entered worktree: %+v", remote.enterRequest)
	}
	response := decodeWorktreeCommandJSON[serverapi.WorktreeCreateResponse](t, output)
	if response.Worktree.Projection.Selector != "worktree-a" {
		t.Fatalf("created selector = %q, want worktree-a", response.Worktree.Projection.Selector)
	}
}

func TestAgentWorktreeDeleteRetainsBranchAndRejectsDeleteBranch(t *testing.T) {
	remote := &worktreeCommandTestRemote{
		deleteResult: serverapi.WorktreeDeleteResult{
			Kind: serverapi.WorktreeDeleteResultKindCompleted,
			Completed: &serverapi.WorktreeDeleteCompletedResult{
				Cleanup: serverapi.WorktreeBranchCleanupOutcome{Kind: serverapi.WorktreeBranchCleanupOutcomeNotRequested},
			},
		},
	}
	replaceWorktreeCommandRemote(t, remote)
	t.Setenv("KENT_SESSION_ID", "shell-session")
	runSuccessfulWorktreeCommand(t, "worktree", "delete", "--force", "feature/a")
	if remote.deleteRequest == nil || remote.deleteRequest.BranchCleanupPolicy != serverapi.WorktreeBranchCleanupModeRetain || !remote.deleteRequest.ForceFolderRemoval {
		t.Fatalf("delete request = %+v", remote.deleteRequest)
	}
	var stdout, stderr bytes.Buffer
	if exitCode := rootCommand([]string{"worktree", "delete", "--delete-branch", "feature/a"}, strings.NewReader(""), &stdout, &stderr); exitCode != 2 {
		t.Fatalf("delete-branch exit=%d stderr=%s", exitCode, stderr.String())
	}
}

func TestHumanWorktreeDeleteReportsRetainedBranchCleanup(t *testing.T) {
	branchName := "feature/a"
	diagnostic := "branch is not fully merged"
	remote := &worktreeCommandTestRemote{
		deleteResult: serverapi.WorktreeDeleteResult{
			Kind: serverapi.WorktreeDeleteResultKindCompleted,
			Completed: &serverapi.WorktreeDeleteCompletedResult{
				Cleanup: serverapi.WorktreeBranchCleanupOutcome{
					Kind:       serverapi.WorktreeBranchCleanupOutcomeRetained,
					BranchName: &branchName,
					Diagnostic: &diagnostic,
				},
			},
		},
	}
	replaceWorktreeCommandRemote(t, remote)
	t.Setenv("KENT_SESSION_ID", "")
	output := runSuccessfulWorktreeCommand(
		t,
		"worktree",
		"delete",
		"--session",
		"human-session",
		"--delete-branch",
		"feature/a",
	)
	if !strings.Contains(output, branchName) || !strings.Contains(output, diagnostic) {
		t.Fatalf("delete output = %q, want retained branch facts %q and %q", output, branchName, diagnostic)
	}
}
