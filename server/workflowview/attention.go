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
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowstore"
	"core/shared/clientui"
	"core/shared/serverapi"
	"core/shared/textutil"
)

type Attention struct {
	queries     *sqlitegen.Queries
	definitions *DefinitionProjection
	status      *TaskStatusProjection
}

func NewAttention(
	metadataStore *metadata.Store,
	definitions *DefinitionProjection,
	status *TaskStatusProjection,
) (*Attention, error) {
	if metadataStore == nil || metadataStore.Queries() == nil {
		return nil, errors.New("metadata store is required")
	}
	if definitions == nil {
		return nil, errors.New("definition projection is required")
	}
	if status == nil {
		return nil, errors.New("task status projection is required")
	}
	return &Attention{
		queries:     metadataStore.Queries(),
		definitions: definitions,
		status:      status,
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
	var items []serverapi.WorkflowAttentionItem
	err = a.status.WithBoundedLifecycle(ctx, func(
		_ string,
		durableSnapshot *TaskStatusDurableSnapshot,
		reader workflowexecution.WorkflowTaskLifecycleReader,
	) error {
		snapshot := *a
		snapshot.queries = durableSnapshot.queries
		questions, err := reader.PendingQuestions(ctx, workflowstore.LifecycleQuestionCursor{
			OccurredAtUnixMs: cursor.occurredAtUnixMs,
			ItemID:           cursor.itemID,
			HasValue:         cursor.hasValue,
		}, pageSize+1)
		if err != nil {
			return err
		}
		live, err := snapshot.liveQuestionCandidates(ctx, questions, nil)
		if err != nil {
			return err
		}
		durable, err := snapshot.durableCandidates(ctx, cursor, nil, pageSize+1)
		if err != nil {
			return err
		}
		items = mergeAttentionCandidates(cursor, durable, live)
		return nil
	})
	if err != nil {
		return serverapi.WorkflowAttentionListResponse{}, err
	}
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
	var items []serverapi.WorkflowAttentionItem
	err := a.status.WithSnapshot(ctx, []workflow.TaskID{workflow.TaskID(taskID)}, func(
		observation TaskStatusObservation,
		durableSnapshot *TaskStatusDurableSnapshot,
	) error {
		snapshot := *a
		snapshot.queries = durableSnapshot.queries
		task, err := snapshot.queries.GetTask(ctx, taskID)
		if err != nil {
			return err
		}
		durable, err := snapshot.durableCandidates(ctx, attentionPageCursor{}, &taskID, 0)
		if err != nil {
			return err
		}
		live, err := snapshot.liveQuestionCandidates(
			ctx,
			lifecyclePendingQuestions(observation.Live, &taskID),
			&task,
		)
		if err != nil {
			return err
		}
		items = mergeAttentionCandidates(attentionPageCursor{}, durable, live)
		return nil
	})
	if err != nil {
		return serverapi.WorkflowTaskAttentionListResponse{}, err
	}
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
		var detailJSON *string
		if row.InterruptionDetailJson.Valid {
			value := strings.TrimSpace(row.InterruptionDetailJson.String)
			if value == "" {
				return serverapi.WorkflowAttentionItem{}, fmt.Errorf("interrupted attention candidate %q has blank interruption detail", row.ID)
			}
			detailJSON = &value
		}
		return serverapi.WorkflowAttentionItem{
			ID:               row.ID,
			Kind:             "interrupted_current_node",
			ProjectID:        row.ProjectID,
			WorkflowID:       row.WorkflowID,
			TaskID:           row.TaskID,
			TaskShortID:      row.ShortID,
			TaskTitle:        row.Title,
			CurrentNode:      &currentNode,
			SessionID:        currentNode.SessionID,
			DetailJSON:       detailJSON,
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
		Commentary:            approval.Commentary,
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

func lifecyclePendingQuestions(
	observation workflowexecution.WorkflowTaskExecutionObservation,
	taskFilter *string,
) []workflowstore.LifecyclePendingQuestion {
	out := make([]workflowstore.LifecyclePendingQuestion, 0)
	for taskID, lifecycle := range observation.Lifecycle {
		if taskFilter != nil && string(taskID) != *taskFilter {
			continue
		}
		for _, execution := range lifecycle.ExactExecutions {
			if execution.Agent == nil {
				continue
			}
			for _, prompt := range execution.PendingPrompts {
				out = append(out, workflowstore.LifecyclePendingQuestion{
					TaskID:      taskID,
					CurrentNode: execution.CurrentNode,
					SessionID:   execution.Agent.SessionID,
					Prompt:      prompt,
				})
			}
		}
	}
	return out
}

func (a *Attention) liveQuestionCandidates(
	ctx context.Context,
	questions []workflowstore.LifecyclePendingQuestion,
	selectedTask *sqlitegen.TaskRecord,
) ([]attentionCandidate, error) {
	out := make([]attentionCandidate, 0, len(questions))
	tasks := make(map[workflow.TaskID]sqlitegen.TaskRecord)
	for _, pending := range questions {
		task, exists := tasks[pending.TaskID]
		if selectedTask != nil && selectedTask.ID == string(pending.TaskID) {
			task = *selectedTask
			exists = true
		}
		if !exists {
			record, err := a.queries.GetTask(ctx, string(pending.TaskID))
			if err != nil {
				return nil, err
			}
			task = record
			tasks[pending.TaskID] = record
		}
		prompt := pending.Prompt
		question, present, err := pendingQuestionFromPrompt(PendingPromptSnapshot{
			ID:                     prompt.ID,
			CreatedAt:              prompt.CreatedAt,
			Question:               prompt.Question,
			Suggestions:            prompt.Suggestions,
			RecommendedOptionIndex: prompt.RecommendedOptionIndex,
			Approval:               prompt.Kind == workflowstore.LifecyclePendingPromptSessionApproval,
			ApprovalDecisions:      lifecycleApprovalDecisions(prompt.ApprovalDecisions),
		})
		if err != nil {
			return nil, err
		}
		if !present {
			continue
		}
		occurredAt := prompt.CreatedAt.UnixMilli()
		if occurredAt <= 0 {
			return nil, fmt.Errorf("task %q session %q prompt %q has no occurrence time", pending.TaskID, pending.SessionID, prompt.ID)
		}
		questionID := prompt.ID
		sessionID := pending.SessionID.String()
		itemID, err := workflowstore.LifecycleQuestionItemID(pending.SessionID, questionID)
		if err != nil {
			return nil, err
		}
		currentNode := workflowCurrentNodeReference(pending.CurrentNode)
		currentNode.SessionID = &sessionID
		out = append(out, attentionCandidate{item: serverapi.WorkflowAttentionItem{
			ID:                     itemID,
			Kind:                   "question",
			ProjectID:              task.ProjectID,
			WorkflowID:             task.WorkflowID,
			TaskID:                 task.ID,
			TaskShortID:            task.ShortID,
			TaskTitle:              task.Title,
			Message:                textutil.Value(question.message),
			CurrentNode:            &currentNode,
			SessionID:              &sessionID,
			QuestionID:             &questionID,
			Suggestions:            question.suggestions,
			RecommendedOptionIndex: question.recommendedOptionIndex,
			Question:               question.prompt,
			OccurredAtUnixMs:       occurredAt,
		}})
	}
	if err := a.attachLiveQuestionSessionNames(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

func lifecycleApprovalDecisions(
	decisions []workflowstore.LifecycleApprovalDecision,
) []clientui.ApprovalDecision {
	out := make([]clientui.ApprovalDecision, len(decisions))
	for index, decision := range decisions {
		out[index] = clientui.ApprovalDecision(decision)
	}
	return out
}

func (a *Attention) attachLiveQuestionSessionNames(ctx context.Context, candidates []attentionCandidate) error {
	sessionIDs := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidate.item.SessionID == nil {
			return fmt.Errorf("live question attention %q has no session id", candidate.item.ID)
		}
		sessionID := strings.TrimSpace(*candidate.item.SessionID)
		if sessionID == "" {
			return fmt.Errorf("live question attention %q has a blank session id", candidate.item.ID)
		}
		if _, exists := seen[sessionID]; exists {
			continue
		}
		seen[sessionID] = struct{}{}
		sessionIDs = append(sessionIDs, sessionID)
	}
	if len(sessionIDs) == 0 {
		return nil
	}
	rows, err := a.queries.ListSessionNamesByIDs(ctx, sessionIDs)
	if err != nil {
		return err
	}
	names := make(map[string]*string, len(rows))
	for _, row := range rows {
		sessionID := strings.TrimSpace(row.ID)
		if sessionID == "" {
			return errors.New("session name lookup returned a blank session id")
		}
		if _, exists := names[sessionID]; exists {
			return fmt.Errorf("session name lookup returned duplicate session %q", sessionID)
		}
		var name *string
		if trimmed := strings.TrimSpace(row.Name); trimmed != "" {
			name = &trimmed
		}
		names[sessionID] = name
	}
	for index := range candidates {
		sessionID := strings.TrimSpace(*candidates[index].item.SessionID)
		name, exists := names[sessionID]
		if !exists {
			return fmt.Errorf("live question session %q has no persisted metadata", sessionID)
		}
		candidates[index].item.SessionName = name
	}
	return nil
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
