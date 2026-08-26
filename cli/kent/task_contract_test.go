package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"core/shared/apicontract"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessionenv"
	"core/shared/workflowcontract"
)

type taskPaginationStub struct {
	apicontract.WorkflowService
	taskListRequest  serverapi.WorkflowTaskListRequest
	taskListResponse serverapi.WorkflowTaskListResponse
}

type taskSearchServiceStub struct {
	apicontract.ProjectViewService
	apicontract.WorkflowService
	request  serverapi.TaskSearchRequest
	response serverapi.TaskSearchResponse
	err      error
}

func (s *taskSearchServiceStub) SearchWorkflowTasks(
	_ context.Context,
	request serverapi.TaskSearchRequest,
) (serverapi.TaskSearchResponse, error) {
	s.request = request
	return s.response, s.err
}

func (s *taskPaginationStub) ListWorkflowTasks(
	_ context.Context,
	request serverapi.WorkflowTaskListRequest,
) (serverapi.WorkflowTaskListResponse, error) {
	s.taskListRequest = request
	return s.taskListResponse, nil
}

func TestTaskListPureFilterSortAndPaginationContracts(t *testing.T) {
	values, err := parseTaskListFilterValues([]string{"active,done", "active"}, "status")
	if err != nil || !slices.Equal(values, []string{"active", "done"}) {
		t.Fatalf("values=%v err=%v", values, err)
	}
	if _, err := parseTaskListFilterValues([]string{"active, "}, "status"); err == nil {
		t.Fatal("blank filter value accepted")
	}

	statuses, err := parseTaskListStatusKinds([]string{"active,done"})
	if err != nil || !slices.Equal(statuses, []serverapi.WorkflowTaskStatusKind{
		serverapi.WorkflowTaskStatusKindActive,
		serverapi.WorkflowTaskStatusKindDone,
	}) {
		t.Fatalf("statuses=%v err=%v", statuses, err)
	}
	if _, err := parseTaskListStatusKinds([]string{"future"}); err == nil {
		t.Fatal("unknown status accepted")
	}
	attention, err := parseTaskListAttentionKinds([]string{"question,interrupted"})
	if err != nil || !slices.Equal(attention, []serverapi.WorkflowTaskAttentionKind{
		serverapi.WorkflowTaskAttentionKindQuestion,
		serverapi.WorkflowTaskAttentionKindInterrupted,
	}) {
		t.Fatalf("attention=%v err=%v", attention, err)
	}

	sortSelectors, err := parseTaskListSortSelectors([]string{
		"labels:desc,short_id:asc,created:asc",
		"updated:desc,status:asc,column:desc,title:asc",
	})
	if err != nil || len(sortSelectors) != 7 {
		t.Fatalf("sort=%v err=%v", sortSelectors, err)
	}
	if sortSelectors[0].Field != serverapi.WorkflowTaskListSortFieldLabels ||
		sortSelectors[1].Field != serverapi.WorkflowTaskListSortFieldShortID ||
		sortSelectors[6].Field != serverapi.WorkflowTaskListSortFieldTitle {
		t.Fatalf("sort=%+v", sortSelectors)
	}
	for _, invalid := range []string{
		"title",
		"title:sideways",
		"title:asc,title:desc",
		"label:asc",
	} {
		if _, err := parseTaskListSortSelectors([]string{invalid}); err == nil {
			t.Fatalf("invalid sort %q accepted", invalid)
		}
	}

	if _, err := parseTaskListLabelMatch("all", true, 0, false); err == nil {
		t.Fatal("label match without selectors accepted")
	}
	if _, err := parseTaskListLabelMatch("any", false, 1, true); err == nil {
		t.Fatal("unlabeled with selector accepted")
	}
	if mode, err := parseTaskListLabelMatch("all", true, 2, false); err != nil ||
		mode != serverapi.WorkflowTaskNamedLabelFilterModeAll {
		t.Fatalf("label mode=%q err=%v", mode, err)
	}

	if err := validateWorkflowPagination(0, taskListDefaultLimit); err != nil {
		t.Fatalf("valid pagination: %v", err)
	}
	for _, window := range [][2]int{{-1, 1}, {0, 0}, {0, serverapi.WorkflowPaginationMaxLimit + 1}} {
		if err := validateWorkflowPagination(window[0], window[1]); err == nil {
			t.Fatalf("invalid pagination %v accepted", window)
		}
	}
}

