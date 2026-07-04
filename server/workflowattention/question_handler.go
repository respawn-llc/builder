package workflowattention

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	askquestion "core/server/tools"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/clientui"
)

type QuestionStore interface {
	GetRun(context.Context, workflow.RunID) (workflowstore.RunRecord, error)
	SetRunWaitingAsk(context.Context, workflow.RunID, int64, string) error
	ClearRunWaitingAsk(context.Context, workflow.RunID, int64, string) error
}

type QuestionAwaiter interface {
	AwaitPromptResponse(ctx context.Context, sessionID string, req askquestion.AskQuestionRequest) (askquestion.AskQuestionResponse, error)
}

type QuestionAttentionRegistry interface {
	PrepareTaskQuestionBatch(batch askquestion.AskQuestionBatchMetadata, sessionID string, target *clientui.AttentionNotificationTarget, preview string, occurredAt time.Time) error
	MarkTaskQuestionCleared(batch askquestion.AskQuestionBatchMetadata, askID string)
	MarkTaskQuestionSkipped(batch askquestion.AskQuestionBatchMetadata)
}

type ApprovalQuestionAttentionRegistry interface {
	MarkTaskApprovalQuestionCleared(target clientui.AttentionNotificationTarget, askID string)
}

type TaskQuestionRequest struct {
	SessionID  string
	RunID      workflow.RunID
	Generation int64
	Input      workflowstore.RunStartContext
	Question   askquestion.AskQuestionRequest
}

func HandleTaskQuestion(ctx context.Context, store QuestionStore, awaiter QuestionAwaiter, attention QuestionAttentionRegistry, req TaskQuestionRequest) (askquestion.AskQuestionResponse, error) {
	askReq := req.Question
	if askReq.Origin != askquestion.AskQuestionOriginModelTool || askReq.QuestionBatch == nil {
		return askquestion.AskQuestionResponse{}, fmt.Errorf("workflow task question missing batch metadata: operation=ask_question task_id=%s run_id=%s step_id=%s call_id=%s ask_id=%s approval=%t", req.Input.Task.ID, req.RunID, askReq.StepID, askReq.ToolCallID, askReq.ID, askReq.Approval)
	}
	askReq.AttentionTarget = TaskQuestionAttentionTarget(req.Input, req.SessionID, req.RunID, *askReq.QuestionBatch)
	if err := store.SetRunWaitingAsk(context.Background(), req.RunID, req.Generation, askReq.ID); err != nil {
		if attention != nil {
			if prepareErr := attention.PrepareTaskQuestionBatch(*askReq.QuestionBatch, req.SessionID, askReq.AttentionTarget, strings.TrimSpace(askReq.Question), time.Now().UTC()); prepareErr == nil {
				MarkTaskQuestionBatchSkipped(attention, *askReq.QuestionBatch, "")
			}
		}
		return askquestion.AskQuestionResponse{}, err
	}
	resp, askErr := awaiter.AwaitPromptResponse(ctx, req.SessionID, askReq)
	clearErr := store.ClearRunWaitingAsk(context.Background(), req.RunID, req.Generation, askReq.ID)
	if clearErr != nil {
		if taskQuestionAlreadyDurablyCleared(context.Background(), store, req.RunID, askReq.ID, clearErr, askErr, ctx.Err()) {
			if attention != nil {
				attention.MarkTaskQuestionCleared(*askReq.QuestionBatch, askReq.ID)
				MarkTaskQuestionBatchSkipped(attention, *askReq.QuestionBatch, askReq.ID)
			}
			return resp, askErr
		}
		if askErr == nil {
			return askquestion.AskQuestionResponse{}, clearErr
		}
		return resp, errors.Join(askErr, clearErr)
	}
	if attention != nil {
		attention.MarkTaskQuestionCleared(*askReq.QuestionBatch, askReq.ID)
		if ShouldSkipRemainingTaskQuestions(askErr, ctx.Err()) {
			MarkTaskQuestionBatchSkipped(attention, *askReq.QuestionBatch, askReq.ID)
		}
	}
	return resp, askErr
}

