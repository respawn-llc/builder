package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

const taskListDefaultPageSize = 100

type taskListOutput struct {
	ProjectID     string         `json:"project_id"`
	WorkflowID    string         `json:"workflow_id"`
	NextPageToken string         `json:"next_page_token,omitempty"`
	Tasks         []taskListItem `json:"tasks"`
}

type taskListItem struct {
	ShortID         string                       `json:"short_id"`
	TaskID          string                       `json:"task_id"`
	WorkflowID      string                       `json:"workflow_id"`
	Status          serverapi.WorkflowTaskStatus `json:"status"`
	ColumnKeys      []string                     `json:"column_keys"`
	Title           string                       `json:"title"`
	CreatedAtUnixMs int64                        `json:"created_at_unix_ms"`
	UpdatedAtUnixMs int64                        `json:"updated_at_unix_ms"`
	RunCount        int                          `json:"run_count"`
}

func taskListSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" task list", stderr, taskListUsage)
	projectRef := fs.String("project", ".", "project id or path")
	workflowID := fs.String("workflow", "", "workflow UUID")
	pageSize := fs.Int("page-size", taskListDefaultPageSize, "maximum tasks to print")
	pageToken := fs.String("page-token", "", "page token from a previous task list response")
	var statusFlags repeatedStringFlag
	var columnFlags repeatedStringFlag
	var attentionFlags repeatedStringFlag
	var sortFlags repeatedStringFlag
	fs.Var(&statusFlags, "status", "task status filter; comma-separated or repeatable")
	fs.Var(&columnFlags, "column", "workflow column key filter; comma-separated or repeatable")
	fs.Var(&attentionFlags, "attention", "task attention filter; comma-separated or repeatable")
	fs.Var(&sortFlags, "sort", "sort selectors such as status:asc,updated:desc")
	jsonOut := fs.Bool("json", false, "print machine-readable JSON")
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(stderr, "task list does not accept positional arguments")
		return 2
	}
	if *pageSize < 1 {
		fmt.Fprintln(stderr, "task list requires --page-size to be positive")
		return 2
	}
	columnKeys, err := parseTaskListFilterValues([]string(columnFlags), "column")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	statusKinds, err := parseTaskListStatusKinds([]string(statusFlags))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	attentionKinds, err := parseTaskListAttentionKinds([]string(attentionFlags))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	sortSelectors, err := parseTaskListSortSelectors([]string(sortFlags))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	projectProvided := flagWasProvided(fs, "project")
	workflowProvided := flagWasProvided(fs, "workflow")
	var selectedWorkflowID *string
	if workflowProvided {
		selectedWorkflowID, err = workflowPointer(*workflowID)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
	}
	cfg, remote, err := workflowCommandRemoteOpener(context.Background(), ".")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	request := serverapi.WorkflowTaskListRequest{
		WorkflowID:     selectedWorkflowID,
		ColumnKeys:     columnKeys,
		StatusKinds:    statusKinds,
		AttentionKinds: attentionKinds,
		Sort:           sortSelectors,
		PageSize:       *pageSize,
		PageToken:      *pageToken,
	}
	var resp serverapi.WorkflowTaskListResponse
	if projectProvided || (!workflowProvided && strings.TrimSpace(*pageToken) == "") {
		resp, err = workflowTaskListForProject(context.Background(), cfg, remote, *projectRef, request)
	} else {
		resp, err = workflowTaskList(context.Background(), remote, request)
	}
	if err != nil {
		writeTaskListError(stderr, err)
		return 1
	}
	return writeTaskListResponse(stdout, stderr, resp, *jsonOut)
}

