package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/server/workflowruntime"
	compactionutil "core/shared/config"
	"core/shared/jsoncontract"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"
)

type requestAssembly struct {
	request llm.Request
}

type dispatchRequestIdentity struct {
	SessionID            string
	RunID                string
	CompactionGeneration int
	RequestKind          *llm.CodexRequestKind
}

type dispatchRequestFactory struct {
	identity dispatchRequestIdentity
}

func (e *Engine) buildRequest(ctx context.Context, stepID string, allowTools bool) (llm.Request, error) {
	return e.buildContextFreeRequest(ctx, stepID, nil, allowTools)
}

func (e *Engine) buildContextFreeRequest(ctx context.Context, stepID string, extra []llm.ResponseItem, allowTools bool) (llm.Request, error) {
	assembly, err := e.assembleRequest(ctx, stepID, extra, allowTools, true)
	if err != nil {
		return llm.Request{}, err
	}
	return assembly.contextFree(), nil
}

func (e *Engine) buildContextFreeRequestWithoutPromptRefresh(ctx context.Context, allowTools bool) (llm.Request, error) {
	assembly, err := e.assembleRequest(ctx, "", nil, allowTools, false)
	if err != nil {
		return llm.Request{}, err
	}
	return assembly.contextFree(), nil
}

func (e *Engine) buildRequestWithExtraItems(ctx context.Context, stepID string, extra []llm.ResponseItem, allowTools bool) (llm.Request, error) {
	return e.buildContextFreeRequest(ctx, stepID, extra, allowTools)
}

func (e *Engine) buildDispatchRequest(ctx context.Context, stepID string, extra []llm.ResponseItem, allowTools bool, identity dispatchRequestIdentity) (llm.Request, error) {
	factory, err := newDispatchRequestFactory(identity)
	if err != nil {
		return llm.Request{}, err
	}
	assembly, err := e.assembleRequest(ctx, stepID, extra, allowTools, true)
	if err != nil {
		return llm.Request{}, err
	}
	return factory.generation(assembly.request)
}

func (e *Engine) buildActiveTurnDispatchRequest(ctx context.Context, stepID string, extra []llm.ResponseItem, allowTools bool) (llm.Request, error) {
	factory, err := e.activeDispatchRequestFactory(stepID, llm.CodexRequestKindTurn.Optional())
	if err != nil {
		return llm.Request{}, err
	}
	assembly, err := e.assembleRequest(ctx, stepID, extra, allowTools, true)
	if err != nil {
		return llm.Request{}, err
	}
	return factory.generation(assembly.request)
}