func TestTaskListDependencyFilterAndRetryArguments(t *testing.T) {
	for _, test := range []struct {
		name              string
		unblocked         bool
		unblockedProvided bool
		blocked           bool
		blockedProvided   bool
		want              *bool
		wantError         bool
	}{
		{name: "none"},
		{name: "unblocked", unblocked: true, unblockedProvided: true, want: boolTaskPointer(true)},
		{name: "blocked", blocked: true, blockedProvided: true, want: boolTaskPointer(false)},
		{name: "mutually exclusive", unblocked: true, unblockedProvided: true, blocked: true, blockedProvided: true, wantError: true},
		{name: "explicit false", unblockedProvided: true, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseTaskListDependencyFilter(
				test.unblocked,
				test.unblockedProvided,
				test.blocked,
				test.blockedProvided,
			)
			if (err != nil) != test.wantError || !equalBoolTaskPointers(got, test.want) {
				t.Fatalf("filter=%v err=%v", got, err)
			}
		})
	}

	mode := serverapi.WorkflowTaskNamedLabelFilterModeAll
	blocked := false
	args := taskListRetryCommandArgs(taskListCommandContext{
		ProjectRef:             "project-ref",
		StatusKinds:            []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindActive},
		AttentionKinds:         []serverapi.WorkflowTaskAttentionKind{serverapi.WorkflowTaskAttentionKindQuestion},
		ColumnKeys:             []string{"build"},
		Sort:                   []serverapi.WorkflowTaskListSort{{Field: serverapi.WorkflowTaskListSortFieldUpdated, Direction: serverapi.WorkflowTaskListSortDirectionDesc}},
		LabelSelectors:         []string{"Alpha"},
		ExcludedLabelSelectors: []string{"Beta"},
		LabelMatch:             &mode,
		DependencyFilter:       &blocked,
		Limit:                  100,
		JSON:                   true,
	}, nil)
	want := []string{
		config.Command, "task", "list", "--project", "project-ref",
		"--status", "active",
		"--attention", "question",
		"--column", "build",
		"--sort", "updated:desc",
		"--label", "Alpha",
		"--not-label", "Beta",
		"--label-match", "all",
		"--blocked",
		"--limit", "100",
		"--json",
	}
	if !slices.Equal(args, want) {
		t.Fatalf("retry args=%v want=%v", args, want)
	}

	placeholder := taskListRetryCommandArgsForSelector(
		taskListCommandContext{ProjectRef: "project-ref", Limit: 100},
		taskWorkflowRetryWorkflowPlaceholder{},
	)
	if !slices.Equal(placeholder, []string{
		config.Command, "task", "list", "--project", "project-ref",
		"--workflow", "<uuid>", "--limit", "100",
	}) {
		t.Fatalf("placeholder retry=%v", placeholder)
	}
}

