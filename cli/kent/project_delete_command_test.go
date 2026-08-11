package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type projectDeleteTestOperations struct {
	listResponse   serverapi.WorkflowTaskListResponse
	listErr        error
	deleteResponse serverapi.ProjectDeleteResponse
	deleteErr      error
	listRequests   []serverapi.WorkflowTaskListRequest
	deleteRequests []serverapi.ProjectDeleteRequest
}

func (r *projectDeleteTestOperations) ListWorkflowTasks(_ context.Context, req serverapi.WorkflowTaskListRequest) (serverapi.WorkflowTaskListResponse, error) {
	r.listRequests = append(r.listRequests, req)
	return r.listResponse, r.listErr
}

func (r *projectDeleteTestOperations) DeleteProject(_ context.Context, req serverapi.ProjectDeleteRequest) (serverapi.ProjectDeleteResponse, error) {
	r.deleteRequests = append(r.deleteRequests, req)
	return r.deleteResponse, r.deleteErr
}

func TestProjectDeleteWithoutConfirmPreflightsAndReturnsJSONError(t *testing.T) {
	const projectID = "project-123"
	remote := &projectDeleteTestOperations{
		listResponse: serverapi.WorkflowTaskListResponse{
			Scope: serverapi.WorkflowTaskListScope{ProjectID: projectID},
			Tasks: []serverapi.WorkflowTaskListItem{},
		},
	}

	outcome := runProjectDeleteUseCase(context.Background(), remote, projectID, false, false)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exitCode := writeProjectDeleteOutcome(&stdout, &stderr, outcome, true); exitCode != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%q", exitCode, stderr.String())
	}
	if len(remote.listRequests) != 1 {
		t.Fatalf("list requests = %d, want 1", len(remote.listRequests))
	}
	if len(remote.deleteRequests) != 0 {
		t.Fatalf("delete requests = %d, want 0", len(remote.deleteRequests))
	}
	var envelope struct {
		Status string `json:"status"`
		Error  struct {
			Code      string `json:"code"`
			ProjectID string `json:"project_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("decode output: %v; output=%q", err, stdout.String())
	}
	if envelope.Status != "error" ||
		envelope.Error.Code != "confirmation_required" ||
		envelope.Error.ProjectID != projectID {
		t.Fatalf("envelope = %+v, want confirmation error for %q", envelope, projectID)
	}
}

func TestProjectDeleteHelpDispatchesExplicitly(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exitCode := projectSubcommand([]string{"delete", "--help"}, &stdout, &stderr); exitCode != 0 {
		t.Fatalf("exit code = %d; stderr=%q", exitCode, stderr.String())
	}
	if stderr.Len() == 0 {
		t.Fatal("help output is empty")
	}
}

func TestProjectDeleteOutcomeRejectsAmbiguousSuccessState(t *testing.T) {
	tests := []projectDeleteOutcome{
		{},
		{
			Result: &projectDeleteResult{ProjectID: "project-123"},
			Error:  &projectDeleteError{Code: "request_failed", Message: "failed", ProjectID: "project-123"},
		},
	}
	for index, outcome := range tests {
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if exitCode := writeProjectDeleteOutcome(&stdout, &stderr, outcome, false); exitCode != 1 {
				t.Fatalf("exit code = %d, want 1", exitCode)
			}
			if stdout.Len() != 0 || stderr.Len() == 0 {
				t.Fatalf("streams = stdout %q stderr %q, want diagnostic-only failure", stdout.String(), stderr.String())
			}
		})
	}
}

func TestProjectDeleteAgentWithBacklogIsDeniedBeforeConfirmation(t *testing.T) {
	const projectID = "project-123"
	remote := &projectDeleteTestOperations{
		listResponse: projectDeleteTaskListResponse(projectID, serverapi.WorkflowTaskStatus{
			Kind:        serverapi.WorkflowTaskStatusKindBacklog,
			NativeState: serverapi.WorkflowTaskNativeStateActive,
		}),
	}

	outcome := runProjectDeleteUseCase(context.Background(), remote, projectID, false, true)

	assertProjectDeleteFailure(t, outcome, "human_only_unfinished_work", projectID)
	if len(remote.deleteRequests) != 0 {
		t.Fatalf("delete requests = %d, want 0", len(remote.deleteRequests))
	}
	assertProjectDeletePreflightRequest(t, remote.listRequests)
}

func TestProjectDeleteHumanWithBacklogContinuesToConfirmation(t *testing.T) {
	const projectID = "project-123"
	remote := &projectDeleteTestOperations{
		listResponse: projectDeleteTaskListResponse(projectID, serverapi.WorkflowTaskStatus{
			Kind:        serverapi.WorkflowTaskStatusKindBacklog,
			NativeState: serverapi.WorkflowTaskNativeStateActive,
		}),
	}

	outcome := runProjectDeleteUseCase(context.Background(), remote, projectID, false, false)

	assertProjectDeleteFailure(t, outcome, "confirmation_required", projectID)
	if len(remote.deleteRequests) != 0 {
		t.Fatalf("delete requests = %d, want 0", len(remote.deleteRequests))
	}
}

func TestProjectDeleteAgentWithoutUnfinishedWorkContinuesToConfirmation(t *testing.T) {
	const projectID = "project-123"
	remote := &projectDeleteTestOperations{
		listResponse: projectDeleteTaskListResponse(projectID),
	}

	outcome := runProjectDeleteUseCase(context.Background(), remote, projectID, false, true)

	assertProjectDeleteFailure(t, outcome, "confirmation_required", projectID)
}

func TestProjectDeleteTreatsCorrectlyScopedNoLinkedWorkflowsAsEmpty(t *testing.T) {
	const projectID = "project-123"
	remote := &projectDeleteTestOperations{
		listErr: &serverapi.WorkflowTaskListScopeError{
			Reason:    serverapi.WorkflowTaskListScopeReasonNoLinkedWorkflows,
			ProjectID: stringPointer(projectID),
		},
	}

	outcome := runProjectDeleteUseCase(context.Background(), remote, projectID, false, true)

	assertProjectDeleteFailure(t, outcome, "confirmation_required", projectID)
}

func TestProjectDeleteRejectsInvalidPreflightScopesAndStatuses(t *testing.T) {
	const projectID = "project-123"
	tests := []struct {
		name     string
		response serverapi.WorkflowTaskListResponse
		err      error
	}{
		{
			name: "mismatched project",
			response: serverapi.WorkflowTaskListResponse{
				Scope: serverapi.WorkflowTaskListScope{ProjectID: "other-project"},
			},
		},
		{
			name: "workflow scope",
			response: serverapi.WorkflowTaskListResponse{
				Scope: serverapi.WorkflowTaskListScope{
					ProjectID:  projectID,
					WorkflowID: workflowIDPointer(t),
				},
			},
		},
		{
			name: "terminal task",
			response: projectDeleteTaskListResponse(projectID, serverapi.WorkflowTaskStatus{
				Kind:        serverapi.WorkflowTaskStatusKindDone,
				NativeState: serverapi.WorkflowTaskNativeStateTerminal,
			}),
		},
		{
			name: "malformed status",
			response: projectDeleteTaskListResponse(projectID, serverapi.WorkflowTaskStatus{
				Kind:        "unknown",
				NativeState: serverapi.WorkflowTaskNativeStateActive,
			}),
		},
		{
			name: "workflow scoped no links",
			err: &serverapi.WorkflowTaskListScopeError{
				Reason:     serverapi.WorkflowTaskListScopeReasonNoLinkedWorkflows,
				ProjectID:  stringPointer(projectID),
				WorkflowID: workflowIDPointer(t),
			},
		},
		{
			name: "transport failure",
			err:  errors.New("preflight failed"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			remote := &projectDeleteTestOperations{listResponse: test.response, listErr: test.err}
			outcome := runProjectDeleteUseCase(context.Background(), remote, projectID, true, false)
			assertProjectDeleteFailure(t, outcome, "request_failed", projectID)
			if len(remote.deleteRequests) != 0 {
				t.Fatalf("delete requests = %d, want 0", len(remote.deleteRequests))
			}
		})
	}
}

func TestProjectDeleteConfirmedSuccessCallsDeleteOnceAndProjectsJSON(t *testing.T) {
	const projectID = "project-123"
	remote := &projectDeleteTestOperations{
		listResponse: projectDeleteTaskListResponse(projectID),
		deleteResponse: serverapi.ProjectDeleteResponse{
			ProjectID: projectID,
			Deleted:   true,
		},
	}

	outcome := runProjectDeleteUseCase(context.Background(), remote, projectID, true, false)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exitCode := writeProjectDeleteOutcome(&stdout, &stderr, outcome, true); exitCode != 0 {
		t.Fatalf("exit code = %d; stderr=%q", exitCode, stderr.String())
	}
	if len(remote.listRequests) != 1 || len(remote.deleteRequests) != 1 {
		t.Fatalf("requests = list %d, delete %d; want one each", len(remote.listRequests), len(remote.deleteRequests))
	}
	if remote.deleteRequests[0].ProjectID != projectID {
		t.Fatalf("delete project id = %q, want %q", remote.deleteRequests[0].ProjectID, projectID)
	}
	var envelope struct {
		Status string `json:"status"`
		Result struct {
			ProjectID string `json:"project_id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("decode output: %v; output=%q", err, stdout.String())
	}
	if envelope.Status != "ok" || envelope.Result.ProjectID != projectID {
		t.Fatalf("envelope = %+v, want successful project result", envelope)
	}
}