func (e *Engine) assembleRequest(ctx context.Context, stepID string, extra []llm.ResponseItem, allowTools bool, refreshPrompt bool) (requestAssembly, error) {
	locked, err := e.ensureLocked()
	if err != nil {
		return requestAssembly{}, err
	}
	if _, err := e.lockedRequestShape(); err != nil {
		return requestAssembly{}, err
	}
	if refreshPrompt {
		locked, err = e.ensureMainPromptFacingContractFresh(ctx, locked)
		if err != nil {
			return requestAssembly{}, err
		}
	}

	var workflowMode workflowruntime.CompletionMode
	if e.workflowPromptActive() {
		resolved, modeErr := e.workflowCompletionMode(ctx)
		if modeErr != nil {
			return requestAssembly{}, modeErr
		}
		workflowMode = resolved
	}
	var requestTools []llm.Tool
	if allowTools {
		requestTools, err = e.requestTools(ctx, workflowMode)
		if err != nil {
			return requestAssembly{}, err
		}
	} else {
		requestTools = []llm.Tool{}
	}

	items := e.transcriptRuntimeState().SnapshotItems()
	if len(extra) > 0 {
		items = append(items, llm.CloneResponseItems(extra)...)
	}
	systemPrompt := ""
	if refreshPrompt {
		systemPrompt, err = e.systemPrompt(locked)
	} else {
		systemPrompt, err = e.systemPromptWithoutBackfill(locked)
	}
	if err != nil {
		return requestAssembly{}, err
	}
	nativeWebSearch := false
	if allowTools {
		nativeWebSearch, err = e.enableNativeWebSearch(ctx)
		if err != nil {
			return requestAssembly{}, err
		}
	}
	toolChoiceMode := llm.ToolChoiceModeAutomatic
	if allowTools {
		toolChoiceMode = toolChoiceModeForWorkflowCompletion(
			workflowMode,
			e.currentNodeExecutionActive() && e.workflowUseRequiredToolCalls(),
		)
	}
	req, err := llm.RequestFromLockedContract(locked, systemPrompt, items, requestTools, llm.ToolControls{
		ChoiceMode:            toolChoiceMode,
		EnableNativeWebSearch: nativeWebSearch,
	})
	if err != nil {
		return requestAssembly{}, err
	}
	req.ReasoningEffort = e.ThinkingLevel()
	req.FastMode = e.FastModeEnabled()
	if e.supportsPromptCacheKey(ctx) {
		if cacheKey := e.conversationPromptCacheKey(e.SessionID()); cacheKey != "" {
			req.PromptCacheKey = cacheKey
			req.PromptCacheScope = transcript.CacheWarningScopeConversation
		}
	}
	if workflowMode != "" {
		if workflowMode == workflowruntime.CompletionModeStructuredOutput {
			contract, contractErr := e.workflowCompletionContract()
			if contractErr != nil {
				return requestAssembly{}, contractErr
			}
			output, outputErr := workflowruntime.StructuredOutput(contract)
			if outputErr != nil {
				return requestAssembly{}, outputErr
			}
			req.StructuredOutput = output
		}
		if err := req.Validate(); err != nil {
			return requestAssembly{}, err
		}
	}
	if err := e.validateToolChoiceSupport(ctx, toolChoiceMode); err != nil {
		return requestAssembly{}, err
	}
	return requestAssembly{request: req}, nil
}

func (a requestAssembly) contextFree() llm.Request {
	request := a.request
	request.SessionID = nil
	request.CodexDispatch = nil
	return request
}

func newDispatchRequestFactory(identity dispatchRequestIdentity) (dispatchRequestFactory, error) {
	if _, err := identity.newDispatchContext(); err != nil {
		return dispatchRequestFactory{}, err
	}
	return dispatchRequestFactory{identity: identity}, nil
}

func (e *Engine) activeDispatchRequestFactory(stepID string, requestKind *llm.CodexRequestKind) (dispatchRequestFactory, error) {
	runID := activeRunIDForStep(e, stepID)
	if runID == "" {
		return dispatchRequestFactory{}, fmt.Errorf("%w: active Run identity is required for dispatch", llm.ErrInvalidRequest)
	}
	return newDispatchRequestFactory(dispatchRequestIdentity{
		SessionID:            e.SessionID(),
		RunID:                runID,
		CompactionGeneration: e.compactionRuntimeState().Count(),
		RequestKind:          requestKind,
	})
}

func (f dispatchRequestFactory) generation(base llm.Request) (llm.Request, error) {
	dispatch, err := f.identity.newDispatchContext()
	if err != nil {
		return llm.Request{}, err
	}
	request := base
	request.SessionID = textutil.Value(f.identity.SessionID)
	request.CodexDispatch = dispatch
	if err := request.Validate(); err != nil {
		return llm.Request{}, err
	}
	return request, nil
}

func (f dispatchRequestFactory) compaction(base llm.CompactionRequest) (llm.CompactionRequest, error) {
	dispatch, err := f.identity.newDispatchContext()
	if err != nil {
		return llm.CompactionRequest{}, err
	}
	request := base
	request.SessionID = textutil.Value(f.identity.SessionID)
	request.CodexDispatch = dispatch
	return request, nil
}

func (i dispatchRequestIdentity) newDispatchContext() (*llm.CodexDispatchContext, error) {
	return llm.NewCodexDispatchContext(llm.CodexDispatchFacts{
		SessionID:            i.SessionID,
		RunID:                i.RunID,
		CompactionGeneration: i.CompactionGeneration,
		RequestKind:          i.RequestKind,
	})
}

