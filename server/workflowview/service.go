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
	"strconv"
	"strings"
	"time"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/server/workflowscript"
	"core/server/workflowstore"
	"core/server/worktree"
	"core/shared/serverapi"
)

type Service struct {
	metadata    *metadata.Store
	queries     *sqlitegen.Queries
	definitions *DefinitionProjection
	projector   *TaskProjector
	taskDetail  *TaskDetail
	attention   *Attention
}

const attentionKindInterruptedRun = "interrupted_run"

const interruptedRunAttentionMessage = "This task's run was stopped."

// Sentinel errors returned by the workflow view service. Callers and tests must
// match these with errors.Is/errors.As rather than comparing rendered message
// text. Dynamic context is wrapped via fmt.Errorf("... %w", Err...).
var (
	// ErrTaskIDRequired is returned when a task id is required but blank.
	ErrTaskIDRequired = errors.New("task_id is required")
	// ErrInvalidPageToken is returned when a pagination page_token fails to
	// decode or does not match its issuing query.
	ErrInvalidPageToken = errors.New("page_token is invalid")
)

type serviceOptions struct {
	attentionTranscripts SessionActiveTranscriptProvider
	attentionPrompts     PendingPromptSource
}

type Option func(*serviceOptions)

func WithSessionTranscriptProvider(provider SessionActiveTranscriptProvider) Option {
	return func(options *serviceOptions) {
		options.attentionTranscripts = provider
	}
}

func WithPendingPromptSource(source PendingPromptSource) Option {
	return func(options *serviceOptions) {
		options.attentionPrompts = source
	}
}

func New(metadataStore *metadata.Store, opts ...Option) (*Service, error) {
	if metadataStore == nil || metadataStore.Queries() == nil {
		return nil, errors.New("metadata store is required")
	}
	workflowStore, err := workflowstore.New(metadataStore)
	if err != nil {
		return nil, err
	}
	definitions, err := NewDefinitionProjection(workflowStore)
	if err != nil {
		return nil, err
	}
	projector := NewTaskProjector()
	taskDetail, err := NewTaskDetail(metadataStore, definitions, projector, worktree.NewGitInspector(nil))
	if err != nil {
		return nil, err
	}
	options := serviceOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	attention, err := NewAttention(metadataStore, definitions, options.attentionTranscripts, options.attentionPrompts)
	if err != nil {
		return nil, err
	}
	return &Service{
		metadata:    metadataStore,
		queries:     metadataStore.Queries(),
		definitions: definitions,
		projector:   projector,
		taskDetail:  taskDetail,
		attention:   attention,
	}, nil
}

func (s *Service) GetDefinition(ctx context.Context, workflowID string) (serverapi.WorkflowDefinition, map[string]workflow.NodeKind, error) {
	if s == nil {
		return serverapi.WorkflowDefinition{}, nil, errors.New("workflow view service is required")
	}
	if strings.TrimSpace(workflowID) == "" {
		return serverapi.WorkflowDefinition{}, nil, errors.New("workflow_id is required")
	}
	return s.definitions.GetDefinition(ctx, workflowID)
}

func (s *Service) GetBoard(ctx context.Context, req serverapi.WorkflowBoardRequest, roleResolver workflow.RoleResolver) (serverapi.WorkflowBoard, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowBoard{}, err
	}
	if s == nil {
		return serverapi.WorkflowBoard{}, errors.New("workflow view service is required")
	}
	projectID := strings.TrimSpace(req.ProjectID)
	if strings.TrimSpace(projectID) == "" {
		return serverapi.WorkflowBoard{}, errors.New("project_id is required")
	}
	definitions, picker, err := s.workflowSelectionInputs(ctx, projectID, roleResolver)
	if err != nil {
		return serverapi.WorkflowBoard{}, err
	}
	project, err := s.metadata.GetProjectOverview(ctx, projectID)
	if err != nil {
		return serverapi.WorkflowBoard{}, err
	}
	workspaceContext := boardProjectWorkspaceContext(project)
	selector := workflowBoardSelectorFromRequest(req)
	selected := selector.selectFrom(picker)
	if selected == nil {
		return serverapi.WorkflowBoard{ProjectID: projectID, Project: projectBoardProject(project, workspaceContext), WorkflowPicker: picker, GeneratedAtUnixMs: time.Now().UTC().UnixMilli()}, nil
	}
	snapshot := definitions[selected.WorkflowID]
	def := snapshot.api
	groups := boardGroups(def)
	columns := boardColumns(snapshot)
	if err := s.applyBoardColumnTaskCounts(ctx, columns, projectID, selected.WorkflowID, canceledBoardTerminalNodeID(def)); err != nil {
		return serverapi.WorkflowBoard{}, err
	}
	board := serverapi.WorkflowBoard{
		ProjectID:         projectID,
		Project:           projectBoardProject(project, workspaceContext),
		SelectedWorkflow:  selected,
		WorkflowPicker:    picker,
		Groups:            groups,
		Columns:           columns,
		GeneratedAtUnixMs: time.Now().UTC().UnixMilli(),
	}
	return board, nil
}