func TestProjectDeleteProjectsServerBlockersInOrderWithTypedCounts(t *testing.T) {
	const projectID = "project-123"
	remote := &projectDeleteTestOperations{
		listResponse: projectDeleteTaskListResponse(projectID),
		deleteResponse: serverapi.ProjectDeleteResponse{
			ProjectID: projectID,
			Blockers: []serverapi.ProjectDeleteBlocker{
				{Code: "first", Message: "first blocker"},
				{Code: "second", Message: "second blocker", Count: 3},
			},
		},
	}

	outcome := runProjectDeleteUseCase(context.Background(), remote, projectID, true, false)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exitCode := writeProjectDeleteOutcome(&stdout, &stderr, outcome, true); exitCode != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%q", exitCode, stderr.String())
	}
	var envelope struct {
		Status string `json:"status"`
		Error  struct {
			Code      string `json:"code"`
			ProjectID string `json:"project_id"`
			Blockers  []struct {
				Code    string `json:"code"`
				Message string `json:"message"`
				Count   *int   `json:"count,omitempty"`
			} `json:"blockers"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("decode output: %v; output=%q", err, stdout.String())
	}
	if envelope.Status != "error" || envelope.Error.Code != "project_delete_blocked" ||
		envelope.Error.ProjectID != projectID || len(envelope.Error.Blockers) != 2 {
		t.Fatalf("envelope = %+v, want ordered blockers", envelope)
	}
	if envelope.Error.Blockers[0].Code != "first" || envelope.Error.Blockers[0].Count != nil ||
		envelope.Error.Blockers[1].Code != "second" || envelope.Error.Blockers[1].Count == nil ||
		*envelope.Error.Blockers[1].Count != 3 {
		t.Fatalf("blockers = %+v, want absent zero and positive count", envelope.Error.Blockers)
	}
}

func TestProjectDeletePlainOutputUsesExpectedStreams(t *testing.T) {
	const projectID = "project-123"
	success := runProjectDeleteUseCase(
		context.Background(),
		&projectDeleteTestOperations{
			listResponse:   projectDeleteTaskListResponse(projectID),
			deleteResponse: serverapi.ProjectDeleteResponse{ProjectID: projectID, Deleted: true},
		},
		projectID,
		true,
		false,
	)
	var successStdout bytes.Buffer
	var successStderr bytes.Buffer
	if exitCode := writeProjectDeleteOutcome(&successStdout, &successStderr, success, false); exitCode != 0 {
		t.Fatalf("success exit code = %d; stderr=%q", exitCode, successStderr.String())
	}
	if successStdout.Len() == 0 || successStderr.Len() != 0 {
		t.Fatalf("success streams = stdout %q stderr %q", successStdout.String(), successStderr.String())
	}

	blocked := runProjectDeleteUseCase(
		context.Background(),
		&projectDeleteTestOperations{
			listResponse: projectDeleteTaskListResponse(projectID),
			deleteResponse: serverapi.ProjectDeleteResponse{
				ProjectID: projectID,
				Blockers:  []serverapi.ProjectDeleteBlocker{{Code: "blocked", Message: "blocked", Count: 2}},
			},
		},
		projectID,
		true,
		false,
	)
	var blockedStdout bytes.Buffer
	var blockedStderr bytes.Buffer
	if exitCode := writeProjectDeleteOutcome(&blockedStdout, &blockedStderr, blocked, false); exitCode != 1 {
		t.Fatalf("blocked exit code = %d, want 1", exitCode)
	}
	if blockedStdout.Len() != 0 || blockedStderr.Len() == 0 {
		t.Fatalf("blocked streams = stdout %q stderr %q", blockedStdout.String(), blockedStderr.String())
	}
}

func TestProjectDeletePlainOutputFailureReturnsNonZero(t *testing.T) {
	outcome := projectDeleteOutcome{
		Result: &projectDeleteResult{ProjectID: "project-123"},
	}
	var stderr bytes.Buffer
	if exitCode := writeProjectDeleteOutcome(failingCLIWriter{}, &stderr, outcome, false); exitCode != 1 {
		t.Fatalf("exit code = %d, want 1", exitCode)
	}
	if stderr.Len() == 0 {
		t.Fatal("stderr is empty, want write diagnostic")
	}
}

func TestProjectDeletePlainBlockerRenderingPreservesOptionalCount(t *testing.T) {
	var withoutCount bytes.Buffer
	absent, err := projectDeleteBlockerCount(nil)
	if err != nil {
		t.Fatalf("absent count rejected: %v", err)
	}
	writeWorkflowBlockerLine(&withoutCount, "blocked", "message", absent)

	positive := 2
	var withCount bytes.Buffer
	present, err := projectDeleteBlockerCount(&positive)
	if err != nil {
		t.Fatalf("positive count rejected: %v", err)
	}
	writeWorkflowBlockerLine(&withCount, "blocked", "message", present)

	if withoutCount.Len() == 0 || withCount.Len() <= withoutCount.Len() || bytes.Equal(withoutCount.Bytes(), withCount.Bytes()) {
		t.Fatalf("plain blocker outputs have unexpected count shapes: absent=%q positive=%q", withoutCount.String(), withCount.String())
	}
}

func TestProjectDeletePlainOutputRejectsInvalidPresentBlockerCounts(t *testing.T) {
	const projectID = "project-123"
	for _, nonpositive := range []int{0, -1} {
		outcome := projectDeleteOutcome{
			Error: &projectDeleteError{
				Code:      "project_delete_blocked",
				Message:   "blocked",
				ProjectID: projectID,
				Blockers: []projectDeleteBlocker{{
					Code:    "blocked",
					Message: "message",
					Count:   &nonpositive,
				}},
			},
		}
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if exitCode := writeProjectDeleteOutcome(&stdout, &stderr, outcome, false); exitCode != 1 {
			t.Fatalf("count %d exit code = %d, want 1", nonpositive, exitCode)
		}
		if stdout.Len() != 0 || stderr.Len() == 0 {
			t.Fatalf("count %d streams = stdout %q stderr %q, want diagnostic-only failure", nonpositive, stdout.String(), stderr.String())
		}
	}
}

func TestProjectDeleteRejectsNegativeServerBlockerCount(t *testing.T) {
	const projectID = "project-123"
	remote := &projectDeleteTestOperations{
		listResponse: projectDeleteTaskListResponse(projectID),
		deleteResponse: serverapi.ProjectDeleteResponse{
			ProjectID: projectID,
			Blockers: []serverapi.ProjectDeleteBlocker{
				{Code: "invalid", Message: "invalid blocker", Count: -1},
			},
		},
	}

	outcome := runProjectDeleteUseCase(context.Background(), remote, projectID, true, false)

	assertProjectDeleteFailure(t, outcome, "request_failed", projectID)
}

func TestProjectDeleteMapsNotFoundAndDeleteFailures(t *testing.T) {
	const projectID = "project-123"
	tests := []struct {
		name string
		err  error
		code string
	}{
		{name: "not found", err: serverapi.ErrProjectNotFound, code: "project_not_found"},
		{name: "request failure", err: errors.New("delete failed"), code: "request_failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			remote := &projectDeleteTestOperations{
				listResponse: projectDeleteTaskListResponse(projectID),
				deleteErr:    test.err,
			}
			outcome := runProjectDeleteUseCase(context.Background(), remote, projectID, true, false)
			assertProjectDeleteFailure(t, outcome, test.code, projectID)
			if len(remote.listRequests) != 1 || len(remote.deleteRequests) != 1 {
				t.Fatalf("requests = list %d, delete %d; want one each", len(remote.listRequests), len(remote.deleteRequests))
			}
		})
	}
}

func TestProjectDeleteMapsPreflightNotFound(t *testing.T) {
	const projectID = "project-123"
	remote := &projectDeleteTestOperations{
		listErr: serverapi.ErrProjectNotFound,
	}

	outcome := runProjectDeleteUseCase(context.Background(), remote, projectID, true, false)

	assertProjectDeleteFailure(t, outcome, "project_not_found", projectID)
	if len(remote.deleteRequests) != 0 {
		t.Fatalf("delete requests = %d, want 0", len(remote.deleteRequests))
	}
}

func TestProjectDeleteRejectsMalformedDeleteResponses(t *testing.T) {
	const projectID = "project-123"
	tests := []struct {
		name     string
		response serverapi.ProjectDeleteResponse
	}{
		{
			name: "mismatched identity",
			response: serverapi.ProjectDeleteResponse{
				ProjectID: "other-project",
				Deleted:   true,
			},
		},
		{
			name: "deleted with blockers",
			response: serverapi.ProjectDeleteResponse{
				ProjectID: projectID,
				Deleted:   true,
				Blockers: []serverapi.ProjectDeleteBlocker{
					{Code: "blocker", Message: "blocker"},
				},
			},
		},
		{
			name: "not deleted without blockers",
			response: serverapi.ProjectDeleteResponse{
				ProjectID: projectID,
			},
		},
		{
			name: "blank blocker code",
			response: serverapi.ProjectDeleteResponse{
				ProjectID: projectID,
				Blockers: []serverapi.ProjectDeleteBlocker{
					{Message: "blocker"},
				},
			},
		},
		{
			name: "blank blocker message",
			response: serverapi.ProjectDeleteResponse{
				ProjectID: projectID,
				Blockers: []serverapi.ProjectDeleteBlocker{
					{Code: "blocker"},
				},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			remote := &projectDeleteTestOperations{
				listResponse:   projectDeleteTaskListResponse(projectID),
				deleteResponse: test.response,
			}
			outcome := runProjectDeleteUseCase(context.Background(), remote, projectID, true, false)
			assertProjectDeleteFailure(t, outcome, "request_failed", projectID)
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if exitCode := writeProjectDeleteOutcome(&stdout, &stderr, outcome, true); exitCode != 1 {
				t.Fatalf("exit code = %d, want 1", exitCode)
			}
			var envelope struct {
				Status string `json:"status"`
				Error  struct {
					Code      string `json:"code"`
					ProjectID string `json:"project_id"`
					Blockers  []any  `json:"blockers"`
				} `json:"error"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
				t.Fatalf("decode output: %v; output=%q", err, stdout.String())
			}
			if envelope.Error.ProjectID != projectID || envelope.Error.Code != "request_failed" || envelope.Error.Blockers != nil {
				t.Fatalf("envelope = %+v, want request_failed with project id and no blockers", envelope)
			}
		})
	}
}

