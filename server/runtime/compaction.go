package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"core/prompts"
	"core/server/llm"
	"core/server/session"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
	"core/shared/textutil"
	"core/shared/transcript"
)

type compactionMode string

type compactionInstructionsInput struct {
	additional *string
}

const (
	compactionModeAuto                   compactionMode = "auto"
	compactionModeHandoff                compactionMode = "handoff"
	compactionModeManual                 compactionMode = "manual"
	compactionModeWorkflowPostCompletion compactionMode = "workflow_post_completion"

	compactionSoonReminderPercent          = 85
	compactionPreservedUserMessageMaxChars = 4_000

	additionalCompactionInstructionsHeader                 = "# Additional user instructions or commentary for this task:"
	compactionPreservedUserMessageHeader                   = "# Last user message before handoff (work may have been done after it was sent):"
	workflowPostCompletionCompactionAdditionalInstructions = "The current Workflow assignment is complete. Summarize the completed assignment and its durable handoff as completed work. Do not describe the assignment as ongoing, and do not add a current-assignment reminder."
	handoffDisabledByUserMessage                           = "User disabled the handoff manually for now. They do not want you to hand off at this time, so please keep working or retry this tool later."
	handoffTooEarlyMessage                                 = "It's too early to handoff right now. Don't worry, you still have plenty of time and memory to finish your work, so continue the current task for now. Only retry trigger_handoff after an explicit developer message says handoff is enabled."
	localCompactionToolsDisabledMessage                    = "Tools are disabled during compaction. Do NOT attempt to call any tools. Produce only the requested summary."
	localCompactionToolCallRetries                         = 3
)

var (
	ErrManualCompactionTooSoon = serverapi.ErrManualCompactionTooSoon
	ErrManualCompactionActive  = serverapi.ErrManualCompactionActive

	// errHandoffDisabledByUser is returned when the user has disabled handoff and the agent requests one.
	errHandoffDisabledByUser = errors.New(handoffDisabledByUserMessage)
	// errHandoffTooEarly is returned when the agent requests a handoff before the trigger_handoff tool is enabled.
	errHandoffTooEarly = errors.New(handoffTooEarlyMessage)
	// errCompactionDisabledModeNone is returned when manual compaction is requested while compaction_mode=none.
	errCompactionDisabledModeNone = serverapi.ErrManualCompactionDisabled
)

type compactionResult struct {
	engine                      string
	items                       []llm.ResponseItem
	usage                       llm.Usage
	trimmedItemsCount           *int
	overflowRepair              compactionOverflowRepairStats
	localToolCallRejectionCount int
	provider                    string
}

type manualCompactionSelectionFailure struct {
	cause error
}

func (e *manualCompactionSelectionFailure) Error() string {
	if e == nil || e.cause == nil {
		return "manual compaction selection failed"
	}
	return e.cause.Error()
}