func (s *Service) ListTasks(ctx context.Context, req serverapi.WorkflowTaskListRequest, roleResolver workflow.RoleResolver) (serverapi.WorkflowTaskListResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskListResponse{}, err
	}
	if s == nil {
		return serverapi.WorkflowTaskListResponse{}, errors.New("workflow view service is required")
	}
	pageToken, hasPageToken, err := parseWorkflowTaskListPageToken(req.PageToken)
	if err != nil {
		return serverapi.WorkflowTaskListResponse{}, err
	}
	var tokenScope *workflowTaskListPageTokenPayload
	if hasPageToken {
		tokenScope = &pageToken
	}
	projectID, workflowID, err := s.resolveWorkflowTaskListScope(ctx, req.ProjectID, req.WorkflowID, tokenScope)
	if err != nil {
		return serverapi.WorkflowTaskListResponse{}, err
	}
	pageSize := req.PageSize
	if pageSize == 0 {
		pageSize = 100
	}
	if _, err := s.metadata.GetProjectOverview(ctx, projectID); err != nil {
		return serverapi.WorkflowTaskListResponse{}, err
	}
	var def serverapi.WorkflowDefinition
	var snapshot definitionSnapshot
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
		snapshot, err = s.definitions.snapshot(ctx, *workflowID)
		if err != nil {
			return serverapi.WorkflowTaskListResponse{}, err
		}
		def = snapshot.api
		columns = boardColumns(snapshot)
		if err := validateWorkflowTaskListColumnKeys(req.ColumnKeys, columns); err != nil {
			return serverapi.WorkflowTaskListResponse{}, err
		}
	}
	sortSelectors := normalizeWorkflowTaskListSort(req.Sort)
	fingerprintScope := workflowTaskListFingerprintScope{
		ProjectWide: &workflowTaskListProjectWideFingerprintInvariants{},
	}
	var columnStructureHash *string
	if workflowID != nil {
		value, hashErr := workflowTaskListColumnStructureHash(def, columns)
		if hashErr != nil {
			return serverapi.WorkflowTaskListResponse{}, hashErr
		}
		columnStructureHash = &value
		fingerprintScope = workflowTaskListFingerprintScope{
			Narrowed: &workflowTaskListNarrowedFingerprintInvariants{ColumnStructureHash: value},
		}
	}
	fingerprint, err := workflowTaskListRequestFingerprint(req, sortSelectors, fingerprintScope)
	if err != nil {
		return serverapi.WorkflowTaskListResponse{}, err
	}
	cursor := workflowTaskListCursor{}
	matchingWorkflowCardinality := serverapi.WorkflowTaskListMatchingWorkflowCardinalityNone
	if hasPageToken {
		if pageToken.Scope.ProjectID != projectID ||
			pageToken.StatusModelVersion != workflowTaskStatusModelVersion ||
			pageToken.Fingerprint != fingerprint {
			return serverapi.WorkflowTaskListResponse{}, ErrInvalidPageToken
		}
		if workflowID == nil {
			if pageToken.Scope.ProjectWide == nil {
				return serverapi.WorkflowTaskListResponse{}, ErrInvalidPageToken
			}
		} else {
			narrowed := pageToken.Scope.Narrowed
			if narrowed == nil ||
				narrowed.WorkflowID != *workflowID ||
				narrowed.WorkflowVersion != def.Workflow.Version ||
				columnStructureHash == nil ||
				narrowed.ColumnStructureHash != *columnStructureHash {
				return serverapi.WorkflowTaskListResponse{}, ErrInvalidPageToken
			}
		}
		cursor = pageToken.Cursor
		matchingWorkflowCardinality = pageToken.MatchingWorkflowCardinality
	}
	var narrowedQuery *workflowTaskListNarrowedQueryFacts
	if workflowID != nil {
		narrowedQuery = &workflowTaskListNarrowedQueryFacts{
			workflowID:             *workflowID,
			canceledTerminalNodeID: canceledBoardTerminalNodeID(def),
			columns:                columns,
			columnKeys:             req.ColumnKeys,
		}
	}
	rows, err := s.listWorkflowTaskListRows(ctx, workflowTaskListQueryRequest{
		projectID:      projectID,
		narrowed:       narrowedQuery,
		statusKinds:    req.StatusKinds,
		attentionKinds: req.AttentionKinds,
		sortSelectors:  sortSelectors,
		cursor:         cursor,
		cursorSet:      hasPageToken,
		limit:          pageSize + 1,
	})
	if err != nil {
		return serverapi.WorkflowTaskListResponse{}, err
	}
	if !hasPageToken {
		matchingWorkflowCardinality, err = workflowTaskListMatchingWorkflowCardinality(rows)
		if err != nil {
			return serverapi.WorkflowTaskListResponse{}, err
		}
	}
	pageItems := rows
	hasNext := len(pageItems) > pageSize
	if hasNext {
		pageItems = pageItems[:pageSize]
	}
	responseItems := make([]serverapi.WorkflowTaskListItem, 0, len(pageItems))
	for _, item := range pageItems {
		responseItem := item.item
		if matchingWorkflowCardinality != serverapi.WorkflowTaskListMatchingWorkflowCardinalityMultiple {
			responseItem.WorkflowName = nil
		}
		responseItems = append(responseItems, responseItem)
	}
	var nextPageToken *string
	if hasNext && len(pageItems) > 0 {
		tokenScope := workflowTaskListPageTokenScope{ProjectID: projectID}
		if workflowID == nil {
			tokenScope.ProjectWide = &workflowTaskListProjectWidePageTokenInvariants{}
		} else {
			tokenScope.Narrowed = &workflowTaskListNarrowedPageTokenInvariants{
				WorkflowID:          *workflowID,
				WorkflowVersion:     def.Workflow.Version,
				ColumnStructureHash: *columnStructureHash,
			}
		}
		encodedToken, encodeErr := workflowTaskListPageToken(workflowTaskListPageTokenPayload{
			Version:                     workflowTaskListPageTokenVersion,
			Scope:                       tokenScope,
			MatchingWorkflowCardinality: matchingWorkflowCardinality,
			StatusModelVersion:          workflowTaskStatusModelVersion,
			Fingerprint:                 fingerprint,
			Cursor:                      workflowTaskListCursorFromRow(pageItems[len(pageItems)-1]),
		})
		if encodeErr != nil {
			return serverapi.WorkflowTaskListResponse{}, encodeErr
		}
		nextPageToken = &encodedToken
	}
	return serverapi.WorkflowTaskListResponse{
		Scope: serverapi.WorkflowTaskListScope{
			ProjectID:  projectID,
			WorkflowID: workflowID,
		},
		MatchingWorkflowCardinality: matchingWorkflowCardinality,
		NextPageToken:               nextPageToken,
		GeneratedAtUnixMs:           time.Now().UTC().UnixMilli(),
		Tasks:                       responseItems,
	}, nil
}

