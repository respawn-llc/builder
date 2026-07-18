package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/apicontract"
	"core/shared/config"
	"core/shared/serverapi"
)

func stringSlicePointerForTest(values ...string) *[]string {
	return &values
}

func stringPointerForTaskListTest(value string) *string {
	return &value
}

const taskListWorkflowSelector = "7e8d24d2-8a98-4dcf-a197-6214db1cb3c0"
const taskListWorkflowID = "workflow-" + taskListWorkflowSelector
const taskListSecondWorkflowSelector = "8e8d24d2-8a98-4dcf-a197-6214db1cb3c0"
const taskListSecondWorkflowID = "workflow-" + taskListSecondWorkflowSelector

func TestTaskListSendsTypedFiltersAndSorts(t *testing.T) {
	remote := &capturingTaskListRemote{response: serverapi.WorkflowTaskListResponse{
		Scope:                       taskListResponseScope("project-1", stringPointerForTaskListTest(taskListWorkflowID)),
		MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityNone,
	}}
	restore := replaceWorkflowCommandRemoteOpener(t, config.App{WorkspaceRoot: t.TempDir()}, remote)
	defer restore()

	_, stderr, code := runWorkflowRootCommand(
		"task", "list",
		"--project", "project-1",
		"--workflow", taskListWorkflowSelector,
		"--status", "queued,running",
		"--attention", "approval",
		"--column", "plan",
		"--sort", "status:asc",
		"--sort", "column:desc",
	)
	if code != 0 {
		t.Fatalf("task list exit=%d stderr=%q", code, stderr)
	}
	if len(remote.requests) != 1 {
		t.Fatalf("requests = %+v, want one request", remote.requests)
	}
	request := remote.requests[0]
	if request.ProjectID == nil || *request.ProjectID != "project-1" || request.WorkflowID == nil || *request.WorkflowID != taskListWorkflowID {
		t.Fatalf("scope = %+v, want exact pair", request)
	}
	if !reflect.DeepEqual(request.StatusKinds, []serverapi.WorkflowTaskStatusKind{
		serverapi.WorkflowTaskStatusKindQueued,
		serverapi.WorkflowTaskStatusKindRunning,
	}) {
		t.Fatalf("status kinds = %+v", request.StatusKinds)
	}
	if !reflect.DeepEqual(request.AttentionKinds, []serverapi.WorkflowTaskAttentionKind{
		serverapi.WorkflowTaskAttentionKindApproval,
	}) {
		t.Fatalf("attention kinds = %+v", request.AttentionKinds)
	}
	if !reflect.DeepEqual(request.ColumnKeys, []string{"plan"}) {
		t.Fatalf("column keys = %+v", request.ColumnKeys)
	}
	if !reflect.DeepEqual(request.Sort, []serverapi.WorkflowTaskListSort{
		{Field: serverapi.WorkflowTaskListSortFieldStatus, Direction: serverapi.WorkflowTaskListSortDirectionAsc},
		{Field: serverapi.WorkflowTaskListSortFieldColumn, Direction: serverapi.WorkflowTaskListSortDirectionDesc},
	}) {
		t.Fatalf("sort = %+v", request.Sort)
	}
}

func TestTaskListLeavesDefaultSortToServer(t *testing.T) {
	remote := &capturingTaskListRemote{response: serverapi.WorkflowTaskListResponse{
		Scope:                       taskListResponseScope("project-1", nil),
		MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityNone,
	}}
	restore := replaceWorkflowCommandRemoteOpener(t, config.App{WorkspaceRoot: t.TempDir()}, remote)
	defer restore()

	_, stderr, code := runWorkflowRootCommand("task", "list", "--project", "project-1")
	if code != 0 {
		t.Fatalf("task list exit=%d stderr=%q", code, stderr)
	}
	if len(remote.requests) != 1 || remote.requests[0].Sort != nil {
		t.Fatalf("requests = %+v, want nil sort for server defaulting", remote.requests)
	}
}

func TestTaskListWorkflowOnlyResolvesCurrentProject(t *testing.T) {
	workspaceRoot := t.TempDir()
	remote := &capturingTaskListRemote{response: serverapi.WorkflowTaskListResponse{
		Scope:                       taskListResponseScope("project-current", stringPointerForTaskListTest(taskListWorkflowID)),
		MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityNone,
	}}
	remote.resolvedProjectID = stringPointerForTaskListTest("project-current")
	restore := replaceWorkflowCommandRemoteOpener(t, config.App{WorkspaceRoot: workspaceRoot}, remote)
	defer restore()

	_, stderr, code := runWorkflowRootCommand("task", "list", "--workflow", taskListWorkflowSelector)
	if code != 0 {
		t.Fatalf("task list exit=%d stderr=%q", code, stderr)
	}
	if len(remote.resolveRequests) != 1 || remote.resolveRequests[0].Path != workspaceRoot {
		t.Fatalf("project resolutions = %+v, want current workspace", remote.resolveRequests)
	}
	if len(remote.requests) != 1 || remote.requests[0].ProjectID == nil || *remote.requests[0].ProjectID != "project-current" || remote.requests[0].WorkflowID == nil || *remote.requests[0].WorkflowID != taskListWorkflowID {
		t.Fatalf("requests = %+v, want current project narrowed by workflow", remote.requests)
	}
}

