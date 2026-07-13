package workflowview

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/runtime"
	askquestion "core/server/tools"
	"core/server/workflow"
	"core/server/workflowscript"
	"core/server/worktree"
	"core/shared/clientui"
	"core/shared/serverapi"
	"core/shared/toolspec"
)

type Service struct {
	metadata    *metadata.Store
	queries     *sqlitegen.Queries
	git         *worktree.GitInspector
	transcripts SessionTranscriptTailEntryProvider
	prompts     PendingPromptSource
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
	// ErrPendingQuestionNotFound is returned when no pending question matches the
	// requested ask id in a session transcript.
	ErrPendingQuestionNotFound = errors.New("pending question was not found")
)

type Option func(*Service)

type SessionTranscriptTailEntryProvider interface {
	SessionTranscriptTailEntries(ctx context.Context, sessionID string) ([]runtime.ChatEntry, error)
}

type PendingPromptSnapshot struct {
	Request askquestion.AskQuestionRequest
}

type PendingPromptSource interface {
	ListPendingPrompts(sessionID string) []PendingPromptSnapshot
}

func WithSessionTranscriptProvider(provider SessionTranscriptTailEntryProvider) Option {
	return func(s *Service) {
		s.transcripts = provider
	}
}

func WithPendingPromptSource(source PendingPromptSource) Option {
	return func(s *Service) {
		s.prompts = source
	}
}

func New(metadataStore *metadata.Store, opts ...Option) (*Service, error) {
	if metadataStore == nil || metadataStore.Queries() == nil {
		return nil, errors.New("metadata store is required")
	}
	svc := &Service{metadata: metadataStore, queries: metadataStore.Queries(), git: worktree.NewGitInspector(nil)}
	for _, opt := range opts {
		if opt != nil {
			opt(svc)
		}
	}
	return svc, nil
}

func (s *Service) GetDefinition(ctx context.Context, workflowID string) (serverapi.WorkflowDefinition, map[string]workflow.NodeKind, error) {
	if s == nil {
		return serverapi.WorkflowDefinition{}, nil, errors.New("workflow view service is required")
	}
	if strings.TrimSpace(workflowID) == "" {
		return serverapi.WorkflowDefinition{}, nil, errors.New("workflow_id is required")
	}
	return s.definition(ctx, workflowID)
}

