package workflowview

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/shared/labelcontract"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type Board struct {
	metadata     *metadata.Store
	queries      *sqlitegen.Queries
	definitions  *DefinitionProjection
	roleResolver workflow.RoleResolver
	projector    *TaskProjector
	authority    *sessionruntime.Authority
	quiescence   TaskQuiescenceSource
}

func NewBoard(metadataStore *metadata.Store, definitions *DefinitionProjection, roleResolver workflow.RoleResolver, projector *TaskProjector, authority *sessionruntime.Authority, quiescence TaskQuiescenceSource) (*Board, error) {
	if metadataStore == nil || metadataStore.Queries() == nil {
		return nil, errors.New("metadata store is required")
	}
	if definitions == nil {
		return nil, errors.New("definition projection is required")
	}
	if roleResolver == nil {
		return nil, errors.New("role resolver is required")
	}
	if projector == nil {
		return nil, errors.New("task projector is required")
	}
	if authority == nil {
		return nil, errors.New("session runtime authority is required")
	}
	if quiescence == nil {
		return nil, errors.New("task quiescence source is required")
	}
	return &Board{
		metadata:     metadataStore,
		queries:      metadataStore.Queries(),
		definitions:  definitions,
		roleResolver: roleResolver,
		projector:    projector,
		authority:    authority,
		quiescence:   quiescence,
	}, nil
}

func (b *Board) Get(ctx context.Context, req serverapi.WorkflowBoardRequest) (serverapi.WorkflowBoard, error) {
	if b == nil {
		return serverapi.WorkflowBoard{}, errors.New("board is required")
	}
	if err := req.ValidateRPC(); err != nil {
		return serverapi.WorkflowBoard{}, err
	}
	projectID := strings.TrimSpace(req.ProjectID)
	if projectID == "" {
		return serverapi.WorkflowBoard{}, errors.New("project_id is required")
	}
	definitions, picker, err := b.selectionInputs(ctx, projectID)
	if err != nil {
		return serverapi.WorkflowBoard{}, err
	}
	project, err := b.metadata.GetProjectOverview(ctx, projectID)
	if err != nil {
		return serverapi.WorkflowBoard{}, err
	}
	labelFilter, err := resolveWorkflowTaskLabelFilter(ctx, b.queries, projectID, req.LabelFilter)
	if err != nil {
		return serverapi.WorkflowBoard{}, err
	}
	workspaceContext := boardProjectWorkspaceContext(project)
	selected := workflowBoardSelectorFromRequest(req).selectFrom(picker)
	if selected == nil {
		return serverapi.WorkflowBoard{
			ProjectID:         projectID,
			Project:           projectBoardProject(project, workspaceContext),
			WorkflowPicker:    picker,
			GeneratedAtUnixMs: time.Now().UTC().UnixMilli(),
		}, nil
	}
	snapshot := definitions[selected.WorkflowID.String()]
	groups := boardGroups(snapshot.api)
	columns := boardColumns(snapshot)
	if err := b.applyColumnTaskCounts(ctx, columns, projectID, selected.WorkflowID, labelFilter); err != nil {
		return serverapi.WorkflowBoard{}, err
	}
	return serverapi.WorkflowBoard{
		ProjectID:         projectID,
		Project:           projectBoardProject(project, workspaceContext),
		SelectedWorkflow:  selected,
		WorkflowPicker:    picker,
		Groups:            groups,
		Columns:           columns,
		GeneratedAtUnixMs: time.Now().UTC().UnixMilli(),
	}, nil
}

