package workflowview

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"core/server/metadata/sqlitegen"
	"core/shared/serverapi"
)

const (
	workflowTaskListPageTokenVersion = 2
	workflowTaskStatusModelVersion   = 1
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

type workflowTaskListPageTokenPayload struct {
	Version             int                    `json:"version"`
	ProjectID           string                 `json:"project_id"`
	WorkflowID          string                 `json:"workflow_id"`
	WorkflowVersion     int64                  `json:"workflow_version"`
	ColumnStructureHash string                 `json:"column_structure_hash"`
	StatusModelVersion  int                    `json:"status_model_version"`
	Fingerprint         string                 `json:"fingerprint"`
	Cursor              workflowTaskListCursor `json:"cursor"`
}

type workflowTaskListCursor struct {
	TaskID            string `json:"task_id"`
	CreatedAtUnixMs   int64  `json:"created_at_unix_ms"`
	UpdatedAtUnixMs   int64  `json:"updated_at_unix_ms"`
	PrimaryStatusRank int    `json:"primary_status_rank"`
	ColumnRank        int    `json:"column_rank"`
	RunCount          int    `json:"run_count"`
	TitleSort         string `json:"title_sort"`
}

func parseWorkflowTaskListPageToken(raw string) (workflowTaskListPageTokenPayload, bool, error) {
	if strings.TrimSpace(raw) == "" {
		return workflowTaskListPageTokenPayload{}, false, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return workflowTaskListPageTokenPayload{}, false, ErrInvalidPageToken
	}
	var payload workflowTaskListPageTokenPayload
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return workflowTaskListPageTokenPayload{}, false, ErrInvalidPageToken
	}
	if payload.Version != workflowTaskListPageTokenVersion || strings.TrimSpace(payload.ProjectID) == "" || strings.TrimSpace(payload.WorkflowID) == "" || payload.WorkflowVersion < 1 || strings.TrimSpace(payload.ColumnStructureHash) == "" || payload.StatusModelVersion != workflowTaskStatusModelVersion || strings.TrimSpace(payload.Cursor.TaskID) == "" || strings.TrimSpace(payload.Fingerprint) == "" {
		return workflowTaskListPageTokenPayload{}, false, ErrInvalidPageToken
	}
	return payload, true, nil
}

