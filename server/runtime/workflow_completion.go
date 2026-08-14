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

func (e *Engine) completeWorkflowCurrentNode(
	ctx context.Context,
	stepID string,
	parsed workflowruntime.ParsedCompletion,
) (workflowruntime.CompletionOutcome, error) {
	execution, active := e.currentNodeExecutionConfig()
	if !active || execution.Controller == nil {
		return workflowruntime.CompletionOutcome{}, errors.New("current node execution is unavailable")
	}
	sessionID, err := e.workflowSessionID()
	if err != nil {
		return workflowruntime.CompletionOutcome{}, err
	}
	run := e.ActiveRun()
	if run == nil || run.StepID != stepID {
		return workflowruntime.CompletionOutcome{}, ErrActiveStepInactive
	}
	runID, err := runtimeids.ParseRunID(run.RunID)
	if err != nil {
		return workflowruntime.CompletionOutcome{}, fmt.Errorf("active Workflow run identity: %w", err)
	}
	parsedStepID, err := runtimeids.ParseStepID(stepID)
	if err != nil {
		return workflowruntime.CompletionOutcome{}, fmt.Errorf("active Workflow step identity: %w", err)
	}
	return execution.Controller.CompleteAgentCurrentNode(ctx, workflowruntime.AgentCompletionRequest{
		Provenance: workflowruntime.AgentCompletionProvenance{
			ScopeID: execution.ScopeID,
			RunID:   runID,
			StepID:  parsedStepID,
		},
		SessionID:    sessionID,
		TransitionID: parsed.TransitionID,
		OutputValues: parsed.OutputValues,
		Commentary:   parsed.Commentary,
	})
}

func (e *Engine) ApplyWorkflowAgentCompletion(
	scopeID runtimeids.ExecutionScopeID,
	runID runtimeids.RunID,
	stepID runtimeids.StepID,
	commit func() (workflowruntime.CompletionDecision, error),
) (workflowruntime.CompletionDecision, error) {
	if e == nil || commit == nil {
		return workflowruntime.CompletionDecision{}, errors.New("Workflow Agent completion authority is unavailable")
	}
	execution, active := e.currentNodeExecutionConfig()
	if !active || execution.ScopeID != scopeID {
		return workflowruntime.CompletionDecision{}, ErrActiveStepInactive
	}
	snapshot := e.ActiveRun()
	if snapshot == nil || snapshot.RunID != runID.String() || snapshot.StepID != stepID.String() {
		return workflowruntime.CompletionDecision{}, ErrActiveStepInactive
	}
	var decision workflowruntime.CompletionDecision
	err := e.ApplyForActiveStep(stepID.String(), func() error {
		var err error
		decision, err = commit()
		if err != nil {
			return err
		}
		if decision.Accepted == nil {
			return errors.New("committed Workflow completion returned no accepted outcome")
		}
		recorded, err := e.recordWorkflowTerminalState(
			workflowCompletionSource(execution.CompletionMode),
			*decision.Accepted,
		)
		if err != nil {
			return err
		}
		if !recorded {
			return ErrActiveStepInactive
		}
		return nil
	})
	return decision, err
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