func TestTaskListTokenContinuationResolvesCurrentProject(t *testing.T) {
	workspaceRoot := t.TempDir()
	remote := &capturingTaskListRemote{response: serverapi.WorkflowTaskListResponse{
		Scope:                       taskListResponseScope("project-current", nil),
		MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne,
	}}
	remote.resolvedProjectID = stringPointerForTaskListTest("project-current")
	restore := replaceWorkflowCommandRemoteOpener(t, config.App{WorkspaceRoot: workspaceRoot}, remote)
	defer restore()

	_, stderr, code := runWorkflowRootCommand("task", "list", "--page-token", "opaque-token")
	if code != 0 {
		t.Fatalf("task list exit=%d stderr=%q", code, stderr)
	}
	if len(remote.resolveRequests) != 1 || remote.resolveRequests[0].Path != workspaceRoot {
		t.Fatalf("project resolutions = %+v, want current workspace", remote.resolveRequests)
	}
	if len(remote.requests) != 1 || remote.requests[0].ProjectID == nil || *remote.requests[0].ProjectID != "project-current" || remote.requests[0].WorkflowID != nil || remote.requests[0].PageToken != "opaque-token" {
		t.Fatalf("requests = %+v, want current project with token continuation", remote.requests)
	}
}

func TestTaskListTokenContinuationAcceptsServerRestoredWorkflowScope(t *testing.T) {
	workspaceRoot := t.TempDir()
	remote := &capturingTaskListRemote{response: serverapi.WorkflowTaskListResponse{
		Scope:                       taskListResponseScope("project-current", stringPointerForTaskListTest(taskListWorkflowID)),
		MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityNone,
	}}
	remote.resolvedProjectID = stringPointerForTaskListTest("project-current")
	restore := replaceWorkflowCommandRemoteOpener(t, config.App{WorkspaceRoot: workspaceRoot}, remote)
	defer restore()

	stdout, stderr, code := runWorkflowRootCommand("task", "list", "--page-token", "narrowed-token", "--json")
	if code != 0 {
		t.Fatalf("task list exit=%d stderr=%q", code, stderr)
	}
	if len(remote.requests) != 1 || remote.requests[0].WorkflowID != nil || remote.requests[0].PageToken != "narrowed-token" {
		t.Fatalf("requests = %+v, want token-owned workflow scope", remote.requests)
	}
	var output taskListOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("task list JSON = %q: %v", stdout, err)
	}
	if output.WorkflowID == nil || *output.WorkflowID != taskListWorkflowSelector {
		t.Fatalf("task list workflow = %v, want restored bare selector %q", output.WorkflowID, taskListWorkflowSelector)
	}
}

func TestTaskListRejectsUnknownResponseStatus(t *testing.T) {
	remote := &capturingTaskListRemote{response: serverapi.WorkflowTaskListResponse{
		Scope:                       taskListResponseScope("project-1", stringPointerForTaskListTest(taskListWorkflowID)),
		MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne,
		Tasks: []serverapi.WorkflowTaskListItem{{
			TaskID:     "task-1",
			WorkflowID: taskListWorkflowID,
			Status:     serverapi.WorkflowTaskStatus{Kind: "future_status"},
		}},
	}}
	restore := replaceWorkflowCommandRemoteOpener(t, config.App{WorkspaceRoot: t.TempDir()}, remote)
	defer restore()

	stdout, stderr, code := runWorkflowRootCommand("task", "list", "--project", "project-1")
	if code != 1 || stdout != "" {
		t.Fatalf("task list exit=%d stdout=%q stderr=%q, want unsupported-status failure without output", code, stdout, stderr)
	}
}

func TestTaskListRejectsInvalidFlagsBeforeOpeningRemote(t *testing.T) {
	called := false
	original := workflowCommandRemoteOpener
	workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
		called = true
		return config.App{}, nil, nil
	}
	defer func() { workflowCommandRemoteOpener = original }()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := taskListSubcommand([]string{"--run-status", "running"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("task list code=%d stderr=%q, want usage failure", code, stderr.String())
	}
	if called {
		t.Fatal("remote opener was called for invalid flags")
	}
}

