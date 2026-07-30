package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"core/shared/apicontract"
	"core/shared/config"
	"core/shared/serverapi"
)

type taskSearchCommandRemote struct {
	apicontract.WorkflowService

	requests []serverapi.TaskSearchRequest
	response serverapi.TaskSearchResponse
	err      error
	resolve  func(serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error)
}

func (r *taskSearchCommandRemote) SearchWorkflowTasks(_ context.Context, request serverapi.TaskSearchRequest) (serverapi.TaskSearchResponse, error) {
	r.requests = append(r.requests, request)
	return r.response, r.err
}

func (r *taskSearchCommandRemote) ResolveProjectPath(_ context.Context, request serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	if r.resolve != nil {
		return r.resolve(request)
	}
	return serverapi.ProjectResolvePathResponse{}, nil
}

func (r *taskSearchCommandRemote) Close() error {
	return nil
}

func TestTaskSearchBuildsCanonicalRequestAndPassesJSONResponseThrough(t *testing.T) {
	nextPageToken := "opaque-next-page"
	response := taskSearchTestResponse(serverapi.TaskSearchModeFTS5)
	response.NextPageToken = &nextPageToken
	remote := &taskSearchCommandRemote{response: response}
	installWorkflowCommandRemote(t, remote)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := taskSubcommand(
		[]string{
			"search", "  title:needle  ",
			"--fts5",
			"--include-comments",
			"--context", "7",
			"--status", "done,backlog",
			"--status", "active",
			"--project", "project-b",
			"--project", "project-a",
			"--project", "project-b",
			"--page-size", "2",
			"--page-token", "opaque-token",
			"--json",
		},
		&stdout,
		&stderr,
	)
	if exitCode != 0 {
		t.Fatalf("exit code = %d; stderr=%q", exitCode, stderr.String())
	}
	wantRequest := serverapi.TaskSearchRequest{
		Mode:            serverapi.TaskSearchModeFTS5,
		Query:           "title:needle",
		Context:         7,
		IncludeComments: true,
		ProjectIDs:      []string{"project-a", "project-b"},
		StatusKinds: []serverapi.WorkflowTaskStatusKind{
			serverapi.WorkflowTaskStatusKindActive,
			serverapi.WorkflowTaskStatusKindBacklog,
			serverapi.WorkflowTaskStatusKindDone,
		},
		PageSize:  2,
		PageToken: pointerTo("opaque-token"),
	}
	if !reflect.DeepEqual(remote.requests, []serverapi.TaskSearchRequest{wantRequest}) {
		t.Fatalf("search requests = %#v, want %#v", remote.requests, []serverapi.TaskSearchRequest{wantRequest})
	}
	var output serverapi.TaskSearchResponse
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode JSON output: %v", err)
	}
	if !reflect.DeepEqual(output, response) {
		t.Fatalf("JSON output = %#v, want %#v", output, response)
	}
}

func TestTaskSearchUsesLiteralDefaultsAndTrimsOnlyQueryEdges(t *testing.T) {
	nextPageToken := "opaque-next-page"
	response := taskSearchTestResponse(serverapi.TaskSearchModeLiteral)
	response.NextPageToken = &nextPageToken
	remote := &taskSearchCommandRemote{response: response}
	installWorkflowCommandRemote(t, remote)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := taskSubcommand(
		[]string{"search", "\u2003needle\tinside\u2002"},
		&stdout,
		&stderr,
	)
	if exitCode != 0 {
		t.Fatalf("exit code = %d; stderr=%q", exitCode, stderr.String())
	}
	wantRequest := serverapi.TaskSearchRequest{
		Mode:        serverapi.TaskSearchModeLiteral,
		Query:       "needle\tinside",
		Context:     serverapi.TaskSearchDefaultContext,
		StatusKinds: []serverapi.WorkflowTaskStatusKind{},
		PageSize:    serverapi.TaskSearchDefaultPageSize,
	}
	if !reflect.DeepEqual(remote.requests, []serverapi.TaskSearchRequest{wantRequest}) {
		t.Fatalf("search requests = %#v, want %#v", remote.requests, []serverapi.TaskSearchRequest{wantRequest})
	}
	if stderr.String() != nextPageTokenLine(nextPageToken)+"\n" {
		t.Fatalf("next-page diagnostic = %q", stderr.String())
	}
}

