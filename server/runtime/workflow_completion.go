package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"core/prompts"
	"core/server/llm"
	"core/server/tools"
	"core/server/workflowruntime"
	"core/shared/toolspec"
)

const (
	// This is not a config fallback. Runtime config validation should reject
	// invalid caps; if an invalid value reaches the runtime anyway, fail closed
	// by recording one durable violation and interrupting immediately.
	workflowInvalidCompletionFailClosedMaxCount = 1
)

var workflowFinalAnswerNudge = strings.TrimSpace(prompts.WorkflowFinalAnswerNudgePrompt)

func (e *Engine) workflowCompletionRejectedResult(ctx context.Context, result tools.Result, completionErr error) tools.Result {
	record, err := e.recordWorkflowProtocolViolation(ctx, workflowruntime.ViolationKindInvalidCompletion, completionErr.Error())
	result.IsError = true
	result.Output = workflowruntime.ToolErrorPayload(completionErr)
	result.Summary = "workflow completion rejected"
	if err != nil {
		result.Output = mustJSON(map[string]any{"error": err.Error()})
		result.Summary = err.Error()
	}
	if record.Interrupted {
		result.Terminal = true
	}
	return result
}

func (e *Engine) recordWorkflowProtocolViolation(ctx context.Context, kind workflowruntime.ViolationKind, detail string) (workflowruntime.ViolationResult, error) {
	if !e.workflowRunActive() || e.cfg.WorkflowRun.Controller == nil {
		return workflowruntime.ViolationResult{}, nil
	}
	maxCount := e.cfg.WorkflowRun.MaxInvalidCompletionAttempts
	if maxCount <= 0 {
		maxCount = workflowInvalidCompletionFailClosedMaxCount
	}
	payload, _ := json.Marshal(map[string]any{
		"kind":   string(kind),
		"detail": strings.TrimSpace(detail),
	})
	return e.cfg.WorkflowRun.Controller.RecordWorkflowProtocolViolation(ctx, workflowruntime.ViolationRequest{
		RunID:              e.cfg.WorkflowRun.Contract.RunID,
		Kind:               kind,
		MaxCount:           maxCount,
		Detail:             string(payload),
		ExpectedGeneration: e.cfg.WorkflowRun.Contract.ExpectedGeneration,
		RequireGeneration:  e.cfg.WorkflowRun.Contract.RequireGeneration,
	})
}

func (e *Engine) observeWorkflowDurableCompletion(ctx context.Context) (bool, error) {
	if !e.workflowRunActive() || e.cfg.WorkflowRun.Controller == nil {
		return false, nil
	}
	result, err := e.cfg.WorkflowRun.Controller.ObserveWorkflowRunCompletion(ctx, workflowruntime.CompletionObservationRequest{
		RunID:              e.cfg.WorkflowRun.Contract.RunID,
		ExpectedGeneration: e.cfg.WorkflowRun.Contract.ExpectedGeneration,
		RequireGeneration:  e.cfg.WorkflowRun.Contract.RequireGeneration,
	})
	if err != nil {
		return false, err
	}
	if result.Completed {
		e.recordWorkflowTerminalState(WorkflowCompletionSourceObserved)
	}
	return result.Completed, nil
}

func workflowCompletionCallCount(calls []llm.ToolCall) int {
	count := 0
	for _, call := range calls {
		id, ok := toolspec.ParseID(call.Name)
		if ok && id == toolspec.ToolCompleteNode {
			count++
		}
	}
	return count
}

func hasWorkflowTerminalResult(results []tools.Result) bool {
	for _, result := range results {
		if result.Name == toolspec.ToolCompleteNode && result.Terminal {
			return true
		}
	}
	return false
}

func workflowPreflightError(workflowActive bool, localToolCalls []llm.ToolCall, hostedToolExecutions []hostedToolExecution) error {
	if !workflowActive {
		return nil
	}
	count := workflowCompletionCallCount(localToolCalls)
	if count == 0 {
		return nil
	}
	if count != 1 {
		return fmt.Errorf("complete_node must be called exactly once")
	}
	return nil
}
