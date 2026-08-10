package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"core/shared/serverapi"
	"core/shared/sessionenv"
)

const (
	worktreeOriginTestRunID  = "018fdd67-89ab-4cde-8123-456789abc001"
	worktreeOriginTestStepID = "018fdd67-89ab-4cde-8123-456789abc002"
)

type worktreeOriginCaptureRemote struct {
	createResolveCalls int
	createRequests     []serverapi.WorktreeCreateRequest
	enter              *serverapi.WorktreeEnterRequest
	leave              *serverapi.WorktreeLeaveRequest
	delete             *serverapi.WorktreeDeleteRequest
}

func (*worktreeOriginCaptureRemote) GetWorktreeStatus(context.Context, serverapi.WorktreeStatusRequest) (serverapi.WorktreeStatusResponse, error) {
	return serverapi.WorktreeStatusResponse{}, errors.New("unexpected worktree status request")
}

func (*worktreeOriginCaptureRemote) ListWorktrees(context.Context, serverapi.WorktreeListRequest) (serverapi.WorktreeListResponse, error) {
	return serverapi.WorktreeListResponse{}, errors.New("unexpected worktree list request")
}

func (r *worktreeOriginCaptureRemote) ResolveWorktreeCreateTarget(context.Context, serverapi.WorktreeCreateTargetResolveRequest) (serverapi.WorktreeCreateTargetResolveResponse, error) {
	r.createResolveCalls++
	return serverapi.WorktreeCreateTargetResolveResponse{
		Resolution: serverapi.WorktreeCreateTargetResolution{
			Kind: serverapi.WorktreeCreateTargetResolutionKindNewBranch,
		},
	}, nil
}

func (r *worktreeOriginCaptureRemote) CreateWorktree(_ context.Context, request serverapi.WorktreeCreateRequest) (serverapi.WorktreeCreateResponse, error) {
	r.createRequests = append(r.createRequests, request)
	return serverapi.WorktreeCreateResponse{}, nil
}

func (r *worktreeOriginCaptureRemote) EnterWorktree(_ context.Context, request serverapi.WorktreeEnterRequest) (serverapi.WorktreeScheduledAcknowledgement, error) {
	r.enter = &request
	return serverapi.WorktreeScheduledAcknowledgement{OperationID: request.OperationID}, nil
}

func (r *worktreeOriginCaptureRemote) LeaveWorktree(_ context.Context, request serverapi.WorktreeLeaveRequest) (serverapi.WorktreeScheduledAcknowledgement, error) {
	r.leave = &request
	return serverapi.WorktreeScheduledAcknowledgement{OperationID: request.OperationID}, nil
}

func (r *worktreeOriginCaptureRemote) DeleteWorktree(_ context.Context, request serverapi.WorktreeDeleteRequest) (serverapi.WorktreeDeleteResult, error) {
	r.delete = &request
	return serverapi.WorktreeDeleteResult{
		Kind: serverapi.WorktreeDeleteResultKindCompleted,
		Completed: &serverapi.WorktreeDeleteCompletedResult{
			Cleanup: serverapi.WorktreeBranchCleanupOutcome{Kind: serverapi.WorktreeBranchCleanupOutcomeNotRequested},
		},
	}, nil
}

func (*worktreeOriginCaptureRemote) Close() error {
	return nil
}

func replaceWorktreeOriginRemote(t *testing.T, remote *worktreeOriginCaptureRemote) {
	t.Helper()
	previous := worktreeCommandRemoteOpener
	worktreeCommandRemoteOpener = func(context.Context, string) (worktreeCommandRemote, error) {
		return remote, nil
	}
	t.Cleanup(func() {
		worktreeCommandRemoteOpener = previous
	})
}

