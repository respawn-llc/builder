package main

import (
	"bytes"
	"context"
	"testing"

	"core/shared/serverapi"
	"core/shared/sessionenv"
)

type taskCompleteCommandRemote struct {
	*taskInterruptCommandRemote
	requests []serverapi.WorkflowTaskCompleteRequest
}

func (r *taskCompleteCommandRemote) CompleteWorkflowTask(
	_ context.Context,
	request serverapi.WorkflowTaskCompleteRequest,
) (serverapi.WorkflowTaskCompleteResponse, error) {
	r.requests = append(r.requests, request)
	return serverapi.WorkflowTaskCompleteResponse{TaskID: "task-1"}, nil
}

func TestTaskCompleteFromAttachedWorkflowSessionUsesThatSessionExact(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "attached-workflow-session")
	remote := &taskCompleteCommandRemote{taskInterruptCommandRemote: &taskInterruptCommandRemote{}}
	installWorkflowCommandRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := taskSubcommand(
		[]string{"complete", "--transition", "done", "--commentary", "replacement turn completed"},
		&stdout,
		&stderr,
	)

	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("task complete exit=%d stderr=%q", exitCode, stderr.String())
	}
	if len(remote.requests) != 1 {
		t.Fatalf("task complete requests = %+v, want one", remote.requests)
	}
	request := remote.requests[0]
	if request.ActorKind != serverapi.WorkflowTaskCompleteActorAgent ||
		request.AgentSessionID != "attached-workflow-session" ||
		request.TransitionID != "done" ||
		request.Commentary != "replacement turn completed" {
		t.Fatalf("task complete request = %+v, want attached Workflow Session completion", request)
	}
}
