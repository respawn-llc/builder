package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/prompts"
	"core/server/llm"
	"core/server/session"
	"core/shared/textutil"
	"core/shared/transcript"
)

type compactionMode string

type compactionInstructionsInput struct {
	additional *string
}

const (
	compactionModeAuto    compactionMode = "auto"
	compactionModeHandoff compactionMode = "handoff"
	compactionModeManual  compactionMode = "manual"

	defaultContextWindowTokens         = 200_000
	autoCompactNearLimitMargin         = 8_000
	compactionSoonReminderPercent      = 85
	manualCompactionCarryoverMaxChars  = 4_000
	preciseTokenCountSupportDiagnostic = "precise_token_count_support_failure"
	preciseTokenCountFailureDiagnostic = "precise_token_count_failure"

	additionalCompactionInstructionsHeader = "# Additional user instructions or commentary for this task:"
	manualCompactionCarryoverHeader        = "# Last user message before handoff (work may have been done after it was sent):"
	handoffDisabledByUserMessage           = "User disabled the handoff manually for now. They do not want you to hand off at this time, so please keep working or retry this tool later."
	handoffTooEarlyMessage                 = "It's too early to handoff right now. Don't worry, you still have plenty of time and memory to finish your work, so continue the current task for now. Only retry trigger_handoff after an explicit developer message says handoff is enabled."
	handoffCompactionToolsDisabledMessage  = "Tools are disabled during handoff. Do NOT attempt to call any tools. Produce only the requested summary."
	handoffCompactionToolCallRetries       = 3
)

var errRemoteCompactionMissingCheckpoint = errors.New("remote compaction output missing checkpoint item")

var (
	// errHandoffDisabledByUser is returned when the user has disabled handoff and the agent requests one.
	errHandoffDisabledByUser = errors.New(handoffDisabledByUserMessage)
	// errHandoffTooEarly is returned when the agent requests a handoff before the trigger_handoff tool is enabled.
	errHandoffTooEarly = errors.New(handoffTooEarlyMessage)
	// errCompactionDisabledModeNone is returned when manual compaction is requested while compaction_mode=none.
	errCompactionDisabledModeNone = errors.New("context compaction is disabled (compaction_mode=none)")
)

type compactionResult struct {
	engine            string
	items             []llm.ResponseItem
	usage             llm.Usage
	trimmedItemsCount *int
	overflowRepair    compactionOverflowRepairStats
	provider          string
	summary           string
}

type defaultContextCompactor struct {
	engine *Engine
	steps  exclusiveStepLifecycle
}

func (e *Engine) CompactContext(ctx context.Context, args string) error {
	e.ensureOrchestrationCollaborators()
	_, err := e.compactionFlow.CompactContextWithActiveHook(ctx, args, nil)
	return err
}

func (e *Engine) CompactContextWithActiveHook(ctx context.Context, args string, onActive func()) (session.CommitReceipt, error) {
	e.ensureOrchestrationCollaborators()
	return e.compactionFlow.CompactContextWithActiveHook(ctx, args, onActive)
}

func (e *Engine) CompactContextForPreSubmit(ctx context.Context) error {
	e.ensureOrchestrationCollaborators()
	_, err := e.compactionFlow.CompactContextForPreSubmitWithActiveHook(ctx, nil)
	return err
}

func (e *Engine) CompactContextForWorkflowContinuation(ctx context.Context) error {
	e.ensureOrchestrationCollaborators()
	_, err := e.compactionFlow.CompactContextForWorkflowContinuation(ctx)
	return err
}

func (e *Engine) CompactContextForPreSubmitWithActiveHook(ctx context.Context, onActive func()) (session.CommitReceipt, error) {
	e.ensureOrchestrationCollaborators()
	return e.compactionFlow.CompactContextForPreSubmitWithActiveHook(ctx, onActive)
}

func (e *Engine) TriggerHandoff(ctx context.Context, stepID string, activeCall llm.ToolCall, summarizerPrompt string, futureAgentMessage string) (string, bool, error) {
	e.ensureOrchestrationCollaborators()
	return e.compactionFlow.TriggerHandoff(ctx, stepID, activeCall, summarizerPrompt, futureAgentMessage)
}

func (c *defaultContextCompactor) CompactContextWithActiveHook(ctx context.Context, args string, onActive func()) (session.CommitReceipt, error) {
	instructions, err := newCompactionInstructionsInput(args)
	if err != nil {
		return session.CommitReceipt{}, err
	}
	return c.compactManualContext(ctx, instructions, onActive)
}

