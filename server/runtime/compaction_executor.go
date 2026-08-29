package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/server/llm"
	"core/shared/rpcwire"
	"core/shared/textutil"
	"core/shared/transcript"
)

var (
	// errLocalCompactionAttemptedToolCalls is returned when the local compaction summary model emits tool calls.
	errLocalCompactionAttemptedToolCalls = errors.New("local compaction summary attempted tool calls")
	// errLocalCompactionToolCallEmptyID is returned when a local compaction retry tool call lacks an id.
	errLocalCompactionToolCallEmptyID = errors.New("local compaction summary attempted tool call with empty id")
)

func (e *Engine) compactRemote(ctx context.Context, stepID string, input []llm.ResponseItem, providerID string, instructions string, dispatchFactory dispatchRequestFactory) (compactionResult, []llm.ResponseItem, error) {
	if !e.llm.supportsCompaction() {
		return compactionResult{}, nil, errors.New("llm client does not support remote compaction")
	}
	baseRequest, err := e.compactionRequest(ctx, input, instructions)
	if err != nil {
		return compactionResult{}, nil, err
	}

	resp, sentInput, repairStats, err := e.compactWithContextRepairRetry(ctx, stepID, e.llm, baseRequest, input, instructions, dispatchFactory)
	if err != nil {
		return compactionResult{overflowRepair: repairStats, provider: providerID}, sentInput, err
	}

	replacement := []llm.ResponseItem{llm.CloneResponseItems([]llm.ResponseItem{resp.Checkpoint})[0]}
	return compactionResult{
		engine:         "remote",
		items:          replacement,
		usage:          resp.Usage,
		overflowRepair: repairStats,
		provider:       providerID,
	}, nil, nil
}

func compactionConversationWithPromptItems(items []llm.ResponseItem, instructions string) []llm.ResponseItem {
	conversation := llm.CloneResponseItems(items)
	prompt := strings.TrimSpace(instructions)
	if prompt == "" {
		return conversation
	}
	return append(conversation, llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleDeveloper, Content: textutil.Value(prompt)}})...)
}

func (e *Engine) compactWithContextRepairRetry(
	ctx context.Context,
	stepID string,
	client *observedModelClient,
	request llm.CompactionRequest,
	input []llm.ResponseItem,
	instructions string,
	dispatchFactory dispatchRequestFactory,
) (llm.CompactionResponse, []llm.ResponseItem, compactionOverflowRepairStats, error) {
	currentInput := llm.CloneResponseItems(input)
	repairStats := compactionOverflowRepairStats{}
	contextWindowTokens := e.compactionPlannerState().contextWindowTokens(e.compactionPlanningSnapshot())

	// send issues one compaction request. canRepair is set only for the first,
	// uncollapsed send: a missing-tool-output HTTP 400 there is repaired in place
	// by appending synthetic outputs and retrying with the re-snapshotted
	// transcript. Overflow collapse preserves output items, so a missing-output
	// 400 after a collapse is an invariant violation and panics; any other 400
	// (including context overflow) falls through to the overflow loop unchanged.
	send := func(items []llm.ResponseItem, canRepair bool) (llm.CompactionResponse, []llm.ResponseItem, error) {
		req := request
		req.Items = compactionConversationWithPromptItems(items, instructions)
		req, err := dispatchFactory.compaction(req)
		if err != nil {
			return llm.CompactionResponse{}, items, err
		}
		resp, err := e.compactWithRetry(ctx, stepID, client, req)
		if !isMissingToolOutputProviderError(err, items) {
			return resp, items, err
		}
		if !canRepair {
			panic(missingToolOutputAfterCollapseInvariant)
		}
		repaired, repairErr := e.repairMissingToolOutputsByAppending(
			textutil.OptionalTrimmedString(stepID),
			missingToolOutputRepairLiveProvider400,
		)
		if repairErr != nil {
			return resp, items, errors.Join(err, repairErr)
		}
		if repaired == 0 {
			return resp, items, err
		}
		repairedItems := llm.CloneResponseItems(e.transcriptRuntimeState().SnapshotItems())
		req.Items = compactionConversationWithPromptItems(repairedItems, instructions)
		req, freshErr := dispatchFactory.compaction(req)
		if freshErr != nil {
			return llm.CompactionResponse{}, repairedItems, freshErr
		}
		resp, err = e.compactWithRetry(ctx, stepID, client, req)
		return resp, repairedItems, err
	}

	for attempt := 0; attempt <= len(compactionOverflowRepairTargetPercents); attempt++ {
		resp, sentInput, err := send(currentInput, attempt == 0)
		currentInput = sentInput
		if err == nil {
			return resp, currentInput, repairStats, nil
		}
		if !llm.IsContextLengthOverflowError(err) || attempt == len(compactionOverflowRepairTargetPercents) {
			return llm.CompactionResponse{}, currentInput, repairStats, err
		}

		targetSavedTokens := compactionOverflowRepairTargetTokens(contextWindowTokens, attempt+1)
		nextInput, repaired := collapseCompactionOverflowToolPayloadsAfterSavings(currentInput, targetSavedTokens, repairStats.EstimatedSavedTokens)
		if !repaired.Collapsed() {
			// Only known tool payloads are safe to collapse here. Ordinary
			// conversation history must not be trimmed or request-shaped at
			// compaction time, so fail instead of retrying the same payload.
			return llm.CompactionResponse{}, currentInput, repairStats, err
		}
		currentInput = nextInput
		repairStats = repairStats.Add(repaired)
	}

	return llm.CompactionResponse{}, nil, repairStats, errors.New("compaction context repair retry exhausted")
}

