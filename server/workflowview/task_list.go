package workflowview

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type TaskList struct {
	metadata    *metadata.Store
	queries     *sqlitegen.Queries
	definitions *DefinitionProjection
	projection  *TaskStatusProjection
}

func NewTaskList(metadataStore *metadata.Store, definitions *DefinitionProjection, projection *TaskStatusProjection) (*TaskList, error) {
	if metadataStore == nil || metadataStore.Queries() == nil {
		return nil, errors.New("metadata store is required")
	}
	if definitions == nil {
		return nil, errors.New("definition projection is required")
	}
	if projection == nil {
		return nil, errors.New("task status projection is required")
	}
	return &TaskList{
		metadata:    metadataStore,
		queries:     metadataStore.Queries(),
		definitions: definitions,
		projection:  projection,
	}, nil
}

func (l *TaskList) List(ctx context.Context, req serverapi.WorkflowTaskListRequest) (serverapi.WorkflowTaskListResponse, error) {
	if l == nil {
		return serverapi.WorkflowTaskListResponse{}, errors.New("task list is required")
	}
	if err := req.ValidateRPC(); err != nil {
		return serverapi.WorkflowTaskListResponse{}, err
	}
	window, err := serverapi.ResolveWorkflowOffsetWindow(req.Offset, req.Limit)
	if err != nil {
		return serverapi.WorkflowTaskListResponse{}, err
	}
	projectID, workflowID, err := l.resolveScope(ctx, req.ProjectID, req.WorkflowID)
	if err != nil {
		return serverapi.WorkflowTaskListResponse{}, err
	}
	if _, err := l.metadata.GetProjectOverview(ctx, projectID); err != nil {
		return serverapi.WorkflowTaskListResponse{}, err
	}
	labelFilter, err := resolveWorkflowTaskLabelFilter(ctx, l.queries, projectID, req.LabelFilter)
	if err != nil {
		return serverapi.WorkflowTaskListResponse{}, err
	}
	var columns []serverapi.WorkflowBoardColumn
	if workflowID == nil {
		if len(req.ColumnKeys) > 0 || workflowTaskListSortUsesColumn(req.Sort) {
			errorProjectID := projectID
			return serverapi.WorkflowTaskListResponse{}, &serverapi.WorkflowTaskListScopeError{
				Reason:    serverapi.WorkflowTaskListScopeReasonWorkflowRequiredColumns,
				ProjectID: &errorProjectID,
			}
		}
	} else {
		snapshot, snapshotErr := l.definitions.snapshot(ctx, *workflowID)
		if snapshotErr != nil {
			return serverapi.WorkflowTaskListResponse{}, snapshotErr
		}
		columns = boardColumns(snapshot)
		if err := validateWorkflowTaskListColumnKeys(req.ColumnKeys, columns); err != nil {
			return serverapi.WorkflowTaskListResponse{}, err
		}
	}
	sortSelectors := normalizeWorkflowTaskListSort(req.Sort)
	var narrowedQuery *workflowTaskListNarrowedQueryFacts
	if workflowID != nil {
		narrowedQuery = &workflowTaskListNarrowedQueryFacts{
			workflowID: *workflowID,
			columns:    columns,
			columnKeys: req.ColumnKeys,
		}
	}
	observation, err := l.projection.Observe(nil)
	if err != nil {
		return serverapi.WorkflowTaskListResponse{}, err
	}
	page, err := l.queryRows(ctx, workflowTaskListQueryRequest{
		projectID:          projectID,
		narrowed:           narrowedQuery,
		statusKinds:        req.StatusKinds,
		attentionKinds:     req.AttentionKinds,
		labelFilter:        labelFilter,
		dependencyFilter:   req.DependencyFilter,
		sortSelectors:      sortSelectors,
		offset:             window.Offset,
		limit:              window.Limit + 1,
		liveTaskStatesJSON: observation.LiveTaskStatesJSON,
	})
	if err != nil {
		return serverapi.WorkflowTaskListResponse{}, err
	}
	matchingWorkflowCardinality, err := workflowTaskListMatchingWorkflowCardinality(page.matchingWorkflowCount)
	if err != nil {
		return serverapi.WorkflowTaskListResponse{}, err
	}
	pageItems := page.rows
	hasNext := len(pageItems) > window.Limit
	if hasNext {
		pageItems = pageItems[:window.Limit]
	}
	pageTaskIDs := make([]string, 0, len(pageItems))
	for _, row := range pageItems {
		pageTaskIDs = append(pageTaskIDs, row.item.TaskID)
	}
	labelsByTask, err := loadTaskLabelsByTask(ctx, l.queries, pageTaskIDs)
	if err != nil {
		return serverapi.WorkflowTaskListResponse{}, err
	}
	dependencyRows, err := l.queries.ListTaskDependencyProgressByTasks(ctx, pageTaskIDs)
	if err != nil {
		return serverapi.WorkflowTaskListResponse{}, err
	}
	progressRows := make([]taskDependencyProgressRow, 0, len(dependencyRows))
	for _, row := range dependencyRows {
		progressRows = append(progressRows, taskDependencyProgressRow{
			taskID:         row.TaskID,
			satisfiedCount: row.DependencySatisfiedCount,
			totalCount:     row.DependencyTotalCount,
		})
	}
	dependencyProgressByTask, err := projectTaskDependencyProgress(progressRows)
	if err != nil {
		return serverapi.WorkflowTaskListResponse{}, err
	}
	responseItems := make([]serverapi.WorkflowTaskListItem, 0, len(pageItems))
	for _, row := range pageItems {
		item := row.item
		item.Labels = labelsByTask[item.TaskID]
		item.DependencyProgress = dependencyProgressByTask[item.TaskID]
		responseItems = append(responseItems, item)
	}
	var nextOffset *int
	if hasNext {
		value := window.Offset + len(pageItems)
		nextOffset = &value
	}
	return serverapi.WorkflowTaskListResponse{
		Scope: serverapi.WorkflowTaskListScope{
			ProjectID:  projectID,
			WorkflowID: workflowID,
		},
		MatchingWorkflowCardinality: matchingWorkflowCardinality,
		NextOffset:                  nextOffset,
		GeneratedAtUnixMs:           time.Now().UTC().UnixMilli(),
		Tasks:                       responseItems,
	}, nil
}

