package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"core/server/llm"
	"core/server/tools"
	"core/server/workflowruntime"
	"core/shared/textutil"
	"core/shared/toolspec"

	"github.com/google/uuid"
)

type defaultToolExecutor struct {
	engine *Engine
}

type completedToolResult struct {
	result tools.Result
}

var ErrMissingProviderToolCallID = errors.New("provider tool call id is required")

func (t *defaultToolExecutor) ExecuteToolCalls(
	ctx context.Context,
	stepID string,
	preparedCalls []executorToolCall,
	collector *resultGroupCollector,
) ([]*completedToolResult, error) {
	e := t.engine
	results := make([]*completedToolResult, len(preparedCalls))
	callErrs := make([]error, len(preparedCalls))
	wg := sync.WaitGroup{}
	runID := activeRunIDForStep(e, stepID)
	workflowActive := e.currentNodeExecutionActive()
	serialGate := newSerialToolGate()
	nextSerialOrdinal := 0
	if collector == nil {
		return results, errors.New("tool execution requires a result group collector")
	}
	executionCtx, cancelExecution := context.WithCancel(ctx)
	defer cancelExecution()
	executionCtx = tools.WithEffectBarrier(
		executionCtx,
		t.resultGroupEffectBarrier(
			executionCtx,
			stepID,
			collector,
			cancelExecution,
		),
	)

	for i := range preparedCalls {
		prepared := preparedCalls[i]
		call := prepared.call
		toolID := prepared.toolID
		knownTool := prepared.knownTool
		executableCall := prepared.call
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
		go func(tc llm.ToolCall, toolID toolspec.ID, knownTool bool, serialOrdinal int, askBatch *tools.AskQuestionBatchMetadata) {
			defer wg.Done()
			defer e.forgetPendingToolCallStart(tc.ID)

			if serialOrdinal >= 0 {
				serialGate.wait(serialOrdinal)
				defer serialGate.done(serialOrdinal)
			}
			completed, callErr := t.executePreparedToolCall(executionCtx, stepID, runID, tc, toolID, knownTool, askBatch)
			if fatal := collector.fatalSnapshot(); fatal != nil {
				return
			}
			if completed == nil {
				callErrs[idx] = callErr
				return
			}
			res := completed.result
			outcome := resultGroupReportOutcome(0)
			if err := e.steer(stepID, steerResultGroupReportIntent(
				collector,
				tc.ID,
				resultGroupUnit{result: res},
				&outcome,
			)); err != nil {
				if fatal := collector.fatalSnapshot(); fatal != nil {
					return
				}
				callErrs[idx] = errors.Join(callErr, fmt.Errorf(
					"report tool result (call_id=%s tool=%s): %w",
					tc.ID,
					res.Name,
					err,
				))
				return
			}
			if fatal := collector.fatalSnapshot(); fatal != nil {
				return
			}
			if outcome != resultGroupReportAccepted {
				callErrs[idx] = fmt.Errorf(
					"result group ignored tool result without fatal (call_id=%s tool=%s)",
					tc.ID,
					res.Name,
				)
				return
			}
			results[idx] = completed
			callErrs[idx] = callErr
		}(executableCall, toolID, knownTool, serialOrdinal, prepared.askQuestionBatch)
	}

	wg.Wait()
	var joined error
	for _, err := range callErrs {
		joined = errors.Join(joined, err)
	}
	if joined != nil {
		return results, joined
	}
	return results, nil
}

func (t *defaultToolExecutor) resultGroupEffectBarrier(
	ctx context.Context,
	stepID string,
	collector *resultGroupCollector,
	cancel context.CancelFunc,
) tools.EffectBarrier {
	return func(reason tools.EffectBarrierReason) error {
		flushReason, err := resultGroupFlushReasonForEffect(reason)
		if err != nil {
			return err
		}
		return runResultGroupEffectBarrier(
			ctx,
			collector,
			cancel,
			func() error {
				return t.engine.steer(
					stepID,
					steerResultGroupFlushIntent(collector, flushReason),
				)
			},
		)
	}
}

