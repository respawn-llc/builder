package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"core/shared/apicontract"
	"core/shared/serverapi"
)

const (
	workflowDeleteTestSelector = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	workflowDeleteTestID       = "workflow-" + workflowDeleteTestSelector
)

type workflowDeleteCommandRemote struct {
	apicontract.WorkflowService

	previewResponse serverapi.WorkflowDeletePreviewResponse
	deleteResponse  serverapi.WorkflowDeleteResponse
	previewRequests []serverapi.WorkflowDeletePreviewRequest
	deleteRequests  []serverapi.WorkflowDeleteRequest
}

func (r *workflowDeleteCommandRemote) PreviewWorkflowDelete(_ context.Context, req serverapi.WorkflowDeletePreviewRequest) (serverapi.WorkflowDeletePreviewResponse, error) {
	r.previewRequests = append(r.previewRequests, req)
	return r.previewResponse, nil
}

func (r *workflowDeleteCommandRemote) DeleteWorkflow(_ context.Context, req serverapi.WorkflowDeleteRequest) (serverapi.WorkflowDeleteResponse, error) {
	r.deleteRequests = append(r.deleteRequests, req)
	return r.deleteResponse, nil
}

func (r *workflowDeleteCommandRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, nil
}

func (r *workflowDeleteCommandRemote) Close() error {
	return nil
}

func TestWorkflowDeleteWithoutConfirmReturnsPreviewOnly(t *testing.T) {
	impact := serverapi.WorkflowDeleteImpact{
		WorkflowID:                     workflowDeleteTestID,
		Version:                        7,
		ProjectCount:                   2,
		LinkCount:                      3,
		DefaultReplacementProjectCount: 1,
		TaskCount:                      5,
		CurrentNodeCount:               1,
		PendingApprovalCount:           2,
		BlockedTaskCount:               3,
	}
	remote := &workflowDeleteCommandRemote{
		previewResponse: serverapi.WorkflowDeletePreviewResponse{Impact: impact},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := workflowSubcommand([]string{"delete", workflowDeleteTestSelector, "--json"}, &stdout, &stderr)

	if exitCode != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%q", exitCode, stderr.String())
	}
	if len(remote.previewRequests) != 1 || remote.previewRequests[0].WorkflowID != workflowDeleteTestID {
		t.Fatalf("preview requests = %+v, want selected workflow", remote.previewRequests)
	}
	if len(remote.deleteRequests) != 0 {
		t.Fatalf("delete requests = %+v, want none without confirmation", remote.deleteRequests)
	}
	var output serverapi.WorkflowDeleteResponse
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode output: %v; output=%q", err, stdout.String())
	}
	expectedOutputImpact := impact
	expectedOutputImpact.WorkflowID = workflowDeleteTestSelector
	if output.Deleted || output.Impact != expectedOutputImpact || len(output.Blockers) != 0 {
		t.Fatalf("output = %+v, want preview impact without deletion", output)
	}
}

func TestWorkflowDeleteConfirmUsesPreviewedImpact(t *testing.T) {
	impact := serverapi.WorkflowDeleteImpact{
		WorkflowID:       workflowDeleteTestID,
		Version:          11,
		ProjectCount:     2,
		LinkCount:        4,
		TaskCount:        6,
		BlockedTaskCount: 0,
	}
	remote := &workflowDeleteCommandRemote{
		previewResponse: serverapi.WorkflowDeletePreviewResponse{Impact: impact},
		deleteResponse:  serverapi.WorkflowDeleteResponse{Deleted: true, Impact: impact},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := workflowSubcommand([]string{"delete", workflowDeleteTestSelector, "--confirm", "--json"}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", exitCode, stderr.String())
	}
	if len(remote.previewRequests) != 1 {
		t.Fatalf("preview requests = %+v, want one", remote.previewRequests)
	}
	wantRequest := serverapi.WorkflowDeleteRequest{
		WorkflowID:           workflowDeleteTestID,
		Confirmed:            true,
		ExpectedVersion:      impact.Version,
		ExpectedProjectCount: impact.ProjectCount,
		ExpectedLinkCount:    impact.LinkCount,
		ExpectedTaskCount:    impact.TaskCount,
	}
	if len(remote.deleteRequests) != 1 || remote.deleteRequests[0] != wantRequest {
		t.Fatalf("delete requests = %+v, want %+v", remote.deleteRequests, wantRequest)
	}
	var output serverapi.WorkflowDeleteResponse
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode output: %v; output=%q", err, stdout.String())
	}
	expectedOutputImpact := impact
	expectedOutputImpact.WorkflowID = workflowDeleteTestSelector
	if !output.Deleted || output.Impact != expectedOutputImpact || len(output.Blockers) != 0 {
		t.Fatalf("output = %+v, want confirmed deletion", output)
	}
}