func TestTaskListAndCommentPaginationSuccess(t *testing.T) {
	offset, limit, nextOffset := 5, 2, 7
	stub := &taskPaginationStub{
		taskListResponse: serverapi.WorkflowTaskListResponse{
			Scope: serverapi.WorkflowTaskListScope{
				ProjectID: "project-1",
			},
			MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityNone,
			Tasks:                       []serverapi.WorkflowTaskListItem{},
			NextOffset:                  &nextOffset,
		},
	}
	response, err := workflowTaskList(t.Context(), stub, serverapi.WorkflowTaskListRequest{
		ProjectID: func() *string {
			projectID := "project-1"
			return &projectID
		}(),
		Offset: &offset,
		Limit:  &limit,
	})
	if err != nil ||
		stub.taskListRequest.Offset == nil ||
		*stub.taskListRequest.Offset != offset ||
		stub.taskListRequest.Limit == nil ||
		*stub.taskListRequest.Limit != limit {
		t.Fatalf("request=%+v response=%+v err=%v", stub.taskListRequest, response, err)
	}

	var stdout, stderr bytes.Buffer
	expected := taskListExpectedScope{
		ProjectID:     "project-1",
		WorkflowOwner: taskListExpectedWorkflowFromRequest,
	}
	if code := writeTaskListResponse(&stdout, &stderr, response, expected, true); code != 0 || stderr.Len() != 0 {
		t.Fatalf("JSON exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var output struct {
		NextOffset *int              `json:"next_offset"`
		Tasks      []json.RawMessage `json:"tasks"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil ||
		output.NextOffset == nil ||
		*output.NextOffset != nextOffset ||
		len(output.Tasks) != 0 {
		t.Fatalf("output=%+v err=%v", output, err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := writeTaskListResponse(&stdout, &stderr, response, expected, false); code != 0 ||
		stdout.Len() != 0 ||
		stderr.Len() == 0 {
		t.Fatalf("human exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := writeTaskCommentListResponse(&stdout, &stderr, serverapi.WorkflowTaskCommentListResponse{
		WorkflowOffsetPage: serverapi.WorkflowOffsetPage[serverapi.WorkflowTaskComment]{
			Items:      []serverapi.WorkflowTaskComment{},
			NextOffset: &nextOffset,
		},
	}); code != 0 ||
		stdout.Len() != 0 ||
		stderr.Len() == 0 {
		t.Fatalf("comment exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestTaskListProjectionSuppressesEnrichedWorkflowNameForOneWorkflow(t *testing.T) {
	workflowID := runtimeids.NewWorkflowID()
	workflowName := "Delivery"
	projection, err := taskListProjectionFromResponse(
		serverapi.WorkflowTaskListResponse{
			Scope:                       serverapi.WorkflowTaskListScope{ProjectID: "project-1"},
			MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne,
			Tasks: []serverapi.WorkflowTaskListItem{{
				TaskID:       "task-1",
				ShortID:      "KENT-1",
				WorkflowID:   workflowID,
				WorkflowName: &workflowName,
				Title:        "One Workflow",
				Status:       taskContractStatus(serverapi.WorkflowTaskStatusKindActive),
				Labels:       []serverapi.WorkflowProjectLabel{{ID: "label-1", Name: "Priority"}},
			}},
		},
		taskListExpectedScope{
			ProjectID:     "project-1",
			WorkflowOwner: taskListExpectedWorkflowFromRequest,
		},
	)
	if err != nil {
		t.Fatalf("project Task-list projection: %v", err)
	}
	if len(projection.Rows) != 1 ||
		projection.Rows[0].WorkflowName != nil ||
		!slices.Equal(projection.Rows[0].LabelNames, []string{"Priority"}) {
		t.Fatalf("one-Workflow row = %+v", projection.Rows)
	}
}

func TestTaskListCommandRejectsPaginationAndRemovedFlagsBeforeRemote(t *testing.T) {
	for _, test := range []struct {
		args []string
		code int
	}{
		{args: []string{"--offset", "-1"}, code: 2},
		{args: []string{"--limit", "0"}, code: 2},
		{args: []string{"--limit", "101"}, code: 2},
		{args: []string{"--page-token", "legacy"}, code: 2},
		{args: []string{"--page-size", "1"}, code: 2},
		{args: []string{"extra"}, code: 2},
	} {
		var stdout, stderr bytes.Buffer
		if code := taskListSubcommand(test.args, &stdout, &stderr); code != test.code ||
			stdout.Len() != 0 || stderr.Len() == 0 {
			t.Fatalf("args=%q exit=%d stdout=%q stderr=%q", test.args, code, stdout.String(), stderr.String())
		}
	}
}

func TestTaskCommentListRejectsInvalidPaginationBeforeRemote(t *testing.T) {
	for _, args := range [][]string{
		{"DIS-1", "--offset", "-1"},
		{"DIS-1", "--limit", "0"},
		{"DIS-1", "--limit", "101"},
	} {
		var stdout, stderr bytes.Buffer
		if code := taskCommentListSubcommand(args, &stdout, &stderr); code != 2 ||
			stdout.Len() != 0 ||
			stderr.Len() == 0 {
			t.Fatalf("args=%q exit=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
}

func TestTaskSearchExecutionProjectsScopeAndTypedOutcomes(t *testing.T) {
	nextOffset := 7
	request := serverapi.TaskSearchRequest{
		Mode:            serverapi.TaskSearchModeFTS5,
		Query:           "title:needle",
		Context:         7,
		IncludeComments: true,
		StatusKinds: []serverapi.WorkflowTaskStatusKind{
			serverapi.WorkflowTaskStatusKindActive,
			serverapi.WorkflowTaskStatusKindDone,
		},
		PageSize: 2,
		Offset:   func() *int { value := 4; return &value }(),
	}
	response := serverapi.TaskSearchResponse{
		Mode:       serverapi.TaskSearchModeFTS5,
		Groups:     []serverapi.TaskSearchGroup{},
		NextOffset: &nextOffset,
	}
	stub := &taskSearchServiceStub{response: response}
	var stdout, stderr bytes.Buffer
	if code := runTaskSearch(
		t.Context(),
		config.App{},
		stub,
		stub,
		[]string{"project-b", "project-a", "project-b"},
		request,
		true,
		&stdout,
		&stderr,
	); code != 0 || stdout.Len() == 0 || stderr.Len() == 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !slices.Equal(stub.request.ProjectIDs, []string{"project-a", "project-b"}) ||
		stub.request.Mode != request.Mode ||
		stub.request.Query != request.Query ||
		stub.request.Context != request.Context ||
		stub.request.IncludeComments != request.IncludeComments ||
		!slices.Equal(stub.request.StatusKinds, request.StatusKinds) ||
		stub.request.PageSize != request.PageSize ||
		stub.request.Offset == nil ||
		*stub.request.Offset != *request.Offset {
		t.Fatalf("request=%+v", stub.request)
	}
	var output serverapi.TaskSearchResponse
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil ||
		output.Mode != response.Mode ||
		output.NextOffset == nil ||
		*output.NextOffset != nextOffset ||
		len(output.Groups) != 0 {
		t.Fatalf("output=%+v err=%v", output, err)
	}

	stub = &taskSearchServiceStub{
		err: &serverapi.TaskSearchError{
			Reason: serverapi.TaskSearchErrorReasonNormalizedTooShort,
		},
	}
	stdout.Reset()
	stderr.Reset()
	if code := runTaskSearch(
		t.Context(),
		config.App{},
		stub,
		stub,
		nil,
		request,
		false,
		&stdout,
		&stderr,
	); code != 2 || stdout.Len() != 0 || stderr.Len() == 0 {
		t.Fatalf("failure exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestTaskDependencyDirectionRenderingAndTypedJSON(t *testing.T) {
	for raw, want := range map[string]*serverapi.WorkflowTaskDependencyDirection{
		"":           nil,
		"blocks":     taskDependencyDirectionPointer(serverapi.WorkflowTaskDependencyDirectionBlocks),
		"blocked-by": taskDependencyDirectionPointer(serverapi.WorkflowTaskDependencyDirectionBlockedBy),
	} {
		got, err := parseTaskDependencyDirection(raw)
		if err != nil || !equalTaskDependencyDirections(got, want) {
			t.Fatalf("raw=%q direction=%v err=%v", raw, got, err)
		}
	}
	if _, err := parseTaskDependencyDirection("upstream"); err == nil {
		t.Fatal("invalid dependency direction accepted")
	}

	directions := []serverapi.WorkflowTaskDependencyListDirectionProjection{
		{
			Direction:  serverapi.WorkflowTaskDependencyDirectionBlockedBy,
			TotalCount: 1,
			Items: []serverapi.WorkflowTaskDependencyItem{{
				TaskID: "task-1", ShortID: "KENT-1", Title: "Foundation",
				Status: taskContractStatus(serverapi.WorkflowTaskStatusKindActive),
			}},
		},
		{
			Direction:  serverapi.WorkflowTaskDependencyDirectionBlocks,
			TotalCount: 1,
			Items: []serverapi.WorkflowTaskDependencyItem{{
				TaskID: "task-2", ShortID: "KENT-2", Title: "Follow-up",
				Status: taskContractStatus(serverapi.WorkflowTaskStatusKindBacklog),
			}},
		},
	}
	ordered := taskDependencyDirectionsForRender(directions)
	if len(ordered) != 2 ||
		ordered[0].Direction != serverapi.WorkflowTaskDependencyDirectionBlocks ||
		ordered[0].Items[0].TaskID != "task-2" ||
		ordered[1].Direction != serverapi.WorkflowTaskDependencyDirectionBlockedBy ||
		ordered[1].Items[0].TaskID != "task-1" {
		t.Fatalf("ordered directions=%+v", ordered)
	}
	var stdout bytes.Buffer
	if err := writeTaskDependencyDirections(&stdout, directions); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() == 0 {
		t.Fatalf("render=%q", stdout.String())
	}

	response := serverapi.WorkflowTaskDependencyMutationResponse{
		Outcome:        serverapi.WorkflowTaskDependencyOutcomeAlreadyAbsent,
		BlockerTaskID:  "task-1",
		BlockerShortID: "KENT-1",
		BlockedTaskID:  "task-2",
		BlockedShortID: "KENT-2",
	}
	stdout.Reset()
	var stderr bytes.Buffer
	if code := writeCommandJSON(&stdout, &stderr, response); code != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"outcome", "blocker_task_id", "blocker_short_id", "blocked_task_id", "blocked_short_id"} {
		if fields[key] == nil {
			t.Fatalf("JSON omitted %q: %s", key, stdout.String())
		}
	}
}

func TestTaskMoveStructuredValuesSelectionAndDependencyGuidance(t *testing.T) {
	values, err := readManualMoveValues(
		`{"build":{"artifact":"release.zip"},"test":{"result":"passed"}}`,
		"",
		true,
		false,
	)
	if err != nil || values["build"]["artifact"] != "release.zip" || values["test"]["result"] != "passed" {
		t.Fatalf("values=%v err=%v", values, err)
	}
	if _, err := readManualMoveValues("{}", "values.json", true, true); err == nil {
		t.Fatal("combined values sources accepted")
	}
	if _, err := readManualMoveValues("null", "", true, false); err == nil {
		t.Fatal("null values object accepted")
	}
	path := filepath.Join(t.TempDir(), "values.json")
	if err := os.WriteFile(path, []byte(`{"node":{"output":"value"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fileValues, err := readManualMoveValues("", path, false, true)
	if err != nil || fileValues["node"]["output"] != "value" {
		t.Fatalf("file values=%v err=%v", fileValues, err)
	}

	preview := serverapi.WorkflowTaskMovePreviewResponse{
		Outcome: serverapi.WorkflowTaskMovePreviewOutcomeTransition,
		Transition: &serverapi.WorkflowTaskMovePreviewTransition{Choices: []serverapi.WorkflowTaskMovePreviewTransitionChoice{
			{TransitionKey: "approve"},
			{TransitionKey: "revise"},
		}},
	}
	if _, err := selectTaskMoveTransition(preview, "", false); err == nil {
		t.Fatal("ambiguous transition auto-selected")
	}
	selected, err := selectTaskMoveTransition(preview, " revise ", true)
	if err != nil || selected == nil || *selected != "revise" {
		t.Fatalf("selected=%v err=%v", selected, err)
	}
	if _, err := selectTaskMoveTransition(preview, "missing", true); err == nil {
		t.Fatal("unknown transition accepted")
	}
	preview.Transition.Choices = preview.Transition.Choices[:1]
	selected, err = selectTaskMoveTransition(preview, "", false)
	if err != nil || selected == nil || *selected != "approve" {
		t.Fatalf("auto selection=%v err=%v", selected, err)
	}

	count := 2
	var stderr bytes.Buffer
	writeTaskDependencyConfirmationRequired(&stderr, "KENT-2", &count)
	if stderr.Len() == 0 {
		t.Fatalf("guidance=%q", stderr.String())
	}

	response := serverapi.WorkflowTaskMoveResponse{
		Outcome:                    serverapi.WorkflowExecutionTargetActionOutcomeDependencyConfirmationRequired,
		UnsatisfiedDependencyCount: &count,
	}
	var stdout bytes.Buffer
	stderr.Reset()
	if code := writeCommandJSON(&stdout, &stderr, response); code != 0 || stderr.Len() != 0 {
		t.Fatalf("dependency JSON exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 2 || fields["outcome"] == nil || fields["unsatisfied_dependency_count"] == nil {
		t.Fatalf("dependency JSON=%s", stdout.String())
	}
}

func TestTaskSetupGuidanceContracts(t *testing.T) {
	target := workflowcontract.ExecutionTargetSelection{Mode: workflowcontract.ExecutionTargetModeHead}
	script := "/repo/setup.sh"
	failed := &serverapi.WorktreeSetupEvent{
		Phase: serverapi.WorktreeSetupPhaseFailed,
		Failed: &serverapi.WorktreeSetupFailed{
			RetryReadiness: serverapi.WorktreeSetupRetryReady,
			Cause: serverapi.WorktreeSetupFailureCause{
				Kind:        serverapi.WorktreeSetupFailureProcessExit,
				ProcessExit: &serverapi.WorktreeSetupProcessExit{ExitCode: 1},
			},
			Diagnostic:               "failed twice",
			ScriptPath:               &script,
			ExecutionTarget:          &target,
			RetainedWorktree:         taskContractSetupWorktree("/tmp/retained"),
			RetainedPreviousWorktree: &serverapi.RetainedPreviousWorktree{Worktree: *taskContractSetupWorktree("/tmp/previous")},
		},
	}
	start, err := projectTaskSetupGuidance(taskSetupObservedActionStart, "task-1", nil, failed, nil)
	if err != nil ||
		start.Outcome != taskSetupOutcomeStartInterruptedSetupFailure ||
		start.RetainedRoot == nil ||
		*start.RetainedRoot != "/tmp/retained" ||
		start.RetainedPreviousWorktree == nil ||
		len(start.Actions) != 5 ||
		start.Actions[0].Kind != taskSetupActionRetry ||
		start.Actions[0].Args[len(start.Actions[0].Args)-1] != "head" {
		t.Fatalf("start setup guidance=%+v err=%v", start, err)
	}
	resume, err := projectTaskSetupGuidance(taskSetupObservedActionResume, "task-1", nil, failed, nil)
	if err != nil || resume.Outcome != taskSetupOutcomeResumeInterruptedSetupFailure {
		t.Fatalf("resume setup guidance=%+v err=%v", resume, err)
	}
	completed, err := projectTaskSetupGuidance(taskSetupObservedActionStart, "task-1", nil, &serverapi.WorktreeSetupEvent{
		Phase: serverapi.WorktreeSetupPhaseNotRequired,
		NotRequired: &serverapi.WorktreeSetupNotRequired{
			Reason:                   serverapi.WorktreeSetupNotRequiredNoConfiguredScript,
			RetainedPreviousWorktree: &serverapi.RetainedPreviousWorktree{Worktree: *taskContractSetupWorktree("/tmp/orphan")},
		},
	}, nil)
	if err != nil ||
		completed.Outcome != taskSetupOutcomeCompleted ||
		completed.RetainedPreviousWorktree == nil ||
		len(completed.Actions) != 1 ||
		completed.Actions[0].Kind != taskSetupActionListWorktrees {
		t.Fatalf("completed setup guidance=%+v err=%v", completed, err)
	}
	observation, err := projectTaskSetupGuidance(
		taskSetupObservedActionStart,
		"task-1",
		nil,
		nil,
		context.DeadlineExceeded,
	)
	if err != nil ||
		observation.Outcome != taskSetupOutcomeObservationFailure ||
		len(observation.Actions) != 2 {
		t.Fatalf("observation guidance=%+v err=%v", observation, err)
	}
}

func TestTaskMoveSetupRecoveryPreservesStructuredInput(t *testing.T) {
	project, commentary, transition := "project-1", "note", "next"
	base, err := taskMoveRecoveryArgs(
		"task-1",
		"done",
		&project,
		&commentary,
		&transition,
		map[string]map[string]string{"plan": {"summary": "done"}},
		true,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	target := workflowcontract.ExecutionTargetSelection{Mode: workflowcontract.ExecutionTargetModeHead}
	guidance, err := projectMoveSetupGuidance(base, &target, &serverapi.WorktreeSetupRetainedError{
		Worktree:                 *taskContractSetupWorktree("/tmp/retained"),
		Diagnostic:               "failed twice",
		ScriptPath:               "/repo/setup.sh",
		RetainedPreviousWorktree: &serverapi.RetainedPreviousWorktree{Worktree: *taskContractSetupWorktree("/tmp/previous")},
	})
	if err != nil ||
		guidance.Outcome != taskSetupOutcomeMoveSetupFailure ||
		guidance.RetainedRoot == nil ||
		*guidance.RetainedRoot != "/tmp/retained" ||
		guidance.RetainedPreviousWorktree == nil ||
		len(guidance.Actions) != 5 ||
		!slices.Contains(guidance.Actions[0].Args, `{"plan":{"summary":"done"}}`) {
		t.Fatalf("move setup guidance=%+v err=%v", guidance, err)
	}
}

func TestWorktreeRuntimeOriginHeaderAndBranchCleanupPolicy(t *testing.T) {
	const (
		runID  = "018fdd67-89ab-4cde-8123-456789abc001"
		stepID = "018fdd67-89ab-4cde-8123-456789abc002"
	)
	t.Setenv(sessionenv.RunIDEnv, runID)
	t.Setenv(sessionenv.StepIDEnv, stepID)
	origin, err := worktreeCommandRuntimeOrigin()
	if err != nil || origin == nil || origin.RunID != runID || origin.StepID != stepID {
		t.Fatalf("origin=%+v err=%v", origin, err)
	}
	header, err := newWorktreeCommandTransitionHeader("session-1")
	if err != nil || header.SessionID != "session-1" || header.Origin == nil ||
		header.Origin.RunID != runID || header.Origin.StepID != stepID ||
		header.OperationID.String() == "" {
		t.Fatalf("header=%+v err=%v", header, err)
	}

	for _, invalid := range []struct {
		run  string
		step string
	}{
		{run: runID},
		{step: stepID},
		{run: "invalid", step: stepID},
		{run: runID, step: "invalid"},
	} {
		t.Run(invalid.run+"/"+invalid.step, func(t *testing.T) {
			t.Setenv(sessionenv.RunIDEnv, invalid.run)
			t.Setenv(sessionenv.StepIDEnv, invalid.step)
			if _, err := worktreeCommandRuntimeOrigin(); err == nil {
				t.Fatal("invalid origin accepted")
			}
			if _, err := newWorktreeCommandTransitionHeader("session-1"); err == nil {
				t.Fatal("header accepted invalid origin")
			}
		})
	}

	unsetEnvironmentForTaskContractTest(t, sessionenv.RunIDEnv)
	unsetEnvironmentForTaskContractTest(t, sessionenv.StepIDEnv)
	if origin, err := worktreeCommandRuntimeOrigin(); err != nil || origin != nil {
		t.Fatalf("absent origin=%+v err=%v", origin, err)
	}
	if header, err := newWorktreeCommandTransitionHeader("session-1"); err != nil || header.Origin != nil {
		t.Fatalf("external header=%+v err=%v", header, err)
	}

	for _, test := range []struct {
		name        string
		delete      bool
		forceDelete bool
		agent       bool
		want        serverapi.WorktreeBranchCleanupMode
		wantError   bool
	}{
		{name: "retain", want: serverapi.WorktreeBranchCleanupModeRetain},
		{name: "safe delete", delete: true, want: serverapi.WorktreeBranchCleanupModeDeleteSafe},
		{name: "force delete", delete: true, forceDelete: true, want: serverapi.WorktreeBranchCleanupModeDeleteForce},
		{name: "force requires delete", forceDelete: true, wantError: true},
		{name: "agent retains branch", delete: true, agent: true, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := worktreeBranchCleanupPolicy(test.delete, test.forceDelete, test.agent)
			if (err != nil) != test.wantError || got != test.want {
				t.Fatalf("policy=%q err=%v", got, err)
			}
		})
	}
}

func TestTaskDispatchAndDependencyAliasesRemainAvailable(t *testing.T) {
	var canonical bytes.Buffer
	if code := taskSubcommand([]string{"dep", "--help"}, &canonical, &canonical); code != 0 {
		t.Fatalf("canonical help exit=%d output=%q", code, canonical.String())
	}
	for _, alias := range []string{"deps", "dependency", "dependencies"} {
		var output bytes.Buffer
		if code := taskSubcommand([]string{alias, "--help"}, &output, &output); code != 0 ||
			output.Len() == 0 {
			t.Fatalf("alias=%q exit=%d output=%q", alias, code, output.String())
		}
	}
	var taskHelp bytes.Buffer
	if code := taskSubcommand([]string{"--help"}, &taskHelp, &taskHelp); code != 0 ||
		taskHelp.Len() == 0 {
		t.Fatalf("task help exit=%d output=%q", code, taskHelp.String())
	}
}

func boolTaskPointer(value bool) *bool { return &value }

func equalBoolTaskPointers(left *bool, right *bool) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func taskDependencyDirectionPointer(value serverapi.WorkflowTaskDependencyDirection) *serverapi.WorkflowTaskDependencyDirection {
	return &value
}

func equalTaskDependencyDirections(
	left *serverapi.WorkflowTaskDependencyDirection,
	right *serverapi.WorkflowTaskDependencyDirection,
) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func taskContractStatus(kind serverapi.WorkflowTaskStatusKind) serverapi.WorkflowTaskStatus {
	native, ok := kind.NativeState()
	if !ok {
		panic("invalid task status")
	}
	return serverapi.WorkflowTaskStatus{Kind: kind, NativeState: native}
}

func taskContractSetupWorktree(root string) *serverapi.WorktreeTopologyEntry {
	return &serverapi.WorktreeTopologyEntry{
		Variant: serverapi.WorktreeTopologyVariantRegistered,
		Registered: &serverapi.WorktreeRegisteredFacts{
			Git:  serverapi.WorktreeGitFacts{CanonicalRoot: root, HeadObject: "0123456789abcdef"},
			Kent: serverapi.WorktreeKentFacts{WorktreeID: "worktree-1", CanonicalRoot: root, DisplayName: "KENT-453", Managed: true},
		},
	}
}

func unsetEnvironmentForTaskContractTest(t *testing.T, name string) {
	t.Helper()
	value, present := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if present {
			_ = os.Setenv(name, value)
			return
		}
		_ = os.Unsetenv(name)
	})
}