func (b *Board) ListNodeCards(ctx context.Context, req serverapi.WorkflowBoardNodeCardsListRequest) (serverapi.WorkflowBoardNodeCardsListResponse, error) {
	if b == nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, errors.New("board is required")
	}
	if err := req.ValidateRPC(); err != nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, err
	}
	projectID := strings.TrimSpace(req.ProjectID)
	workflowID := req.WorkflowID
	workflowIDString := workflowID.String()
	workflowIDValue, err := workflowID.Value()
	if err != nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, err
	}
	workflowIDBlob, ok := workflowIDValue.([]byte)
	if !ok {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, errors.New("workflow_id database value is not a BLOB")
	}
	nodeID := strings.TrimSpace(req.NodeID)
	project, err := b.metadata.GetProjectOverview(ctx, projectID)
	if err != nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, err
	}
	labelFilter, err := resolveWorkflowTaskLabelFilter(ctx, b.queries, projectID, req.LabelFilter)
	if err != nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, err
	}
	labelFilterArgs, err := labelFilter.queryArgs()
	if err != nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, err
	}
	snapshot, err := b.definitions.snapshot(ctx, workflowID)
	if err != nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, err
	}
	definition := snapshot.api
	if _, ok := workflowNodesByID(definition)[nodeID]; !ok {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, errors.New("node_id is invalid for workflow")
	}
	pageSize := req.PageSize
	if pageSize == 0 {
		pageSize = serverapi.WorkflowBoardNodeCardsMaxPageSize
	}
	sort := normalizeBoardNodeCardsSort(req.Sort)
	cursor, err := parseBoardNodeCardsPageToken(req.PageToken, projectID, workflowIDString, nodeID, labelFilter, sort)
	if err != nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, err
	}
	cursorSortValue := sql.NullString{}
	cursorTaskSeq := sql.NullInt64{}
	cursorUnlabeled := sql.NullInt64{}
	cursorUpdatedAtUnixMs := sql.NullInt64{}
	if cursor.anchor != nil {
		if sort.Field != serverapi.WorkflowBoardNodeCardsSortFieldLabels || cursor.anchor.labelOrdinals != nil {
			cursorSortValue = sql.NullString{String: cursor.anchor.sortValue(sort), Valid: true}
		}
		cursorTaskSeq = sql.NullInt64{Int64: cursor.anchor.taskSeq, Valid: true}
		if cursor.anchor.updatedAtUnixMs != nil {
			cursorUpdatedAtUnixMs = sql.NullInt64{Int64: *cursor.anchor.updatedAtUnixMs, Valid: true}
		}
		if cursor.anchor.unlabeled != nil {
			cursorUnlabeled = sql.NullInt64{Int64: boolInt64(*cursor.anchor.unlabeled), Valid: true}
		}
	}
	var tasks []boardNodeCardsPageTask
	if sort.Field == serverapi.WorkflowBoardNodeCardsSortFieldUpdated {
		projectWorkflowLinkID, linkErr := b.projectWorkflowLinkID(ctx, projectID, workflowID)
		if linkErr != nil {
			return serverapi.WorkflowBoardNodeCardsListResponse{}, linkErr
		}
		if sort.Direction == serverapi.WorkflowTaskListSortDirectionDesc {
			if cursor.direction == boardNodeCardsPageDirectionNewer {
				rows, queryErr := b.queries.ListBoardNodeTasksUpdatedDescPrevious(ctx, sqlitegen.ListBoardNodeTasksUpdatedDescPreviousParams{
					ProjectID:             projectID,
					WorkflowID:            workflowIDBlob,
					ProjectWorkflowLinkID: projectWorkflowLinkID,
					NodeID:                nodeID,
					LabelFilterKind:       labelFilterArgs.kind,
					LabelFilterMode:       labelFilterArgs.mode,
					LabelIdsJson:          labelFilterArgs.labelIDsJSON,
					ExcludedLabelIdsJson:  labelFilterArgs.excludedLabelIDsJSON,
					CursorTaskSeq:         cursorTaskSeq,
					CursorUpdatedAtUnixMs: cursorUpdatedAtUnixMs,
					LimitRows:             int64(pageSize + 1),
				})
				if queryErr != nil {
					return serverapi.WorkflowBoardNodeCardsListResponse{}, queryErr
				}
				tasks = boardNodeTaskRecordsUpdated(rows)
			} else {
				rows, queryErr := b.queries.ListBoardNodeTasksUpdatedDesc(ctx, sqlitegen.ListBoardNodeTasksUpdatedDescParams{
					ProjectID:             projectID,
					WorkflowID:            workflowIDBlob,
					ProjectWorkflowLinkID: projectWorkflowLinkID,
					NodeID:                nodeID,
					LabelFilterKind:       labelFilterArgs.kind,
					LabelFilterMode:       labelFilterArgs.mode,
					LabelIdsJson:          labelFilterArgs.labelIDsJSON,
					ExcludedLabelIdsJson:  labelFilterArgs.excludedLabelIDsJSON,
					CursorDirection:       string(cursor.direction),
					CursorTaskSeq:         cursorTaskSeq,
					CursorUpdatedAtUnixMs: cursorUpdatedAtUnixMs,
					LimitRows:             int64(pageSize + 1),
				})
				if queryErr != nil {
					return serverapi.WorkflowBoardNodeCardsListResponse{}, queryErr
				}
				tasks = boardNodeTaskRecordsUpdated(rows)
			}
		} else {
			if cursor.direction == boardNodeCardsPageDirectionNewer {
				rows, queryErr := b.queries.ListBoardNodeTasksUpdatedAscPrevious(ctx, sqlitegen.ListBoardNodeTasksUpdatedAscPreviousParams{
					ProjectID:             projectID,
					WorkflowID:            workflowIDBlob,
					ProjectWorkflowLinkID: projectWorkflowLinkID,
					NodeID:                nodeID,
					LabelFilterKind:       labelFilterArgs.kind,
					LabelFilterMode:       labelFilterArgs.mode,
					LabelIdsJson:          labelFilterArgs.labelIDsJSON,
					ExcludedLabelIdsJson:  labelFilterArgs.excludedLabelIDsJSON,
					CursorTaskSeq:         cursorTaskSeq,
					CursorUpdatedAtUnixMs: cursorUpdatedAtUnixMs,
					LimitRows:             int64(pageSize + 1),
				})
				if queryErr != nil {
					return serverapi.WorkflowBoardNodeCardsListResponse{}, queryErr
				}
				tasks = boardNodeTaskRecordsUpdated(rows)
			} else {
				rows, queryErr := b.queries.ListBoardNodeTasksUpdatedAsc(ctx, sqlitegen.ListBoardNodeTasksUpdatedAscParams{
					ProjectID:             projectID,
					WorkflowID:            workflowIDBlob,
					ProjectWorkflowLinkID: projectWorkflowLinkID,
					NodeID:                nodeID,
					LabelFilterKind:       labelFilterArgs.kind,
					LabelFilterMode:       labelFilterArgs.mode,
					LabelIdsJson:          labelFilterArgs.labelIDsJSON,
					ExcludedLabelIdsJson:  labelFilterArgs.excludedLabelIDsJSON,
					CursorDirection:       string(cursor.direction),
					CursorTaskSeq:         cursorTaskSeq,
					CursorUpdatedAtUnixMs: cursorUpdatedAtUnixMs,
					LimitRows:             int64(pageSize + 1),
				})
				if queryErr != nil {
					return serverapi.WorkflowBoardNodeCardsListResponse{}, queryErr
				}
				tasks = boardNodeTaskRecordsUpdated(rows)
			}
		}
	} else {
		rows, queryErr := b.queries.ListBoardNodeTasksGeneralized(ctx, sqlitegen.ListBoardNodeTasksGeneralizedParams{
			ProjectID:            projectID,
			WorkflowID:           workflowID,
			NodeID:               nodeID,
			LabelFilterKind:      labelFilterArgs.kind,
			LabelFilterMode:      labelFilterArgs.mode,
			LabelIdsJson:         labelFilterArgs.labelIDsJSON,
			ExcludedLabelIdsJson: labelFilterArgs.excludedLabelIDsJSON,
			SortField:            string(sort.Field),
			SortDescending:       boolInt64(sort.Direction == serverapi.WorkflowTaskListSortDirectionDesc),
			CursorDirection:      string(cursor.direction),
			CursorSortValue:      cursorSortValue,
			CursorTaskSeq:        cursorTaskSeq,
			CursorUnlabeled:      cursorUnlabeled,
			LimitRows:            int64(pageSize + 1),
		})
		if queryErr != nil {
			return serverapi.WorkflowBoardNodeCardsListResponse{}, queryErr
		}
		tasks = boardNodeTaskRecordsGeneralized(rows)
	}
	hasExtra := len(tasks) > pageSize
	if hasExtra {
		tasks = tasks[:pageSize]
	}
	if cursor.direction == boardNodeCardsPageDirectionNewer {
		slices.Reverse(tasks)
	}
	workspaceContext := boardProjectWorkspaceContext(project)
	taskRecords := boardNodeCardTaskRecords(tasks)
	currentNodesByTaskID, err := b.currentNodesByTask(ctx, taskRecords)
	if err != nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, err
	}
	taskIDs := taskIDs(taskRecords)
	dependencyProgressByTaskID, err := b.dependencyProgressByTaskID(ctx, taskIDs)
	if err != nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, err
	}
	statusesByTaskID, err := loadWorkflowTaskStatusFacts(ctx, b.queries, b.projector, taskIDs)
	if err != nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, err
	}
	liveExecutionsByTaskID, err := b.liveExecutionsByTask(ctx, projectID, workflowID, taskIDs)
	if err != nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, err
	}
	quiescenceByTaskID, err := b.quiescence.CurrentTaskQuiescence(workflowTaskIDs(taskIDs))
	if err != nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, err
	}
	labelIDsByTask, err := loadTaskLabelIDsByTask(ctx, b.queries, taskIDs)
	if err != nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, err
	}
	cards := make([]serverapi.WorkflowBoardTaskCard, 0, len(tasks))
	for _, task := range tasks {
		canDelete, exists := quiescenceByTaskID[workflow.TaskID(task.task.ID)]
		if !exists {
			return serverapi.WorkflowBoardNodeCardsListResponse{}, fmt.Errorf("workflow execution omitted Task %q from Quiescence snapshot", task.task.ID)
		}
		status := taskDetailStatusFact(statusesByTaskID[task.task.ID], liveExecutionsByTaskID[task.task.ID])
		card, _ := b.card(
			task.task,
			status,
			currentNodesByTaskID[task.task.ID],
			liveExecutionsByTaskID[task.task.ID],
			canDelete,
			labelIDsByTask[task.task.ID],
			snapshot,
			sourceWorkspaceForTask(task.task, workspaceContext.byID, workspaceContext.primary),
			dependencyProgressByTaskID[task.task.ID],
		)
		cards = append(cards, card)
	}
	previousPageToken, nextPageToken, err := boardNodeCardsPageTokens(projectID, workflowIDString, nodeID, labelFilter, sort, cursor, tasks, hasExtra)
	if err != nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, err
	}
	return serverapi.WorkflowBoardNodeCardsListResponse{
		ProjectID:         projectID,
		WorkflowID:        workflowID,
		NodeID:            nodeID,
		Cards:             cards,
		PreviousPageToken: previousPageToken,
		NextPageToken:     nextPageToken,
		GeneratedAtUnixMs: time.Now().UTC().UnixMilli(),
	}, nil
}

