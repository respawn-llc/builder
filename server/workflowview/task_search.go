package workflowview

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/shared/serverapi"
	"core/shared/tasksearchtext"

	sqlitedriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const taskSearchStableCaptureAttempts = 3

type TaskSearch struct {
	metadata  *metadata.Store
	queries   *sqlitegen.Queries
	projector *TaskProjector
	authority *sessionruntime.Authority
	permit    *workflowexecution.MutationPermit
}

type taskSearchLiveSnapshot struct {
	executions map[workflow.TaskID]sessionruntime.TaskExecutionSnapshot
	revision   uint64
}

type taskSearchReadSnapshot struct {
	queries *sqlitegen.Queries
	live    taskSearchLiveSnapshot
	anchor  func(context.Context) error
	close   func() error
}

func NewTaskSearch(
	metadataStore *metadata.Store,
	projector *TaskProjector,
	authority *sessionruntime.Authority,
	permit *workflowexecution.MutationPermit,
) (*TaskSearch, error) {
	switch {
	case metadataStore == nil || metadataStore.Queries() == nil:
		return nil, errors.New("metadata store is required")
	case projector == nil:
		return nil, errors.New("task projector is required")
	case authority == nil:
		return nil, errors.New("session runtime authority is required")
	case permit == nil:
		return nil, errors.New("workflow mutation permit is required")
	}
	return &TaskSearch{
		metadata:  metadataStore,
		queries:   metadataStore.Queries(),
		projector: projector,
		authority: authority,
		permit:    permit,
	}, nil
}

func (s *TaskSearch) Search(ctx context.Context, req serverapi.TaskSearchRequest) (response serverapi.TaskSearchResponse, err error) {
	if s == nil || s.metadata == nil || s.metadata.DB() == nil || s.queries == nil || s.projector == nil || s.authority == nil || s.permit == nil {
		return serverapi.TaskSearchResponse{}, errors.New("task search is required")
	}
	if err := req.Validate(); err != nil {
		return serverapi.TaskSearchResponse{}, err
	}
	if err := context.Cause(ctx); err != nil {
		return serverapi.TaskSearchResponse{}, err
	}
	offset := 0
	if req.Offset != nil {
		offset = *req.Offset
	}
	snapshot, err := s.captureReadSnapshot(ctx, req)
	if err != nil {
		if req.Mode == serverapi.TaskSearchModeFTS5 {
			return serverapi.TaskSearchResponse{}, taskSearchFTS5OperationalError(err)
		}
		return serverapi.TaskSearchResponse{}, err
	}
	defer func() {
		if closeErr := snapshot.Close(); closeErr != nil {
			response = serverapi.TaskSearchResponse{}
			err = errors.Join(err, fmt.Errorf("close task search read snapshot: %w", closeErr))
		}
	}()
	if req.Mode == serverapi.TaskSearchModeFTS5 {
		if _, validationErr := snapshot.queries.ValidateTaskSearchFTS5Expression(ctx, sql.NullString{String: req.Query, Valid: true}); validationErr != nil {
			return serverapi.TaskSearchResponse{}, taskSearchFTS5OperationalError(validationErr)
		}
	}
	rows, err := s.queryPage(ctx, snapshot.queries, snapshot.live.executions, req, offset)
	if err != nil {
		if req.Mode == serverapi.TaskSearchModeFTS5 {
			return serverapi.TaskSearchResponse{}, taskSearchFTS5OperationalError(err)
		}
		return serverapi.TaskSearchResponse{}, err
	}
	hasNext := len(rows) > req.PageSize
	if hasNext {
		rows = rows[:req.PageSize]
	}
	groups, err := s.materializeGroups(ctx, snapshot.queries, req, rows)
	if err != nil {
		return serverapi.TaskSearchResponse{}, err
	}
	var next *int
	if hasNext && len(rows) > 0 {
		value := offset + len(rows)
		next = &value
	}
	response = serverapi.TaskSearchResponse{Mode: req.Mode, Groups: groups, NextOffset: next}
	if err := response.Validate(); err != nil {
		return serverapi.TaskSearchResponse{}, fmt.Errorf("validate task search response: %w", err)
	}
	return response, nil
}

