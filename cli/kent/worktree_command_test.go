package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"core/shared/client"
	"core/shared/clientui"
	"core/shared/serverapi"
)

func TestOpenWorktreeCommandRemoteAttachesCurrentProjectWorkspace(t *testing.T) {
	workspace := t.TempDir()
	binding := registerBindingCommandWorkspace(t, workspace)
	cleanup := startBindingCommandServer(t, workspace)
	defer cleanup()
	t.Chdir(workspace)

	remoteClient, err := openWorktreeCommandRemote(context.Background())
	if err != nil {
		t.Fatalf("openWorktreeCommandRemote: %v", err)
	}
	defer func() { _ = remoteClient.Close() }()
	remote, ok := remoteClient.(*client.Remote)
	if !ok {
		t.Fatalf("remote type = %T, want *client.Remote", remoteClient)
	}
	if remote.ProjectID() != binding.ProjectID || remote.WorkspaceID() != binding.WorkspaceID {
		t.Fatalf(
			"attachment = project %q workspace %q, want %q %q",
			remote.ProjectID(),
			remote.WorkspaceID(),
			binding.ProjectID,
			binding.WorkspaceID,
		)
	}
}

type worktreeCommandTestRemote struct {
	statusRequest serverapi.WorktreeStatusRequest
	status        serverapi.WorktreeStatusResponse
	listRequest   serverapi.WorktreeListRequest
	list          serverapi.WorktreeListResponse
	resolve       serverapi.WorktreeCreateTargetResolveResponse
	createRequest serverapi.WorktreeCreateRequest
	create        serverapi.WorktreeCreateResponse
	enterRequest  serverapi.WorktreeEnterRequest
	leaveRequest  serverapi.WorktreeLeaveRequest
	deleteRequest serverapi.WorktreeDeleteRequest
	deleteResult  serverapi.WorktreeDeleteResult
}

func (r *worktreeCommandTestRemote) GetWorktreeStatus(_ context.Context, req serverapi.WorktreeStatusRequest) (serverapi.WorktreeStatusResponse, error) {
	r.statusRequest = req
	return r.status, nil
}

func (r *worktreeCommandTestRemote) ListWorktrees(_ context.Context, req serverapi.WorktreeListRequest) (serverapi.WorktreeListResponse, error) {
	r.listRequest = req
	return r.list, nil
}

func (r *worktreeCommandTestRemote) ResolveWorktreeCreateTarget(_ context.Context, _ serverapi.WorktreeCreateTargetResolveRequest) (serverapi.WorktreeCreateTargetResolveResponse, error) {
	return r.resolve, nil
}

func (r *worktreeCommandTestRemote) CreateWorktree(_ context.Context, req serverapi.WorktreeCreateRequest) (serverapi.WorktreeCreateResponse, error) {
	r.createRequest = req
	return r.create, nil
}

func (r *worktreeCommandTestRemote) EnterWorktree(_ context.Context, req serverapi.WorktreeEnterRequest) (serverapi.WorktreeScheduledAcknowledgement, error) {
	r.enterRequest = req
	return serverapi.WorktreeScheduledAcknowledgement{OperationID: req.OperationID}, nil
}

func (r *worktreeCommandTestRemote) LeaveWorktree(_ context.Context, req serverapi.WorktreeLeaveRequest) (serverapi.WorktreeScheduledAcknowledgement, error) {
	r.leaveRequest = req
	return serverapi.WorktreeScheduledAcknowledgement{OperationID: req.OperationID}, nil
}

func (r *worktreeCommandTestRemote) DeleteWorktree(_ context.Context, req serverapi.WorktreeDeleteRequest) (serverapi.WorktreeDeleteResult, error) {
	r.deleteRequest = req
	return r.deleteResult, nil
}

func (*worktreeCommandTestRemote) Close() error { return nil }

func replaceWorktreeCommandRemote(t *testing.T, remote *worktreeCommandTestRemote) {
	t.Helper()
	previous := worktreeCommandRemoteOpener
	worktreeCommandRemoteOpener = func(context.Context) (worktreeCommandRemote, error) { return remote, nil }
	t.Cleanup(func() { worktreeCommandRemoteOpener = previous })
}