func (s *Service) resolveWorkflowTaskListScope(ctx context.Context, projectIDValue *string, workflowIDValue *string, token *workflowTaskListPageTokenPayload) (string, *string, error) {
	if token != nil {
		if projectIDValue != nil && *projectIDValue != token.Scope.ProjectID {
			return "", nil, ErrInvalidPageToken
		}
		var tokenWorkflowID *string
		if token.Scope.Narrowed != nil {
			tokenWorkflowID = &token.Scope.Narrowed.WorkflowID
		}
		if workflowIDValue != nil {
			if tokenWorkflowID == nil || *workflowIDValue != *tokenWorkflowID {
				return "", nil, ErrInvalidPageToken
			}
		}
		projectIDValue = &token.Scope.ProjectID
		workflowIDValue = tokenWorkflowID
	}
	if projectIDValue != nil && workflowIDValue != nil {
		if _, err := s.queries.GetActiveProjectWorkflowLinkByWorkflow(ctx, sqlitegen.GetActiveProjectWorkflowLinkByWorkflowParams{ProjectID: *projectIDValue, WorkflowID: *workflowIDValue}); err == nil {
			workflowID := *workflowIDValue
			return *projectIDValue, &workflowID, nil
		} else if errors.Is(err, sql.ErrNoRows) {
			errorProjectID := *projectIDValue
			errorWorkflowID := *workflowIDValue
			return "", nil, &serverapi.WorkflowTaskListScopeError{Reason: serverapi.WorkflowTaskListScopeReasonWorkflowNotLinked, ProjectID: &errorProjectID, WorkflowID: &errorWorkflowID}
		} else {
			return "", nil, err
		}
	}
	if projectIDValue != nil {
		linkCount, err := s.queries.CountActiveProjectWorkflowLinks(ctx, *projectIDValue)
		if err != nil {
			return "", nil, err
		}
		if linkCount == 0 {
			errorProjectID := *projectIDValue
			return "", nil, &serverapi.WorkflowTaskListScopeError{Reason: serverapi.WorkflowTaskListScopeReasonNoLinkedWorkflows, ProjectID: &errorProjectID}
		}
		return *projectIDValue, nil, nil
	}
	return "", nil, &serverapi.WorkflowTaskListScopeError{Reason: serverapi.WorkflowTaskListScopeReasonNoLinkedWorkflows}
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

func (s *Service) workflowSelectionInputs(ctx context.Context, projectID string, roleResolver workflow.RoleResolver) (map[string]definitionSnapshot, []serverapi.WorkflowPickerItem, error) {
	links, err := s.queries.ListProjectWorkflowLinks(ctx, projectID)
	if err != nil {
		return nil, nil, err
	}
	taskActivityRows, err := s.queries.ListProjectWorkflowTaskActivity(ctx, projectID)
	if err != nil {
		return nil, nil, err
	}
	workflowIDs := make([]string, 0, len(links))
	linkByWorkflowID := map[string]sqlitegen.ProjectWorkflowLinkRecord{}
	for _, link := range links {
		if _, exists := linkByWorkflowID[link.WorkflowID]; exists {
			continue
		}
		linkByWorkflowID[link.WorkflowID] = link
		workflowIDs = append(workflowIDs, link.WorkflowID)
	}
	activityByWorkflowID := map[string]int64{}
	for _, activity := range taskActivityRows {
		if _, linked := linkByWorkflowID[activity.WorkflowID]; linked {
			activityByWorkflowID[activity.WorkflowID] = activity.LatestUpdatedAtUnixMs
		}
	}
	definitions := make(map[string]definitionSnapshot, len(workflowIDs))
	picker := make([]serverapi.WorkflowPickerItem, 0, len(workflowIDs))
	for _, workflowID := range workflowIDs {
		snapshot, err := s.definitions.snapshot(ctx, workflowID)
		if err != nil {
			return nil, nil, err
		}
		definitions[workflowID] = snapshot
		def := snapshot.api
		link, linked := linkByWorkflowID[workflowID]
		if !linked {
			return nil, nil, fmt.Errorf("workflow selection invariant violated: active link missing for project_id=%q workflow_id=%q", projectID, workflowID)
		}
		validation := definitionExecutionValidation(snapshot.domain, roleResolver)
		picker = append(picker, serverapi.WorkflowPickerItem{
			WorkflowID:           workflowID,
			DisplayName:          def.Workflow.Name,
			Description:          def.Workflow.Description,
			Version:              def.Workflow.Version,
			IsProjectDefault:     link.IsDefault != 0,
			ValidForTaskCreation: !validation.HasBlockingErrors(),
			ValidationErrors:     ValidationErrors(def.Workflow.ID, validation.Errors),
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

func (s *Service) ListBoardNodeCards(ctx context.Context, req serverapi.WorkflowBoardNodeCardsListRequest, _ workflow.RoleResolver) (serverapi.WorkflowBoardNodeCardsListResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, err
	}
	if s == nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, errors.New("workflow view service is required")
	}
	projectID := strings.TrimSpace(req.ProjectID)
	workflowID := strings.TrimSpace(req.WorkflowID)
	nodeID := strings.TrimSpace(req.NodeID)
	snapshot, err := s.definitions.snapshot(ctx, workflowID)
	if err != nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, err
	}
	def := snapshot.api
	if _, ok := workflowNodeByID(def)[nodeID]; !ok {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, errors.New("node_id is invalid for workflow")
	}
	pageSize := req.PageSize
	if pageSize == 0 {
		pageSize = serverapi.WorkflowBoardNodeCardsMaxPageSize
	}
	cursor, err := parseBoardNodeCardsPageToken(req.PageToken, projectID, workflowID, nodeID)
	if err != nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, err
	}
	cursorUpdatedAtUnixMs := sql.NullInt64{}
	cursorTaskID := sql.NullString{}
	if cursor.anchor != nil {
		cursorUpdatedAtUnixMs = sql.NullInt64{Int64: cursor.anchor.updatedAtUnixMs, Valid: true}
		cursorTaskID = sql.NullString{String: cursor.anchor.taskID, Valid: true}
	}
	rows, err := s.queries.ListBoardNodeTasks(ctx, sqlitegen.ListBoardNodeTasksParams{
		ProjectID:              projectID,
		WorkflowID:             workflowID,
		CursorDirection:        string(cursor.direction),
		CursorUpdatedAtUnixMs:  cursorUpdatedAtUnixMs,
		CursorTaskID:           cursorTaskID,
		NodeID:                 sql.NullString{String: nodeID, Valid: true},
		CanceledTerminalNodeID: nullableWorkflowViewString(canceledBoardTerminalNodeID(def)),
		LimitRows:              int64(pageSize + 1),
	})
	if err != nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, err
	}
	tasks := boardNodeTaskRecords(rows)
	hasExtra := len(tasks) > pageSize
	if hasExtra {
		tasks = tasks[:pageSize]
	}
	if cursor.direction == boardNodeCardsPageDirectionNewer {
		slices.Reverse(tasks)
	}
	project, err := s.metadata.GetProjectOverview(ctx, projectID)
	if err != nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, err
	}
	workspaceContext := boardProjectWorkspaceContext(project)
	placementsByTaskID, err := s.boardPlacementsByTask(ctx, tasks)
	if err != nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, err
	}
	candidateTaskIDs := make([]string, 0, len(tasks))
	for _, task := range tasks {
		candidateTaskIDs = append(candidateTaskIDs, task.ID)
	}
	statusesByTaskID, err := s.taskStatusFacts(ctx, candidateTaskIDs)
	if err != nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, err
	}
	runsByTaskID, err := s.taskRunsByTask(ctx, tasks)
	if err != nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, err
	}
	cards := make([]serverapi.WorkflowBoardTaskCard, 0, len(tasks))
	for _, task := range tasks {
		card, _ := s.taskCard(task, statusesByTaskID[task.ID], placementsByTaskID[task.ID], runsByTaskID[task.ID], snapshot, sourceWorkspaceForTask(task, workspaceContext.byID, workspaceContext.primary))
		cards = append(cards, card)
	}
	var previousPageToken *string
	var nextPageToken *string
	if len(tasks) > 0 {
		first := tasks[0]
		last := tasks[len(tasks)-1]
		switch cursor.direction {
		case boardNodeCardsPageDirectionOlder:
			if cursor.anchor != nil {
				previousPageToken, err = boardNodeCardsPageToken(projectID, workflowID, nodeID, boardNodeCardsPageDirectionNewer, first)
				if err != nil {
					return serverapi.WorkflowBoardNodeCardsListResponse{}, err
				}
			}
			if hasExtra {
				nextPageToken, err = boardNodeCardsPageToken(projectID, workflowID, nodeID, boardNodeCardsPageDirectionOlder, last)
				if err != nil {
					return serverapi.WorkflowBoardNodeCardsListResponse{}, err
				}
			}
		case boardNodeCardsPageDirectionNewer:
			if hasExtra {
				previousPageToken, err = boardNodeCardsPageToken(projectID, workflowID, nodeID, boardNodeCardsPageDirectionNewer, first)
				if err != nil {
					return serverapi.WorkflowBoardNodeCardsListResponse{}, err
				}
			}
			nextPageToken, err = boardNodeCardsPageToken(projectID, workflowID, nodeID, boardNodeCardsPageDirectionOlder, last)
			if err != nil {
				return serverapi.WorkflowBoardNodeCardsListResponse{}, err
			}
		default:
			return serverapi.WorkflowBoardNodeCardsListResponse{}, ErrInvalidPageToken
		}
	}
	return serverapi.WorkflowBoardNodeCardsListResponse{ProjectID: projectID, WorkflowID: workflowID, NodeID: nodeID, Cards: cards, PreviousPageToken: previousPageToken, NextPageToken: nextPageToken, GeneratedAtUnixMs: time.Now().UTC().UnixMilli()}, nil
}

