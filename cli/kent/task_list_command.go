package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"core/shared/config"
	"core/shared/serverapi"
)

const taskListDefaultPageSize = 100

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
	var labelFlags repeatedStringFlag
	fs.Var(&statusFlags, "status", "task status filter; comma-separated or repeatable")
	fs.Var(&columnFlags, "column", "workflow column key filter; comma-separated or repeatable")
	fs.Var(&attentionFlags, "attention", "task attention filter; comma-separated or repeatable")
	fs.Var(&sortFlags, "sort", "sort selectors such as status:asc,updated:desc")
	fs.Var(&labelFlags, "label", "label name or canonical UUIDv4; repeat for multiple labels")
	labelMatchRaw := fs.String("label-match", string(serverapi.WorkflowTaskNamedLabelFilterModeAny), "label match mode: any or all")
	unlabeled := fs.Bool("unlabeled", false, "only include tasks without labels")
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
	labelMatchExplicit := flagWasProvided(fs, "label-match")
	labelMatch, err := parseTaskListLabelMatch(*labelMatchRaw, labelMatchExplicit, len(labelFlags), *unlabeled)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	workflowProvided := flagWasProvided(fs, "workflow")
	var selectedWorkflowID *string
	var selectedWorkflowSelector *string
	if workflowProvided {
		selector, parseErr := parseWorkflowSelector(*workflowID)
		err = parseErr
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		persistedID := selector.PersistedID()
		selectedWorkflowID = &persistedID
		selectorValue := selector.String()
		selectedWorkflowSelector = &selectorValue
	}
	var recoveryLabelMatch *serverapi.WorkflowTaskNamedLabelFilterMode
	if labelMatchExplicit {
		value := labelMatch
		recoveryLabelMatch = &value
	}
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote workflowCommandRemote) int {
		projectID, err := resolveWorkflowProjectID(context.Background(), cfg, remote, *projectRef)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		labelFilter := serverapi.WorkflowTaskLabelFilterNone()
		if *unlabeled {
			labelFilter = serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindUnlabeled}
		} else if len(labelFlags) > 0 {
			_, snapshot, err := loadWorkflowProjectLabelCatalog(context.Background(), remote, projectID)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			labelIDs, err := resolveWorkflowProjectLabelSelectors(snapshot, labelFlags)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			labelFilter = serverapi.WorkflowTaskLabelFilter{
				Kind: serverapi.WorkflowTaskLabelFilterKindNamed,
				Named: &serverapi.WorkflowTaskNamedLabelFilter{
					Mode:     labelMatch,
					LabelIDs: labelIDs,
				},
			}
		}
		request := serverapi.WorkflowTaskListRequest{
			ProjectID:      &projectID,
			WorkflowID:     selectedWorkflowID,
			LabelFilter:    labelFilter,
			ColumnKeys:     columnKeys,
			StatusKinds:    statusKinds,
			AttentionKinds: attentionKinds,
			Sort:           sortSelectors,
			PageSize:       *pageSize,
			PageToken:      *pageToken,
		}
		resp, err := workflowTaskList(context.Background(), remote, request)
		if err != nil {
			writeTaskListError(stderr, err, taskListCommandContext{
				ProjectRef:         *projectRef,
				ResolvedProjectID:  projectID,
				SelectedWorkflowID: selectedWorkflowSelector,
				ColumnKeys:         columnKeys,
				StatusKinds:        statusKinds,
				AttentionKinds:     attentionKinds,
				Sort:               sortSelectors,
				LabelSelectors:     append([]string(nil), labelFlags...),
				LabelMatch:         recoveryLabelMatch,
				Unlabeled:          *unlabeled,
				PageSize:           *pageSize,
				PageToken:          *pageToken,
				JSON:               *jsonOut,
			})
			return 1
		}
		expectedScope := taskListExpectedScope{
			ProjectID:  projectID,
			WorkflowID: selectedWorkflowID,
		}
		if selectedWorkflowID == nil && strings.TrimSpace(*pageToken) != "" {
			expectedScope.WorkflowOwner = taskListExpectedWorkflowFromToken
		}
		return writeTaskListResponse(context.Background(), stdout, stderr, remote, resp, expectedScope, *jsonOut)
	})
}