func writeTaskListResponse(stdout io.Writer, stderr io.Writer, resp serverapi.WorkflowTaskListResponse, jsonOut bool) int {
	items := taskListItemsFromResponse(resp.Tasks)
	if jsonOut {
		if err := json.NewEncoder(stdout).Encode(taskListOutput{ProjectID: resp.ProjectID, WorkflowID: resp.WorkflowID, NextPageToken: resp.NextPageToken, Tasks: items}); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	for _, item := range items {
		statusText, err := taskStatusText(item.Status)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "%s: %s.\nStatus: %s\nColumns: %s\n", item.ShortID, item.Title, statusText, taskListColumnKeysText(item.ColumnKeys))
	}
	if strings.TrimSpace(resp.NextPageToken) != "" {
		fmt.Fprintf(stderr, "Next page token: `%s`\n", resp.NextPageToken)
	}
	return 0
}

func writeTaskListError(stderr io.Writer, err error) {
	var scopeErr *serverapi.WorkflowTaskListScopeError
	if !errors.As(err, &scopeErr) {
		fmt.Fprintln(stderr, err)
		return
	}
	switch scopeErr.Kind {
	case serverapi.WorkflowTaskListScopeErrorKindAmbiguous:
		fmt.Fprint(stderr, "Task list scope is ambiguous.")
	case serverapi.WorkflowTaskListScopeErrorKindNotLinked:
		fmt.Fprint(stderr, "Task list scope has no active project/workflow link.")
	default:
		fmt.Fprintln(stderr, err)
		return
	}
	if scopeErr.MissingScope != nil {
		switch *scopeErr.MissingScope {
		case serverapi.WorkflowTaskListScopeDimensionProject:
			fmt.Fprint(stderr, " Specify --project <project>.")
		case serverapi.WorkflowTaskListScopeDimensionWorkflow:
			fmt.Fprint(stderr, " Specify --workflow <uuid>.")
		}
	}
	if len(scopeErr.ProjectIDs) > 0 {
		fmt.Fprintf(stderr, " Available project UUIDs: %s.", strings.Join(scopeErr.ProjectIDs, ", "))
	}
	if len(scopeErr.WorkflowIDs) > 0 {
		fmt.Fprintf(stderr, " Available workflow UUIDs: %s.", strings.Join(workflowSelectorsForDisplay(scopeErr.WorkflowIDs), ", "))
	}
	fmt.Fprintln(stderr)
}

func workflowSelectorsForDisplay(workflowIDs []string) []string {
	selectors := make([]string, 0, len(workflowIDs))
	for _, workflowID := range workflowIDs {
		selector, hasPrefix := strings.CutPrefix(workflowID, "workflow-")
		if !hasPrefix {
			selectors = append(selectors, workflowID)
			continue
		}
		parsed, err := runtimeids.ParseCanonicalUUIDv4(selector, "workflow id")
		if err != nil {
			selectors = append(selectors, workflowID)
			continue
		}
		selectors = append(selectors, parsed.String())
	}
	return selectors
}

func taskListColumnKeysText(columnKeys []string) string {
	if len(columnKeys) == 0 {
		return "(none)"
	}
	return strings.Join(columnKeys, ", ")
}

func taskListItemsFromResponse(tasks []serverapi.WorkflowTaskListItem) []taskListItem {
	items := make([]taskListItem, 0, len(tasks))
	for _, task := range tasks {
		items = append(items, taskListItem{
			ShortID:         task.ShortID,
			TaskID:          task.TaskID,
			WorkflowID:      task.WorkflowID,
			Status:          task.Status,
			ColumnKeys:      append([]string(nil), task.ColumnKeys...),
			Title:           task.Title,
			CreatedAtUnixMs: task.CreatedAtUnixMs,
			UpdatedAtUnixMs: task.UpdatedAtUnixMs,
			RunCount:        task.RunCount,
		})
	}
	return items
}

func parseTaskListFilterValues(raw []string, name string) ([]string, error) {
	values := []string{}
	seen := map[string]bool{}
	for _, entry := range raw {
		for _, part := range strings.Split(entry, ",") {
			value := strings.TrimSpace(part)
			if value == "" {
				return nil, fmt.Errorf("--%s contains a blank value", name)
			}
			if !seen[value] {
				seen[value] = true
				values = append(values, value)
			}
		}
	}
	return values, nil
}