func toolChoiceModeForWorkflowCompletion(mode workflowruntime.CompletionMode, useRequiredToolCalls bool) llm.ToolChoiceMode {
	if !useRequiredToolCalls {
		return llm.ToolChoiceModeAutomatic
	}
	switch mode {
	case workflowruntime.CompletionModeShellCommand, workflowruntime.CompletionModeTool:
		return llm.ToolChoiceModeRequired
	default:
		return llm.ToolChoiceModeAutomatic
	}
}

func (e *Engine) workflowUseRequiredToolCalls() bool {
	prompt, configured := e.workflowPrompt()
	return configured && !prompt.UseAutomaticToolChoice
}

func (e *Engine) validateToolChoiceSupport(ctx context.Context, mode llm.ToolChoiceMode) error {
	if mode != llm.ToolChoiceModeRequired {
		return nil
	}
	capabilities, err := e.providerCapabilities(ctx)
	if err != nil {
		return fmt.Errorf("resolve provider capabilities for required tool choice: %w", err)
	}
	return llm.ValidateToolChoiceSupport(capabilities, mode)
}

func (e *Engine) supportsPromptCacheKey(ctx context.Context) bool {
	caps, err := e.providerCapabilities(ctx)
	if err != nil {
		return false
	}
	return llm.SupportsPromptCacheKeyProvider(caps)
}

func supportsPromptCacheKeyForClient(ctx context.Context, client *observedModelClient) bool {
	if client == nil {
		return false
	}
	caps, err := client.capabilities(ctx)
	if err != nil {
		return false
	}
	return llm.SupportsPromptCacheKeyProvider(caps)
}

func (e *Engine) enableNativeWebSearch(ctx context.Context) (bool, error) {
	shape, err := e.lockedRequestShape()
	if err != nil {
		return false, err
	}
	if !tools.NeedsNativeWebSearch(shape.EnabledTools, shape.WebSearchMode) {
		return false, nil
	}
	caps, err := e.providerCapabilities(ctx)
	if err != nil {
		return false, fmt.Errorf("resolve provider capabilities for native web search: %w", err)
	}
	return caps.SupportsNativeWebSearch, nil
}

func (e *Engine) currentNodeExecutionActive() bool {
	_, active := e.currentNodeExecutionConfig()
	return active
}

func (e *Engine) workflowPromptActive() bool {
	_, configured := e.workflowPrompt()
	return configured
}

func (e *Engine) workflowPrompt() (*workflowruntime.PromptContract, bool) {
	if e == nil {
		return nil, false
	}
	if e.cfg.WorkflowPrompt != nil {
		return e.cfg.WorkflowPrompt, true
	}
	execution, active := e.currentNodeExecutionConfig()
	if !active {
		return nil, false
	}
	return &workflowruntime.PromptContract{
		Identity:               workflowruntime.CurrentNodePromptIdentity(execution.Instructions.CurrentNode),
		CompletionMode:         execution.CompletionMode,
		UseAutomaticToolChoice: execution.UseAutomaticToolChoice,
		Instructions:           execution.Instructions,
		Transitions:            append([]workflowruntime.CompletionTransition(nil), execution.Contract.Transitions...),
		TaskAwareness:          workflowruntime.TaskAwareness{},
	}, true
}

func newWorkflowPromptCompletionContract(
	prompt *workflowruntime.PromptContract,
) (workflowruntime.CompletionContract, error) {
	if prompt == nil {
		return workflowruntime.CompletionContract{}, errors.New("workflow prompt is unavailable")
	}
	contract := workflowruntime.CompletionContract{
		Transitions: append([]workflowruntime.CompletionTransition(nil), prompt.Transitions...),
	}
	if _, err := workflowruntime.CompletionJSONSchema(contract); err != nil {
		return workflowruntime.CompletionContract{}, err
	}
	return contract, nil
}