func TestWorktreeTransitionCommandsDeriveOneRuntimeOrigin(t *testing.T) {
	remote := &worktreeOriginCaptureRemote{}
	replaceWorktreeOriginRemote(t, remote)
	t.Setenv(sessionenv.SessionIDEnv, "shell-session")
	t.Setenv(sessionenv.RunIDEnv, worktreeOriginTestRunID)
	t.Setenv(sessionenv.StepIDEnv, worktreeOriginTestStepID)

	for _, command := range []struct {
		name string
		run  func([]string, io.Writer, io.Writer) int
		args []string
	}{
		{name: "enter", run: worktreeEnterSubcommand, args: []string{"feature/a"}},
		{name: "leave", run: worktreeLeaveSubcommand},
		{name: "delete", run: worktreeDeleteSubcommand, args: []string{"feature/a"}},
	} {
		t.Run(command.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if exitCode := command.run(command.args, &stdout, &stderr); exitCode != 0 {
				t.Fatalf("exit=%d stderr=%s", exitCode, stderr.String())
			}
		})
	}

	for _, header := range []serverapi.WorktreeTransitionHeader{
		remote.enter.WorktreeTransitionHeader,
		remote.leave.WorktreeTransitionHeader,
		remote.delete.WorktreeTransitionHeader,
	} {
		if header.Origin == nil || header.Origin.RunID != worktreeOriginTestRunID || header.Origin.StepID != worktreeOriginTestStepID {
			t.Fatalf("transition header origin = %+v", header.Origin)
		}
	}
}

func TestWorktreeCreateCommandSendsOneSetupOperation(t *testing.T) {
	remote := &worktreeOriginCaptureRemote{}
	replaceWorktreeOriginRemote(t, remote)
	t.Setenv(sessionenv.SessionIDEnv, "shell-session")

	var stdout, stderr bytes.Buffer
	if exitCode := worktreeCreateSubcommand(
		[]string{"--json", "feature/create-once"},
		&stdout,
		&stderr,
	); exitCode != 0 {
		t.Fatalf("exit=%d stderr=%s", exitCode, stderr.String())
	}
	if remote.createResolveCalls != 1 || len(remote.createRequests) != 1 {
		t.Fatalf(
			"create calls = resolve:%d send:%d, want one each",
			remote.createResolveCalls,
			len(remote.createRequests),
		)
	}
	request := remote.createRequests[0]
	if err := request.SetupOperationID.Validate(); err != nil {
		t.Fatalf("setup operation ID = %q: %v", request.SetupOperationID, err)
	}
	if request.SessionID != "shell-session" ||
		request.BranchName != "feature/create-once" ||
		!request.CreateBranch {
		t.Fatalf("create request = %+v", request)
	}
}

func TestWorktreeTransitionCommandsRejectInvalidRuntimeOriginBeforeRPC(t *testing.T) {
	for _, origin := range []struct {
		name  string
		runID string
		step  string
	}{
		{name: "run_without_step", runID: worktreeOriginTestRunID},
		{name: "step_without_run", step: worktreeOriginTestStepID},
		{name: "malformed_run", runID: "not-a-uuid", step: worktreeOriginTestStepID},
		{name: "malformed_step", runID: worktreeOriginTestRunID, step: "not-a-uuid"},
	} {
		t.Run(origin.name, func(t *testing.T) {
			remote := &worktreeOriginCaptureRemote{}
			replaceWorktreeOriginRemote(t, remote)
			t.Setenv(sessionenv.SessionIDEnv, "shell-session")
			t.Setenv(sessionenv.RunIDEnv, origin.runID)
			t.Setenv(sessionenv.StepIDEnv, origin.step)

			for _, command := range []struct {
				name   string
				run    func([]string, io.Writer, io.Writer) int
				args   []string
				called func() bool
			}{
				{name: "enter", run: worktreeEnterSubcommand, args: []string{"feature/a"}, called: func() bool { return remote.enter != nil }},
				{name: "leave", run: worktreeLeaveSubcommand, called: func() bool { return remote.leave != nil }},
				{name: "delete", run: worktreeDeleteSubcommand, args: []string{"feature/a"}, called: func() bool { return remote.delete != nil }},
			} {
				t.Run(command.name, func(t *testing.T) {
					var stdout, stderr bytes.Buffer
					if exitCode := command.run(command.args, &stdout, &stderr); exitCode != 2 {
						t.Fatalf("exit=%d stderr=%s, want argument error", exitCode, stderr.String())
					}
					if command.called() {
						t.Fatal("worktree RPC ran with an invalid runtime origin")
					}
				})
			}
		})
	}
}