func taskListResponseScope(projectID string, workflowID *string) serverapi.WorkflowTaskListScope {
	return serverapi.WorkflowTaskListScope{ProjectID: projectID, WorkflowID: workflowID}
}

func taskListExpectedScopeForTest(projectID string, workflowID *string) taskListExpectedScope {
	return taskListExpectedScope{ProjectID: projectID, WorkflowID: workflowID}
}

func TestTaskListRejectsBlankWorkflowBeforeOpeningRemote(t *testing.T) {
	called := false
	original := workflowCommandRemoteOpener
	workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
		called = true
		return config.App{}, nil, nil
	}
	defer func() { workflowCommandRemoteOpener = original }()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := taskListSubcommand([]string{"--workflow", " "}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("task list code=%d stderr=%q, want usage failure", code, stderr.String())
	}
	if called {
		t.Fatal("remote opener was called for a blank workflow selector")
	}
}

func TestTaskListWorkflowSelectorRequiresV4UUID(t *testing.T) {
	value, err := parseWorkflowSelector(taskListWorkflowSelector)
	if err != nil || value.PersistedID() != taskListWorkflowID {
		t.Fatalf("parseWorkflowSelector valid selector = %v, %v", value, err)
	}
	for _, invalid := range []string{"", " ", "7e8d24d2-8a98-1dcf-a197-6214db1cb3c0", "not-a-uuid", taskListWorkflowID} {
		if _, err := parseWorkflowSelector(invalid); err == nil {
			t.Fatalf("parseWorkflowSelector(%q) succeeded", invalid)
		}
	}
}

func TestTaskListParsesTypedStatusFilters(t *testing.T) {
	statuses, err := parseTaskListStatusKinds([]string{"queued,running"})
	if err != nil {
		t.Fatalf("parseTaskListStatusKinds: %v", err)
	}
	if len(statuses) != 2 || statuses[0] != serverapi.WorkflowTaskStatusKindQueued || statuses[1] != serverapi.WorkflowTaskStatusKindRunning {
		t.Fatalf("statuses = %+v", statuses)
	}
	attention, err := parseTaskListAttentionKinds([]string{"approval"})
	if err != nil || len(attention) != 1 || attention[0] != serverapi.WorkflowTaskAttentionKindApproval {
		t.Fatalf("attention = %+v, err=%v", attention, err)
	}
}

type capturingTaskListRemote struct {
	apicontract.WorkflowService
	requests          []serverapi.WorkflowTaskListRequest
	resolveRequests   []serverapi.ProjectResolvePathRequest
	resolvedProjectID *string
	response          serverapi.WorkflowTaskListResponse
	err               error
}

func (r *capturingTaskListRemote) Close() error { return nil }

func (r *capturingTaskListRemote) ResolveProjectPath(_ context.Context, request serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	r.resolveRequests = append(r.resolveRequests, request)
	if r.resolvedProjectID == nil {
		return serverapi.ProjectResolvePathResponse{}, errors.New("unexpected project path resolution")
	}
	return serverapi.ProjectResolvePathResponse{
		Binding: &serverapi.ProjectBinding{
			ProjectID:     *r.resolvedProjectID,
			CanonicalRoot: request.Path,
		},
	}, nil
}

func (r *capturingTaskListRemote) ListWorkflowTasks(_ context.Context, request serverapi.WorkflowTaskListRequest) (serverapi.WorkflowTaskListResponse, error) {
	r.requests = append(r.requests, request)
	if r.err != nil {
		return serverapi.WorkflowTaskListResponse{}, r.err
	}
	return r.response, nil
}

func TestTaskListJSONUsesTypedStatusObject(t *testing.T) {
	remote := &capturingTaskListRemote{response: serverapi.WorkflowTaskListResponse{
		Scope:                       taskListResponseScope("project-1", stringPointerForTaskListTest(taskListWorkflowID)),
		MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne,
		Tasks: []serverapi.WorkflowTaskListItem{{
			TaskID:     "task-1",
			WorkflowID: taskListWorkflowID,
			Status: serverapi.WorkflowTaskStatus{
				Kind:        serverapi.WorkflowTaskStatusKindQueued,
				NativeState: "queued",
			},
			ColumnKeys: stringSlicePointerForTest("plan"),
		}},
	}}
	restore := replaceWorkflowCommandRemoteOpener(t, config.App{WorkspaceRoot: t.TempDir()}, remote)
	defer restore()

	stdout, stderr, code := runWorkflowRootCommand("task", "list", "--project", "project-1", "--workflow", taskListWorkflowSelector, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("task list exit=%d stderr=%q", code, stderr)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("list json = %q: %v", stdout, err)
	}
	if _, exists := payload["run_status"]; exists {
		t.Fatalf("list json = %q, must not contain run_status", stdout)
	}
}

