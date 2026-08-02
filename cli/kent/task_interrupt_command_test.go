package main

import (
	"bytes"
	"context"
	"testing"

	"core/shared/apicontract"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/sessionenv"
)

type taskInterruptCommandRemote struct {
	apicontract.WorkflowService
	interruptRequests []serverapi.WorkflowTaskInterruptRequest
	resumeRequests    []serverapi.WorkflowTaskResumeRequest
	moveRequests      []serverapi.WorkflowTaskMoveRequest
}

func (r *taskInterruptCommandRemote) GetWorkflowTask(_ context.Context, req serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
	return serverapi.WorkflowTaskGetResponse{
		Task: serverapi.WorkflowTaskDetail{
			Summary: serverapi.WorkflowTaskSummary{ID: req.TaskID, ShortID: "KENT-1"},
		},
	}, nil
}

func (r *taskInterruptCommandRemote) ListWorkflowTaskAttention(context.Context, serverapi.WorkflowTaskAttentionListRequest) (serverapi.WorkflowTaskAttentionListResponse, error) {
	return serverapi.WorkflowTaskAttentionListResponse{Items: []serverapi.WorkflowAttentionItem{}}, nil
}

func (r *taskInterruptCommandRemote) InterruptWorkflowTask(_ context.Context, req serverapi.WorkflowTaskInterruptRequest) (serverapi.WorkflowTaskInterruptResponse, error) {
	r.interruptRequests = append(r.interruptRequests, req)
	return serverapi.WorkflowTaskInterruptResponse{}, nil
}

func (r *taskInterruptCommandRemote) ResumeWorkflowTask(_ context.Context, req serverapi.WorkflowTaskResumeRequest) (serverapi.WorkflowTaskResumeResponse, error) {
	r.resumeRequests = append(r.resumeRequests, req)
	return serverapi.WorkflowTaskResumeResponse{
		Outcome: serverapi.WorkflowExecutionTargetActionOutcomeApplied,
		Applied: &serverapi.WorkflowTaskResumeApplied{
			CurrentNodes: []serverapi.WorkflowTaskCurrentNode{{NodeID: "node-1"}},
		},
	}, nil
}

func (r *taskInterruptCommandRemote) MoveWorkflowTask(_ context.Context, req serverapi.WorkflowTaskMoveRequest) (serverapi.WorkflowTaskMoveResponse, error) {
	r.moveRequests = append(r.moveRequests, req)
	return serverapi.WorkflowTaskMoveResponse{
		Outcome: serverapi.WorkflowExecutionTargetActionOutcomeApplied,
		Applied: &serverapi.WorkflowTaskMoveApplied{
			CurrentNodes: []serverapi.WorkflowTaskCurrentNode{{NodeID: req.TargetNodeID}},
		},
	}, nil
}

func (r *taskInterruptCommandRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, nil
}

func (r *taskInterruptCommandRemote) Close() error {
	return nil
}

func TestTaskInterruptTargetsTheResolvedTask(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "")
	remote := &taskInterruptCommandRemote{}
	previous := workflowCommandRemoteOpener
	workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
		return config.App{}, remote, nil
	}
	t.Cleanup(func() {
		workflowCommandRemoteOpener = previous
	})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := taskSubcommand([]string{"interrupt", "task-1", "--session", "session-1", "--reason", "operator request"}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", exitCode, stderr.String())
	}
	want := serverapi.WorkflowTaskInterruptRequest{TaskID: "task-1", SessionID: "session-1", Reason: "operator request"}
	if len(remote.interruptRequests) != 1 || remote.interruptRequests[0] != want {
		t.Fatalf("interrupt requests = %+v, want %+v", remote.interruptRequests, want)
	}
	if got := stdout.String(); got != "Interrupted KENT-1.\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestTaskInterruptFromWorkflowSessionCarriesInvokingSession(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "agent-session")
	remote := &taskInterruptCommandRemote{}
	previous := workflowCommandRemoteOpener
	workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
		return config.App{}, remote, nil
	}
	t.Cleanup(func() {
		workflowCommandRemoteOpener = previous
	})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := taskSubcommand([]string{"interrupt", "task-1"}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", exitCode, stderr.String())
	}
	if len(remote.interruptRequests) != 1 ||
		remote.interruptRequests[0].InvokingSessionID == nil ||
		remote.interruptRequests[0].InvokingSessionID.String() != "agent-session" {
		t.Fatalf("interrupt requests = %+v, want invoking Session agent-session", remote.interruptRequests)
	}
}

func TestTaskResumeFromWorkflowSessionCarriesInvokingSession(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "agent-session")
	remote := &taskInterruptCommandRemote{}
	previous := workflowCommandRemoteOpener
	workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
		return config.App{}, remote, nil
	}
	t.Cleanup(func() {
		workflowCommandRemoteOpener = previous
	})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := taskSubcommand([]string{"resume", "task-1"}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", exitCode, stderr.String())
	}
	if len(remote.resumeRequests) != 1 ||
		remote.resumeRequests[0].InvokingSessionID == nil ||
		remote.resumeRequests[0].InvokingSessionID.String() != "agent-session" {
		t.Fatalf("resume requests = %+v, want invoking Session agent-session", remote.resumeRequests)
	}
}

func TestTaskMoveCarriesExecutionTargetAndSetupOperation(t *testing.T) {
	remote := &taskInterruptCommandRemote{}
	previous := workflowCommandRemoteOpener
	workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
		return config.App{}, remote, nil
	}
	t.Cleanup(func() {
		workflowCommandRemoteOpener = previous
	})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := taskSubcommand([]string{"move", "task-1", "done", "--execution-target", "head"}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", exitCode, stderr.String())
	}
	if len(remote.moveRequests) != 1 {
		t.Fatalf("move requests = %+v, want one", remote.moveRequests)
	}
	request := remote.moveRequests[0]
	if request.TaskID != "task-1" || request.TargetNodeID != "done" || request.SetupOperationID.Validate() != nil ||
		request.ExecutionTarget == nil || request.ExecutionTarget.Mode != serverapi.WorkflowExecutionTargetModeHead {
		t.Fatalf("move request = %+v", request)
	}
}
