package workflowview

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/shared/serverapi"
)

type Attention struct {
	queries     *sqlitegen.Queries
	definitions *DefinitionProjection
	authority   *sessionruntime.Authority
	prompts     PendingPromptSource
}

func NewAttention(metadataStore *metadata.Store, definitions *DefinitionProjection, authority *sessionruntime.Authority, prompts PendingPromptSource) (*Attention, error) {
	if metadataStore == nil || metadataStore.Queries() == nil {
		return nil, errors.New("metadata store is required")
	}
	if definitions == nil {
		return nil, errors.New("definition projection is required")
	}
	if authority == nil {
		return nil, errors.New("session runtime authority is required")
	}
	if prompts == nil {
		return nil, errors.New("pending prompt source is required")
	}
	return &Attention{
		queries:     metadataStore.Queries(),
		definitions: definitions,
		authority:   authority,
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
	live, err := a.liveQuestionCandidates(ctx, nil, nil)
	if err != nil {
		return serverapi.WorkflowAttentionListResponse{}, err
	}
	durable, err := a.durableCandidates(ctx, cursor, nil, pageSize+len(live)+1)
	if err != nil {
		return serverapi.WorkflowAttentionListResponse{}, err
	}
	items := mergeAttentionCandidates(cursor, durable, live)
	hasNext := len(items) > pageSize
	if hasNext {
		items = items[:pageSize]
	}
	nextPageToken := ""
	if hasNext && len(items) != 0 {
		last := items[len(items)-1]
		nextPageToken = attentionPageTokenFor(last.OccurredAtUnixMs, last.ID)
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
	durable, err := a.durableCandidates(ctx, attentionPageCursor{}, &taskID, 0)
	if err != nil {
		return serverapi.WorkflowTaskAttentionListResponse{}, err
	}
	live, err := a.liveQuestionCandidates(ctx, &taskID, &task)
	if err != nil {
		return serverapi.WorkflowTaskAttentionListResponse{}, err
	}
	items := mergeAttentionCandidates(attentionPageCursor{}, durable, live)
	return serverapi.WorkflowTaskAttentionListResponse{
		Items:             items,
		GeneratedAtUnixMs: time.Now().UTC().UnixMilli(),
	}, nil
}

type attentionPageCursor struct {
	occurredAtUnixMs int64
	itemID           string
	hasValue         bool
}

type attentionCandidate struct {
	item serverapi.WorkflowAttentionItem
}

func (a *Attention) durableCandidates(ctx context.Context, cursor attentionPageCursor, taskID *string, limit int) ([]attentionCandidate, error) {
	rows, err := a.durableCandidateRows(ctx, cursor, taskID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]attentionCandidate, 0, len(rows))
	for _, row := range rows {
		item, err := a.durableCandidate(ctx, row)
		if err != nil {
			return nil, err
		}
		out = append(out, attentionCandidate{item: item})
	}
	return out, nil
}

func (a *Attention) durableCandidateRows(ctx context.Context, cursor attentionPageCursor, taskID *string, limit int) ([]sqlitegen.ListWorkflowDurableAttentionCandidatesRow, error) {
	task := sql.NullString{}
	if taskID != nil {
		task = sql.NullString{String: *taskID, Valid: true}
	}
	return a.queries.ListWorkflowDurableAttentionCandidates(ctx, sqlitegen.ListWorkflowDurableAttentionCandidatesParams{
		SelectedTaskID:         task,
		PageLimit:              int64(limit),
		CursorActive:           boolInt64(cursor.hasValue),
		CursorOccurredAtUnixMs: cursor.occurredAtUnixMs,
		CursorItemID:           cursor.itemID,
	})
}

func (a *Attention) durableCandidate(ctx context.Context, row sqlitegen.ListWorkflowDurableAttentionCandidatesRow) (serverapi.WorkflowAttentionItem, error) {
	reference, err := durableAttentionNotificationReference(row)
	if err != nil {
		return serverapi.WorkflowAttentionItem{}, err
	}
	switch typed := reference.(type) {
	case DurableApprovalAttentionReference:
		approvalID := typed.ApprovalID.String()
		approval, err := a.pendingApproval(ctx, workflow.TaskID(row.TaskID), approvalID)
		if err != nil {
			return serverapi.WorkflowAttentionItem{}, err
		}
		snapshot := approvalAttentionSnapshot(approval)
		return serverapi.WorkflowAttentionItem{
			ID:               row.ID,
			Kind:             "approval",
			ProjectID:        row.ProjectID,
			WorkflowID:       row.WorkflowID,
			TaskID:           row.TaskID,
			TaskShortID:      row.ShortID,
			TaskTitle:        row.Title,
			ApprovalID:       &approvalID,
			ApprovalSnapshot: &snapshot,
			OccurredAtUnixMs: row.OccurredAtUnixMs,
		}, nil
	case DurableInterruptedCurrentNodeAttentionReference:
		currentNode, err := currentNodeFromAttentionCandidate(row, typed.CurrentNode)
		if err != nil {
			return serverapi.WorkflowAttentionItem{}, err
		}
		return serverapi.WorkflowAttentionItem{
			ID:               row.ID,
			Kind:             "interrupted",
			ProjectID:        row.ProjectID,
			WorkflowID:       row.WorkflowID,
			TaskID:           row.TaskID,
			TaskShortID:      row.ShortID,
			TaskTitle:        row.Title,
			CurrentNode:      &currentNode,
			OccurredAtUnixMs: row.OccurredAtUnixMs,
		}, nil
	default:
		return serverapi.WorkflowAttentionItem{}, fmt.Errorf("unsupported durable workflow attention notification reference %T", reference)
	}
}

func (a *Attention) pendingApproval(ctx context.Context, taskID workflow.TaskID, approvalID string) (workflow.PendingApproval, error) {
	approvals, err := a.definitions.store.ListPendingApprovals(ctx, taskID)
	if err != nil {
		return workflow.PendingApproval{}, err
	}
	for _, approval := range approvals {
		if approval.ID.String() == approvalID {
			return approval, nil
		}
	}
	return workflow.PendingApproval{}, fmt.Errorf("approval attention candidate %q is no longer pending for task %q", approvalID, taskID)
}

func approvalAttentionSnapshot(approval workflow.PendingApproval) serverapi.WorkflowAttentionApprovalSnapshot {
	targets := make([]serverapi.WorkflowAttentionApprovalTarget, 0, len(approval.Branches))
	for _, branch := range approval.Branches {
		targets = append(targets, serverapi.WorkflowAttentionApprovalTarget{DisplayName: branch.Target.DisplayName})
	}
	return serverapi.WorkflowAttentionApprovalSnapshot{
		SourceNodeDisplayName: approval.Transition.SourceDisplayName,
		Targets:               targets,
		Commentary:            "",
		OutputValues:          cloneOutputValues(approval.OutputValues),
		WorkflowRevisionSeen:  approval.WorkflowVersion,
	}
}

func cloneOutputValues(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func currentNodeFromAttentionCandidate(row sqlitegen.ListWorkflowDurableAttentionCandidatesRow, reference workflow.CurrentNodeReference) (serverapi.WorkflowTaskCurrentNode, error) {
	currentNode := workflowCurrentNodeReference(reference)
	if row.SessionID.Valid {
		value := strings.TrimSpace(row.SessionID.String)
		if value == "" {
			return serverapi.WorkflowTaskCurrentNode{}, fmt.Errorf("interrupted attention candidate %q has blank session id", row.ID)
		}
		currentNode.SessionID = &value
	}
	return currentNode, nil
}

func currentNodeReferenceFromAttentionCandidate(row sqlitegen.ListWorkflowDurableAttentionCandidatesRow) (workflow.CurrentNodeReference, error) {
	if !row.NodeID.Valid || strings.TrimSpace(row.NodeID.String) == "" {
		return workflow.CurrentNodeReference{}, fmt.Errorf("interrupted attention candidate %q has no current node", row.ID)
	}
	var branchKey *workflow.TransitionBranchKey
	if row.TransitionBranchKey.Valid {
		value := workflow.TransitionBranchKey(strings.TrimSpace(row.TransitionBranchKey.String))
		if value == "" {
			return workflow.CurrentNodeReference{}, fmt.Errorf("interrupted attention candidate %q has invalid branch key", row.ID)
		}
		branchKey = &value
	}
	reference, err := workflow.NewCurrentNodeReference(workflow.TaskID(row.TaskID), workflow.NodeID(row.NodeID.String), branchKey)
	if err != nil {
		return workflow.CurrentNodeReference{}, fmt.Errorf("interrupted attention candidate %q has invalid current node: %w", row.ID, err)
	}
	return reference, nil
}

func (a *Attention) liveQuestionCandidates(ctx context.Context, taskFilter *string, selectedTask *sqlitegen.TaskRecord) ([]attentionCandidate, error) {
	var snapshots map[workflow.TaskID]sessionruntime.TaskExecutionSnapshot
	var err error
	if selectedTask == nil {
		snapshots, err = a.authority.CurrentWorkflowTaskExecutionSnapshots()
	} else {
		snapshots, err = a.authority.CurrentScopedTaskExecutionSnapshots(
			selectedTask.ProjectID, workflow.WorkflowID(selectedTask.WorkflowID), []workflow.TaskID{workflow.TaskID(selectedTask.ID)},
		)
	}
	if err != nil {
		return nil, err
	}
	out := []attentionCandidate{}
	for taskID, snapshot := range snapshots {
		if taskFilter != nil && string(taskID) != *taskFilter {
			continue
		}
		var task *sqlitegen.TaskRecord
		for _, execution := range snapshot.Executions {
			if !execution.WaitingQuestion {
				continue
			}
			if execution.Agent == nil {
				return nil, fmt.Errorf("task %q has a question on a non-agent execution", taskID)
			}
			if task == nil {
				record, err := a.queries.GetTask(ctx, string(taskID))
				if err != nil {
					return nil, err
				}
				task = &record
			}
			prompts, err := a.prompts.ListPendingPrompts(execution.Agent.SessionID.String())
			if err != nil {
				return nil, err
			}
			for _, prompt := range prompts {
				if strings.TrimSpace(prompt.ID) == "" {
					return nil, fmt.Errorf("task %q session %q has a pending prompt without question identity", taskID, execution.Agent.SessionID)
				}
				question, present, err := pendingQuestionFromPrompt(prompt)
				if err != nil {
					return nil, err
				}
				if !present {
					return nil, fmt.Errorf("task %q session %q prompt %q cannot be projected", taskID, execution.Agent.SessionID, prompt.ID)
				}
				occurredAt := prompt.CreatedAt.UnixMilli()
				if occurredAt <= 0 {
					return nil, fmt.Errorf("task %q session %q prompt %q has no occurrence time", taskID, execution.Agent.SessionID, prompt.ID)
				}
				questionID := prompt.ID
				sessionID := execution.Agent.SessionID.String()
				currentNode := workflowCurrentNodeReference(execution.Ref.CurrentNode)
				currentNode.SessionID = &sessionID
				out = append(out, attentionCandidate{item: serverapi.WorkflowAttentionItem{
					ID:                     "question:" + sessionID + ":" + questionID,
					Kind:                   "question",
					ProjectID:              task.ProjectID,
					WorkflowID:             task.WorkflowID,
					TaskID:                 task.ID,
					TaskShortID:            task.ShortID,
					TaskTitle:              task.Title,
					CurrentNode:            &currentNode,
					SessionID:              &sessionID,
					QuestionID:             &questionID,
					Suggestions:            question.suggestions,
					RecommendedOptionIndex: question.recommendedOptionIndex,
					Question:               question.prompt,
					OccurredAtUnixMs:       occurredAt,
				}})
			}
		}
	}
	return out, nil
}

func mergeAttentionCandidates(cursor attentionPageCursor, groups ...[]attentionCandidate) []serverapi.WorkflowAttentionItem {
	items := []serverapi.WorkflowAttentionItem{}
	seen := map[string]struct{}{}
	for _, group := range groups {
		for _, candidate := range group {
			item := candidate.item
			if cursor.hasValue && !attentionItemBefore(item, cursor) {
				continue
			}
			if _, exists := seen[item.ID]; exists {
				continue
			}
			seen[item.ID] = struct{}{}
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].OccurredAtUnixMs != items[j].OccurredAtUnixMs {
			return items[i].OccurredAtUnixMs > items[j].OccurredAtUnixMs
		}
		return items[i].ID > items[j].ID
	})
	return items
}

func attentionItemBefore(item serverapi.WorkflowAttentionItem, cursor attentionPageCursor) bool {
	return item.OccurredAtUnixMs < cursor.occurredAtUnixMs ||
		(item.OccurredAtUnixMs == cursor.occurredAtUnixMs && item.ID < cursor.itemID)
}

func parseAttentionPageToken(token string) (attentionPageCursor, error) {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return attentionPageCursor{}, nil
	}
	timestampPart, encodedID, ok := strings.Cut(trimmed, "|")
	if !ok {
		return attentionPageCursor{}, ErrInvalidPageToken
	}
	occurredAt, err := strconv.ParseInt(timestampPart, 10, 64)
	if err != nil || occurredAt < 0 {
		return attentionPageCursor{}, ErrInvalidPageToken
	}
	decodedID, err := base64.RawURLEncoding.DecodeString(encodedID)
	if err != nil || strings.TrimSpace(string(decodedID)) == "" {
		return attentionPageCursor{}, ErrInvalidPageToken
	}
	return attentionPageCursor{occurredAtUnixMs: occurredAt, itemID: string(decodedID), hasValue: true}, nil
}

func attentionPageTokenFor(occurredAtUnixMs int64, id string) string {
	return strconv.FormatInt(occurredAtUnixMs, 10) + "|" + base64.RawURLEncoding.EncodeToString([]byte(id))
}