func (e *Engine) compactWithRetry(ctx context.Context, stepID string, client *observedModelClient, request llm.CompactionRequest) (llm.CompactionResponse, error) {
	observed, err := e.prepareCacheObservedRequest(stepID, request, cacheResponseObservationExactStep)
	if err != nil {
		return llm.CompactionResponse{}, err
	}

	delays := compactionRetryDelays
	var lastErr error
	publishedProviderDiagnostics := make(map[llm.CodexTurnStateDiagnosticCategory]struct{}, 2)
	for i := 0; i <= len(delays); i++ {
		resp, err := client.compactObserved(ctx, observed, func() {
			e.publishProviderTurnStateDiagnostics(stepID, observed.request.CodexDispatch, publishedProviderDiagnostics)
		})
		if err != nil && ctx.Err() != nil {
			return llm.CompactionResponse{}, ctx.Err()
		}
		if err == nil {
			return resp, nil
		}
		var observationErr *cacheObservationDispatchError
		if errors.As(err, &observationErr) {
			return llm.CompactionResponse{}, err
		}
		if llm.IsNonRetriableModelError(err) || llm.IsContextLengthOverflowError(err) {
			return llm.CompactionResponse{}, err
		}
		lastErr = err
		if i == len(delays) {
			break
		}
		if err := rpcwire.WaitForRetry(ctx, delays[i]); err != nil {
			return llm.CompactionResponse{}, err
		}
	}
	return llm.CompactionResponse{}, fmt.Errorf("compaction request failed after retries: %w", lastErr)
}

func (e *Engine) compactionRequest(ctx context.Context, input []llm.ResponseItem, instructions string) (llm.CompactionRequest, error) {
	return e.compactionRequestFromItems(ctx, compactionConversationWithPromptItems(input, instructions))
}