func TestTaskListProjectionUsesFrozenCardinalityAndNormalizesWorkflowIDs(t *testing.T) {
	projectWide, err := taskListProjectionFromResponse(serverapi.WorkflowTaskListResponse{
		Scope:                       taskListResponseScope("project-1", nil),
		MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityMultiple,
		Tasks: []serverapi.WorkflowTaskListItem{
			{
				TaskID:       "task-1",
				ShortID:      "KENT-1",
				WorkflowID:   taskListWorkflowID,
				WorkflowName: stringPointerForTaskListTest("First"),
				Title:        "First task",
				Status:       serverapi.WorkflowTaskStatus{Kind: serverapi.WorkflowTaskStatusKindQueued, NativeState: "queued"},
			},
			{
				TaskID:       "task-2",
				ShortID:      "KENT-2",
				WorkflowID:   taskListSecondWorkflowID,
				WorkflowName: stringPointerForTaskListTest("Second"),
				Title:        "Second task",
				Status:       serverapi.WorkflowTaskStatus{Kind: serverapi.WorkflowTaskStatusKindQueued, NativeState: "queued"},
			},
		},
	}, taskListExpectedScopeForTest("project-1", nil))
	if err != nil {
		t.Fatalf("taskListProjectionFromResponse project-wide: %v", err)
	}
	if projectWide.Output.WorkflowID != nil ||
		projectWide.Output.MatchingWorkflowCardinality != serverapi.WorkflowTaskListMatchingWorkflowCardinalityMultiple ||
		len(projectWide.Rows) != 2 {
		t.Fatalf("project-wide projection = %+v", projectWide)
	}
	for index, workflowID := range []string{taskListWorkflowSelector, taskListSecondWorkflowSelector} {
		if projectWide.Output.Tasks[index].WorkflowID != workflowID ||
			projectWide.Output.Tasks[index].ColumnKeys != nil ||
			!projectWide.Rows[index].ShowWorkflow ||
			projectWide.Rows[index].ShowColumns ||
			projectWide.Rows[index].WorkflowName == "" {
			t.Fatalf("project-wide row %d = %+v/%+v", index, projectWide.Output.Tasks[index], projectWide.Rows[index])
		}
	}

	narrowed, err := taskListProjectionFromResponse(serverapi.WorkflowTaskListResponse{
		Scope:                       taskListResponseScope("project-1", stringPointerForTaskListTest(taskListWorkflowID)),
		MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne,
		Tasks: []serverapi.WorkflowTaskListItem{{
			TaskID:     "task-1",
			ShortID:    "KENT-1",
			WorkflowID: taskListWorkflowID,
			ColumnKeys: stringSlicePointerForTest("plan"),
			Title:      "First task",
			Status:     serverapi.WorkflowTaskStatus{Kind: serverapi.WorkflowTaskStatusKindQueued, NativeState: "queued"},
		}},
	}, taskListExpectedScopeForTest("project-1", stringPointerForTaskListTest(taskListWorkflowID)))
	if err != nil {
		t.Fatalf("taskListProjectionFromResponse narrowed: %v", err)
	}
	if narrowed.Output.WorkflowID == nil || *narrowed.Output.WorkflowID != taskListWorkflowSelector ||
		len(narrowed.Rows) != 1 ||
		narrowed.Rows[0].ShowWorkflow ||
		!narrowed.Rows[0].ShowColumns ||
		narrowed.Output.Tasks[0].ColumnKeys == nil ||
		!reflect.DeepEqual(*narrowed.Output.Tasks[0].ColumnKeys, []string{"plan"}) {
		t.Fatalf("narrowed projection = %+v", narrowed)
	}
}