func workflowTaskListPageToken(payload workflowTaskListPageTokenPayload) string {
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func workflowTaskListRequestFingerprint(req serverapi.WorkflowTaskListRequest, sortSelectors []serverapi.WorkflowTaskListSort, columnStructureHash string) string {
	statusKinds := make([]string, 0, len(req.StatusKinds))
	for _, kind := range req.StatusKinds {
		statusKinds = append(statusKinds, string(kind))
	}
	attentionKinds := make([]string, 0, len(req.AttentionKinds))
	for _, kind := range req.AttentionKinds {
		attentionKinds = append(attentionKinds, string(kind))
	}
	payload := struct {
		ColumnKeys          []string                         `json:"column_keys"`
		StatusKinds         []string                         `json:"status_kinds"`
		AttentionKinds      []string                         `json:"attention_kinds"`
		Sort                []serverapi.WorkflowTaskListSort `json:"sort"`
		ColumnStructureHash string                           `json:"column_structure_hash"`
		StatusModelVersion  int                              `json:"status_model_version"`
	}{
		ColumnKeys:          dedupeSortedStrings(req.ColumnKeys),
		StatusKinds:         dedupeSortedStrings(statusKinds),
		AttentionKinds:      dedupeSortedStrings(attentionKinds),
		Sort:                sortSelectors,
		ColumnStructureHash: columnStructureHash,
		StatusModelVersion:  workflowTaskStatusModelVersion,
	}
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func workflowTaskListColumnStructureHash(def serverapi.WorkflowDefinition, columns []serverapi.WorkflowBoardColumn) string {
	parts := make([]string, 0, len(columns)+1)
	for _, column := range columns {
		parts = append(parts, strings.Join([]string{column.Node.NodeID, column.Node.Key, column.Node.Kind, strconv.Itoa(column.SortOrder), strconv.FormatBool(column.IsBacklog), strconv.FormatBool(column.IsDone)}, "\x00"))
	}
	parts = append(parts, "status-model:"+strconv.Itoa(workflowTaskStatusModelVersion))
	parts = append(parts, "canceled:"+canceledBoardTerminalNodeID(def))
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x01")))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func dedupeSortedStrings(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	sorted := append([]string(nil), values...)
	sort.Strings(sorted)
	out := make([]string, 0, len(sorted))
	for _, value := range sorted {
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}

type workflowTaskListQueryRequest struct {
	projectID      string
	workflowID     string
	columns        []serverapi.WorkflowBoardColumn
	columnKeys     []string
	statusKinds    []serverapi.WorkflowTaskStatusKind
	attentionKinds []serverapi.WorkflowTaskAttentionKind
	sortSelectors  []serverapi.WorkflowTaskListSort
	cursor         workflowTaskListCursor
	cursorSet      bool
	limit          int
}

type workflowTaskListRow struct {
	item              serverapi.WorkflowTaskListItem
	titleSort         string
	primaryStatusRank int
	columnRank        int
}

func (s *Service) listWorkflowTaskListRows(ctx context.Context, req workflowTaskListQueryRequest) ([]workflowTaskListRow, error) {
	if len(req.columns) == 0 {
		return []workflowTaskListRow{}, nil
	}
	visibleColumnsJSON, err := workflowTaskListVisibleColumnsJSON(req.columns)
	if err != nil {
		return nil, err
	}
	columnKeysJSON, err := json.Marshal(req.columnKeys)
	if err != nil {
		return nil, err
	}
	statusKinds := make([]string, 0, len(req.statusKinds))
	for _, kind := range req.statusKinds {
		statusKinds = append(statusKinds, string(kind))
	}
	statusKindsJSON, err := json.Marshal(statusKinds)
	if err != nil {
		return nil, err
	}
	attentionKinds := make([]string, 0, len(req.attentionKinds))
	for _, kind := range req.attentionKinds {
		attentionKinds = append(attentionKinds, string(kind))
	}
	attentionKindsJSON, err := json.Marshal(attentionKinds)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListWorkflowTaskListRows(ctx, sqlitegen.ListWorkflowTaskListRowsParams{
		ProjectID:               req.projectID,
		WorkflowID:              req.workflowID,
		VisibleColumnsJson:      visibleColumnsJSON,
		ColumnFilterSet:         boolInt64(len(req.columnKeys) > 0),
		ColumnKeysJson:          string(columnKeysJSON),
		StatusFilterSet:         boolInt64(len(req.statusKinds) > 0),
		StatusKindsJson:         string(statusKindsJSON),
		AttentionFilterSet:      boolInt64(len(req.attentionKinds) > 0),
		AttentionKindsJson:      string(attentionKindsJSON),
		CursorSet:               boolInt64(req.cursorSet),
		CursorCreatedAtUnixMs:   req.cursor.CreatedAtUnixMs,
		CursorUpdatedAtUnixMs:   req.cursor.UpdatedAtUnixMs,
		CursorPrimaryStatusRank: int64(req.cursor.PrimaryStatusRank),
		CursorColumnRank:        int64(req.cursor.ColumnRank),
		CursorRunCount:          int64(req.cursor.RunCount),
		CursorTitleSort:         req.cursor.TitleSort,
		CursorTaskID:            req.cursor.TaskID,
		Sort1Field:              string(workflowTaskListSortSelector(req.sortSelectors, 0).Field),
		Sort1Desc:               workflowTaskListSortDescending(req.sortSelectors, 0),
		Sort2Field:              string(workflowTaskListSortSelector(req.sortSelectors, 1).Field),
		Sort2Desc:               workflowTaskListSortDescending(req.sortSelectors, 1),
		Sort3Field:              string(workflowTaskListSortSelector(req.sortSelectors, 2).Field),
		Sort3Desc:               workflowTaskListSortDescending(req.sortSelectors, 2),
		Sort4Field:              string(workflowTaskListSortSelector(req.sortSelectors, 3).Field),
		Sort4Desc:               workflowTaskListSortDescending(req.sortSelectors, 3),
		Sort5Field:              string(workflowTaskListSortSelector(req.sortSelectors, 4).Field),
		Sort5Desc:               workflowTaskListSortDescending(req.sortSelectors, 4),
		LimitRows:               int64(req.limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]workflowTaskListRow, 0, len(rows))
	for _, row := range rows {
		status, err := workflowTaskStatusFromFields(row.ID, row.Kind, row.NodeIdsJson, row.RunIdsJson, row.AttentionTypesJson)
		if err != nil {
			return nil, err
		}
		columnKeys, err := workflowTaskListColumnKeys(row.ID, row.ColumnKeysJson)
		if err != nil {
			return nil, err
		}
		out = append(out, workflowTaskListRow{
			item: serverapi.WorkflowTaskListItem{
				TaskID:          row.ID,
				ShortID:         row.ShortID,
				WorkflowID:      row.WorkflowID,
				Title:           row.Title,
				CreatedAtUnixMs: row.CreatedAtUnixMs,
				UpdatedAtUnixMs: row.UpdatedAtUnixMs,
				ColumnKeys:      columnKeys,
				Status:          status,
				RunCount:        int(row.RunCount),
			},
			titleSort:         row.TitleSort,
			primaryStatusRank: int(row.PrimaryStatusRank),
			columnRank:        int(row.ColumnRank),
		})
	}
	return out, nil
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

func workflowTaskListCursorFromRow(row workflowTaskListRow) workflowTaskListCursor {
	return workflowTaskListCursor{
		TaskID:            row.item.TaskID,
		CreatedAtUnixMs:   row.item.CreatedAtUnixMs,
		UpdatedAtUnixMs:   row.item.UpdatedAtUnixMs,
		PrimaryStatusRank: row.primaryStatusRank,
		ColumnRank:        row.columnRank,
		RunCount:          row.item.RunCount,
		TitleSort:         row.titleSort,
	}
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
