package workflowview

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/shared/serverapi"
)

type Activity struct {
	queries     *sqlitegen.Queries
	definitions *DefinitionProjection
}

type activityPage struct {
	task          sqlitegen.TaskRecord
	rows          []taskActivityRow
	comments      map[string]sqlitegen.TaskComment
	transitions   map[string]sqlitegen.TaskTransitionRecord
	edges         map[string][]sqlitegen.TaskTransitionEdgeRecord
	runs          map[string]sqlitegen.TaskRunRecord
	nodes         map[string]serverapi.WorkflowNode
	sessionNames  map[string]string
	nextPageToken string
}

type taskActivityRow struct {
	activityID       string
	kind             string
	sourceID         string
	occurredAtUnixMs int64
	updatedAtUnixMs  int64
	actor            string
}

type activityPageCursor struct {
	occurredAtUnixMs int64
	activityID       string
	hasValue         bool
}

func NewActivity(metadataStore *metadata.Store, definitions *DefinitionProjection) (*Activity, error) {
	if metadataStore == nil || metadataStore.Queries() == nil {
		return nil, errors.New("metadata store is required")
	}
	if definitions == nil {
		return nil, errors.New("definition projection is required")
	}
	return &Activity{
		queries:     metadataStore.Queries(),
		definitions: definitions,
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
	snapshot, err := a.definitions.snapshot(ctx, task.WorkflowID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
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
	transitions, err := a.transitionsByID(ctx, sourceIDsByType(pageRows, "transition"))
	if err != nil {
		return activityPage{}, err
	}
	edges, err := loadTransitionEdgesByTransitionID(ctx, a.queries, transitions)
	if err != nil {
		return activityPage{}, err
	}
	runs, err := a.runsByID(ctx, sourceIDsByTypes(pageRows, "run_started", "run_completed", "run_interrupted"))
	if err != nil {
		return activityPage{}, err
	}
	sessionNames, err := loadSessionNamesByRun(ctx, a.queries, runs)
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
		transitions:   taskTransitionByID(transitions),
		edges:         edges,
		runs:          taskRunByID(runs),
		nodes:         workflowNodeByID(snapshot.api),
		sessionNames:  sessionNames,
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
			actor:            row.Actor,
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

func (a *Activity) transitionsByID(ctx context.Context, ids []string) ([]sqlitegen.TaskTransitionRecord, error) {
	if len(ids) == 0 {
		return []sqlitegen.TaskTransitionRecord{}, nil
	}
	return a.queries.ListTaskTransitionsByIDs(ctx, ids)
}

func (a *Activity) runsByID(ctx context.Context, ids []string) ([]sqlitegen.TaskRunRecord, error) {
	if len(ids) == 0 {
		return []sqlitegen.TaskRunRecord{}, nil
	}
	return a.queries.ListTaskRunsByIDs(ctx, ids)
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

func sourceIDsByTypes(rows []taskActivityRow, kinds ...string) []string {
	allowed := map[string]bool{}
	for _, kind := range kinds {
		allowed[kind] = true
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, row := range rows {
		if !allowed[row.kind] || seen[row.sourceID] {
			continue
		}
		ids = append(ids, row.sourceID)
		seen[row.sourceID] = true
	}
	return ids
}

func taskTransitionByID(transitions []sqlitegen.TaskTransitionRecord) map[string]sqlitegen.TaskTransitionRecord {
	out := make(map[string]sqlitegen.TaskTransitionRecord, len(transitions))
	for _, transition := range transitions {
		out[transition.ID] = transition
	}
	return out
}

func taskRunByID(runs []sqlitegen.TaskRunRecord) map[string]sqlitegen.TaskRunRecord {
	out := make(map[string]sqlitegen.TaskRunRecord, len(runs))
	for _, run := range runs {
		out[run.ID] = run
	}
	return out
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