func (s *TaskSearch) captureReadSnapshot(ctx context.Context, req serverapi.TaskSearchRequest) (*taskSearchReadSnapshot, error) {
	var snapshot *taskSearchReadSnapshot
	err := s.permit.Run(ctx, func(ctx context.Context) error {
		captured, err := s.captureStableReadSnapshot(ctx)
		if err != nil {
			return err
		}
		if err := s.validateSchemaAndScope(ctx, captured.queries, req); err != nil {
			return closeFailedTaskSearchReadSnapshot(captured, err)
		}
		snapshot = captured
		return nil
	})
	if err != nil {
		return nil, err
	}
	if snapshot == nil {
		return nil, errors.New("task search read snapshot capture ended without a result")
	}
	return snapshot, nil
}

func (s *TaskSearch) captureStableReadSnapshot(ctx context.Context) (*taskSearchReadSnapshot, error) {
	return taskSearchStableReadCapture(
		ctx,
		func() (taskSearchLiveSnapshot, error) {
			executions, revision, err := s.authority.CurrentWorkflowTaskExecutionSnapshotsWithRevision()
			if err != nil {
				return taskSearchLiveSnapshot{}, err
			}
			return taskSearchLiveSnapshot{executions: executions, revision: revision}, nil
		},
		func(ctx context.Context) (*taskSearchReadSnapshot, error) {
			tx, err := s.metadata.DB().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
			if err != nil {
				return nil, fmt.Errorf("begin task search read transaction: %w", err)
			}
			queries := s.queries.WithTx(tx)
			snapshot := &taskSearchReadSnapshot{
				queries: queries,
				close: func() error {
					rollbackErr := tx.Rollback()
					if errors.Is(rollbackErr, sql.ErrTxDone) {
						return nil
					}
					return rollbackErr
				},
			}
			snapshot.anchor = func(ctx context.Context) error {
				_, err := queries.AnchorTaskSearchReadSnapshot(ctx)
				return err
			}
			return snapshot, nil
		},
	)
}

func taskSearchStableReadCapture(
	ctx context.Context,
	captureLive func() (taskSearchLiveSnapshot, error),
	openRead func(context.Context) (*taskSearchReadSnapshot, error),
) (*taskSearchReadSnapshot, error) {
	for attempt := 0; attempt < taskSearchStableCaptureAttempts; attempt++ {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		snapshot, err := openRead(ctx)
		if err != nil {
			return nil, err
		}
		if snapshot == nil {
			return nil, errors.New("task search read snapshot capture returned no snapshot")
		}
		before, err := captureLive()
		if err != nil {
			return nil, closeFailedTaskSearchReadSnapshot(snapshot, err)
		}
		if snapshot.anchor == nil {
			return nil, closeFailedTaskSearchReadSnapshot(snapshot, errors.New("task search durable snapshot anchor is required"))
		}
		if err := snapshot.anchor(ctx); err != nil {
			return nil, closeFailedTaskSearchReadSnapshot(snapshot, err)
		}
		after, err := captureLive()
		if err != nil {
			return nil, closeFailedTaskSearchReadSnapshot(snapshot, err)
		}
		if before.revision == after.revision {
			snapshot.live = before
			return snapshot, nil
		}
		if err := snapshot.Close(); err != nil {
			return nil, fmt.Errorf("discard unstable task search read snapshot: %w", err)
		}
	}
	return nil, errors.New("task search live activity did not stabilize during capture")
}

func closeFailedTaskSearchReadSnapshot(snapshot *taskSearchReadSnapshot, cause error) error {
	if closeErr := snapshot.Close(); closeErr != nil {
		return errors.Join(cause, fmt.Errorf("close task search read snapshot: %w", closeErr))
	}
	return cause
}

func (s *taskSearchReadSnapshot) Close() error {
	if s == nil || s.close == nil {
		return nil
	}
	close := s.close
	s.close = nil
	s.queries = nil
	s.live = taskSearchLiveSnapshot{}
	s.anchor = nil
	return close()
}

func (s *TaskSearch) validateSchemaAndScope(ctx context.Context, queries *sqlitegen.Queries, req serverapi.TaskSearchRequest) error {
	schemaFailures, err := queries.ListTaskSearchSchemaContractFailures(ctx)
	if err != nil {
		return err
	}
	if len(schemaFailures) != 0 {
		return errors.New("task search schema is incomplete")
	}
	projectIDsJSON, err := json.Marshal(taskSearchProjectIDs(req))
	if err != nil {
		return fmt.Errorf("encode task search project ids: %w", err)
	}
	unknown, err := queries.ListUnknownTaskSearchProjectIDs(ctx, string(projectIDsJSON))
	if err != nil {
		return err
	}
	if len(unknown) != 0 {
		return fmt.Errorf("task search project scope contains unknown project ids: %s", strings.Join(unknown, ","))
	}
	return nil
}

