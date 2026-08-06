package main

import (
	"bytes"
	"context"
	"testing"

	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/sessionenv"
)

func TestTaskCompleteFromAgentShellTargetsItsActivatedSessionExecution(t *testing.T) {
	const sessionID = "9b9447ad-04e7-4c70-b4b0-f0eb1a53b47d"
	t.Setenv(sessionenv.SessionIDEnv, sessionID)
	remote := &taskCompleteCommandRemote{
		response: serverapi.WorkflowTaskCompleteResponse{
			TaskID: "task-activated-session",
			Handoff: serverapi.WorkflowTaskCompletionHandoff{
				SourceNodeDisplayName:  "Agent",
				DestinationDisplayName: "Done",
			},
		},
	}
	previous := workflowCommandRemoteOpener
	workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
		return config.App{}, remote, nil
	}
	t.Cleanup(func() {
		workflowCommandRemoteOpener = previous
	})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := taskSubcommand(
		[]string{"complete", "--transition", "done", "--commentary", "completed interactively"},
		&stdout,
		&stderr,
	)
	if exitCode != 0 {
		t.Fatalf("task complete exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if len(remote.requests) != 1 {
		t.Fatalf("CompleteWorkflowTask requests = %d, want 1", len(remote.requests))
	}
	request := remote.requests[0]
	if request.ActorKind != serverapi.WorkflowTaskCompleteActorAgent ||
		request.AgentSessionID != sessionID ||
		request.SessionID != "" ||
		request.TaskID != "" ||
		request.TransitionID != "done" ||
		request.Commentary != "completed interactively" {
		t.Fatalf("CompleteWorkflowTask request = %+v, want activated agent Session target", request)
	}
}

type taskCompleteCommandRemote struct {
	workflowCommandRemote
	requests []serverapi.WorkflowTaskCompleteRequest
	response serverapi.WorkflowTaskCompleteResponse
}

func (r *taskCompleteCommandRemote) CompleteWorkflowTask(
	_ context.Context,
	request serverapi.WorkflowTaskCompleteRequest,
) (serverapi.WorkflowTaskCompleteResponse, error) {
	r.requests = append(r.requests, request)
	return r.response, nil
}

func (r *taskCompleteCommandRemote) Close() error {
	return nil
}
