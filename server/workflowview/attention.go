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
	"core/server/workflow"
	"core/server/workflowattention"
	"core/shared/serverapi"
)

const attentionKindInterruptedRun = "interrupted_run"

const interruptedRunAttentionMessage = "This task's run was stopped."

type Attention struct {
	queries      *sqlitegen.Queries
	definitions  *DefinitionProjection
	projector    *TaskProjector
	roleResolver workflow.RoleResolver
	transcripts  SessionActiveTranscriptProvider
	prompts      PendingPromptSource
}

func NewAttention(metadataStore *metadata.Store, definitions *DefinitionProjection, projector *TaskProjector, roleResolver workflow.RoleResolver, transcripts SessionActiveTranscriptProvider, prompts PendingPromptSource) (*Attention, error) {
	if metadataStore == nil || metadataStore.Queries() == nil {
		return nil, errors.New("metadata store is required")
	}
	if definitions == nil {
		return nil, errors.New("definition projection is required")
	}
	if projector == nil {
		return nil, errors.New("task projector is required")
	}
	if roleResolver == nil {
		return nil, errors.New("role resolver is required")
	}
	return &Attention{
		queries:      metadataStore.Queries(),
		definitions:  definitions,
		projector:    projector,
		roleResolver: roleResolver,
		transcripts:  transcripts,
		prompts:      prompts,
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
	return serverapi.WorkflowAttentionListResponse{Items: items, NextPageToken: nextPageToken, GeneratedAtUnixMs: time.Now().UTC().UnixMilli()}, nil
}

func (a *Attention) ListTask(ctx context.Context, req serverapi.WorkflowTaskAttentionListRequest) (serverapi.WorkflowTaskAttentionListResponse, error) {
	if a == nil {
		return serverapi.WorkflowTaskAttentionListResponse{}, errors.New("attention read model is required")
	}
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskAttentionListResponse{}, err
	}
	task, err := a.queries.GetTask(ctx, strings.TrimSpace(req.TaskID))
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
	items := make([]serverapi.WorkflowAttentionItem, 0, pageSize)
	questions := newPendingQuestionResolver(a.transcripts, a.prompts)
	current := cursor
	// Candidate projection can drop a validation candidate that no longer
	// blocks. Continue reading until the response page is full or exhausted.
	for len(items) < pageSize {
		candidates, err := a.itemCandidates(ctx, current, pageSize+1)
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
			item, include, err := a.itemFromCandidate(ctx, candidate, questions)
			if err != nil {
				return nil, "", err
			}
			if !include {
				continue
			}
			items = append(items, item)
			if len(items) == pageSize {
				return items, attentionPageTokenFor(candidate.occurredAtUnixMs, candidate.id), nil
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
		return serverapi.WorkflowAttentionItem{ID: row.id, Kind: "approval", ProjectID: row.projectID, WorkflowID: &workflowID, TaskID: taskID, TaskShortID: shortID, TaskTitle: title, TaskTransitionID: transitionID, Message: workflowattention.ApprovalRequiredMessage, ApprovalSnapshot: &snapshot, OccurredAtUnixMs: row.occurredAtUnixMs}, true, nil
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
		return workflowQuestionAttentionItem(row.id, row.projectID, row.workflowID, taskID, shortID, title, runID, sessionID, askID, question, row.occurredAtUnixMs), true, nil
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
		return serverapi.WorkflowAttentionItem{ID: row.id, Kind: attentionKindInterruptedRun, ProjectID: row.projectID, WorkflowID: &workflowID, TaskID: taskID, TaskShortID: shortID, TaskTitle: title, RunID: runID, SessionID: optionalAttentionCandidateValue(row.sessionID), Message: workflowattention.InterruptedRunMessage(row.interruptionReason, detailJSON), DetailJSON: detailJSON, OccurredAtUnixMs: row.occurredAtUnixMs}, true, nil
	case "validation_blocker":
		snapshot, err := a.definitions.snapshot(ctx, row.workflowID)
		if err != nil {
			return serverapi.WorkflowAttentionItem{}, false, err
		}
		validation := definitionExecutionValidation(snapshot.domain, a.roleResolver)
		if !validation.HasBlockingErrors() {
			return serverapi.WorkflowAttentionItem{}, false, nil
		}
		return serverapi.WorkflowAttentionItem{ID: row.id, Kind: "validation_blocker", ProjectID: row.projectID, WorkflowID: &workflowID, Message: fmt.Sprintf("Workflow %q is invalid for task start", snapshot.api.Workflow.Name), OccurredAtUnixMs: row.occurredAtUnixMs}, true, nil
	default:
		return serverapi.WorkflowAttentionItem{}, false, fmt.Errorf("unknown attention item kind %q", row.kind)
	}
}

func (a *Attention) approvalSnapshot(ctx context.Context, taskID string, transitionID string) (serverapi.WorkflowAttentionApprovalSnapshot, error) {
	transitions, err := a.queries.ListTaskTransitionsByIDs(ctx, []string{transitionID})
	if err != nil {
		return serverapi.WorkflowAttentionApprovalSnapshot{}, err
	}
	if len(transitions) != 1 {
		return serverapi.WorkflowAttentionApprovalSnapshot{}, fmt.Errorf("approval attention transition %q returned %d records", transitionID, len(transitions))
	}
	transition := transitions[0]
	if transition.ID != transitionID || transition.TaskID != taskID || transition.State != "pending_approval" {
		return serverapi.WorkflowAttentionApprovalSnapshot{}, fmt.Errorf("approval attention transition %q does not match pending task %q", transitionID, taskID)
	}
	edges, err := a.queries.ListTaskTransitionEdges(ctx, transitionID)
	if err != nil {
		return serverapi.WorkflowAttentionApprovalSnapshot{}, err
	}
	projected, err := a.projector.ProjectTransition(TransitionProjectionInput{Transition: transition, Edges: edges})
	if err != nil {
		return serverapi.WorkflowAttentionApprovalSnapshot{}, err
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
	return snapshot, nil
}

func requiredAttentionCandidateValue(row attentionCandidateRow, field string, value *string) (string, error) {
	if value == nil || strings.TrimSpace(*value) == "" {
		return "", fmt.Errorf("workflow attention candidate invariant violated: kind=%q id=%q field=%q is absent", row.kind, row.id, field)
	}
	return *value, nil
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
