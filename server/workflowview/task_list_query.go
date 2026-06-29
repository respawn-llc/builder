package workflowview

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"core/server/metadata/sqlitegen"
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

type workflowTaskListPageTokenPayload struct {
	Version             int                    `json:"version"`
	ProjectID           string                 `json:"project_id"`
	WorkflowID          string                 `json:"workflow_id"`
	WorkflowVersion     int64                  `json:"workflow_version"`
	StatusStructureHash string                 `json:"status_structure_hash"`
	Fingerprint         string                 `json:"fingerprint"`
	Cursor              workflowTaskListCursor `json:"cursor"`
}

type workflowTaskListCursor struct {
	TaskID          string `json:"task_id"`
	CreatedAtUnixMs int64  `json:"created_at_unix_ms"`
	UpdatedAtUnixMs int64  `json:"updated_at_unix_ms"`
	StatusOrder     int    `json:"status_order"`
	RunCount        int    `json:"run_count"`
	TitleSort       string `json:"title_sort"`
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
	if payload.Version != 1 || strings.TrimSpace(payload.ProjectID) == "" || strings.TrimSpace(payload.WorkflowID) == "" || strings.TrimSpace(payload.Cursor.TaskID) == "" || strings.TrimSpace(payload.Fingerprint) == "" {
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

func workflowTaskListRequestFingerprint(req serverapi.WorkflowTaskListRequest, sortSelectors []serverapi.WorkflowTaskListSort, statusStructureHash string) string {
	statusKeys := dedupeSortedStrings(req.StatusKeys)
	runStatusStrings := make([]string, 0, len(req.RunStatuses))
	for _, status := range req.RunStatuses {
		runStatusStrings = append(runStatusStrings, string(status))
	}
	runStatuses := dedupeSortedStrings(runStatusStrings)
	payload := struct {
		StatusKeys          []string                         `json:"status_keys"`
		RunStatuses         []string                         `json:"run_statuses"`
		Sort                []serverapi.WorkflowTaskListSort `json:"sort"`
		StatusStructureHash string                           `json:"status_structure_hash"`
	}{StatusKeys: statusKeys, RunStatuses: runStatuses, Sort: sortSelectors, StatusStructureHash: statusStructureHash}
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func workflowTaskListStatusStructureHash(def serverapi.WorkflowDefinition, columns []serverapi.WorkflowBoardColumn) string {
	parts := make([]string, 0, len(columns)+1)
	for _, column := range columns {
		parts = append(parts, strings.Join([]string{column.Node.NodeID, column.Node.Key, column.Node.Kind, strconv.Itoa(column.SortOrder), strconv.FormatBool(column.IsBacklog), strconv.FormatBool(column.IsDone)}, "\x00"))
	}
	parts = append(parts, "canceled:"+canceledBoardTerminalNodeID(def))
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x01")))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

type workflowTaskListItemWithSort struct {
	item        serverapi.WorkflowTaskListItem
	statusOrder int
	titleSort   string
}

func workflowTaskListCursorFromItem(item workflowTaskListItemWithSort) workflowTaskListCursor {
	return workflowTaskListCursor{
		TaskID:          item.item.TaskID,
		CreatedAtUnixMs: item.item.CreatedAtUnixMs,
		UpdatedAtUnixMs: item.item.UpdatedAtUnixMs,
		StatusOrder:     item.statusOrder,
		RunCount:        item.item.RunCount,
		TitleSort:       item.titleSort,
	}
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
	projectID          string
	workflowID         string
	columns            []serverapi.WorkflowBoardColumn
	canceledTerminalID string
	statusKeys         []string
	runStatuses        []serverapi.WorkflowTaskRunStatus
	sortSelectors      []serverapi.WorkflowTaskListSort
	cursor             workflowTaskListCursor
	cursorSet          bool
	limit              int
}

type workflowTaskListRow struct {
	task        sqlitegen.TaskRecord
	statusOrder int
	runCount    int
	runStatus   serverapi.WorkflowTaskRunStatus
	titleSort   string
}

type workflowTaskListSortSlot struct {
	field string
	desc  int64
}

func (s *Service) listWorkflowTaskListRows(ctx context.Context, req workflowTaskListQueryRequest) ([]workflowTaskListRow, error) {
	if len(req.columns) == 0 {
		return []workflowTaskListRow{}, nil
	}
	visibleColumnsJSON, err := workflowTaskListVisibleColumnsJSON(req.columns)
	if err != nil {
		return nil, err
	}
	statusKeysJSON, err := json.Marshal(req.statusKeys)
	if err != nil {
		return nil, err
	}
	runStatuses := make([]string, 0, len(req.runStatuses))
	for _, status := range req.runStatuses {
		runStatuses = append(runStatuses, string(status))
	}
	runStatusesJSON, err := json.Marshal(runStatuses)
	if err != nil {
		return nil, err
	}
	sortSlots := workflowTaskListSortSlots(req.sortSelectors)
	rows, err := s.queries.ListWorkflowTaskListRows(ctx, sqlitegen.ListWorkflowTaskListRowsParams{
		StatusFilterSet:        boolInt64(len(req.statusKeys) > 0),
		StatusKeysJson:         string(statusKeysJSON),
		RunStatusFilterSet:     boolInt64(len(req.runStatuses) > 0),
		RunStatusesJson:        string(runStatusesJSON),
		VisibleColumnsJson:     visibleColumnsJSON,
		ProjectID:              req.projectID,
		WorkflowID:             req.workflowID,
		CanceledTerminalNodeID: req.canceledTerminalID,
		CursorSet:              boolInt64(req.cursorSet),
		CursorTaskID:           req.cursor.TaskID,
		CursorCreatedAtUnixMs:  req.cursor.CreatedAtUnixMs,
		CursorUpdatedAtUnixMs:  req.cursor.UpdatedAtUnixMs,
		CursorStatusOrder:      int64(req.cursor.StatusOrder),
		CursorRunCount:         int64(req.cursor.RunCount),
		CursorTitleSort:        req.cursor.TitleSort,
		Sort1Field:             sortSlots[0].field,
		Sort1Desc:              sortSlots[0].desc,
		Sort2Field:             sortSlots[1].field,
		Sort2Desc:              sortSlots[1].desc,
		Sort3Field:             sortSlots[2].field,
		Sort3Desc:              sortSlots[2].desc,
		Sort4Field:             sortSlots[3].field,
		Sort4Desc:              sortSlots[3].desc,
		Sort5Field:             sortSlots[4].field,
		Sort5Desc:              sortSlots[4].desc,
		LimitRows:              int64(req.limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]workflowTaskListRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, workflowTaskListRow{
			task: sqlitegen.TaskRecord{
				ID:                    row.ID,
				ProjectID:             row.ProjectID,
				ProjectWorkflowLinkID: row.ProjectWorkflowLinkID,
				WorkflowID:            row.WorkflowID,
				WorkflowRevisionSeen:  row.WorkflowRevisionSeen,
				TaskSeq:               row.TaskSeq,
				ShortID:               row.ShortID,
				Title:                 row.Title,
				Body:                  row.Body,
				SourceUrl:             row.SourceUrl,
				SourceWorkspaceID:     row.SourceWorkspaceID,
				ManagedWorktreeID:     row.ManagedWorktreeID,
				CanceledAtUnixMs:      row.CanceledAtUnixMs,
				CancellationReason:    row.CancellationReason,
				CreatedAtUnixMs:       row.CreatedAtUnixMs,
				UpdatedAtUnixMs:       row.UpdatedAtUnixMs,
				MetadataJson:          row.MetadataJson,
			},
			statusOrder: int(row.StatusOrder),
			runCount:    int(row.RunCount),
			runStatus:   serverapi.WorkflowTaskRunStatus(row.RunStatus),
			titleSort:   row.TitleSort,
		})
	}
	return out, nil
}

func workflowTaskListSortSlots(sortSelectors []serverapi.WorkflowTaskListSort) [5]workflowTaskListSortSlot {
	var slots [5]workflowTaskListSortSlot
	for index, selector := range sortSelectors {
		if index >= len(slots) {
			break
		}
		desc := int64(0)
		if selector.Direction == serverapi.WorkflowTaskListSortDirectionDesc {
			desc = 1
		}
		slots[index] = workflowTaskListSortSlot{field: string(selector.Field), desc: desc}
	}
	return slots
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