func TestWorktreeStatusUsesShellSessionAndJSONHasNoSelector(t *testing.T) {
	remote := &worktreeCommandTestRemote{status: serverapi.WorktreeStatusResponse{
		Target:   clientui.SessionExecutionTarget{WorkspaceID: "workspace", WorkspaceRoot: "/repo"},
		Worktree: serverapi.WorktreeStatusTarget{RecordedRoot: "/repo"},
	}}
	replaceWorktreeCommandRemote(t, remote)
	t.Setenv("KENT_SESSION_ID", "shell-session")
	var stdout, stderr bytes.Buffer
	if exitCode := rootCommand([]string{"worktree", "status", "--json", "--session", "ignored"}, strings.NewReader(""), &stdout, &stderr); exitCode != 0 {
		t.Fatalf("rootCommand exit=%d stderr=%s", exitCode, stderr.String())
	}
	if remote.statusRequest.SessionID != "shell-session" {
		t.Fatalf("status session = %q", remote.statusRequest.SessionID)
	}
	if strings.Contains(stdout.String(), "selector") {
		t.Fatalf("status JSON exposed selector: %s", stdout.String())
	}
}

func TestWorktreeEnterAndLeaveReturnScheduledAcknowledgements(t *testing.T) {
	remote := &worktreeCommandTestRemote{}
	replaceWorktreeCommandRemote(t, remote)
	t.Setenv("KENT_SESSION_ID", "shell-session")
	for _, args := range [][]string{
		{"worktree", "enter", "feature/a"},
		{"worktree", "leave"},
	} {
		var stdout, stderr bytes.Buffer
		if exitCode := rootCommand(args, strings.NewReader(""), &stdout, &stderr); exitCode != 0 {
			t.Fatalf("%v exit=%d stderr=%s", args, exitCode, stderr.String())
		}
		if !strings.Contains(stdout.String(), "Scheduled:") {
			t.Fatalf("%v stdout=%q", args, stdout.String())
		}
	}
	if remote.enterRequest.SessionID != "shell-session" || remote.enterRequest.Selector != "feature/a" {
		t.Fatalf("enter request = %+v", remote.enterRequest)
	}
	if remote.leaveRequest.SessionID != "shell-session" {
		t.Fatalf("leave request = %+v", remote.leaveRequest)
	}
}

func TestWorktreeCreateDoesNotEnterAndPrintsEnterAction(t *testing.T) {
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
	var stdout, stderr bytes.Buffer
	if exitCode := rootCommand([]string{"worktree", "create", "--base", "main", "feature/a"}, strings.NewReader(""), &stdout, &stderr); exitCode != 0 {
		t.Fatalf("exit=%d stderr=%s", exitCode, stderr.String())
	}
	if remote.createRequest.SessionID != "shell-session" || !remote.createRequest.CreateBranch || remote.createRequest.BaseRef != "main" {
		t.Fatalf("create request = %+v", remote.createRequest)
	}
	if remote.enterRequest.OperationID.Validate() == nil {
		t.Fatalf("create unexpectedly entered worktree: %+v", remote.enterRequest)
	}
	if !strings.Contains(stdout.String(), "worktree enter worktree-a") {
		t.Fatalf("stdout=%q", stdout.String())
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
	var stdout, stderr bytes.Buffer
	if exitCode := rootCommand([]string{"worktree", "delete", "--force", "feature/a"}, strings.NewReader(""), &stdout, &stderr); exitCode != 0 {
		t.Fatalf("delete exit=%d stderr=%s", exitCode, stderr.String())
	}
	if remote.deleteRequest.BranchCleanupPolicy != serverapi.WorktreeBranchCleanupModeRetain || !remote.deleteRequest.ForceFolderRemoval {
		t.Fatalf("delete request = %+v", remote.deleteRequest)
	}
	stdout.Reset()
	stderr.Reset()
	if exitCode := rootCommand([]string{"worktree", "delete", "--delete-branch", "feature/a"}, strings.NewReader(""), &stdout, &stderr); exitCode != 2 {
		t.Fatalf("delete-branch exit=%d stderr=%s", exitCode, stderr.String())
	}
}