func (e *Engine) workflowCompletionContract() (workflowruntime.CompletionContract, error) {
	if execution, active := e.currentNodeExecutionConfig(); active {
		return execution.Contract, nil
	}
	if e != nil && e.workflowPromptContract != nil {
		return *e.workflowPromptContract, nil
	}
	return workflowruntime.CompletionContract{}, errors.New("workflow completion contract is unavailable")
}

func (e *Engine) CurrentNodeExecutionConfigured() bool {
	return e.currentNodeExecutionActive()
}

func (e *Engine) workflowCompletionMode(ctx context.Context) (workflowruntime.CompletionMode, error) {
	prompt, configured := e.workflowPrompt()
	if !configured {
		return "", nil
	}
	promptMode, err := workflowruntime.ParseCompletionMode(string(prompt.CompletionMode))
	if err != nil {
		return "", err
	}
	locked, lockedConfigured := e.lockedContractState().Snapshot()
	if !lockedConfigured || locked.WorkflowCompletionMode == nil {
		return promptMode, nil
	}
	lockedMode, err := workflowruntime.ParseCompletionMode(string(*locked.WorkflowCompletionMode))
	if err != nil {
		return "", fmt.Errorf("parse Session-locked workflow completion mode: %w", err)
	}
	if lockedMode != promptMode {
		return "", fmt.Errorf(
			"workflow completion mode invariant violated: Session contract has %q while live execution has %q",
			lockedMode,
			promptMode,
		)
	}
	return lockedMode, nil
}

func (e *Engine) systemPrompt(locked session.LockedContract) (string, error) {
	if locked.HasSystemPrompt {
		return strings.TrimSpace(locked.SystemPrompt), nil
	}
	if prompt := strings.TrimSpace(locked.SystemPrompt); prompt != "" {
		return prompt, nil
	}
	prompt, err := e.buildSystemPromptSnapshotForRoot(locked, e.systemPromptWorkspaceRoot())
	if err != nil {
		return "", err
	}
	if err := e.store.BackfillLockedSystemPrompt(prompt); err != nil {
		return "", err
	}
	if meta := e.store.Meta(); meta.Locked != nil && meta.Locked.HasSystemPrompt {
		persisted := strings.TrimSpace(meta.Locked.SystemPrompt)
		prompt = persisted
	}
	e.lockedContractState().FillSystemPrompt(prompt)
	return prompt, nil
}

func (e *Engine) systemPromptWithoutBackfill(locked session.LockedContract) (string, error) {
	if locked.HasSystemPrompt {
		return strings.TrimSpace(locked.SystemPrompt), nil
	}
	if prompt := strings.TrimSpace(locked.SystemPrompt); prompt != "" {
		return prompt, nil
	}
	return e.buildSystemPromptSnapshotForRoot(locked, e.systemPromptWorkspaceRoot())
}

func (e *Engine) estimatedToolCallsForLockedContext(locked session.LockedContract) int {
	budget := e.promptContextBudget(locked)
	return compactionutil.EstimatedToolCallsForContextWindow(budget.window, budget.percent)
}

type promptContextBudget struct {
	window  int
	percent int
}

func (e *Engine) promptContextBudget(locked session.LockedContract) promptContextBudget {
	if locked.ContextWindow > 0 && locked.ContextPercent > 0 {
		return promptContextBudget{window: locked.ContextWindow, percent: locked.ContextPercent}
	}
	budget := e.promptContextBudgetFromConfig()
	if locked.ContextWindow > 0 {
		budget.window = locked.ContextWindow
	}
	if locked.ContextPercent > 0 {
		budget.percent = locked.ContextPercent
	}
	return budget
}

func (e *Engine) promptContextBudgetFromConfig() promptContextBudget {
	return promptContextBudget{window: e.cfg.ContextWindowTokens, percent: e.cfg.EffectiveContextWindowPercent}
}