func workflowExecutionTargetPolicyFromRow(mode string, customRef sql.NullString) serverapi.WorkflowExecutionTargetConfiguration {
	var ref *string
	if customRef.Valid {
		value := customRef.String
		ref = &value
	}
	return serverapi.WorkflowExecutionTargetConfiguration{
		Mode:      serverapi.WorkflowExecutionTargetMode(mode),
		CustomRef: ref,
	}
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
	pageSize := req.PageSize
	if pageSize == 0 {
		pageSize = 100
	}
	donePreviewLimit := req.DonePreviewLimit
	if donePreviewLimit == 0 {
		donePreviewLimit = 20
	}
	definitions, nodeKindsByWorkflowID, picker, err := s.workflowSelectionInputs(ctx, projectID, roleResolver)
	if err != nil {
		return serverapi.WorkflowBoard{}, err
	}
	project, err := s.metadata.GetProjectOverview(ctx, projectID)
	if err != nil {
		return serverapi.WorkflowBoard{}, err
	}
	workspaceContext := boardProjectWorkspaceContext(project)
	requestedWorkflowID := strings.TrimSpace(req.WorkflowID)
	if requestedWorkflowID == "" && strings.TrimSpace(req.PageToken) != "" {
		tokenWorkflowID, err := workflowBoardPageTokenWorkflowID(req.PageToken, projectID)
		if err != nil {
			return serverapi.WorkflowBoard{}, err
		}
		requestedWorkflowID = tokenWorkflowID
	}
	selected := selectWorkflow(picker, requestedWorkflowID)
	if selected.WorkflowID == "" {
		if strings.TrimSpace(req.PageToken) != "" {
			return serverapi.WorkflowBoard{}, errors.New("page_token is invalid")
		}
		return serverapi.WorkflowBoard{ProjectID: projectID, Project: projectBoardProject(project, workspaceContext), WorkflowPicker: picker, GeneratedAtUnixMs: time.Now().UTC().UnixMilli()}, nil
	}
	cursor, err := parseWorkflowBoardPageToken(req.PageToken, projectID, selected.WorkflowID)
	if err != nil {
		return serverapi.WorkflowBoard{}, err
	}
	def := definitions[selected.WorkflowID]
	nodeKinds := nodeKindsByWorkflowID[selected.WorkflowID]
	groups := boardGroups(def)
	columns := boardColumns(def)
	if err := s.applyBoardColumnTaskCounts(ctx, columns, projectID, selected.WorkflowID, canceledBoardTerminalNodeID(def)); err != nil {
		return serverapi.WorkflowBoard{}, err
	}
	cursorSet := int64(0)
	if cursor.hasValue {
		cursorSet = 1
	}
	openTasks, err := s.queries.ListBoardOpenTasks(ctx, sqlitegen.ListBoardOpenTasksParams{
		ProjectID:              projectID,
		WorkflowID:             selected.WorkflowID,
		CursorSet:              cursorSet,
		CursorUpdatedAtUnixMs:  cursor.updatedAtUnixMs,
		CursorTaskID:           cursor.taskID,
		CanceledTerminalNodeID: canceledBoardTerminalNodeID(def),
		LimitRows:              int64(pageSize + 1),
	})
	if err != nil {
		return serverapi.WorkflowBoard{}, err
	}
	candidates := openTasks
	hasNext := len(candidates) > pageSize
	if hasNext {
		candidates = candidates[:pageSize]
	}
	candidateTaskIDs := make([]string, 0, len(candidates))
	for _, task := range candidates {
		candidateTaskIDs = append(candidateTaskIDs, task.ID)
	}
	statusesByTaskID, err := s.taskStatusFacts(ctx, candidateTaskIDs)
	if err != nil {
		return serverapi.WorkflowBoard{}, err
	}
	pagePlacementsByTaskID, err := s.boardPlacementsByTask(ctx, candidates)
	if err != nil {
		return serverapi.WorkflowBoard{}, err
	}
	pageRunsByTaskID, err := s.taskRunsByTask(ctx, candidates)
	if err != nil {
		return serverapi.WorkflowBoard{}, err
	}
	cards := make([]serverapi.WorkflowBoardTaskCard, 0, len(candidates))
	for _, task := range candidates {
		cardPlacements := pagePlacementsByTaskID[task.ID]
		card, done := s.taskCard(task, statusesByTaskID[task.ID], effectiveBoardPlacementsForTask(task, cardPlacements, def, nodeKinds), pageRunsByTaskID[task.ID], def, nodeKinds, sourceWorkspaceForTask(task, workspaceContext.byID, workspaceContext.primary))
		if done {
			continue
		}
		cards = append(cards, card)
	}
	nextPageToken := ""
	if hasNext && len(candidates) > 0 {
		last := candidates[len(candidates)-1]
		nextPageToken = workflowBoardPageToken(projectID, selected.WorkflowID, last)
	}
	doneTasks, err := s.queries.ListBoardDonePreviewTasks(ctx, sqlitegen.ListBoardDonePreviewTasksParams{
		ProjectID:              projectID,
		WorkflowID:             selected.WorkflowID,
		CanceledTerminalNodeID: canceledBoardTerminalNodeID(def),
		LimitRows:              int64(donePreviewLimit),
	})
	if err != nil {
		return serverapi.WorkflowBoard{}, err
	}
	donePlacementsByTaskID, err := s.boardPlacementsByTask(ctx, doneTasks)
	if err != nil {
		return serverapi.WorkflowBoard{}, err
	}
	doneRunsByTaskID, err := s.taskRunsByTask(ctx, doneTasks)
	if err != nil {
		return serverapi.WorkflowBoard{}, err
	}
	doneTaskIDs := make([]string, 0, len(doneTasks))
	for _, task := range doneTasks {
		doneTaskIDs = append(doneTaskIDs, task.ID)
	}
	doneStatusesByTaskID, err := s.taskStatusFacts(ctx, doneTaskIDs)
	if err != nil {
		return serverapi.WorkflowBoard{}, err
	}
	donePreview := make([]serverapi.WorkflowBoardTaskCard, 0, len(doneTasks))
	for _, task := range doneTasks {
		card, done := s.taskCard(task, doneStatusesByTaskID[task.ID], effectiveBoardPlacementsForTask(task, donePlacementsByTaskID[task.ID], def, nodeKinds), doneRunsByTaskID[task.ID], def, nodeKinds, sourceWorkspaceForTask(task, workspaceContext.byID, workspaceContext.primary))
		if done {
			donePreview = append(donePreview, card)
		}
	}
	board := serverapi.WorkflowBoard{
		ProjectID:          projectID,
		Project:            projectBoardProject(project, workspaceContext),
		SelectedWorkflow:   selected,
		WorkflowPicker:     picker,
		Groups:             groups,
		Columns:            columns,
		Cards:              cards,
		DonePreview:        donePreview,
		HasHiddenDoneCards: false,
		NextPageToken:      nextPageToken,
		GeneratedAtUnixMs:  time.Now().UTC().UnixMilli(),
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
	definitions, _, picker, err := s.workflowSelectionInputs(ctx, projectID, roleResolver)
	if err != nil {
		return serverapi.WorkflowTaskListResponse{}, err
	}
	var selected serverapi.WorkflowPickerItem
	for _, candidate := range picker {
		if candidate.WorkflowID == workflowID {
			selected = candidate
			break
		}
	}
	if selected.WorkflowID == "" {
		return serverapi.WorkflowTaskListResponse{}, serverapi.WorkflowRequestValidationError{
			Code:    serverapi.WorkflowRequestErrorInvalidValue,
			Field:   "workflow_id",
			Message: fmt.Sprintf("unknown workflow %q for project %s", workflowID, projectID),
		}
	}
	def := definitions[selected.WorkflowID]
	columns := boardColumns(def)
	if err := validateWorkflowTaskListColumnKeys(req.ColumnKeys, columns); err != nil {
		return serverapi.WorkflowTaskListResponse{}, err
	}
	sortSelectors := normalizeWorkflowTaskListSort(req.Sort)
	columnStructureHash := workflowTaskListColumnStructureHash(def, columns)
	fingerprint := workflowTaskListRequestFingerprint(req, sortSelectors, columnStructureHash)
	cursor := workflowTaskListCursor{}
	if hasPageToken {
		if pageToken.ProjectID != projectID ||
			pageToken.WorkflowID != selected.WorkflowID ||
			pageToken.WorkflowVersion != def.Workflow.Version ||
			pageToken.ColumnStructureHash != columnStructureHash ||
			pageToken.StatusModelVersion != workflowTaskStatusModelVersion ||
			pageToken.Fingerprint != fingerprint {
			return serverapi.WorkflowTaskListResponse{}, ErrInvalidPageToken
		}
		cursor = pageToken.Cursor
	}
	rows, err := s.listWorkflowTaskListRows(ctx, workflowTaskListQueryRequest{
		projectID:              projectID,
		workflowID:             selected.WorkflowID,
		canceledTerminalNodeID: canceledBoardTerminalNodeID(def),
		columns:                columns,
		columnKeys:             req.ColumnKeys,
		statusKinds:            req.StatusKinds,
		attentionKinds:         req.AttentionKinds,
		sortSelectors:          sortSelectors,
		cursor:                 cursor,
		cursorSet:              hasPageToken,
		limit:                  pageSize + 1,
	})
	if err != nil {
		return serverapi.WorkflowTaskListResponse{}, err
	}
	pageItems := rows
	hasNext := len(pageItems) > pageSize
	if hasNext {
		pageItems = pageItems[:pageSize]
	}
	responseItems := make([]serverapi.WorkflowTaskListItem, 0, len(pageItems))
	for _, item := range pageItems {
		responseItems = append(responseItems, item.item)
	}
	nextPageToken := ""
	if hasNext && len(pageItems) > 0 {
		nextPageToken = workflowTaskListPageToken(workflowTaskListPageTokenPayload{
			Version:             workflowTaskListPageTokenVersion,
			ProjectID:           projectID,
			WorkflowID:          selected.WorkflowID,
			WorkflowVersion:     def.Workflow.Version,
			ColumnStructureHash: columnStructureHash,
			StatusModelVersion:  workflowTaskStatusModelVersion,
			Fingerprint:         fingerprint,
			Cursor:              workflowTaskListCursorFromRow(pageItems[len(pageItems)-1]),
		})
	}
	selectedCopy := selected
	return serverapi.WorkflowTaskListResponse{
		ProjectID:         projectID,
		WorkflowID:        selected.WorkflowID,
		SelectedWorkflow:  &selectedCopy,
		NextPageToken:     nextPageToken,
		GeneratedAtUnixMs: time.Now().UTC().UnixMilli(),
		Tasks:             responseItems,
	}, nil
}

func (s *Service) resolveWorkflowTaskListScope(ctx context.Context, projectIDValue *string, workflowIDValue *string, token *workflowTaskListPageTokenPayload) (string, string, error) {
	if token != nil {
		if projectIDValue != nil && *projectIDValue != token.ProjectID {
			return "", "", ErrInvalidPageToken
		}
		if workflowIDValue != nil && *workflowIDValue != token.WorkflowID {
			return "", "", ErrInvalidPageToken
		}
		projectIDValue = &token.ProjectID
		workflowIDValue = &token.WorkflowID
	}
	if projectIDValue != nil && workflowIDValue != nil {
		if _, err := s.queries.GetActiveProjectWorkflowLinkByWorkflow(ctx, sqlitegen.GetActiveProjectWorkflowLinkByWorkflowParams{ProjectID: *projectIDValue, WorkflowID: *workflowIDValue}); err == nil {
			return *projectIDValue, *workflowIDValue, nil
		} else if errors.Is(err, sql.ErrNoRows) {
			return "", "", &serverapi.WorkflowTaskListScopeError{Kind: serverapi.WorkflowTaskListScopeErrorKindNotLinked, ProjectIDs: []string{*projectIDValue}, WorkflowIDs: []string{*workflowIDValue}}
		} else {
			return "", "", err
		}
	}
	if projectIDValue != nil {
		links, err := s.queries.ListProjectWorkflowLinks(ctx, *projectIDValue)
		if err != nil {
			return "", "", err
		}
		if len(links) == 1 {
			return *projectIDValue, links[0].WorkflowID, nil
		}
		candidates := make([]string, 0, len(links))
		for _, link := range links {
			candidates = append(candidates, link.WorkflowID)
		}
		if len(candidates) == 0 {
			return "", "", &serverapi.WorkflowTaskListScopeError{Kind: serverapi.WorkflowTaskListScopeErrorKindNotLinked, ProjectIDs: []string{*projectIDValue}}
		}
		missing := serverapi.WorkflowTaskListScopeDimensionWorkflow
		return "", "", &serverapi.WorkflowTaskListScopeError{Kind: serverapi.WorkflowTaskListScopeErrorKindAmbiguous, MissingScope: &missing, ProjectIDs: []string{*projectIDValue}, WorkflowIDs: candidates}
	}
	if workflowIDValue != nil {
		links, err := s.queries.ListWorkflowProjectLinks(ctx, *workflowIDValue)
		if err != nil {
			return "", "", err
		}
		if len(links) == 1 {
			return links[0].ProjectID, *workflowIDValue, nil
		}
		candidates := make([]string, 0, len(links))
		for _, link := range links {
			candidates = append(candidates, link.ProjectID)
		}
		if len(candidates) == 0 {
			return "", "", &serverapi.WorkflowTaskListScopeError{Kind: serverapi.WorkflowTaskListScopeErrorKindNotLinked, WorkflowIDs: []string{*workflowIDValue}}
		}
		missing := serverapi.WorkflowTaskListScopeDimensionProject
		return "", "", &serverapi.WorkflowTaskListScopeError{Kind: serverapi.WorkflowTaskListScopeErrorKindAmbiguous, MissingScope: &missing, ProjectIDs: candidates, WorkflowIDs: []string{*workflowIDValue}}
	}
	return "", "", &serverapi.WorkflowTaskListScopeError{Kind: serverapi.WorkflowTaskListScopeErrorKindNotLinked}
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

func (s *Service) workflowSelectionInputs(ctx context.Context, projectID string, roleResolver workflow.RoleResolver) (map[string]serverapi.WorkflowDefinition, map[string]map[string]workflow.NodeKind, []serverapi.WorkflowPickerItem, error) {
	links, err := s.queries.ListProjectWorkflowLinks(ctx, projectID)
	if err != nil {
		return nil, nil, nil, err
	}
	taskActivityRows, err := s.queries.ListProjectWorkflowTaskActivity(ctx, projectID)
	if err != nil {
		return nil, nil, nil, err
	}
	workflowIDs := make([]string, 0, len(links)+len(taskActivityRows))
	seen := map[string]bool{}
	linkByWorkflowID := map[string]sqlitegen.ProjectWorkflowLinkRecord{}
	for _, link := range links {
		if linkByWorkflowID[link.WorkflowID].ID == "" {
			linkByWorkflowID[link.WorkflowID] = link
		}
		if !seen[link.WorkflowID] {
			workflowIDs = append(workflowIDs, link.WorkflowID)
			seen[link.WorkflowID] = true
		}
	}
	activityByWorkflowID := map[string]int64{}
	for _, activity := range taskActivityRows {
		activityByWorkflowID[activity.WorkflowID] = activity.LatestUpdatedAtUnixMs
		if !seen[activity.WorkflowID] {
			workflowIDs = append(workflowIDs, activity.WorkflowID)
			seen[activity.WorkflowID] = true
		}
	}
	definitions := make(map[string]serverapi.WorkflowDefinition, len(workflowIDs))
	nodeKindsByWorkflowID := make(map[string]map[string]workflow.NodeKind, len(workflowIDs))
	picker := make([]serverapi.WorkflowPickerItem, 0, len(workflowIDs))
	for _, workflowID := range workflowIDs {
		def, nodeKinds, err := s.definition(ctx, workflowID)
		if err != nil {
			return nil, nil, nil, err
		}
		definitions[workflowID] = def
		nodeKindsByWorkflowID[workflowID] = nodeKinds
		link := linkByWorkflowID[workflowID]
		validation := definitionExecutionValidation(def, roleResolver)
		picker = append(picker, serverapi.WorkflowPickerItem{
			WorkflowID:           workflowID,
			DisplayName:          def.Workflow.Name,
			Description:          def.Workflow.Description,
			Version:              def.Workflow.Version,
			IsProjectDefault:     link.ID != "" && link.IsDefault != 0,
			ValidForTaskCreation: !validation.HasBlockingErrors() && link.ID != "",
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
	return definitions, nodeKindsByWorkflowID, picker, nil
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
	def, nodeKinds, err := s.definition(ctx, workflowID)
	if err != nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, err
	}
	if _, ok := workflowNodeByID(def)[nodeID]; !ok {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, errors.New("node_id is invalid for workflow")
	}
	pageSize := req.PageSize
	if pageSize == 0 {
		pageSize = 100
	}
	if pageSize > 200 {
		pageSize = 200
	}
	cursor, err := parseBoardNodeCardsPageToken(req.PageToken, projectID, workflowID, nodeID)
	if err != nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, err
	}
	cursorSet := int64(0)
	if cursor.hasValue {
		cursorSet = 1
	}
	tasks, err := s.queries.ListBoardNodeTasks(ctx, sqlitegen.ListBoardNodeTasksParams{
		ProjectID:              projectID,
		WorkflowID:             workflowID,
		CursorSet:              cursorSet,
		CursorUpdatedAtUnixMs:  cursor.updatedAtUnixMs,
		CursorTaskID:           cursor.taskID,
		NodeID:                 sql.NullString{String: nodeID, Valid: strings.TrimSpace(nodeID) != ""},
		CanceledTerminalNodeID: canceledBoardTerminalNodeID(def),
		LimitRows:              int64(pageSize + 1),
	})
	if err != nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, err
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
	candidates := tasks
	hasNext := len(candidates) > pageSize
	if hasNext {
		candidates = candidates[:pageSize]
	}
	candidateTaskIDs := make([]string, 0, len(candidates))
	for _, task := range candidates {
		candidateTaskIDs = append(candidateTaskIDs, task.ID)
	}
	statusesByTaskID, err := s.taskStatusFacts(ctx, candidateTaskIDs)
	if err != nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, err
	}
	runsByTaskID, err := s.taskRunsByTask(ctx, candidates)
	if err != nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, err
	}
	cards := make([]serverapi.WorkflowBoardTaskCard, 0, len(candidates))
	for _, task := range candidates {
		card, _ := s.taskCard(task, statusesByTaskID[task.ID], effectiveBoardPlacementsForTask(task, placementsByTaskID[task.ID], def, nodeKinds), runsByTaskID[task.ID], def, nodeKinds, sourceWorkspaceForTask(task, workspaceContext.byID, workspaceContext.primary))
		cards = append(cards, card)
	}
	nextPageToken := ""
	if hasNext && len(candidates) > 0 {
		last := candidates[len(candidates)-1]
		nextPageToken = boardNodeCardsPageToken(projectID, workflowID, nodeID, last)
	}
	return serverapi.WorkflowBoardNodeCardsListResponse{ProjectID: projectID, WorkflowID: workflowID, NodeID: nodeID, Cards: cards, NextPageToken: nextPageToken, GeneratedAtUnixMs: time.Now().UTC().UnixMilli()}, nil
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
	rows, err := s.queries.ListPendingApprovalSourcePlacementsByTasks(ctx, taskIDs)
	if err != nil {
		return nil, err
	}
	byTaskID := make(map[string][]sqlitegen.TaskNodePlacementRecord)
	for _, row := range rows {
		byTaskID[row.TaskID] = append(byTaskID[row.TaskID], pendingApprovalSourcePlacement(row))
	}
	return byTaskID, nil
}

func pendingApprovalSourcePlacement(row sqlitegen.ListPendingApprovalSourcePlacementsByTasksRow) sqlitegen.TaskNodePlacementRecord {
	return sqlitegen.TaskNodePlacementRecord{
		ID:                        row.ID,
		TaskID:                    row.TaskID,
		NodeID:                    row.NodeID,
		State:                     row.State,
		CreatedByTransitionID:     row.CreatedByTransitionID,
		ParallelBatchTransitionID: row.ParallelBatchTransitionID,
		ParallelBranchEdgeID:      row.ParallelBranchEdgeID,
		CreatedAtUnixMs:           row.CreatedAtUnixMs,
		UpdatedAtUnixMs:           row.UpdatedAtUnixMs,
	}
}

func taskNodePlacementNodeID(placement sqlitegen.TaskNodePlacementRecord) (string, bool) {
	nodeID := nullableWorkflowViewNodeID(placement.NodeID)
	return nodeID, nodeID != ""
}

func nullableWorkflowViewNodeID(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return strings.TrimSpace(value.String)
}

func (s *Service) GetTask(ctx context.Context, taskID string) (serverapi.WorkflowTaskDetail, error) {
	if s == nil {
		return serverapi.WorkflowTaskDetail{}, errors.New("workflow view service is required")
	}
	if strings.TrimSpace(taskID) == "" {
		return serverapi.WorkflowTaskDetail{}, ErrTaskIDRequired
	}
	task, err := s.queries.GetTask(ctx, taskID)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	def, nodeKinds, err := s.definition(ctx, task.WorkflowID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return serverapi.WorkflowTaskDetail{}, err
	}
	placements, err := s.queries.ListTaskNodePlacements(ctx, task.ID)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	pendingApprovalPlacements, err := s.pendingApprovalSourcePlacementsByTask(ctx, []string{task.ID})
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	placements = append(placements, pendingApprovalPlacements[task.ID]...)
	runs, err := s.queries.ListTaskRuns(ctx, task.ID)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	transitions, err := s.queries.ListTaskTransitions(ctx, task.ID)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	comments, err := s.queries.ListTaskComments(ctx, sqlitegen.ListTaskCommentsParams{TaskID: task.ID, OffsetRows: 0, LimitRows: -1})
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	project, err := s.metadata.GetProjectOverview(ctx, task.ProjectID)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	workspaceContext := boardProjectWorkspaceContext(project)
	linkByWorkflowID := map[string]sqlitegen.ProjectWorkflowLinkRecord{}
	links, err := s.queries.ListProjectWorkflowLinks(ctx, task.ProjectID)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	for _, link := range links {
		if linkByWorkflowID[link.WorkflowID].ID == "" {
			linkByWorkflowID[link.WorkflowID] = link
		}
	}
	nodeByID := workflowNodeByID(def)
	statusFact, err := s.taskStatusFact(ctx, task.ID)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	status := statusFact.Status
	summary := taskSummary(task, status, statusFact.Done)
	actions := taskActions(task, summary, placements, runs, def, nodeKinds)
	sourceWorkspace := sourceWorkspaceForTask(task, workspaceContext.byID, workspaceContext.primary)
	executionTarget, err := s.executionTargetForTask(ctx, task, sourceWorkspace)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	detail := serverapi.WorkflowTaskDetail{Summary: summary, Project: projectBoardProject(project, workspaceContext), Workflow: workflowPickerItem(def, linkByWorkflowID[task.WorkflowID], nil), Body: task.Body, SourceURL: task.SourceUrl, SourceWorkspace: sourceWorkspace, ExecutionTarget: executionTarget, Status: status, Actions: actions}
	attention, err := s.attentionItems(ctx, task.ProjectID, task.ID, nil)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	sort.SliceStable(attention, func(i, j int) bool {
		if attention[i].OccurredAtUnixMs != attention[j].OccurredAtUnixMs {
			return attention[i].OccurredAtUnixMs > attention[j].OccurredAtUnixMs
		}
		return attention[i].ID > attention[j].ID
	})
	detail.Attention = attention
	for _, placement := range placements {
		detail.Placements = append(detail.Placements, placementDTO(placement, nodeByID))
	}
	sessionNames, err := s.sessionNamesByRun(ctx, runs)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	for _, run := range runs {
		detail.Runs = append(detail.Runs, runDTO(run, nodeByID, sessionNames))
	}
	edgesByTransitionID, err := s.transitionEdgesByTransitionID(ctx, transitions)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	for _, transition := range transitions {
		dto, err := transitionDTO(transition, edgesByTransitionID[transition.ID])
		if err != nil {
			return serverapi.WorkflowTaskDetail{}, err
		}
		detail.Transitions = append(detail.Transitions, dto)
	}
	for _, comment := range comments {
		detail.Comments = append(detail.Comments, commentDTO(comment))
	}
	return detail, nil
}

func (s *Service) executionTargetForTask(ctx context.Context, task sqlitegen.TaskRecord, sourceWorkspace serverapi.ProjectWorkspaceSummary) (*serverapi.WorkflowExecutionTarget, error) {
	if !task.ExecutionTargetMode.Valid {
		if task.ExecutionTargetRequestedRef.Valid ||
			task.ExecutionTargetResolvedRef.Valid ||
			task.ExecutionTargetCommitOid.Valid ||
			task.ExecutionTargetProvenance.Valid {
			return nil, errors.New("unlocked task has execution target facts")
		}
		return nil, nil
	}
	target := &serverapi.WorkflowExecutionTarget{
		Mode:         serverapi.WorkflowExecutionTargetMode(task.ExecutionTargetMode.String),
		RequestedRef: metadata.OptionalString(task.ExecutionTargetRequestedRef),
		ResolvedRef:  metadata.OptionalString(task.ExecutionTargetResolvedRef),
		CommitOID:    metadata.OptionalString(task.ExecutionTargetCommitOid),
		Provenance:   serverapi.WorkflowExecutionTargetProvenance(task.ExecutionTargetProvenance.String),
	}
	if target.Mode == serverapi.WorkflowExecutionTargetModeNone {
		root := sourceWorkspace.RootPath
		target.EffectiveRoot = &root
		if err := target.Validate(); err != nil {
			return nil, fmt.Errorf("project task execution target: %w", err)
		}
		return target, nil
	}

	worktreeID := strings.TrimSpace(task.ManagedWorktreeID.String)
	if task.ManagedWorktreeID.Valid && worktreeID != "" {
		row, err := s.queries.GetWorktreeByID(ctx, worktreeID)
		switch {
		case err == nil:
			facts := worktreeKentFacts(row)
			target.ManagedWorktree = &facts
			if info, statErr := os.Stat(facts.CanonicalRoot); statErr == nil && info.IsDir() {
				root := facts.CanonicalRoot
				target.EffectiveRoot = &root
				if identity, inspectErr := s.git.ValidateManagedWorktreeIdentity(ctx, worktree.ManagedWorktreeIdentitySpec{
					SourceWorkspaceRoot:  sourceWorkspace.RootPath,
					ExpectedWorktreeRoot: facts.CanonicalRoot,
				}); inspectErr == nil {
					if branchName, ok := identity.NamedBranch(); ok {
						target.CurrentBranch = &branchName
					}
				} else if ctxErr := ctx.Err(); ctxErr != nil {
					return nil, ctxErr
				}
			} else if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
		case !errors.Is(err, sql.ErrNoRows):
			return nil, err
		}
	}
	if err := target.Validate(); err != nil {
		return nil, fmt.Errorf("project task execution target: %w", err)
	}
	return target, nil
}

func (s *Service) GetTaskByProjectShortID(ctx context.Context, projectID string, shortID string) (serverapi.WorkflowTaskDetail, error) {
	if s == nil {
		return serverapi.WorkflowTaskDetail{}, errors.New("workflow view service is required")
	}
	trimmedProjectID := strings.TrimSpace(projectID)
	if trimmedProjectID == "" {
		return serverapi.WorkflowTaskDetail{}, errors.New("project_id is required")
	}
	trimmedShortID := strings.TrimSpace(shortID)
	if trimmedShortID == "" {
		return serverapi.WorkflowTaskDetail{}, errors.New("short_id is required")
	}
	task, err := s.queries.GetTaskByProjectShortID(ctx, sqlitegen.GetTaskByProjectShortIDParams{ProjectID: trimmedProjectID, ShortID: trimmedShortID})
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	return s.GetTask(ctx, task.ID)
}

func (s *Service) GetTaskByShortID(ctx context.Context, shortID string) (serverapi.WorkflowTaskDetail, error) {
	if s == nil {
		return serverapi.WorkflowTaskDetail{}, errors.New("workflow view service is required")
	}
	trimmedShortID := strings.TrimSpace(shortID)
	if trimmedShortID == "" {
		return serverapi.WorkflowTaskDetail{}, errors.New("short_id is required")
	}
	tasks, err := s.queries.ListTasksByShortID(ctx, trimmedShortID)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	if len(tasks) == 0 {
		return serverapi.WorkflowTaskDetail{}, sql.ErrNoRows
	}
	if len(tasks) > 1 {
		return serverapi.WorkflowTaskDetail{}, fmt.Errorf("task short_id %q is ambiguous; use task id", trimmedShortID)
	}
	return s.GetTask(ctx, tasks[0].ID)
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
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowAttentionListResponse{}, err
	}
	pageSize := req.PageSize
	if pageSize == 0 {
		pageSize = 50
	}
	cursor, err := parseAttentionPageToken(req.PageToken, strings.TrimSpace(req.ProjectID))
	if err != nil {
		return serverapi.WorkflowAttentionListResponse{}, err
	}
	items, nextPageToken, err := s.attentionItemsPage(ctx, strings.TrimSpace(req.ProjectID), pageSize, cursor, roleResolver)
	if err != nil {
		return serverapi.WorkflowAttentionListResponse{}, err
	}
	return serverapi.WorkflowAttentionListResponse{Items: items, NextPageToken: nextPageToken, GeneratedAtUnixMs: time.Now().UTC().UnixMilli()}, nil
}

