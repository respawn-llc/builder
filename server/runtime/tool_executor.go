package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"core/server/llm"
	"core/server/tools"
	"core/server/workflowruntime"
	"core/shared/toolspec"

	"github.com/google/uuid"
)

type defaultToolExecutor struct {
	engine *Engine
}

func (t *defaultToolExecutor) ExecuteToolCalls(ctx context.Context, stepID string, calls []llm.ToolCall) ([]tools.Result, error) {
	e := t.engine
	results := make([]tools.Result, len(calls))
	callErrs := make([]error, len(calls))
	wg := sync.WaitGroup{}
	runID := activeRunIDForStep(e, stepID)
	workflowActive := e.workflowRunActive()
	serialGate := newSerialToolGate()
	nextSerialOrdinal := 0

	for i := range calls {
		call := calls[i]
		if call.ID == "" {
			call.ID = uuid.NewString()
		}
		toolID, knownTool := toolspec.ParseID(call.Name)
		executableCall := call
		if knownTool {
			executableCall.Name = string(toolID)
		}
		if call.Custom && knownTool {
			executableCall.Input = executorInputForCustomTool(toolID, call.CustomInput)
		}
		transcriptCall := normalizeToolCallForTranscript(executableCall, e.transcriptWorkingDir())
		started := Event{Kind: EventToolCallStarted, StepID: stepID, ToolCall: &transcriptCall, CommittedTranscriptChanged: true}
		if start, ok := e.pendingToolCallStart(call.ID); ok {
			started.CommittedEntryStart = start
			started.CommittedEntryStartSet = true
		}
		if err := e.steer(stepID, steerEventIntent(started)); err != nil {
			callErrs[i] = fmt.Errorf("persist tool started (call_id=%s tool=%s): %w", call.ID, executableCall.Name, err)
			continue
		}
		idx := i
		serialOrdinal := -1
		if serialToolExecutionRequired(toolID, workflowActive) {
			serialOrdinal = nextSerialOrdinal
			nextSerialOrdinal++
		}
		wg.Add(1)
		go func(tc llm.ToolCall, toolID toolspec.ID, knownTool bool, serialOrdinal int) {
			defer wg.Done()
			defer e.forgetPendingToolCallStart(tc.ID)
			var callErr error

			if serialOrdinal >= 0 {
				serialGate.wait(serialOrdinal)
				defer serialGate.done(serialOrdinal)
			}
			if !knownTool {
				results[idx] = tools.Result{CallID: tc.ID, Name: toolspec.ID(tc.Name), IsError: true, Output: mustJSON(map[string]any{"error": "unknown tool"}), Summary: "unknown tool"}
				if err := e.steer(stepID, steerToolCompletionIntent(results[idx])); err != nil {
					callErrs[idx] = fmt.Errorf("%w (call_id=%s tool=%s): %w", errPersistToolCompletion, tc.ID, results[idx].Name, err)
				}
				return
			}
			if toolID == toolspec.ToolCompleteNode {
				results[idx] = t.executeCompleteNodeTool(ctx, stepID, tc)
				if err := e.steer(stepID, steerToolCompletionIntent(results[idx])); err != nil {
					callErrs[idx] = fmt.Errorf("%w (call_id=%s tool=%s): %w", errPersistToolCompletion, tc.ID, results[idx].Name, err)
				}
				return
			}
			h, ok := e.registry.Get(toolID)
			if toolID == toolspec.ToolWebSearch {
				if err := tools.ValidateWebSearchInput(tc.Input); err != nil {
					results[idx] = tools.ErrorResult(tools.Call{ID: tc.ID, Name: toolID, Input: tc.Input, RunID: runID, StepID: stepID}, tools.InvalidWebSearchQueryMessage)
					if err := e.steer(stepID, steerToolCompletionIntent(results[idx])); err != nil {
						callErrs[idx] = fmt.Errorf("%w (call_id=%s tool=%s): %w", errPersistToolCompletion, tc.ID, results[idx].Name, err)
					}
					return
				}
			}
			if !ok {
				results[idx] = tools.Result{CallID: tc.ID, Name: toolID, IsError: true, Output: mustJSON(map[string]any{"error": "unknown tool"}), Summary: "unknown tool"}
				if err := e.steer(stepID, steerToolCompletionIntent(results[idx])); err != nil {
					callErrs[idx] = fmt.Errorf("%w (call_id=%s tool=%s): %w", errPersistToolCompletion, tc.ID, results[idx].Name, err)
				}
				return
			}
			res, err := h.Call(ctx, tools.Call{ID: tc.ID, Name: toolID, Input: tc.Input, RunID: runID, StepID: stepID})
			if err != nil {
				callErr = err
				res = tools.Result{CallID: tc.ID, Name: toolID, IsError: true, Output: mustJSON(map[string]any{"error": err.Error()}), Summary: err.Error()}
			}
			if res.Name == "" {
				res.Name = toolID
			}
			results[idx] = res
			if err := e.steer(stepID, steerToolCompletionIntent(res)); err != nil {
				persistErr := fmt.Errorf("%w (call_id=%s tool=%s): %w", errPersistToolCompletion, tc.ID, res.Name, err)
				callErrs[idx] = errors.Join(callErr, persistErr)
				return
			}
			callErrs[idx] = callErr
		}(executableCall, toolID, knownTool, serialOrdinal)
	}

	wg.Wait()
	var joined error
	for _, err := range callErrs {
		joined = errors.Join(joined, err)
	}
	joined = errors.Join(joined, e.drainActiveStepGoalMutations(stepID))
	if joined != nil {
		return results, joined
	}
	return results, nil
}

