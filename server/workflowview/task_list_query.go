package workflowview

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata/sqlitegen"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func normalizeWorkflowTaskListSort(sortSelectors []serverapi.WorkflowTaskListSort) []serverapi.WorkflowTaskListSort {
	if len(sortSelectors) == 0 {
		return []serverapi.WorkflowTaskListSort{
			{Field: serverapi.WorkflowTaskListSortFieldStatus, Direction: serverapi.WorkflowTaskListSortDirectionAsc},
			{Field: serverapi.WorkflowTaskListSortFieldUpdated, Direction: serverapi.WorkflowTaskListSortDirectionDesc},
		}
	}
	return append([]serverapi.WorkflowTaskListSort(nil), sortSelectors...)
}

func workflowTaskListSortUsesColumn(sortSelectors []serverapi.WorkflowTaskListSort) bool {
	for _, selector := range sortSelectors {
		if selector.Field == serverapi.WorkflowTaskListSortFieldColumn {
			return true
		}
	}
	return false
}

type workflowTaskListQueryRequest struct {
	projectID           string
	narrowed            *workflowTaskListNarrowedQueryFacts
	statusKinds         []serverapi.WorkflowTaskStatusKind
	attentionKinds      []serverapi.WorkflowTaskAttentionKind
	labelFilter         workflowTaskLabelFilterFacts
	dependencyFilter    *bool
	sortSelectors       []serverapi.WorkflowTaskListSort
	lifecycleStateToken string
	offset              int
	limit               int
}

type workflowTaskListNarrowedQueryFacts struct {
	workflowID runtimeids.WorkflowID
	columns    []serverapi.WorkflowBoardColumn
	columnKeys []string
}

type workflowTaskListRow struct {
	item                  serverapi.WorkflowTaskListItem
	titleSort             string
	primaryStatusRank     int
	columnRank            *int
	matchingWorkflowCount int
}

type workflowTaskListPageResult struct {
	rows                  []workflowTaskListRow
	matchingWorkflowCount int
}

