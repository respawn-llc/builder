package workflowview

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/workflowattention"
	"core/shared/serverapi"
	"core/shared/textutil"
)

const attentionKindInterruptedRun = "interrupted_run"

const interruptedRunAttentionMessage = "This task's run was stopped."

type Attention struct {
	queries     *sqlitegen.Queries
	projector   *TaskProjector
	transcripts SessionActiveTranscriptProvider
	prompts     PendingPromptSource
}

func NewAttention(queries *sqlitegen.Queries, projector *TaskProjector, transcripts SessionActiveTranscriptProvider, prompts PendingPromptSource) (*Attention, error) {
	if queries == nil {
		return nil, errors.New("metadata queries are required")
	}
	if projector == nil {
		return nil, errors.New("task projector is required")
	}
	return &Attention{
		queries:     queries,
		projector:   projector,
		transcripts: transcripts,
		prompts:     prompts,
	}, nil
}

func (a *Attention) List(ctx context.Context, req serverapi.WorkflowAttentionListRequest) (serverapi.WorkflowAttentionListResponse, error) {
	if a == nil {
		return serverapi.WorkflowAttentionListResponse{}, errors.New("attention read model is required")
	}
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowAttentionListResponse{}, err
	}
	pageSize := req.PageSize
	if pageSize == 0 {
		pageSize = 50
	}
	cursor, err := parseAttentionPageToken(req.PageToken)
	if err != nil {
		return serverapi.WorkflowAttentionListResponse{}, err
	}
	items, nextPageToken, err := a.itemsPage(ctx, pageSize, cursor)
	if err != nil {
		return serverapi.WorkflowAttentionListResponse{}, err
	}
	return serverapi.WorkflowAttentionListResponse{
		Items:             items,
		NextPageToken:     nextPageToken,
		GeneratedAtUnixMs: time.Now().UTC().UnixMilli(),
	}, nil
}

func (a *Attention) ListTask(ctx context.Context, req serverapi.WorkflowTaskAttentionListRequest) (serverapi.WorkflowTaskAttentionListResponse, error) {
	if a == nil {
		return serverapi.WorkflowTaskAttentionListResponse{}, errors.New("attention read model is required")
	}
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskAttentionListResponse{}, err
	}
	taskID := strings.TrimSpace(req.TaskID)
	task, err := a.queries.GetTask(ctx, taskID)
	if err != nil {
		return serverapi.WorkflowTaskAttentionListResponse{}, err
	}
	rows, err := a.queries.ListWorkflowTaskAttentionCandidates(ctx, task.ID)
	if err != nil {
		return serverapi.WorkflowTaskAttentionListResponse{}, err
	}
	candidates := attentionCandidateRows(rows)
	items := make([]serverapi.WorkflowAttentionItem, 0, len(candidates))
	questions := newPendingQuestionResolver(a.transcripts, a.prompts)
	for _, candidate := range candidates {
		item, include, err := a.itemFromCandidate(ctx, candidate, questions)
		if err != nil {
			return serverapi.WorkflowTaskAttentionListResponse{}, err
		}
		if include {
			items = append(items, item)
		}
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
	taskID                 *string
	shortID                *string
	title                  *string
	runID                  *string
	sessionID              *string
	askID                  *string
	taskTransitionID       *string
	interruptionReason     *string
	interruptionDetailJSON *string
	occurredAtUnixMs       int64
}

func (a *Attention) itemsPage(ctx context.Context, pageSize int, cursor attentionPageCursor) ([]serverapi.WorkflowAttentionItem, string, error) {
	questions := newPendingQuestionResolver(a.transcripts, a.prompts)
	page, err := fillAttentionPage(
		pageSize,
		cursor,
		func(current attentionPageCursor, limit int) ([]attentionCandidateRow, error) {
			return a.itemCandidates(ctx, current, limit)
		},
		func(candidate attentionCandidateRow) (serverapi.WorkflowAttentionItem, bool, error) {
			return a.itemFromCandidate(ctx, candidate, questions)
		},
	)
	if err != nil {
		return nil, "", err
	}
	if page.continuation == nil {
		return page.items, "", nil
	}
	return page.items, attentionPageTokenFor(page.continuation.occurredAtUnixMs, page.continuation.itemID), nil
}

func attentionCandidateCursor(row attentionCandidateRow) attentionPageCursor {
	return attentionPageCursor{occurredAtUnixMs: row.occurredAtUnixMs, itemID: row.id, hasValue: true}
}