func boardNodeTaskRecords(rows []sqlitegen.ListBoardNodeTasksRow) []sqlitegen.TaskRecord {
	tasks := make([]sqlitegen.TaskRecord, 0, len(rows))
	for _, row := range rows {
		tasks = append(tasks, sqlitegen.TaskRecord{
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
			CanceledAtUnixMs:            row.CanceledAtUnixMs,
			CancellationReason:          row.CancellationReason,
			CreatedAtUnixMs:             row.CreatedAtUnixMs,
			UpdatedAtUnixMs:             row.UpdatedAtUnixMs,
			MetadataJson:                row.MetadataJson,
		})
	}
	return tasks
}

func (s *Service) boardPlacementsByTask(ctx context.Context, tasks []sqlitegen.TaskRecord) (map[string][]sqlitegen.TaskNodePlacementRecord, error) {
	if len(tasks) == 0 {
		return map[string][]sqlitegen.TaskNodePlacementRecord{}, nil
	}
	taskIDs := make([]string, 0, len(tasks))
	for _, task := range tasks {
		taskIDs = append(taskIDs, task.ID)
	}
	placements, err := s.queries.ListTaskNodePlacementsByTasks(ctx, taskIDs)
	if err != nil {
		return nil, err
	}
	byTaskID := make(map[string][]sqlitegen.TaskNodePlacementRecord, len(tasks))
	for _, placement := range placements {
		byTaskID[placement.TaskID] = append(byTaskID[placement.TaskID], placement)
	}
	pendingApprovalPlacements, err := s.pendingApprovalSourcePlacementsByTask(ctx, taskIDs)
	if err != nil {
		return nil, err
	}
	for taskID, taskPlacements := range pendingApprovalPlacements {
		byTaskID[taskID] = append(byTaskID[taskID], taskPlacements...)
	}
	return byTaskID, nil
}