func (c *defaultContextCompactor) CompactContextForWorkflowContinuation(ctx context.Context) (session.CommitReceipt, error) {
	return c.compactManualContext(ctx, compactionInstructionsInput{}, nil)
}

func (c *defaultContextCompactor) compactManualContext(ctx context.Context, instructions compactionInstructionsInput, onActive func()) (session.CommitReceipt, error) {
	reservation := &exclusiveStepReservation{Kind: exclusiveStepReservationManualCompaction}
	if err := c.steps.AcquireReservation(reservation); err != nil {
		return session.CommitReceipt{}, err
	}
	defer c.steps.ReleaseReservation(reservation)
	return c.compactContext(ctx, compactionModeManual, instructions, true, reservation, onActive)
}

func (c *defaultContextCompactor) CompactContextForPreSubmitWithActiveHook(ctx context.Context, onActive func()) (session.CommitReceipt, error) {
	return c.compactContext(ctx, compactionModeManual, compactionInstructionsInput{}, false, nil, onActive)
}

func (c *defaultContextCompactor) TriggerHandoff(ctx context.Context, stepID string, activeCall llm.ToolCall, summarizerPrompt string, futureAgentMessage string) (string, bool, error) {
	e := c.engine
	_ = activeCall
	if strings.TrimSpace(stepID) == "" {
		return "", false, errors.New("trigger_handoff requires an active step")
	}
	planningSnapshot := e.compactionPlanningSnapshot()
	planner := e.compactionPlannerState()
	if !planningSnapshot.autoCompactionEnabled {
		return "", false, errHandoffDisabledByUser
	}
	if planner.mode(planningSnapshot.compactionMode) == "none" {
		return "", false, errors.New("User explicitly disabled compaction in configuration.")
	}
	if !e.compactionRuntimeState().SoonReminderIssued() {
		return "", false, errHandoffTooEarly
	}
	e.handoffRuntimeState().QueueRequest(summarizerPrompt, futureAgentMessage)
	summary := "Handoff scheduled to run now."
	appended := strings.TrimSpace(futureAgentMessage) != ""
	return summary, appended, nil
}

func (c *defaultContextCompactor) compactContext(ctx context.Context, mode compactionMode, instructions compactionInstructionsInput, includeManualCarryover bool, reservation *exclusiveStepReservation, onActive func()) (session.CommitReceipt, error) {
	e := c.engine
	activeKind := ActiveKindPreSubmitCompaction
	if includeManualCarryover {
		activeKind = ActiveKindCompaction
	}
	e.pauseQueuedUserAutoDrain()
	defer e.resumeQueuedUserAutoDrain()
	var receipt session.CommitReceipt
	err := runExclusiveStepWhenIdle(ctx, c.steps, activeKind, reservation, func(stepCtx context.Context, stepID string) error {
		if onActive != nil {
			onActive()
		}
		if err := e.ensureMetaContextForCompaction(stepCtx, stepID); err != nil {
			return err
		}
		_, compactReceipt, err := e.compactNow(stepCtx, stepID, mode, instructions, includeManualCarryover)
		receipt = compactReceipt
		if err == nil || receipt.Committed {
			e.handoffRuntimeState().ClearRequest()
		}
		return err
	})
	return receipt, err
}

func (e *Engine) autoCompactIfNeeded(ctx context.Context, stepID string, mode compactionMode) error {
	e.ensureOrchestrationCollaborators()
	return e.compactionFlow.AutoCompactIfNeeded(ctx, stepID, mode)
}

func (c *defaultContextCompactor) AutoCompactIfNeeded(ctx context.Context, stepID string, mode compactionMode) error {
	e := c.engine
	if mode == compactionModeAuto && !e.shouldAutoCompactWithContext(ctx) {
		return nil
	}
	_, receipt, err := e.compactNow(ctx, stepID, mode, compactionInstructionsInput{}, false)
	if err == nil || receipt.Committed {
		e.handoffRuntimeState().ClearRequest()
	}
	if err != nil && mode == compactionModeAuto {
		return fmt.Errorf("auto compaction failed: %w", err)
	}
	if err == nil && mode == compactionModeAuto && e.shouldAutoCompactWithContext(ctx) {
		return errors.New("auto compaction did not reduce context below threshold")
	}
	return err
}