func parseTaskListStatusKinds(raw []string) ([]serverapi.WorkflowTaskStatusKind, error) {
	values, err := parseTaskListFilterValues(raw, "status")
	if err != nil {
		return nil, err
	}
	statuses := make([]serverapi.WorkflowTaskStatusKind, 0, len(values))
	for _, value := range values {
		status := serverapi.WorkflowTaskStatusKind(value)
		switch status {
		case serverapi.WorkflowTaskStatusKindCanceled, serverapi.WorkflowTaskStatusKindDone, serverapi.WorkflowTaskStatusKindWaitingQuestion, serverapi.WorkflowTaskStatusKindWaitingApproval, serverapi.WorkflowTaskStatusKindInterrupted, serverapi.WorkflowTaskStatusKindRunning, serverapi.WorkflowTaskStatusKindQueued, serverapi.WorkflowTaskStatusKindBacklog, serverapi.WorkflowTaskStatusKindActive:
			statuses = append(statuses, status)
		default:
			return nil, fmt.Errorf("--status is invalid")
		}
	}
	return statuses, nil
}

func parseTaskListAttentionKinds(raw []string) ([]serverapi.WorkflowTaskAttentionKind, error) {
	values, err := parseTaskListFilterValues(raw, "attention")
	if err != nil {
		return nil, err
	}
	out := make([]serverapi.WorkflowTaskAttentionKind, 0, len(values))
	for _, value := range values {
		kind := serverapi.WorkflowTaskAttentionKind(value)
		switch kind {
		case serverapi.WorkflowTaskAttentionKindQuestion, serverapi.WorkflowTaskAttentionKindApproval, serverapi.WorkflowTaskAttentionKindInterrupted:
			out = append(out, kind)
		default:
			return nil, fmt.Errorf("--attention is invalid")
		}
	}
	return out, nil
}

func workflowPointer(value string) (*string, error) {
	selector, err := runtimeids.ParseCanonicalUUIDv4(value, "workflow selector")
	if err != nil {
		return nil, err
	}
	workflowID := "workflow-" + selector.String()
	return &workflowID, nil
}

func parseTaskListSortSelectors(raw []string) ([]serverapi.WorkflowTaskListSort, error) {
	values, err := parseTaskListFilterValues(raw, "sort")
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, nil
	}
	selectors := make([]serverapi.WorkflowTaskListSort, 0, len(values))
	seen := map[serverapi.WorkflowTaskListSortField]bool{}
	for _, value := range values {
		fieldValue, directionValue, ok := strings.Cut(value, ":")
		if !ok {
			return nil, fmt.Errorf("--sort selector %q must be field:direction", value)
		}
		field, err := parseTaskListSortField(strings.TrimSpace(fieldValue))
		if err != nil {
			return nil, err
		}
		if seen[field] {
			return nil, fmt.Errorf("--sort field %q must not be repeated", field)
		}
		seen[field] = true
		direction := serverapi.WorkflowTaskListSortDirection(strings.TrimSpace(directionValue))
		switch direction {
		case serverapi.WorkflowTaskListSortDirectionAsc, serverapi.WorkflowTaskListSortDirectionDesc:
		default:
			return nil, fmt.Errorf("--sort direction must be asc or desc")
		}
		selectors = append(selectors, serverapi.WorkflowTaskListSort{Field: field, Direction: direction})
	}
	return selectors, nil
}

func parseTaskListSortField(value string) (serverapi.WorkflowTaskListSortField, error) {
	switch value {
	case "created", "created_at":
		return serverapi.WorkflowTaskListSortFieldCreated, nil
	case "updated", "updated_at":
		return serverapi.WorkflowTaskListSortFieldUpdated, nil
	case "status":
		return serverapi.WorkflowTaskListSortFieldStatus, nil
	case "column":
		return serverapi.WorkflowTaskListSortFieldColumn, nil
	case "run_count":
		return serverapi.WorkflowTaskListSortFieldRunCount, nil
	case "title":
		return serverapi.WorkflowTaskListSortFieldTitle, nil
	default:
		return "", fmt.Errorf("--sort field must be created, updated, status, column, run_count, or title")
	}
}