func TestProjectDeleteOpenerFailureUsesOperationalJSONEnvelope(t *testing.T) {
	persistenceRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(persistenceRoot, "config.toml"), []byte("server_host = \"127.0.0.1\"\nserver_port = 1\n"), 0o600); err != nil {
		t.Fatalf("write isolated config: %v", err)
	}
	t.Setenv(config.PersistenceRootEnvName, persistenceRoot)
	t.Chdir(workspaceRoot)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exitCode := projectDeleteSubcommand([]string{"project-123", "--json"}, &stdout, &stderr); exitCode != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	var envelope struct {
		Status string `json:"status"`
		Error  struct {
			Code      string `json:"code"`
			ProjectID string `json:"project_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("decode output: %v; output=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if envelope.Status != "error" || envelope.Error.Code != "request_failed" || envelope.Error.ProjectID != "project-123" {
		t.Fatalf("envelope = %+v, want request_failed for requested project", envelope)
	}
}

func projectDeleteTaskListResponse(projectID string, statuses ...serverapi.WorkflowTaskStatus) serverapi.WorkflowTaskListResponse {
	response := serverapi.WorkflowTaskListResponse{
		Scope: serverapi.WorkflowTaskListScope{ProjectID: projectID},
	}
	for _, status := range statuses {
		response.Tasks = append(response.Tasks, serverapi.WorkflowTaskListItem{Status: status})
	}
	return response
}

func assertProjectDeleteFailure(t *testing.T, outcome projectDeleteOutcome, code string, projectID string) {
	t.Helper()
	if outcome.Result != nil || outcome.Error == nil {
		t.Fatalf("outcome = %+v, want failure", outcome)
	}
	if outcome.Error.Code != code || outcome.Error.ProjectID != projectID {
		t.Fatalf("error = %+v, want code %q and project %q", outcome.Error, code, projectID)
	}
}

func assertProjectDeletePreflightRequest(t *testing.T, requests []serverapi.WorkflowTaskListRequest) {
	t.Helper()
	if len(requests) != 1 {
		t.Fatalf("preflight requests = %d, want 1", len(requests))
	}
	request := requests[0]
	if request.ProjectID == nil || *request.ProjectID != "project-123" {
		t.Fatalf("project id = %v, want project-123", request.ProjectID)
	}
	limit := 1
	if request.Limit == nil || *request.Limit != limit {
		t.Fatalf("limit = %v, want %d", request.Limit, limit)
	}
	if request.WorkflowID != nil || len(request.ColumnKeys) != 0 || len(request.AttentionKinds) != 0 ||
		len(request.Sort) != 0 || request.Offset != nil ||
		request.LabelFilter.Kind != serverapi.WorkflowTaskLabelFilterKindNone ||
		request.LabelFilter.Named != nil {
		t.Fatalf("request filters = %+v, want project-wide unfiltered existence query", request)
	}
	if !slices.Equal(request.StatusKinds, projectDeleteUnfinishedTaskStatuses()) {
		t.Fatalf("status kinds = %v, want %v", request.StatusKinds, projectDeleteUnfinishedTaskStatuses())
	}
}

func stringPointer(value string) *string {
	return &value
}

func workflowIDPointer(t *testing.T) *runtimeids.WorkflowID {
	t.Helper()
	value := runtimeids.NewWorkflowID()
	return &value
}
