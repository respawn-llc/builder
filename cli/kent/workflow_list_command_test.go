package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"core/shared/apicontract"
	"core/shared/serverapi"
)

type workflowListCommandRemote struct {
	apicontract.WorkflowService

	response serverapi.WorkflowListResponse
	requests []serverapi.WorkflowListRequest
}

func (r *workflowListCommandRemote) ListWorkflows(_ context.Context, req serverapi.WorkflowListRequest) (serverapi.WorkflowListResponse, error) {
	r.requests = append(r.requests, req)
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowListResponse{}, err
	}
	return r.response, nil
}

func (r *workflowListCommandRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, nil
}

func (r *workflowListCommandRemote) Close() error {
	return nil
}

func TestWorkflowListUsesNumericOffsetsAndWritesStructuredContinuation(t *testing.T) {
	nextOffset := 2
	remote := &workflowListCommandRemote{
		response: serverapi.WorkflowListResponse{
			Workflows: []serverapi.WorkflowRecord{{
				ID:   workflowDeleteTestID,
				Name: "Delivery",
			}},
			NextOffset: &nextOffset,
		},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := workflowSubcommand([]string{"list", "--offset", "0", "--limit", "2", "--json"}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("exit code = %d; stderr=%q", exitCode, stderr.String())
	}
	if len(remote.requests) != 1 || remote.requests[0].Offset == nil || *remote.requests[0].Offset != 0 || remote.requests[0].Limit == nil || *remote.requests[0].Limit != 2 {
		t.Fatalf("requests = %+v", remote.requests)
	}
	var output struct {
		NextOffset *int `json:"next_offset"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode JSON output: %v", err)
	}
	if output.NextOffset == nil || *output.NextOffset != nextOffset {
		t.Fatalf("JSON output = %+v", output)
	}
	if stderr.Len() == 0 {
		t.Fatal("continuation was not written to stderr")
	}
}

func TestWorkflowListReturnsFailureForInvalidOffsetWindow(t *testing.T) {
	for _, args := range [][]string{
		{"list", "--offset", "-1"},
		{"list", "--limit", "0"},
		{"list", "--limit", "101"},
	} {
		t.Run(args[1], func(t *testing.T) {
			remote := &workflowListCommandRemote{}
			installWorkflowCommandRemote(t, remote)

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := workflowSubcommand(args, &stdout, &stderr)

			if exitCode != 1 {
				t.Fatalf("exit code = %d; stderr=%q", exitCode, stderr.String())
			}
			if stdout.Len() != 0 || stderr.Len() == 0 {
				t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestWorkflowListRejectsRemovedPaginationFlags(t *testing.T) {
	for _, args := range [][]string{
		{"list", "--page-token", "legacy"},
		{"list", "--page-size", "1"},
	} {
		t.Run(args[1], func(t *testing.T) {
			remote := &workflowListCommandRemote{}
			installWorkflowCommandRemote(t, remote)

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := workflowSubcommand(args, &stdout, &stderr)

			if exitCode != 2 {
				t.Fatalf("exit code = %d; stderr=%q", exitCode, stderr.String())
			}
			if len(remote.requests) != 0 || stdout.Len() != 0 || stderr.Len() == 0 {
				t.Fatalf("requests=%+v stdout=%q stderr=%q", remote.requests, stdout.String(), stderr.String())
			}
		})
	}
}