func (e *Engine) compactionRequestFromItems(ctx context.Context, items []llm.ResponseItem) (llm.CompactionRequest, error) {
	locked, err := e.ensureLocked()
	if err != nil {
		return llm.CompactionRequest{}, err
	}
	systemPrompt, err := e.systemPromptWithoutBackfill(locked)
	if err != nil {
		return llm.CompactionRequest{}, err
	}
	workflowMode, err := e.workflowCompletionMode(ctx)
	if err != nil {
		return llm.CompactionRequest{}, err
	}
	requestTools, err := e.requestTools(ctx, workflowMode)
	if err != nil {
		return llm.CompactionRequest{}, err
	}
	req, err := llm.RequestFromLockedContract(locked, systemPrompt, items, requestTools, llm.ToolControls{ChoiceMode: llm.ToolChoiceModeAutomatic})
	if err != nil {
		return llm.CompactionRequest{}, err
	}
	req.ReasoningEffort = e.ThinkingLevel()
	req.FastMode = e.FastModeEnabled()
	if e.supportsPromptCacheKey(ctx) {
		req.PromptCacheKey = e.conversationPromptCacheKey(e.SessionID())
		req.PromptCacheScope = transcript.CacheWarningScopeConversation
	}
	return req, nil
}

func (e *Engine) compactLocal(ctx context.Context, stepID string, input []llm.ResponseItem, providerID string, instructions string, dispatchFactory dispatchRequestFactory) (compactionResult, error) {
	summary, repairStats, err := e.localCompactionSummaryWithRepair(
		ctx,
		stepID,
		input,
		instructions,
		dispatchFactory,
	)
	if err != nil {
		return compactionResult{}, err
	}
	replacement := llm.ItemsFromMessages([]llm.Message{{
		Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeCompactionSummary), Content: textutil.Value(strings.TrimSpace(summary)),
	}})

	usageInputTokens := estimateItemsTokens(replacement)
	return compactionResult{
		engine:            "local",
		items:             replacement,
		usage:             llm.Usage{InputTokens: usageInputTokens, WindowTokens: e.compactionPlannerState().contextWindowTokens(e.compactionPlanningSnapshot())},
		trimmedItemsCount: nil,
		overflowRepair:    repairStats,
		provider:          providerID,
	}, nil
}

func (e *Engine) localCompactionSummary(ctx context.Context, input []llm.ResponseItem, instructions string) (string, error) {
	run := e.ActiveRun()
	if run == nil {
		return "", fmt.Errorf("%w: active Run identity is required for local compaction", llm.ErrInvalidRequest)
	}
	factory, err := e.activeDispatchRequestFactory(run.StepID, nil)
	if err != nil {
		return "", err
	}
	summary, _, err := e.localCompactionSummaryWithRepair(ctx, run.StepID, input, instructions, factory)
	return summary, err
}

func (e *Engine) localCompactionSummaryWithRepair(ctx context.Context, stepID string, input []llm.ResponseItem, instructions string, dispatchFactory dispatchRequestFactory) (string, compactionOverflowRepairStats, error) {
	window := llm.CloneResponseItems(input)
	repairStats := compactionOverflowRepairStats{}
	contextWindowTokens := e.compactionPlannerState().contextWindowTokens(e.compactionPlanningSnapshot())
	// summarize mirrors the remote send closure: it repairs a missing-tool-output
	// HTTP 400 (append + re-snapshot + retry) only on the first, uncollapsed
	// window. After a collapse, output items are preserved, so a missing-output
	// 400 is an invariant violation and panics; other 400s fall through.
	summarize := func(w []llm.ResponseItem, canRepair bool) (string, []llm.ResponseItem, error) {
		summary, err := e.localCompactionSummaryFromWindow(ctx, stepID, w, instructions, dispatchFactory)
		if !isMissingToolOutputProviderError(err, w) {
			return summary, w, err
		}
		if !canRepair {
			panic(missingToolOutputAfterCollapseInvariant)
		}
		repaired, repairErr := e.repairMissingToolOutputsByAppending(
			textutil.OptionalTrimmedString(stepID),
			missingToolOutputRepairLiveProvider400,
		)
		if repairErr != nil {
			return "", w, errors.Join(err, repairErr)
		}
		if repaired == 0 {
			return summary, w, err
		}
		repairedWindow := e.transcriptRuntimeState().SnapshotItems()
		summary, err = e.localCompactionSummaryFromWindow(ctx, stepID, repairedWindow, instructions, dispatchFactory)
		return summary, repairedWindow, err
	}

	for repairAttempt := 0; repairAttempt <= len(compactionOverflowRepairTargetPercents); repairAttempt++ {
		summary, sentWindow, err := summarize(window, repairAttempt == 0)
		window = sentWindow
		if err == nil {
			return summary, repairStats, nil
		}
		if !llm.IsContextLengthOverflowError(err) || repairAttempt == len(compactionOverflowRepairTargetPercents) {
			return "", repairStats, err
		}
		targetSavedTokens := compactionOverflowRepairTargetTokens(contextWindowTokens, repairAttempt+1)
		nextWindow, repaired := collapseCompactionOverflowToolPayloadsAfterSavings(window, targetSavedTokens, repairStats.EstimatedSavedTokens)
		if !repaired.Collapsed() {
			// Only known tool payloads are safe to collapse here. Ordinary
			// conversation history must not be trimmed or request-shaped at
			// compaction time, so fail instead of retrying the same payload.
			return "", repairStats, err
		}
		window = nextWindow
		repairStats = repairStats.Add(repaired)
	}
	return "", repairStats, errors.New("local compaction context repair retry exhausted")
}