func (s *Service) ListTaskAttention(ctx context.Context, req serverapi.WorkflowTaskAttentionListRequest, roleResolver workflow.RoleResolver) (serverapi.WorkflowTaskAttentionListResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskAttentionListResponse{}, err
	}
	task, err := s.queries.GetTask(ctx, strings.TrimSpace(req.TaskID))
	if err != nil {
		return serverapi.WorkflowTaskAttentionListResponse{}, err
	}
	items, err := s.attentionItems(ctx, task.ProjectID, task.ID, roleResolver)
	if err != nil {
		return serverapi.WorkflowTaskAttentionListResponse{}, err
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].OccurredAtUnixMs != items[j].OccurredAtUnixMs {
			return items[i].OccurredAtUnixMs > items[j].OccurredAtUnixMs
		}
		return items[i].ID > items[j].ID
	})
	return serverapi.WorkflowTaskAttentionListResponse{Items: items, GeneratedAtUnixMs: time.Now().UTC().UnixMilli()}, nil
}

type attentionPageCursor struct {
	occurredAtUnixMs int64
	itemID           string
	hasValue         bool
}

type attentionCandidateRow struct {
	kind                   string
	id                     string
	projectID              string
	workflowID             string
	taskID                 string
	shortID                string
	title                  string
	runID                  string
	sessionID              string
	askID                  string
	taskTransitionID       string
	interruptionReason     *string
	interruptionDetailJSON string
	occurredAtUnixMs       int64
}

func (s *Service) attentionItemsPage(ctx context.Context, projectID string, pageSize int, cursor attentionPageCursor, roleResolver workflow.RoleResolver) ([]serverapi.WorkflowAttentionItem, string, error) {
	items := make([]serverapi.WorkflowAttentionItem, 0, pageSize)
	questions := newPendingQuestionResolver(s.transcripts, s.prompts)
	current := cursor
	// attentionItemFromCandidate drops candidates that no longer warrant
	// attention (e.g. a validation_blocker whose workflow now validates), so a
	// single pageSize+1 fetch can come back short while later candidates still
	// hold real items. Keep advancing the candidate cursor until the page is
	// full or the candidate stream is exhausted.
	for len(items) < pageSize {
		candidates, err := s.attentionItemCandidates(ctx, projectID, "", current, pageSize+1)
		if err != nil {
			return nil, "", err
		}
		if len(candidates) == 0 {
			break
		}
		moreCandidates := len(candidates) > pageSize
		batch := candidates
		if moreCandidates {
			batch = candidates[:pageSize]
		}
		for i := range batch {
			candidate := batch[i]
			item, ok, err := s.attentionItemFromCandidate(ctx, candidate, roleResolver, questions)
			if err != nil {
				return nil, "", err
			}
			if !ok {
				continue
			}
			items = append(items, item)
			if len(items) == pageSize {
				return items, attentionPageTokenFor(projectID, candidate.occurredAtUnixMs, candidate.id), nil
			}
		}
		if !moreCandidates {
			break
		}
		current = attentionCandidateCursor(batch[len(batch)-1])
	}
	return items, "", nil
}

func attentionCandidateCursor(row attentionCandidateRow) attentionPageCursor {
	return attentionPageCursor{occurredAtUnixMs: row.occurredAtUnixMs, itemID: row.id, hasValue: true}
}