func (l *TaskList) queryRows(
	ctx context.Context,
	queries *sqlitegen.Queries,
	req workflowTaskListQueryRequest,
) (workflowTaskListPageResult, error) {
	if l == nil {
		return workflowTaskListPageResult{}, errors.New("task list is required")
	}
	if queries == nil {
		return workflowTaskListPageResult{}, errors.New("task list queries are required")
	}
	var workflowFilter *runtimeids.WorkflowID
	visibleColumnsJSON := sql.NullString{}
	columnKeysJSON := sql.NullString{}
	columnFilterSet := false
	if req.narrowed != nil {
		workflowFilter = &req.narrowed.workflowID
		encodedColumns, err := workflowTaskListVisibleColumnsJSON(req.narrowed.columns)
		if err != nil {
			return workflowTaskListPageResult{}, err
		}
		visibleColumnsJSON = sql.NullString{String: encodedColumns, Valid: true}
		encodedColumnKeys, err := json.Marshal(req.narrowed.columnKeys)
		if err != nil {
			return workflowTaskListPageResult{}, err
		}
		columnKeysJSON = sql.NullString{String: string(encodedColumnKeys), Valid: true}
		columnFilterSet = len(req.narrowed.columnKeys) > 0
	}
	statusKinds := make([]string, 0, len(req.statusKinds))
	for _, kind := range req.statusKinds {
		statusKinds = append(statusKinds, string(kind))
	}
	statusKindsJSON, err := json.Marshal(statusKinds)
	if err != nil {
		return workflowTaskListPageResult{}, err
	}
	attentionKinds := make([]string, 0, len(req.attentionKinds))
	for _, kind := range req.attentionKinds {
		attentionKinds = append(attentionKinds, string(kind))
	}
	attentionKindsJSON, err := json.Marshal(attentionKinds)
	if err != nil {
		return workflowTaskListPageResult{}, err
	}
	labelFilterArgs, err := req.labelFilter.queryArgs()
	if err != nil {
		return workflowTaskListPageResult{}, err
	}
	rows, err := queries.ListWorkflowTaskListRows(ctx, sqlitegen.ListWorkflowTaskListRowsParams{
		ProjectID:            req.projectID,
		WorkflowID:           workflowFilter,
		VisibleColumnsJson:   visibleColumnsJSON,
		ColumnFilterSet:      boolInt64(columnFilterSet),
		ColumnKeysJson:       columnKeysJSON,
		StatusFilterSet:      boolInt64(len(req.statusKinds) > 0),
		StatusKindsJson:      string(statusKindsJSON),
		AttentionFilterSet:   boolInt64(len(req.attentionKinds) > 0),
		AttentionKindsJson:   string(attentionKindsJSON),
		LabelFilterKind:      labelFilterArgs.kind,
		LabelFilterMode:      labelFilterArgs.mode,
		LabelIdsJson:         labelFilterArgs.labelIDsJSON,
		ExcludedLabelIdsJson: labelFilterArgs.excludedLabelIDsJSON,
		DependencyFilter:     workflowTaskDependencyFilterQueryArg(req.dependencyFilter),
		OffsetRows:           int64(req.offset),
		SortSelectorCount:    int64(len(req.sortSelectors)),
		Sort1Field:           string(workflowTaskListSortSelector(req.sortSelectors, 0).Field),
		Sort1Desc:            workflowTaskListSortDescending(req.sortSelectors, 0),
		Sort2Field:           string(workflowTaskListSortSelector(req.sortSelectors, 1).Field),
		Sort2Desc:            workflowTaskListSortDescending(req.sortSelectors, 1),
		Sort3Field:           string(workflowTaskListSortSelector(req.sortSelectors, 2).Field),
		Sort3Desc:            workflowTaskListSortDescending(req.sortSelectors, 2),
		Sort4Field:           string(workflowTaskListSortSelector(req.sortSelectors, 3).Field),
		Sort4Desc:            workflowTaskListSortDescending(req.sortSelectors, 3),
		Sort5Field:           string(workflowTaskListSortSelector(req.sortSelectors, 4).Field),
		Sort5Desc:            workflowTaskListSortDescending(req.sortSelectors, 4),
		Sort6Field:           string(workflowTaskListSortSelector(req.sortSelectors, 5).Field),
		Sort6Desc:            workflowTaskListSortDescending(req.sortSelectors, 5),
		Sort7Field:           string(workflowTaskListSortSelector(req.sortSelectors, 6).Field),
		Sort7Desc:            workflowTaskListSortDescending(req.sortSelectors, 6),
		LifecycleStateToken:  req.lifecycleStateToken,
		LimitRows:            int64(req.limit),
	})
	if err != nil {
		return workflowTaskListPageResult{}, err
	}
	if len(rows) == 0 {
		return workflowTaskListPageResult{}, errors.New("workflow task list query omitted its summary row")
	}
	result := workflowTaskListPageResult{
		rows:                  make([]workflowTaskListRow, 0, len(rows)-1),
		matchingWorkflowCount: int(rows[0].MatchingWorkflowCount),
	}
	for _, row := range rows {
		if int(row.MatchingWorkflowCount) != result.matchingWorkflowCount {
			return workflowTaskListPageResult{}, fmt.Errorf("workflow task list query returned inconsistent matching workflow counts: first=%d count=%d", result.matchingWorkflowCount, row.MatchingWorkflowCount)
		}
		if !row.ID.Valid {
			continue
		}
		statusFact, err := l.projection.DecodeStatus(TaskStatusInput{
			TaskID:             row.ID.String,
			Kind:               row.Kind.String,
			NodeIDsJSON:        row.NodeIdsJson.String,
			AttentionTypesJSON: row.AttentionTypesJson.String,
		})
		if err != nil {
			return workflowTaskListPageResult{}, err
		}
		var columnKeys *[]string
		if req.narrowed != nil {
			values := []string{}
			if row.ColumnKeysJson.Valid {
				var err error
				values, err = workflowTaskListColumnKeys(row.ID.String, row.ColumnKeysJson.String)
				if err != nil {
					return workflowTaskListPageResult{}, err
				}
			}
			columnKeys = &values
		}
		columnRank := nullableInt(row.ColumnRank)
		if workflowTaskListSortUsesColumn(req.sortSelectors) && columnRank == nil {
			return workflowTaskListPageResult{}, fmt.Errorf("workflow task list record for task %q is missing a column rank required by column sorting", row.ID.String)
		}
		var workflowName *string
		if req.narrowed == nil {
			if !row.WorkflowName.Valid || strings.TrimSpace(row.WorkflowName.String) == "" {
				return workflowTaskListPageResult{}, fmt.Errorf("project-wide workflow task list record for task %q is missing workflow name", row.ID.String)
			}
			value := row.WorkflowName.String
			workflowName = &value
		}
		result.rows = append(result.rows, workflowTaskListRow{
			item: serverapi.WorkflowTaskListItem{
				TaskID:          row.ID.String,
				ShortID:         row.ShortID.String,
				WorkflowID:      *row.WorkflowID,
				WorkflowName:    workflowName,
				Title:           row.Title.String,
				CreatedAtUnixMs: row.CreatedAtUnixMs.Int64,
				UpdatedAtUnixMs: row.UpdatedAtUnixMs.Int64,
				ColumnKeys:      columnKeys,
				Status:          statusFact.Status,
			},
			titleSort:             row.TitleSort.String,
			primaryStatusRank:     int(row.PrimaryStatusRank.Int64),
			columnRank:            columnRank,
			matchingWorkflowCount: int(row.MatchingWorkflowCount),
		})
	}
	return result, nil
}