func (e *Engine) localCompactionSummaryFromWindow(ctx context.Context, stepID string, window []llm.ResponseItem, instructions string, dispatchFactory dispatchRequestFactory) (string, error) {
	items := compactionConversationWithPromptItems(window, instructions)
	for attempt := 0; ; attempt++ {
		req, err := e.compactionRequestFromItems(ctx, items)
		if err != nil {
			return "", err
		}
		req, err = dispatchFactory.generation(req)
		if err != nil {
			return "", err
		}

		resp, err := e.generateWithRetryClient(ctx, stepID, e.llm, req, nil, nil, nil)
		if err != nil {
			return "", err
		}
		if len(resp.ToolCalls) > 0 {
			if attempt >= localCompactionToolCallRetries {
				return "", errLocalCompactionAttemptedToolCalls
			}
			retryItems, err := localCompactionToolCallRetryItems(resp)
			if err != nil {
				return "", err
			}
			items = append(items, retryItems...)
			continue
		}
		if resp.Assistant.Content == nil {
			return "", errors.New("local compaction summary was empty")
		}
		summary := strings.TrimSpace(*resp.Assistant.Content)
		if summary == "" {
			return "", errors.New("local compaction summary was empty")
		}
		return summary, nil
	}
}

func localCompactionToolCallRetryItems(resp llm.Response) ([]llm.ResponseItem, error) {
	if len(resp.ToolCalls) == 0 {
		return nil, nil
	}
	calls := make([]llm.ToolCall, 0, len(resp.ToolCalls))
	for _, call := range resp.ToolCalls {
		if strings.TrimSpace(call.ID) == "" {
			return nil, errLocalCompactionToolCallEmptyID
		}
		calls = append(calls, call)
	}
	items := llm.ItemsFromMessages([]llm.Message{{
		Role:      llm.RoleAssistant,
		Content:   textutil.Pointer(resp.Assistant.Content),
		ToolCalls: calls,
	}})
	for _, call := range calls {
		items = append(items, llm.ResponseItem{
			Type:   llm.ToolOutputItemType(call.Custom),
			CallID: textutil.OptionalTrimmedString(call.ID),
			Name:   textutil.OptionalExactString(call.Name),
			Output: mustJSON(map[string]any{"error": localCompactionToolsDisabledMessage}),
		})
	}
	return llm.PrepareOpenAIInputItems(items), nil
}

func isCompactionBoundaryItem(item llm.ResponseItem) bool {
	if item.Type == llm.ResponseItemTypeCompaction {
		return true
	}
	return item.Type == llm.ResponseItemTypeMessage &&
		item.MessageType != nil &&
		*item.MessageType == llm.MessageTypeCompactionSummary
}