func (s *Service) attentionItemCandidates(ctx context.Context, projectID string, taskID string, cursor attentionPageCursor, limit int) ([]attentionCandidateRow, error) {
	cursorSet := int64(0)
	if cursor.hasValue {
		cursorSet = 1
	}
	rows, err := s.queries.ListWorkflowAttentionCandidates(ctx, sqlitegen.ListWorkflowAttentionCandidatesParams{
		PageLimit:              int64(limit),
		ProjectID:              strings.TrimSpace(projectID),
		TaskID:                 strings.TrimSpace(taskID),
		CursorActive:           cursorSet,
		CursorOccurredAtUnixMs: cursor.occurredAtUnixMs,
		CursorItemID:           cursor.itemID,
	})
	if err != nil {
		return nil, err
	}
	items := make([]attentionCandidateRow, 0, len(rows))
	for _, row := range rows {
		interruptionReason, err := nullableAttentionInterruptionReason(row.InterruptionReason)
		if err != nil {
			return nil, err
		}
		items = append(items, attentionCandidateRow{
			kind:                   row.Kind,
			id:                     row.ID,
			projectID:              row.ProjectID,
			workflowID:             row.WorkflowID,
			taskID:                 row.TaskID,
			shortID:                row.ShortID,
			title:                  row.Title,
			runID:                  row.RunID,
			sessionID:              row.SessionID,
			askID:                  row.AskID,
			taskTransitionID:       row.TaskTransitionID,
			interruptionReason:     interruptionReason,
			interruptionDetailJSON: row.InterruptionDetailJson,
			occurredAtUnixMs:       row.OccurredAtUnixMs,
		})
	}
	return items, nil
}

func (s *Service) attentionItemFromCandidate(ctx context.Context, row attentionCandidateRow, roleResolver workflow.RoleResolver, questions *pendingQuestionResolver) (serverapi.WorkflowAttentionItem, bool, error) {
	switch row.kind {
	case "approval":
		return serverapi.WorkflowAttentionItem{ID: row.id, Kind: "approval", ProjectID: row.projectID, WorkflowID: row.workflowID, TaskID: row.taskID, TaskShortID: row.shortID, TaskTitle: row.title, TaskTransitionID: row.taskTransitionID, Message: "Approval required", OccurredAtUnixMs: row.occurredAtUnixMs}, true, nil
	case "question":
		question, err := questions.Question(ctx, row.sessionID, row.askID)
		if err != nil {
			question = pendingQuestion{message: pendingQuestionFallbackMessage}
		}
		return workflowQuestionAttentionItem(row.id, row.projectID, row.workflowID, row.taskID, row.shortID, row.title, row.runID, row.sessionID, row.askID, question, row.occurredAtUnixMs), true, nil
	case attentionKindInterruptedRun:
		return serverapi.WorkflowAttentionItem{ID: row.id, Kind: attentionKindInterruptedRun, ProjectID: row.projectID, WorkflowID: row.workflowID, TaskID: row.taskID, TaskShortID: row.shortID, TaskTitle: row.title, RunID: row.runID, SessionID: row.sessionID, Message: interruptedRunMessage(row.interruptionReason, row.interruptionDetailJSON), DetailJSON: row.interruptionDetailJSON, OccurredAtUnixMs: row.occurredAtUnixMs}, true, nil
	case "validation_blocker":
		def, _, err := s.definition(ctx, row.workflowID)
		if err != nil {
			return serverapi.WorkflowAttentionItem{}, false, err
		}
		validation := definitionExecutionValidation(def, roleResolver)
		if !validation.HasBlockingErrors() {
			return serverapi.WorkflowAttentionItem{}, false, nil
		}
		return serverapi.WorkflowAttentionItem{ID: row.id, Kind: "validation_blocker", ProjectID: row.projectID, WorkflowID: row.workflowID, Message: fmt.Sprintf("Workflow %q is invalid for task start", def.Workflow.Name), OccurredAtUnixMs: row.occurredAtUnixMs}, true, nil
	default:
		return serverapi.WorkflowAttentionItem{}, false, fmt.Errorf("unknown attention item kind %q", row.kind)
	}
}

func (s *Service) attentionItems(ctx context.Context, projectID string, taskID string, roleResolver workflow.RoleResolver) ([]serverapi.WorkflowAttentionItem, error) {
	items := []serverapi.WorkflowAttentionItem{}
	approvals, err := s.approvalAttentionItems(ctx, projectID, taskID)
	if err != nil {
		return nil, err
	}
	items = append(items, approvals...)
	questions, err := s.questionAttentionItems(ctx, projectID, taskID)
	if err != nil {
		return nil, err
	}
	items = append(items, questions...)
	interrupted, err := s.interruptedRunAttentionItems(ctx, projectID, taskID)
	if err != nil {
		return nil, err
	}
	items = append(items, interrupted...)
	if strings.TrimSpace(taskID) == "" {
		blockers, err := s.validationAttentionItems(ctx, projectID, roleResolver)
		if err != nil {
			return nil, err
		}
		items = append(items, blockers...)
	}
	return items, nil
}

func (s *Service) definition(ctx context.Context, workflowID string) (serverapi.WorkflowDefinition, map[string]workflow.NodeKind, error) {
	row, err := s.queries.GetWorkflow(ctx, workflowID)
	if err != nil {
		return serverapi.WorkflowDefinition{}, nil, err
	}
	nodes, err := s.queries.ListWorkflowNodes(ctx, workflowID)
	if err != nil {
		return serverapi.WorkflowDefinition{}, nil, err
	}
	def := serverapi.WorkflowDefinition{Workflow: serverapi.WorkflowRecord{
		ID:                    row.ID,
		Name:                  row.Name,
		Description:           row.Description,
		Version:               row.Version,
		ExecutionTargetPolicy: workflowExecutionTargetPolicyFromRow(row.ExecutionTargetPolicy, row.ExecutionTargetCustomRef),
	}}
	nodeGroups, err := s.queries.ListWorkflowNodeGroups(ctx, workflowID)
	if err != nil {
		return serverapi.WorkflowDefinition{}, nil, err
	}
	groupByID := map[string]serverapi.WorkflowNodeGroup{}
	for _, group := range nodeGroups {
		dto := serverapi.WorkflowNodeGroup{GroupID: group.ID, WorkflowID: group.WorkflowID, GroupKey: group.GroupKey, DisplayName: group.DisplayName, SortOrder: int(group.SortOrder)}
		groupByID[group.ID] = dto
		def.NodeGroups = append(def.NodeGroups, dto)
	}
	groups, err := s.queries.ListWorkflowTransitionGroups(ctx, workflowID)
	if err != nil {
		return serverapi.WorkflowDefinition{}, nil, err
	}
	edges, err := s.queries.ListWorkflowEdges(ctx, workflowID)
	if err != nil {
		return serverapi.WorkflowDefinition{}, nil, err
	}
	nodeKinds := map[string]workflow.NodeKind{}
	for _, node := range nodes {
		inputFields := []serverapi.WorkflowInputField{}
		if err := workflow.UnmarshalString(node.InputFieldsJson, &inputFields); err != nil {
			return serverapi.WorkflowDefinition{}, nil, err
		}
		joinProviders := []serverapi.WorkflowJoinInputProvider{}
		if err := workflow.UnmarshalString(node.JoinInputProvidersJson, &joinProviders); err != nil {
			return serverapi.WorkflowDefinition{}, nil, err
		}
		fields := []serverapi.WorkflowOutputField{}
		if err := workflow.UnmarshalString(node.OutputFieldsJson, &fields); err != nil {
			return serverapi.WorkflowDefinition{}, nil, err
		}
		groupID := strings.TrimSpace(node.GroupID.String)
		groupKey := ""
		if group, ok := groupByID[groupID]; ok {
			groupKey = group.GroupKey
		}
		var scriptPath *string
		if node.ScriptPath.Valid {
			value := node.ScriptPath.String
			scriptPath = &value
		}
		def.Nodes = append(def.Nodes, serverapi.WorkflowNode{ID: node.ID, WorkflowID: node.WorkflowID, Key: node.NodeKey, Kind: node.Kind, DisplayName: node.DisplayName, GroupID: groupID, GroupKey: groupKey, SubagentRole: node.SubagentRole, PromptTemplate: node.PromptTemplate, CompletionMode: node.CompletionMode, ScriptPath: scriptPath, InputFields: inputFields, JoinInputProviders: joinProviders, OutputFields: fields})
		nodeKinds[node.ID] = workflow.NodeKind(node.Kind)
	}
	for _, group := range groups {
		def.TransitionGroups = append(def.TransitionGroups, serverapi.WorkflowTransitionGroup{ID: group.ID, WorkflowID: group.WorkflowID, SourceNodeID: group.SourceNodeID, TransitionID: string(group.TransitionID), DisplayName: group.DisplayName, Description: group.Description})
	}
	for _, edge := range edges {
		inputs := []serverapi.WorkflowInputBinding{}
		parameters := []serverapi.WorkflowParameter{}
		requirements := []serverapi.WorkflowOutputRequirement{}
		if err := workflow.UnmarshalString(edge.ParametersJson, &parameters); err != nil {
			return serverapi.WorkflowDefinition{}, nil, err
		}
		if err := workflow.UnmarshalString(edge.InputBindingsJson, &inputs); err != nil {
			return serverapi.WorkflowDefinition{}, nil, err
		}
		if err := workflow.UnmarshalString(edge.OutputRequirementsJson, &requirements); err != nil {
			return serverapi.WorkflowDefinition{}, nil, err
		}
		def.Edges = append(def.Edges, serverapi.WorkflowEdge{ID: edge.ID, WorkflowID: edge.WorkflowID, TransitionGroupID: edge.TransitionGroupID, Key: edge.EdgeKey, TargetNodeID: edge.TargetNodeID, RequiresApproval: edge.RequiresApproval != 0, ContextMode: edge.ContextMode, ContextSource: apiContextSource(workflow.ContextSource{Kind: workflow.ContextSourceKind(edge.ContextSourceKind), NodeKey: workflow.ModelKey(edge.ContextSourceNodeKey)}), PromptTemplate: edge.PromptTemplate, Parameters: parameters, InputBindings: inputs, OutputRequirements: requirements})
	}
	def.DerivedWiring = DerivedWiring(definitionForValidation(def))
	return def, nodeKinds, nil
}

func taskSummary(task sqlitegen.TaskRecord, status serverapi.WorkflowTaskStatus, done bool) serverapi.WorkflowTaskSummary {
	return serverapi.WorkflowTaskSummary{
		ID:                task.ID,
		ProjectID:         task.ProjectID,
		WorkflowID:        task.WorkflowID,
		ShortID:           task.ShortID,
		Title:             task.Title,
		BodyPreview:       bodyPreview(task.Body),
		SourceWorkspaceID: strings.TrimSpace(task.SourceWorkspaceID.String),
		CanceledAt:        metadata.OptionalInt64(task.CanceledAtUnixMs),
		CancelReason:      metadata.OptionalString(task.CancellationReason),
		CreatedAtUnixMs:   task.CreatedAtUnixMs,
		UpdatedAtUnixMs:   task.UpdatedAtUnixMs,
		Done:              done,
		ActiveNodeIDs:     append([]string(nil), status.NodeIDs...),
	}
}

func placementDTO(placement sqlitegen.TaskNodePlacementRecord, nodes map[string]serverapi.WorkflowNode) serverapi.WorkflowPlacement {
	nodeID, _ := taskNodePlacementNodeID(placement)
	dto := serverapi.WorkflowPlacement{ID: placement.ID, TaskID: placement.TaskID, NodeID: nodeID, State: placement.State, ParallelBatchTransitionID: strings.TrimSpace(placement.ParallelBatchTransitionID.String), ParallelBranchEdgeID: strings.TrimSpace(placement.ParallelBranchEdgeID.String)}
	if node, ok := nodes[nodeID]; ok {
		dto.NodeKey = node.Key
		dto.NodeDisplayName = node.DisplayName
		dto.NodeKind = node.Kind
	}
	return dto
}

