package main

import (
	"bytes"
	"context"
	"testing"

	"core/shared/apicontract"
	"core/shared/serverapi"
)

const taskCommentListCommandTestTaskID = "task-1"

type taskCommentListCommandRemote struct {
	apicontract.WorkflowService

	listRequests []serverapi.WorkflowTaskCommentListRequest
	responses    map[int]serverapi.WorkflowTaskCommentListResponse
}

func (r *taskCommentListCommandRemote) GetWorkflowTask(_ context.Context, req serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
	return serverapi.WorkflowTaskGetResponse{
		Task: serverapi.WorkflowTaskDetail{
			Summary: serverapi.WorkflowTaskSummary{ID: req.TaskID, ShortID: "KENT-1"},
		},
	}, nil
}

func (r *taskCommentListCommandRemote) ListWorkflowTaskComments(_ context.Context, req serverapi.WorkflowTaskCommentListRequest) (serverapi.WorkflowTaskCommentListResponse, error) {
	r.listRequests = append(r.listRequests, req)
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskCommentListResponse{}, err
	}
	return r.responses[*req.Offset], nil
}

func (r *taskCommentListCommandRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, nil
}

func (r *taskCommentListCommandRemote) Close() error {
	return nil
}

func TestTaskCommentListUsesOffsetWindowsAndRoutesContinuationToStderr(t *testing.T) {
	nextOffset := 2
	remote := &taskCommentListCommandRemote{
		responses: map[int]serverapi.WorkflowTaskCommentListResponse{
			0: {
				Comments:   []serverapi.WorkflowTaskComment{{ID: "comment-1", TaskID: taskCommentListCommandTestTaskID, Author: "user", Body: "first", CreatedAtUnixMs: 1}},
				NextOffset: &nextOffset,
			},
			2: {
				Comments: []serverapi.WorkflowTaskComment{{ID: "comment-3", TaskID: taskCommentListCommandTestTaskID, Author: "user", Body: "continued", CreatedAtUnixMs: 3}},
			},
			3: {},
		},
	}
	installWorkflowCommandRemote(t, remote)

	for _, tt := range []struct {
		name       string
		args       []string
		wantOffset int
		wantLimit  int
		wantStdout bool
		wantStderr bool
	}{
		{name: "first page", args: []string{"list", taskCommentListCommandTestTaskID, "--offset", "0", "--limit", "2"}, wantOffset: 0, wantLimit: 2, wantStdout: true, wantStderr: true},
		{name: "continued page", args: []string{"list", taskCommentListCommandTestTaskID, "--offset", "2", "--limit", "2"}, wantOffset: 2, wantLimit: 2, wantStdout: true},
		{name: "beyond end", args: []string{"list", taskCommentListCommandTestTaskID, "--offset", "3", "--limit", "2"}, wantOffset: 3, wantLimit: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := taskCommentSubcommand(tt.args, &stdout, &stderr)

			if exitCode != 0 {
				t.Fatalf("exit code = %d; stderr=%q", exitCode, stderr.String())
			}
			request := remote.listRequests[len(remote.listRequests)-1]
			if request.Offset == nil || *request.Offset != tt.wantOffset || request.Limit == nil || *request.Limit != tt.wantLimit {
				t.Fatalf("list request = %+v", request)
			}
			if (stdout.Len() > 0) != tt.wantStdout || (stderr.Len() > 0) != tt.wantStderr {
				t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestTaskCommentListRejectsInvalidOffsetWindows(t *testing.T) {
	for _, args := range [][]string{
		{"list", taskCommentListCommandTestTaskID, "--offset", "-1"},
		{"list", taskCommentListCommandTestTaskID, "--limit", "0"},
		{"list", taskCommentListCommandTestTaskID, "--limit", "101"},
	} {
		t.Run(args[len(args)-2], func(t *testing.T) {
			remote := &taskCommentListCommandRemote{}
			installWorkflowCommandRemote(t, remote)

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := taskCommentSubcommand(args, &stdout, &stderr)

			if exitCode != 1 {
				t.Fatalf("exit code = %d; stderr=%q", exitCode, stderr.String())
			}
			if stdout.Len() != 0 || stderr.Len() == 0 || len(remote.listRequests) != 1 {
				t.Fatalf("stdout=%q stderr=%q requests=%+v", stdout.String(), stderr.String(), remote.listRequests)
			}
		})
	}
}

func TestTaskCommentListRejectsRemovedPaginationFlags(t *testing.T) {
	for _, args := range [][]string{
		{"list", taskCommentListCommandTestTaskID, "--page-token", "legacy"},
		{"list", taskCommentListCommandTestTaskID, "--page-size", "1"},
	} {
		t.Run(args[len(args)-2], func(t *testing.T) {
			remote := &taskCommentListCommandRemote{}
			installWorkflowCommandRemote(t, remote)

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := taskCommentSubcommand(args, &stdout, &stderr)

			if exitCode != 2 {
				t.Fatalf("exit code = %d; stderr=%q", exitCode, stderr.String())
			}
			if len(remote.listRequests) != 0 || stdout.Len() != 0 || stderr.Len() == 0 {
				t.Fatalf("requests=%+v stdout=%q stderr=%q", remote.listRequests, stdout.String(), stderr.String())
			}
		})
	}
}