func TestWorkflowDeleteConfirmReturnsTypedBlockers(t *testing.T) {
	impact := serverapi.WorkflowDeleteImpact{
		WorkflowID:       workflowDeleteTestID,
		Version:          13,
		ProjectCount:     1,
		LinkCount:        1,
		TaskCount:        1,
		CurrentNodeCount: 1,
		BlockedTaskCount: 1,
	}
	blocker := serverapi.WorkflowDeleteBlocker{
		Code:    "current_nodes",
		Message: "test blocker",
		Count:   1,
	}
	remote := &workflowDeleteCommandRemote{
		previewResponse: serverapi.WorkflowDeletePreviewResponse{Impact: impact},
		deleteResponse: serverapi.WorkflowDeleteResponse{
			Impact:   impact,
			Blockers: []serverapi.WorkflowDeleteBlocker{blocker},
		},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := workflowSubcommand([]string{"delete", workflowDeleteTestSelector, "--confirm", "--json"}, &stdout, &stderr)

	if exitCode != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%q", exitCode, stderr.String())
	}
	var output serverapi.WorkflowDeleteResponse
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode output: %v; output=%q", err, stdout.String())
	}
	expectedImpact := impact
	expectedImpact.WorkflowID = workflowDeleteTestSelector
	if output.Deleted || output.Impact != expectedImpact || len(output.Blockers) != 1 || output.Blockers[0] != blocker {
		t.Fatalf("output = %+v, want typed current-node blocker", output)
	}
}

func TestWorkflowDeleteRejectsMismatchedResponseIdentity(t *testing.T) {
	validImpact := serverapi.WorkflowDeleteImpact{
		WorkflowID: workflowDeleteTestID,
		Version:    17,
	}
	mismatchedImpact := validImpact
	mismatchedImpact.WorkflowID = "workflow-bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"

	tests := []struct {
		name               string
		previewImpact      serverapi.WorkflowDeleteImpact
		deleteResponse     serverapi.WorkflowDeleteResponse
		wantDeleteRequests int
	}{
		{
			name:          "preview",
			previewImpact: mismatchedImpact,
		},
		{
			name:          "delete result",
			previewImpact: validImpact,
			deleteResponse: serverapi.WorkflowDeleteResponse{
				Deleted: true,
				Impact:  mismatchedImpact,
			},
			wantDeleteRequests: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			remote := &workflowDeleteCommandRemote{
				previewResponse: serverapi.WorkflowDeletePreviewResponse{Impact: test.previewImpact},
				deleteResponse:  test.deleteResponse,
			}
			installWorkflowCommandRemote(t, remote)

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := workflowSubcommand(
				[]string{"delete", workflowDeleteTestSelector, "--confirm", "--json"},
				&stdout,
				&stderr,
			)

			if exitCode != 1 {
				t.Fatalf("exit code = %d, want 1", exitCode)
			}
			if len(remote.deleteRequests) != test.wantDeleteRequests {
				t.Fatalf("delete requests = %d, want %d", len(remote.deleteRequests), test.wantDeleteRequests)
			}
			if stdout.Len() != 0 {
				t.Fatalf("unexpected successful output: %q", stdout.String())
			}
		})
	}
}

func TestWorkflowDeleteRejectsInconsistentDeletionResult(t *testing.T) {
	impact := serverapi.WorkflowDeleteImpact{
		WorkflowID: workflowDeleteTestID,
		Version:    19,
	}
	blocker := serverapi.WorkflowDeleteBlocker{
		Code:    "active_runs",
		Message: "test blocker",
		Count:   1,
	}
	tests := []serverapi.WorkflowDeleteResponse{
		{Deleted: false, Impact: impact},
		{Deleted: true, Impact: impact, Blockers: []serverapi.WorkflowDeleteBlocker{blocker}},
	}

	for _, response := range tests {
		remote := &workflowDeleteCommandRemote{
			previewResponse: serverapi.WorkflowDeletePreviewResponse{Impact: impact},
			deleteResponse:  response,
		}
		installWorkflowCommandRemote(t, remote)

		var stdout bytes.Buffer
		var stderr bytes.Buffer
		exitCode := workflowSubcommand(
			[]string{"delete", workflowDeleteTestSelector, "--confirm", "--json"},
			&stdout,
			&stderr,
		)

		if exitCode != 1 {
			t.Fatalf("response %+v exit code = %d, want 1", response, exitCode)
		}
		if stdout.Len() != 0 {
			t.Fatalf("response %+v produced successful output %q", response, stdout.String())
		}
	}
}