func (e *Engine) shouldAutoCompactWithContext(ctx context.Context) bool {
	snapshot := e.compactionPlanningSnapshot()
	planner := e.compactionPlannerState()
	if !planner.autoCompactionAvailable(snapshot) {
		return false
	}
	limit := planner.autoCompactTokenLimit(snapshot)
	if limit <= 0 {
		return false
	}
	return e.usageAtOrAboveLimit(ctx, limit)
}

func (e *Engine) ShouldCompactBeforeUserMessage(ctx context.Context, text string) (bool, error) {
	e.ensureOrchestrationCollaborators()
	return e.compactionFlow.ShouldCompactBeforeUserMessage(ctx, text)
}

func (c *defaultContextCompactor) ShouldCompactBeforeUserMessage(ctx context.Context, text string) (bool, error) {
	e := c.engine
	if strings.TrimSpace(text) == "" {
		return false, nil
	}
	planningSnapshot := e.compactionPlanningSnapshot()
	planner := e.compactionPlannerState()
	if !planner.autoCompactionAvailable(planningSnapshot) {
		return false, nil
	}
	limit := planner.autoCompactTokenLimit(planningSnapshot)
	if limit <= 0 {
		return false, nil
	}
	reservedOutput := planner.reservedOutputTokens(planningSnapshot)
	preSubmitLimit := planner.preSubmitTokenLimit(planningSnapshot)
	if preSubmitLimit > 0 {
		_, _ = e.currentInputTokensPreciselyIfDueWithPriority(ctx, preSubmitLimit, true)
	}
	estimatedCurrentTotal := e.currentTokenUsage() + reservedOutput
	if preSubmitLimit > 0 && estimatedCurrentTotal >= preSubmitLimit {
		if preciseInput, ok := e.currentInputTokensPrecisely(ctx); ok {
			return preciseInput+reservedOutput >= preSubmitLimit, nil
		}
		return true, nil
	}
	promptEstimate := estimateItemsTokens(llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleUser, Content: textutil.Value(text)}}))
	if estimatedCurrentTotal+promptEstimate < limit {
		return false, nil
	}
	extra := llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleUser, Content: textutil.Value(text)}})
	req, err := e.buildRequestWithExtraItems(ctx, "", extra, true)
	if err != nil {
		return false, err
	}
	if preciseInput, ok := e.requestInputTokensPreciselyTracked(ctx, req, false); ok {
		return preciseInput+reservedOutput >= limit, nil
	}
	return estimatedCurrentTotal+promptEstimate >= limit, nil
}

func (e *Engine) resolveContextWindowTokens(ctx context.Context) int {
	if configured := e.configuredContextWindowTokens(); configured > 0 {
		return configured
	}

	model := e.currentModel()
	if resolver, ok := e.llm.(llm.ModelContextWindowClient); ok {
		resolved, err := resolver.ResolveModelContextWindow(ctx, model)
		if err == nil && resolved > 0 {
			e.setContextWindowTokens(resolved)
			return resolved
		}
	}
	return e.compactionPlannerState().contextWindowTokens(e.compactionPlanningSnapshot())
}

func (e *Engine) configuredContextWindowTokens() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cfg.ContextWindowTokens > 0 {
		return e.cfg.ContextWindowTokens
	}
	return 0
}

func (e *Engine) setContextWindowTokens(tokens int) {
	if tokens <= 0 {
		return
	}
	e.mu.Lock()
	e.cfg.ContextWindowTokens = tokens
	e.mu.Unlock()
}

func (e *Engine) currentModel() string {
	if model := e.lockedContractState().Model(); model != "" {
		return model
	}
	return strings.TrimSpace(e.cfg.Model)
}

func autoCompactPrecisionMarginForLimit(limit int) int {
	if limit <= 0 {
		return autoCompactNearLimitMargin
	}
	percentMargin := limit / 50
	if percentMargin > autoCompactNearLimitMargin {
		return percentMargin
	}
	return autoCompactNearLimitMargin
}

func (e *Engine) usageAtOrAboveLimit(ctx context.Context, limit int) bool {
	if limit <= 0 {
		return false
	}
	reservedOutput := e.compactionPlannerState().reservedOutputTokens(e.compactionPlanningSnapshot())
	if preciseInput, ok := e.currentInputTokensPreciselyIfDueWithPriority(ctx, limit, true); ok {
		return preciseInput+reservedOutput >= limit
	}
	estimatedInput := e.currentTokenUsage()
	estimatedTotal := estimatedInput + reservedOutput
	margin := autoCompactPrecisionMarginForLimit(limit)
	if estimatedTotal < limit && estimatedTotal+margin < limit {
		return false
	}
	preciseInput, ok := e.currentInputTokensPrecisely(ctx)
	if !ok {
		return estimatedTotal >= limit
	}
	return preciseInput+reservedOutput >= limit
}