func (b *Board) liveExecutionsByTask(ctx context.Context, projectID string, workflowID runtimeids.WorkflowID, taskIDs []string) (map[string][]sessionruntime.TaskExecution, error) {
	if len(taskIDs) == 0 {
		return map[string][]sessionruntime.TaskExecution{}, nil
	}
	snapshots, err := b.authority.CurrentScopedTaskExecutionSnapshots(
		projectID, workflowID, workflowTaskIDs(taskIDs),
	)
	if err != nil {
		return nil, err
	}
	currentByTaskID := make(map[string][]sessionruntime.TaskExecution, len(taskIDs))
	for _, taskID := range taskIDs {
		currentByTaskID[taskID] = currentTaskExecutions(snapshots[workflow.TaskID(taskID)].Executions)
	}
	return currentByTaskID, nil
}

func (b *Board) currentNodesByTask(ctx context.Context, tasks []sqlitegen.TaskRecord) (map[string][]workflow.CurrentNode, error) {
	taskIDs := taskIDs(tasks)
	if len(taskIDs) == 0 {
		return map[string][]workflow.CurrentNode{}, nil
	}
	currentNodes, err := b.definitions.CurrentNodesByTask(ctx, workflowTaskIDs(taskIDs))
	if err != nil {
		return nil, err
	}
	byTaskID := make(map[string][]workflow.CurrentNode, len(tasks))
	for _, taskID := range taskIDs {
		byTaskID[taskID] = currentNodes[workflow.TaskID(taskID)]
	}
	return byTaskID, nil
}