func (e *manualCompactionSelectionFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

type defaultContextCompactor struct {
	engine *Engine
	steps  exclusiveStepLifecycle
}

func (e *Engine) CompactContext(ctx context.Context, args string) error {
	e.ensureOrchestrationCollaborators()
	_, err := e.compactionFlow.CompactContextWithAcceptance(ctx, runtimeids.NewCompactionRequestID(), args, nil, nil)
	return err
}

func (e *Engine) CompactContextWithActiveHook(ctx context.Context, args string, onActive func()) (session.CommitReceipt, error) {
	e.ensureOrchestrationCollaborators()
	return e.compactionFlow.CompactContextWithAcceptance(ctx, runtimeids.NewCompactionRequestID(), args, onActive, nil)
}

func (e *Engine) CompactContextWithAcceptance(ctx context.Context, args string, accept CommandAcceptance) (session.CommitReceipt, error) {
	return e.CompactContextForRequestWithAcceptance(ctx, runtimeids.NewCompactionRequestID(), args, accept)
}

func (e *Engine) CompactContextForRequestWithAcceptance(
	ctx context.Context,
	requestID runtimeids.CompactionRequestID,
	args string,
	accept CommandAcceptance,
) (session.CommitReceipt, error) {
	e.ensureOrchestrationCollaborators()
	return e.compactionFlow.CompactContextWithAcceptance(ctx, requestID, args, nil, accept)
}

func (e *Engine) CompactContextForPreSubmit(ctx context.Context, text string) error {
	e.ensureOrchestrationCollaborators()
	_, err := e.compactionFlow.CompactContextForPreSubmitWithAcceptance(ctx, text, nil, nil)
	return err
}

func (e *Engine) CompactContextForWorkflowContinuation(ctx context.Context) error {
	e.ensureOrchestrationCollaborators()
	_, err := e.compactionFlow.CompactContextForWorkflowContinuation(ctx)
	return err
}

func (e *Engine) CompactContextForWorkflowPostCompletion(ctx context.Context) (session.CommitReceipt, error) {
	e.ensureOrchestrationCollaborators()
	return e.compactionFlow.CompactContextForWorkflowPostCompletion(ctx)
}

// SubmitWorkflowContinuationTurn runs the existing lazy CAC operation and
// consumes a committed Workflow Pre-Compaction boundary only after the target
// turn succeeds. A failed target attempt therefore preserves the boundary for
// the existing Resume path.
func (e *Engine) SubmitWorkflowContinuationTurn(ctx context.Context) (WorkflowTurnResult, error) {
	if e == nil {
		return WorkflowTurnResult{}, errors.New("runtime engine is required")
	}
	if !e.compactionRuntimeState().WorkflowPostCompletionBoundary() {
		if err := e.CompactContextForWorkflowContinuation(ctx); err != nil {
			return WorkflowTurnResult{}, err
		}
	}
	result, err := e.SubmitWorkflowTurn(ctx)
	if err != nil {
		return WorkflowTurnResult{}, err
	}
	e.compactionRuntimeState().ApplyWorkflowPostCompletionActivity(workflowPostCompletionDurableActivity)
	return result, nil
}

func (e *Engine) WorkflowPreCompactionTokenLimit() (int, error) {
	if e == nil {
		return 0, errors.New("runtime engine is required")
	}
	if e.cfg.WorkflowPreCompactionTokenLimit <= 0 {
		return 0, errors.New("workflow pre-compaction token limit must be positive")
	}
	return e.cfg.WorkflowPreCompactionTokenLimit, nil
}

func (e *Engine) CompactContextForPreSubmitWithActiveHook(ctx context.Context, text string, onActive func()) (session.CommitReceipt, error) {
	e.ensureOrchestrationCollaborators()
	return e.compactionFlow.CompactContextForPreSubmitWithAcceptance(ctx, text, onActive, nil)
}

func (e *Engine) CompactContextForPreSubmitWithAcceptance(ctx context.Context, text string, accept CommandAcceptance) (session.CommitReceipt, error) {
	e.ensureOrchestrationCollaborators()
	return e.compactionFlow.CompactContextForPreSubmitWithAcceptance(ctx, text, nil, accept)
}

func (e *Engine) TriggerHandoff(ctx context.Context, stepID string, activeCall llm.ToolCall, summarizerPrompt string, futureAgentMessage string) (string, bool, error) {
	e.ensureOrchestrationCollaborators()
	return e.compactionFlow.TriggerHandoff(ctx, stepID, activeCall, summarizerPrompt, futureAgentMessage)
}

func (c *defaultContextCompactor) CompactContextWithAcceptance(
	ctx context.Context,
	requestID runtimeids.CompactionRequestID,
	args string,
	onActive func(),
	accept CommandAcceptance,
) (session.CommitReceipt, error) {
	normalizedGuidance := strings.Join(strings.Fields(args), " ")
	admission := runtimeinput.ManualCompactionAdmission{}
	if normalizedGuidance != "" {
		admission.Guidance = &normalizedGuidance
	}
	instructions, err := newCompactionInstructionsInput(normalizedGuidance)
	if err != nil {
		return session.CommitReceipt{}, err
	}
	if requestID.IsZero() {
		return session.CommitReceipt{}, errors.New("compaction request id is required")
	}
	return c.scheduleManualCompaction(ctx, requestID, instructions, admission, onActive, accept)
}

func (e *Engine) CompactContextAdmissionForRequestWithAcceptance(
	ctx context.Context,
	requestID runtimeids.CompactionRequestID,
	admission runtimeinput.ManualCompactionAdmission,
	accept CommandAcceptance,
) (session.CommitReceipt, error) {
	e.ensureOrchestrationCollaborators()
	return e.compactionFlow.CompactContextAdmissionWithAcceptance(ctx, requestID, admission, accept)
}

func (c *defaultContextCompactor) CompactContextAdmissionWithAcceptance(
	ctx context.Context,
	requestID runtimeids.CompactionRequestID,
	admission runtimeinput.ManualCompactionAdmission,
	accept CommandAcceptance,
) (session.CommitReceipt, error) {
	if requestID.IsZero() {
		return session.CommitReceipt{}, errors.New("compaction request id is required")
	}
	if err := admission.Validate(); err != nil {
		return session.CommitReceipt{}, err
	}
	args := ""
	if admission.Guidance != nil {
		args = *admission.Guidance
	}
	instructions, err := newCompactionInstructionsInput(args)
	if err != nil {
		return session.CommitReceipt{}, err
	}
	return c.scheduleManualCompaction(ctx, requestID, instructions, admission, nil, accept)
}

func (c *defaultContextCompactor) scheduleManualCompaction(
	ctx context.Context,
	requestID runtimeids.CompactionRequestID,
	instructions compactionInstructionsInput,
	admission runtimeinput.ManualCompactionAdmission,
	onActive func(),
	accept CommandAcceptance,
) (session.CommitReceipt, error) {
	e := c.engine
	item, err := manualCompactionPendingWorkItem(requestID, admission)
	if err != nil {
		return session.CommitReceipt{}, err
	}
	err = e.scheduleOperationalPendingWork(ctx, operationalPendingWorkRequest{
		item:   item,
		accept: accept,
		run: func(pendingCtx context.Context, reservation *exclusiveStepReservation, pendingItem runtimeinput.PendingWorkItem) error {
			receipt, runErr := c.compactContext(
				pendingCtx,
				ActiveKindCompaction,
				compactionModeManual,
				&requestID,
				instructions,
				true,
				reservation,
				onActive,
				nil,
				true,
				nil,
			)
			var selectionFailure *manualCompactionSelectionFailure
			if runErr != nil &&
				!receipt.Committed &&
				!errors.As(runErr, &selectionFailure) &&
				!errors.Is(runErr, errPendingOperationalWorkRemoved) {
				runErr = errors.Join(runErr, e.publishPendingWorkTechnicalRestoration(pendingItem))
			}
			return runErr
		},
	})
	return session.CommitReceipt{}, err
}

func (c *defaultContextCompactor) CompactContextForWorkflowContinuation(ctx context.Context) (session.CommitReceipt, error) {
	return c.compactManualContext(ctx, compactionInstructionsInput{}, nil, nil, false)
}

func (c *defaultContextCompactor) CompactContextForWorkflowPostCompletion(ctx context.Context) (session.CommitReceipt, error) {
	return c.compactContext(
		ctx,
		ActiveKindCompaction,
		compactionModeWorkflowPostCompletion,
		nil,
		compactionInstructionsInput{},
		false,
		nil,
		nil,
		nil,
		false,
		nil,
	)
}

func (c *defaultContextCompactor) compactManualContext(ctx context.Context, instructions compactionInstructionsInput, onActive func(), accept CommandAcceptance, requireEligibility bool) (session.CommitReceipt, error) {
	reservation := &exclusiveStepReservation{
		Kind:      exclusiveStepReservationManualCompaction,
		queueable: true,
	}
	if err := c.steps.AcquireReservation(reservation); err != nil {
		return session.CommitReceipt{}, err
	}
	defer c.steps.ReleaseReservation(reservation)
	return c.compactContext(ctx, ActiveKindCompaction, compactionModeManual, nil, instructions, true, reservation, onActive, accept, requireEligibility, nil)
}

func (c *defaultContextCompactor) CompactContextForPreSubmitWithAcceptance(ctx context.Context, text string, onActive func(), accept CommandAcceptance) (session.CommitReceipt, error) {
	return c.compactContext(ctx, ActiveKindPreSubmitCompaction, compactionModeManual, nil, compactionInstructionsInput{}, false, nil, onActive, accept, false, &text)
}

func isAgentStepKind(kind ActiveKind) bool {
	switch kind {
	case ActiveKindUserTurn, ActiveKindWorkflowTurn, ActiveKindGoalLoop, ActiveKindBackground:
		return true
	default:
		return false
	}
}

func isInterruptibleAgentTurn(kind ActiveKind) bool {
	switch kind {
	case ActiveKindUserTurn, ActiveKindWorkflowTurn, ActiveKindGoalLoop:
		return true
	default:
		return false
	}
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
	if planner.mode(planningSnapshot.policy) == "none" {
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

func (c *defaultContextCompactor) compactContext(
	ctx context.Context,
	activeKind ActiveKind,
	mode compactionMode,
	requestID *runtimeids.CompactionRequestID,
	instructions compactionInstructionsInput,
	includePreservedUserMessage bool,
	reservation *exclusiveStepReservation,
	onActive func(),
	accept CommandAcceptance,
	requireEligibility bool,
	preSubmitText *string,
) (session.CommitReceipt, error) {
	e := c.engine
	e.pauseQueuedUserAutoDrain()
	defer e.resumeQueuedUserAutoDrain()
	var receipt session.CommitReceipt
	err := runExclusiveStepWhenIdle(ctx, c.steps, activeKind, reservation, func(stepCtx context.Context, stepID string) error {
		if requireEligibility {
			if e.compactionRuntimeState().ActiveSnapshot() != nil {
				return c.reportManualCompactionSelectionFailure(stepID, requestID, ErrManualCompactionActive)
			}
			planningSnapshot := e.compactionPlanningSnapshot()
			if e.compactionPlannerState().mode(planningSnapshot.policy) == "none" {
				return c.reportManualCompactionSelectionFailure(stepID, requestID, errCompactionDisabledModeNone)
			}
			if !e.compactionRuntimeState().ManualCompactionEligible() {
				return c.reportManualCompactionSelectionFailure(stepID, requestID, ErrManualCompactionTooSoon)
			}
		}
		if preSubmitText != nil {
			eligible, err := c.ShouldCompactBeforeUserMessage(stepCtx, *preSubmitText)
			if err != nil {
				return err
			}
			if !eligible {
				return nil
			}
		}
		if onActive != nil {
			onActive()
		}
		if err := e.ensureMetaContextForCompaction(stepCtx, stepID); err != nil {
			if requireEligibility {
				return c.reportManualCompactionTechnicalFailure(stepID, requestID, err)
			}
			return err
		}
		_, compactReceipt, err := e.compactNowWithAcceptance(stepCtx, stepID, activeKind, requestID, mode, instructions, includePreservedUserMessage, accept)
		receipt = compactReceipt
		if err == nil || receipt.Committed {
			e.handoffRuntimeState().ClearRequest()
		}
		return err
	})
	return receipt, err
}

func (c *defaultContextCompactor) reportManualCompactionSelectionFailure(
	stepID string,
	requestID *runtimeids.CompactionRequestID,
	cause error,
) error {
	reported := c.reportManualCompactionTechnicalFailure(stepID, requestID, cause)
	if reported == nil {
		return nil
	}
	return &manualCompactionSelectionFailure{cause: reported}
}

func (c *defaultContextCompactor) reportManualCompactionTechnicalFailure(
	stepID string,
	requestID *runtimeids.CompactionRequestID,
	cause error,
) error {
	if cause == nil {
		return nil
	}
	emitErr := newCompactionPersistence(c.engine).emitStatus(
		stepID,
		requestID,
		EventCompactionFailed,
		compactionModeManual,
		"selector",
		"unknown",
		nil,
		0,
		cause.Error(),
	)
	return errors.Join(cause, emitErr)
}

func (e *Engine) autoCompactIfNeeded(ctx context.Context, stepID string, mode compactionMode) error {
	e.ensureOrchestrationCollaborators()
	return e.compactionFlow.AutoCompactIfNeeded(ctx, stepID, mode)
}

func (e *Engine) maybeReserveEagerCompaction(activeKind ActiveKind, resultKind LiveRunResultKind, assistant llm.Message) {
	if e == nil || e.isWorkflowAgent() || resultKind != LiveRunResultAssistantFinalAnswer || isBlankFinalAnswer(assistant) {
		return
	}
	switch activeKind {
	case ActiveKindUserTurn, ActiveKindGoalLoop, ActiveKindBackground:
	default:
		return
	}
	planningSnapshot := e.compactionPlanningSnapshot()
	planner := e.compactionPlannerState()
	if !planner.autoCompactionAvailable(planningSnapshot) || !planner.eagerCompactionEligible(planningSnapshot) {
		return
	}
	reservation := &exclusiveStepReservation{
		Kind:      exclusiveStepReservationManualCompaction,
		queueable: true,
	}
	if err := e.stepLifecycle.AcquireReservation(reservation); err != nil {
		e.surfaceRunError(err)
		return
	}
	if !e.launchLifecycleTask(func(ctx context.Context) *resultGroupFatal {
		defer e.stepLifecycle.ReleaseReservation(reservation)
		err := runExclusiveStepWhenIdle(ctx, e.stepLifecycle, ActiveKindCompaction, reservation, func(stepCtx context.Context, stepID string) error {
			planningSnapshot := e.compactionPlanningSnapshot()
			planner := e.compactionPlannerState()
			if !planner.autoCompactionAvailable(planningSnapshot) || !planner.eagerCompactionEligible(planningSnapshot) {
				return nil
			}
			_, receipt, compactErr := e.compactNow(stepCtx, stepID, compactionModeAuto, compactionInstructionsInput{}, false)
			if compactErr == nil || receipt.Committed {
				e.handoffRuntimeState().ClearRequest()
			}
			return compactErr
		})
		e.surfaceRunError(err)
		return nil
	}) {
		e.stepLifecycle.ReleaseReservation(reservation)
	}
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
	estimatedCurrentTotal := e.currentTokenUsage() + reservedOutput
	if preSubmitLimit > 0 && estimatedCurrentTotal >= preSubmitLimit {
		return true, nil
	}
	promptEstimate := estimateItemsTokens(llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleUser, Content: textutil.Value(text)}}))
	return estimatedCurrentTotal+promptEstimate >= limit, nil
}

