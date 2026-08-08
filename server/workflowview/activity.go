package workflowview

import (
	"context"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/shared/serverapi"
)

type Activity struct {
	queries   *sqlitegen.Queries
	projector *TaskProjector
}

type activityPage struct {
	task          sqlitegen.TaskRecord
	rows          []taskActivityRow
	comments      map[string]sqlitegen.TaskComment
	nextPageToken string
}

type taskActivityRow struct {
	activityID       string
	kind             string
	sourceID         string
	occurredAtUnixMs int64
	updatedAtUnixMs  int64
	sessionName      *string
}

type activityPageCursor struct {
	occurredAtUnixMs int64
	activityID       string
	hasValue         bool
}

func NewActivity(metadataStore *metadata.Store, projector *TaskProjector) (*Activity, error) {
	if metadataStore == nil || metadataStore.Queries() == nil {
		return nil, errors.New("metadata store is required")
	}
	if projector == nil {
		return nil, errors.New("task projector is required")
	}
	return &Activity{queries: metadataStore.Queries(), projector: projector}, nil
}

func (a *Activity) List(ctx context.Context, req serverapi.WorkflowTaskActivityListRequest) (serverapi.WorkflowTaskActivityListResponse, error) {
	page, err := a.loadPage(ctx, req)
	if err != nil {
		return serverapi.WorkflowTaskActivityListResponse{}, err
	}
	items, err := a.itemsFromPage(page)
	if err != nil {
		return serverapi.WorkflowTaskActivityListResponse{}, err
	}
	return serverapi.WorkflowTaskActivityListResponse{
		Items:             items,
		NextPageToken:     page.nextPageToken,
		GeneratedAtUnixMs: time.Now().UTC().UnixMilli(),
	}, nil
}

func (a *Activity) loadPage(ctx context.Context, req serverapi.WorkflowTaskActivityListRequest) (activityPage, error) {
	if a == nil {
		return activityPage{}, errors.New("activity is required")
	}
	if err := req.Validate(); err != nil {
		return activityPage{}, err
	}
	task, err := a.queries.GetTask(ctx, strings.TrimSpace(req.TaskID))
	if err != nil {
		return activityPage{}, err
	}
	pageSize := req.PageSize
	if pageSize == 0 {
		pageSize = 50
	}
	cursor, err := parseActivityPageToken(req.PageToken)
	if err != nil {
		return activityPage{}, err
	}
	rows, err := a.activityRows(ctx, task.ID, cursor, pageSize+1)
	if err != nil {
		return activityPage{}, err
	}
	pageRows := rows
	hasNext := len(rows) > pageSize
	if hasNext {
		pageRows = rows[:pageSize]
	}
	comments, err := a.commentsByID(ctx, sourceIDsByType(pageRows, "comment"))
	if err != nil {
		return activityPage{}, err
	}
	nextPageToken := ""
	if hasNext && len(pageRows) > 0 {
		nextPageToken = activityPageToken(pageRows[len(pageRows)-1])
	}
	return activityPage{
		task:          task,
		rows:          pageRows,
		comments:      comments,
		nextPageToken: nextPageToken,
	}, nil
}

func (a *Activity) activityRows(ctx context.Context, taskID string, cursor activityPageCursor, limit int) ([]taskActivityRow, error) {
	if limit <= 0 {
		return []taskActivityRow{}, nil
	}
	cursorActive := int64(0)
	if cursor.hasValue {
		cursorActive = 1
	}
	rows, err := a.queries.ListWorkflowTaskActivityRows(ctx, sqlitegen.ListWorkflowTaskActivityRowsParams{
		PageLimit:              int64(limit),
		TaskID:                 taskID,
		CursorActive:           cursorActive,
		CursorOccurredAtUnixMs: cursor.occurredAtUnixMs,
		CursorActivityID:       cursor.activityID,
	})
	if err != nil {
		return nil, err
	}
	out := make([]taskActivityRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, taskActivityRow{
			activityID:       row.ActivityID,
			kind:             row.Kind,
			sourceID:         row.SourceID,
			occurredAtUnixMs: row.OccurredAtUnixMs,
			updatedAtUnixMs:  row.UpdatedAtUnixMs,
			sessionName:      metadata.OptionalString(row.SessionName),
		})
	}
	return out, nil
}

func (a *Activity) commentsByID(ctx context.Context, ids []string) (map[string]sqlitegen.TaskComment, error) {
	out := map[string]sqlitegen.TaskComment{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := a.queries.ListTaskCommentsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ID] = row
	}
	return out, nil
}

func (a *Activity) itemsFromPage(page activityPage) ([]serverapi.WorkflowTaskActivityItem, error) {
	items := make([]serverapi.WorkflowTaskActivityItem, 0, len(page.rows))
	for _, row := range page.rows {
		item := serverapi.WorkflowTaskActivityItem{
			ActivityID:       row.activityID,
			Type:             row.kind,
			TaskID:           page.task.ID,
			OccurredAtUnixMs: row.occurredAtUnixMs,
			UpdatedAtUnixMs:  row.updatedAtUnixMs,
		}
		switch row.kind {
		case "comment":
			comment, ok := page.comments[row.sourceID]
			if !ok {
				return nil, errors.New("activity comment source is missing")
			}
			dto := a.projector.ProjectComment(comment)
			item.Comment = &dto
		case "session_started":
			if row.sessionName == nil || strings.TrimSpace(*row.sessionName) == "" {
				return nil, errors.New("activity session source has no name")
			}
			item.SessionStarted = &serverapi.WorkflowTaskSessionStarted{SessionID: row.sourceID, Name: *row.sessionName}
		default:
			return nil, errors.New("activity kind is unsupported")
		}
		items = append(items, item)
	}
	return items, nil
}

func sourceIDsByType(rows []taskActivityRow, kind string) []string {
	ids := []string{}
	seen := map[string]bool{}
	for _, row := range rows {
		if row.kind != kind || seen[row.sourceID] {
			continue
		}
		ids = append(ids, row.sourceID)
		seen[row.sourceID] = true
	}
	return ids
}

func parseActivityPageToken(token string) (activityPageCursor, error) {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return activityPageCursor{}, nil
	}
	timestampPart, encodedID, ok := strings.Cut(trimmed, "|")
	if !ok {
		return activityPageCursor{}, ErrInvalidPageToken
	}
	occurredAt, err := strconv.ParseInt(timestampPart, 10, 64)
	if err != nil || occurredAt < 0 {
		return activityPageCursor{}, ErrInvalidPageToken
	}
	decodedID, err := base64.RawURLEncoding.DecodeString(encodedID)
	if err != nil || strings.TrimSpace(string(decodedID)) == "" {
		return activityPageCursor{}, ErrInvalidPageToken
	}
	return activityPageCursor{occurredAtUnixMs: occurredAt, activityID: string(decodedID), hasValue: true}, nil
}

func activityPageToken(row taskActivityRow) string {
	return strconv.FormatInt(row.occurredAtUnixMs, 10) + "|" + base64.RawURLEncoding.EncodeToString([]byte(row.activityID))
}