func (l *TaskList) CountGroups(ctx context.Context, req serverapi.WorkflowProjectTaskGroupCountsRequest) (serverapi.WorkflowProjectTaskGroupCountsResponse, error) {
	if l == nil {
		return serverapi.WorkflowProjectTaskGroupCountsResponse{}, errors.New("task list is required")
	}
	if err := req.ValidateRPC(); err != nil {
		return serverapi.WorkflowProjectTaskGroupCountsResponse{}, err
	}
	if _, err := l.metadata.GetProjectOverview(ctx, req.ProjectID); err != nil {
		return serverapi.WorkflowProjectTaskGroupCountsResponse{}, err
	}
	labelFilter, err := resolveWorkflowTaskLabelFilter(ctx, l.queries, req.ProjectID, req.LabelFilter)
	if err != nil {
		return serverapi.WorkflowProjectTaskGroupCountsResponse{}, err
	}
	labelFilterArgs, err := labelFilter.queryArgs()
	if err != nil {
		return serverapi.WorkflowProjectTaskGroupCountsResponse{}, err
	}
	statusKindsJSON, err := workflowTaskStatusKindsJSON(req.StatusKinds)
	if err != nil {
		return serverapi.WorkflowProjectTaskGroupCountsResponse{}, err
	}
	attentionKindsJSON, err := workflowTaskAttentionKindsJSON(req.AttentionKinds)
	if err != nil {
		return serverapi.WorkflowProjectTaskGroupCountsResponse{}, err
	}
	observation, err := l.projection.Observe(nil)
	if err != nil {
		return serverapi.WorkflowProjectTaskGroupCountsResponse{}, err
	}
	counts, err := l.queries.CountProjectTaskGroups(ctx, sqlitegen.CountProjectTaskGroupsParams{
		ProjectID:            req.ProjectID,
		StatusFilterSet:      boolInt64(len(req.StatusKinds) > 0),
		StatusKindsJson:      statusKindsJSON,
		AttentionFilterSet:   boolInt64(len(req.AttentionKinds) > 0),
		AttentionKindsJson:   attentionKindsJSON,
		LabelFilterKind:      labelFilterArgs.kind,
		LabelFilterMode:      labelFilterArgs.mode,
		LabelIdsJson:         labelFilterArgs.labelIDsJSON,
		ExcludedLabelIdsJson: labelFilterArgs.excludedLabelIDsJSON,
		DependencyFilter:     workflowTaskDependencyFilterQueryArg(req.DependencyFilter),
		LiveTaskStatesJson:   observation.LiveTaskStatesJSON,
	})
	if err != nil {
		return serverapi.WorkflowProjectTaskGroupCountsResponse{}, err
	}
	return serverapi.WorkflowProjectTaskGroupCountsResponse{
		ProjectID: req.ProjectID,
		Counts: serverapi.WorkflowProjectTaskGroupCounts{
			Active:  int(counts.ActiveCount),
			Backlog: int(counts.BacklogCount),
			Done:    int(counts.DoneCount),
		},
		GeneratedAtUnixMs: time.Now().UTC().UnixMilli(),
	}, nil
}