func commentDTO(comment sqlitegen.TaskComment) serverapi.WorkflowTaskComment {
	return serverapi.WorkflowTaskComment{ID: comment.ID, TaskID: comment.TaskID, Body: comment.Body, Author: comment.AuthorKind, AuthorID: comment.AuthorID, CreatedAtUnixMs: comment.CreatedAtUnixMs, UpdatedAt: comment.UpdatedAtUnixMs}
}

func workflowNodeByID(def serverapi.WorkflowDefinition) map[string]serverapi.WorkflowNode {
	out := make(map[string]serverapi.WorkflowNode, len(def.Nodes))
	for _, node := range def.Nodes {
		out[node.ID] = node
	}
	return out
}

func workflowPickerItem(def serverapi.WorkflowDefinition, link sqlitegen.ProjectWorkflowLinkRecord, validation *workflow.ValidationResult) serverapi.WorkflowPickerItem {
	item := serverapi.WorkflowPickerItem{WorkflowID: def.Workflow.ID, DisplayName: def.Workflow.Name, Description: def.Workflow.Description, Version: def.Workflow.Version, IsProjectDefault: link.ID != "" && link.IsDefault != 0, ValidForTaskCreation: link.ID != ""}
	if validation != nil {
		item.ValidForTaskCreation = link.ID != "" && !validation.HasBlockingErrors()
		item.ValidationErrors = ValidationErrors(def.Workflow.ID, validation.Errors)
	}
	return item
}

func worktreeKentFacts(row sqlitegen.GetWorktreeByIDRow) serverapi.WorktreeKentFacts {
	facts := serverapi.WorktreeKentFacts{
		WorktreeID:    row.ID,
		DisplayName:   displayNameForPath(row.CanonicalRootPath),
		CanonicalRoot: row.CanonicalRootPath,
		Managed:       row.Managed != 0,
		CreatedBranch: row.CreatedBranch != 0,
	}
	if originSessionID := strings.TrimSpace(row.OriginSessionID); originSessionID != "" {
		facts.OriginSessionID = &originSessionID
	}
	return facts
}

func displayNameForPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	base := filepath.Base(filepath.Clean(trimmed))
	if base == "." || base == string(filepath.Separator) {
		return ""
	}
	return base
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
			dto := commentDTO(comment)
			item.Comment = &dto
		case "transition":
			transition, ok := transitions[row.sourceID]
			if !ok {
				return nil, errors.New("activity transition source is missing")
			}
			dto, err := transitionDTO(transition, edges[transition.ID])
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
			runView := runDTO(run, nodes, sessionNames)
			item.Run = &runView
			switch row.kind {
			case "run_started":
				item.Summary = "Run started"
			case "run_completed":
				item.Summary = "Run completed"
			case "run_interrupted":
				item.Summary = interruptedRunMessage(metadata.OptionalString(run.InterruptionReason), run.InterruptionDetailJson)
				attention := serverapi.WorkflowAttentionItem{ID: attentionKindInterruptedRun + ":" + run.ID, Kind: attentionKindInterruptedRun, ProjectID: task.ProjectID, WorkflowID: task.WorkflowID, TaskID: task.ID, TaskShortID: task.ShortID, TaskTitle: task.Title, RunID: run.ID, SessionID: run.SessionID.String, Message: item.Summary, DetailJSON: run.InterruptionDetailJson, OccurredAtUnixMs: run.InterruptedAtUnixMs.Int64}
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

func runDTO(run sqlitegen.TaskRunRecord, nodes map[string]serverapi.WorkflowNode, sessionNames map[string]string) serverapi.WorkflowRun {
	nodeID := nullableWorkflowViewNodeID(run.NodeID)
	dto := serverapi.WorkflowRun{ID: run.ID, TaskID: run.TaskID, PlacementID: run.PlacementID, NodeID: nodeID, SessionID: run.SessionID.String, Generation: run.RunGeneration, StartedAtUnixMs: metadata.OptionalInt64(run.StartedAtUnixMs), CompletedAtUnixMs: metadata.OptionalInt64(run.CompletedAtUnixMs), InterruptedAtUnixMs: metadata.OptionalInt64(run.InterruptedAtUnixMs), InterruptionReason: metadata.OptionalString(run.InterruptionReason), InterruptionDetail: run.InterruptionDetailJson, WaitingAskID: metadata.OptionalString(run.WaitingAskID), Status: runStatus(run)}
	if node, ok := nodes[nodeID]; ok {
		dto.NodeKind = node.Kind
		if node.ScriptPath != nil {
			dto.ScriptPath = strings.TrimSpace(*node.ScriptPath)
		}
		dto.Role = node.SubagentRole
	}
	if name, ok := sessionNames[strings.TrimSpace(run.SessionID.String)]; ok {
		dto.SessionName = name
	}
	return dto
}

func runStatus(run sqlitegen.TaskRunRecord) string {
	switch {
	case run.CompletedAtUnixMs.Valid:
		return "completed"
	case run.InterruptedAtUnixMs.Valid:
		return "interrupted"
	case run.WaitingAskID.Valid:
		return "waiting_question"
	case run.StartedAtUnixMs.Valid:
		return "running"
	default:
		return "pending"
	}
}

func transitionDTO(transition sqlitegen.TaskTransitionRecord, edges []sqlitegen.TaskTransitionEdgeRecord) (serverapi.WorkflowTaskTransition, error) {
	outputs := map[string]string{}
	if err := workflow.UnmarshalString(transition.OutputValuesJson, &outputs); err != nil {
		return serverapi.WorkflowTaskTransition{}, err
	}
	dto := serverapi.WorkflowTaskTransition{
		ID:                    transition.ID,
		TaskID:                transition.TaskID,
		SourceRunID:           strings.TrimSpace(transition.SourceRunID.String),
		SourcePlacementID:     strings.TrimSpace(transition.SourcePlacementID.String),
		SourceNodeID:          nullableWorkflowViewNodeID(transition.SourceNodeID),
		SourceNodeKey:         transition.SourceNodeKey,
		SourceNodeDisplayName: transition.SourceNodeDisplayName,
		TransitionGroupID:     strings.TrimSpace(transition.TransitionGroupID.String),
		TransitionID:          transition.TransitionID,
		TransitionDisplayName: transition.TransitionDisplayName,
		WorkflowRevisionSeen:  transition.WorkflowRevisionSeen,
		Actor:                 transition.Actor,
		State:                 transition.State,
		Commentary:            transition.Commentary,
		OutputValues:          outputs,
		CreatedAt:             transition.CreatedAtUnixMs,
		AppliedAtUnixMs:       metadata.OptionalInt64(transition.AppliedAtUnixMs),
	}
	for _, edge := range edges {
		inputs := []serverapi.WorkflowInputBinding{}
		if err := workflow.UnmarshalString(edge.InputBindingsJson, &inputs); err != nil {
			return serverapi.WorkflowTaskTransition{}, err
		}
		requirements := []serverapi.WorkflowOutputRequirement{}
		if err := workflow.UnmarshalString(edge.OutputRequirementsJson, &requirements); err != nil {
			return serverapi.WorkflowTaskTransition{}, err
		}
		dto.Edges = append(dto.Edges, serverapi.WorkflowTransitionEdge{
			ID:                    edge.ID,
			TaskTransitionID:      edge.TaskTransitionID,
			WorkflowEdgeID:        strings.TrimSpace(edge.WorkflowEdgeID.String),
			EdgeKey:               edge.EdgeKey,
			TargetNodeID:          strings.TrimSpace(edge.TargetNodeID.String),
			TargetNodeKey:         edge.TargetNodeKey,
			TargetNodeDisplayName: edge.TargetNodeDisplayName,
			TargetNodeKind:        edge.TargetNodeKind,
			TargetPlacementID:     strings.TrimSpace(edge.TargetPlacementID.String),
			State:                 edge.State,
			ContextMode:           edge.ContextMode,
			RequiresApproval:      edge.RequiresApproval != 0,
			InputBindings:         inputs,
			OutputRequirements:    requirements,
			WorkflowRevisionSeen:  edge.WorkflowRevisionSeen,
		})
	}
	return dto, nil
}

func (s *Service) sessionNamesByRun(ctx context.Context, runs []sqlitegen.TaskRunRecord) (map[string]string, error) {
	sessionIDs := []string{}
	seen := map[string]bool{}
	for _, run := range runs {
		sessionID := strings.TrimSpace(run.SessionID.String)
		if sessionID == "" || seen[sessionID] {
			continue
		}
		sessionIDs = append(sessionIDs, sessionID)
		seen[sessionID] = true
	}
	if len(sessionIDs) == 0 {
		return map[string]string{}, nil
	}
	rows, err := s.queries.ListSessionNamesByIDs(ctx, sessionIDs)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, row := range rows {
		out[row.ID] = row.Name
	}
	return out, nil
}

func (s *Service) transitionEdgesByTransitionID(ctx context.Context, transitions []sqlitegen.TaskTransitionRecord) (map[string][]sqlitegen.TaskTransitionEdgeRecord, error) {
	transitionIDs := make([]string, 0, len(transitions))
	for _, transition := range transitions {
		transitionIDs = append(transitionIDs, transition.ID)
	}
	out := map[string][]sqlitegen.TaskTransitionEdgeRecord{}
	if len(transitionIDs) == 0 {
		return out, nil
	}
	rows, err := s.queries.ListTaskTransitionEdgesByTransitionIDs(ctx, transitionIDs)
	if err != nil {
		return nil, err
	}
	for _, edge := range rows {
		out[edge.TaskTransitionID] = append(out[edge.TaskTransitionID], edge)
	}
	return out, nil
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

// parseAttentionPageToken decodes a page token and rejects it unless it was
// issued for expectedProjectID, so a token can't silently paginate a different
// project's attention list from the wrong cursor.
func parseAttentionPageToken(token string, expectedProjectID string) (attentionPageCursor, error) {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return attentionPageCursor{}, nil
	}
	parts := strings.SplitN(trimmed, "|", 3)
	if len(parts) != 3 {
		return attentionPageCursor{}, errors.New("page_token is invalid")
	}
	decodedProjectID, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return attentionPageCursor{}, errors.New("page_token is invalid")
	}
	if string(decodedProjectID) != strings.TrimSpace(expectedProjectID) {
		return attentionPageCursor{}, errors.New("page_token does not match project")
	}
	occurredAt, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || occurredAt < 0 {
		return attentionPageCursor{}, errors.New("page_token is invalid")
	}
	decodedID, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || strings.TrimSpace(string(decodedID)) == "" {
		return attentionPageCursor{}, errors.New("page_token is invalid")
	}
	return attentionPageCursor{occurredAtUnixMs: occurredAt, itemID: string(decodedID), hasValue: true}, nil
}

func attentionPageTokenFor(projectID string, occurredAtUnixMs int64, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strings.TrimSpace(projectID))) + "|" +
		strconv.FormatInt(occurredAtUnixMs, 10) + "|" +
		base64.RawURLEncoding.EncodeToString([]byte(id))
}

func (s *Service) approvalAttentionItems(ctx context.Context, projectID string, taskID string) ([]serverapi.WorkflowAttentionItem, error) {
	rows, err := s.queries.ListWorkflowApprovalAttentionItems(ctx, sqlitegen.ListWorkflowApprovalAttentionItemsParams{
		ProjectID: strings.TrimSpace(projectID),
		TaskID:    strings.TrimSpace(taskID),
	})
	if err != nil {
		return nil, err
	}
	items := make([]serverapi.WorkflowAttentionItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, serverapi.WorkflowAttentionItem{ID: "approval:" + row.TaskTransitionID, Kind: "approval", ProjectID: row.ProjectID, WorkflowID: row.WorkflowID, TaskID: row.TaskID, TaskShortID: row.ShortID, TaskTitle: row.Title, TaskTransitionID: row.TaskTransitionID, Message: "Approval required", OccurredAtUnixMs: row.CreatedAtUnixMs})
	}
	return items, nil
}