func (e *Engine) currentInputTokensPrecisely(ctx context.Context) (int, bool) {
	req, err := e.buildRequest(ctx, "", true)
	if err != nil {
		return 0, false
	}
	return e.requestInputTokensPreciselyTracked(ctx, req, true)
}

func (e *Engine) currentInputTokensPreciselyWithoutPromptRefresh(ctx context.Context) (int, bool) {
	req, err := e.buildRequestWithoutPromptRefresh(ctx)
	if err != nil {
		return 0, false
	}
	return e.requestInputTokensPreciselyTracked(ctx, req, true)
}

func (e *Engine) buildRequestWithoutPromptRefresh(ctx context.Context) (llm.Request, error) {
	locked, err := e.ensureLocked()
	if err != nil {
		return llm.Request{}, err
	}
	workflowMode, err := e.workflowCompletionMode(ctx)
	if err != nil {
		return llm.Request{}, err
	}
	requestTools, err := e.requestTools(ctx, workflowMode)
	if err != nil {
		return llm.Request{}, err
	}
	systemPrompt, err := e.systemPromptWithoutBackfill(locked)
	if err != nil {
		return llm.Request{}, err
	}
	nativeWebSearch, nativeErr := e.enableNativeWebSearch(ctx)
	if nativeErr != nil {
		return llm.Request{}, nativeErr
	}
	toolChoiceMode := toolChoiceModeForWorkflowCompletion(workflowMode, e.workflowUseRequiredToolCalls())
	req, err := llm.RequestFromLockedContract(locked, systemPrompt, e.transcriptRuntimeState().SnapshotItems(), requestTools, llm.ToolControls{
		ChoiceMode:            toolChoiceMode,
		EnableNativeWebSearch: nativeWebSearch,
	})
	if err != nil {
		return llm.Request{}, err
	}
	req.ReasoningEffort = e.ThinkingLevel()
	req.FastMode = e.FastModeEnabled()
	req.SessionID = e.SessionID()
	if e.supportsPromptCacheKey(ctx) {
		if cacheKey := e.conversationPromptCacheKey(e.SessionID()); cacheKey != "" {
			req.PromptCacheKey = cacheKey
			req.PromptCacheScope = transcript.CacheWarningScopeConversation
		}
	}
	if err := e.validateToolChoiceSupport(ctx, toolChoiceMode); err != nil {
		return llm.Request{}, err
	}
	return req, nil
}

func (e *Engine) currentInputTokensPreciselyIfDueWithPriority(ctx context.Context, limit int, critical bool) (int, bool) {
	if precise, ok := e.lookupCurrentPreciseInputTokens(); ok {
		if !e.shouldRefreshCurrentPreciseInputTokens(limit, critical) {
			return precise, true
		}
	}
	if !e.shouldRefreshCurrentPreciseInputTokens(limit, critical) {
		return 0, false
	}
	req, err := e.buildRequest(ctx, "", true)
	if err != nil {
		return 0, false
	}
	return e.requestInputTokensPreciselyTracked(ctx, req, true)
}

func (e *Engine) requestInputTokensPreciselyTracked(ctx context.Context, req llm.Request, current bool) (int, bool) {
	counter, ok := e.llm.(llm.RequestInputTokenCountClient)
	if !ok {
		return 0, false
	}
	if !e.preciseInputTokenCountSupported(ctx) {
		return 0, false
	}
	cacheKey := ""
	if payload, err := json.Marshal(req); err == nil {
		sum := sha256.Sum256(payload)
		cacheKey = hex.EncodeToString(sum[:])
	}
	if cacheKey != "" {
		if cached, ok := e.lookupPreciseTokenCount(cacheKey, current); ok {
			if current {
				e.storePreciseTokenCount(cacheKey, cached, true)
			}
			return cached, true
		}
	}
	if e.diagnosticDedupeStore().HasPersisted(preciseTokenCountFailureDiagnostic) {
		return 0, false
	}
	count, err := counter.CountRequestInputTokens(ctx, req)
	if err != nil {
		if e.errorIsRepairableMissingToolOutput(err) {
			// The request carries interrupted tool calls without outputs that the
			// model request path repairs by appending synthetic outputs. Fall back to
			// an estimate for this probe only; do not persist a permanent failure that
			// would disable exact counting for the rest of the active list.
			return 0, false
		}
		e.reportPreciseTokenCountFailure(err)
		return 0, false
	}
	if count <= 0 {
		return 0, false
	}
	if cacheKey != "" {
		e.storePreciseTokenCount(cacheKey, count, current)
	}
	return count, true
}