func (a *Attention) itemCandidates(ctx context.Context, cursor attentionPageCursor, limit int) ([]attentionCandidateRow, error) {
	cursorSet := int64(0)
	if cursor.hasValue {
		cursorSet = 1
	}
	rows, err := a.queries.ListWorkflowAttentionCandidates(ctx, sqlitegen.ListWorkflowAttentionCandidatesParams{
		PageLimit:              int64(limit),
		CursorActive:           cursorSet,
		CursorOccurredAtUnixMs: cursor.occurredAtUnixMs,
		CursorItemID:           cursor.itemID,
	})
	if err != nil {
		return nil, err
	}
	return attentionCandidateRows(rows), nil
}

func attentionCandidateRows(rows []sqlitegen.WorkflowAttentionCandidate) []attentionCandidateRow {
	items := make([]attentionCandidateRow, 0, len(rows))
	for _, row := range rows {
		items = append(items, attentionCandidateRow{
			kind:                   row.Kind,
			id:                     row.ID,
			projectID:              row.ProjectID,
			workflowID:             row.WorkflowID,
			taskID:                 metadata.OptionalString(row.TaskID),
			shortID:                metadata.OptionalString(row.ShortID),
			title:                  metadata.OptionalString(row.Title),
			runID:                  metadata.OptionalString(row.RunID),
			sessionID:              metadata.OptionalString(row.SessionID),
			askID:                  metadata.OptionalString(row.AskID),
			taskTransitionID:       metadata.OptionalString(row.TaskTransitionID),
			interruptionReason:     metadata.OptionalString(row.InterruptionReason),
			interruptionDetailJSON: metadata.OptionalString(row.InterruptionDetailJson),
			occurredAtUnixMs:       row.OccurredAtUnixMs,
		})
	}
	return items
}

func (a *Attention) itemFromCandidate(ctx context.Context, row attentionCandidateRow, questions *pendingQuestionResolver) (serverapi.WorkflowAttentionItem, bool, error) {
	workflowID := row.workflowID
	switch row.kind {
	case "approval":
		taskID, err := requiredAttentionCandidateValue(row, "task_id", row.taskID)
		if err != nil {
			return serverapi.WorkflowAttentionItem{}, false, err
		}
		shortID, err := requiredAttentionCandidateValue(row, "short_id", row.shortID)
		if err != nil {
			return serverapi.WorkflowAttentionItem{}, false, err
		}
		title, err := requiredAttentionCandidateValue(row, "title", row.title)
		if err != nil {
			return serverapi.WorkflowAttentionItem{}, false, err
		}
		transitionID, err := requiredAttentionCandidateValue(row, "task_transition_id", row.taskTransitionID)
		if err != nil {
			return serverapi.WorkflowAttentionItem{}, false, err
		}
		snapshot, err := a.approvalSnapshot(ctx, taskID, transitionID)
		if err != nil {
			return serverapi.WorkflowAttentionItem{}, false, err
		}
		if snapshot == nil {
			return serverapi.WorkflowAttentionItem{}, false, nil
		}
		return serverapi.WorkflowAttentionItem{ID: row.id, Kind: "approval", ProjectID: row.projectID, WorkflowID: &workflowID, TaskID: taskID, TaskShortID: shortID, TaskTitle: title, TaskTransitionID: textutil.Pointer(&transitionID), Message: workflowattention.ApprovalRequiredMessage, ApprovalSnapshot: snapshot, OccurredAtUnixMs: row.occurredAtUnixMs}, true, nil
	case "question":
		taskID, err := requiredAttentionCandidateValue(row, "task_id", row.taskID)
		if err != nil {
			return serverapi.WorkflowAttentionItem{}, false, err
		}
		shortID, err := requiredAttentionCandidateValue(row, "short_id", row.shortID)
		if err != nil {
			return serverapi.WorkflowAttentionItem{}, false, err
		}
		title, err := requiredAttentionCandidateValue(row, "title", row.title)
		if err != nil {
			return serverapi.WorkflowAttentionItem{}, false, err
		}
		runID, err := requiredAttentionCandidateValue(row, "run_id", row.runID)
		if err != nil {
			return serverapi.WorkflowAttentionItem{}, false, err
		}
		askID, err := requiredAttentionCandidateValue(row, "ask_id", row.askID)
		if err != nil {
			return serverapi.WorkflowAttentionItem{}, false, err
		}
		sessionID := optionalAttentionCandidateValue(row.sessionID)
		question, err := questions.Question(ctx, sessionID, askID)
		if err != nil {
			question = pendingQuestion{message: pendingQuestionFallbackMessage}
		}
		return workflowQuestionAttentionItem(row.id, row.projectID, row.workflowID, taskID, shortID, title, runID, row.sessionID, askID, question, row.occurredAtUnixMs), true, nil
	case attentionKindInterruptedRun:
		taskID, err := requiredAttentionCandidateValue(row, "task_id", row.taskID)
		if err != nil {
			return serverapi.WorkflowAttentionItem{}, false, err
		}
		shortID, err := requiredAttentionCandidateValue(row, "short_id", row.shortID)
		if err != nil {
			return serverapi.WorkflowAttentionItem{}, false, err
		}
		title, err := requiredAttentionCandidateValue(row, "title", row.title)
		if err != nil {
			return serverapi.WorkflowAttentionItem{}, false, err
		}
		runID, err := requiredAttentionCandidateValue(row, "run_id", row.runID)
		if err != nil {
			return serverapi.WorkflowAttentionItem{}, false, err
		}
		detailJSON := optionalAttentionCandidateValue(row.interruptionDetailJSON)
		return serverapi.WorkflowAttentionItem{ID: row.id, Kind: attentionKindInterruptedRun, ProjectID: row.projectID, WorkflowID: &workflowID, TaskID: taskID, TaskShortID: shortID, TaskTitle: title, RunID: textutil.Pointer(&runID), SessionID: textutil.Pointer(row.sessionID), Message: workflowattention.InterruptedRunMessage(row.interruptionReason, detailJSON), DetailJSON: textutil.Pointer(row.interruptionDetailJSON), OccurredAtUnixMs: row.occurredAtUnixMs}, true, nil
	default:
		return serverapi.WorkflowAttentionItem{}, false, attentionCandidateValidationError(row, serverapi.WorkflowRequestErrorInvalidMode, "kind", "kind must be approval, question, or interrupted_run")
	}
}

