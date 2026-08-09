package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"core/server/llm"
	"core/server/session"
	"core/shared/textutil"
	"core/shared/transcript"

	"github.com/google/uuid"
)

type manualCompactionResolver struct {
	done    chan struct{}
	once    sync.Once
	receipt session.CommitReceipt
	err     error
}

func newManualCompactionResolver() *manualCompactionResolver {
	return &manualCompactionResolver{done: make(chan struct{})}
}

func (r *manualCompactionResolver) settle(receipt session.CommitReceipt, err error) {
	if r == nil {
		return
	}
	r.once.Do(func() {
		r.receipt = receipt
		r.err = err
		close(r.done)
	})
}

func (r *manualCompactionResolver) wait(ctx context.Context) (session.CommitReceipt, error) {
	if r == nil {
		return session.CommitReceipt{}, errors.New("manual compaction resolver is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-r.done:
		return r.receipt, r.err
	case <-ctx.Done():
		return session.CommitReceipt{}, context.Cause(ctx)
	}
}

type manualCompactionAgendaItem struct {
	id           boundaryAgendaItemID
	stepID       string
	binding      boundaryAgendaBinding
	eligibility  boundaryEligibility
	instructions compactionInstructionsInput
	onActive     func()
	resolver     *manualCompactionResolver
	boundary     *manualCompactionBoundaryContinuation
	order        uint64
}

func (i *manualCompactionAgendaItem) agendaID() boundaryAgendaItemID {
	return i.id
}

func (i *manualCompactionAgendaItem) agendaBinding() boundaryAgendaBinding {
	return i.binding
}

func (i *manualCompactionAgendaItem) agendaEligibility() boundaryEligibility {
	return i.eligibility
}

func (i *manualCompactionAgendaItem) agendaOrder() uint64 {
	return i.order
}

func (i *manualCompactionAgendaItem) setAgendaOrder(order uint64) {
	i.order = order
}

func (i *manualCompactionAgendaItem) settleBoundaryAgenda(err error) {
	i.resolver.settle(session.CommitReceipt{}, err)
}

func (i *manualCompactionAgendaItem) selectLongWork() boundaryLongWork {
	return &manualCompactionSelection{
		id:           i.id,
		stepID:       i.stepID,
		instructions: i.instructions,
		onActive:     i.onActive,
		resolver:     i.resolver,
		boundary:     i.boundary,
	}
}

type manualCompactionSelection struct {
	id           boundaryAgendaItemID
	stepID       string
	instructions compactionInstructionsInput
	onActive     func()
	resolver     *manualCompactionResolver
	boundary     *manualCompactionBoundaryContinuation
}

func (s *manualCompactionSelection) longWorkID() boundaryAgendaItemID {
	return s.id
}

func (s *manualCompactionSelection) runLongWork(ctx context.Context, engine *Engine) error {
	return engine.runManualCompactionSelection(ctx, s)
}

func (s *manualCompactionSelection) settleLongWork(err error) {
	s.resolver.settle(session.CommitReceipt{}, err)
	s.boundary.settleForced(err)
}

type manualCompactionBoundaryContinuation struct {
	engine       *Engine
	grant        AgentStepReducerGrant
	continueTurn bool
	step         activeAgentStep
	complete     func(agentStepBoundaryDecision, error)
	settled      sync.Once
}

func (c *manualCompactionBoundaryContinuation) claimResult() bool {
	if c == nil {
		return true
	}
	claimed := false
	c.settled.Do(func() {
		claimed = true
	})
	return claimed
}

func (c *manualCompactionBoundaryContinuation) settleForced(err error) {
	if c == nil {
		return
	}
	c.settled.Do(func() {
		if c.engine != nil &&
			c.engine.agentSteps.boundary != nil &&
			*c.engine.agentSteps.boundary == c.step {
			c.engine.agentSteps.boundary = nil
		}
		releaseErr := c.grant.Release()
		c.complete(nil, errors.Join(err, releaseErr))
	})
}

type preparedCompactionWork struct {
	result           compactionResult
	replacementItems []llm.ResponseItem
	providerID       string
	windowTokens     int
	started          bool
	skipped          bool
}

type manualCompactionRuntimeResult struct {
	id       boundaryAgendaItemID
	prepared preparedCompactionWork
	err      error
}

func (e *Engine) admitManualCompaction(
	admission runtimeEventAdmission,
	instructions compactionInstructionsInput,
	onActive func(),
) (*manualCompactionResolver, error) {
	planningSnapshot := e.compactionPlanningSnapshot()
	if e.compactionPlannerState().mode(planningSnapshot.compactionMode) == "none" {
		return nil, errCompactionDisabledModeNone
	}
	if e.manualCompactionActive() {
		return nil, ErrManualCompactionActive
	}
	current := e.agentSteps.current
	activeProviderStep := current != nil && current.phase == agentStepProviderRunning
	if !e.compactionRuntimeState().ManualCompactionEligible() && !activeProviderStep {
		return nil, ErrManualCompactionTooSoon
	}
	resolver := newManualCompactionResolver()
	item := &manualCompactionAgendaItem{
		id:           boundaryAgendaItemID("manual-compaction:" + uuid.NewString()),
		stepID:       uuid.NewString(),
		binding:      runtimeBoundaryBinding(),
		eligibility:  boundaryEligibilityIdle,
		instructions: instructions,
		onActive:     onActive,
		resolver:     resolver,
	}
	if activeProviderStep {
		item.binding = scopeBoundaryBinding(current.scopeID, current.origin)
		item.eligibility = boundaryEligibilityStep
	}
	if err := e.boundaryAgenda.accept(item); err != nil {
		return nil, err
	}
	if item.eligibility == boundaryEligibilityIdle {
		if err := e.reduceIdleBoundary(admission); err != nil {
			if !e.boundaryAgenda.discard(item.id, err) {
				resolver.settle(session.CommitReceipt{}, err)
			}
		}
	}
	return resolver, nil
}

func (e *Engine) manualCompactionActive() bool {
	if _, selected := e.longBoundary.selected.(*manualCompactionSelection); selected {
		return true
	}
	if e.stepLifecycle == nil {
		return false
	}
	snapshot := e.stepLifecycle.Snapshot()
	return snapshot != nil &&
		(snapshot.ActiveKind == ActiveKindCompaction ||
			snapshot.ActiveKind == ActiveKindPreSubmitCompaction)
}

func (e *Engine) startNextManualCompactionLongWork(
	admission runtimeEventAdmission,
) error {
	if !e.idleBoundaryReductionEligible() || e.runtimeEvents == nil {
		return nil
	}
	_, ok := e.boundaryAgenda.peekNext(idleBoundarySelection()).(*manualCompactionAgendaItem)
	if !ok {
		return nil
	}
	selected, err := e.longBoundary.selectNext(
		e.boundaryAgenda,
		idleBoundarySelection(),
	)
	if err != nil || selected == nil {
		return err
	}
	selection, ok := selected.(*manualCompactionSelection)
	if !ok {
		panic(fmt.Sprintf(
			"manual compaction selection has unexpected type %T",
			selected,
		))
	}
	return e.launchManualCompactionSelection(admission, selection)
}

func (e *Engine) launchManualCompactionSelection(
	admission runtimeEventAdmission,
	selected *manualCompactionSelection,
) error {
	return e.transferBoundaryLongWork(admission, selected, func(workCtx context.Context) {
		runCtx := workCtx
		var cancel context.CancelCauseFunc
		var stop func() bool
		if selected.boundary != nil {
			if source, ok := e.stepLifecycle.(*defaultExclusiveStepLifecycle); ok {
				if activeCtx := source.activeContext(); activeCtx != nil {
					runCtx, cancel = context.WithCancelCause(workCtx)
					stop = context.AfterFunc(activeCtx, func() {
						cancel(context.Cause(activeCtx))
					})
					if cause := context.Cause(activeCtx); cause != nil {
						cancel(cause)
					}
				}
			}
		}
		if stop != nil {
			defer stop()
		}
		if cancel != nil {
			defer cancel(nil)
		}
		_ = selected.runLongWork(runCtx, e)
	})
}

func (e *Engine) runManualCompactionSelection(
	ctx context.Context,
	selected *manualCompactionSelection,
) error {
	if selected.onActive != nil {
		selected.onActive()
	}
	if err := e.ensureMetaContextForCompaction(ctx, selected.stepID); err != nil {
		e.submitManualCompactionRuntimeResult(manualCompactionRuntimeResult{
			id:  selected.id,
			err: err,
		})
		return nil
	}
	prepared, err := e.prepareCompactionWork(
		ctx,
		selected.stepID,
		compactionModeManual,
		selected.instructions,
		true,
	)
	e.submitManualCompactionRuntimeResult(manualCompactionRuntimeResult{
		id:       selected.id,
		prepared: prepared,
		err:      err,
	})
	return nil
}

func (e *Engine) submitManualCompactionRuntimeResult(
	result manualCompactionRuntimeResult,
) {
	_, err := submitRuntimeEventWithContext(
		e.lifecycleCtx,
		e.lifecycleCtx,
		e,
		result,
		e.applyManualCompactionRuntimeResult,
	)
	if err != nil &&
		!errors.Is(err, context.Canceled) &&
		!errors.Is(err, ErrEngineClosed) {
		e.surfaceRunError(err)
	}
}

func (e *Engine) prepareCompactionWork(
	ctx context.Context,
	stepID string,
	mode compactionMode,
	instructionsInput compactionInstructionsInput,
	includePreservedUserMessage bool,
) (preparedCompactionWork, error) {
	planningSnapshot := e.compactionPlanningSnapshot()
	planner := e.compactionPlannerState()
	if planner.mode(planningSnapshot.compactionMode) == "none" {
		if mode == compactionModeAuto {
			return preparedCompactionWork{skipped: true}, nil
		}
		return preparedCompactionWork{}, errCompactionDisabledModeNone
	}
	input := e.transcriptRuntimeState().SnapshotItems()
	if len(input) == 0 {
		return preparedCompactionWork{skipped: true}, nil
	}
	windowTokens := e.resolveContextWindowTokens(ctx)
	caps, err := e.providerCapabilities(ctx)
	if err != nil {
		return preparedCompactionWork{}, err
	}
	providerID := strings.TrimSpace(caps.ProviderID)
	if providerID == "" {
		providerID = "unknown"
	}
	if err := newCompactionPersistence(e).emitStatus(
		stepID,
		EventCompactionStarted,
		mode,
		"selector",
		providerID,
		nil,
		0,
		"",
	); err != nil {
		return preparedCompactionWork{}, err
	}
	instructions := compactionInstructionsForMode(mode, instructionsInput)
	preservedUserMessageText := ""
	if mode == compactionModeManual && includePreservedUserMessage {
		preservedUserMessageText = lastVisibleUserMessageSinceLatestCompaction(input)
	}
	var result compactionResult
	enginePlan := planner.enginePlan(planningSnapshot, caps)
	if enginePlan.engineKind == compactionEngineRemote {
		result, err = e.compactRemote(
			ctx,
			stepID,
			input,
			providerID,
			instructions,
		)
		if err != nil &&
			enginePlan.fallbackToLocalOnBadCheckpoint &&
			errors.Is(err, errRemoteCompactionMissingCheckpoint) {
			result, err = e.compactLocal(
				ctx,
				stepID,
				input,
				providerID,
				instructions,
				mode,
			)
		}
	} else {
		result, err = e.compactLocal(
			ctx,
			stepID,
			input,
			providerID,
			instructions,
			mode,
		)
	}
	if err != nil {
		return preparedCompactionWork{
			result:     result,
			providerID: providerID,
			started:    true,
		}, err
	}
	if len(result.items) == 0 {
		return preparedCompactionWork{
			result:     result,
			providerID: providerID,
			started:    true,
		}, errors.New("compaction returned empty replacement history")
	}
	postReplacementMeta, err := e.compactionReinjectedMetaMessagesForMode(
		ctx,
		mode,
	)
	if err != nil {
		return preparedCompactionWork{
			result:     result,
			providerID: providerID,
			started:    true,
		}, err
	}
	replacementItems := append(
		llm.CloneResponseItems(result.items),
		llm.ItemsFromMessages(postReplacementMeta)...,
	)
	if mode == compactionModeManual && includePreservedUserMessage {
		if preservedMessage, ok := compactionPreservedUserMessage(preservedUserMessageText); ok {
			replacementItems = append(
				replacementItems,
				llm.ItemsFromMessages([]llm.Message{preservedMessage})...,
			)
		}
	}
	if result.usage.WindowTokens > 0 {
		windowTokens = result.usage.WindowTokens
	}
	return preparedCompactionWork{
		result:           result,
		replacementItems: replacementItems,
		providerID:       providerID,
		windowTokens:     windowTokens,
		started:          true,
	}, nil
}

func (e *Engine) applyManualCompactionRuntimeResult(
	admission runtimeEventAdmission,
	runtimeResult manualCompactionRuntimeResult,
) (struct{}, error) {
	selected, ok := e.longBoundary.selected.(*manualCompactionSelection)
	if !ok || selected.id != runtimeResult.id {
		return struct{}{}, fmt.Errorf(
			"manual compaction result %q has no matching selected work",
			runtimeResult.id,
		)
	}
	receipt, applyErr := e.applyPreparedCompactionWork(
		admission,
		selected.stepID,
		compactionModeManual,
		runtimeResult.prepared,
		runtimeResult.err,
	)
	finalErr := errors.Join(runtimeResult.err, applyErr)
	continuation := selected.boundary
	if !continuation.claimResult() {
		return struct{}{}, errors.New(
			"manual compaction Boundary was already terminally settled",
		)
	}
	selected.resolver.settle(receipt, finalErr)
	if _, err := e.longBoundary.settle(boundaryLongWorkResult{
		id:  runtimeResult.id,
		err: finalErr,
	}); err != nil {
		return struct{}{}, err
	}
	if continuation != nil {
		decision, err := e.acceptReducerBoundaryGrantWithResolver(
			admission,
			continuation.grant,
			continuation.continueTurn,
			continuation.step,
			continuation.complete,
		)
		if err != nil || decision != nil {
			continuation.complete(decision, err)
		}
		return struct{}{}, nil
	}
	return struct{}{}, e.reduceIdleBoundary(admission)
}

func (e *Engine) applyPreparedCompactionWork(
	admission runtimeEventAdmission,
	stepID string,
	mode compactionMode,
	prepared preparedCompactionWork,
	workErr error,
) (session.CommitReceipt, error) {
	if workErr != nil {
		if !prepared.started {
			return session.CommitReceipt{}, nil
		}
		status := &CompactionStatus{
			Mode:              string(mode),
			Engine:            strings.TrimSpace(prepared.result.engine),
			Provider:          strings.TrimSpace(prepared.providerID),
			TrimmedItemsCount: textutil.Pointer(prepared.result.trimmedItemsCount),
			Error:             strings.TrimSpace(workErr.Error()),
		}
		message := fmt.Sprintf(
			"Context compaction failed (%s): %s",
			status.Mode,
			status.Error,
		)
		return session.CommitReceipt{}, admission.applySteering(
			stepID,
			steerLocalEntryIntent(storedLocalEntry{Role: "error", Text: message}),
			steerEventIntent(Event{
				Kind:       EventCompactionFailed,
				StepID:     stepID,
				Compaction: status,
			}),
		)
	}
	if prepared.skipped || len(prepared.replacementItems) == 0 {
		return session.CommitReceipt{}, nil
	}
	receipt := session.CommitReceipt{}
	replacement := steerHistoryReplacementIntent(
		prepared.result.engine,
		mode,
		e.compactionRuntimeState().Count()+1,
		pendingHandoffFutureMessage(e),
		e.LastCommittedAssistantFinalAnswer(),
		prepared.replacementItems,
	)
	replacement.items[0].commitReceipt = &receipt
	replacementErr := admission.applySteering(stepID, replacement)
	if !receipt.Committed {
		if replacementErr == nil {
			replacementErr = errors.New(
				"history replacement returned an uncommitted receipt without an error",
			)
		}
		status := &CompactionStatus{
			Mode:              string(mode),
			Engine:            strings.TrimSpace(prepared.result.engine),
			Provider:          strings.TrimSpace(prepared.providerID),
			TrimmedItemsCount: textutil.Pointer(prepared.result.trimmedItemsCount),
			Error:             strings.TrimSpace(replacementErr.Error()),
		}
		statusErr := admission.applySteering(
			stepID,
			steerLocalEntryIntent(storedLocalEntry{
				Role: "error",
				Text: fmt.Sprintf(
					"Context compaction failed (%s): %s",
					status.Mode,
					status.Error,
				),
			}),
			steerEventIntent(Event{
				Kind:       EventCompactionFailed,
				StepID:     stepID,
				Compaction: status,
			}),
		)
		return receipt, errors.Join(replacementErr, statusErr)
	}
	e.compactionRuntimeState().SetManualCompactionEligible(false)
	finalizationErr := replacementErr
	if prepared.result.overflowRepair.Collapsed() {
		finalizationErr = errors.Join(
			finalizationErr,
			admission.applySteering(
				stepID,
				steerLocalEntryIntent(storedLocalEntry{
					Role: string(transcript.EntryRoleDeveloperErrorFeedback),
					Text: fmt.Sprintf(
						"Context compaction succeeded after collapsing tool payloads: %d shell outputs, %d patch inputs, ~%d tokens omitted. Full original tool payloads remain in pre-compaction transcript history but are omitted from the compacted model context.",
						prepared.result.overflowRepair.ShellOutputsCollapsed,
						prepared.result.overflowRepair.PatchInputsCollapsed,
						prepared.result.overflowRepair.EstimatedSavedTokens,
					),
				}),
			),
		)
	}
	if mode == compactionModeHandoff {
		req := e.handoffRuntimeState().RequestSnapshot()
		if req != nil {
			if futureMessage, ok := handoffFutureAgentMessage(req.futureAgentMessage); ok {
				futureReceipt := session.CommitReceipt{}
				intent := steerMessagesWithPersistenceIntent(
					steeringPriorityNormal,
					steeringMessageEventDefault,
					true,
					[]llm.Message{futureMessage},
				)
				intent.items[0].commitReceipt = &futureReceipt
				futureErr := admission.applySteering(stepID, intent)
				if futureReceipt.Committed {
					e.handoffRuntimeState().ClearFutureMessage()
				} else if futureErr != nil {
					e.handoffRuntimeState().QueueFutureMessage(req.futureAgentMessage)
				}
				finalizationErr = errors.Join(finalizationErr, futureErr)
			}
		}
	}
	compactedUsage := llm.Usage{
		InputTokens:  estimateItemsTokens(e.transcriptRuntimeState().SnapshotItems()),
		WindowTokens: prepared.windowTokens,
	}
	usageReceipt, usageErr := e.recordLastUsage(compactedUsage)
	if !usageReceipt.Committed {
		e.setLastUsage(compactedUsage)
	}
	finalizationErr = errors.Join(finalizationErr, usageErr)
	staleResult, staleErr := e.store.MarkLockedPromptFacingSnapshotsStale()
	if staleResult.Committed && staleResult.Locked != nil {
		e.lockedContractState().Set(*staleResult.Locked)
	}
	finalizationErr = errors.Join(finalizationErr, staleErr)
	compactionNumber := e.compactionRuntimeState().Count()
	finalizationErr = errors.Join(
		finalizationErr,
		admission.applySteering(
			stepID,
			steerEventIntent(Event{
				Kind:   EventCompactionCompleted,
				StepID: stepID,
				Compaction: &CompactionStatus{
					Mode:              string(mode),
					Engine:            strings.TrimSpace(prepared.result.engine),
					Provider:          strings.TrimSpace(prepared.providerID),
					TrimmedItemsCount: textutil.Pointer(prepared.result.trimmedItemsCount),
					Count:             compactionNumber,
				},
			}),
		),
	)
	e.handoffRuntimeState().ClearRequest()
	return receipt, finalizationErr
}

func pendingHandoffFutureMessage(e *Engine) string {
	if e == nil {
		return ""
	}
	request := e.handoffRuntimeState().RequestSnapshot()
	if request == nil {
		return ""
	}
	return strings.TrimSpace(request.futureAgentMessage)
}