func (b *Board) card(task sqlitegen.TaskRecord, status workflowTaskStatusFact, currentNodes []workflow.CurrentNode, liveExecutions []sessionruntime.TaskExecution, canDelete bool, labelIDs []string, definition definitionSnapshot, sourceWorkspace serverapi.ProjectWorkspaceSummary, dependencyProgress *serverapi.WorkflowTaskDependencyProgress) (serverapi.WorkflowBoardTaskCard, bool) {
	facts := b.projector.ProjectTaskFacts(TaskFactsInput{
		Task:           task,
		Status:         status,
		CurrentNodes:   currentNodes,
		LiveExecutions: liveExecutions,
		Definition:     definition,
		CanDelete:      canDelete,
	})
	return serverapi.WorkflowBoardTaskCard{
		TaskID:             task.ID,
		ShortID:            task.ShortID,
		Title:              task.Title,
		Preview:            markdownPreview(task.Body),
		WorkflowID:         task.WorkflowID,
		ActiveNodeIDs:      append([]string(nil), facts.Status.NodeIDs...),
		SourceWorkspace:    sourceWorkspace,
		Status:             facts.Status,
		Actions:            facts.Actions,
		LabelIDs:           labelIDs,
		DependencyProgress: dependencyProgress,
		UpdatedAtUnixMs:    task.UpdatedAtUnixMs,
	}, facts.Done
}

func (b *Board) dependencyProgressByTaskID(ctx context.Context, taskIDs []string) (map[string]*serverapi.WorkflowTaskDependencyProgress, error) {
	if len(taskIDs) == 0 {
		return map[string]*serverapi.WorkflowTaskDependencyProgress{}, nil
	}
	rows, err := b.queries.ListTaskDependencyProgressByTasks(ctx, taskIDs)
	if err != nil {
		return nil, err
	}
	progress := make(map[string]*serverapi.WorkflowTaskDependencyProgress, len(rows))
	for _, row := range rows {
		if row.DependencyTotalCount < 1 || row.DependencySatisfiedCount < 0 || row.DependencySatisfiedCount > row.DependencyTotalCount {
			return nil, fmt.Errorf("board task %q dependency aggregate is invalid: satisfied=%d total=%d", row.TaskID, row.DependencySatisfiedCount, row.DependencyTotalCount)
		}
		progress[row.TaskID] = &serverapi.WorkflowTaskDependencyProgress{
			SatisfiedCount: int(row.DependencySatisfiedCount),
			TotalCount:     int(row.DependencyTotalCount),
		}
	}
	return progress, nil
}

func taskIDs(tasks []sqlitegen.TaskRecord) []string {
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}
	return ids
}

func workflowTaskIDs(taskIDs []string) []workflow.TaskID {
	ids := make([]workflow.TaskID, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		ids = append(ids, workflow.TaskID(taskID))
	}
	return ids
}

type boardNodeCardsPageTask struct {
	task          sqlitegen.TaskRecord
	labelOrdinals *string
	unlabeled     bool
}

func boardNodeCardsPageTasks[T any](rows []T, mapRow func(T) boardNodeCardsPageTask) []boardNodeCardsPageTask {
	tasks := make([]boardNodeCardsPageTask, 0, len(rows))
	for _, row := range rows {
		tasks = append(tasks, mapRow(row))
	}
	return tasks
}

func boardNodeTaskRecordsGeneralized(rows []sqlitegen.ListBoardNodeTasksGeneralizedRow) []boardNodeCardsPageTask {
	return boardNodeCardsPageTasks(rows, func(row sqlitegen.ListBoardNodeTasksGeneralizedRow) boardNodeCardsPageTask {
		return boardNodeCardsPageTask{task: sqlitegen.TaskRecord{
			ID:                          row.ID,
			ProjectID:                   row.ProjectID,
			ProjectWorkflowLinkID:       row.ProjectWorkflowLinkID,
			WorkflowID:                  row.WorkflowID,
			WorkflowRevisionSeen:        row.WorkflowRevisionSeen,
			TaskSeq:                     row.TaskSeq,
			ShortID:                     row.ShortID,
			Title:                       row.Title,
			Body:                        row.Body,
			SourceUrl:                   row.SourceUrl,
			SourceWorkspaceID:           row.SourceWorkspaceID,
			ManagedWorktreeID:           row.ManagedWorktreeID,
			ExecutionTargetMode:         row.ExecutionTargetMode,
			ExecutionTargetRequestedRef: row.ExecutionTargetRequestedRef,
			ExecutionTargetResolvedRef:  row.ExecutionTargetResolvedRef,
			ExecutionTargetCommitOid:    row.ExecutionTargetCommitOid,
			ExecutionTargetProvenance:   row.ExecutionTargetProvenance,
			CreatedAtUnixMs:             row.CreatedAtUnixMs,
			UpdatedAtUnixMs:             row.UpdatedAtUnixMs,
			MetadataJson:                row.MetadataJson,
		}, labelOrdinals: labelOrdinalKey(row.LabelOrdinals), unlabeled: row.LabelsUnlabeled != 0}
	})
}

type boardUpdatedTaskRow interface {
	sqlitegen.ListBoardNodeTasksUpdatedAscRow |
		sqlitegen.ListBoardNodeTasksUpdatedAscPreviousRow |
		sqlitegen.ListBoardNodeTasksUpdatedDescRow |
		sqlitegen.ListBoardNodeTasksUpdatedDescPreviousRow
}

// All updated-task queries intentionally select the same shape. Converting
// their generated row types to this shape makes query-shape drift fail at compile time.
type boardUpdatedTaskRowShape sqlitegen.ListBoardNodeTasksUpdatedAscRow