func (s *TaskSearch) queryPage(ctx context.Context, queries *sqlitegen.Queries, liveSnapshots map[workflow.TaskID]sessionruntime.TaskExecutionSnapshot, req serverapi.TaskSearchRequest, offset int) ([]sqlitegen.ListTaskSearchPageDescriptorsRow, error) {
	liveTaskStatesJSON, err := workflowTaskListLiveStatesJSON(liveSnapshots)
	if err != nil {
		return nil, err
	}
	projectIDsJSON, err := taskSearchOptionalJSON(taskSearchProjectIDs(req))
	if err != nil {
		return nil, fmt.Errorf("encode task search project ids: %w", err)
	}
	statusKindsJSON, err := taskSearchOptionalJSON(taskSearchStatusKinds(req))
	if err != nil {
		return nil, fmt.Errorf("encode task search status kinds: %w", err)
	}
	candidateExpression := req.Query
	caseMode := int64(tasksearchtext.LiteralCaseInsensitive)
	if req.Mode == serverapi.TaskSearchModeLiteral {
		mode := tasksearchtext.LiteralCaseInsensitive
		if req.CaseSensitive {
			mode = tasksearchtext.LiteralCaseSensitive
		}
		matcher, matcherErr := tasksearchtext.NewLiteralMatcher(req.Query, mode)
		if matcherErr != nil {
			return nil, matcherErr
		}
		candidateExpression = matcher.CandidateExpression()
		caseMode = int64(mode)
	}
	return queries.ListTaskSearchPageDescriptors(ctx, sqlitegen.ListTaskSearchPageDescriptorsParams{
		Mode:                string(req.Mode),
		CandidateExpression: candidateExpression,
		LiteralQuery:        req.Query,
		CaseMode:            caseMode,
		IncludeComments:     boolInt64(req.IncludeComments),
		ProjectIdsJson:      projectIDsJSON,
		StatusKindsJson:     statusKindsJSON,
		ContextClusters:     int64(req.Context),
		OffsetRows:          int64(offset),
		LimitRows:           int64(req.PageSize + 1),
		LiveTaskStatesJson:  liveTaskStatesJSON,
	})
}

func taskSearchOptionalJSON[T any](values []T) (sql.NullString, error) {
	if len(values) == 0 {
		return sql.NullString{}, nil
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return sql.NullString{}, err
	}
	return sql.NullString{String: string(encoded), Valid: true}, nil
}

func (s *TaskSearch) materializeGroups(ctx context.Context, queries *sqlitegen.Queries, req serverapi.TaskSearchRequest, rows []sqlitegen.ListTaskSearchPageDescriptorsRow) ([]serverapi.TaskSearchGroup, error) {
	var matcher tasksearchtext.LiteralMatcher
	if req.Mode == serverapi.TaskSearchModeLiteral {
		mode := tasksearchtext.LiteralCaseInsensitive
		if req.CaseSensitive {
			mode = tasksearchtext.LiteralCaseSensitive
		}
		var err error
		matcher, err = tasksearchtext.NewLiteralMatcher(req.Query, mode)
		if err != nil {
			return nil, err
		}
	}
	groups := make([]serverapi.TaskSearchGroup, 0, len(rows))
	groupIndexes := make(map[string]int, len(rows))
	for _, row := range rows {
		statusKind, err := taskSearchSQLiteString(row.StatusKind, "status kind")
		if err != nil {
			return nil, err
		}
		nodeIDsJSON, err := taskSearchSQLiteString(row.NodeIdsJson, "node ids")
		if err != nil {
			return nil, err
		}
		attentionTypesJSON, err := taskSearchSQLiteString(row.AttentionTypesJson, "attention types")
		if err != nil {
			return nil, err
		}
		status, err := s.projector.DecodeStatus(TaskStatusInput{
			TaskID:             row.TaskID,
			Kind:               statusKind,
			NodeIDsJSON:        nodeIDsJSON,
			AttentionTypesJSON: attentionTypesJSON,
			Done:               row.IsDone != 0,
		})
		if err != nil {
			return nil, err
		}
		index, exists := groupIndexes[row.TaskID]
		if !exists {
			index = len(groups)
			groupIndexes[row.TaskID] = index
			groups = append(groups, serverapi.TaskSearchGroup{
				ProjectID:     row.ProjectID,
				ProjectKey:    row.ProjectKey,
				TaskID:        row.TaskID,
				ShortID:       row.ShortID,
				WorkflowID:    row.WorkflowID,
				Title:         row.TaskTitle,
				Status:        status.Status,
				TotalHitCount: int(row.TotalHitCount),
				Hits:          []serverapi.TaskSearchHit{},
			})
		}
		hit, err := taskSearchMaterializeHit(ctx, queries, req, matcher, row)
		if err != nil {
			return nil, err
		}
		groups[index].Hits = append(groups[index].Hits, hit)
	}
	return groups, nil
}

