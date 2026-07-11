package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"core/shared/config"
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
	ShortID         string   `json:"short_id"`
	TaskID          string   `json:"task_id"`
	WorkflowID      string   `json:"workflow_id"`
	Status          string   `json:"status"`
	StatusKeys      []string `json:"status_keys"`
	RunStatus       string   `json:"run_status"`
	Title           string   `json:"title"`
	CreatedAtUnixMs int64    `json:"created_at_unix_ms"`
	UpdatedAtUnixMs int64    `json:"updated_at_unix_ms"`
	RunCount        int      `json:"run_count"`
}

func taskListSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" task list", stderr, taskCommandUsage)
	projectRef := fs.String("project", ".", "project id or path")
	pageSize := fs.Int("page-size", taskListDefaultPageSize, "maximum tasks to print")
	pageToken := fs.String("page-token", "", "page token from a previous task list response")
	var statusFlags repeatedStringFlag
	var columnFlags repeatedStringFlag
	var runStatusFlags repeatedStringFlag
	var sortFlags repeatedStringFlag
	fs.Var(&statusFlags, "status", "workflow status key filter; comma-separated or repeatable")
	fs.Var(&columnFlags, "column", "alias for --status")
	fs.Var(&runStatusFlags, "run-status", "run status filter: open|running|interrupted|done|canceled; comma-separated or repeatable")
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
	statusKeys, err := parseTaskListFilterValues(append([]string(statusFlags), []string(columnFlags)...), "status")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	runStatuses, err := parseTaskListRunStatuses([]string(runStatusFlags))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	sortSelectors, err := parseTaskListSortSelectors([]string(sortFlags))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(sortSelectors) == 0 {
		sortSelectors = defaultTaskListSortSelectors()
	}
	cfg, remote, err := workflowCommandRemoteOpener(context.Background(), ".")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	resp, err := workflowTaskListForProject(context.Background(), cfg, remote, *projectRef, serverapi.WorkflowTaskListRequest{
		StatusKeys:  statusKeys,
		RunStatuses: runStatuses,
		Sort:        sortSelectors,
		PageSize:    *pageSize,
		PageToken:   *pageToken,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	items := taskListItemsFromResponse(resp.Tasks)
	if *jsonOut {
		if err := json.NewEncoder(stdout).Encode(taskListOutput{ProjectID: resp.ProjectID, WorkflowID: resp.WorkflowID, NextPageToken: resp.NextPageToken, Tasks: items}); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	for _, item := range items {
		fmt.Fprintf(stdout, "%s: %s.\nStatus: %s\nRun status: %s\n", item.ShortID, item.Title, taskListStatusKeysText(item.StatusKeys), item.RunStatus)
	}
	if strings.TrimSpace(resp.NextPageToken) != "" {
		fmt.Fprintf(stderr, "Next page token: `%s`\n", resp.NextPageToken)
	}
	return 0
}

func taskListStatusKeysText(statusKeys []string) string {
	if len(statusKeys) == 0 {
		return "(none)"
	}
	return strings.Join(statusKeys, ", ")
}

func defaultTaskListSortSelectors() []serverapi.WorkflowTaskListSort {
	return []serverapi.WorkflowTaskListSort{
		{Field: serverapi.WorkflowTaskListSortFieldStatus, Direction: serverapi.WorkflowTaskListSortDirectionAsc},
		{Field: serverapi.WorkflowTaskListSortFieldUpdated, Direction: serverapi.WorkflowTaskListSortDirectionDesc},
	}
}

func taskListItemsFromResponse(tasks []serverapi.WorkflowTaskListItem) []taskListItem {
	items := make([]taskListItem, 0, len(tasks))
	for _, task := range tasks {
		runStatus := string(task.RunStatus)
		items = append(items, taskListItem{
			ShortID:         task.ShortID,
			TaskID:          task.TaskID,
			WorkflowID:      task.WorkflowID,
			Status:          runStatus,
			StatusKeys:      append([]string(nil), task.StatusKeys...),
			RunStatus:       runStatus,
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

func parseTaskListRunStatuses(raw []string) ([]serverapi.WorkflowTaskRunStatus, error) {
	values, err := parseTaskListFilterValues(raw, "run-status")
	if err != nil {
		return nil, err
	}
	statuses := make([]serverapi.WorkflowTaskRunStatus, 0, len(values))
	for _, value := range values {
		status := serverapi.WorkflowTaskRunStatus(value)
		switch status {
		case serverapi.WorkflowTaskRunStatusOpen, serverapi.WorkflowTaskRunStatusRunning, serverapi.WorkflowTaskRunStatusInterrupted, serverapi.WorkflowTaskRunStatusDone, serverapi.WorkflowTaskRunStatusCanceled:
			statuses = append(statuses, status)
		default:
			return nil, fmt.Errorf("--run-status must be open, running, interrupted, done, or canceled")
		}
	}
	return statuses, nil
}

func parseTaskListSortSelectors(raw []string) ([]serverapi.WorkflowTaskListSort, error) {
	values, err := parseTaskListFilterValues(raw, "sort")
	if err != nil {
		return nil, err
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
	case "run_count":
		return serverapi.WorkflowTaskListSortFieldRunCount, nil
	case "title":
		return serverapi.WorkflowTaskListSortFieldTitle, nil
	default:
		return "", fmt.Errorf("--sort field must be created, updated, status, run_count, or title")
	}
}
