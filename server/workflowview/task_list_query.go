package workflowview

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"core/server/metadata/sqlitegen"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

const (
	workflowTaskListPageTokenVersion = 3
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

func workflowTaskListSortUsesColumn(sortSelectors []serverapi.WorkflowTaskListSort) bool {
	for _, selector := range sortSelectors {
		if selector.Field == serverapi.WorkflowTaskListSortFieldColumn {
			return true
		}
	}
	return false
}

type workflowTaskListPageTokenPayload struct {
	Version                     int                                                   `json:"version"`
	Scope                       workflowTaskListPageTokenScope                        `json:"scope"`
	MatchingWorkflowCardinality serverapi.WorkflowTaskListMatchingWorkflowCardinality `json:"matching_workflow_cardinality"`
	StatusModelVersion          int                                                   `json:"status_model_version"`
	Fingerprint                 string                                                `json:"fingerprint"`
	Cursor                      workflowTaskListCursor                                `json:"cursor"`
}

type workflowTaskListPageTokenScope struct {
	ProjectID   string                                          `json:"project_id"`
	ProjectWide *workflowTaskListProjectWidePageTokenInvariants `json:"project_wide,omitempty"`
	Narrowed    *workflowTaskListNarrowedPageTokenInvariants    `json:"narrowed,omitempty"`
}

type workflowTaskListProjectWidePageTokenInvariants struct{}

type workflowTaskListNarrowedPageTokenInvariants struct {
	WorkflowID          string `json:"workflow_id"`
	WorkflowVersion     int64  `json:"workflow_version"`
	ColumnStructureHash string `json:"column_structure_hash"`
}

type workflowTaskListCursor struct {
	TaskID            string `json:"task_id"`
	CreatedAtUnixMs   int64  `json:"created_at_unix_ms"`
	UpdatedAtUnixMs   int64  `json:"updated_at_unix_ms"`
	PrimaryStatusRank int    `json:"primary_status_rank"`
	ColumnRank        *int   `json:"column_rank,omitempty"`
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
	if payload.Version != workflowTaskListPageTokenVersion ||
		strings.TrimSpace(payload.Scope.ProjectID) == "" ||
		strings.TrimSpace(payload.Scope.ProjectID) != payload.Scope.ProjectID ||
		(payload.Scope.ProjectWide == nil) == (payload.Scope.Narrowed == nil) ||
		!workflowTaskListContinuationCardinalityValid(payload.MatchingWorkflowCardinality) ||
		payload.StatusModelVersion != workflowTaskStatusModelVersion ||
		strings.TrimSpace(payload.Cursor.TaskID) == "" ||
		strings.TrimSpace(payload.Fingerprint) == "" {
		return workflowTaskListPageTokenPayload{}, false, ErrInvalidPageToken
	}
	if narrowed := payload.Scope.Narrowed; narrowed != nil {
		if _, err := runtimeids.ParseCanonicalPrefixedUUIDv4(narrowed.WorkflowID, "workflow-", "workflow id"); err != nil ||
			narrowed.WorkflowVersion < 1 ||
			strings.TrimSpace(narrowed.ColumnStructureHash) == "" ||
			strings.TrimSpace(narrowed.ColumnStructureHash) != narrowed.ColumnStructureHash {
			return workflowTaskListPageTokenPayload{}, false, ErrInvalidPageToken
		}
	}
	return payload, true, nil
}

func workflowTaskListContinuationCardinalityValid(cardinality serverapi.WorkflowTaskListMatchingWorkflowCardinality) bool {
	return cardinality == serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne ||
		cardinality == serverapi.WorkflowTaskListMatchingWorkflowCardinalityMultiple
}

func workflowTaskListPageToken(payload workflowTaskListPageTokenPayload) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal task list page token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

type workflowTaskListProjectWideFingerprintInvariants struct{}

type workflowTaskListNarrowedFingerprintInvariants struct {
	ColumnStructureHash string `json:"column_structure_hash"`
}

type workflowTaskListFingerprintScope struct {
	ProjectWide *workflowTaskListProjectWideFingerprintInvariants `json:"project_wide,omitempty"`
	Narrowed    *workflowTaskListNarrowedFingerprintInvariants    `json:"narrowed,omitempty"`
}

func workflowTaskListRequestFingerprint(req serverapi.WorkflowTaskListRequest, sortSelectors []serverapi.WorkflowTaskListSort, scope workflowTaskListFingerprintScope) (string, error) {
	if (scope.ProjectWide == nil) == (scope.Narrowed == nil) {
		return "", errors.New("task list fingerprint requires exactly one scope mode")
	}
	if scope.Narrowed != nil &&
		(strings.TrimSpace(scope.Narrowed.ColumnStructureHash) == "" ||
			strings.TrimSpace(scope.Narrowed.ColumnStructureHash) != scope.Narrowed.ColumnStructureHash) {
		return "", errors.New("task list narrowed fingerprint requires column structure hash")
	}
	statusKinds := make([]string, 0, len(req.StatusKinds))
	for _, kind := range req.StatusKinds {
		statusKinds = append(statusKinds, string(kind))
	}
	attentionKinds := make([]string, 0, len(req.AttentionKinds))
	for _, kind := range req.AttentionKinds {
		attentionKinds = append(attentionKinds, string(kind))
	}
	payload := struct {
		ColumnKeys         []string                         `json:"column_keys"`
		StatusKinds        []string                         `json:"status_kinds"`
		AttentionKinds     []string                         `json:"attention_kinds"`
		Sort               []serverapi.WorkflowTaskListSort `json:"sort"`
		Scope              workflowTaskListFingerprintScope `json:"scope"`
		StatusModelVersion int                              `json:"status_model_version"`
	}{
		ColumnKeys:         dedupeSortedStrings(req.ColumnKeys),
		StatusKinds:        dedupeSortedStrings(statusKinds),
		AttentionKinds:     dedupeSortedStrings(attentionKinds),
		Sort:               sortSelectors,
		Scope:              scope,
		StatusModelVersion: workflowTaskStatusModelVersion,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal task list fingerprint: %w", err)
	}
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func workflowTaskListColumnStructureHash(def serverapi.WorkflowDefinition, columns []serverapi.WorkflowBoardColumn) (string, error) {
	columnFacts := make([]string, 0, len(columns))
	for _, column := range columns {
		columnFacts = append(columnFacts, strings.Join([]string{column.Node.NodeID, column.Node.Key, column.Node.Kind, strconv.Itoa(column.SortOrder), strconv.FormatBool(column.IsBacklog), strconv.FormatBool(column.IsDone)}, "\x00"))
	}
	payload := struct {
		Columns                []string `json:"columns"`
		StatusModelVersion     int      `json:"status_model_version"`
		CanceledTerminalNodeID *string  `json:"canceled_terminal_node_id"`
	}{
		Columns:                columnFacts,
		StatusModelVersion:     workflowTaskStatusModelVersion,
		CanceledTerminalNodeID: canceledBoardTerminalNodeID(def),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal task list column structure: %w", err)
	}
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
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
	narrowed       *workflowTaskListNarrowedQueryFacts
	statusKinds    []serverapi.WorkflowTaskStatusKind
	attentionKinds []serverapi.WorkflowTaskAttentionKind
	sortSelectors  []serverapi.WorkflowTaskListSort
	cursor         workflowTaskListCursor
	cursorSet      bool
	limit          int
}

type workflowTaskListNarrowedQueryFacts struct {
	workflowID             string
	canceledTerminalNodeID *string
	columns                []serverapi.WorkflowBoardColumn
	columnKeys             []string
}

type workflowTaskListRow struct {
	item                  serverapi.WorkflowTaskListItem
	titleSort             string
	primaryStatusRank     int
	columnRank            *int
	matchingWorkflowCount int
}

func (s *Service) listWorkflowTaskListRows(ctx context.Context, req workflowTaskListQueryRequest) ([]workflowTaskListRow, error) {
	workflowFilter := sql.NullString{}
	canceledTerminalNodeID := sql.NullString{}
	visibleColumnsJSON := sql.NullString{}
	columnKeysJSON := sql.NullString{}
	columnFilterSet := false
	if req.narrowed != nil {
		workflowFilter = sql.NullString{String: req.narrowed.workflowID, Valid: true}
		if req.narrowed.canceledTerminalNodeID != nil {
			canceledTerminalNodeID = sql.NullString{String: *req.narrowed.canceledTerminalNodeID, Valid: true}
		}
		encodedColumns, err := workflowTaskListVisibleColumnsJSON(req.narrowed.columns)
		if err != nil {
			return nil, err
		}
		visibleColumnsJSON = sql.NullString{String: encodedColumns, Valid: true}
		encodedColumnKeys, err := json.Marshal(req.narrowed.columnKeys)
		if err != nil {
			return nil, err
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
	cursorColumnRank := sql.NullInt64{}
	if req.cursor.ColumnRank != nil {
		cursorColumnRank = sql.NullInt64{Int64: int64(*req.cursor.ColumnRank), Valid: true}
	}
	rows, err := s.queries.ListWorkflowTaskListRows(ctx, sqlitegen.ListWorkflowTaskListRowsParams{
		ProjectID:               req.projectID,
		WorkflowID:              workflowFilter,
		CanceledTerminalNodeID:  canceledTerminalNodeID,
		VisibleColumnsJson:      visibleColumnsJSON,
		ColumnFilterSet:         boolInt64(columnFilterSet),
		ColumnKeysJson:          columnKeysJSON,
		StatusFilterSet:         boolInt64(len(req.statusKinds) > 0),
		StatusKindsJson:         string(statusKindsJSON),
		AttentionFilterSet:      boolInt64(len(req.attentionKinds) > 0),
		AttentionKindsJson:      string(attentionKindsJSON),
		CursorSet:               boolInt64(req.cursorSet),
		CursorCreatedAtUnixMs:   req.cursor.CreatedAtUnixMs,
		CursorUpdatedAtUnixMs:   req.cursor.UpdatedAtUnixMs,
		CursorPrimaryStatusRank: int64(req.cursor.PrimaryStatusRank),
		CursorColumnRank:        cursorColumnRank,
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
		var columnKeys *[]string
		if req.narrowed != nil {
			values := []string{}
			if row.ColumnKeysJson.Valid {
				var err error
				values, err = workflowTaskListColumnKeys(row.ID, row.ColumnKeysJson.String)
				if err != nil {
					return nil, err
				}
			}
			columnKeys = &values
		}
		columnRank := nullableInt(row.ColumnRank)
		if workflowTaskListSortUsesColumn(req.sortSelectors) && columnRank == nil {
			return nil, fmt.Errorf("workflow task list record for task %q is missing a column rank required by column sorting", row.ID)
		}
		out = append(out, workflowTaskListRow{
			item: serverapi.WorkflowTaskListItem{
				TaskID:          row.ID,
				ShortID:         row.ShortID,
				WorkflowID:      row.WorkflowID,
				WorkflowName:    row.WorkflowName,
				Title:           row.Title,
				CreatedAtUnixMs: row.CreatedAtUnixMs,
				UpdatedAtUnixMs: row.UpdatedAtUnixMs,
				ColumnKeys:      columnKeys,
				Status:          status,
				RunCount:        int(row.RunCount),
			},
			titleSort:             row.TitleSort,
			primaryStatusRank:     int(row.PrimaryStatusRank),
			columnRank:            columnRank,
			matchingWorkflowCount: int(row.MatchingWorkflowCount),
		})
	}
	return out, nil
}

func workflowTaskListMatchingWorkflowCardinality(rows []workflowTaskListRow) (serverapi.WorkflowTaskListMatchingWorkflowCardinality, error) {
	if len(rows) == 0 {
		return serverapi.WorkflowTaskListMatchingWorkflowCardinalityNone, nil
	}
	count := rows[0].matchingWorkflowCount
	for _, row := range rows[1:] {
		if row.matchingWorkflowCount != count {
			return "", fmt.Errorf("workflow task list query returned inconsistent matching workflow counts: first=%d task_id=%q count=%d", count, row.item.TaskID, row.matchingWorkflowCount)
		}
	}
	switch count {
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
