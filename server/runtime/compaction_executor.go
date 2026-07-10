package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/server/llm"
	"core/server/session"
	"core/shared/transcript"
)

var (
	// errLocalCompactionAttemptedToolCalls is returned when the local compaction summary model emits tool calls.
	errLocalCompactionAttemptedToolCalls = errors.New("local compaction summary attempted tool calls")
	// errLocalCompactionToolCallEmptyID is returned when a local compaction retry tool call lacks an id.
	errLocalCompactionToolCallEmptyID = errors.New("local compaction summary attempted tool call with empty id")
)

func (e *Engine) compactRemote(ctx context.Context, stepID string, input []llm.ResponseItem, providerID string, instructions string) (compactionResult, error) {
	compactor, ok := e.llm.(llm.CompactionClient)
	if !ok {
		return compactionResult{}, errors.New("llm client does not support remote compaction")
	}
	locked, err := e.ensureLocked()
	if err != nil {
		return compactionResult{}, err
	}
	requestItems := llm.CloneResponseItems(input)
	baseRequest := llm.CompactionRequest{
		Model:        locked.Model,
		Instructions: instructions,
		SessionID:    e.store.Meta().SessionID,
		InputItems:   requestItems,
	}

	resp, _, repairStats, err := e.compactWithContextRepairRetry(ctx, stepID, compactor, baseRequest)
	if err != nil {
		return compactionResult{}, err
	}

	sanitized, err := sanitizeRemoteCompactionOutput(resp.OutputItems)
	if err != nil {
		return compactionResult{}, err
	}
	replacement := llm.CloneResponseItems(sanitized)
	return compactionResult{
		engine:            "remote",
		items:             replacement,
		usage:             resp.Usage,
		trimmedItemsCount: cloneOptionalInt(resp.TrimmedItemsCount),
		overflowRepair:    repairStats,
		provider:          providerID,
	}, nil
}

func compactionConversationWithPromptItems(items []llm.ResponseItem, instructions string) []llm.ResponseItem {
	conversation := llm.CloneResponseItems(items)
	prompt := strings.TrimSpace(instructions)
	if prompt == "" {
		return conversation
	}
	return append(conversation, llm.ResponseItem{Type: llm.ResponseItemTypeMessage, Role: llm.RoleDeveloper, Content: prompt})
}

func (e *Engine) compactWithContextRepairRetry(
	ctx context.Context,
	stepID string,
	client llm.CompactionClient,
	request llm.CompactionRequest,
) (llm.CompactionResponse, []llm.ResponseItem, compactionOverflowRepairStats, error) {
	currentInput := llm.CloneResponseItems(request.InputItems)
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
		req.InputItems = llm.CloneResponseItems(items)
		resp, err := e.compactWithRetry(ctx, stepID, client, req)
		if !isMissingToolOutputProviderError(err, items) {
			return resp, items, err
		}
		if !canRepair {
			panic(missingToolOutputAfterCollapseInvariant)
		}
		repaired, repairErr := e.repairMissingToolOutputsByAppending(stepID)
		if repairErr != nil {
			return resp, items, errors.Join(err, repairErr)
		}
		if repaired == 0 {
			return resp, items, err
		}
		repairedItems := llm.CloneResponseItems(e.transcriptRuntimeState().SnapshotItems())
		req.InputItems = llm.CloneResponseItems(repairedItems)
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
			return llm.CompactionResponse{}, nil, repairStats, err
		}

		targetSavedTokens := compactionOverflowRepairTargetTokens(contextWindowTokens, attempt+1)
		nextInput, repaired := collapseCompactionOverflowToolPayloadsAfterSavings(currentInput, targetSavedTokens, repairStats.EstimatedSavedTokens)
		if !repaired.Collapsed() {
			// Only known tool payloads are safe to collapse here. Ordinary
			// conversation history must not be trimmed or request-shaped at
			// compaction time, so fail instead of retrying the same payload.
			return llm.CompactionResponse{}, nil, repairStats, err
		}
		currentInput = nextInput
		repairStats = repairStats.Add(repaired)
	}

	return llm.CompactionResponse{}, nil, repairStats, errors.New("compaction context repair retry exhausted")
}