func (l *TaskList) resolveScope(ctx context.Context, projectIDValue *string, workflowIDValue *runtimeids.WorkflowID) (string, *runtimeids.WorkflowID, error) {
	if projectIDValue != nil && workflowIDValue != nil {

		if _, err := l.queries.GetActiveProjectWorkflowLinkByWorkflow(ctx, sqlitegen.GetActiveProjectWorkflowLinkByWorkflowParams{
			ProjectID:  *projectIDValue,
			WorkflowID: *workflowIDValue,
		}); err == nil {
			workflowID := *workflowIDValue
			return *projectIDValue, &workflowID, nil
		} else if errors.Is(err, sql.ErrNoRows) {
			errorProjectID := *projectIDValue
			errorWorkflowID := *workflowIDValue
			return "", nil, &serverapi.WorkflowTaskListScopeError{
				Reason:     serverapi.WorkflowTaskListScopeReasonWorkflowNotLinked,
				ProjectID:  &errorProjectID,
				WorkflowID: &errorWorkflowID,
			}
		} else {
			return "", nil, err
		}
	}
	if projectIDValue != nil {
		linkCount, err := l.queries.CountActiveProjectWorkflowLinks(ctx, *projectIDValue)
		if err != nil {
			return "", nil, err
		}
		if linkCount == 0 {
			errorProjectID := *projectIDValue
			return "", nil, &serverapi.WorkflowTaskListScopeError{
				Reason:    serverapi.WorkflowTaskListScopeReasonNoLinkedWorkflows,
				ProjectID: &errorProjectID,
			}
		}
		return *projectIDValue, nil, nil
	}
	return "", nil, &serverapi.WorkflowTaskListScopeError{
		Reason: serverapi.WorkflowTaskListScopeReasonNoLinkedWorkflows,
	}
}

func validateWorkflowTaskListColumnKeys(columnKeys []string, columns []serverapi.WorkflowBoardColumn) error {
	visible := map[string]bool{}
	for _, column := range columns {
		visible[column.Node.Key] = true
	}
	for index, columnKey := range columnKeys {
		if !visible[columnKey] {
			return serverapi.WorkflowRequestValidationError{
				Code:    serverapi.WorkflowRequestErrorInvalidValue,
				Field:   fmt.Sprintf("column_keys[%d]", index),
				Message: fmt.Sprintf("unknown workflow column key %q", columnKey),
			}
		}
	}
	return nil
}