func taskSearchMaterializeHit(ctx context.Context, queries *sqlitegen.Queries, req serverapi.TaskSearchRequest, matcher tasksearchtext.LiteralMatcher, row sqlitegen.ListTaskSearchPageDescriptorsRow) (serverapi.TaskSearchHit, error) {
	apiSource := serverapi.TaskSearchSource{Kind: serverapi.TaskSearchSourceKind(row.SourceKind)}
	if row.CommentID.Valid {
		value := row.CommentID.String
		apiSource.CommentID = &value
	}
	hit := serverapi.TaskSearchHit{Ordinal: int(row.Ordinal), Source: apiSource}
	switch req.Mode {
	case serverapi.TaskSearchModeLiteral:
		source, err := queries.GetTaskSearchSourceByDocumentID(ctx, row.DocumentID)
		if err != nil {
			return serverapi.TaskSearchHit{}, err
		}
		if source.SourceKind != row.SourceKind {
			return serverapi.TaskSearchHit{}, errors.New("task search source identity changed during read")
		}
		sourceText, err := taskSearchSourceText(source)
		if err != nil {
			return serverapi.TaskSearchHit{}, err
		}
		literal, found := matcher.NthHit(sourceText, int(row.SourceOrdinal), req.Context)
		if !found {
			return serverapi.TaskSearchHit{}, errors.New("task search selected literal occurrence is absent")
		}
		hit.Literal = &serverapi.TaskSearchLiteralHit{
			Before:         literal.Before,
			Match:          literal.Match,
			After:          literal.After,
			LeftTruncated:  literal.LeftTruncated,
			RightTruncated: literal.RightTruncated,
		}
	case serverapi.TaskSearchModeFTS5:
		snippet, err := taskSearchSQLiteString(row.RawSnippet, "FTS5 snippet")
		if err != nil {
			return serverapi.TaskSearchHit{}, err
		}
		hit.FTS5 = &serverapi.TaskSearchFTS5Hit{Snippet: snippet}
	default:
		return serverapi.TaskSearchHit{}, errors.New("task search mode is invalid")
	}
	return hit, nil
}

func taskSearchSourceText(source sqlitegen.GetTaskSearchSourceByDocumentIDRow) (string, error) {
	switch source.SourceKind {
	case string(serverapi.TaskSearchSourceKindTitle):
		return taskSearchSQLiteString(source.Title, "title")
	case string(serverapi.TaskSearchSourceKindBody):
		return taskSearchSQLiteString(source.Body, "body")
	case string(serverapi.TaskSearchSourceKindComment):
		return taskSearchSQLiteString(source.Comment, "comment")
	default:
		return "", errors.New("task search source kind is invalid")
	}
}

func taskSearchSQLiteString(value interface{}, field string) (string, error) {
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("task search %s has type %T, want text", field, value)
	}
	return text, nil
}

func taskSearchFTS5OperationalError(err error) error {
	var sqliteErr *sqlitedriver.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == sqlite3.SQLITE_ERROR {
		return errors.New("task search FTS5 query could not be evaluated")
	}
	return err
}

func taskSearchProjectIDs(req serverapi.TaskSearchRequest) []string {
	return append([]string{}, req.ProjectIDs...)
}

func taskSearchStatusKinds(req serverapi.TaskSearchRequest) []string {
	statuses := make([]string, 0, len(req.StatusKinds))
	for _, status := range req.StatusKinds {
		statuses = append(statuses, string(status))
	}
	return statuses
}