func (s *Service) questionAttentionItems(ctx context.Context, projectID string, taskID string) ([]serverapi.WorkflowAttentionItem, error) {
	rows, err := s.queries.ListWorkflowQuestionAttentionItems(ctx, sqlitegen.ListWorkflowQuestionAttentionItemsParams{
		ProjectID: strings.TrimSpace(projectID),
		TaskID:    strings.TrimSpace(taskID),
	})
	if err != nil {
		return nil, err
	}
	items := []serverapi.WorkflowAttentionItem{}
	questions := newPendingQuestionResolver(s.transcripts, s.prompts)
	for _, row := range rows {
		askID, err := requiredWaitingAskID(row.WaitingAskID, row.RunID)
		if err != nil {
			return nil, err
		}
		question, err := questions.Question(ctx, row.SessionID, askID)
		if err != nil {
			question = pendingQuestion{message: pendingQuestionFallbackMessage}
		}
		items = append(items, workflowQuestionAttentionItem("question:"+row.RunID+":"+askID, row.ProjectID, row.WorkflowID, row.TaskID, row.ShortID, row.Title, row.RunID, row.SessionID, askID, question, row.UpdatedAtUnixMs))
	}
	return items, nil
}

func requiredWaitingAskID(value sql.NullString, runID string) (string, error) {
	askID := strings.TrimSpace(value.String)
	if !value.Valid || askID == "" {
		return "", fmt.Errorf("workflow question attention projection returned invalid ask id for run %q", runID)
	}
	return askID, nil
}

func workflowQuestionAttentionItem(id string, projectID string, workflowID string, taskID string, shortID string, title string, runID string, sessionID string, askID string, question pendingQuestion, occurredAtUnixMs int64) serverapi.WorkflowAttentionItem {
	return serverapi.WorkflowAttentionItem{ID: id, Kind: "question", ProjectID: projectID, WorkflowID: workflowID, TaskID: taskID, TaskShortID: shortID, TaskTitle: title, RunID: runID, SessionID: sessionID, AskID: askID, Message: question.message, Suggestions: question.suggestions, RecommendedOptionIndex: question.recommendedOptionIndex, Question: question.prompt, OccurredAtUnixMs: occurredAtUnixMs}
}

const pendingQuestionFallbackMessage = "Question pending; open the task to answer."

type pendingQuestionResolver struct {
	transcripts SessionTranscriptTailEntryProvider
	prompts     PendingPromptSource
}

type pendingQuestion struct {
	message                string
	suggestions            []string
	recommendedOptionIndex int
	prompt                 *serverapi.WorkflowAttentionQuestionPrompt
}

func newPendingQuestionResolver(transcripts SessionTranscriptTailEntryProvider, prompts PendingPromptSource) *pendingQuestionResolver {
	return &pendingQuestionResolver{transcripts: transcripts, prompts: prompts}
}

func (r *pendingQuestionResolver) Question(ctx context.Context, sessionID string, askID string) (pendingQuestion, error) {
	sessionID = strings.TrimSpace(sessionID)
	askID = strings.TrimSpace(askID)
	if question, ok, err := r.questionFromPendingPrompt(sessionID, askID); ok || err != nil {
		return question, err
	}
	if r == nil || r.transcripts == nil {
		return pendingQuestion{}, errors.New("session transcript provider is required to resolve pending question")
	}
	if sessionID == "" || askID == "" {
		return pendingQuestion{}, errors.New("session_id and ask_id are required to resolve pending question")
	}
	entries, err := r.transcripts.SessionTranscriptTailEntries(ctx, sessionID)
	if err != nil {
		return pendingQuestion{}, fmt.Errorf("load session %q transcript tail for pending question %q: %w", sessionID, askID, err)
	}
	question := askQuestionFromTranscriptEntries(entries, askID)
	if strings.TrimSpace(question.message) == "" {
		return pendingQuestion{}, fmt.Errorf("pending question %q in session %q transcript: %w", askID, sessionID, ErrPendingQuestionNotFound)
	}
	return question, nil
}

func (r *pendingQuestionResolver) questionFromPendingPrompt(sessionID string, askID string) (pendingQuestion, bool, error) {
	if r == nil || r.prompts == nil || sessionID == "" || askID == "" {
		return pendingQuestion{}, false, nil
	}
	for _, snapshot := range r.prompts.ListPendingPrompts(sessionID) {
		req := snapshot.Request
		if strings.TrimSpace(req.ID) != askID {
			continue
		}
		return pendingQuestionFromRequest(req)
	}
	return pendingQuestion{}, false, nil
}

func pendingQuestionFromRequest(req askquestion.AskQuestionRequest) (pendingQuestion, bool, error) {
	if req.Approval {
		decisions := make([]clientui.ApprovalDecision, 0, len(req.ApprovalOptions))
		for _, option := range req.ApprovalOptions {
			decision := clientui.ApprovalDecision(option.Decision)
			switch decision {
			case clientui.ApprovalDecisionAllowOnce, clientui.ApprovalDecisionAllowSession, clientui.ApprovalDecisionDeny:
				decisions = append(decisions, decision)
			default:
				return pendingQuestion{}, true, fmt.Errorf("pending approval question %q has invalid decision %q", req.ID, option.Decision)
			}
		}
		if len(decisions) == 0 {
			return pendingQuestion{}, true, fmt.Errorf("pending approval question %q has no approval decisions", req.ID)
		}
		return pendingQuestion{
			message: strings.TrimSpace(req.Question),
			prompt: &serverapi.WorkflowAttentionQuestionPrompt{
				Kind:              serverapi.WorkflowAttentionQuestionKindApproval,
				ApprovalDecisions: decisions,
			},
		}, true, nil
	}
	suggestions := normalizedPendingQuestionSuggestions(req.Suggestions)
	return pendingQuestion{
		message:                strings.TrimSpace(req.Question),
		suggestions:            suggestions,
		recommendedOptionIndex: req.RecommendedOptionIndex,
		prompt: &serverapi.WorkflowAttentionQuestionPrompt{
			Kind:                   serverapi.WorkflowAttentionQuestionKindOrdinary,
			Suggestions:            suggestions,
			RecommendedOptionIndex: req.RecommendedOptionIndex,
		},
	}, true, nil
}

func normalizedPendingQuestionSuggestions(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	return append([]string(nil), in...)
}

func askQuestionFromTranscriptEntries(entries []runtime.ChatEntry, askID string) pendingQuestion {
	for _, entry := range entries {
		entryAskID := strings.TrimSpace(entry.ToolCallID)
		if strings.TrimSpace(entry.Role) != "tool_call" || entryAskID != askID || entry.ToolCall == nil {
			continue
		}
		if strings.TrimSpace(entry.ToolCall.ToolName) != string(toolspec.ToolAskQuestion) {
			continue
		}
		if question := strings.TrimSpace(entry.ToolCall.Question); question != "" {
			return pendingQuestion{
				message:                question,
				suggestions:            append([]string(nil), entry.ToolCall.Suggestions...),
				recommendedOptionIndex: entry.ToolCall.RecommendedOptionIndex,
				prompt: &serverapi.WorkflowAttentionQuestionPrompt{
					Kind:                   serverapi.WorkflowAttentionQuestionKindOrdinary,
					Suggestions:            append([]string(nil), entry.ToolCall.Suggestions...),
					RecommendedOptionIndex: entry.ToolCall.RecommendedOptionIndex,
				},
			}
		}
	}
	return pendingQuestion{}
}

func (s *Service) interruptedRunAttentionItems(ctx context.Context, projectID string, taskID string) ([]serverapi.WorkflowAttentionItem, error) {
	rows, err := s.queries.ListWorkflowInterruptedRunAttentionItems(ctx, sqlitegen.ListWorkflowInterruptedRunAttentionItemsParams{
		ProjectID: strings.TrimSpace(projectID),
		TaskID:    strings.TrimSpace(taskID),
	})
	if err != nil {
		return nil, err
	}
	items := make([]serverapi.WorkflowAttentionItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, serverapi.WorkflowAttentionItem{ID: attentionKindInterruptedRun + ":" + row.RunID, Kind: attentionKindInterruptedRun, ProjectID: row.ProjectID, WorkflowID: row.WorkflowID, TaskID: row.TaskID, TaskShortID: row.ShortID, TaskTitle: row.Title, RunID: row.RunID, SessionID: row.SessionID, Message: interruptedRunMessage(metadata.OptionalString(row.InterruptionReason), row.InterruptionDetailJson), DetailJSON: row.InterruptionDetailJson, OccurredAtUnixMs: row.InterruptedAtUnixMs.Int64})
	}
	return items, nil
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

func nullableAttentionInterruptionReason(value any) (*string, error) {
	if value == nil {
		return nil, nil
	}
	reason, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("attention interruption reason has unexpected type %T", value)
	}
	return &reason, nil
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

func (s *Service) validationAttentionItems(ctx context.Context, projectID string, roleResolver workflow.RoleResolver) ([]serverapi.WorkflowAttentionItem, error) {
	rows, err := s.queries.ListWorkflowValidationAttentionItems(ctx, strings.TrimSpace(projectID))
	if err != nil {
		return nil, err
	}
	type workflowLink struct {
		projectID  string
		workflowID string
		occurredAt int64
	}
	links := make([]workflowLink, 0, len(rows))
	for _, row := range rows {
		links = append(links, workflowLink{projectID: row.ProjectID, workflowID: row.WorkflowID, occurredAt: row.UpdatedAtUnixMs})
	}
	items := []serverapi.WorkflowAttentionItem{}
	for _, link := range links {
		def, _, err := s.definition(ctx, link.workflowID)
		if err != nil {
			return nil, err
		}
		validation := workflow.ValidateDefinition(definitionForValidation(def), workflow.ValidationOptions{Context: workflow.ValidationContextExecution, RoleResolver: roleResolver})
		if !validation.HasBlockingErrors() {
			continue
		}
		items = append(items, serverapi.WorkflowAttentionItem{ID: "validation_blocker:" + link.projectID + ":" + link.workflowID, Kind: "validation_blocker", ProjectID: link.projectID, WorkflowID: link.workflowID, Message: fmt.Sprintf("Workflow %q is invalid for task start", def.Workflow.Name), OccurredAtUnixMs: link.occurredAt})
	}
	return items, nil
}

func parseOffsetPageToken(token string) (int, error) {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return 0, nil
	}
	offset, err := strconv.Atoi(trimmed)
	if err != nil || offset < 0 {
		return 0, errors.New("page_token is invalid")
	}
	return offset, nil
}

type boardProjectWorkspaceFacts struct {
	primary   serverapi.ProjectWorkspaceSummary
	byID      map[string]serverapi.ProjectWorkspaceSummary
	count     int
	defaultID string
}

func projectBoardProject(project clientui.ProjectOverview, workspaceContext boardProjectWorkspaceFacts) serverapi.ProjectBoardProject {
	return serverapi.ProjectBoardProject{
		ProjectID:              project.Project.ProjectID,
		ProjectKey:             project.Project.ProjectKey,
		DisplayName:            project.Project.DisplayName,
		DefaultWorkspaceID:     workspaceContext.defaultID,
		AttachedWorkspaceCount: workspaceContext.count,
	}
}

func projectWorkspaceSummary(workspace clientui.ProjectWorkspaceSummary) serverapi.ProjectWorkspaceSummary {
	return serverapi.ProjectWorkspaceSummary{WorkspaceID: workspace.WorkspaceID, DisplayName: workspace.DisplayName, RootPath: workspace.RootPath, Availability: string(workspace.Availability), IsPrimary: workspace.IsPrimary, UpdatedAtUnixMs: workspace.UpdatedAt.UnixMilli()}
}