func (e *Engine) preciseInputTokenCountSupported(ctx context.Context) bool {
	caps, err := e.providerCapabilities(ctx)
	if err != nil {
		e.reportPreciseTokenCountSupportFailure(err)
		return false
	}
	if !caps.SupportsRequestInputTokenCount {
		return false
	}
	support, ok := e.llm.(llm.RequestInputTokenCountSupportClient)
	if !ok {
		return true
	}
	supported, err := support.SupportsRequestInputTokenCount(ctx)
	if err != nil {
		e.reportPreciseTokenCountSupportFailure(err)
		return false
	}
	return supported
}

func (e *Engine) reportPreciseTokenCountSupportFailure(err error) {
	if err == nil {
		return
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		message = "unknown exact token counting support failure"
	}
	entryText := fmt.Sprintf("Exact token counting availability check failed: %s. Falling back to a local token estimate.", message)
	if persistErr := e.steerPersistedDiagnosticEntry(
		"",
		preciseTokenCountSupportDiagnostic,
		"error",
		entryText,
	); persistErr != nil {
		e.AppendCommittedEntry("error", fmt.Sprintf("%s Diagnostic persistence failed: %v", entryText, persistErr))
	}
}

func (e *Engine) reportPreciseTokenCountFailure(err error) {
	if err == nil {
		return
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		message = "unknown exact token counting failure"
	}
	entryText := fmt.Sprintf("Exact token counting failed: %s. Falling back to a local token estimate.", message)
	if persistErr := e.steerPersistedDiagnosticEntry(
		"",
		preciseTokenCountFailureDiagnostic,
		"error",
		entryText,
	); persistErr != nil {
		e.AppendCommittedEntry("error", fmt.Sprintf("%s Diagnostic persistence failed: %v", entryText, persistErr))
	}
}

func (e *Engine) lookupPreciseTokenCount(cacheKey string, current bool) (int, bool) {
	if strings.TrimSpace(cacheKey) == "" || e.modelRequests().TokenUsage() == nil {
		return 0, false
	}
	if current {
		if cached, ok := e.modelRequests().TokenUsage().lookupCurrent(cacheKey); ok {
			return cached, true
		}
	}
	return e.modelRequests().TokenUsage().lookup(cacheKey)
}

func (e *Engine) storePreciseTokenCount(cacheKey string, count int, current bool) {
	if strings.TrimSpace(cacheKey) == "" || count <= 0 || e.modelRequests().TokenUsage() == nil {
		return
	}
	e.modelRequests().TokenUsage().store(cacheKey, count, current)
}

func (e *Engine) lookupCurrentPreciseInputTokens() (int, bool) {
	if e.modelRequests().TokenUsage() == nil {
		return 0, false
	}
	return e.modelRequests().TokenUsage().lookupCurrent("")
}

// markCurrentRequestShapeDirty invalidates the current-context exact token count
// whenever the next provider request may differ from the previously counted one.
func (e *Engine) markCurrentRequestShapeDirty() {
	tracker := e.modelRequests().TokenUsage()
	if tracker == nil {
		return
	}
	tracker.invalidateCurrent(tokenUsageMutationPlain)
}

func (e *Engine) markCurrentRequestShapeDirtyForSignificantMutation() {
	tracker := e.modelRequests().TokenUsage()
	if tracker == nil {
		return
	}
	tracker.invalidateCurrent(tokenUsageMutationSignificant)
}

func (e *Engine) resetCurrentPreciseInputTracking() {
	tracker := e.modelRequests().TokenUsage()
	if tracker == nil {
		return
	}
	tracker.invalidateCurrent(tokenUsageMutationHardReset)
}

func (e *Engine) shouldRefreshCurrentPreciseInputTokens(limit int, critical bool) bool {
	if limit <= 0 || e.modelRequests().TokenUsage() == nil {
		return false
	}
	return e.modelRequests().TokenUsage().currentCheckpointDue(e.estimatedCurrentTokenUsage(), limit, critical)
}