func parseTaskListLabelMatch(raw string, explicit bool, selectorCount int, unlabeled bool) (serverapi.WorkflowTaskNamedLabelFilterMode, error) {
	mode := serverapi.WorkflowTaskNamedLabelFilterMode(raw)
	if mode != serverapi.WorkflowTaskNamedLabelFilterModeAny && mode != serverapi.WorkflowTaskNamedLabelFilterModeAll {
		return "", errors.New("--label-match is invalid")
	}
	if unlabeled && (selectorCount > 0 || explicit) {
		return "", errors.New("--unlabeled cannot be combined with --label or --label-match")
	}
	if explicit && selectorCount == 0 {
		return "", errors.New("--label-match requires at least one --label")
	}
	return mode, nil
}

func writeTaskListResponse(ctx context.Context, stdout io.Writer, stderr io.Writer, remote workflowCommandRemote, resp serverapi.WorkflowTaskListResponse, expectedScope taskListExpectedScope, jsonOut bool) int {
	projection, err := taskListProjectionFromResponse(resp, expectedScope)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if jsonOut {
		return writeCommandJSON(stdout, stderr, projection.Output)
	}
	hasLabels := false
	for _, row := range projection.Rows {
		if len(row.Item.LabelIDs) > 0 {
			hasLabels = true
			break
		}
	}
	if hasLabels {
		_, snapshot, err := loadWorkflowProjectLabelCatalog(ctx, remote, resp.Scope.ProjectID)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		for index := range projection.Rows {
			names, err := workflowProjectLabelNames(snapshot, projection.Rows[index].Item.LabelIDs)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			projection.Rows[index].LabelNames = names
		}
	}
	for _, row := range projection.Rows {
		statusText, err := taskStatusText(row.Item.Status)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "%s: %s.\nStatus: %s\n", row.Item.ShortID, row.Item.Title, statusText)
		if row.ShowWorkflow {
			fmt.Fprintf(stdout, "Workflow: %s\n", row.WorkflowName)
		}
		if row.ShowColumns {
			fmt.Fprintf(stdout, "Columns: %s\n", taskListColumnKeysText(*row.Item.ColumnKeys))
		}
		if len(row.LabelNames) > 0 {
			fmt.Fprint(stdout, "Labels:")
			for _, name := range row.LabelNames {
				fmt.Fprintf(stdout, " %q", name)
			}
			fmt.Fprintln(stdout)
		}
	}
	if resp.NextPageToken != nil {
		fmt.Fprintf(stderr, "Next page token: `%s`\n", *resp.NextPageToken)
	}
	return 0
}

func writeTaskListError(stderr io.Writer, err error, commandContext taskListCommandContext) {
	var scopeErr *serverapi.WorkflowTaskListScopeError
	if !errors.As(err, &scopeErr) {
		fmt.Fprintln(stderr, err)
		return
	}
	recovery, projectionErr := taskListRecoveryForScopeError(scopeErr, commandContext)
	if projectionErr != nil {
		fmt.Fprintln(stderr, projectionErr)
		return
	}
	switch recovery.Kind {
	case taskWorkflowRecoveryNoLinkedWorkflows:
		fmt.Fprintln(stderr, "This project doesn't have any linked workflows yet. First, create a workflow or link an existing one, then retry.")
	case taskWorkflowRecoveryWorkflowNotLinked:
		fmt.Fprintln(stderr, "The selected workflow isn't linked to this project.")
	case taskWorkflowRecoveryWorkflowRequiredColumns:
		fmt.Fprintln(stderr, "Column filters and column sorting require an explicit workflow.")
	}
	for _, command := range recovery.Commands {
		fmt.Fprintf(stderr, "  %s\n", commandString(command.Args))
	}
}

func taskListColumnKeysText(columnKeys []string) string {
	if len(columnKeys) == 0 {
		return "(none)"
	}
	return strings.Join(columnKeys, ", ")
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
		case serverapi.WorkflowTaskStatusKindDone, serverapi.WorkflowTaskStatusKindWaitingQuestion, serverapi.WorkflowTaskStatusKindWaitingApproval, serverapi.WorkflowTaskStatusKindInterrupted, serverapi.WorkflowTaskStatusKindRunning, serverapi.WorkflowTaskStatusKindQueued, serverapi.WorkflowTaskStatusKindBacklog, serverapi.WorkflowTaskStatusKindActive:
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
	case "title":
		return serverapi.WorkflowTaskListSortFieldTitle, nil
	default:
		return "", fmt.Errorf("--sort field must be created, updated, status, column, or title")
	}
}
