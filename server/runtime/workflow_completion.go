package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/prompts"
	"core/server/llm"
	"core/server/tools"
	"core/server/workflowruntime"
	"core/shared/runtimeids"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"
)

const (
	// This is not a config fallback. Runtime config validation should reject
	// invalid caps; if an invalid value reaches the runtime anyway, fail closed
	// by recording one durable violation and interrupting immediately.
	workflowInvalidCompletionFailClosedMaxCount = 1
)

func workflowCompletionOperatorDiagnostic(
	diagnostic error,
	afterToolCallID *string,
) storedLocalEntry {
	if diagnostic == nil {
		panic("workflow completion operator diagnostic requires an error")
	}
	return storedLocalEntry{
		Visibility:      transcript.EntryVisibilityAuto,
		Role:            string(transcript.EntryRoleDeveloperErrorFeedback),
		Text:            diagnostic.Error(),
		AfterToolCallID: textutil.Pointer(afterToolCallID),
	}
}

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
	sessionID, err := e.workflowSessionID()
	if err != nil {
		return workflowruntime.ViolationResult{}, err
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
		ScopeID:   execution.ScopeID,
		SessionID: &sessionID,
		Kind:      kind,
		MaxCount:  maxCount,
		Detail:    string(payload),
	})
}

func (e *Engine) resetWorkflowProtocolViolationBudget(ctx context.Context) error {
	execution, active := e.currentNodeExecutionConfig()
	if !active || execution.Controller == nil {
		return nil
	}
	sessionID, err := e.workflowSessionID()
	if err != nil {
		return err
	}
	return execution.Controller.ResetProtocolViolationBudget(ctx, workflowruntime.ViolationResetRequest{
		ScopeID:   execution.ScopeID,
		SessionID: &sessionID,
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

func (e *Engine) completeWorkflowCurrentNode(
	ctx context.Context,
	parsed workflowruntime.ParsedCompletion,
) (workflowruntime.CompletionResult, error) {
	execution, active := e.currentNodeExecutionConfig()
	if !active || execution.Controller == nil {
		return workflowruntime.CompletionResult{}, errors.New("current node execution is unavailable")
	}
	sessionID, err := e.workflowSessionID()
	if err != nil {
		return workflowruntime.CompletionResult{}, err
	}
	return execution.Controller.CompleteCurrentNode(ctx, workflowruntime.CompletionRequest{
		ScopeID:      execution.ScopeID,
		SessionID:    &sessionID,
		TransitionID: parsed.TransitionID,
		OutputValues: parsed.OutputValues,
		Commentary:   parsed.Commentary,
	})
}

func (e *Engine) workflowSessionID() (runtimeids.SessionID, error) {
	sessionID, err := runtimeids.ParseSessionID(e.SessionID())
	if err != nil {
		return runtimeids.SessionID{}, fmt.Errorf("parse workflow Session identity: %w", err)
	}
	return sessionID, nil
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
