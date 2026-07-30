package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"core/shared/apicontract"
	"core/shared/config"
	"core/shared/serverapi"
)

const (
	taskListCommandTestProjectID = "project-1"
	taskListCommandAlphaID       = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	taskListCommandBetaID        = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	taskListCommandBangID        = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
)

type taskListCommandRemote struct {
	apicontract.WorkflowService

	catalogResponse serverapi.WorkflowProjectLabelCatalogResponse
	catalogRequests []serverapi.WorkflowProjectLabelCatalogRequest
	listRequests    []serverapi.WorkflowTaskListRequest
	listResponse    serverapi.WorkflowTaskListResponse
	listErr         error
}

func (r *taskListCommandRemote) ListWorkflowProjectLabels(_ context.Context, req serverapi.WorkflowProjectLabelCatalogRequest) (serverapi.WorkflowProjectLabelCatalogResponse, error) {
	r.catalogRequests = append(r.catalogRequests, req)
	return r.catalogResponse, nil
}

func (r *taskListCommandRemote) ListWorkflowTasks(_ context.Context, req serverapi.WorkflowTaskListRequest) (serverapi.WorkflowTaskListResponse, error) {
	r.listRequests = append(r.listRequests, req)
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskListResponse{}, err
	}
	if r.listErr != nil {
		return serverapi.WorkflowTaskListResponse{}, r.listErr
	}
	return r.listResponse, nil
}

func (r *taskListCommandRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, nil
}

func (r *taskListCommandRemote) Close() error {
	return nil
}

func newTaskListCommandRemote() *taskListCommandRemote {
	return &taskListCommandRemote{
		catalogResponse: serverapi.WorkflowProjectLabelCatalogResponse{
			Catalog: serverapi.WorkflowProjectLabelCatalog{
				ProjectID: taskListCommandTestProjectID,
				Labels: []serverapi.WorkflowProjectLabel{
					{ID: taskListCommandAlphaID, Name: "Alpha"},
					{ID: taskListCommandBetaID, Name: "Beta"},
					{ID: taskListCommandBangID, Name: "!literal"},
				},
			},
		},
		listResponse: serverapi.WorkflowTaskListResponse{
			Scope: serverapi.WorkflowTaskListScope{
				ProjectID: taskListCommandTestProjectID,
			},
			MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityNone,
			Tasks:                       []serverapi.WorkflowTaskListItem{},
		},
	}
}

