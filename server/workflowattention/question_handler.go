package workflowattention

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	askquestion "core/server/tools"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/clientui"
)

type QuestionAwaiter interface {
	AwaitPromptResolution(context.Context, string, askquestion.AskQuestionRequest) (askquestion.AskQuestionResolution, error)
}

type QuestionAttentionRegistry interface {
	PrepareTaskQuestionBatch(batch askquestion.AskQuestionBatchMetadata, sessionID string, target *clientui.AttentionNotificationTarget, preview string, occurredAt time.Time) error
	MarkTaskQuestionCleared(batch askquestion.AskQuestionBatchMetadata, askID string)
	MarkTaskQuestionSkipped(batch askquestion.AskQuestionBatchMetadata)
}

type ApprovalQuestionAttentionRegistry interface {
	MarkTaskApprovalQuestionCleared(target clientui.AttentionNotificationTarget, askID string)
}

// TaskQuestionContext contains durable Task facts plus the exact Current Node
// that owns a volatile prompt. It contains no persisted execution identity.
type TaskQuestionContext struct {
	Task        workflowstore.TaskRecord
	CurrentNode workflow.CurrentNodeReference
	SessionID   string
}

func (c TaskQuestionContext) validate() error {
	if strings.TrimSpace(string(c.Task.ID)) == "" {
		return errors.New("workflow task id is required")
	}
	if err := c.CurrentNode.Validate(); err != nil {
		return fmt.Errorf("workflow question current node: %w", err)
	}
	if c.CurrentNode.TaskID != c.Task.ID {
		return errors.New("workflow question current node does not belong to task")
	}
	if strings.TrimSpace(c.SessionID) == "" {
		return errors.New("workflow question session id is required")
	}
	return nil
}

type TaskQuestionRequest struct {
	Context  TaskQuestionContext
	Question askquestion.AskQuestionRequest
}

// HandleTaskQuestion owns only volatile prompt waiting and attention
// lifecycle. It must never persist a waiting-question marker.
func HandleTaskQuestion(ctx context.Context, awaiter QuestionAwaiter, attention QuestionAttentionRegistry, req TaskQuestionRequest) (askquestion.AskQuestionResolution, error) {
	if err := req.Context.validate(); err != nil {
		return nil, err
	}
	if awaiter == nil {
		return nil, errors.New("workflow question awaiter is required")
	}
	askReq := req.Question
	if askReq.Origin != askquestion.AskQuestionOriginModelTool || askReq.QuestionBatch == nil {
		return nil, fmt.Errorf("workflow task question missing batch metadata: operation=ask_question task_id=%s node_id=%s step_id=%s call_id=%s ask_id=%s approval=%t", req.Context.Task.ID, req.Context.CurrentNode.NodeID, askReq.StepID, askReq.ToolCallID, askReq.ID, askReq.Approval)
	}
	askReq.AttentionTarget = TaskQuestionAttentionTarget(req.Context, *askReq.QuestionBatch)
	resolution, askErr := awaiter.AwaitPromptResolution(ctx, req.Context.SessionID, askReq)
	if attention != nil {
		attention.MarkTaskQuestionCleared(*askReq.QuestionBatch, askReq.ID)
		if askquestion.ShouldSkipRemainingQuestionBatch(askErr, context.Cause(ctx)) {
			MarkTaskQuestionBatchSkipped(attention, *askReq.QuestionBatch, askReq.ID)
		}
	}
	return resolution, askErr
}

func HandleTaskApprovalQuestion(ctx context.Context, awaiter QuestionAwaiter, attention ApprovalQuestionAttentionRegistry, req TaskQuestionRequest) (askquestion.AskQuestionResolution, error) {
	if err := req.Context.validate(); err != nil {
		return nil, err
	}
	if awaiter == nil {
		return nil, errors.New("workflow approval question awaiter is required")
	}
	askReq := req.Question
	if !askReq.Approval {
		return nil, fmt.Errorf("workflow task approval question requires approval prompt: task_id=%s node_id=%s ask_id=%s", req.Context.Task.ID, req.Context.CurrentNode.NodeID, askReq.ID)
	}
	target := TaskApprovalQuestionAttentionTarget(req.Context, askReq.ID)
	askReq.AttentionTarget = target
	resolution, askErr := awaiter.AwaitPromptResolution(ctx, req.Context.SessionID, askReq)
	if attention != nil {
		attention.MarkTaskApprovalQuestionCleared(*target, askReq.ID)
	}
	return resolution, askErr
}

func PrepareSkippedTaskQuestionBatch(attention QuestionAttentionRegistry, context TaskQuestionContext, batch askquestion.AskQuestionBatchMetadata, occurredAt time.Time) error {
	if attention == nil {
		return nil
	}
	if err := context.validate(); err != nil {
		return err
	}
	target := TaskQuestionAttentionTarget(context, batch)
	err := attention.PrepareTaskQuestionBatch(batch, context.SessionID, target, "", occurredAt)
	MarkTaskQuestionBatchSkipped(attention, batch, "")
	return err
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

func TaskQuestionAttentionTarget(context TaskQuestionContext, batch askquestion.AskQuestionBatchMetadata) *clientui.AttentionNotificationTarget {
	return taskQuestionAttentionTarget(context, clientui.AttentionNotificationFocusQuestion, append([]string(nil), batch.BatchPromptIDs...))
}

func TaskApprovalQuestionAttentionTarget(context TaskQuestionContext, askID string) *clientui.AttentionNotificationTarget {
	return taskQuestionAttentionTarget(context, clientui.AttentionNotificationFocusQuestion, []string{strings.TrimSpace(askID)})
}

func taskQuestionAttentionTarget(context TaskQuestionContext, focusKind clientui.AttentionNotificationFocusKind, askIDs []string) *clientui.AttentionNotificationTarget {
	nodeID := string(context.CurrentNode.NodeID)
	workflowID := context.Task.WorkflowID
	target := &clientui.AttentionNotificationTarget{
		Kind:          clientui.AttentionNotificationTargetWorkflowTask,
		ProjectID:     strings.TrimSpace(context.Task.ProjectID),
		WorkflowID:    &workflowID,
		TaskID:        strings.TrimSpace(string(context.Task.ID)),
		TaskShortID:   strings.TrimSpace(context.Task.ShortID),
		TaskTitle:     strings.TrimSpace(context.Task.Title),
		SessionID:     strings.TrimSpace(context.SessionID),
		CurrentNodeID: &nodeID,
		Focus: &clientui.AttentionNotificationTaskDetailFocus{
			Kind:   focusKind,
			AskIDs: askIDs,
		},
	}
	if branchKey, branchScoped := context.CurrentNode.TransitionBranchKey(); branchScoped {
		value := string(branchKey)
		target.CurrentNodeBranchKey = &value
	}
	return target
}
