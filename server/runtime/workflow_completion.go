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
	"core/shared/textutil"
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
	result.Summary = textutil.Value("workflow completion rejected")
	if err != nil {
		result.Output = mustJSON(map[string]any{"error": err.Error()})
		result.Summary = textutil.Value(err.Error())
	}
	if record.Interrupted {
		result.Terminal = true
	}
	return result
}

func (e *Engine) recordWorkflowProtocolViolation(ctx context.Context, kind workflowruntime.ViolationKind, detail string) (workflowruntime.ViolationResult, error) {
	execution, active := e.currentNodeExecutionConfig()
	if !active || execution.Controller == nil {
		return workflowruntime.ViolationResult{}, nil
	}
	maxCount := execution.MaxInvalidCompletionAttempts
	if maxCount <= 0 {
		maxCount = workflowInvalidCompletionFailClosedMaxCount
	}
	payload, _ := json.Marshal(map[string]any{
		"kind":   string(kind),
		"detail": strings.TrimSpace(detail),
	})
	return execution.Controller.RecordProtocolViolation(ctx, workflowruntime.ViolationRequest{
		ScopeID:  execution.ScopeID,
		Kind:     kind,
		MaxCount: maxCount,
		Detail:   string(payload),
	})
}

func (e *Engine) resetWorkflowProtocolViolationBudget(ctx context.Context) error {
	execution, active := e.currentNodeExecutionConfig()
	if !active || execution.Controller == nil {
		return nil
	}
	return execution.Controller.ResetProtocolViolationBudget(ctx, workflowruntime.ViolationResetRequest{
		ScopeID: execution.ScopeID,
	})
}

func (e *Engine) observeWorkflowDurableCompletion(ctx context.Context) (bool, error) {
	execution, active := e.currentNodeExecutionConfig()
	if !active || execution.Controller == nil {
		return false, nil
	}
	result, err := execution.Controller.ObserveCurrentNodeCompletion(ctx, workflowruntime.CompletionObservationRequest{
		ScopeID: execution.ScopeID,
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