func boardProjectWorkspaceContext(project clientui.ProjectOverview) boardProjectWorkspaceFacts {
	context := boardProjectWorkspaceFacts{
		byID:  make(map[string]serverapi.ProjectWorkspaceSummary, len(project.Workspaces)),
		count: len(project.Workspaces),
	}
	for _, workspace := range project.Workspaces {
		dto := projectWorkspaceSummary(workspace)
		context.byID[dto.WorkspaceID] = dto
		if workspace.IsPrimary {
			context.primary = dto
			context.defaultID = dto.WorkspaceID
		}
	}
	return context
}

func sourceWorkspaceForTask(task sqlitegen.TaskRecord, workspacesByID map[string]serverapi.ProjectWorkspaceSummary, fallback serverapi.ProjectWorkspaceSummary) serverapi.ProjectWorkspaceSummary {
	if workspace, ok := workspacesByID[strings.TrimSpace(task.SourceWorkspaceID.String)]; ok {
		return workspace
	}
	snapshot := struct {
		SourceWorkspaceSnapshot struct {
			WorkspaceID string `json:"workspace_id"`
			DisplayName string `json:"display_name"`
			RootPath    string `json:"root_path"`
		} `json:"source_workspace_snapshot"`
	}{}
	if err := workflow.UnmarshalString(task.MetadataJson, &snapshot); err == nil {
		if strings.TrimSpace(snapshot.SourceWorkspaceSnapshot.RootPath) != "" {
			return serverapi.ProjectWorkspaceSummary{
				WorkspaceID:  strings.TrimSpace(snapshot.SourceWorkspaceSnapshot.WorkspaceID),
				DisplayName:  strings.TrimSpace(snapshot.SourceWorkspaceSnapshot.DisplayName),
				RootPath:     strings.TrimSpace(snapshot.SourceWorkspaceSnapshot.RootPath),
				Availability: string(clientui.ProjectAvailabilityUnlinked),
			}
		}
	}
	return fallback
}

func bodyPreview(body string) string {
	trimmed := strings.TrimSpace(body)
	const limit = 96
	if len(trimmed) <= limit {
		return trimmed
	}
	return trimmed[:limit]
}

func definitionForValidation(def serverapi.WorkflowDefinition) workflow.Definition {
	out := workflow.Definition{
		ID:                    workflow.WorkflowID(def.Workflow.ID),
		DisplayName:           def.Workflow.Name,
		ExecutionTargetPolicy: workflowExecutionTargetPolicyForValidation(def.Workflow.ExecutionTargetPolicy),
	}
	groupMemberIDs := map[string][]workflow.NodeID{}
	for _, group := range def.NodeGroups {
		out.NodeGroups = append(out.NodeGroups, workflow.NodeGroup{
			WorkflowID:  workflow.WorkflowID(group.WorkflowID),
			ID:          group.GroupID,
			Key:         workflow.ModelKey(group.GroupKey),
			DisplayName: group.DisplayName,
		})
	}
	for _, node := range def.Nodes {
		inputs := make([]workflow.InputField, 0, len(node.InputFields))
		for _, input := range node.InputFields {
			inputs = append(inputs, workflow.InputField{Name: input.Name, Description: input.Description})
		}
		joinProviders := make([]workflow.JoinInputProvider, 0, len(node.JoinInputProviders))
		for _, provider := range node.JoinInputProviders {
			joinProviders = append(joinProviders, workflow.JoinInputProvider{InputName: provider.InputName, ProviderEdgeID: workflow.EdgeID(provider.ProviderEdgeID)})
		}
		fields := make([]workflow.OutputField, 0, len(node.OutputFields))
		for _, field := range node.OutputFields {
			fields = append(fields, workflow.OutputField{Name: field.Name, Description: field.Description})
		}
		if strings.TrimSpace(node.GroupID) != "" {
			groupMemberIDs[node.GroupID] = append(groupMemberIDs[node.GroupID], workflow.NodeID(node.ID))
		}
		workflowNode, err := workflow.NewNode(
			workflow.NodeIdentity{
				WorkflowID:  workflow.WorkflowID(node.WorkflowID),
				ID:          workflow.NodeID(node.ID),
				Key:         workflow.ModelKey(node.Key),
				DisplayName: node.DisplayName,
				GroupID:     node.GroupID,
			},
			workflow.NodeKind(node.Kind),
			workflow.NodeFields{
				SubagentRole:   node.SubagentRole,
				PromptTemplate: node.PromptTemplate,
				CompletionMode: node.CompletionMode,
				ScriptPath: func() workflow.OptionalScriptPath {
					if node.ScriptPath == nil {
						return workflow.AbsentScriptPath()
					}
					if scriptPath, ok := workflow.PresentScriptPath(*node.ScriptPath); ok {
						return scriptPath
					}
					return workflow.AbsentScriptPath()
				}(),
				InputFields:        inputs,
				JoinInputProviders: joinProviders,
				OutputFields:       fields,
			},
		)
		if err != nil {
			panic(err)
		}
		out.Nodes = append(out.Nodes, workflowNode)
	}
	for index := range out.NodeGroups {
		out.NodeGroups[index].MemberNodeIDs = groupMemberIDs[out.NodeGroups[index].ID]
	}
	for _, group := range def.TransitionGroups {
		out.TransitionGroups = append(out.TransitionGroups, workflow.TransitionGroup{WorkflowID: workflow.WorkflowID(group.WorkflowID), ID: workflow.TransitionGroupID(group.ID), SourceNodeID: workflow.NodeID(group.SourceNodeID), TransitionID: workflow.TransitionID(group.TransitionID), DisplayName: group.DisplayName, Description: group.Description})
	}
	for _, edge := range def.Edges {
		parameters := make([]workflow.Parameter, 0, len(edge.Parameters))
		for _, parameter := range edge.Parameters {
			parameters = append(parameters, workflow.Parameter{Key: parameter.Key, Description: parameter.Description})
		}
		inputs := make([]workflow.InputBinding, 0, len(edge.InputBindings))
		for _, input := range edge.InputBindings {
			inputs = append(inputs, workflow.InputBinding{Name: input.Name, Source: workflow.BindingSource(input.Source), Field: input.Field})
		}
		requirements := make([]workflow.OutputRequirement, 0, len(edge.OutputRequirements))
		for _, requirement := range edge.OutputRequirements {
			requirements = append(requirements, workflow.OutputRequirement{FieldName: requirement.FieldName})
		}
		out.Edges = append(out.Edges, workflow.Edge{WorkflowID: workflow.WorkflowID(edge.WorkflowID), ID: workflow.EdgeID(edge.ID), Key: workflow.ModelKey(edge.Key), TransitionGroupID: workflow.TransitionGroupID(edge.TransitionGroupID), TargetNodeID: workflow.NodeID(edge.TargetNodeID), RequiresApproval: edge.RequiresApproval, ContextMode: workflow.ContextMode(edge.ContextMode), ContextSource: workflow.CanonicalContextSource(workflow.ContextSource{Kind: workflow.ContextSourceKind(edge.ContextSource.Kind), NodeKey: workflow.ModelKey(edge.ContextSource.NodeKey)}), PromptTemplate: edge.PromptTemplate, Parameters: parameters, InputBindings: inputs, OutputRequirements: requirements})
	}
	return out
}

func workflowExecutionTargetPolicyForValidation(policy serverapi.WorkflowExecutionTargetConfiguration) workflow.ExecutionTargetPolicy {
	var customRef *string
	if policy.CustomRef != nil {
		value := *policy.CustomRef
		customRef = &value
	}
	return workflow.ExecutionTargetPolicy{
		Mode:      workflow.ExecutionTargetMode(policy.Mode),
		CustomRef: customRef,
	}.Canonical()
}

