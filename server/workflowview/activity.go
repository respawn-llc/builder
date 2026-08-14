package workflowview

import (
	"context"
	"errors"
	"strings"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/shared/serverapi"
)

type Activity struct {
	queries   *sqlitegen.Queries
	projector *TaskProjector
}

type activityPage struct {
	task       sqlitegen.TaskRecord
	rows       []taskActivityRow
	comments   map[string]sqlitegen.TaskComment
	offsetPage serverapi.WorkflowOffsetPage[taskActivityRow]
}

type taskActivityRow struct {
	activityID       string
	kind             string
	sourceID         string
	occurredAtUnixMs int64
	updatedAtUnixMs  int64
	sessionName      *string
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

func (a *Activity) List(ctx context.Context, req serverapi.WorkflowTaskOffsetPageRequest) (serverapi.WorkflowTaskActivityListResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskActivityListResponse{}, err
	}
	window, err := serverapi.ResolveWorkflowOffsetWindow(req.Offset, req.Limit)
	if err != nil {
		return serverapi.WorkflowTaskActivityListResponse{}, err
	}
	return a.ReadActivity(ctx, req.TaskID, window)
}

func (a *Activity) ReadActivity(ctx context.Context, taskID string, window serverapi.WorkflowOffsetWindow) (serverapi.WorkflowTaskActivityListResponse, error) {
	page, err := a.loadPage(ctx, taskID, window)
	if err != nil {
		return serverapi.WorkflowTaskActivityListResponse{}, err
	}
	items, err := a.itemsFromPage(page)
	if err != nil {
		return serverapi.WorkflowTaskActivityListResponse{}, err
	}
	return serverapi.WorkflowTaskActivityListResponse{
		WorkflowOffsetPage: serverapi.WorkflowOffsetPage[serverapi.WorkflowTaskActivityItem]{
			Items:      items,
			NextOffset: page.offsetPage.NextOffset,
		},
	}, nil
}

func (a *Activity) loadPage(ctx context.Context, taskID string, window serverapi.WorkflowOffsetWindow) (activityPage, error) {
	if a == nil {
		return activityPage{}, errors.New("activity is required")
	}
	task, err := a.queries.GetTask(ctx, taskID)
	if err != nil {
		return activityPage{}, err
	}
	rows, err := a.activityRows(ctx, task.ID, window.Offset, window.Limit+1)
	if err != nil {
		return activityPage{}, err
	}
	offsetPage := serverapi.FinalizeWorkflowOffsetPage(window, rows)
	comments, err := a.commentsByID(ctx, sourceIDsByType(offsetPage.Items, "comment"))
	if err != nil {
		return activityPage{}, err
	}
	return activityPage{
		task:       task,
		rows:       offsetPage.Items,
		comments:   comments,
		offsetPage: offsetPage,
	}, nil
}

func (a *Activity) activityRows(ctx context.Context, taskID string, offset int, limit int) ([]taskActivityRow, error) {
	if limit <= 0 {
		return []taskActivityRow{}, nil
	}
	rows, err := a.queries.ListWorkflowTaskActivityRows(ctx, sqlitegen.ListWorkflowTaskActivityRowsParams{
		PageLimit:  int64(limit),
		PageOffset: int64(offset),
		TaskID:     taskID,
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