func TestExternalWorktreeTransitionCommandsHaveNoRuntimeOrigin(t *testing.T) {
	remote := &worktreeOriginCaptureRemote{}
	replaceWorktreeOriginRemote(t, remote)
	t.Setenv(sessionenv.SessionIDEnv, "shell-session")
	unsetWorktreeOriginEnvironment(t)

	for _, command := range []struct {
		name string
		run  func([]string, io.Writer, io.Writer) int
		args []string
	}{
		{name: "enter", run: worktreeEnterSubcommand, args: []string{"feature/a"}},
		{name: "leave", run: worktreeLeaveSubcommand},
		{name: "delete", run: worktreeDeleteSubcommand, args: []string{"feature/a"}},
	} {
		t.Run(command.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if exitCode := command.run(command.args, &stdout, &stderr); exitCode != 0 {
				t.Fatalf("exit=%d stderr=%s", exitCode, stderr.String())
			}
		})
	}

	for _, header := range []serverapi.WorktreeTransitionHeader{
		remote.enter.WorktreeTransitionHeader,
		remote.leave.WorktreeTransitionHeader,
		remote.delete.WorktreeTransitionHeader,
	} {
		if header.Origin != nil {
			t.Fatalf("external transition unexpectedly carried origin %+v", header.Origin)
		}
	}
}

func TestWorktreeDeleteForceBranchRequestsAuthoritativeForceCleanup(t *testing.T) {
	remote := &worktreeOriginCaptureRemote{}
	replaceWorktreeOriginRemote(t, remote)
	t.Setenv(sessionenv.SessionIDEnv, "")
	unsetWorktreeOriginEnvironment(t)

	var stdout, stderr bytes.Buffer
	exitCode := worktreeDeleteSubcommand([]string{
		"--session", "cleanup-session",
		"--delete-branch",
		"--force-delete-branch",
		"feature/a",
	}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("exit=%d stderr=%s", exitCode, stderr.String())
	}
	if remote.delete == nil {
		t.Fatal("worktree delete request was not sent")
	}
	if remote.delete.BranchCleanupPolicy != serverapi.WorktreeBranchCleanupModeDeleteForce {
		t.Fatalf("branch cleanup policy = %q, want force delete", remote.delete.BranchCleanupPolicy)
	}
}

func TestWorktreeDeleteForceBranchRequiresDeleteBranch(t *testing.T) {
	remote := &worktreeOriginCaptureRemote{}
	replaceWorktreeOriginRemote(t, remote)
	t.Setenv(sessionenv.SessionIDEnv, "")
	unsetWorktreeOriginEnvironment(t)

	var stdout, stderr bytes.Buffer
	exitCode := worktreeDeleteSubcommand([]string{
		"--session", "cleanup-session",
		"--force-delete-branch",
		"feature/a",
	}, &stdout, &stderr)
	if exitCode != 2 {
		t.Fatalf("exit=%d stderr=%s, want argument error", exitCode, stderr.String())
	}
	if remote.delete != nil {
		t.Fatal("worktree delete request was sent without --delete-branch")
	}
}

func unsetWorktreeOriginEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{sessionenv.RunIDEnv, sessionenv.StepIDEnv} {
		value, present := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unset %s: %v", name, err)
		}
		t.Cleanup(func() {
			if present {
				_ = os.Setenv(name, value)
				return
			}
			_ = os.Unsetenv(name)
		})
	}
}