type serialToolGate struct {
	mu   sync.Mutex
	cond *sync.Cond
	next int
}

func newSerialToolGate() *serialToolGate {
	gate := &serialToolGate{}
	gate.cond = sync.NewCond(&gate.mu)
	return gate
}

func (g *serialToolGate) wait(ordinal int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for g.next != ordinal {
		g.cond.Wait()
	}
}

func (g *serialToolGate) done(ordinal int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.next == ordinal {
		g.next++
		g.cond.Broadcast()
	}
}

func serialToolExecutionRequired(toolID toolspec.ID, workflowActive bool) bool {
	switch toolID {
	case toolspec.ToolAskQuestion:
		return true
	case toolspec.ToolPatch, toolspec.ToolEdit, toolspec.ToolViewImage:
		return workflowActive
	default:
		return false
	}
}

func (t *defaultToolExecutor) executeCompleteNodeTool(ctx context.Context, stepID string, call llm.ToolCall) tools.Result {
	e := t.engine
	result := tools.Result{CallID: call.ID, Name: toolspec.ToolCompleteNode}
	if !e.workflowRunActive() || e.cfg.WorkflowRun.Controller == nil {
		result.IsError = true
		result.Output = mustJSON(map[string]any{"error": "complete_node is only available during a workflow run"})
		result.Summary = "not in workflow run"
		return result
	}
	parsed, err := workflowruntime.DecodeCompletion(call.Input, e.cfg.WorkflowRun.Contract)
	if err != nil {
		return e.workflowCompletionRejectedResult(ctx, result, err)
	}
	completed, err := e.cfg.WorkflowRun.Controller.CompleteWorkflowRun(ctx, workflowruntime.CompletionRequest{
		RunID:              e.cfg.WorkflowRun.Contract.RunID,
		ExpectedGeneration: e.cfg.WorkflowRun.Contract.ExpectedGeneration,
		RequireGeneration:  e.cfg.WorkflowRun.Contract.RequireGeneration,
		TransitionID:       parsed.TransitionID,
		OutputValues:       parsed.OutputValues,
		Commentary:         parsed.Commentary,
	})
	if err != nil {
		return e.workflowCompletionRejectedResult(ctx, result, err)
	}
	e.recordWorkflowTerminalState(WorkflowCompletionSourceTool)
	result.Output = workflowruntime.ToolSuccessPayload(completed)
	result.Summary = "workflow node completed"
	result.Terminal = true
	return result
}

func executorInputForCustomTool(toolID toolspec.ID, input string) json.RawMessage {
	switch toolID {
	case toolspec.ToolPatch:
		encoded, _ := json.Marshal(map[string]string{"patch": input})
		return encoded
	default:
		if json.Valid([]byte(input)) {
			return json.RawMessage(input)
		}
		encoded, _ := json.Marshal(input)
		return encoded
	}
}

func activeRunIDForStep(engine *Engine, stepID string) string {
	if engine == nil {
		return ""
	}
	snapshot := engine.ActiveRun()
	if snapshot == nil || snapshot.StepID != stepID {
		return ""
	}
	return snapshot.RunID
}