func workflowTaskListMatchingWorkflowCardinality(count int) (serverapi.WorkflowTaskListMatchingWorkflowCardinality, error) {
	switch count {
	case 0:
		return serverapi.WorkflowTaskListMatchingWorkflowCardinalityNone, nil
	case 1:
		return serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne, nil
	case 2:
		return serverapi.WorkflowTaskListMatchingWorkflowCardinalityMultiple, nil
	default:
		return "", fmt.Errorf("workflow task list query returned invalid matching workflow count %d", count)
	}
}

func workflowTaskListColumnKeys(taskID string, encoded string) ([]string, error) {
	var values []string
	if err := json.Unmarshal([]byte(encoded), &values); err != nil {
		return nil, fmt.Errorf("workflow task list record for task %q has malformed column_keys_json: %w", taskID, err)
	}
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("workflow task list record for task %q has blank column_keys_json[%d]", taskID, index)
		}
		if _, exists := seen[value]; exists {
			return nil, fmt.Errorf("workflow task list record for task %q has duplicate column_keys_json value %q", taskID, value)
		}
		seen[value] = struct{}{}
	}
	return values, nil
}

func nullableInt(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	converted := int(value.Int64)
	return &converted
}

func workflowTaskListSortSelector(sortSelectors []serverapi.WorkflowTaskListSort, index int) serverapi.WorkflowTaskListSort {
	if index >= len(sortSelectors) {
		return serverapi.WorkflowTaskListSort{}
	}
	return sortSelectors[index]
}

func workflowTaskListSortDescending(sortSelectors []serverapi.WorkflowTaskListSort, index int) int64 {
	if workflowTaskListSortSelector(sortSelectors, index).Direction == serverapi.WorkflowTaskListSortDirectionDesc {
		return 1
	}
	return 0
}

type workflowTaskListVisibleColumn struct {
	NodeID      string `json:"node_id"`
	NodeKey     string `json:"node_key"`
	NodeKind    string `json:"node_kind"`
	StatusOrder int    `json:"status_order"`
}

func workflowTaskListVisibleColumnsJSON(columns []serverapi.WorkflowBoardColumn) (string, error) {
	rows := make([]workflowTaskListVisibleColumn, 0, len(columns))
	for _, column := range columns {
		rows = append(rows, workflowTaskListVisibleColumn{
			NodeID:      column.Node.NodeID,
			NodeKey:     column.Node.Key,
			NodeKind:    column.Node.Kind,
			StatusOrder: column.SortOrder,
		})
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func boolInt64(value bool) int64 {
	if value {
		return 1
	}
	return 0
}
