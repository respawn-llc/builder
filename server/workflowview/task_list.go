package workflowview

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/shared/serverapi"
)

type TaskList struct {
	metadata    *metadata.Store
	queries     *sqlitegen.Queries
	definitions *DefinitionProjection
	projector   *TaskProjector
}

func NewTaskList(metadataStore *metadata.Store, definitions *DefinitionProjection, projector *TaskProjector) (*TaskList, error) {
	if metadataStore == nil || metadataStore.Queries() == nil {
		return nil, errors.New("metadata store is required")
	}
	if definitions == nil {
		return nil, errors.New("definition projection is required")
	}
	if projector == nil {
		return nil, errors.New("task projector is required")
	}
	return &TaskList{
		metadata:    metadataStore,
		queries:     metadataStore.Queries(),
		definitions: definitions,
		projector:   projector,
	}, nil
}

func (l *TaskList) List(ctx context.Context, req serverapi.WorkflowTaskListRequest) (serverapi.WorkflowTaskListResponse, error) {
	if l == nil {
		return serverapi.WorkflowTaskListResponse{}, errors.New("task list is required")
	}
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskListResponse{}, err
	}
	pageToken, hasPageToken, err := parseWorkflowTaskListPageToken(req.PageToken)
	if err != nil {
		return serverapi.WorkflowTaskListResponse{}, err
	}
	var tokenScope *workflowTaskListPageTokenPayload
	if hasPageToken {
		tokenScope = &pageToken
	}
	projectID, workflowID, err := l.resolveScope(ctx, req.ProjectID, req.WorkflowID, tokenScope)
	if err != nil {
		return serverapi.WorkflowTaskListResponse{}, err
	}
	pageSize := req.PageSize
	if pageSize == 0 {
		pageSize = 100
	}
	if _, err := l.metadata.GetProjectOverview(ctx, projectID); err != nil {
		return serverapi.WorkflowTaskListResponse{}, err
	}
	var definition serverapi.WorkflowDefinition
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
		snapshot, snapshotErr := l.definitions.snapshot(ctx, *workflowID)
		if snapshotErr != nil {
			return serverapi.WorkflowTaskListResponse{}, snapshotErr
		}
		definition = snapshot.api
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
		value, hashErr := workflowTaskListColumnStructureHash(definition, columns)
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
				narrowed.WorkflowVersion != definition.Workflow.Version ||
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
			canceledTerminalNodeID: canceledBoardTerminalNodeID(definition),
			columns:                columns,
			columnKeys:             req.ColumnKeys,
		}
	}
	rows, err := l.queryRows(ctx, workflowTaskListQueryRequest{
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
	for _, row := range pageItems {
		item := row.item
		if matchingWorkflowCardinality != serverapi.WorkflowTaskListMatchingWorkflowCardinalityMultiple {
			item.WorkflowName = nil
		}
		responseItems = append(responseItems, item)
	}
	var nextPageToken *string
	if hasNext && len(pageItems) > 0 {
		tokenScope := workflowTaskListPageTokenScope{ProjectID: projectID}
		if workflowID == nil {
			tokenScope.ProjectWide = &workflowTaskListProjectWidePageTokenInvariants{}
		} else {
			tokenScope.Narrowed = &workflowTaskListNarrowedPageTokenInvariants{
				WorkflowID:          *workflowID,
				WorkflowVersion:     definition.Workflow.Version,
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

func (l *TaskList) resolveScope(ctx context.Context, projectIDValue *string, workflowIDValue *string, token *workflowTaskListPageTokenPayload) (string, *string, error) {
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
		if _, err := l.queries.GetActiveProjectWorkflowLinkByWorkflow(ctx, sqlitegen.GetActiveProjectWorkflowLinkByWorkflowParams{
			ProjectID:  *projectIDValue,
			WorkflowID: *workflowIDValue,
		}); err == nil {
			workflowID := *workflowIDValue
			return *projectIDValue, &workflowID, nil
		} else if errors.Is(err, sql.ErrNoRows) {
			errorProjectID := *projectIDValue
			errorWorkflowID := *workflowIDValue
			return "", nil, &serverapi.WorkflowTaskListScopeError{
				Reason:     serverapi.WorkflowTaskListScopeReasonWorkflowNotLinked,
				ProjectID:  &errorProjectID,
				WorkflowID: &errorWorkflowID,
			}
		} else {
			return "", nil, err
		}
	}
	if projectIDValue != nil {
		linkCount, err := l.queries.CountActiveProjectWorkflowLinks(ctx, *projectIDValue)
		if err != nil {
			return "", nil, err
		}
		if linkCount == 0 {
			errorProjectID := *projectIDValue
			return "", nil, &serverapi.WorkflowTaskListScopeError{
				Reason:    serverapi.WorkflowTaskListScopeReasonNoLinkedWorkflows,
				ProjectID: &errorProjectID,
			}
		}
		return *projectIDValue, nil, nil
	}
	return "", nil, &serverapi.WorkflowTaskListScopeError{
		Reason: serverapi.WorkflowTaskListScopeReasonNoLinkedWorkflows,
	}
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