func (e *Engine) estimatedCurrentTokenUsage() int {
	estimated := 0
	if e != nil {
		estimated = e.transcriptRuntimeState().EstimatedProviderTokens()
	}
	if e.modelRequests().TokenUsage() != nil {
		if baseline, ok := e.modelRequests().TokenUsage().estimateCurrentInputTokens(estimated); ok {
			return baseline
		}
	}
	if estimated > 0 {
		return estimated
	}
	usage := e.usageTrackingState().Last()
	if usage.InputTokens > 0 {
		return usage.InputTokens
	}
	return 0
}

func (e *Engine) currentTokenUsage() int {
	if precise, ok := e.lookupCurrentPreciseInputTokens(); ok {
		return precise
	}
	return e.estimatedCurrentTokenUsage()
}

func (e *Engine) compactNow(ctx context.Context, stepID string, mode compactionMode, instructionsInput compactionInstructionsInput, includeManualCarryover bool) (compactionResult, session.CommitReceipt, error) {
	planningSnapshot := e.compactionPlanningSnapshot()
	planner := e.compactionPlannerState()
	if planner.mode(planningSnapshot.compactionMode) == "none" {
		if mode == compactionModeAuto {
			return compactionResult{}, session.CommitReceipt{}, nil
		}
		return compactionResult{}, session.CommitReceipt{}, errCompactionDisabledModeNone
	}

	input := e.transcriptRuntimeState().SnapshotItems()
	if len(input) == 0 {
		return compactionResult{}, session.CommitReceipt{}, nil
	}

	_ = e.resolveContextWindowTokens(ctx)

	caps, err := e.providerCapabilities(ctx)
	if err != nil {
		return compactionResult{}, session.CommitReceipt{}, err
	}
	providerID := strings.TrimSpace(caps.ProviderID)
	if providerID == "" {
		providerID = "unknown"
	}

	if err := newCompactionPersistence(e).emitStatus(stepID, EventCompactionStarted, mode, "selector", providerID, nil, 0, ""); err != nil {
		return compactionResult{}, session.CommitReceipt{}, err
	}

	instructions := compactionInstructions(instructionsInput)
	manualCarryover := ""
	if mode == compactionModeManual && includeManualCarryover {
		manualCarryover = lastVisibleUserMessageSinceLatestCompaction(input)
	}
	var result compactionResult
	enginePlan := planner.enginePlan(planningSnapshot, caps)
	if enginePlan.engineKind == compactionEngineRemote {
		result, err = e.compactRemote(ctx, stepID, input, providerID, instructions)
		if err != nil && enginePlan.fallbackToLocalOnBadCheckpoint && errors.Is(err, errRemoteCompactionMissingCheckpoint) {
			result, err = e.compactLocal(ctx, input, providerID, instructions, mode)
		}
	} else {
		result, err = e.compactLocal(ctx, input, providerID, instructions, mode)
	}
	if err != nil {
		statusErr := newCompactionPersistence(e).emitStatus(stepID, EventCompactionFailed, mode, result.engine, providerID, result.trimmedItemsCount, 0, err.Error())
		return compactionResult{}, session.CommitReceipt{}, errors.Join(err, statusErr)
	}

	if len(result.items) == 0 {
		err := errors.New("compaction returned empty replacement history")
		statusErr := newCompactionPersistence(e).emitStatus(stepID, EventCompactionFailed, mode, result.engine, providerID, result.trimmedItemsCount, 0, err.Error())
		return compactionResult{}, session.CommitReceipt{}, errors.Join(err, statusErr)
	}

	compactionNumber := e.compactionRuntimeState().Count() + 1
	result.items = withCompactionSummaryLabel(
		result.items,
		fmt.Sprintf("Context compacted for the %s time.", ordinal(compactionNumber)),
	)
	postReplacementMeta, err := e.compactionReinjectedMetaMessages(ctx)
	if err != nil {
		statusErr := newCompactionPersistence(e).emitStatus(stepID, EventCompactionFailed, mode, result.engine, providerID, result.trimmedItemsCount, 0, err.Error())
		return compactionResult{}, session.CommitReceipt{}, errors.Join(err, statusErr)
	}
	// Reinject canonical generation context as part of the single
	// history_replaced commit. The rebuilt active list is born with all runtime
	// context atomically, and the summary precedes it in both provider and
	// transcript order.
	replacementItems := append(llm.CloneResponseItems(result.items), llm.ItemsFromMessages(postReplacementMeta)...)
	if mode == compactionModeManual {
		if carryover, ok := manualCompactionCarryoverMessage(manualCarryover); ok {
			replacementItems = append(replacementItems, llm.ItemsFromMessages([]llm.Message{carryover})...)
		}
	}
	replacementReceipt, replacementErr := newCompactionPersistence(e).replaceHistory(stepID, result.engine, mode, replacementItems)
	if !replacementReceipt.Committed {
		if replacementErr == nil {
			replacementErr = errors.New("history replacement returned an uncommitted receipt without an error")
		}
		statusErr := newCompactionPersistence(e).emitStatus(stepID, EventCompactionFailed, mode, result.engine, providerID, result.trimmedItemsCount, 0, replacementErr.Error())
		return compactionResult{}, replacementReceipt, errors.Join(replacementErr, statusErr)
	}
	finalizationErr := replacementErr
	if strings.TrimSpace(result.summary) != "" && result.engine != "local" {
		summary := strings.TrimSpace(result.summary)
		if err := e.steer(stepID, steerLocalEntryIntent(storedLocalEntry{Role: "compaction_summary", Text: summary})); err != nil {
			finalizationErr = errors.Join(finalizationErr, err)
		}
	}
	if result.overflowRepair.Collapsed() {
		if err := e.steer(stepID, steerLocalEntryIntent(storedLocalEntry{Role: string(transcript.EntryRoleDeveloperErrorFeedback), Text: fmt.Sprintf(
			"Context compaction succeeded after collapsing tool payloads: %d shell outputs, %d patch inputs, ~%d tokens omitted. Full original tool payloads remain in pre-compaction transcript history but are omitted from the compacted model context.",
			result.overflowRepair.ShellOutputsCollapsed,
			result.overflowRepair.PatchInputsCollapsed,
			result.overflowRepair.EstimatedSavedTokens,
		)})); err != nil {
			finalizationErr = errors.Join(finalizationErr, err)
		}
	}
	if mode == compactionModeHandoff {
		if err := newCompactionCarryoverCoordinator(e).appendHandoffFutureMessage(stepID); err != nil {
			finalizationErr = errors.Join(finalizationErr, err)
		}
	}
	compactionNumber = e.compactionRuntimeState().Count()
	windowTokens := result.usage.WindowTokens
	if windowTokens <= 0 {
		windowTokens = e.compactionPlannerState().contextWindowTokens(e.compactionPlanningSnapshot())
	}
	inputTokens := estimateItemsTokens(e.transcriptRuntimeState().SnapshotItems())
	if preciseInput, ok := e.currentInputTokensPreciselyWithoutPromptRefresh(ctx); ok {
		inputTokens = preciseInput
	}
	compactedUsage := llm.Usage{
		InputTokens:  inputTokens,
		OutputTokens: 0,
		WindowTokens: windowTokens,
	}
	usageReceipt, usageErr := e.recordLastUsage(compactedUsage)
	if !usageReceipt.Committed {
		e.setLastUsage(compactedUsage)
	}
	if usageErr != nil {
		finalizationErr = errors.Join(finalizationErr, usageErr)
	}
	staleResult, staleErr := e.store.MarkLockedPromptFacingSnapshotsStale()
	if staleResult.Committed && staleResult.Locked != nil {
		e.lockedContractState().Set(*staleResult.Locked)
	}
	if staleErr != nil {
		finalizationErr = errors.Join(finalizationErr, staleErr)
	}

	if err := newCompactionPersistence(e).emitStatus(stepID, EventCompactionCompleted, mode, result.engine, providerID, result.trimmedItemsCount, compactionNumber, ""); err != nil {
		finalizationErr = errors.Join(finalizationErr, err)
	}
	return result, replacementReceipt, finalizationErr
}