func boardUpdatedTaskRecord(row boardUpdatedTaskRowShape) sqlitegen.TaskRecord {
	var workflowID runtimeids.WorkflowID
	err := workflowID.Scan(row.WorkflowID)
	if err != nil {
		panic(fmt.Sprintf("board updated task row has invalid workflow_id: %v", err))
	}
	return sqlitegen.TaskRecord{
		ID:                          row.ID,
		ProjectID:                   row.ProjectID,
		ProjectWorkflowLinkID:       row.ProjectWorkflowLinkID,
		WorkflowID:                  workflowID,
		WorkflowRevisionSeen:        row.WorkflowRevisionSeen,
		TaskSeq:                     row.TaskSeq,
		ShortID:                     row.ShortID,
		Title:                       row.Title,
		Body:                        row.Body,
		SourceUrl:                   row.SourceUrl,
		SourceWorkspaceID:           row.SourceWorkspaceID,
		ManagedWorktreeID:           row.ManagedWorktreeID,
		ExecutionTargetMode:         row.ExecutionTargetMode,
		ExecutionTargetRequestedRef: row.ExecutionTargetRequestedRef,
		ExecutionTargetResolvedRef:  row.ExecutionTargetResolvedRef,
		ExecutionTargetCommitOid:    row.ExecutionTargetCommitOid,
		ExecutionTargetProvenance:   row.ExecutionTargetProvenance,
		CreatedAtUnixMs:             row.CreatedAtUnixMs,
		UpdatedAtUnixMs:             row.UpdatedAtUnixMs,
		MetadataJson:                row.MetadataJson,
	}
}

func boardNodeTaskRecordsUpdated[T boardUpdatedTaskRow](rows []T) []boardNodeCardsPageTask {
	return boardNodeCardsPageTasks(rows, func(row T) boardNodeCardsPageTask {
		return boardNodeCardsPageTask{task: boardUpdatedTaskRecord(boardUpdatedTaskRowShape(row))}
	})
}

func labelOrdinalKey(value any) *string {
	switch typed := value.(type) {
	case string:
		return &typed
	case []byte:
		decoded := string(typed)
		return &decoded
	case sql.NullString:
		if !typed.Valid {
			return nil
		}
		return &typed.String
	case nil:
		return nil
	default:
		panic(fmt.Sprintf("board label ordinal key has unexpected type %T", value))
	}
}

func boardNodeCardTaskRecords(tasks []boardNodeCardsPageTask) []sqlitegen.TaskRecord {
	records := make([]sqlitegen.TaskRecord, 0, len(tasks))
	for _, task := range tasks {
		records = append(records, task.task)
	}
	return records
}

type boardNodeCardsPageCursor struct {
	direction boardNodeCardsPageDirection
	anchor    *boardNodeCardsPageAnchor
}

type boardNodeCardsPageAnchor struct {
	updatedAtUnixMs *int64
	createdAtUnixMs *int64
	labelOrdinals   *string
	unlabeled       *bool
	title           *string
	taskSeq         int64
}

type boardNodeCardsPageDirection string

const (
	boardNodeCardsPageDirectionOlder boardNodeCardsPageDirection = "older"
	boardNodeCardsPageDirectionNewer boardNodeCardsPageDirection = "newer"
	boardNodeCardsPageTokenVersion                               = 4
)

type boardNodeCardsPageTokenPayload struct {
	Version         int                                  `json:"version"`
	ProjectID       string                               `json:"project_id"`
	WorkflowID      string                               `json:"workflow_id"`
	NodeID          string                               `json:"node_id"`
	LabelFilter     workflowTaskLabelFilterFacts         `json:"label_filter"`
	Sort            serverapi.WorkflowBoardNodeCardsSort `json:"sort"`
	UpdatedAtUnixMs *int64                               `json:"updated_at_unix_ms,omitempty"`
	CreatedAtUnixMs *int64                               `json:"created_at_unix_ms,omitempty"`
	LabelOrdinals   *string                              `json:"label_ordinals,omitempty"`
	Unlabeled       *bool                                `json:"unlabeled,omitempty"`
	Title           *string                              `json:"title,omitempty"`
	TaskSeq         int64                                `json:"task_seq"`
	Direction       boardNodeCardsPageDirection          `json:"direction"`
}

func normalizeBoardNodeCardsSort(sort *serverapi.WorkflowBoardNodeCardsSort) serverapi.WorkflowBoardNodeCardsSort {
	if sort == nil {
		return serverapi.WorkflowBoardNodeCardsSort{
			Field:     serverapi.WorkflowBoardNodeCardsSortFieldUpdated,
			Direction: serverapi.WorkflowTaskListSortDirectionDesc,
		}
	}
	return *sort
}