func (e *Engine) currentModel() string {
	if model := e.lockedContractState().Model(); model != "" {
		return model
	}
	return strings.TrimSpace(e.cfg.Model)
}

func (e *Engine) usageAtOrAboveLimit(_ context.Context, limit int) bool {
	if limit <= 0 {
		return false
	}
	reservedOutput := e.compactionPlannerState().reservedOutputTokens(e.compactionPlanningSnapshot())
	return e.currentTokenUsage()+reservedOutput >= limit
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
	return e.estimatedCurrentTokenUsage()
}

func (e *Engine) compactNow(ctx context.Context, stepID string, mode compactionMode, instructionsInput compactionInstructionsInput, includePreservedUserMessage bool) (compactionResult, session.CommitReceipt, error) {
	return e.compactNowWithAcceptance(ctx, stepID, ActiveKindCompaction, nil, mode, instructionsInput, includePreservedUserMessage, nil)
}

func (e *Engine) compactNowWithAcceptance(
	ctx context.Context,
	stepID string,
	activeKind ActiveKind,
	requestID *runtimeids.CompactionRequestID,
	mode compactionMode,
	instructionsInput compactionInstructionsInput,
	includePreservedUserMessage bool,
	accept CommandAcceptance,
) (compactionResult, session.CommitReceipt, error) {
	planningSnapshot := e.compactionPlanningSnapshot()
	planner := e.compactionPlannerState()
	if planner.mode(planningSnapshot.policy) == "none" {
		if mode == compactionModeAuto {
			return compactionResult{}, session.CommitReceipt{}, nil
		}
		return compactionResult{}, session.CommitReceipt{}, errCompactionDisabledModeNone
	}

	input := e.transcriptRuntimeState().SnapshotItems()
	if len(input) == 0 {
		return compactionResult{}, session.CommitReceipt{}, nil
	}

	providerID := "unknown"
	persistence := newCompactionPersistence(e)
	compactionFailure := func(result compactionResult, err error) error {
		feedbackErr := e.emitLocalCompactionToolCallFeedback(stepID, result.localToolCallRejectionCount)
		if accept != nil {
			return errors.Join(err, feedbackErr)
		}
		return errors.Join(err, feedbackErr, persistence.emitStatus(stepID, requestID, EventCompactionFailed, mode, result.engine, providerID, result.trimmedItemsCount, 0, err.Error()))
	}

	caps, err := e.providerCapabilities(ctx)
	if err != nil {
		return compactionResult{}, session.CommitReceipt{}, compactionFailure(compactionResult{}, err)
	}
	if resolvedProviderID := strings.TrimSpace(caps.ProviderID); resolvedProviderID != "" {
		providerID = resolvedProviderID
	}

	if err := persistence.setActivity(stepID, requestID, mode, e.compactionRuntimeState().Count()+1, activeKind, true); err != nil {
		return compactionResult{}, session.CommitReceipt{}, err
	}
	defer func() {
		if err := persistence.setActivity(stepID, requestID, mode, 0, activeKind, false); err != nil {
			e.surfaceRunError(fmt.Errorf("clear compaction activity: %w", err))
		}
	}()
	if accept == nil {
		if err := persistence.emitStatus(stepID, requestID, EventCompactionStarted, mode, "selector", providerID, nil, 0, ""); err != nil {
			return compactionResult{}, session.CommitReceipt{}, err
		}
	}
	instructions := compactionInstructionsForMode(mode, instructionsInput)
	preservedUserMessageText := lastVisibleUserMessageSinceLatestCompaction(input)
	var result compactionResult
	enginePlan := planner.enginePlan(planningSnapshot)
	var requestKind *llm.CodexRequestKind
	if enginePlan.engineKind == compactionEngineRemote {
		requestKind = llm.CodexRequestKindCompaction.Optional()
	}
	dispatchFactory, err := e.activeDispatchRequestFactory(stepID, requestKind)
	if err != nil {
		return compactionResult{}, session.CommitReceipt{}, compactionFailure(result, err)
	}
	if enginePlan.engineKind == compactionEngineRemote {
		var remoteInput []llm.ResponseItem
		result, remoteInput, err = e.compactRemote(ctx, stepID, input, providerID, instructions, dispatchFactory)
		var checkpointErr *llm.CompactionCheckpointContractError
		if err != nil && errors.As(err, &checkpointErr) {
			if e.cfg.Debug {
				panic(checkpointErr)
			}
			attrs := []slog.Attr{
				slog.String("provider_id", providerID),
				slog.String("checkpoint_reason", string(checkpointErr.Reason)),
				slog.Int("compaction_count", checkpointErr.CompactionCount),
				slog.Int("output_count", checkpointErr.OutputCount),
				slog.Any("output_type_counts", checkpointErr.OutputTypeCounts),
			}
			var providerErr *llm.ProviderAPIError
			if errors.As(err, &providerErr) {
				attrs = append(attrs, slog.Int("status_code", providerErr.StatusCode))
				if requestID, ok := textutil.OptionalTrimmed(providerErr.ProviderRequestID); ok {
					attrs = append(attrs, slog.String("request_id", requestID))
				}
			}
			slog.LogAttrs(ctx, slog.LevelError, "remote compaction returned a malformed checkpoint", attrs...)

			localFactory, factoryErr := e.activeDispatchRequestFactory(stepID, nil)
			if factoryErr != nil {
				err = errors.Join(err, factoryErr)
			} else {
				localResult, localErr := e.compactLocal(ctx, stepID, remoteInput, providerID, instructions, localFactory)
				localResult.overflowRepair = result.overflowRepair.Add(localResult.overflowRepair)
				result = localResult
				if localErr != nil {
					err = errors.Join(err, localErr)
				} else {
					err = nil
				}
			}
		}
	} else {
		result, err = e.compactLocal(ctx, stepID, input, providerID, instructions, dispatchFactory)
	}
	if err != nil {
		return compactionResult{}, session.CommitReceipt{}, compactionFailure(result, err)
	}

	if len(result.items) == 0 {
		err := errors.New("compaction returned empty replacement history")
		return compactionResult{}, session.CommitReceipt{}, compactionFailure(result, err)
	}

	compactionNumber := e.compactionRuntimeState().Count() + 1
	postReplacementMeta, err := e.compactionReinjectedMetaContextProjection(ctx, mode)
	if err != nil {
		return compactionResult{}, session.CommitReceipt{}, compactionFailure(result, err)
	}
	replacementItems := append(llm.ItemsFromMessages(postReplacementMeta.StablePrefix), llm.CloneResponseItems(result.items)...)
	replacementItems = append(replacementItems, llm.ItemsFromMessages(postReplacementMeta.Environment)...)
	if preservedUserMessageText != nil {
		if preservedMessage, ok := compactionPreservedUserMessage(*preservedUserMessageText); ok {
			replacementItems = append(replacementItems, llm.ItemsFromMessages([]llm.Message{preservedMessage})...)
		}
	}
	if mode == compactionModeHandoff {
		if req := e.handoffRuntimeState().RequestSnapshot(); req != nil {
			if futureMessage, ok := handoffFutureAgentMessage(req.futureAgentMessage); ok {
				replacementItems = append(replacementItems, llm.ItemsFromMessages([]llm.Message{futureMessage})...)
			}
		}
	}
	var replacementReceipt session.CommitReceipt
	committed, replacementErr := runCommandAcceptance(accept, func() (bool, error) {
		var err error
		replacementReceipt, err = persistence.replaceHistory(stepID, result.engine, mode, replacementItems)
		return replacementReceipt.Committed, err
	})
	if accept != nil {
		replacementErr = commandAcceptanceResult(committed, replacementErr)
	}
	if !replacementReceipt.Committed {
		if replacementErr == nil {
			replacementErr = errors.New("history replacement returned an uncommitted receipt without an error")
		}
		return compactionResult{}, replacementReceipt, compactionFailure(result, replacementErr)
	}
	e.compactionRuntimeState().SetManualCompactionEligible(false)
	e.persistCompletedCompactionFactsBestEffort(stepID, e.compactionRuntimeState().Count())
	finalizationErr := replacementErr
	if err := e.emitLocalCompactionToolCallFeedback(stepID, result.localToolCallRejectionCount); err != nil {
		finalizationErr = errors.Join(finalizationErr, err)
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
	compactionNumber = e.compactionRuntimeState().Count()
	windowTokens := result.usage.WindowTokens
	if windowTokens <= 0 {
		windowTokens = e.compactionPlannerState().contextWindowTokens(e.compactionPlanningSnapshot())
	}
	inputTokens := estimateItemsTokens(e.transcriptRuntimeState().SnapshotItems())
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

	if err := persistence.emitStatus(stepID, requestID, EventCompactionCompleted, mode, result.engine, providerID, result.trimmedItemsCount, compactionNumber, ""); err != nil {
		finalizationErr = errors.Join(finalizationErr, err)
	}
	return result, replacementReceipt, finalizationErr
}

func (e *Engine) emitLocalCompactionToolCallFeedback(stepID string, count int) error {
	var err error
	for range count {
		err = errors.Join(err, e.steer(stepID, steerLocalEntryIntent(storedLocalEntry{
			Role: string(transcript.EntryRoleDeveloperErrorFeedback),
			Text: localCompactionToolsDisabledMessage,
		})))
	}
	return err
}

func lastVisibleUserMessageSinceLatestCompaction(items []llm.ResponseItem) *string {
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
		if item.MessageType != nil {
			continue
		}
		content, present := textutil.OptionalTrimmed(item.Content)
		if !present {
			continue
		}
		return textutil.Value(content)
	}
	return nil
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
		preSubmitCompactionLeadTokens: e.cfg.PreSubmitCompactionLeadTokens,
		policy:                        e.contextPolicy,
		maxOutputTokens:               e.cfg.MaxTokens,
	}
	e.mu.Unlock()
	snapshot.lockedMaxOutputTokens = e.lockedContractState().MaxOutputToken()
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

func compactionInstructionsForMode(mode compactionMode, input compactionInstructionsInput) string {
	if mode != compactionModeWorkflowPostCompletion {
		return compactionInstructions(input)
	}
	additional := workflowPostCompletionCompactionAdditionalInstructions
	if input.additional != nil {
		additional = strings.TrimSpace(additional) + "\n\n" + strings.TrimSpace(*input.additional)
	}
	return compactionInstructions(compactionInstructionsInput{additional: &additional})
}