func TestTaskSearchProjectResolutionIsAllOrNothing(t *testing.T) {
	remote := &taskSearchCommandRemote{
		response: taskSearchTestResponse(serverapi.TaskSearchModeLiteral),
		resolve: func(serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
			return serverapi.ProjectResolvePathResponse{}, nil
		},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := taskSubcommand(
		[]string{"search", "needle", "--project", t.TempDir()},
		&stdout,
		&stderr,
	)
	if exitCode != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%q", exitCode, stderr.String())
	}
	if len(remote.requests) != 0 {
		t.Fatalf("search requests = %#v, want no partially scoped search", remote.requests)
	}
	if stdout.Len() != 0 {
		t.Fatalf("unresolved project wrote stdout: %q", stdout.String())
	}
}

func TestTaskSearchRejectsInvalidArityBeforeOpeningRemote(t *testing.T) {
	previous := workflowCommandRemoteOpener
	workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
		return config.App{}, nil, errors.New("remote should not open")
	}
	t.Cleanup(func() {
		workflowCommandRemoteOpener = previous
	})

	for _, args := range [][]string{
		{"search"},
		{"search", "needle", "extra"},
	} {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		exitCode := taskSubcommand(args, &stdout, &stderr)
		if exitCode != 2 {
			t.Fatalf("args %q exit code = %d, want 2; stderr=%q", args, exitCode, stderr.String())
		}
	}
}

func TestTaskSearchMapsTypedSearchFailuresToSpecifiedExitClasses(t *testing.T) {
	for _, test := range []struct {
		name     string
		err      error
		exitCode int
	}{
		{
			name:     "normalized too short is a query validation failure",
			err:      &serverapi.TaskSearchError{Reason: serverapi.TaskSearchErrorReasonNormalizedTooShort},
			exitCode: 2,
		},
		{
			name:     "raw FTS5 SQLite failure remains operational",
			err:      errors.New("task search FTS5 query could not be evaluated"),
			exitCode: 1,
		},
		{
			name:     "invalid cursor remains operational",
			err:      &serverapi.TaskSearchError{Reason: serverapi.TaskSearchErrorReasonInvalidCursor},
			exitCode: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			remote := &taskSearchCommandRemote{
				response: taskSearchTestResponse(serverapi.TaskSearchModeLiteral),
				err:      test.err,
			}
			installWorkflowCommandRemote(t, remote)

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := taskSubcommand([]string{"search", "needle"}, &stdout, &stderr)
			if exitCode != test.exitCode {
				t.Fatalf("exit code = %d, want %d; stderr=%q", exitCode, test.exitCode, stderr.String())
			}
			if stderr.Len() == 0 {
				t.Fatal("search error did not produce diagnostics")
			}
			if stdout.Len() != 0 {
				t.Fatalf("search error wrote stdout: %q", stdout.String())
			}
		})
	}
}

func TestTaskSearchRejectsInvalidFlagCombinationBeforeOpeningRemote(t *testing.T) {
	previous := workflowCommandRemoteOpener
	workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
		return config.App{}, nil, errors.New("remote should not open")
	}
	t.Cleanup(func() {
		workflowCommandRemoteOpener = previous
	})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := taskSubcommand(
		[]string{"search", "needle", "--fts5", "--case-sensitive"},
		&stdout,
		&stderr,
	)
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%q", exitCode, stderr.String())
	}
}

func taskSearchTestResponse(mode serverapi.TaskSearchMode) serverapi.TaskSearchResponse {
	hit := serverapi.TaskSearchHit{
		Ordinal: 1,
		Source:  serverapi.TaskSearchSource{Kind: serverapi.TaskSearchSourceKindTitle},
	}
	if mode == serverapi.TaskSearchModeLiteral {
		hit.Literal = &serverapi.TaskSearchLiteralHit{Match: "needle"}
	} else {
		hit.FTS5 = &serverapi.TaskSearchFTS5Hit{Snippet: "needle"}
	}
	return serverapi.TaskSearchResponse{
		Mode: mode,
		Groups: []serverapi.TaskSearchGroup{{
			ProjectID:  "project-1",
			ProjectKey: "KNT",
			TaskID:     "task-1",
			ShortID:    "KNT-1",
			WorkflowID: "workflow-1",
			Title:      "Task",
			Status: serverapi.WorkflowTaskStatus{
				Kind:        serverapi.WorkflowTaskStatusKindBacklog,
				NativeState: serverapi.WorkflowTaskNativeStateActive,
			},
			TotalHitCount: 1,
			Hits:          []serverapi.TaskSearchHit{hit},
		}},
	}
}

func pointerTo(value string) *string {
	return &value
}