func parseBoardNodeCardsPageToken(token *string, projectID string, workflowID string, nodeID string, labelFilter workflowTaskLabelFilterFacts, sort serverapi.WorkflowBoardNodeCardsSort) (boardNodeCardsPageCursor, error) {
	if token == nil {
		return boardNodeCardsPageCursor{direction: boardNodeCardsPageDirectionOlder}, nil
	}
	if strings.TrimSpace(*token) == "" || strings.TrimSpace(*token) != *token {
		return boardNodeCardsPageCursor{}, ErrInvalidPageToken
	}
	decoded, err := base64.RawURLEncoding.DecodeString(*token)
	if err != nil {
		return boardNodeCardsPageCursor{}, ErrInvalidPageToken
	}
	var payload boardNodeCardsPageTokenPayload
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return boardNodeCardsPageCursor{}, ErrInvalidPageToken
	}
	if payload.Version != boardNodeCardsPageTokenVersion ||
		payload.ProjectID != projectID ||
		payload.WorkflowID != workflowID ||
		payload.NodeID != nodeID ||
		payload.Sort != sort ||
		!payload.LabelFilter.validCanonical() ||
		!payload.LabelFilter.equal(labelFilter) ||
		strings.TrimSpace(payload.ProjectID) == "" ||
		strings.TrimSpace(payload.WorkflowID) == "" ||
		strings.TrimSpace(payload.NodeID) == "" ||
		payload.TaskSeq < 1 ||
		!boardNodeCardsPageAnchorMatchesSort(payload, sort) {
		return boardNodeCardsPageCursor{}, ErrInvalidPageToken
	}
	switch payload.Direction {
	case boardNodeCardsPageDirectionOlder, boardNodeCardsPageDirectionNewer:
	default:
		return boardNodeCardsPageCursor{}, ErrInvalidPageToken
	}
	return boardNodeCardsPageCursor{
		direction: payload.Direction,
		anchor: &boardNodeCardsPageAnchor{
			updatedAtUnixMs: payload.UpdatedAtUnixMs,
			createdAtUnixMs: payload.CreatedAtUnixMs,
			labelOrdinals:   payload.LabelOrdinals,
			unlabeled:       payload.Unlabeled,
			title:           payload.Title,
			taskSeq:         payload.TaskSeq,
		},
	}, nil
}

func boardNodeCardsPageAnchorMatchesSort(payload boardNodeCardsPageTokenPayload, sort serverapi.WorkflowBoardNodeCardsSort) bool {
	if payload.UpdatedAtUnixMs != nil && *payload.UpdatedAtUnixMs < 0 || payload.CreatedAtUnixMs != nil && *payload.CreatedAtUnixMs < 0 {
		return false
	}
	switch sort.Field {
	case serverapi.WorkflowBoardNodeCardsSortFieldUpdated:
		return payload.UpdatedAtUnixMs != nil && payload.CreatedAtUnixMs == nil && payload.LabelOrdinals == nil && payload.Unlabeled == nil && payload.Title == nil
	case serverapi.WorkflowBoardNodeCardsSortFieldCreated:
		return payload.UpdatedAtUnixMs == nil && payload.CreatedAtUnixMs != nil && payload.LabelOrdinals == nil && payload.Unlabeled == nil && payload.Title == nil
	case serverapi.WorkflowBoardNodeCardsSortFieldLabels:
		return payload.UpdatedAtUnixMs == nil && payload.CreatedAtUnixMs == nil && payload.Unlabeled != nil && payload.Title == nil &&
			((*payload.Unlabeled && payload.LabelOrdinals == nil) ||
				(!*payload.Unlabeled && payload.LabelOrdinals != nil && *payload.LabelOrdinals == strings.TrimSpace(*payload.LabelOrdinals)))
	case serverapi.WorkflowBoardNodeCardsSortFieldTitle:
		return payload.UpdatedAtUnixMs == nil && payload.CreatedAtUnixMs == nil && payload.LabelOrdinals == nil && payload.Unlabeled == nil && payload.Title != nil && *payload.Title == boardNodeCardsTitleSortValue(*payload.Title)
	case serverapi.WorkflowBoardNodeCardsSortFieldShortID:
		return payload.UpdatedAtUnixMs == nil && payload.CreatedAtUnixMs == nil && payload.LabelOrdinals == nil && payload.Unlabeled == nil && payload.Title == nil
	default:
		return false
	}
}

func (a boardNodeCardsPageAnchor) sortValue(sort serverapi.WorkflowBoardNodeCardsSort) string {
	switch sort.Field {
	case serverapi.WorkflowBoardNodeCardsSortFieldUpdated:
		return fmt.Sprintf("%020d", *a.updatedAtUnixMs)
	case serverapi.WorkflowBoardNodeCardsSortFieldCreated:
		return fmt.Sprintf("%020d", *a.createdAtUnixMs)
	case serverapi.WorkflowBoardNodeCardsSortFieldLabels:
		if a.labelOrdinals == nil {
			return ""
		}
		return *a.labelOrdinals
	case serverapi.WorkflowBoardNodeCardsSortFieldTitle:
		return *a.title
	case serverapi.WorkflowBoardNodeCardsSortFieldShortID:
		return fmt.Sprintf("%020d", a.taskSeq)
	default:
		panic(fmt.Sprintf("board node cards sort invariant violated: unsupported field %q", sort.Field))
	}
}

func boardNodeCardsPageTokens(projectID string, workflowID string, nodeID string, labelFilter workflowTaskLabelFilterFacts, sort serverapi.WorkflowBoardNodeCardsSort, cursor boardNodeCardsPageCursor, tasks []boardNodeCardsPageTask, hasExtra bool) (*string, *string, error) {
	if len(tasks) == 0 {
		return nil, nil, nil
	}
	first := tasks[0]
	last := tasks[len(tasks)-1]
	var previousPageToken *string
	var nextPageToken *string
	var err error
	switch cursor.direction {
	case boardNodeCardsPageDirectionOlder:
		if cursor.anchor != nil {
			previousPageToken, err = boardNodeCardsPageToken(projectID, workflowID, nodeID, labelFilter, sort, boardNodeCardsPageDirectionNewer, first)
			if err != nil {
				return nil, nil, err
			}
		}
		if hasExtra {
			nextPageToken, err = boardNodeCardsPageToken(projectID, workflowID, nodeID, labelFilter, sort, boardNodeCardsPageDirectionOlder, last)
			if err != nil {
				return nil, nil, err
			}
		}
	case boardNodeCardsPageDirectionNewer:
		if hasExtra {
			previousPageToken, err = boardNodeCardsPageToken(projectID, workflowID, nodeID, labelFilter, sort, boardNodeCardsPageDirectionNewer, first)
			if err != nil {
				return nil, nil, err
			}
		}
		nextPageToken, err = boardNodeCardsPageToken(projectID, workflowID, nodeID, labelFilter, sort, boardNodeCardsPageDirectionOlder, last)
		if err != nil {
			return nil, nil, err
		}
	default:
		return nil, nil, ErrInvalidPageToken
	}
	return previousPageToken, nextPageToken, nil
}