func definitionExecutionValidation(def serverapi.WorkflowDefinition, roleResolver workflow.RoleResolver) *workflow.ValidationResult {
	domain := definitionForValidation(def)
	result := workflow.ValidateDefinition(domain, workflow.ValidationOptions{Context: workflow.ValidationContextExecution, RoleResolver: roleResolver})
	result.Errors = append(result.Errors, scriptPathDefinitionValidationErrors(domain, nil)...)
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

func selectWorkflow(picker []serverapi.WorkflowPickerItem, requested string) serverapi.WorkflowPickerItem {
	trimmed := strings.TrimSpace(requested)
	if trimmed != "" {
		for _, item := range picker {
			if item.WorkflowID == trimmed {
				return item
			}
		}
	}
	for _, item := range picker {
		if item.IsProjectDefault {
			return item
		}
	}
	if len(picker) == 0 {
		return serverapi.WorkflowPickerItem{}
	}
	return picker[0]
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

func boardColumns(def serverapi.WorkflowDefinition) []serverapi.WorkflowBoardColumn {
	columns := make([]serverapi.WorkflowBoardColumn, 0, len(def.Nodes))
	domainDef := definitionForValidation(def)
	derived := workflow.DeriveWiring(domainDef)
	for index, node := range boardColumnNodes(def) {
		columns = append(columns, serverapi.WorkflowBoardColumn{
			Node: serverapi.WorkflowBoardNodeSummary{
				NodeID:                 node.ID,
				Key:                    node.Key,
				Kind:                   node.Kind,
				DisplayName:            node.DisplayName,
				AssigneeRole:           node.SubagentRole,
				SortOrder:              index,
				OutputFields:           OutputFields(derived.PossibleProvisionFieldsForNode(workflow.NodeID(node.ID))),
				TransitionOutputFields: OutputFields(workflow.TransitionOutputFieldsForTargetNode(domainDef, derived, workflow.NodeID(node.ID))),
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

func (s *Service) taskCard(task sqlitegen.TaskRecord, statusFact workflowTaskStatusFact, placements []sqlitegen.TaskNodePlacementRecord, runs []sqlitegen.TaskRunRecord, def serverapi.WorkflowDefinition, nodeKinds map[string]workflow.NodeKind, sourceWorkspace serverapi.ProjectWorkspaceSummary) (serverapi.WorkflowBoardTaskCard, bool) {
	summary := taskSummary(task, statusFact.Status, statusFact.Done || hasActiveTerminalPlacement(placements, nodeKinds))
	actions := taskActions(task, summary, placements, runs, def, nodeKinds)
	return serverapi.WorkflowBoardTaskCard{TaskID: task.ID, ShortID: task.ShortID, Title: task.Title, Body: strings.TrimSpace(task.Body), WorkflowID: task.WorkflowID, ActiveNodeIDs: summary.ActiveNodeIDs, SourceWorkspace: sourceWorkspace, Status: statusFact.Status, Actions: actions, UpdatedAtUnixMs: task.UpdatedAtUnixMs}, summary.Done
}

func hasActiveTerminalPlacement(placements []sqlitegen.TaskNodePlacementRecord, nodeKinds map[string]workflow.NodeKind) bool {
	for _, placement := range placements {
		nodeID, ok := taskNodePlacementNodeID(placement)
		if ok && placement.State == "active" && nodeKinds[nodeID] == workflow.NodeKindTerminal {
			return true
		}
	}
	return false
}

func taskActions(task sqlitegen.TaskRecord, summary serverapi.WorkflowTaskSummary, placements []sqlitegen.TaskNodePlacementRecord, runs []sqlitegen.TaskRunRecord, def serverapi.WorkflowDefinition, nodeKinds map[string]workflow.NodeKind) serverapi.WorkflowTaskActions {
	actions := serverapi.WorkflowTaskActions{CanCancel: !task.CanceledAtUnixMs.Valid && !summary.Done}
	currentPlacementIDs := currentTaskPlacementIDs(placements)
	runningRunIDs := []string{}
	interruptedRunIDs := []string{}
	waitingAskRunIDs := []string{}
	waitingApproval := false
	backlog := false
	for _, placement := range placements {
		if placement.State == "waiting_approval" {
			waitingApproval = true
		}
		nodeID, ok := taskNodePlacementNodeID(placement)
		if ok && placement.State == "active" && nodeKinds[nodeID] == workflow.NodeKindStart {
			backlog = true
		}
	}
	for _, run := range runs {
		if !currentPlacementIDs[run.PlacementID] {
			continue
		}
		if run.CompletedAtUnixMs.Valid {
			continue
		}
		if run.WaitingAskID.Valid {
			waitingAskRunIDs = append(waitingAskRunIDs, run.ID)
		}
		if run.InterruptedAtUnixMs.Valid {
			interruptedRunIDs = append(interruptedRunIDs, run.ID)
			continue
		}
		if run.StartedAtUnixMs.Valid {
			runningRunIDs = append(runningRunIDs, run.ID)
		}
	}
	actions.CanStart = !task.CanceledAtUnixMs.Valid && backlog && !waitingApproval && len(runningRunIDs) == 0 && len(waitingAskRunIDs) == 0
	taskActive := !task.CanceledAtUnixMs.Valid
	if taskActive && len(runningRunIDs) == 0 {
		actions.ManualMoveTargetNodeIDs = manualMoveTargetNodeIDs(def, placements, nodeKinds)
	}
	actions.CanInterrupt = taskActive && len(runningRunIDs) >= 1
	actions.CanResume = taskActive && len(interruptedRunIDs) >= 1
	return actions
}

func currentTaskPlacementIDs(placements []sqlitegen.TaskNodePlacementRecord) map[string]bool {
	ids := make(map[string]bool, len(placements))
	for _, placement := range placements {
		if placement.State != "active" && placement.State != "waiting_approval" {
			continue
		}
		ids[placement.ID] = true
	}
	return ids
}

func manualMoveTargetNodeIDs(def serverapi.WorkflowDefinition, placements []sqlitegen.TaskNodePlacementRecord, nodeKinds map[string]workflow.NodeKind) []string {
	sourceNodeID := ""
	for _, placement := range placements {
		nodeID, ok := taskNodePlacementNodeID(placement)
		if !ok {
			continue
		}
		nodeKind := nodeKinds[nodeID]
		isCurrentSource := placement.State == "active" || placement.State == "waiting_approval"
		if !isCurrentSource {
			continue
		}
		if sourceNodeID != "" {
			return []string{}
		}
		if nodeKind == workflow.NodeKindTerminal && placement.State != "active" {
			return []string{}
		}
		sourceNodeID = nodeID
	}
	if sourceNodeID == "" {
		return []string{}
	}
	groupIDs := map[string]bool{}
	for _, group := range def.TransitionGroups {
		if group.SourceNodeID == sourceNodeID {
			groupIDs[group.ID] = true
		}
	}
	derivedEdges := workflowDerivedEdgeWiringByID(def.DerivedWiring)
	targets := []string{}
	seen := map[string]bool{}
	for _, node := range def.Nodes {
		if workflow.NodeKind(node.Kind) == workflow.NodeKindTerminal {
			if node.ID == sourceNodeID {
				continue
			}
			seen[node.ID] = true
			targets = append(targets, node.ID)
		}
	}
	for _, edge := range def.Edges {
		contextSource := workflow.CanonicalContextSource(workflow.ContextSource{Kind: workflow.ContextSourceKind(edge.ContextSource.Kind), NodeKey: workflow.ModelKey(edge.ContextSource.NodeKey)})
		if !groupIDs[edge.TransitionGroupID] || edge.RequiresApproval || len(derivedEdges[edge.ID].RequiredProvisionFields) > 0 || contextSource.Kind == workflow.ContextSourceSelectedNode || contextSource.Kind == workflow.ContextSourcePreviousTarget {
			continue
		}
		if !seen[edge.TargetNodeID] {
			seen[edge.TargetNodeID] = true
			targets = append(targets, edge.TargetNodeID)
		}
	}
	return targets
}

func workflowDerivedEdgeWiringByID(derived serverapi.WorkflowDerivedWiring) map[string]serverapi.WorkflowDerivedEdgeWiring {
	byID := make(map[string]serverapi.WorkflowDerivedEdgeWiring, len(derived.Edges))
	for _, edge := range derived.Edges {
		byID[edge.EdgeID] = edge
	}
	return byID
}

func (s *Service) applyBoardColumnTaskCounts(ctx context.Context, columns []serverapi.WorkflowBoardColumn, projectID string, workflowID string, canceledTerminalNodeID string) error {
	rows, err := s.queries.ListBoardColumnTaskCounts(ctx, sqlitegen.ListBoardColumnTaskCountsParams{
		ProjectID:              projectID,
		WorkflowID:             workflowID,
		CanceledTerminalNodeID: canceledTerminalNodeID,
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

func effectiveBoardPlacements(placements []sqlitegen.TaskNodePlacementRecord, nodeKinds map[string]workflow.NodeKind) []sqlitegen.TaskNodePlacementRecord {
	effective := make([]sqlitegen.TaskNodePlacementRecord, 0, len(placements))
	for _, placement := range placements {
		nodeID, ok := taskNodePlacementNodeID(placement)
		if !ok {
			continue
		}
		nodeKind := nodeKinds[nodeID]
		if nodeKind == workflow.NodeKindTerminal {
			if placement.State == "active" {
				effective = append(effective, placement)
			}
			continue
		}
		if placement.State == "active" || placement.State == "waiting_approval" {
			effective = append(effective, placement)
		}
	}
	return effective
}

func effectiveBoardPlacementsForTask(task sqlitegen.TaskRecord, placements []sqlitegen.TaskNodePlacementRecord, def serverapi.WorkflowDefinition, nodeKinds map[string]workflow.NodeKind) []sqlitegen.TaskNodePlacementRecord {
	active := effectiveBoardPlacements(placements, nodeKinds)
	if !task.CanceledAtUnixMs.Valid {
		return active
	}
	terminalNodeID := canceledBoardTerminalNodeID(def)
	if terminalNodeID == "" {
		return active
	}
	terminalPlacements := make([]sqlitegen.TaskNodePlacementRecord, 0, len(active))
	for _, placement := range active {
		nodeID, ok := taskNodePlacementNodeID(placement)
		if ok && nodeKinds[nodeID] == workflow.NodeKindTerminal {
			terminalPlacements = append(terminalPlacements, placement)
		}
	}
	if len(terminalPlacements) > 0 {
		return terminalPlacements
	}
	return []sqlitegen.TaskNodePlacementRecord{{
		ID:              "",
		TaskID:          task.ID,
		NodeID:          sql.NullString{String: terminalNodeID, Valid: true},
		State:           "active",
		CreatedAtUnixMs: task.UpdatedAtUnixMs,
		UpdatedAtUnixMs: task.UpdatedAtUnixMs,
	}}
}

func canceledBoardTerminalNodeID(def serverapi.WorkflowDefinition) string {
	fallback := ""
	for _, node := range def.Nodes {
		if workflow.NodeKind(node.Kind) != workflow.NodeKindTerminal {
			continue
		}
		if fallback == "" {
			fallback = node.ID
		}
		if node.Key == "done" {
			return node.ID
		}
	}
	return fallback
}

type boardNodeCardsPageCursor struct {
	projectID       string
	workflowID      string
	nodeID          string
	updatedAtUnixMs int64
	taskID          string
	hasValue        bool
}

type boardNodeCardsPageTokenPayload struct {
	Version         int    `json:"version"`
	ProjectID       string `json:"project_id"`
	WorkflowID      string `json:"workflow_id"`
	NodeID          string `json:"node_id"`
	UpdatedAtUnixMs int64  `json:"updated_at_unix_ms"`
	TaskID          string `json:"task_id"`
}

type workflowBoardPageCursor struct {
	projectID       string
	workflowID      string
	updatedAtUnixMs int64
	taskID          string
	hasValue        bool
}

type workflowBoardPageTokenPayload struct {
	Version         int    `json:"version"`
	ProjectID       string `json:"project_id"`
	WorkflowID      string `json:"workflow_id"`
	UpdatedAtUnixMs int64  `json:"updated_at_unix_ms"`
	TaskID          string `json:"task_id"`
}

func workflowBoardPageTokenWorkflowID(token string, projectID string) (string, error) {
	payload, err := decodeWorkflowBoardPageToken(token)
	if err != nil {
		return "", err
	}
	if payload.ProjectID != projectID {
		return "", errors.New("page_token is invalid")
	}
	return payload.WorkflowID, nil
}

func parseWorkflowBoardPageToken(token string, projectID string, workflowID string) (workflowBoardPageCursor, error) {
	if strings.TrimSpace(token) == "" {
		return workflowBoardPageCursor{}, nil
	}
	payload, err := decodeWorkflowBoardPageToken(token)
	if err != nil {
		return workflowBoardPageCursor{}, errors.New("page_token is invalid")
	}
	if payload.Version != 1 || payload.ProjectID != projectID || payload.WorkflowID != workflowID || strings.TrimSpace(payload.TaskID) == "" || payload.UpdatedAtUnixMs < 0 {
		return workflowBoardPageCursor{}, errors.New("page_token is invalid")
	}
	return workflowBoardPageCursor{projectID: payload.ProjectID, workflowID: payload.WorkflowID, updatedAtUnixMs: payload.UpdatedAtUnixMs, taskID: payload.TaskID, hasValue: true}, nil
}

func decodeWorkflowBoardPageToken(token string) (workflowBoardPageTokenPayload, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return workflowBoardPageTokenPayload{}, errors.New("page_token is invalid")
	}
	var payload workflowBoardPageTokenPayload
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return workflowBoardPageTokenPayload{}, errors.New("page_token is invalid")
	}
	if payload.Version != 1 || strings.TrimSpace(payload.WorkflowID) == "" || strings.TrimSpace(payload.TaskID) == "" || payload.UpdatedAtUnixMs < 0 {
		return workflowBoardPageTokenPayload{}, errors.New("page_token is invalid")
	}
	return payload, nil
}

func workflowBoardPageToken(projectID string, workflowID string, task sqlitegen.TaskRecord) string {
	payload := workflowBoardPageTokenPayload{Version: 1, ProjectID: projectID, WorkflowID: workflowID, UpdatedAtUnixMs: task.UpdatedAtUnixMs, TaskID: task.ID}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func parseBoardNodeCardsPageToken(token string, projectID string, workflowID string, nodeID string) (boardNodeCardsPageCursor, error) {
	if strings.TrimSpace(token) == "" {
		return boardNodeCardsPageCursor{}, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return boardNodeCardsPageCursor{}, ErrInvalidPageToken
	}
	var payload boardNodeCardsPageTokenPayload
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return boardNodeCardsPageCursor{}, ErrInvalidPageToken
	}
	if payload.Version != 1 || payload.ProjectID != projectID || payload.WorkflowID != workflowID || payload.NodeID != nodeID || strings.TrimSpace(payload.TaskID) == "" || payload.UpdatedAtUnixMs < 0 {
		return boardNodeCardsPageCursor{}, ErrInvalidPageToken
	}
	return boardNodeCardsPageCursor{projectID: payload.ProjectID, workflowID: payload.WorkflowID, nodeID: payload.NodeID, updatedAtUnixMs: payload.UpdatedAtUnixMs, taskID: payload.TaskID, hasValue: true}, nil
}

func boardNodeCardsPageToken(projectID string, workflowID string, nodeID string, task sqlitegen.TaskRecord) string {
	payload := boardNodeCardsPageTokenPayload{Version: 1, ProjectID: projectID, WorkflowID: workflowID, NodeID: nodeID, UpdatedAtUnixMs: task.UpdatedAtUnixMs, TaskID: task.ID}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func apiContextSource(in workflow.ContextSource) serverapi.WorkflowContextSource {
	source := workflow.CanonicalContextSource(in)
	return serverapi.WorkflowContextSource{Kind: string(source.Kind), NodeKey: string(source.NodeKey)}
}