func TestTaskListProjectWideJSONOmitsWorkflowAndColumnSentinels(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := writeTaskListResponse(&stdout, &stderr, serverapi.WorkflowTaskListResponse{
		Scope:                       taskListResponseScope("project-1", nil),
		MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityMultiple,
		Tasks: []serverapi.WorkflowTaskListItem{{
			TaskID:       "task-1",
			ShortID:      "KENT-1",
			WorkflowID:   taskListWorkflowID,
			WorkflowName: stringPointerForTaskListTest("First"),
			Title:        "Task",
			Status:       serverapi.WorkflowTaskStatus{Kind: serverapi.WorkflowTaskStatusKindQueued, NativeState: "queued"},
		}},
	}, taskListExpectedScopeForTest("project-1", nil), true)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("writeTaskListResponse exit=%d stderr=%q", code, stderr.String())
	}
	var payload struct {
		WorkflowID *json.RawMessage             `json:"workflow_id"`
		Tasks      []map[string]json.RawMessage `json:"tasks"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("task list JSON = %q: %v", stdout.String(), err)
	}
	if payload.WorkflowID != nil {
		t.Fatalf("task list JSON = %q, want omitted workflow_id", stdout.String())
	}
	if len(payload.Tasks) != 1 {
		t.Fatalf("task list JSON tasks = %+v", payload.Tasks)
	}
	for _, omitted := range []string{"column_keys", "workflow_name"} {
		if _, exists := payload.Tasks[0][omitted]; exists {
			t.Fatalf("task list JSON = %q, want omitted %s", stdout.String(), omitted)
		}
	}
	var workflowID string
	if err := json.Unmarshal(payload.Tasks[0]["workflow_id"], &workflowID); err != nil || workflowID != taskListWorkflowSelector {
		t.Fatalf("task workflow_id = %q/%v, want bare selector", workflowID, err)
	}
}

func TestTaskListProjectionRejectsMalformedOrImpossibleWorkflowScope(t *testing.T) {
	for name, response := range map[string]serverapi.WorkflowTaskListResponse{
		"malformed selected workflow": {
			Scope:                       taskListResponseScope("project-1", stringPointerForTaskListTest("workflow-not-a-uuid")),
			MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityNone,
		},
		"malformed task workflow": {
			Scope:                       taskListResponseScope("project-1", nil),
			MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne,
			Tasks:                       []serverapi.WorkflowTaskListItem{{TaskID: "task-1", WorkflowID: "workflow-not-a-uuid"}},
		},
		"project-wide columns": {
			Scope:                       taskListResponseScope("project-1", nil),
			MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne,
			Tasks:                       []serverapi.WorkflowTaskListItem{{TaskID: "task-1", WorkflowID: taskListWorkflowID, ColumnKeys: stringSlicePointerForTest("plan")}},
		},
		"narrowed multiple cardinality": {
			Scope:                       taskListResponseScope("project-1", stringPointerForTaskListTest(taskListWorkflowID)),
			MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityMultiple,
		},
	} {
		t.Run(name, func(t *testing.T) {
			expectedScope := taskListExpectedScopeForTest(response.Scope.ProjectID, nil)
			if response.Scope.WorkflowID != nil {
				expectedScope.WorkflowID = response.Scope.WorkflowID
			}
			if _, err := taskListProjectionFromResponse(response, expectedScope); err == nil {
				t.Fatalf("taskListProjectionFromResponse(%s) succeeded", name)
			}
		})
	}
}

func TestTaskListProjectionRejectsResponseScopeMismatch(t *testing.T) {
	for name, testCase := range map[string]struct {
		response serverapi.WorkflowTaskListResponse
		expected taskListExpectedScope
	}{
		"project mismatch": {
			response: serverapi.WorkflowTaskListResponse{Scope: taskListResponseScope("project-other", nil)},
			expected: taskListExpectedScopeForTest("project-1", nil),
		},
		"unexpected narrowed workflow": {
			response: serverapi.WorkflowTaskListResponse{Scope: taskListResponseScope("project-1", stringPointerForTaskListTest(taskListWorkflowID))},
			expected: taskListExpectedScopeForTest("project-1", nil),
		},
		"missing narrowed workflow": {
			response: serverapi.WorkflowTaskListResponse{Scope: taskListResponseScope("project-1", nil)},
			expected: taskListExpectedScopeForTest("project-1", stringPointerForTaskListTest(taskListWorkflowID)),
		},
		"workflow mismatch": {
			response: serverapi.WorkflowTaskListResponse{Scope: taskListResponseScope("project-1", stringPointerForTaskListTest(taskListSecondWorkflowID))},
			expected: taskListExpectedScopeForTest("project-1", stringPointerForTaskListTest(taskListWorkflowID)),
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := taskListProjectionFromResponse(testCase.response, testCase.expected); err == nil {
				t.Fatalf("taskListProjectionFromResponse(%s) accepted mismatched response scope", name)
			}
		})
	}
}

func TestTaskListProjectionAcceptsTokenOwnedWorkflowScope(t *testing.T) {
	for name, response := range map[string]serverapi.WorkflowTaskListResponse{
		"project wide": {
			Scope:                       taskListResponseScope("project-1", nil),
			MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityNone,
		},
		"narrowed": {
			Scope:                       taskListResponseScope("project-1", stringPointerForTaskListTest(taskListWorkflowID)),
			MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityNone,
		},
	} {
		t.Run(name, func(t *testing.T) {
			projection, err := taskListProjectionFromResponse(response, taskListExpectedScope{
				ProjectID:     "project-1",
				WorkflowOwner: taskListExpectedWorkflowFromToken,
			})
			if err != nil {
				t.Fatalf("taskListProjectionFromResponse(%s): %v", name, err)
			}
			if name == "narrowed" {
				if projection.Output.WorkflowID == nil || *projection.Output.WorkflowID != taskListWorkflowSelector {
					t.Fatalf("narrowed output workflow = %v, want %q", projection.Output.WorkflowID, taskListWorkflowSelector)
				}
			} else if projection.Output.WorkflowID != nil {
				t.Fatalf("project-wide output workflow = %v, want nil", projection.Output.WorkflowID)
			}
		})
	}
}

func TestTaskListProjectionAcceptsLiveMixedTokenContinuationWithFrozenOneCardinality(t *testing.T) {
	response := serverapi.WorkflowTaskListResponse{
		Scope:                       taskListResponseScope("project-1", nil),
		MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne,
		Tasks: []serverapi.WorkflowTaskListItem{
			{
				TaskID:     "task-1",
				WorkflowID: taskListWorkflowID,
				Status:     serverapi.WorkflowTaskStatus{Kind: serverapi.WorkflowTaskStatusKindQueued, NativeState: "queued"},
			},
			{
				TaskID:     "task-2",
				WorkflowID: taskListSecondWorkflowID,
				Status:     serverapi.WorkflowTaskStatus{Kind: serverapi.WorkflowTaskStatusKindQueued, NativeState: "queued"},
			},
		},
	}

	projection, err := taskListProjectionFromResponse(response, taskListExpectedScope{
		ProjectID:     "project-1",
		WorkflowOwner: taskListExpectedWorkflowFromToken,
	})
	if err != nil {
		t.Fatalf("taskListProjectionFromResponse: %v", err)
	}
	if len(projection.Rows) != 2 ||
		projection.Rows[0].ShowWorkflow ||
		projection.Rows[1].ShowWorkflow ||
		projection.Output.MatchingWorkflowCardinality != serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne {
		t.Fatalf("projection = %+v, want live mixed rows with frozen no-label display", projection)
	}
}

func TestTaskListRecoveryProjectionBuildsLockedCommandMatrix(t *testing.T) {
	projectID := "project-1"
	selectedWorkflowID := taskListWorkflowID
	for _, testCase := range []struct {
		name     string
		scopeErr *serverapi.WorkflowTaskListScopeError
		context  taskListCommandContext
		want     taskWorkflowRecovery
	}{
		{
			name: "no linked workflows preserves dot",
			scopeErr: &serverapi.WorkflowTaskListScopeError{
				Reason:    serverapi.WorkflowTaskListScopeReasonNoLinkedWorkflows,
				ProjectID: &projectID,
			},
			context: taskListCommandContext{
				ProjectRef:        ".",
				ResolvedProjectID: projectID,
				StatusKinds:       []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindRunning},
				PageSize:          50,
				JSON:              true,
			},
			want: taskWorkflowRecovery{
				Kind:       taskWorkflowRecoveryNoLinkedWorkflows,
				ProjectRef: ".",
				Commands: []taskWorkflowRecoveryCommand{
					{Kind: taskWorkflowRecoveryCommandCreateWorkflow, Args: []string{config.Command, "workflow", "create", "<name>"}},
					{Kind: taskWorkflowRecoveryCommandLinkCreatedWorkflow, Args: []string{config.Command, "workflow", "link", ".", "<created-uuid>"}},
					{Kind: taskWorkflowRecoveryCommandListWorkflows, Args: []string{config.Command, "workflow", "list"}},
					{Kind: taskWorkflowRecoveryCommandLinkExistingWorkflow, Args: []string{config.Command, "workflow", "link", ".", "<uuid>"}},
					{Kind: taskWorkflowRecoveryCommandRetryTaskList, Args: []string{config.Command, "task", "list", "--project", ".", "--status", "running", "--page-size", "50", "--json"}},
				},
			},
		},
		{
			name: "not linked preserves path and selected workflow",
			scopeErr: &serverapi.WorkflowTaskListScopeError{
				Reason:     serverapi.WorkflowTaskListScopeReasonWorkflowNotLinked,
				ProjectID:  &projectID,
				WorkflowID: &selectedWorkflowID,
			},
			context: taskListCommandContext{
				ProjectRef:         "/tmp/my project",
				ResolvedProjectID:  projectID,
				SelectedWorkflowID: stringPointerForTaskListTest(taskListWorkflowSelector),
				AttentionKinds:     []serverapi.WorkflowTaskAttentionKind{serverapi.WorkflowTaskAttentionKindApproval},
				PageSize:           100,
				PageToken:          "discard-me",
			},
			want: taskWorkflowRecovery{
				Kind:               taskWorkflowRecoveryWorkflowNotLinked,
				ProjectRef:         "/tmp/my project",
				SelectedWorkflowID: stringPointerForTaskListTest(taskListWorkflowSelector),
				Commands: []taskWorkflowRecoveryCommand{
					{Kind: taskWorkflowRecoveryCommandListProjectWorkflows, Args: []string{config.Command, "workflow", "list", "--project", "/tmp/my project"}},
					{Kind: taskWorkflowRecoveryCommandRetryTaskList, Args: []string{config.Command, "task", "list", "--project", "/tmp/my project", "--workflow", "<uuid>", "--attention", "approval", "--page-size", "100"}},
					{Kind: taskWorkflowRecoveryCommandLinkSelectedWorkflow, Args: []string{config.Command, "workflow", "link", "/tmp/my project", taskListWorkflowSelector}},
				},
			},
		},
		{
			name: "column filter preserves compatible options",
			scopeErr: &serverapi.WorkflowTaskListScopeError{
				Reason:    serverapi.WorkflowTaskListScopeReasonWorkflowRequiredColumns,
				ProjectID: &projectID,
			},
			context: taskListCommandContext{
				ProjectRef:        "project-selector",
				ResolvedProjectID: projectID,
				StatusKinds:       []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindRunning},
				AttentionKinds:    []serverapi.WorkflowTaskAttentionKind{serverapi.WorkflowTaskAttentionKindQuestion},
				ColumnKeys:        []string{"plan"},
				Sort: []serverapi.WorkflowTaskListSort{{
					Field:     serverapi.WorkflowTaskListSortFieldUpdated,
					Direction: serverapi.WorkflowTaskListSortDirectionDesc,
				}},
				PageSize:  25,
				PageToken: "discard-me",
				JSON:      true,
			},
			want: taskWorkflowRecovery{
				Kind:       taskWorkflowRecoveryWorkflowRequiredColumns,
				ProjectRef: "project-selector",
				Commands: []taskWorkflowRecoveryCommand{
					{Kind: taskWorkflowRecoveryCommandListProjectWorkflows, Args: []string{config.Command, "workflow", "list", "--project", "project-selector"}},
					{Kind: taskWorkflowRecoveryCommandRetryTaskList, Args: []string{config.Command, "task", "list", "--project", "project-selector", "--workflow", "<uuid>", "--status", "running", "--attention", "question", "--column", "plan", "--sort", "updated:desc", "--page-size", "25", "--json"}},
				},
			},
		},
		{
			name: "column sort requires workflow and discards token",
			scopeErr: &serverapi.WorkflowTaskListScopeError{
				Reason:    serverapi.WorkflowTaskListScopeReasonWorkflowRequiredColumns,
				ProjectID: &projectID,
			},
			context: taskListCommandContext{
				ProjectRef:        ".",
				ResolvedProjectID: projectID,
				Sort: []serverapi.WorkflowTaskListSort{{
					Field:     serverapi.WorkflowTaskListSortFieldColumn,
					Direction: serverapi.WorkflowTaskListSortDirectionAsc,
				}},
				PageSize:  75,
				PageToken: "discard-me",
			},
			want: taskWorkflowRecovery{
				Kind:       taskWorkflowRecoveryWorkflowRequiredColumns,
				ProjectRef: ".",
				Commands: []taskWorkflowRecoveryCommand{
					{Kind: taskWorkflowRecoveryCommandListProjectWorkflows, Args: []string{config.Command, "workflow", "list", "--project", "."}},
					{Kind: taskWorkflowRecoveryCommandRetryTaskList, Args: []string{config.Command, "task", "list", "--project", ".", "--workflow", "<uuid>", "--sort", "column:asc", "--page-size", "75"}},
				},
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := taskListRecoveryForScopeError(testCase.scopeErr, testCase.context)
			if err != nil {
				t.Fatalf("taskListRecoveryForScopeError: %v", err)
			}
			if !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("recovery = %+v, want %+v", got, testCase.want)
			}
		})
	}
}

func TestTaskListLoopbackKeepsProjectOnlyScopeWithOneWorkflow(t *testing.T) {
	ctx := context.Background()
	cfg, binding, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()
	workflowID := setupLinkedWorkflow(t, binding.ProjectID, "Unique Scope")
	task := createTaskForListCommandTest(t, ctx, remote, binding.ProjectID, workflowID, "Task")

	for _, args := range [][]string{
		{"task", "list", "--project", binding.ProjectID, "--json"},
	} {
		stdout, stderr, code := runWorkflowRootCommand(args...)
		if code != 0 {
			t.Fatalf("%v exit=%d stderr=%q", args, code, stderr)
		}
		var response taskListOutput
		if err := json.Unmarshal([]byte(stdout), &response); err != nil {
			t.Fatalf("%v JSON = %q: %v", args, stdout, err)
		}
		if response.ProjectID != binding.ProjectID || response.WorkflowID != nil || len(response.Tasks) != 1 || response.Tasks[0].TaskID != string(task.ID) || response.Tasks[0].WorkflowID != workflowID {
			t.Fatalf("%v response = %+v, want project-wide scope and normalized task workflow", args, response)
		}
	}
}

func TestTaskListLoopbackProjectScopeSpansLinkedWorkflows(t *testing.T) {
	ctx := context.Background()
	cfg, binding, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()
	firstWorkflowID := setupLinkedWorkflow(t, binding.ProjectID, "First Workflow")
	secondWorkflowID := setupLinkedWorkflow(t, binding.ProjectID, "Second Workflow")
	firstTask := createTaskForListCommandTest(t, ctx, remote, binding.ProjectID, firstWorkflowID, "First")
	secondTask := createTaskForListCommandTest(t, ctx, remote, binding.ProjectID, secondWorkflowID, "Second")

	stdout, stderr, code := runWorkflowRootCommand("task", "list", "--project", binding.ProjectID, "--json")
	if code != 0 {
		t.Fatalf("task list exit=%d stderr=%q", code, stderr)
	}
	var response taskListOutput
	if err := json.Unmarshal([]byte(stdout), &response); err != nil {
		t.Fatalf("task list JSON = %q: %v", stdout, err)
	}
	if response.WorkflowID != nil || response.MatchingWorkflowCardinality != serverapi.WorkflowTaskListMatchingWorkflowCardinalityMultiple || len(response.Tasks) != 2 {
		t.Fatalf("task list response = %+v, want project-wide multiple-workflow page", response)
	}
	seen := map[string]string{}
	for _, task := range response.Tasks {
		seen[task.TaskID] = task.WorkflowID
	}
	if seen[string(firstTask.ID)] != firstWorkflowID || seen[string(secondTask.ID)] != secondWorkflowID {
		t.Fatalf("task list workflows = %+v, want normalized linked workflow IDs", seen)
	}
}

func TestTaskListLoopbackValidatesExactPairAndContinuesFromToken(t *testing.T) {
	ctx := context.Background()
	cfg, binding, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()
	workflowID := setupLinkedWorkflow(t, binding.ProjectID, "Exact Scope")
	createTaskForListCommandTest(t, ctx, remote, binding.ProjectID, workflowID, "First")
	createTaskForListCommandTest(t, ctx, remote, binding.ProjectID, workflowID, "Second")

	firstJSON, firstErr, code := runWorkflowRootCommand("task", "list", "--project", binding.ProjectID, "--page-size", "1", "--json")
	if code != 0 {
		t.Fatalf("first list exit=%d stderr=%q", code, firstErr)
	}
	var first taskListOutput
	if err := json.Unmarshal([]byte(firstJSON), &first); err != nil {
		t.Fatalf("first list JSON = %q: %v", firstJSON, err)
	}
	if first.ProjectID != binding.ProjectID || first.WorkflowID != nil || len(first.Tasks) != 1 || first.NextPageToken == nil {
		t.Fatalf("first page = %+v, want project-wide scope and continuation", first)
	}

	secondJSON, secondErr, code := runWorkflowRootCommand("task", "list", "--page-token", *first.NextPageToken, "--page-size", "1", "--json")
	if code != 0 {
		t.Fatalf("continuation exit=%d stderr=%q", code, secondErr)
	}
	var second taskListOutput
	if err := json.Unmarshal([]byte(secondJSON), &second); err != nil {
		t.Fatalf("continuation JSON = %q: %v", secondJSON, err)
	}
	if second.ProjectID != binding.ProjectID || second.WorkflowID != nil || len(second.Tasks) != 1 || second.Tasks[0].TaskID == first.Tasks[0].TaskID {
		t.Fatalf("continuation = %+v, want distinct next task in exact token scope", second)
	}

	unlinkedWorkflowID := createRunnableWorkflowForCommandTest(t, "Unlinked Workflow")
	if unlinkedWorkflowID == workflowID {
		t.Fatal("test fixture must create an unlinked workflow")
	}
}

func createTaskForListCommandTest(t *testing.T, ctx context.Context, remote *workflowCommandLoopbackRemote, projectID string, workflowID string, title string) workflowstore.TaskRecord {
	t.Helper()
	persistedWorkflowID := workflow.WorkflowID(workflowPersistedIDForTest(t, workflowID))
	task, err := remote.store.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID:  projectID,
		WorkflowID: &persistedWorkflowID,
		Title:      title,
		Body:       "Body",
	})
	if err != nil {
		t.Fatalf("CreateTask %q: %v", title, err)
	}
	return task
}
