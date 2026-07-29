package workflowview

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/shared/serverapi"
	"core/shared/tasksearchtext"

	sqlitedriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const taskSearchPageTokenVersion = 1

type TaskSearch struct {
	queries   *sqlitegen.Queries
	projector *TaskProjector
	authority *sessionruntime.Authority
}

type taskSearchPageToken struct {
	Version     int    `json:"version"`
	Fingerprint string `json:"fingerprint"`
	Ordinal     int64  `json:"ordinal"`
	RankBits    uint64 `json:"rank_bits"`
	TaskID      string `json:"task_id"`
}

func NewTaskSearch(metadataStore *metadata.Store, projector *TaskProjector, authority *sessionruntime.Authority) (*TaskSearch, error) {
	switch {
	case metadataStore == nil || metadataStore.Queries() == nil:
		return nil, errors.New("metadata store is required")
	case projector == nil:
		return nil, errors.New("task projector is required")
	case authority == nil:
		return nil, errors.New("session runtime authority is required")
	default:
		return &TaskSearch{
			queries:   metadataStore.Queries(),
			projector: projector,
			authority: authority,
		}, nil
	}
}

func (s *TaskSearch) Search(ctx context.Context, req serverapi.TaskSearchRequest) (response serverapi.TaskSearchResponse, err error) {
	if s == nil || s.queries == nil || s.projector == nil || s.authority == nil {
		return serverapi.TaskSearchResponse{}, errors.New("task search is required")
	}
	if err := req.Validate(); err != nil {
		return serverapi.TaskSearchResponse{}, err
	}
	if err := context.Cause(ctx); err != nil {
		return serverapi.TaskSearchResponse{}, err
	}
	fingerprint, err := taskSearchRequestFingerprint(req)
	if err != nil {
		return serverapi.TaskSearchResponse{}, err
	}
	cursor, hasCursor, err := parseTaskSearchPageToken(req.PageToken, fingerprint)
	if err != nil {
		return serverapi.TaskSearchResponse{}, err
	}
	liveSnapshots, err := s.authority.CurrentWorkflowTaskExecutionSnapshots()
	if err != nil {
		return serverapi.TaskSearchResponse{}, err
	}
	if err := s.validateSchemaAndScope(ctx, s.queries, req); err != nil {
		return serverapi.TaskSearchResponse{}, err
	}
	if req.Mode == serverapi.TaskSearchModeFTS5 {
		if _, validationErr := s.queries.ValidateTaskSearchFTS5Expression(ctx, sql.NullString{String: req.Query, Valid: true}); validationErr != nil {
			if taskSearchSQLiteMalformedExpression(validationErr) {
				return serverapi.TaskSearchResponse{}, &serverapi.TaskSearchError{Reason: serverapi.TaskSearchErrorReasonMalformedFTS5}
			}
			return serverapi.TaskSearchResponse{}, validationErr
		}
	}
	rows, err := s.queryPage(ctx, liveSnapshots, req, cursor, hasCursor)
	if err != nil {
		return serverapi.TaskSearchResponse{}, err
	}
	hasNext := len(rows) > req.PageSize
	if hasNext {
		rows = rows[:req.PageSize]
	}
	groups, err := s.materializeGroups(ctx, s.queries, req, rows)
	if err != nil {
		return serverapi.TaskSearchResponse{}, err
	}
	var next *string
	if hasNext && len(rows) > 0 {
		last := rows[len(rows)-1]
		encoded, encodeErr := encodeTaskSearchPageToken(taskSearchPageToken{
			Version:     taskSearchPageTokenVersion,
			Fingerprint: fingerprint,
			Ordinal:     last.Ordinal,
			RankBits:    math.Float64bits(last.TaskWeightedRank),
			TaskID:      last.TaskID,
		})
		if encodeErr != nil {
			return serverapi.TaskSearchResponse{}, encodeErr
		}
		next = &encoded
	}
	response = serverapi.TaskSearchResponse{Mode: req.Mode, Groups: groups, NextPageToken: next}
	if err := response.Validate(); err != nil {
		return serverapi.TaskSearchResponse{}, fmt.Errorf("validate task search response: %w", err)
	}
	return response, nil
}