func boardNodeCardsPageToken(projectID string, workflowID string, nodeID string, labelFilter workflowTaskLabelFilterFacts, sort serverapi.WorkflowBoardNodeCardsSort, direction boardNodeCardsPageDirection, task boardNodeCardsPageTask) (*string, error) {
	payload := boardNodeCardsPageTokenPayload{
		Version:     boardNodeCardsPageTokenVersion,
		ProjectID:   projectID,
		WorkflowID:  workflowID,
		NodeID:      nodeID,
		LabelFilter: labelFilter,
		Sort:        sort,
		TaskSeq:     task.task.TaskSeq,
		Direction:   direction,
	}
	switch sort.Field {
	case serverapi.WorkflowBoardNodeCardsSortFieldUpdated:
		payload.UpdatedAtUnixMs = &task.task.UpdatedAtUnixMs
	case serverapi.WorkflowBoardNodeCardsSortFieldCreated:
		payload.CreatedAtUnixMs = &task.task.CreatedAtUnixMs
	case serverapi.WorkflowBoardNodeCardsSortFieldLabels:
		payload.LabelOrdinals = task.labelOrdinals
		payload.Unlabeled = &task.unlabeled
	case serverapi.WorkflowBoardNodeCardsSortFieldTitle:
		title := boardNodeCardsTitleSortValue(task.task.Title)
		payload.Title = &title
	case serverapi.WorkflowBoardNodeCardsSortFieldShortID:
	default:
		return nil, fmt.Errorf("board node cards sort invariant violated: unsupported field %q", sort.Field)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(encoded)
	return &token, nil
}

func boardNodeCardsTitleSortValue(title string) string {
	return labelcontract.Fold(title)
}

func (b *Board) selectionInputs(ctx context.Context, projectID string) (map[string]definitionSnapshot, []serverapi.WorkflowPickerItem, error) {
	links, err := b.queries.ListProjectWorkflowLinks(ctx, projectID)
	if err != nil {
		return nil, nil, err
	}
	taskActivityRows, err := b.queries.ListProjectWorkflowTaskActivity(ctx, projectID)
	if err != nil {
		return nil, nil, err
	}
	workflowIDs := make([]runtimeids.WorkflowID, 0, len(links))
	linkByWorkflowID := map[runtimeids.WorkflowID]sqlitegen.ProjectWorkflowLinkRecord{}
	for _, link := range links {
		if _, exists := linkByWorkflowID[link.WorkflowID]; exists {
			continue
		}
		linkByWorkflowID[link.WorkflowID] = link
		workflowIDs = append(workflowIDs, link.WorkflowID)
	}
	activityByWorkflowID := map[runtimeids.WorkflowID]int64{}
	for _, activity := range taskActivityRows {
		if _, linked := linkByWorkflowID[activity.WorkflowID]; linked {
			activityByWorkflowID[activity.WorkflowID] = activity.LatestUpdatedAtUnixMs
		}
	}
	definitions := make(map[string]definitionSnapshot, len(workflowIDs))
	picker := make([]serverapi.WorkflowPickerItem, 0, len(workflowIDs))
	for _, workflowID := range workflowIDs {
		snapshot, err := b.definitions.snapshot(ctx, workflowID)
		if err != nil {
			return nil, nil, err
		}
		definitions[workflowID.String()] = snapshot
		link, linked := linkByWorkflowID[workflowID]
		if !linked {
			return nil, nil, fmt.Errorf("workflow selection invariant violated: active link missing for project_id=%q workflow_id=%q", projectID, workflowID.String())
		}
		validation := definitionExecutionValidation(snapshot.domain, b.roleResolver)
		picker = append(picker, serverapi.WorkflowPickerItem{
			WorkflowID:           workflowID,
			DisplayName:          snapshot.api.Workflow.Name,
			Description:          snapshot.api.Workflow.Description,
			Version:              snapshot.api.Workflow.Version,
			IsProjectDefault:     link.IsDefault != 0,
			ValidForTaskCreation: !validation.HasBlockingErrors(),
			ValidationErrors:     ValidationErrors(&snapshot.api.Workflow.ID, validation.Errors),
		})
	}
	sort.SliceStable(picker, func(i, j int) bool {
		if picker[i].IsProjectDefault != picker[j].IsProjectDefault {
			return picker[i].IsProjectDefault
		}
		if activityByWorkflowID[picker[i].WorkflowID] != activityByWorkflowID[picker[j].WorkflowID] {
			return activityByWorkflowID[picker[i].WorkflowID] > activityByWorkflowID[picker[j].WorkflowID]
		}
		return strings.ToLower(picker[i].DisplayName) < strings.ToLower(picker[j].DisplayName)
	})
	return definitions, picker, nil
}

func (b *Board) applyColumnTaskCounts(ctx context.Context, columns []serverapi.WorkflowBoardColumn, projectID string, workflowID runtimeids.WorkflowID, labelFilter workflowTaskLabelFilterFacts) error {
	labelFilterArgs, err := labelFilter.queryArgs()
	if err != nil {
		return err
	}
	rows, err := b.queries.ListBoardColumnTaskCounts(ctx, sqlitegen.ListBoardColumnTaskCountsParams{
		ProjectID:            projectID,
		WorkflowID:           workflowID,
		LabelFilterKind:      labelFilterArgs.kind,
		LabelFilterMode:      labelFilterArgs.mode,
		LabelIdsJson:         labelFilterArgs.labelIDsJSON,
		ExcludedLabelIdsJson: labelFilterArgs.excludedLabelIDsJSON,
	})
	if err != nil {
		return err
	}
	indexByNodeID := map[string]int{}
	for index, column := range columns {
		indexByNodeID[column.Node.NodeID] = index
	}
	for _, row := range rows {
		nodeID := strings.TrimSpace(row.NodeID)
		if nodeID == "" {
			continue
		}
		if index, ok := indexByNodeID[nodeID]; ok {
			columns[index].TaskCount = int(row.TaskCount)
		}
	}
	return nil
}

func (b *Board) projectWorkflowLinkID(ctx context.Context, projectID string, workflowID runtimeids.WorkflowID) (string, error) {
	links, err := b.queries.ListProjectWorkflowLinks(ctx, projectID)
	if err != nil {
		return "", err
	}
	var linkID *string
	for _, link := range links {
		if link.WorkflowID != workflowID {
			continue
		}
		if linkID != nil {
			return "", fmt.Errorf("workflow link invariant violated: project_id=%q workflow_id=%q has multiple links", projectID, workflowID.String())
		}
		id := link.ID
		linkID = &id
	}
	if linkID == nil {
		return "", fmt.Errorf("workflow link not found: project_id=%q workflow_id=%q", projectID, workflowID.String())
	}
	return *linkID, nil
}

type workflowBoardSelector interface {
	selectFrom(picker []serverapi.WorkflowPickerItem) *serverapi.WorkflowPickerItem
}

type workflowBoardDefaultSelector struct{}

func (workflowBoardDefaultSelector) selectFrom(picker []serverapi.WorkflowPickerItem) *serverapi.WorkflowPickerItem {
	return defaultWorkflowBoardSelection(picker)
}

type workflowBoardExplicitSelector struct {
	workflowID runtimeids.WorkflowID
}

func (s workflowBoardExplicitSelector) selectFrom(picker []serverapi.WorkflowPickerItem) *serverapi.WorkflowPickerItem {
	if selected := exactWorkflowBoardSelection(picker, s.workflowID); selected != nil {
		return selected
	}
	return defaultWorkflowBoardSelection(picker)
}

func workflowBoardSelectorFromRequest(req serverapi.WorkflowBoardRequest) workflowBoardSelector {
	if req.WorkflowID != nil {
		return workflowBoardExplicitSelector{workflowID: *req.WorkflowID}
	}
	return workflowBoardDefaultSelector{}
}

func exactWorkflowBoardSelection(picker []serverapi.WorkflowPickerItem, workflowID runtimeids.WorkflowID) *serverapi.WorkflowPickerItem {
	for index := range picker {
		if picker[index].WorkflowID == workflowID {
			return &picker[index]
		}
	}
	return nil
}

func defaultWorkflowBoardSelection(picker []serverapi.WorkflowPickerItem) *serverapi.WorkflowPickerItem {
	for index := range picker {
		if picker[index].IsProjectDefault {
			return &picker[index]
		}
	}
	if len(picker) == 0 {
		return nil
	}
	return &picker[0]
}

func boardGroups(def serverapi.WorkflowDefinition) []serverapi.WorkflowBoardGroup {
	columnNodes := boardColumnNodes(def)
	groups := make([]serverapi.WorkflowBoardGroup, 0, len(def.NodeGroups))
	for _, group := range def.NodeGroups {
		dto := serverapi.WorkflowBoardGroup{
			GroupID:     group.GroupID,
			Key:         group.GroupKey,
			DisplayName: group.DisplayName,
			SortOrder:   group.SortOrder,
		}
		for _, node := range columnNodes {
			if node.GroupID == group.GroupID {
				dto.NodeIDs = append(dto.NodeIDs, node.ID)
			}
		}
		if len(dto.NodeIDs) > 0 {
			groups = append(groups, dto)
		}
	}
	return groups
}

func boardColumns(snapshot definitionSnapshot) []serverapi.WorkflowBoardColumn {
	columns := make([]serverapi.WorkflowBoardColumn, 0, len(snapshot.api.Nodes))
	derived := workflow.DeriveWiring(snapshot.domain)
	for index, node := range boardColumnNodes(snapshot.api) {
		columns = append(columns, serverapi.WorkflowBoardColumn{
			Node: serverapi.WorkflowBoardNodeSummary{
				NodeID:                 node.ID,
				Key:                    node.Key,
				Kind:                   node.Kind,
				DisplayName:            node.DisplayName,
				AssigneeRole:           node.SubagentRole,
				SortOrder:              index,
				OutputFields:           OutputFields(derived.PossibleProvisionFieldsForNode(workflow.NodeID(node.ID))),
				TransitionOutputFields: OutputFields(workflow.TransitionOutputFieldsForTargetNode(snapshot.domain, derived, workflow.NodeID(node.ID))),
			},
			GroupID:   node.GroupID,
			SortOrder: index,
			IsBacklog: node.Kind == string(workflow.NodeKindStart),
			IsDone:    node.Kind == string(workflow.NodeKindTerminal),
		})
	}
	return columns
}

func boardVisibleNodeKind(kind string) bool {
	return workflow.NodeKind(kind) != workflow.NodeKindJoin
}
