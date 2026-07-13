package main

import (
	"bytes"
	"context"
	"encoding/json"
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
	if remote.statusRequest == nil || remote.statusRequest.SessionID != "shell-session" {
		t.Fatalf("status request = %+v", remote.statusRequest)
	}
	var payload struct {
		Worktree map[string]json.RawMessage `json:"worktree"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("decode status JSON: %v", err)
	}
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
		var stdout, stderr bytes.Buffer
		if exitCode := rootCommand(args, strings.NewReader(""), &stdout, &stderr); exitCode != 0 {
			t.Fatalf("%v exit=%d stderr=%s", args, exitCode, stderr.String())
		}
		var acknowledgement serverapi.WorktreeScheduledAcknowledgement
		if err := json.Unmarshal(stdout.Bytes(), &acknowledgement); err != nil {
			t.Fatalf("%v decode acknowledgement: %v", args, err)
		}
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
	var stdout, stderr bytes.Buffer
	if exitCode := rootCommand([]string{"worktree", "create", "--json", "--base", "main", "feature/a"}, strings.NewReader(""), &stdout, &stderr); exitCode != 0 {
		t.Fatalf("exit=%d stderr=%s", exitCode, stderr.String())
	}
	if remote.createRequest == nil || remote.createRequest.SessionID != "shell-session" || !remote.createRequest.CreateBranch || remote.createRequest.BaseRef != "main" {
		t.Fatalf("create request = %+v", remote.createRequest)
	}
	if remote.enterRequest != nil {
		t.Fatalf("create unexpectedly entered worktree: %+v", remote.enterRequest)
	}
	var response serverapi.WorktreeCreateResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
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
	var stdout, stderr bytes.Buffer
	if exitCode := rootCommand([]string{"worktree", "delete", "--force", "feature/a"}, strings.NewReader(""), &stdout, &stderr); exitCode != 0 {
		t.Fatalf("delete exit=%d stderr=%s", exitCode, stderr.String())
	}
	if remote.deleteRequest == nil || remote.deleteRequest.BranchCleanupPolicy != serverapi.WorktreeBranchCleanupModeRetain || !remote.deleteRequest.ForceFolderRemoval {
		t.Fatalf("delete request = %+v", remote.deleteRequest)
	}
	stdout.Reset()
	stderr.Reset()
	if exitCode := rootCommand([]string{"worktree", "delete", "--delete-branch", "feature/a"}, strings.NewReader(""), &stdout, &stderr); exitCode != 2 {
		t.Fatalf("delete-branch exit=%d stderr=%s", exitCode, stderr.String())
	}
}