func TestTaskListUsesNumericOffsetsAndWritesStructuredContinuation(t *testing.T) {
	nextOffset := 7
	remote := newTaskListCommandRemote()
	remote.listResponse = serverapi.WorkflowTaskListResponse{
		Scope:                       serverapi.WorkflowTaskListScope{ProjectID: taskListCommandTestProjectID},
		MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne,
		NextOffset:                  &nextOffset,
		Tasks: []serverapi.WorkflowTaskListItem{{
			TaskID:     "task-1",
			ShortID:    "KENT-1",
			WorkflowID: "workflow-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			Title:      "Task",
			Status: serverapi.WorkflowTaskStatus{
				Kind:        serverapi.WorkflowTaskStatusKindBacklog,
				NativeState: serverapi.WorkflowTaskNativeStateActive,
			},
		}},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := taskSubcommand(
		[]string{"list", "--project", taskListCommandTestProjectID, "--offset", "5", "--limit", "2", "--json"},
		&stdout,
		&stderr,
	)

	if exitCode != 0 {
		t.Fatalf("exit code = %d; stderr=%q", exitCode, stderr.String())
	}
	if len(remote.listRequests) != 1 ||
		remote.listRequests[0].Offset == nil || *remote.listRequests[0].Offset != 5 ||
		remote.listRequests[0].Limit == nil || *remote.listRequests[0].Limit != 2 {
		t.Fatalf("list requests = %+v", remote.listRequests)
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
	if stderr.Len() != 0 {
		t.Fatalf("JSON stderr = %q, want no human continuation", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	exitCode = taskSubcommand(
		[]string{"list", "--project", taskListCommandTestProjectID, "--offset", "5", "--limit", "2"},
		&stdout,
		&stderr,
	)
	if exitCode != 0 {
		t.Fatalf("human-readable exit code = %d; stderr=%q", exitCode, stderr.String())
	}
	if stderr.Len() == 0 {
		t.Fatal("continuation was not written to stderr")
	}
	if stdout.Len() == 0 {
		t.Fatal("task rows were not written to stdout")
	}
}

func TestTaskListReturnsFailureForInvalidOffsetWindow(t *testing.T) {
	for _, args := range [][]string{
		{"list", "--project", taskListCommandTestProjectID, "--offset", "-1"},
		{"list", "--project", taskListCommandTestProjectID, "--limit", "0"},
		{"list", "--project", taskListCommandTestProjectID, "--limit", "101"},
	} {
		t.Run(args[len(args)-2], func(t *testing.T) {
			remote := newTaskListCommandRemote()
			installWorkflowCommandRemote(t, remote)

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := taskSubcommand(args, &stdout, &stderr)

			if exitCode != 1 {
				t.Fatalf("exit code = %d; stderr=%q", exitCode, stderr.String())
			}
			if stdout.Len() != 0 || stderr.Len() == 0 || len(remote.listRequests) != 1 {
				t.Fatalf("stdout=%q stderr=%q requests=%+v", stdout.String(), stderr.String(), remote.listRequests)
			}
		})
	}
}

func TestTaskListRejectsRemovedPaginationFlags(t *testing.T) {
	for _, args := range [][]string{
		{"list", "--page-token", "legacy"},
		{"list", "--page-size", "1"},
	} {
		t.Run(args[1], func(t *testing.T) {
			remote := newTaskListCommandRemote()
			installWorkflowCommandRemote(t, remote)

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := taskSubcommand(args, &stdout, &stderr)

			if exitCode != 2 {
				t.Fatalf("exit code = %d; stderr=%q", exitCode, stderr.String())
			}
			if len(remote.listRequests) != 0 || stdout.Len() != 0 || stderr.Len() == 0 {
				t.Fatalf("requests=%+v stdout=%q stderr=%q", remote.listRequests, stdout.String(), stderr.String())
			}
		})
	}
}

func TestTaskListBuildsNamedFilterWithBothSelectorPolarities(t *testing.T) {
	for _, tt := range []struct {
		name            string
		args            []string
		wantMode        serverapi.WorkflowTaskNamedLabelFilterMode
		wantLabelIDs    []string
		wantExcludedIDs []string
	}{
		{
			name:         "included default any",
			args:         []string{"--label", "Alpha"},
			wantMode:     serverapi.WorkflowTaskNamedLabelFilterModeAny,
			wantLabelIDs: []string{taskListCommandAlphaID},
		},
		{
			name: "excluded explicit all with literal selector and duplicates",
			args: []string{
				"--not-label", "!literal",
				"--not-label", "Beta",
				"--not-label", taskListCommandBetaID,
				"--label-match", "all",
			},
			wantMode:        serverapi.WorkflowTaskNamedLabelFilterModeAll,
			wantExcludedIDs: []string{taskListCommandBangID, taskListCommandBetaID},
		},
		{
			name: "mixed conditions deduplicate each polarity independently",
			args: []string{
				"--label", "Alpha",
				"--label", taskListCommandAlphaID,
				"--not-label", "Beta",
			},
			wantMode:        serverapi.WorkflowTaskNamedLabelFilterModeAny,
			wantLabelIDs:    []string{taskListCommandAlphaID},
			wantExcludedIDs: []string{taskListCommandBetaID},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			remote := newTaskListCommandRemote()
			installWorkflowCommandRemote(t, remote)

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := taskSubcommand(
				append([]string{"list", "--project", taskListCommandTestProjectID}, tt.args...),
				&stdout,
				&stderr,
			)
			if exitCode != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%q", exitCode, stderr.String())
			}
			if len(remote.catalogRequests) != 1 || remote.catalogRequests[0].ProjectID != taskListCommandTestProjectID {
				t.Fatalf("catalog requests = %+v, want one project-scoped request", remote.catalogRequests)
			}
			if len(remote.listRequests) != 1 {
				t.Fatalf("task list requests = %+v, want one", remote.listRequests)
			}
			filter := remote.listRequests[0].LabelFilter
			if filter.Kind != serverapi.WorkflowTaskLabelFilterKindNamed || filter.Named == nil {
				t.Fatalf("label filter = %+v, want named filter", filter)
			}
			if filter.Named.Mode != tt.wantMode ||
				!slices.Equal(filter.Named.LabelIDs, tt.wantLabelIDs) ||
				!slices.Equal(filter.Named.ExcludedLabelIDs, tt.wantExcludedIDs) {
				t.Fatalf(
					"named filter = %+v, want mode %q included %v excluded %v",
					filter.Named,
					tt.wantMode,
					tt.wantLabelIDs,
					tt.wantExcludedIDs,
				)
			}
		})
	}
}

func TestResolveWorkflowProjectLabelFilterAggregatesUnresolvedSelectorsAcrossPolarities(t *testing.T) {
	remote := newTaskListCommandRemote()
	_, snapshot, err := loadWorkflowProjectLabelCatalog(t.Context(), remote, taskListCommandTestProjectID)
	if err != nil {
		t.Fatalf("loadWorkflowProjectLabelCatalog: %v", err)
	}
	_, err = resolveWorkflowProjectLabelFilter(
		snapshot,
		serverapi.WorkflowTaskNamedLabelFilterModeAny,
		[]string{"missing-include"},
		[]string{"missing-exclude"},
	)
	var unresolved unresolvedWorkflowProjectLabelSelectorsError
	if !errors.As(err, &unresolved) ||
		!slices.Equal(unresolved.Selectors, []string{"missing-include", "missing-exclude"}) {
		t.Fatalf("filter selector error = %T %+v", err, err)
	}
}

func TestTaskListRejectsInvalidNamedFilterFlagCombinations(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
	}{
		{
			name: "unlabeled and included selector",
			args: []string{"--unlabeled", "--label", "Alpha"},
		},
		{
			name: "unlabeled and excluded selector",
			args: []string{"--unlabeled", "--not-label", "Beta"},
		},
		{
			name: "unlabeled and explicit match mode",
			args: []string{"--unlabeled", "--label-match", "all"},
		},
		{
			name: "explicit match mode without either selector",
			args: []string{"--label-match", "all"},
		},
		{
			name: "same label in both polarities",
			args: []string{"--label", "Alpha", "--not-label", taskListCommandAlphaID},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			remote := newTaskListCommandRemote()
			installWorkflowCommandRemote(t, remote)

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := taskSubcommand(
				append([]string{"list", "--project", taskListCommandTestProjectID}, tt.args...),
				&stdout,
				&stderr,
			)
			if exitCode != 2 && exitCode != 1 {
				t.Fatalf("exit code = %d, want a rejected command; stderr=%q", exitCode, stderr.String())
			}
			if len(remote.listRequests) != 0 {
				t.Fatalf("task list requests = %+v, want none", remote.listRequests)
			}
		})
	}
}

