package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"core/shared/clientui"
	"core/shared/serverapi"
)

type worktreeCommandTestRemote struct {
	request serverapi.WorktreeStatusRequest
	status  serverapi.WorktreeStatusResponse
}

func (r *worktreeCommandTestRemote) GetWorktreeStatus(_ context.Context, req serverapi.WorktreeStatusRequest) (serverapi.WorktreeStatusResponse, error) {
	r.request = req
	return r.status, nil
}

func (*worktreeCommandTestRemote) Close() error { return nil }

func TestWorktreeStatusUsesShellSessionAndJSONHasNoSelector(t *testing.T) {
	remote := &worktreeCommandTestRemote{status: serverapi.WorktreeStatusResponse{
		Target:   clientui.SessionExecutionTarget{WorkspaceID: "workspace", WorkspaceRoot: "/repo"},
		Worktree: serverapi.WorktreeStatusTarget{RecordedRoot: "/repo"},
	}}
	previous := worktreeCommandRemoteOpener
	worktreeCommandRemoteOpener = func(context.Context) (worktreeCommandRemote, error) { return remote, nil }
	t.Cleanup(func() { worktreeCommandRemoteOpener = previous })
	t.Setenv("KENT_SESSION_ID", "shell-session")
	var stdout, stderr bytes.Buffer
	if exitCode := rootCommand([]string{"worktree", "status", "--json", "--session", "ignored"}, strings.NewReader(""), &stdout, &stderr); exitCode != 0 {
		t.Fatalf("rootCommand exit=%d stderr=%s", exitCode, stderr.String())
	}
	if remote.request.SessionID != "shell-session" {
		t.Fatalf("status session = %q", remote.request.SessionID)
	}
	if strings.Contains(stdout.String(), "selector") {
		t.Fatalf("status JSON exposed selector: %s", stdout.String())
	}
}