func runResultGroupEffectBarrier(
	ctx context.Context,
	collector *resultGroupCollector,
	cancel context.CancelFunc,
	flush func() error,
) error {
	flushErr := flush()
	if fatal := collector.fatalSnapshot(); fatal != nil {
		cancel()
		return fatal
	}
	if flushErr != nil {
		return fmt.Errorf(
			"result group effect barrier failed without collector fatal: %w",
			flushErr,
		)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func resultGroupFlushReasonForEffect(
	reason tools.EffectBarrierReason,
) (ResultGroupFlushReason, error) {
	switch reason {
	case tools.EffectBarrierQuestion:
		return ResultGroupFlushQuestion, nil
	case tools.EffectBarrierApproval:
		return ResultGroupFlushApproval, nil
	case tools.EffectBarrierCompleteNode:
		return ResultGroupFlushCompleteNode, nil
	default:
		return 0, fmt.Errorf("unknown tool effect barrier reason %d", reason)
	}
}

func (t *defaultToolExecutor) executePreparedToolCall(
	ctx context.Context,
	stepID string,
	runID string,
	call llm.ToolCall,
	toolID toolspec.ID,
	knownTool bool,
	askBatch *tools.AskQuestionBatchMetadata,
) (*completedToolResult, error) {
	if !knownTool {
		return &completedToolResult{result: tools.Result{CallID: call.ID, Name: toolspec.ID(call.Name), IsError: true, Output: mustJSON(map[string]any{"error": "unknown tool"}), Summary: textutil.Value("unknown tool")}}, nil
	}
	if toolID == toolspec.ToolCompleteNode {
		return &completedToolResult{result: t.executeCompleteNodeTool(ctx, stepID, call)}, nil
	}
	if toolID == toolspec.ToolWebSearch {
		if err := tools.ValidateWebSearchInput(call.Input); err != nil {
			return &completedToolResult{result: tools.ErrorResult(tools.Call{ID: call.ID, Name: toolID, Input: call.Input, RunID: runID, StepID: stepID}, tools.InvalidWebSearchQueryMessage)}, nil
		}
	}
	handler, ok := t.engine.registry.Get(toolID)
	if !ok {
		return &completedToolResult{result: tools.Result{CallID: call.ID, Name: toolID, IsError: true, Output: mustJSON(map[string]any{"error": "unknown tool"}), Summary: textutil.Value("unknown tool")}}, nil
	}
	result, err := handler.Call(
		tools.WithExecutionIdentity(ctx, tools.ExecutionIdentity{RunID: runID, StepID: stepID}),
		tools.Call{ID: call.ID, Name: toolID, Input: call.Input, RunID: runID, StepID: stepID, AskQuestionBatch: askBatch, OnAskQuestionBatchSkipped: t.engine.cfg.AskQuestionBatchSkipped},
	)
	if err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return nil, err
		}
		result = tools.Result{CallID: call.ID, Name: toolID, IsError: true, Output: mustJSON(map[string]any{"error": err.Error()}), Summary: textutil.Value(err.Error())}
	}
	result.CallID = call.ID
	result.Name = toolID
	return &completedToolResult{result: tools.MaterializeModelWarnings(result)}, err
}

type executorToolCall struct {
	call             llm.ToolCall
	toolID           toolspec.ID
	knownTool        bool
	askQuestionBatch *tools.AskQuestionBatchMetadata
}

func prepareExecutorToolCalls(engine *Engine, stepID string, runID string, workflowActive bool, calls []llm.ToolCall) ([]executorToolCall, error) {
	prepared := make([]executorToolCall, 0, len(calls))
	askCandidateIndexes := make([]int, 0)
	askCandidatePromptIDs := make([]string, 0)
	for i := range calls {
		call := calls[i]
		if strings.TrimSpace(call.ID) == "" {
			return nil, fmt.Errorf("%w (tool=%s)", ErrMissingProviderToolCallID, call.Name)
		}
		toolID, knownTool := toolspec.ParseID(call.Name)
		executableCall := call
		if knownTool {
			executableCall.Name = string(toolID)
		}
		if call.Custom && knownTool {
			customInput, _ := textutil.OptionalExact(call.CustomInput)
			executableCall.Input = executorInputForCustomTool(toolID, customInput)
		}
		prepared = append(prepared, executorToolCall{call: executableCall, toolID: toolID, knownTool: knownTool})
		if !knownTool || toolID != toolspec.ToolAskQuestion || !askQuestionMaterializable(engine) {
			continue
		}
		if _, err := tools.PrepareAskQuestionToolRequest(executableCall.ID, executableCall.Input); err != nil {
			continue
		}
		askCandidateIndexes = append(askCandidateIndexes, len(prepared)-1)
		askCandidatePromptIDs = append(askCandidatePromptIDs, executableCall.ID)
	}
	if len(askCandidateIndexes) == 0 {
		return prepared, nil
	}
	batchID := uuid.NewString()
	for ordinal, index := range askCandidateIndexes {
		promptIDs := append([]string(nil), askCandidatePromptIDs...)
		call := prepared[index].call
		prepared[index].askQuestionBatch = &tools.AskQuestionBatchMetadata{
			Origin:              tools.AskQuestionOriginModelTool,
			RunID:               runID,
			StepID:              stepID,
			BatchID:             batchID,
			PromptID:            call.ID,
			BatchPromptIDs:      promptIDs,
			CandidateOrdinal:    ordinal,
			PreparedPromptCount: len(promptIDs),
		}
	}
	return prepared, nil
}

func askQuestionMaterializable(engine *Engine) bool {
	if engine == nil || engine.registry == nil {
		return false
	}
	handler, ok := engine.registry.Get(toolspec.ToolAskQuestion)
	if !ok {
		return false
	}
	questions, ok := handler.(interface{ QuestionsEnabled() bool })
	return !ok || questions.QuestionsEnabled()
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
	execution, active := e.currentNodeExecutionConfig()
	if !active || execution.Controller == nil {
		result.IsError = true
		result.Output = mustJSON(map[string]any{"error": "complete_node is only available during current-node execution"})
		result.Summary = textutil.Value("not in current-node execution")
		return result
	}
	parsed, err := workflowruntime.DecodeCompletion(call.Input, execution.Contract)
	if err != nil {
		return e.workflowCompletionRejectedResult(ctx, result, err)
	}
	if barrier, ok := tools.EffectBarrierFromContext(ctx); ok {
		if err := barrier(tools.EffectBarrierCompleteNode); err != nil {
			return tools.ErrorResult(tools.Call{
				ID:    call.ID,
				Name:  toolspec.ToolCompleteNode,
				Input: call.Input,
			}, err.Error())
		}
	}
	completed, err := e.completeWorkflowCurrentNode(ctx, parsed)
	if err != nil {
		return e.workflowCompletionRejectedResult(ctx, result, err)
	}
	e.recordWorkflowTerminalState(WorkflowCompletionSourceTool)
	result.Output = workflowruntime.ToolSuccessPayload(completed)
	result.Summary = textutil.Value("workflow node completed")
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
