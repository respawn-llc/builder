package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"core/shared/config"
	"core/shared/serverapi"
)

func taskSearchSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" task search", stderr, taskSearchUsage)
	fts5 := fs.Bool("fts5", false, "interpret the query as a raw FTS5 expression")
	caseSensitive := fs.Bool("case-sensitive", false, "require exact code points in literal search")
	contextSize := fs.Int("context", serverapi.TaskSearchDefaultContext, "context size for each matching source")
	includeComments := fs.Bool("include-comments", false, "include Task Comments in the search")
	pageSize := fs.Int("page-size", serverapi.TaskSearchDefaultPageSize, "maximum hits to print")
	pageToken := fs.String("page-token", "", "page token from a previous task search response")
	jsonOut := fs.Bool("json", false, "print machine-readable JSON")
	var projectFlags repeatedStringFlag
	var statusFlags repeatedStringFlag
	fs.Var(&projectFlags, "project", "project ID or registered workspace path; repeat for multiple projects")
	fs.Var(&statusFlags, "status", "task status filter; comma-separated or repeatable")

	positionals, flagArgs := takeLeadingPositionals(args, 1)
	if ok, exitCode := parseCommandFlags(fs, flagArgs); !ok {
		return exitCode
	}
	positionals = append(positionals, fs.Args()...)
	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "task search requires exactly one <query>")
		return 2
	}
	statusKinds, err := parseTaskSearchStatusKinds([]string(statusFlags))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	mode := serverapi.TaskSearchModeLiteral
	if *fts5 {
		mode = serverapi.TaskSearchModeFTS5
	}
	request := serverapi.TaskSearchRequest{
		Mode:            mode,
		Query:           strings.TrimSpace(positionals[0]),
		Context:         *contextSize,
		CaseSensitive:   *caseSensitive,
		IncludeComments: *includeComments,
		StatusKinds:     statusKinds,
		PageSize:        *pageSize,
	}
	if flagWasProvided(fs, "page-token") {
		request.PageToken = pageToken
	}
	if err := validateTaskSearchCommandRequest(request); err != nil {
		writeTaskSearchError(stderr, err)
		return 2
	}
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote workflowCommandRemote) int {
		projectIDs, err := resolveTaskSearchProjectIDs(context.Background(), cfg, remote, []string(projectFlags))
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		request.ProjectIDs = projectIDs
		response, err := searchWorkflowTasks(context.Background(), remote, request)
		if err != nil {
			return writeTaskSearchError(stderr, err)
		}
		return writeTaskSearchResponse(stdout, stderr, response, *jsonOut)
	})
}

func validateTaskSearchCommandRequest(request serverapi.TaskSearchRequest) error {
	pageToken := request.PageToken
	request.PageToken = nil
	if err := request.Validate(); err != nil {
		return err
	}
	request.PageToken = pageToken
	return nil
}

func resolveTaskSearchProjectIDs(ctx context.Context, cfg config.App, remote workflowCommandRemote, refs []string) ([]string, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		projectID, err := resolveWorkflowProjectID(ctx, cfg, remote, ref)
		if err != nil {
			return nil, err
		}
		ids = append(ids, projectID)
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}

func parseTaskSearchStatusKinds(raw []string) ([]serverapi.WorkflowTaskStatusKind, error) {
	statuses, err := parseTaskListStatusKinds(raw)
	if err != nil {
		return nil, err
	}
	slices.Sort(statuses)
	return statuses, nil
}

func searchWorkflowTasks(ctx context.Context, remote workflowCommandRemote, request serverapi.TaskSearchRequest) (serverapi.TaskSearchResponse, error) {
	rpcCtx, cancel := context.WithTimeout(ctx, workflowCommandTimeout)
	defer cancel()
	return remote.SearchWorkflowTasks(rpcCtx, request)
}

func writeTaskSearchResponse(stdout io.Writer, stderr io.Writer, response serverapi.TaskSearchResponse, jsonOut bool) int {
	if err := response.Validate(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if jsonOut {
		return writeCommandJSON(stdout, stderr, response)
	}
	projection, err := taskSearchPlainProjectionFromResponse(response)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := writeTaskSearchPlainProjection(stdout, projection); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if response.NextPageToken != nil {
		fmt.Fprintf(stderr, "Next page token: `%s`\n", *response.NextPageToken)
	}
	return 0
}

func writeTaskSearchError(stderr io.Writer, err error) int {
	var searchErr *serverapi.TaskSearchError
	if errors.As(err, &searchErr) {
		switch searchErr.Reason {
		case serverapi.TaskSearchErrorReasonNormalizedTooShort:
			fmt.Fprintln(stderr, "task search query is too short after normalization")
			return 2
		case serverapi.TaskSearchErrorReasonMalformedFTS5:
			fmt.Fprintln(stderr, "task search FTS5 expression is malformed")
			return 2
		case serverapi.TaskSearchErrorReasonInvalidCursor:
			fmt.Fprintln(stderr, "task search page token is invalid")
			return 1
		}
	}
	fmt.Fprintln(stderr, err)
	return 1
}