func (s *Service) taskRunsByTask(ctx context.Context, tasks []sqlitegen.TaskRecord) (map[string][]sqlitegen.TaskRunRecord, error) {
	if len(tasks) == 0 {
		return map[string][]sqlitegen.TaskRunRecord{}, nil
	}
	taskIDs := make([]string, 0, len(tasks))
	for _, task := range tasks {
		taskIDs = append(taskIDs, task.ID)
	}
	runs, err := s.queries.ListTaskRunsByTasks(ctx, taskIDs)
	if err != nil {
		return nil, err
	}
	byTaskID := make(map[string][]sqlitegen.TaskRunRecord, len(tasks))
	for _, run := range runs {
		byTaskID[run.TaskID] = append(byTaskID[run.TaskID], run)
	}
	return byTaskID, nil
}

func (s *Service) pendingApprovalSourcePlacementsByTask(ctx context.Context, taskIDs []string) (map[string][]sqlitegen.TaskNodePlacementRecord, error) {
	return loadPendingApprovalSourcePlacementsByTask(ctx, s.queries, taskIDs)
}

func (s *Service) GetTask(ctx context.Context, taskID string) (serverapi.WorkflowTaskDetail, error) {
	if s == nil {
		return serverapi.WorkflowTaskDetail{}, errors.New("workflow view service is required")
	}
	return s.taskDetail.GetTask(ctx, taskID)
}

func (s *Service) GetTaskByProjectShortID(ctx context.Context, projectID string, shortID string) (serverapi.WorkflowTaskDetail, error) {
	if s == nil {
		return serverapi.WorkflowTaskDetail{}, errors.New("workflow view service is required")
	}
	return s.taskDetail.GetTaskByProjectShortID(ctx, projectID, shortID)
}

func (s *Service) GetTaskByShortID(ctx context.Context, shortID string) (serverapi.WorkflowTaskDetail, error) {
	if s == nil {
		return serverapi.WorkflowTaskDetail{}, errors.New("workflow view service is required")
	}
	return s.taskDetail.GetTaskByShortID(ctx, shortID)
}

func (s *Service) ListTaskActivity(ctx context.Context, req serverapi.WorkflowTaskActivityListRequest) (serverapi.WorkflowTaskActivityListResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskActivityListResponse{}, err
	}
	task, err := s.queries.GetTask(ctx, strings.TrimSpace(req.TaskID))
	if err != nil {
		return serverapi.WorkflowTaskActivityListResponse{}, err
	}
	def, _, err := s.definition(ctx, task.WorkflowID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return serverapi.WorkflowTaskActivityListResponse{}, err
	}
	nodeByID := workflowNodeByID(def)
	pageSize := req.PageSize
	if pageSize == 0 {
		pageSize = 50
	}
	cursor, err := parseActivityPageToken(req.PageToken)
	if err != nil {
		return serverapi.WorkflowTaskActivityListResponse{}, err
	}
	rows, err := s.taskActivityRows(ctx, task.ID, cursor, pageSize+1)
	if err != nil {
		return serverapi.WorkflowTaskActivityListResponse{}, err
	}
	pageRows := rows
	hasNext := len(rows) > pageSize
	if hasNext {
		pageRows = rows[:pageSize]
	}
	comments, err := s.commentsByID(ctx, sourceIDsByType(pageRows, "comment"))
	if err != nil {
		return serverapi.WorkflowTaskActivityListResponse{}, err
	}
	transitions, err := s.transitionsByID(ctx, sourceIDsByType(pageRows, "transition"))
	if err != nil {
		return serverapi.WorkflowTaskActivityListResponse{}, err
	}
	edgesByTransitionID, err := s.transitionEdgesByTransitionID(ctx, transitions)
	if err != nil {
		return serverapi.WorkflowTaskActivityListResponse{}, err
	}
	transitionByID := taskTransitionByID(transitions)
	runs, err := s.runsByID(ctx, sourceIDsByTypes(pageRows, "run_started", "run_completed", "run_interrupted"))
	if err != nil {
		return serverapi.WorkflowTaskActivityListResponse{}, err
	}
	sessionNames, err := s.sessionNamesByRun(ctx, runs)
	if err != nil {
		return serverapi.WorkflowTaskActivityListResponse{}, err
	}
	runByID := taskRunByID(runs)
	items, err := s.activityItemsFromRows(task, pageRows, comments, transitionByID, edgesByTransitionID, runByID, nodeByID, sessionNames)
	if err != nil {
		return serverapi.WorkflowTaskActivityListResponse{}, err
	}
	nextPageToken := ""
	if hasNext && len(items) > 0 {
		last := items[len(items)-1]
		nextPageToken = activityPageToken(last)
	}
	return serverapi.WorkflowTaskActivityListResponse{Items: items, NextPageToken: nextPageToken, GeneratedAtUnixMs: time.Now().UTC().UnixMilli()}, nil
}