func lastVisibleUserMessageSinceLatestCompaction(items []llm.ResponseItem) string {
	start := 0
	for i := len(items) - 1; i >= 0; i-- {
		if !isCompactionBoundaryItem(items[i]) {
			continue
		}
		start = i + 1
		break
	}
	for i := len(items) - 1; i >= start; i-- {
		item := items[i]
		if item.Type != llm.ResponseItemTypeMessage ||
			item.Role == nil ||
			*item.Role != llm.RoleUser {
			continue
		}
		if item.MessageType != nil &&
			*item.MessageType == llm.MessageTypeCompactionSummary {
			continue
		}
		content, present := textutil.OptionalTrimmed(item.Content)
		if !present {
			continue
		}
		return content
	}
	return ""
}

func (e *Engine) handoffRuntimeState() *handoffRuntimeState {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.handoffState == nil {
		e.handoffState = newHandoffRuntimeState()
	}
	return e.handoffState
}

func (e *Engine) applyPendingHandoffIfNeeded(ctx context.Context, stepID string) (bool, error) {
	if futureMessage := e.handoffRuntimeState().FutureMessageSnapshot(); futureMessage != "" {
		message, ok := handoffFutureAgentMessage(futureMessage)
		if !ok {
			return false, nil
		}
		receipt, err := e.steerWithCommitReceipt(stepID, steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{message}))
		if receipt.Committed {
			e.handoffRuntimeState().ClearFutureMessage()
		}
		if err != nil {
			return false, err
		}
		return false, nil
	}
	req := e.handoffRuntimeState().RequestSnapshot()
	if req == nil {
		return false, nil
	}
	instructions, err := newCompactionInstructionsInput(req.summarizerPrompt)
	if err != nil {
		return false, err
	}
	if _, receipt, err := e.compactNow(ctx, stepID, compactionModeHandoff, instructions, false); err != nil {
		if receipt.Committed {
			e.handoffRuntimeState().ClearRequest()
		}
		if e.handoffRuntimeState().FutureMessageSnapshot() != "" {
			e.handoffRuntimeState().ClearRequest()
		}
		return false, err
	}
	e.handoffRuntimeState().ClearRequest()
	return true, nil
}