func (e *Engine) compactWithRetry(ctx context.Context, stepID string, client llm.CompactionClient, request llm.CompactionRequest) (llm.CompactionResponse, error) {
	prepared, err := e.prepareCompactionCacheObservation(ctx, request)
	if err != nil {
		return llm.CompactionResponse{}, err
	}
	if err := e.observePromptCacheRequest(stepID, prepared); err != nil {
		return llm.CompactionResponse{}, err
	}

	delays := compactionRetryDelays
	var lastErr error
	for i := 0; i <= len(delays); i++ {
		resp, err := client.Compact(ctx, request)
		if err != nil && ctx.Err() != nil {
			return llm.CompactionResponse{}, ctx.Err()
		}
		if err == nil {
			if err := e.observePromptCacheResponse(stepID, prepared, resp.Usage); err != nil {
				return llm.CompactionResponse{}, err
			}
			return resp, nil
		}
		if llm.IsNonRetriableModelError(err) || llm.IsContextLengthOverflowError(err) {
			return llm.CompactionResponse{}, err
		}
		lastErr = err
		if i == len(delays) {
			break
		}
		if err := waitForRetryDelay(ctx, delays[i]); err != nil {
			return llm.CompactionResponse{}, err
		}
	}
	return llm.CompactionResponse{}, fmt.Errorf("compaction request failed after retries: %w", lastErr)
}

func (e *Engine) prepareCompactionCacheObservation(ctx context.Context, request llm.CompactionRequest) (preparedCacheRequestObservation, error) {
	if e == nil || e.modelRequests().RequestCache() == nil || !e.supportsPromptCacheKey(ctx) {
		return preparedCacheRequestObservation{}, nil
	}
	lineageRequest, ok, err := e.compactionCacheObservationRequest(ctx, request)
	if err != nil || !ok {
		return preparedCacheRequestObservation{}, err
	}
	return e.modelRequests().RequestCache().Prepare(lineageRequest)
}

func (e *Engine) compactionCacheObservationRequest(ctx context.Context, request llm.CompactionRequest) (llm.Request, bool, error) {
	if e == nil {
		return llm.Request{}, false, nil
	}
	cacheKey := conversationPromptCacheKey(e.SessionID(), e.compactionRuntimeState().Count())
	if cacheKey == "" {
		return llm.Request{}, false, nil
	}
	locked, err := e.ensureLocked()
	if err != nil {
		return llm.Request{}, false, err
	}
	items := compactionConversationWithPromptItems(request.InputItems, request.Instructions)
	systemPrompt, err := e.systemPromptWithoutBackfill(locked)
	if err != nil {
		return llm.Request{}, false, err
	}
	workflowMode, err := e.workflowCompletionMode(ctx)
	if err != nil {
		return llm.Request{}, false, err
	}
	requestTools, err := e.requestTools(ctx, workflowMode)
	if err != nil {
		return llm.Request{}, false, err
	}
	req, err := llm.RequestFromLockedContract(locked, systemPrompt, items, requestTools)
	if err != nil {
		return llm.Request{}, false, err
	}
	req.ReasoningEffort = e.ThinkingLevel()
	req.FastMode = e.FastModeEnabled()
	req.SessionID = e.SessionID()
	req.PromptCacheKey = cacheKey
	req.PromptCacheScope = transcript.CacheWarningScopeConversation
	return req, true, nil
}

func (e *Engine) compactLocal(ctx context.Context, input []llm.ResponseItem, providerID string, instructions string, mode compactionMode) (compactionResult, error) {
	summary, repairStats, err := e.localCompactionSummaryWithRepair(ctx, input, instructions, mode)
	if err != nil {
		return compactionResult{}, err
	}
	replacement := llm.CloneResponseItems([]llm.ResponseItem{{
		Type:        llm.ResponseItemTypeMessage,
		Role:        llm.RoleDeveloper,
		MessageType: llm.MessageTypeCompactionSummary,
		Content:     strings.TrimSpace(summary),
	}})

	usageInputTokens := estimateItemsTokens(replacement)
	if preciseInput, ok := e.inputTokensForItems(ctx, e.currentModel(), "", replacement); ok {
		usageInputTokens = preciseInput
	}
	return compactionResult{
		engine:            "local",
		items:             replacement,
		usage:             llm.Usage{InputTokens: usageInputTokens, WindowTokens: e.compactionPlannerState().contextWindowTokens(e.compactionPlanningSnapshot())},
		trimmedItemsCount: nil,
		overflowRepair:    repairStats,
		provider:          providerID,
		summary:           strings.TrimSpace(summary),
	}, nil
}

func (e *Engine) localCompactionSummary(ctx context.Context, input []llm.ResponseItem, instructions string, mode compactionMode) (string, error) {
	summary, _, err := e.localCompactionSummaryWithRepair(ctx, input, instructions, mode)
	return summary, err
}

