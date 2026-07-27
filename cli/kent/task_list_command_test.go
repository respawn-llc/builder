package main

import (
	"bytes"
	"context"
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
}

func (r *taskListCommandRemote) ListWorkflowProjectLabels(_ context.Context, req serverapi.WorkflowProjectLabelCatalogRequest) (serverapi.WorkflowProjectLabelCatalogResponse, error) {
	r.catalogRequests = append(r.catalogRequests, req)
	return r.catalogResponse, nil
}

func (r *taskListCommandRemote) ListWorkflowTasks(_ context.Context, req serverapi.WorkflowTaskListRequest) (serverapi.WorkflowTaskListResponse, error) {
	r.listRequests = append(r.listRequests, req)
	return serverapi.WorkflowTaskListResponse{
		Scope: serverapi.WorkflowTaskListScope{
			ProjectID: taskListCommandTestProjectID,
		},
		MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityNone,
		Tasks:                       []serverapi.WorkflowTaskListItem{},
	}, nil
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
		PageSize:               100,
	}, nil, false)
	want := []string{
		config.Command, "task", "list", "--project", "project-ref",
		"--label", "Alpha",
		"--not-label", "Beta",
		"--not-label", "!literal",
		"--label-match", "all",
		"--page-size", "100",
	}
	if !slices.Equal(args, want) {
		t.Fatalf("retry args = %v, want %v", args, want)
	}
}
