package workflowview

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
	activity    *Activity
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
	activity, err := NewActivity(metadataStore, definitions, projector)
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
		activity:    activity,
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
	if s == nil {
		return serverapi.WorkflowBoard{}, errors.New("workflow view service is required")
	}
	board, err := NewBoard(s.metadata, s.definitions, roleResolver)
	if err != nil {
		return serverapi.WorkflowBoard{}, err
	}
	return board.Get(ctx, req)
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
	if s == nil {
		return serverapi.WorkflowTaskActivityListResponse{}, errors.New("workflow view service is required")
	}
	return s.activity.List(ctx, req)
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