func withCompactionSummaryLabel(items []llm.ResponseItem, label string) []llm.ResponseItem {
	label = strings.TrimSpace(label)
	if label == "" || len(items) == 0 {
		return llm.CloneResponseItems(items)
	}
	out := llm.CloneResponseItems(items)
	for idx := range out {
		if out[idx].MessageType == nil ||
			*out[idx].MessageType != llm.MessageTypeCompactionSummary {
			continue
		}
		out[idx].CompactContent = textutil.Value(label)
		return out
	}
	return out
}

func (e *Engine) compactionPlannerState() *compactionPlanner {
	if e == nil || e.compactionPlanner == nil {
		return newCompactionPlanner()
	}
	return e.compactionPlanner
}

func (e *Engine) compactionPlanningSnapshot() compactionPlanningSnapshot {
	if e == nil {
		return compactionPlanningSnapshot{autoCompactionEnabled: true}
	}
	e.mu.Lock()
	autoEnabled := true
	if e.cfg.AutoCompactionEnabled != nil {
		autoEnabled = *e.cfg.AutoCompactionEnabled
	}
	snapshot := compactionPlanningSnapshot{
		autoCompactionEnabled:         autoEnabled,
		compactionMode:                e.cfg.CompactionMode,
		autoCompactTokenLimit:         e.cfg.AutoCompactTokenLimit,
		preSubmitCompactionLeadTokens: e.cfg.PreSubmitCompactionLeadTokens,
		contextWindowTokens:           e.cfg.ContextWindowTokens,
		effectiveContextWindowPercent: e.cfg.EffectiveContextWindowPercent,
		maxOutputTokens:               e.cfg.MaxTokens,
	}
	e.mu.Unlock()
	snapshot.lockedMaxOutputTokens = e.lockedContractState().MaxOutputToken()
	snapshot.lastUsage = e.usageTrackingState().Last()
	snapshot.currentUsedTokens = e.currentTokenUsage()
	return snapshot
}

func newCompactionInstructionsInput(args string) (compactionInstructionsInput, error) {
	if args == "" {
		return compactionInstructionsInput{}, nil
	}
	trimmed := strings.TrimSpace(args)
	if trimmed == "" {
		return compactionInstructionsInput{}, errors.New("additional compaction instructions cannot be blank")
	}
	return compactionInstructionsInput{additional: &trimmed}, nil
}

func compactionInstructions(input compactionInstructionsInput) string {
	instructions := prompts.CompactionPrompt
	if input.additional == nil {
		return instructions
	}
	instructions = strings.TrimRight(instructions, "\n")
	return instructions + "\n\n" + additionalCompactionInstructionsHeader + "\n " + *input.additional
}