func summarizeOutputItemTypes(items []llm.ResponseItem) []string {
	if len(items) == 0 {
		return nil
	}
	counts := make(map[string]int, len(items))
	order := make([]string, 0, len(items))
	for _, item := range items {
		t := strings.TrimSpace(string(item.Type))
		if t == "" {
			t = "unknown"
		}
		if _, ok := counts[t]; !ok {
			order = append(order, t)
		}
		counts[t]++
	}
	out := make([]string, 0, len(order))
	for _, t := range order {
		out = append(out, fmt.Sprintf("%s:%d", t, counts[t]))
	}
	return out
}

type hostedToolExecution struct {
	Call           llm.ToolCall
	Result         tools.Result
	outputPosition int
}

func hostedToolExecutionsFromOutputItems(items []llm.ResponseItem, defs []tools.Definition) []hostedToolExecution {
	out := make([]hostedToolExecution, 0, len(items))
	for position, item := range items {
		id, _ := textutil.OptionalTrimmed(item.ID)
		callID, _ := textutil.OptionalTrimmed(item.CallID)
		decoded := tools.HostedExecutionsFromOutputs([]tools.HostedToolOutput{{
			ID:     id,
			CallID: callID,
			Raw:    append(json.RawMessage(nil), item.Raw...),
		}}, defs)
		for _, execution := range decoded {
			out = append(out, hostedToolExecution{
				Call: llm.ToolCall{
					ID:    execution.Call.ID,
					Name:  string(execution.Call.Name),
					Input: execution.Call.Input,
				},
				Result:         execution.Result,
				outputPosition: position,
			})
		}
	}
	return out
}

func (e *Engine) requestTools(ctx context.Context, workflowMode workflowruntime.CompletionMode) ([]llm.Tool, error) {
	workflowToolMode := workflowMode == workflowruntime.CompletionModeTool
	shape, err := e.lockedRequestShape()
	if err != nil {
		return nil, err
	}
	exposure := tools.RequestExposureContext{
		SupportsVision:     llm.LockedContractSupportsVisionInputs(e.store.Meta().Locked, e.cfg.Model),
		WorkflowCompletion: workflowToolMode,
	}
	defs := tools.RequestExposedDefinitionsForSession(shape.EnabledTools, e.registry.Definitions(), exposure)
	if workflowToolMode {
		if def, ok := tools.DefinitionFor(toolspec.ToolCompleteNode); ok && !definitionListContains(defs, toolspec.ToolCompleteNode) {
			defs = append(defs, def)
		}
	}
	if len(defs) == 0 {
		return nil, nil
	}
	out := make([]llm.Tool, 0, len(defs))
	customPatchSupported := e.supportsCustomPatchTool(ctx)
	for _, d := range defs {
		tool := llm.Tool{Name: string(d.ID), Description: d.Description, Schema: d.Schema}
		if d.ID == toolspec.ToolCompleteNode {
			contract, contractErr := e.workflowCompletionContract()
			if contractErr != nil {
				return nil, contractErr
			}
			document, err := workflowruntime.CompletionJSONSchema(contract)
			if err != nil {
				return nil, fmt.Errorf("prepare complete_node request schema: %w", err)
			}
			schema, err := jsoncontract.NewPreparer(e.cfg.Debug).FunctionDocument(
				"workflow completion function",
				document,
			)
			if err != nil {
				return nil, fmt.Errorf("prepare complete_node request schema: %w", err)
			}
			tool.Schema = schema
		}
		if d.ID == toolspec.ToolPatch && customPatchSupported {
			tool.Schema = jsoncontract.Function{}
			tool.Custom = &llm.CustomToolFormat{Type: "grammar", Syntax: "lark", Definition: llm.PatchToolLarkGrammar}
		}
		out = append(out, tool)
	}
	return out, nil
}

func definitionListContains(defs []tools.Definition, id toolspec.ID) bool {
	for _, def := range defs {
		if def.ID == id {
			return true
		}
	}
	return false
}

func (e *Engine) supportsCustomPatchTool(ctx context.Context) bool {
	caps, err := e.providerCapabilities(ctx)
	if err != nil {
		return false
	}
	return caps.SupportsResponsesAPI && caps.IsOpenAIFirstParty
}