func (s *Service) ListAttention(ctx context.Context, req serverapi.WorkflowAttentionListRequest, roleResolver workflow.RoleResolver) (serverapi.WorkflowAttentionListResponse, error) {
	if s == nil {
		return serverapi.WorkflowAttentionListResponse{}, errors.New("workflow view service is required")
	}
	return s.attention.List(ctx, req, roleResolver)
}

func (s *Service) ListTaskAttention(ctx context.Context, req serverapi.WorkflowTaskAttentionListRequest, roleResolver workflow.RoleResolver) (serverapi.WorkflowTaskAttentionListResponse, error) {
	if s == nil {
		return serverapi.WorkflowTaskAttentionListResponse{}, errors.New("workflow view service is required")
	}
	return s.attention.ListTask(ctx, req, roleResolver)
}

func (s *Service) definition(ctx context.Context, workflowID string) (serverapi.WorkflowDefinition, map[string]workflow.NodeKind, error) {
	return s.definitions.GetDefinition(ctx, workflowID)
}

type taskActivityRow struct {
	activityID       string
	kind             string
	sourceID         string
	occurredAtUnixMs int64
	updatedAtUnixMs  int64
	actor            string
}

func (s *Service) taskActivityRows(ctx context.Context, taskID string, cursor activityPageCursor, limit int) ([]taskActivityRow, error) {
	if limit <= 0 {
		return []taskActivityRow{}, nil
	}
	cursorActive := int64(0)
	if cursor.hasValue {
		cursorActive = 1
	}
	rows, err := s.queries.ListWorkflowTaskActivityRows(ctx, sqlitegen.ListWorkflowTaskActivityRowsParams{
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

func (s *Service) activityItemsFromRows(task sqlitegen.TaskRecord, rows []taskActivityRow, comments map[string]sqlitegen.TaskComment, transitions map[string]sqlitegen.TaskTransitionRecord, edges map[string][]sqlitegen.TaskTransitionEdgeRecord, runs map[string]sqlitegen.TaskRunRecord, nodes map[string]serverapi.WorkflowNode, sessionNames map[string]string) ([]serverapi.WorkflowTaskActivityItem, error) {
	items := make([]serverapi.WorkflowTaskActivityItem, 0, len(rows))
	for _, row := range rows {
		item := serverapi.WorkflowTaskActivityItem{ActivityID: row.activityID, Type: row.kind, TaskID: task.ID, OccurredAtUnixMs: row.occurredAtUnixMs, UpdatedAtUnixMs: row.updatedAtUnixMs, Actor: row.actor}
		switch row.kind {
		case "comment":
			comment, ok := comments[row.sourceID]
			if !ok {
				return nil, errors.New("activity comment source is missing")
			}
			item.Summary = "Comment"
			dto := s.projector.ProjectComment(comment)
			item.Comment = &dto
		case "transition":
			transition, ok := transitions[row.sourceID]
			if !ok {
				return nil, errors.New("activity transition source is missing")
			}
			dto, err := s.projector.ProjectTransition(TransitionProjectionInput{Transition: transition, Edges: edges[transition.ID]})
			if err != nil {
				return nil, err
			}
			summary := strings.TrimSpace(dto.TransitionDisplayName)
			if summary == "" {
				summary = dto.TransitionID
			}
			item.Actor = transition.Actor
			item.Summary = "Transition: " + summary
			item.Transition = &dto
		case "run_started", "run_completed", "run_interrupted":
			run, ok := runs[row.sourceID]
			if !ok {
				return nil, errors.New("activity run source is missing")
			}
			runView := s.projector.ProjectRun(RunProjectionInput{Run: run, Nodes: nodes, SessionNames: sessionNames})
			item.Run = &runView
			switch row.kind {
			case "run_started":
				item.Summary = "Run started"
			case "run_completed":
				item.Summary = "Run completed"
			case "run_interrupted":
				item.Summary = interruptedRunMessage(metadata.OptionalString(run.InterruptionReason), run.InterruptionDetailJson)
				workflowID := task.WorkflowID
				attention := serverapi.WorkflowAttentionItem{ID: attentionKindInterruptedRun + ":" + run.ID, Kind: attentionKindInterruptedRun, ProjectID: task.ProjectID, WorkflowID: &workflowID, TaskID: task.ID, TaskShortID: task.ShortID, TaskTitle: task.Title, RunID: run.ID, SessionID: run.SessionID.String, Message: item.Summary, DetailJSON: run.InterruptionDetailJson, OccurredAtUnixMs: run.InterruptedAtUnixMs.Int64}
				item.Attention = &attention
			}
		case "task_canceled":
			item.Summary = "Task canceled"
		default:
			return nil, errors.New("activity kind is unsupported")
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *Service) sessionNamesByRun(ctx context.Context, runs []sqlitegen.TaskRunRecord) (map[string]string, error) {
	return loadSessionNamesByRun(ctx, s.queries, runs)
}

func (s *Service) transitionEdgesByTransitionID(ctx context.Context, transitions []sqlitegen.TaskTransitionRecord) (map[string][]sqlitegen.TaskTransitionEdgeRecord, error) {
	return loadTransitionEdgesByTransitionID(ctx, s.queries, transitions)
}

func (s *Service) commentsByID(ctx context.Context, ids []string) (map[string]sqlitegen.TaskComment, error) {
	out := map[string]sqlitegen.TaskComment{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.queries.ListTaskCommentsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ID] = row
	}
	return out, nil
}

func (s *Service) transitionsByID(ctx context.Context, ids []string) ([]sqlitegen.TaskTransitionRecord, error) {
	if len(ids) == 0 {
		return []sqlitegen.TaskTransitionRecord{}, nil
	}
	return s.queries.ListTaskTransitionsByIDs(ctx, ids)
}

func (s *Service) runsByID(ctx context.Context, ids []string) ([]sqlitegen.TaskRunRecord, error) {
	if len(ids) == 0 {
		return []sqlitegen.TaskRunRecord{}, nil
	}
	return s.queries.ListTaskRunsByIDs(ctx, ids)
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

type activityPageCursor struct {
	occurredAtUnixMs int64
	activityID       string
	hasValue         bool
}

func parseActivityPageToken(token string) (activityPageCursor, error) {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return activityPageCursor{}, nil
	}
	timestampPart, encodedID, ok := strings.Cut(trimmed, "|")
	if !ok {
		return activityPageCursor{}, errors.New("page_token is invalid")
	}
	occurredAt, err := strconv.ParseInt(timestampPart, 10, 64)
	if err != nil || occurredAt < 0 {
		return activityPageCursor{}, errors.New("page_token is invalid")
	}
	decodedID, err := base64.RawURLEncoding.DecodeString(encodedID)
	if err != nil || strings.TrimSpace(string(decodedID)) == "" {
		return activityPageCursor{}, errors.New("page_token is invalid")
	}
	return activityPageCursor{occurredAtUnixMs: occurredAt, activityID: string(decodedID), hasValue: true}, nil
}

func activityPageToken(item serverapi.WorkflowTaskActivityItem) string {
	return strconv.FormatInt(item.OccurredAtUnixMs, 10) + "|" + base64.RawURLEncoding.EncodeToString([]byte(item.ActivityID))
}

func interruptedRunMessage(reason *string, detailJSON string) string {
	message := "Run interrupted"
	if reason != nil && strings.TrimSpace(*reason) != "" {
		trimmedReason := strings.TrimSpace(*reason)
		message += ": " + trimmedReason
	}
	if detail := interruptionErrorDetail(detailJSON); detail != "" {
		message += ": " + detail
	}
	return message
}

func interruptionErrorDetail(detailJSON string) string {
	if strings.TrimSpace(detailJSON) == "" {
		return ""
	}
	var detail struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(detailJSON), &detail); err != nil {
		return ""
	}
	return strings.TrimSpace(detail.Error)
}

func bodyPreview(body string) string {
	trimmed := strings.TrimSpace(body)
	const limit = 96
	if len(trimmed) <= limit {
		return trimmed
	}
	return trimmed[:limit]
}

func markdownPreview(body string) serverapi.MarkdownPreview {
	trimmed := strings.TrimSpace(body)
	const codePointLimit = 512
	codePointCount := 0
	for byteIndex := range trimmed {
		if codePointCount == codePointLimit {
			return serverapi.MarkdownPreview{Markdown: trimmed[:byteIndex], Truncated: true}
		}
		codePointCount++
	}
	return serverapi.MarkdownPreview{Markdown: trimmed}
}

func definitionExecutionValidation(def workflow.Definition, roleResolver workflow.RoleResolver) *workflow.ValidationResult {
	result := workflow.ValidateDefinition(def, workflow.ValidationOptions{Context: workflow.ValidationContextExecution, RoleResolver: roleResolver})
	result.Errors = append(result.Errors, scriptPathDefinitionValidationErrors(def, nil)...)
	return &result
}

func scriptPathDefinitionValidationErrors(def workflow.Definition, rootPath *string) []workflow.ValidationError {
	out := []workflow.ValidationError{}
	for _, node := range def.Nodes {
		if node.Kind() != workflow.NodeKindScript {
			continue
		}
		diagnostics := workflowscript.Validate(workflowscript.ValidationRequest{
			RawPath:  workflow.NodeScriptPath(node).String(),
			RootPath: rootPath,
		})
		for _, diagnostic := range diagnostics {
			out = append(out, workflow.ValidationError{
				Code:          workflow.ValidationErrorCode(diagnostic.Code),
				Message:       diagnostic.Message,
				WorkflowID:    def.ID,
				NodeID:        workflow.NodeIDOf(node),
				BlocksContext: diagnostic.Blocking,
			})
		}
	}
	return out
}

type workflowBoardSelector interface {
	selectFrom(picker []serverapi.WorkflowPickerItem) *serverapi.WorkflowPickerItem
}

type workflowBoardDefaultSelector struct{}

func (workflowBoardDefaultSelector) selectFrom(picker []serverapi.WorkflowPickerItem) *serverapi.WorkflowPickerItem {
	return defaultWorkflowBoardSelection(picker)
}

type workflowBoardExplicitSelector struct {
	workflowID string
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

func exactWorkflowBoardSelection(picker []serverapi.WorkflowPickerItem, workflowID string) *serverapi.WorkflowPickerItem {
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
		dto := serverapi.WorkflowBoardGroup{GroupID: group.GroupID, Key: group.GroupKey, DisplayName: group.DisplayName, SortOrder: group.SortOrder}
		for _, node := range columnNodes {
			if node.GroupID == group.GroupID {
				dto.NodeIDs = append(dto.NodeIDs, node.ID)
			}
		}
		if len(dto.NodeIDs) == 0 {
			continue
		}
		groups = append(groups, dto)
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

func (s *Service) taskCard(task sqlitegen.TaskRecord, statusFact workflowTaskStatusFact, placements []sqlitegen.TaskNodePlacementRecord, runs []sqlitegen.TaskRunRecord, definition definitionSnapshot, sourceWorkspace serverapi.ProjectWorkspaceSummary) (serverapi.WorkflowBoardTaskCard, bool) {
	facts := s.projector.ProjectTaskFacts(TaskFactsInput{
		Task:       task,
		Status:     statusFact,
		Placements: placements,
		Runs:       runs,
		Definition: definition,
	})
	return serverapi.WorkflowBoardTaskCard{TaskID: task.ID, ShortID: task.ShortID, Title: task.Title, Preview: markdownPreview(task.Body), WorkflowID: task.WorkflowID, ActiveNodeIDs: append([]string(nil), facts.Status.NodeIDs...), SourceWorkspace: sourceWorkspace, Status: facts.Status, Actions: facts.Actions, UpdatedAtUnixMs: task.UpdatedAtUnixMs}, facts.Done
}

func (s *Service) applyBoardColumnTaskCounts(ctx context.Context, columns []serverapi.WorkflowBoardColumn, projectID string, workflowID string, canceledTerminalNodeID *string) error {
	rows, err := s.queries.ListBoardColumnTaskCounts(ctx, sqlitegen.ListBoardColumnTaskCountsParams{
		ProjectID:              projectID,
		WorkflowID:             workflowID,
		CanceledTerminalNodeID: nullableWorkflowViewString(canceledTerminalNodeID),
	})
	if err != nil {
		return err
	}
	indexByNodeID := map[string]int{}
	for index, column := range columns {
		indexByNodeID[column.Node.NodeID] = index
	}
	for _, row := range rows {
		nodeID := strings.TrimSpace(row.NodeID.String)
		if !row.NodeID.Valid || nodeID == "" {
			continue
		}
		if index, ok := indexByNodeID[nodeID]; ok {
			columns[index].TaskCount = int(row.TaskCount)
		}
	}
	return nil
}

func nullableWorkflowViewString(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
}

type boardNodeCardsPageCursor struct {
	direction boardNodeCardsPageDirection
	anchor    *boardNodeCardsPageAnchor
}

type boardNodeCardsPageAnchor struct {
	updatedAtUnixMs int64
	taskID          string
}

type boardNodeCardsPageDirection string

const (
	boardNodeCardsPageDirectionOlder boardNodeCardsPageDirection = "older"
	boardNodeCardsPageDirectionNewer boardNodeCardsPageDirection = "newer"
	boardNodeCardsPageTokenVersion                               = 2
)

type boardNodeCardsPageTokenPayload struct {
	Version         int                         `json:"version"`
	ProjectID       string                      `json:"project_id"`
	WorkflowID      string                      `json:"workflow_id"`
	NodeID          string                      `json:"node_id"`
	UpdatedAtUnixMs int64                       `json:"updated_at_unix_ms"`
	TaskID          string                      `json:"task_id"`
	Direction       boardNodeCardsPageDirection `json:"direction"`
}

func parseBoardNodeCardsPageToken(token *string, projectID string, workflowID string, nodeID string) (boardNodeCardsPageCursor, error) {
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
		strings.TrimSpace(payload.ProjectID) == "" ||
		strings.TrimSpace(payload.WorkflowID) == "" ||
		strings.TrimSpace(payload.NodeID) == "" ||
		strings.TrimSpace(payload.TaskID) == "" ||
		payload.TaskID != strings.TrimSpace(payload.TaskID) ||
		payload.UpdatedAtUnixMs < 0 {
		return boardNodeCardsPageCursor{}, ErrInvalidPageToken
	}
	switch payload.Direction {
	case boardNodeCardsPageDirectionOlder, boardNodeCardsPageDirectionNewer:
	default:
		return boardNodeCardsPageCursor{}, ErrInvalidPageToken
	}
	return boardNodeCardsPageCursor{
		direction: payload.Direction,
		anchor:    &boardNodeCardsPageAnchor{updatedAtUnixMs: payload.UpdatedAtUnixMs, taskID: payload.TaskID},
	}, nil
}

func boardNodeCardsPageToken(projectID string, workflowID string, nodeID string, direction boardNodeCardsPageDirection, task sqlitegen.TaskRecord) (*string, error) {
	payload := boardNodeCardsPageTokenPayload{
		Version:         boardNodeCardsPageTokenVersion,
		ProjectID:       projectID,
		WorkflowID:      workflowID,
		NodeID:          nodeID,
		UpdatedAtUnixMs: task.UpdatedAtUnixMs,
		TaskID:          task.ID,
		Direction:       direction,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(encoded)
	return &token, nil
}

func apiContextSource(in workflow.ContextSource) serverapi.WorkflowContextSource {
	source := workflow.CanonicalContextSource(in)
	return serverapi.WorkflowContextSource{Kind: string(source.Kind), NodeKey: string(source.NodeKey)}
}
