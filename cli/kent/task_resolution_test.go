package main

import (
	"context"
	"errors"
	"testing"

	"core/shared/apicontract"
	"core/shared/config"
	"core/shared/serverapi"
)

func TestResolveWorkflowTaskIDUsesDirectShortIDLookup(t *testing.T) {
	remote := &directTaskResolveRemote{}
	taskID, err := resolveWorkflowTaskID(context.Background(), config.App{WorkspaceRoot: t.TempDir()}, remote, "project-1", "BLD-123")
	if err != nil {
		t.Fatalf("resolveWorkflowTaskID: %v", err)
	}
	if taskID != "task-123" {
		t.Fatalf("resolveWorkflowTaskID = %q, want task-123", taskID)
	}
	if len(remote.taskRequests) != 1 || remote.taskRequests[0].ProjectID != "project-1" || remote.taskRequests[0].ShortID != "BLD-123" {
		t.Fatalf("task requests = %+v, want direct project short-id lookup", remote.taskRequests)
	}
	if remote.boardRequests != 0 {
		t.Fatalf("board requests = %d, want none for short-id resolution", remote.boardRequests)
	}
}

type directTaskResolveRemote struct {
	apicontract.WorkflowService
	taskRequests  []serverapi.WorkflowTaskGetRequest
	boardRequests int
}

func (r *directTaskResolveRemote) Close() error { return nil }

func (r *directTaskResolveRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, errors.New("unexpected project path resolution")
}

func (r *directTaskResolveRemote) GetWorkflowTask(_ context.Context, req serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
	r.taskRequests = append(r.taskRequests, req)
	return serverapi.WorkflowTaskGetResponse{Task: serverapi.WorkflowTaskDetail{Summary: serverapi.WorkflowTaskSummary{ID: "task-123", ProjectID: req.ProjectID, ShortID: req.ShortID}}}, nil
}

func (r *directTaskResolveRemote) GetWorkflowBoard(context.Context, serverapi.WorkflowBoardRequest) (serverapi.WorkflowBoardResponse, error) {
	r.boardRequests++
	return serverapi.WorkflowBoardResponse{}, errors.New("unexpected board fetch")
}