func (s *TaskSearch) validateSchemaAndScope(ctx context.Context, queries *sqlitegen.Queries, req serverapi.TaskSearchRequest) error {
	objects, err := queries.ListTaskSearchSchemaObjects(ctx)
	if err != nil {
		return err
	}
	required := map[string]struct{}{
		"table\x00task_search_documents":                    {},
		"table\x00task_search_fts":                          {},
		"view\x00task_search_content":                       {},
		"trigger\x00task_search_document_insert":            {},
		"trigger\x00task_search_document_delete":            {},
		"trigger\x00task_search_task_insert":                {},
		"trigger\x00task_search_comment_insert":             {},
		"trigger\x00task_search_task_title_before_update":   {},
		"trigger\x00task_search_task_title_after_update":    {},
		"trigger\x00task_search_task_body_before_update":    {},
		"trigger\x00task_search_task_body_after_update":     {},
		"trigger\x00task_search_comment_body_before_update": {},
		"trigger\x00task_search_comment_body_after_update":  {},
		"trigger\x00task_search_comment_delete":             {},
		"trigger\x00task_search_task_delete":                {},
	}
	for _, object := range objects {
		delete(required, object.ObjectKind+"\x00"+object.ObjectName)
	}
	if len(required) != 0 {
		return errors.New("task search schema is incomplete")
	}
	if _, err := queries.CheckTaskSearchSchemaContract(ctx); err != nil {
		return err
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

func (s *TaskSearch) queryPage(ctx context.Context, liveSnapshots map[workflow.TaskID]sessionruntime.TaskExecutionSnapshot, req serverapi.TaskSearchRequest, cursor taskSearchPageToken, hasCursor bool) ([]sqlitegen.ListTaskSearchPageDescriptorsRow, error) {
	liveTaskStatesJSON, err := workflowTaskListLiveStatesJSON(liveSnapshots)
	if err != nil {
		return nil, err
	}
	projectIDs := req.ProjectIDs
	if projectIDs == nil {
		projectIDs = []string{}
	}
	projectIDsJSON, err := json.Marshal(projectIDs)
	if err != nil {
		return nil, fmt.Errorf("encode task search project ids: %w", err)
	}
	statusKindsJSON, err := json.Marshal(taskSearchStatusKinds(req))
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
	cursorRank := sql.NullFloat64{}
	if hasCursor {
		cursorRank = sql.NullFloat64{Float64: math.Float64frombits(cursor.RankBits), Valid: true}
	}
	return s.queries.ListTaskSearchPageDescriptors(ctx, sqlitegen.ListTaskSearchPageDescriptorsParams{
		Mode:                string(req.Mode),
		CandidateExpression: candidateExpression,
		LiteralQuery:        req.Query,
		CaseMode:            caseMode,
		IncludeComments:     boolInt64(req.IncludeComments),
		ProjectIdsJson:      string(projectIDsJSON),
		StatusFilterSet:     boolInt64(len(req.StatusKinds) > 0),
		StatusKindsJson:     string(statusKindsJSON),
		ContextClusters:     int64(req.Context),
		CursorSet:           boolInt64(hasCursor),
		CursorOrdinal:       cursor.Ordinal,
		CursorWeightedRank:  cursorRank,
		CursorTaskID:        cursor.TaskID,
		LimitRows:           int64(req.PageSize + 1),
		LiveTaskStatesJson:  liveTaskStatesJSON,
	})
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

func taskSearchSQLiteMalformedExpression(err error) bool {
	var sqliteErr *sqlitedriver.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == sqlite3.SQLITE_ERROR
}

func taskSearchRequestFingerprint(req serverapi.TaskSearchRequest) (string, error) {
	payload := struct {
		Mode               serverapi.TaskSearchMode `json:"mode"`
		Query              string                   `json:"query"`
		Context            int                      `json:"context"`
		CaseSensitive      bool                     `json:"case_sensitive"`
		IncludeComments    bool                     `json:"include_comments"`
		ProjectIDs         []string                 `json:"project_ids"`
		StatusKinds        []string                 `json:"status_kinds"`
		StatusModelVersion int                      `json:"status_model_version"`
		Normalization      string                   `json:"normalization"`
		SparseDocument     string                   `json:"sparse_document"`
		Ranking            string                   `json:"ranking"`
		PageTokenVersion   int                      `json:"page_token_version"`
	}{
		Mode:               req.Mode,
		Query:              req.Query,
		Context:            req.Context,
		CaseSensitive:      req.CaseSensitive,
		IncludeComments:    req.IncludeComments,
		ProjectIDs:         taskSearchProjectIDs(req),
		StatusKinds:        taskSearchStatusKinds(req),
		StatusModelVersion: workflowTaskStatusModelVersion,
		Normalization:      tasksearchtext.NormalizationContractVersion,
		SparseDocument:     serverapi.TaskSearchSparseDocumentContractVersion,
		Ranking:            serverapi.TaskSearchRankingContractVersion,
		PageTokenVersion:   taskSearchPageTokenVersion,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal task search fingerprint: %w", err)
	}
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
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

func parseTaskSearchPageToken(raw *string, fingerprint string) (taskSearchPageToken, bool, error) {
	if raw == nil {
		return taskSearchPageToken{}, false, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(*raw)
	if err != nil {
		return taskSearchPageToken{}, false, &serverapi.TaskSearchError{Reason: serverapi.TaskSearchErrorReasonInvalidCursor}
	}
	var token taskSearchPageToken
	if err := json.Unmarshal(decoded, &token); err != nil {
		return taskSearchPageToken{}, false, &serverapi.TaskSearchError{Reason: serverapi.TaskSearchErrorReasonInvalidCursor}
	}
	rank := math.Float64frombits(token.RankBits)
	if token.Version != taskSearchPageTokenVersion ||
		token.Fingerprint != fingerprint ||
		token.Ordinal < 1 ||
		math.IsInf(rank, 0) ||
		math.IsNaN(rank) ||
		strings.TrimSpace(token.TaskID) == "" ||
		strings.TrimSpace(token.TaskID) != token.TaskID {
		return taskSearchPageToken{}, false, &serverapi.TaskSearchError{Reason: serverapi.TaskSearchErrorReasonInvalidCursor}
	}
	return token, true, nil
}

func encodeTaskSearchPageToken(token taskSearchPageToken) (string, error) {
	raw, err := json.Marshal(token)
	if err != nil {
		return "", fmt.Errorf("marshal task search cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