func HandleTaskApprovalQuestion(ctx context.Context, store QuestionStore, awaiter QuestionAwaiter, attention ApprovalQuestionAttentionRegistry, req TaskQuestionRequest) (askquestion.AskQuestionResponse, error) {
	askReq := req.Question
	if !askReq.Approval {
		return askquestion.AskQuestionResponse{}, fmt.Errorf("workflow task approval question requires approval prompt: task_id=%s run_id=%s ask_id=%s", req.Input.Task.ID, req.RunID, askReq.ID)
	}
	target := TaskApprovalQuestionAttentionTarget(req.Input, req.SessionID, req.RunID, askReq.ID)
	askReq.AttentionTarget = target
	if err := store.SetRunWaitingAsk(context.Background(), req.RunID, req.Generation, askReq.ID); err != nil {
		return askquestion.AskQuestionResponse{}, err
	}
	resp, askErr := awaiter.AwaitPromptResponse(ctx, req.SessionID, askReq)
	clearErr := store.ClearRunWaitingAsk(context.Background(), req.RunID, req.Generation, askReq.ID)
	if clearErr != nil {
		if askErr == nil {
			return askquestion.AskQuestionResponse{}, clearErr
		}
		return resp, errors.Join(askErr, clearErr)
	}
	if attention != nil {
		attention.MarkTaskApprovalQuestionCleared(*target, askReq.ID)
	}
	return resp, askErr
}

func PrepareSkippedTaskQuestionBatch(attention QuestionAttentionRegistry, input workflowstore.RunStartContext, sessionID string, runID workflow.RunID, batch askquestion.AskQuestionBatchMetadata, occurredAt time.Time) error {
	if attention == nil {
		return nil
	}
	target := TaskQuestionAttentionTarget(input, sessionID, runID, batch)
	err := attention.PrepareTaskQuestionBatch(batch, sessionID, target, "", occurredAt)
	MarkTaskQuestionBatchSkipped(attention, batch, "")
	return err
}

func ShouldSkipRemainingTaskQuestions(askErr error, ctxErr error) bool {
	return ctxErr != nil || errors.Is(askErr, context.Canceled) || errors.Is(askErr, io.EOF)
}

func MarkTaskQuestionBatchSkipped(attention QuestionAttentionRegistry, batch askquestion.AskQuestionBatchMetadata, materializedAskID string) {
	if attention == nil {
		return
	}
	for _, askID := range batch.BatchPromptIDs {
		if askID == "" || askID == materializedAskID {
			continue
		}
		skipped := batch
		skipped.BatchPromptIDs = append([]string(nil), batch.BatchPromptIDs...)
		skipped.PromptID = askID
		attention.MarkTaskQuestionSkipped(skipped)
	}
}

func TaskQuestionAttentionTarget(input workflowstore.RunStartContext, sessionID string, runID workflow.RunID, batch askquestion.AskQuestionBatchMetadata) *clientui.AttentionNotificationTarget {
	return &clientui.AttentionNotificationTarget{
		Kind:        clientui.AttentionNotificationTargetWorkflowTask,
		ProjectID:   strings.TrimSpace(input.Task.ProjectID),
		WorkflowID:  strings.TrimSpace(string(input.Task.WorkflowID)),
		TaskID:      strings.TrimSpace(string(input.Task.ID)),
		TaskShortID: strings.TrimSpace(input.Task.ShortID),
		TaskTitle:   strings.TrimSpace(input.Task.Title),
		SessionID:   strings.TrimSpace(sessionID),
		RunID:       strings.TrimSpace(string(runID)),
		Focus: &clientui.AttentionNotificationTaskDetailFocus{
			Kind:   clientui.AttentionNotificationFocusQuestion,
			AskIDs: append([]string(nil), batch.BatchPromptIDs...),
		},
	}
}

func TaskApprovalQuestionAttentionTarget(input workflowstore.RunStartContext, sessionID string, runID workflow.RunID, askID string) *clientui.AttentionNotificationTarget {
	return &clientui.AttentionNotificationTarget{
		Kind:        clientui.AttentionNotificationTargetWorkflowTask,
		ProjectID:   strings.TrimSpace(input.Task.ProjectID),
		WorkflowID:  strings.TrimSpace(string(input.Task.WorkflowID)),
		TaskID:      strings.TrimSpace(string(input.Task.ID)),
		TaskShortID: strings.TrimSpace(input.Task.ShortID),
		TaskTitle:   strings.TrimSpace(input.Task.Title),
		SessionID:   strings.TrimSpace(sessionID),
		RunID:       strings.TrimSpace(string(runID)),
		Focus: &clientui.AttentionNotificationTaskDetailFocus{
			Kind:   clientui.AttentionNotificationFocusQuestion,
			AskIDs: []string{strings.TrimSpace(askID)},
		},
	}
}

func taskQuestionAlreadyDurablyCleared(ctx context.Context, store QuestionStore, runID workflow.RunID, askID string, clearErr error, askErr error, ctxErr error) bool {
	if !errors.Is(clearErr, sql.ErrNoRows) {
		return false
	}
	if !ShouldSkipRemainingTaskQuestions(askErr, ctxErr) {
		return false
	}
	run, err := store.GetRun(ctx, runID)
	if err != nil {
		return false
	}
	return strings.TrimSpace(run.WaitingAskID) != strings.TrimSpace(askID)
}