func (e *Engine) localCompactionSummaryWithRepair(ctx context.Context, input []llm.ResponseItem, instructions string, mode compactionMode) (string, compactionOverflowRepairStats, error) {
	locked, err := e.ensureLocked()
	if err != nil {
		return "", compactionOverflowRepairStats{}, err
	}
	systemPrompt, err := e.systemPromptWithoutBackfill(locked)
	if err != nil {
		return "", compactionOverflowRepairStats{}, err
	}
	workflowMode, err := e.workflowCompletionMode(ctx)
	if err != nil {
		return "", compactionOverflowRepairStats{}, err
	}
	requestTools, err := e.requestTools(ctx, workflowMode)
	if err != nil {
		return "", compactionOverflowRepairStats{}, err
	}
	window := localCompactionWindow(input)
	repairStats := compactionOverflowRepairStats{}
	contextWindowTokens := e.compactionPlannerState().contextWindowTokens(e.compactionPlanningSnapshot())
	// summarize mirrors the remote send closure: it repairs a missing-tool-output
	// HTTP 400 (append + re-snapshot + retry) only on the first, uncollapsed
	// window. After a collapse, output items are preserved, so a missing-output
	// 400 is an invariant violation and panics; other 400s fall through.
	summarize := func(w []llm.ResponseItem, canRepair bool) (string, []llm.ResponseItem, error) {
		summary, err := e.localCompactionSummaryFromWindow(ctx, locked, systemPrompt, w, instructions, requestTools, mode)
		if !isMissingToolOutputProviderError(err, w) {
			return summary, w, err
		}
		if !canRepair {
			panic(missingToolOutputAfterCollapseInvariant)
		}
		repaired, repairErr := e.repairMissingToolOutputsByAppending("")
		if repairErr != nil {
			return "", w, errors.Join(err, repairErr)
		}
		if repaired == 0 {
			return summary, w, err
		}
		repairedWindow := localCompactionWindow(e.transcriptRuntimeState().SnapshotItems())
		summary, err = e.localCompactionSummaryFromWindow(ctx, locked, systemPrompt, repairedWindow, instructions, requestTools, mode)
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

func (e *Engine) localCompactionSummaryFromWindow(ctx context.Context, locked session.LockedContract, systemPrompt string, window []llm.ResponseItem, instructions string, requestTools []llm.Tool, mode compactionMode) (string, error) {
	items := append(llm.CloneResponseItems(window), llm.ResponseItem{
		Type:    llm.ResponseItemTypeMessage,
		Role:    llm.RoleDeveloper,
		Content: instructions,
	})
	for attempt := 0; ; attempt++ {
		req, err := llm.RequestFromLockedContract(locked, systemPrompt, items, requestTools)
		if err != nil {
			return "", err
		}
		req.ReasoningEffort = e.ThinkingLevel()
		req.FastMode = e.FastModeEnabled()
		req.SessionID = e.SessionID()
		if e.supportsPromptCacheKey(ctx) {
			if cacheKey := conversationPromptCacheKey(e.SessionID(), e.compactionRuntimeState().Count()); cacheKey != "" {
				req.PromptCacheKey = cacheKey
				req.PromptCacheScope = transcript.CacheWarningScopeConversation
			}
		}

		resp, err := e.generateWithRetryClient(ctx, "", e.llm, req, nil, nil, nil)
		if err != nil {
			return "", err
		}
		if len(resp.ToolCalls) > 0 {
			if mode != compactionModeHandoff || attempt >= handoffCompactionToolCallRetries {
				return "", errLocalCompactionAttemptedToolCalls
			}
			retryItems, err := handoffCompactionToolCallRetryItems(resp)
			if err != nil {
				return "", err
			}
			items = append(items, retryItems...)
			continue
		}
		summary := strings.TrimSpace(resp.Assistant.Content)
		if summary == "" {
			return "", errors.New("local compaction summary was empty")
		}
		return summary, nil
	}
}

func handoffCompactionToolCallRetryItems(resp llm.Response) ([]llm.ResponseItem, error) {
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
		Content:   resp.Assistant.Content,
		ToolCalls: calls,
	}})
	for _, call := range calls {
		items = append(items, llm.ResponseItem{
			Type:   llm.ToolOutputItemType(call.Custom),
			CallID: strings.TrimSpace(call.ID),
			Name:   call.Name,
			Output: mustJSON(map[string]any{"error": handoffCompactionToolsDisabledMessage}),
		})
	}
	return items, nil
}

func localCompactionWindow(input []llm.ResponseItem) []llm.ResponseItem {
	if len(input) == 0 {
		return nil
	}
	start := 0
	for i := len(input) - 1; i >= 0; i-- {
		if isCompactionBoundaryItem(input[i]) {
			start = i
			break
		}
	}
	window := llm.CloneResponseItems(input[start:])
	return window
}

func isCompactionBoundaryItem(item llm.ResponseItem) bool {
	if item.Type == llm.ResponseItemTypeCompaction {
		return true
	}
	if item.Type == llm.ResponseItemTypeMessage {
		return item.MessageType == llm.MessageTypeCompactionSummary
	}
	return false
}