func (a *Attention) approvalSnapshot(ctx context.Context, taskID string, transitionID string) (*serverapi.WorkflowAttentionApprovalSnapshot, error) {
	transitions, err := a.queries.ListTaskTransitionsByIDs(ctx, []string{transitionID})
	if err != nil {
		return nil, err
	}
	if len(transitions) == 0 {
		return nil, nil
	}
	if len(transitions) > 1 {
		return nil, fmt.Errorf("approval attention transition %q returned %d records", transitionID, len(transitions))
	}
	transition := transitions[0]
	if transition.ID != transitionID || transition.TaskID != taskID {
		return nil, fmt.Errorf("approval attention transition %q does not match task %q", transitionID, taskID)
	}
	if transition.State != "pending_approval" {
		return nil, nil
	}
	edges, err := a.queries.ListTaskTransitionEdges(ctx, transitionID)
	if err != nil {
		return nil, err
	}
	projected, err := a.projector.ProjectTransition(TransitionProjectionInput{Transition: transition, Edges: edges})
	if err != nil {
		return nil, err
	}
	snapshot := serverapi.WorkflowAttentionApprovalSnapshot{
		SourceNodeDisplayName: projected.SourceNodeDisplayName,
		Targets:               make([]serverapi.WorkflowAttentionApprovalTarget, 0, len(projected.Edges)),
		Commentary:            projected.Commentary,
		OutputValues:          projected.OutputValues,
		WorkflowRevisionSeen:  projected.WorkflowRevisionSeen,
	}
	for _, edge := range projected.Edges {
		snapshot.Targets = append(snapshot.Targets, serverapi.WorkflowAttentionApprovalTarget{
			DisplayName: edge.TargetNodeDisplayName,
		})
	}
	return &snapshot, nil
}

func requiredAttentionCandidateValue(row attentionCandidateRow, field string, value *string) (string, error) {
	if value == nil || strings.TrimSpace(*value) == "" {
		return "", attentionCandidateValidationError(row, serverapi.WorkflowRequestErrorRequired, field, field+" is required")
	}
	return *value, nil
}

func attentionCandidateValidationError(row attentionCandidateRow, code string, field string, message string) error {
	return serverapi.WorkflowRequestValidationError{
		Code:    code,
		Field:   field,
		Message: fmt.Sprintf("workflow attention candidate kind=%q id=%q: %s", row.kind, row.id, message),
	}
}

func optionalAttentionCandidateValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func parseAttentionPageToken(token string) (attentionPageCursor, error) {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return attentionPageCursor{}, nil
	}
	timestampPart, encodedID, ok := strings.Cut(trimmed, "|")
	if !ok {
		return attentionPageCursor{}, errors.New("page_token is invalid")
	}
	occurredAt, err := strconv.ParseInt(timestampPart, 10, 64)
	if err != nil || occurredAt < 0 {
		return attentionPageCursor{}, errors.New("page_token is invalid")
	}
	decodedID, err := base64.RawURLEncoding.DecodeString(encodedID)
	if err != nil || strings.TrimSpace(string(decodedID)) == "" {
		return attentionPageCursor{}, errors.New("page_token is invalid")
	}
	return attentionPageCursor{occurredAtUnixMs: occurredAt, itemID: string(decodedID), hasValue: true}, nil
}

func attentionPageTokenFor(occurredAtUnixMs int64, id string) string {
	return strconv.FormatInt(occurredAtUnixMs, 10) + "|" + base64.RawURLEncoding.EncodeToString([]byte(id))
}