func TestResolveWorkflowProjectLabelFilterReportsConflictingSelectors(t *testing.T) {
	remote := newTaskListCommandRemote()
	_, snapshot, err := loadWorkflowProjectLabelCatalog(t.Context(), remote, taskListCommandTestProjectID)
	if err != nil {
		t.Fatalf("loadWorkflowProjectLabelCatalog: %v", err)
	}
	_, err = resolveWorkflowProjectLabelFilter(
		snapshot,
		serverapi.WorkflowTaskNamedLabelFilterModeAny,
		[]string{"Alpha"},
		[]string{taskListCommandAlphaID},
	)
	var conflict conflictingWorkflowProjectLabelSelectorsError
	if !errors.As(err, &conflict) {
		t.Fatalf("resolver error = %T %v, want conflicting selector error", err, err)
	}
	if conflict.Included != "Alpha" || conflict.Excluded != taskListCommandAlphaID {
		t.Fatalf("selector conflict = %+v", conflict)
	}
}

func TestTaskListRetryCommandRetainsBothSelectorPolarities(t *testing.T) {
	mode := serverapi.WorkflowTaskNamedLabelFilterModeAll
	args := taskListRetryCommandArgs(taskListCommandContext{
		ProjectRef:             "project-ref",
		LabelSelectors:         []string{"Alpha"},
		ExcludedLabelSelectors: []string{"Beta", "!literal"},
		LabelMatch:             &mode,
		Limit:                  100,
	}, nil)
	want := []string{
		config.Command, "task", "list", "--project", "project-ref",
		"--label", "Alpha",
		"--not-label", "Beta",
		"--not-label", "!literal",
		"--label-match", "all",
		"--limit", "100",
	}
	if !slices.Equal(args, want) {
		t.Fatalf("retry args = %v, want %v", args, want)
	}
}

func TestTaskListRetryCommandRendersWorkflowPlaceholderAtSelectorBoundary(t *testing.T) {
	args := taskListRetryCommandArgsForSelector(
		taskListCommandContext{ProjectRef: "project-ref", PageSize: 100},
		taskWorkflowRetryWorkflowPlaceholder{},
		false,
	)
	want := []string{
		config.Command, "task", "list", "--project", "project-ref",
		"--workflow", "<uuid>",
		"--page-size", "100",
	}
	if !slices.Equal(args, want) {
		t.Fatalf("retry args = %v, want %v", args, want)
	}
}
