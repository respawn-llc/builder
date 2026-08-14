package runtime

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"core/prompts"
	"core/server/llm"
	"core/server/tools"
	"core/server/workflowruntime"
	"core/shared/textutil"
	"core/shared/toolspec"
)

type defaultStepExecutor struct {
	engine   *Engine
	phase    phaseProtocolEnforcer
	reviewer reviewerPipeline
	messages messageLifecycle
}

type completedResponseNext uint8

const (
	completedResponseNextInvalid completedResponseNext = iota
	completedResponseNextAccepted
	completedResponseNextExternalWorkflowTerminal
	completedResponseNextWorkflowPreflightRejected
	completedResponseNextFinalAnswerToolsTerminal
)

type preparedCompletedResponse struct {
	next                         completedResponseNext
	resolution                   completedResponseResolutionInstruction
	response                     llm.Response
	phaseTurn                    phaseProtocolTurn
	assistant                    llm.Message
	assistantProvenance          *TranscriptCommittedRowProvenance
	resolutionOutcome            completedResponseResolutionOutcome
	resolutionResolved           bool
	acceptedCalls                acceptedResponseCalls
	silentFinalAnswer            bool
	assistantCommittedCoordinate *committedAssistantCoordinate
	executedToolCall             bool
	patchEditsApplied            bool
	preflightRejection           *completedResponsePreflightRejection
}

type completedResponsePreflightRejection struct {
	cause error
}

type acceptedResponseCalls struct {
	local  []llm.ToolCall
	hosted []hostedToolExecution
	order  []acceptedResponseCallRef
}

type acceptedResponseCallSource uint8

const (
	acceptedResponseCallLocal acceptedResponseCallSource = iota + 1
	acceptedResponseCallHosted
)

type acceptedResponseCallRef struct {
	source acceptedResponseCallSource
	index  int
}

func planAcceptedResponseCalls(
	workflowActive bool,
	phaseTurn *phaseProtocolTurn,
	outputItems []llm.ResponseItem,
) (acceptedResponseCalls, *completedResponsePreflightRejection, error) {
	calls := acceptedResponseCalls{
		local:  append([]llm.ToolCall(nil), phaseTurn.LocalToolCalls...),
		hosted: append([]hostedToolExecution(nil), phaseTurn.HostedToolExecutions...),
	}
	phaseTurn.LocalToolCalls = nil
	phaseTurn.HostedToolExecutions = nil
	if rejection := classifyCompletedResponsePreflightRejection(
		workflowActive,
		calls,
	); rejection != nil {
		return acceptedResponseCalls{}, rejection, nil
	}
	if err := calls.establishCanonicalOrder(outputItems); err != nil {
		return acceptedResponseCalls{}, nil, err
	}
	return calls, nil, nil
}

func (c acceptedResponseCalls) hasCalls() bool {
	return len(c.local) > 0 || len(c.hosted) > 0
}

func (c acceptedResponseCalls) toolCalls() []llm.ToolCall {
	result := make([]llm.ToolCall, 0, len(c.order))
	for _, ref := range c.order {
		switch ref.source {
		case acceptedResponseCallLocal:
			result = append(result, c.local[ref.index])
		case acceptedResponseCallHosted:
			result = append(result, c.hosted[ref.index].Call)
		default:
			panic(fmt.Sprintf(
				"accepted response call order contains unknown source %d at index %d",
				ref.source,
				ref.index,
			))
		}
	}
	return result
}

func (c *acceptedResponseCalls) establishCanonicalOrder(outputItems []llm.ResponseItem) error {
	seenIDs := make(map[string]struct{}, len(c.local)+len(c.hosted))
	for _, call := range c.local {
		if err := registerAcceptedCallID(seenIDs, call.ID); err != nil {
			return err
		}
	}
	for _, hosted := range c.hosted {
		if err := registerAcceptedCallID(seenIDs, hosted.Call.ID); err != nil {
			return err
		}
	}

	if len(c.hosted) == 0 {
		c.order = make([]acceptedResponseCallRef, len(c.local))
		for index := range c.local {
			c.order[index] = acceptedResponseCallRef{
				source: acceptedResponseCallLocal,
				index:  index,
			}
		}
		return nil
	}
	if len(c.local) == 0 {
		c.order = make([]acceptedResponseCallRef, len(c.hosted))
		for index := range c.hosted {
			c.order[index] = acceptedResponseCallRef{
				source: acceptedResponseCallHosted,
				index:  index,
			}
		}
		return nil
	}

	type positionedCall struct {
		position int
		ref      acceptedResponseCallRef
	}
	positioned := make([]positionedCall, 0, len(c.local)+len(c.hosted))
	seenPositions := make(map[int]string, len(c.local)+len(c.hosted))
	for index, call := range c.local {
		positions := outputPositionsForLocalCall(outputItems, call.ID)
		if len(positions) != 1 {
			return fmt.Errorf(
				"mixed accepted local tool call %q has %d canonical output positions, want 1",
				call.ID,
				len(positions),
			)
		}
		if err := registerAcceptedOutputPosition(seenPositions, positions[0], call.ID); err != nil {
			return err
		}
		positioned = append(positioned, positionedCall{
			position: positions[0],
			ref: acceptedResponseCallRef{
				source: acceptedResponseCallLocal,
				index:  index,
			},
		})
	}
	for index, hosted := range c.hosted {
		if err := registerAcceptedOutputPosition(
			seenPositions,
			hosted.outputPosition,
			hosted.Call.ID,
		); err != nil {
			return err
		}
		positioned = append(positioned, positionedCall{
			position: hosted.outputPosition,
			ref: acceptedResponseCallRef{
				source: acceptedResponseCallHosted,
				index:  index,
			},
		})
	}
	sort.Slice(positioned, func(left, right int) bool {
		return positioned[left].position < positioned[right].position
	})
	c.order = make([]acceptedResponseCallRef, len(positioned))
	for index, call := range positioned {
		c.order[index] = call.ref
	}
	return nil
}

func registerAcceptedCallID(seen map[string]struct{}, callID string) error {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return errors.New("accepted tool call ID is required")
	}
	if _, exists := seen[callID]; exists {
		return fmt.Errorf("accepted tool call ID %q is duplicated", callID)
	}
	seen[callID] = struct{}{}
	return nil
}

func outputPositionsForLocalCall(items []llm.ResponseItem, callID string) []int {
	callID = strings.TrimSpace(callID)
	var positions []int
	for position, item := range items {
		switch item.Type {
		case llm.ResponseItemTypeFunctionCall, llm.ResponseItemTypeCustomToolCall:
		default:
			continue
		}
		itemID, hasItemID := textutil.OptionalTrimmed(item.ID)
		itemCallID, hasItemCallID := textutil.OptionalTrimmed(item.CallID)
		if hasItemID && itemID == callID || hasItemCallID && itemCallID == callID {
			positions = append(positions, position)
		}
	}
	return positions
}

func registerAcceptedOutputPosition(
	seen map[int]string,
	position int,
	callID string,
) error {
	if previousCallID, exists := seen[position]; exists {
		return fmt.Errorf(
			"accepted tool calls %q and %q share canonical output position %d",
			previousCallID,
			callID,
			position,
		)
	}
	seen[position] = callID
	return nil
}

func (s *defaultStepExecutor) RunStepLoopWithOptions(ctx context.Context, stepID string, options stepLoopOptions) (stepLoopResult, error) {
	result, err := s.runStepLoopWithOptions(ctx, stepID, options)
	var stopped *queuedUserFlushStoppedError
	if errors.As(err, &stopped) && !s.engine.currentNodeExecutionActive() {
		return stepLoopResult{}, nil
	}
	return result, err
}

func (s *defaultStepExecutor) runStepLoopWithOptions(ctx context.Context, stepID string, options stepLoopOptions) (stepLoopResult, error) {
	e := s.engine
	executedToolCall := false
	patchEditsApplied, mismatchWarningCommitted := false, false
	stepNo := 0
	for {
		stepNo++
		if err := ctx.Err(); err != nil {
			return stepLoopResult{}, err
		}
		if err := e.drainActiveStepGoalMutations(stepID); err != nil {
			return stepLoopResult{}, err
		}
		e.stepLifecycle.EndAgentStepBoundary()
		if terminal, err := s.workflowDurableCompletionTerminal(ctx, stepID); err != nil {
			return stepLoopResult{}, err
		} else if terminal {
			e.cascadeCompleteActiveGoalOnWorkflowCompletion()
			return stepLoopResult{ExecutedToolCall: executedToolCall}, nil
		}
		if err := s.prepareModelTurn(ctx, stepID); err != nil {
			return stepLoopResult{}, err
		}
		e.assertWorkflowInstructionsPresent(stepNo)

		var reasoningSteerErr error
		candidate, err := e.generateWithMissingToolOutputRepair(
			ctx,
			stepID,
			func() (llm.Request, error) {
				if err := s.commitPendingUserSteer(stepID, options, &mismatchWarningCommitted); err != nil {
					return llm.Request{}, err
				}
				return e.buildActiveTurnDispatchRequest(ctx, stepID, nil, true)
			},
			func(delta llm.AssistantDelta) {
				_ = e.steer(stepID, steerAssistantDeltaIntent(delta))
			},
			func(delta llm.ReasoningSummaryDelta) {
				if reasoningSteerErr == nil {
					reasoningSteerErr = e.steer(stepID, steerReasoningDeltaIntent(delta))
				}
			},
			func() {
				_ = e.resetReasoningAndClearStreamingState(stepID)
			},
		)
		if err != nil {
			return stepLoopResult{}, err
		}
		if reasoningSteerErr != nil {
			return stepLoopResult{}, fmt.Errorf("apply streamed reasoning update: %w", reasoningSteerErr)
		}
		resp := candidate.response

		prepared, err := s.prepareCompletedResponse(ctx, stepID, resp)
		if err != nil {
			return stepLoopResult{}, err
		}
		if prepared.executedToolCall {
			executedToolCall = true
		}
		patchEditsApplied = patchEditsApplied || prepared.patchEditsApplied
		var resolution completedResponseResolutionOutcome
		if prepared.resolutionResolved {
			resolution = prepared.resolutionOutcome
		} else {
			resolution, err = e.resolveCompletedResponseStream(stepID, prepared.resolution)
			if err != nil {
				return stepLoopResult{}, err
			}
		}
		if err := s.resolveReasoningDisposition(stepID, prepared.next, prepared.response.Reasoning); err != nil {
			return stepLoopResult{}, err
		}
		switch prepared.next {
		case completedResponseNextExternalWorkflowTerminal:
			e.cascadeCompleteActiveGoalOnWorkflowCompletion()
			return stepLoopResult{ExecutedToolCall: executedToolCall}, nil
		case completedResponseNextWorkflowPreflightRejected:
			if prepared.preflightRejection == nil {
				return stepLoopResult{}, errors.New("workflow preflight rejection action requires rejection details")
			}
			terminal, err := s.appendWorkflowInvalidCompletionNudge(ctx, stepID, prepared.preflightRejection.cause)
			if err != nil {
				return stepLoopResult{}, err
			}
			if terminal {
				return stepLoopResult{ExecutedToolCall: executedToolCall}, nil
			}
			continue
		case completedResponseNextFinalAnswerToolsTerminal:
			_, err = e.commitAcceptedResponseCandidate(stepID, candidate, mismatchWarningCommitted)
			if err != nil {
				return stepLoopResult{}, err
			}
			e.cascadeCompleteActiveGoalOnWorkflowCompletion()
			return stepLoopResult{FinalAnswer: textutil.Value(prepared.assistant), ExecutedToolCall: true}, nil
		case completedResponseNextAccepted:
			mismatchWarningCommitted, err = e.commitAcceptedResponseCandidate(stepID, candidate, mismatchWarningCommitted)
			if err != nil {
				return stepLoopResult{}, err
			}
		default:
			return stepLoopResult{}, errors.New("completed response preparation produced an invalid next action")
		}

		resp = prepared.response
		phaseTurn := prepared.phaseTurn
		assistantMsg := prepared.assistant
		acceptedCalls := prepared.acceptedCalls
		localToolCalls := acceptedCalls.local
		hostedToolExecutions := acceptedCalls.hosted
		silentFinalAnswer := prepared.silentFinalAnswer
		assistantCommittedCoordinate := prepared.assistantCommittedCoordinate
		assistantProvenance := cloneTranscriptCommittedRowProvenance(prepared.assistantProvenance)
		assistantEventEmitted := resolution.committedAssistantEventPublished

		if !silentFinalAnswer {
			if phaseTurn.MissingAssistantPhase {
				if err := e.steer(stepID, steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeErrorFeedback), Content: textutil.Value(missingAssistantPhaseWarning)}})); err != nil {
					return stepLoopResult{}, err
				}
			}
		}
		if acceptedCalls.hasCalls() {
			applied, terminal, err := s.executeAcceptedToolCallsAndAppendResults(
				ctx,
				stepID,
				acceptedCalls,
			)
			if err != nil {
				return stepLoopResult{}, err
			}
			if err := s.completeAgentStepBoundary(ctx, stepID); err != nil {
				return stepLoopResult{}, err
			}
			patchEditsApplied = patchEditsApplied || applied
			if terminal {
				e.cascadeCompleteActiveGoalOnWorkflowCompletion()
				return stepLoopResult{ExecutedToolCall: true}, nil
			}
			if _, err := s.flushPendingUserInjections(stepID, options, &mismatchWarningCommitted); err != nil {
				return stepLoopResult{}, err
			}
			continue
		}

		if assistantMsg.Content == nil && responseContainsProgress(resp) {
			if err := s.completeAgentStepBoundary(ctx, stepID); err != nil {
				return stepLoopResult{}, err
			}
			continue
		}
		if assistantMsg.Content == nil {
			return stepLoopResult{}, errors.New(
				"provider contract violation: response contained no final content, tool calls, reasoning, or commentary",
			)
		}

		if phaseTurn.EffectivePhase.Is(llm.MessagePhaseFinal) &&
			assistantMsg.Content != nil &&
			strings.TrimSpace(*assistantMsg.Content) != "" {
			handled, terminal, err := s.handleWorkflowCompletionSubmission(ctx, stepID, *assistantMsg.Content)
			if err != nil {
				return stepLoopResult{}, err
			}
			if terminal {
				return stepLoopResult{FinalAnswer: textutil.Value(assistantMsg), ExecutedToolCall: executedToolCall}, nil
			}
			if handled {
				continue
			}
		}

		if len(localToolCalls) == 0 {
			if phaseTurn.MissingAssistantPhase {
				if _, err := s.flushPendingUserInjections(stepID, options, &mismatchWarningCommitted); err != nil {
					return stepLoopResult{}, err
				}
				continue
			}
			if phaseTurn.EnforcePhaseProtocol && !messagePhaseIs(assistantMsg, llm.MessagePhaseFinal) {
				if err := e.steer(stepID, steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeErrorFeedback), Content: textutil.Value(commentaryWithoutToolCallsWarning)}})); err != nil {
					return stepLoopResult{}, err
				}
				if _, err := s.flushPendingUserInjections(stepID, options, &mismatchWarningCommitted); err != nil {
					return stepLoopResult{}, err
				}
				continue
			}
			flushed, err := s.flushPendingUserInjections(stepID, options, &mismatchWarningCommitted)
			if err != nil {
				return stepLoopResult{}, err
			}
			if flushed > 0 {
				if len(localToolCalls) == 0 && len(hostedToolExecutions) == 0 {
					if err := e.stepLifecycle.DrainAgentStepBoundary(ctx); err != nil {
						return stepLoopResult{}, err
					}
				}
				continue
			}
			if len(hostedToolExecutions) > 0 {
				_ = e.steer(stepID, steerEventIntent(Event{Kind: EventConversationUpdated, StepID: stepID, CommittedTranscriptChanged: true}))
				continue
			}
			if execution, active := e.currentNodeExecutionConfig(); active && execution.Controller != nil {
				content := ""
				if assistantMsg.Content != nil {
					content = strings.TrimSpace(*assistantMsg.Content)
				}
				if assistantMsg.Content != nil && content == "" && !silentFinalAnswer {
					if err := e.steer(stepID, steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeErrorFeedback), Content: textutil.Value(workflowFinalWithoutContentWarning)}})); err != nil {
						return stepLoopResult{}, err
					}
					continue
				}
				if phaseTurn.EffectivePhase == nil || phaseTurn.EffectivePhase.IsAbsent() {
					handled, terminal, err := s.handleWorkflowCompletionSubmission(ctx, stepID, content)
					if err != nil {
						return stepLoopResult{}, err
					}
					if terminal {
						if silentFinalAnswer {
							return stepLoopResult{ExecutedToolCall: executedToolCall}, nil
						}
						return stepLoopResult{FinalAnswer: textutil.Value(assistantMsg), ExecutedToolCall: executedToolCall}, nil
					}
					if handled {
						continue
					}
				}
				if phaseTurn.EffectivePhase.Is(llm.MessagePhaseCommentary) {
					continue
				}
			}

			resolved := assistantMsg
			resolvedSilentFinalAnswer := silentFinalAnswer
			resolvedCommittedCoordinate := cloneCommittedAssistantCoordinate(assistantCommittedCoordinate)
			var reviewerCompletion *ReviewerStatus
			reviewerRan := false
			if resolvedSilentFinalAnswer {
				resolvedCommittedStart, resolvedCommittedStartSet := committedAssistantCoordinateFields(resolvedCommittedCoordinate)
				return stepLoopResult{SilentFinal: true, ExecutedToolCall: executedToolCall, AssistantCommittedStart: resolvedCommittedStart, AssistantCommittedStartSet: resolvedCommittedStartSet}, nil
			}

			resolvedCommittedStart, resolvedCommittedStartSet := committedAssistantCoordinateFields(resolvedCommittedCoordinate)
			effectiveReviewerFrequency := options.ReviewerFrequency
			effectiveReviewerClient := options.ReviewerClient
			if options.RefreshReviewerConfigOnResolve {
				effectiveReviewerFrequency, effectiveReviewerClient = e.reviewerTurnConfigSnapshot()
			}
			if s.reviewer.ShouldRunTurn(effectiveReviewerFrequency, effectiveReviewerClient, patchEditsApplied) {
				reviewerRan = true
				startErr := e.steer(stepID, steerEventIntent(Event{Kind: EventReviewerStarted, StepID: stepID}))
				if startErr != nil {
					return stepLoopResult{}, fmt.Errorf("start Reviewer lifecycle: %w", startErr)
				}
				if !assistantEventEmitted {
					// The answer is already committed before supervisor entries are appended.
					// Publish it first so live clients never see supervisor entries as a gap
					// after an unannounced committed assistant message.
					_ = s.publishCommittedAssistantMessage(stepID, resolved, resolvedCommittedCoordinate, assistantProvenance)
					assistantEventEmitted = true
				}
				preReviewMessage := resolved
				reviewed, err := s.reviewer.RunFollowUp(ctx, stepID, resolved, resolvedCommittedStart, resolvedCommittedStartSet, effectiveReviewerClient)
				if err == nil {
					resolved = reviewed.Message
					reviewerCompletion = reviewed.Completion
					resolvedCommittedStart = reviewed.AssistantCommittedStart
					resolvedCommittedStartSet = reviewed.AssistantCommittedStartSet
					resolvedCommittedCoordinate = committedAssistantCoordinateFromFields(resolvedCommittedStart, resolvedCommittedStartSet)
					assistantEventEmitted = reviewed.AssistantEventEmitted || (assistantEventEmitted && sameVisibleAssistantMessage(preReviewMessage, resolved))
				} else {
					assistantEventEmitted = assistantEventEmitted && sameVisibleAssistantMessage(preReviewMessage, resolved)
				}
				if err != nil {
					return stepLoopResult{}, errors.Join(err, s.terminalizeReviewerLifecycle(stepID, nil))
				}
			}
			if !assistantEventEmitted {
				_ = s.publishCommittedAssistantMessage(stepID, resolved, resolvedCommittedCoordinate, assistantProvenance)
			}
			if reviewerRan {
				var statusErr error
				if reviewerCompletionHasTranscriptStatus(reviewerCompletion) {
					statusErr = e.steer(stepID, steerLocalEntryIntent(storedLocalEntry{
						Role: reviewerStatusEntryRole(*reviewerCompletion),
						Text: reviewerStatusText(*reviewerCompletion, nil),
					}))
				}
				terminalizationErr := s.terminalizeReviewerLifecycle(stepID, reviewerCompletion)
				if statusErr != nil || terminalizationErr != nil {
					return stepLoopResult{}, errors.Join(statusErr, terminalizationErr)
				}
			}
			if err := e.drainActiveStepGoalMutations(stepID); err != nil {
				return stepLoopResult{}, err
			}
			resolvedCommittedStart, resolvedCommittedStartSet = committedAssistantCoordinateFields(resolvedCommittedCoordinate)
			return stepLoopResult{FinalAnswer: textutil.Value(resolved), ExecutedToolCall: executedToolCall, AssistantCommittedStart: resolvedCommittedStart, AssistantCommittedStartSet: resolvedCommittedStartSet}, nil
		}

	}
}

func reviewerCompletionHasTranscriptStatus(status *ReviewerStatus) bool {
	if status == nil {
		return false
	}
	switch strings.TrimSpace(status.Outcome) {
	case "applied", "noop", "no_suggestions":
		return true
	default:
		return false
	}
}

func (s *defaultStepExecutor) terminalizeReviewerLifecycle(stepID string, status *ReviewerStatus) error {
	if s == nil || s.engine == nil {
		return errors.New("Reviewer lifecycle terminalizer requires an engine")
	}
	err := s.engine.steer(stepID, steerEventIntent(Event{
		Kind:     EventReviewerCompleted,
		StepID:   stepID,
		Reviewer: status,
	}))
	return err
}

func (s *defaultStepExecutor) flushPendingUserInjections(stepID string, options stepLoopOptions, mismatchWarningCommitted *bool) (int, error) {
	result, err := s.messages.FlushPendingUserInjections(stepID, steerUserInjections(s.engine.queuedUserAutoDrainIDSnapshot()))
	observeQueuedUserFlushCommit(options, result.receipt)
	if err != nil {
		return 0, err
	}
	if result.disposition == userInjectionFlushStopped {
		return 0, &queuedUserFlushStoppedError{}
	}
	if result.startedStep {
		*mismatchWarningCommitted = false
	}
	return result.flushed, nil
}

func (s *defaultStepExecutor) commitPendingUserSteer(stepID string, options stepLoopOptions, mismatchWarningCommitted *bool) error {
	result, err := s.messages.CommitPendingUserInjections(stepID, steerUserInjections(s.engine.queuedUserAutoDrainIDSnapshot()))
	observeQueuedUserFlushCommit(options, result.receipt)
	if err != nil {
		return err
	}
	if result.disposition == userInjectionFlushStopped {
		return &queuedUserFlushStoppedError{}
	}
	if result.startedStep {
		*mismatchWarningCommitted = false
	}
	return nil
}

func (s *defaultStepExecutor) prepareCompletedResponse(ctx context.Context, stepID string, resp llm.Response) (preparedCompletedResponse, error) {
	e := s.engine
	if completed, err := e.observeWorkflowDurableCompletion(ctx); err != nil {
		return preparedCompletedResponse{}, err
	} else if completed {
		return preparedCompletedResponse{
			next:       completedResponseNextExternalWorkflowTerminal,
			resolution: completedResponseAbortInstruction(),
			response:   resp,
			assistant:  resp.Assistant,
		}, nil
	}

	localToolCalls := append([]llm.ToolCall(nil), resp.ToolCalls...)
	shape, err := e.lockedRequestShape()
	if err != nil {
		return preparedCompletedResponse{}, err
	}
	hostedToolExecutions := hostedToolExecutionsFromOutputItems(resp.OutputItems, tools.DefinitionsFor(shape.EnabledTools))
	phaseTurn, err := s.phase.Apply(ctx, resp, resp.Assistant, localToolCalls, hostedToolExecutions)
	if err != nil {
		return preparedCompletedResponse{}, err
	}
	assistantMsg := phaseTurn.Assistant
	acceptedCalls, rejection, err := planAcceptedResponseCalls(
		e.currentNodeExecutionActive(),
		&phaseTurn,
		resp.OutputItems,
	)
	if err != nil {
		return preparedCompletedResponse{}, err
	}
	executedToolCall := acceptedCalls.hasCalls()
	silentFinalAnswer := isBlankFinalAnswer(assistantMsg) &&
		!e.goalActive() &&
		!e.currentNodeExecutionActive()
	if silentFinalAnswer && acceptedCalls.hasCalls() {
		return preparedCompletedResponse{}, fmt.Errorf(
			"provider returned a blank final answer with %d accepted tool calls",
			len(acceptedCalls.order),
		)
	}

	if rejection != nil {
		return preparedCompletedResponse{
			next:               completedResponseNextWorkflowPreflightRejected,
			resolution:         completedResponseAbortInstruction(),
			response:           resp,
			phaseTurn:          phaseTurn,
			assistant:          assistantMsg,
			silentFinalAnswer:  silentFinalAnswer,
			executedToolCall:   executedToolCall,
			preflightRejection: rejection,
		}, nil
	}
	preparedLocalCalls, err := prepareExecutorToolCalls(
		e,
		stepID,
		activeRunIDForStep(e, stepID),
		e.currentNodeExecutionActive(),
		acceptedCalls.local,
	)
	if err != nil {
		return preparedCompletedResponse{}, err
	}
	for index := range preparedLocalCalls {
		if preparedLocalCalls[index].inputErr == nil {
			acceptedCalls.local[index] = prepareRawToolCallForTranscript(
				preparedLocalCalls[index].call,
				preparedLocalCalls[index].executableCall,
				e.transcriptWorkingDir(),
			)
		}
	}
	assistantMsg.ToolCalls = acceptedCalls.toolCalls()
	phaseTurn.Assistant = assistantMsg

	finalAnswerWithToolCalls := messagePhaseIs(assistantMsg, llm.MessagePhaseFinal) &&
		assistantMsg.Content != nil &&
		strings.TrimSpace(*assistantMsg.Content) != "" &&
		acceptedCalls.hasCalls()
	patchEditsApplied := false
	if finalAnswerWithToolCalls {
		if err := e.markProviderVisibleModelRecovery(stepID); err != nil {
			return preparedCompletedResponse{}, err
		}
		applied, terminal, err := s.materializeFinalAnswerToolCalls(ctx, stepID, acceptedCalls)
		if err != nil {
			return preparedCompletedResponse{}, err
		}
		patchEditsApplied = applied
		if terminal {
			return preparedCompletedResponse{
				next:              completedResponseNextFinalAnswerToolsTerminal,
				resolution:        completedResponseAbortInstruction(),
				response:          resp,
				phaseTurn:         phaseTurn,
				assistant:         assistantMsg,
				silentFinalAnswer: silentFinalAnswer,
				executedToolCall:  true,
				patchEditsApplied: patchEditsApplied,
			}, nil
		}
		assistantMsg.ToolCalls = nil
		acceptedCalls = acceptedResponseCalls{}
		phaseTurn.Assistant = assistantMsg
		if err := e.steer(stepID, steerEventIntent(Event{Kind: EventConversationUpdated, StepID: stepID, CommittedTranscriptChanged: true})); err != nil {
			return preparedCompletedResponse{}, err
		}
	}

	if !silentFinalAnswer {
		assistantChars := 0
		assistantPhase, _ := textutil.OptionalValue(assistantMsg.Phase)
		if assistantMsg.Content != nil {
			assistantChars = len(*assistantMsg.Content)
		}
		if err := e.steer(stepID, steerEventIntent(Event{
			Kind:   EventModelResponse,
			StepID: stepID,
			ModelResponse: &ModelResponseTrace{
				AssistantPhase:   assistantPhase,
				AssistantChars:   assistantChars,
				ToolCallsCount:   len(resp.ToolCalls),
				OutputItemsCount: len(resp.OutputItems),
				OutputItemTypes:  summarizeOutputItemTypes(resp.OutputItems),
			},
		})); err != nil {
			return preparedCompletedResponse{}, err
		}
	}
	if !silentFinalAnswer && !finalAnswerWithToolCalls {
		if err := e.markProviderVisibleModelRecovery(stepID); err != nil {
			return preparedCompletedResponse{}, err
		}
	}
	executableCallIDs := make(map[string]struct{}, len(acceptedCalls.local))
	for _, call := range acceptedCalls.local {
		if callID := strings.TrimSpace(call.ID); callID != "" {
			executableCallIDs[callID] = struct{}{}
		}
	}
	assistantCommitResult := steeringAssistantCommitResult{}
	if err := e.steer(
		stepID,
		steerAssistantCommitIntent(assistantMsg, executableCallIDs, &assistantCommitResult),
	); err != nil {
		return preparedCompletedResponse{}, err
	}
	assistantCommittedCoordinate := assistantCommitResult.coordinate
	assistantProvenance := assistantCommitResult.provenance
	if !acceptedCalls.hasCalls() {
		e.compactionRuntimeState().SetManualCompactionEligible(true)
	}
	return preparedCompletedResponse{
		next:                         completedResponseNextAccepted,
		resolution:                   completedResponseAbortInstruction(),
		resolutionOutcome:            assistantCommitResult.resolution,
		resolutionResolved:           true,
		response:                     resp,
		phaseTurn:                    phaseTurn,
		assistant:                    assistantMsg,
		assistantProvenance:          cloneTranscriptCommittedRowProvenance(assistantProvenance),
		acceptedCalls:                acceptedCalls,
		silentFinalAnswer:            silentFinalAnswer,
		assistantCommittedCoordinate: assistantCommittedCoordinate,
		executedToolCall:             executedToolCall,
		patchEditsApplied:            patchEditsApplied,
	}, nil
}

func classifyCompletedResponsePreflightRejection(workflowActive bool, calls acceptedResponseCalls) *completedResponsePreflightRejection {
	err := workflowPreflightError(workflowActive, calls.local, calls.hosted)
	if err == nil {
		return nil
	}
	return &completedResponsePreflightRejection{cause: err}
}

func (s *defaultStepExecutor) materializeFinalAnswerToolCalls(ctx context.Context, stepID string, calls acceptedResponseCalls) (bool, bool, error) {
	e := s.engine
	toolCallMessage := llm.Message{
		Role:      llm.RoleAssistant,
		Phase:     textutil.Value(llm.MessagePhaseCommentary),
		ToolCalls: calls.toolCalls(),
	}
	if err := e.steer(stepID, steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{toolCallMessage})); err != nil {
		return false, false, err
	}

	executableCallIDs := make(map[string]struct{}, len(calls.local))
	for _, call := range calls.local {
		if callID := strings.TrimSpace(call.ID); callID != "" {
			executableCallIDs[callID] = struct{}{}
		}
	}
	_, toolCallStarts := committedStartsForPersistedAssistantMessage(e, toolCallMessage, executableCallIDs)
	e.rememberPendingToolCallStarts(toolCallStarts)
	if len(VisibleChatEntriesFromMessage(toolCallMessage)) > 0 {
		var committedCoordinate *committedAssistantCoordinate
		for _, start := range toolCallStarts {
			if committedCoordinate == nil || start < committedCoordinate.start {
				committedCoordinate = &committedAssistantCoordinate{start: start}
			}
		}
		if committedCoordinate == nil {
			committedCoordinate, _ = committedStartsForPersistedAssistantMessage(e, toolCallMessage, nil)
		}
		if err := s.publishCommittedAssistantMessage(stepID, toolCallMessage, committedCoordinate); err != nil {
			return false, false, err
		}
	}

	patchEditsApplied, terminal, err := s.executeAcceptedToolCallsAndAppendResults(
		ctx,
		stepID,
		calls,
	)
	if err != nil {
		return false, false, err
	}
	if calls.hasCalls() {
		if err := s.completeAgentStepBoundary(ctx, stepID); err != nil {
			return false, false, err
		}
	}
	return patchEditsApplied, terminal, nil
}

func (s *defaultStepExecutor) completeAgentStepBoundary(ctx context.Context, stepID string) error {
	s.engine.compactionRuntimeState().SetManualCompactionEligible(true)
	if err := s.engine.flushPendingWorkflowAssignments(stepID); err != nil {
		return err
	}
	return s.engine.stepLifecycle.DrainAgentStepBoundary(ctx)
}

func (s *defaultStepExecutor) executeAcceptedToolCallsAndAppendResults(
	ctx context.Context,
	stepID string,
	calls acceptedResponseCalls,
) (bool, bool, error) {
	if !calls.hasCalls() {
		return false, false, nil
	}
	e := s.engine
	results, durableTerminal, err := e.executeAcceptedToolCallsCoordinated(
		ctx,
		stepID,
		calls,
	)
	if err != nil {
		return false, false, err
	}
	patchEditsApplied := false
	terminal := hasWorkflowTerminalResult(results)
	for _, result := range results {
		if !result.IsError && (result.Name == toolspec.ToolPatch || result.Name == toolspec.ToolEdit) {
			patchEditsApplied = true
		}
	}
	return patchEditsApplied, terminal || durableTerminal, nil
}

func (s *defaultStepExecutor) workflowDurableCompletionTerminal(ctx context.Context, stepID string) (bool, error) {
	completed, err := s.engine.observeWorkflowDurableCompletion(ctx)
	if err != nil || !completed {
		return false, err
	}
	if err := s.engine.resetReasoningAndClearStreamingState(stepID); err != nil {
		return false, err
	}
	return true, nil
}

func (s *defaultStepExecutor) handleWorkflowCompletionSubmission(ctx context.Context, stepID string, content string) (bool, bool, error) {
	e := s.engine
	execution, active := e.currentNodeExecutionConfig()
	if !active || execution.Controller == nil {
		return false, false, nil
	}
	mode, err := e.workflowCompletionMode(ctx)
	if err != nil {
		return false, false, err
	}
	content = strings.TrimSpace(content)
	var (
		parsed workflowruntime.ParsedCompletion
		source WorkflowCompletionSource
	)
	switch mode {
	case workflowruntime.CompletionModeStructuredOutput:
		parsed, err = workflowruntime.DecodeCompletion([]byte(content), execution.Contract)
		source = WorkflowCompletionSourceStructuredOutput
	case workflowruntime.CompletionModeUnstructuredOutput:
		parsed, err = workflowruntime.DecodeUnstructuredCompletion(content, execution.Contract)
		source = WorkflowCompletionSourceUnstructured
	case workflowruntime.CompletionModeShellCommand:
		terminal, nudgeErr := s.appendWorkflowInvalidCompletionNudge(ctx, stepID, errors.New("normal final answers do not complete shell-command workflow nodes"))
		return true, terminal, nudgeErr
	case workflowruntime.CompletionModeTool:
		record, recordErr := e.recordWorkflowProtocolViolation(ctx, workflowruntime.ViolationKindInvalidCompletion, content)
		if recordErr != nil {
			return true, false, recordErr
		}
		if record.Interrupted {
			return true, true, nil
		}
		if err := e.steer(stepID, steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeErrorFeedback), Content: textutil.Value(workflowFinalAnswerNudge)}})); err != nil {
			return true, false, err
		}
		return true, false, nil
	default:
		return false, false, nil
	}
	if err != nil {
		terminal, nudgeErr := s.appendWorkflowInvalidCompletionNudge(ctx, stepID, err)
		return true, terminal, nudgeErr
	}
	completed, completeErr := s.completeCurrentNodeExecutionFromParsed(ctx, parsed)
	if completeErr != nil && !completed.IsApplied() {
		if isWorkflowCompletionOperationalError(completeErr) {
			return true, true, completeErr
		}
		terminal, nudgeErr := s.appendWorkflowInvalidCompletionNudge(ctx, stepID, completeErr)
		return true, terminal, nudgeErr
	}
	e.setWorkflowTerminalState(source)
	if completeErr != nil {
		if err := e.steer(
			stepID,
			steerLocalEntryIntent(workflowCompletionOperatorDiagnostic(completeErr, nil)),
		); err != nil {
			return true, true, err
		}
	}
	return true, true, nil
}

func (s *defaultStepExecutor) completeCurrentNodeExecutionFromParsed(
	ctx context.Context,
	parsed workflowruntime.ParsedCompletion,
) (workflowruntime.CompletionResult, error) {
	return s.engine.completeWorkflowCurrentNode(ctx, parsed)
}

func (s *defaultStepExecutor) appendWorkflowInvalidCompletionNudge(ctx context.Context, stepID string, err error) (bool, error) {
	e := s.engine
	record, recordErr := e.recordWorkflowProtocolViolation(ctx, workflowruntime.ViolationKindInvalidCompletion, err.Error())
	if recordErr != nil {
		return false, recordErr
	}
	if record.Interrupted {
		return true, nil
	}
	instructions, instructionsErr := e.currentWorkflowCompletionInstructions(ctx)
	if instructionsErr != nil {
		return false, instructionsErr
	}
	goalText := ""
	goalReminder := ""
	if objective, reminder, ok := e.goalContinuation().reminderContext(); ok {
		goalText = objective
		goalReminder = reminder
	}
	content, renderErr := prompts.RenderWorkflowNudgePrompt(err.Error(), instructions, goalText, goalReminder)
	if renderErr != nil {
		return false, renderErr
	}
	return false, e.steer(stepID, steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeErrorFeedback), Content: textutil.Value(content)}}))
}

func (e *Engine) currentWorkflowCompletionInstructions(ctx context.Context) (string, error) {
	execution, active := e.currentNodeExecutionConfig()
	if !active {
		return "", nil
	}
	mode, err := e.workflowCompletionMode(ctx)
	if err != nil {
		return "", err
	}
	return workflowCompletionInstructionsFragment(mode, execution.Instructions.WorkflowID, execution.Contract)
}

func (s *defaultStepExecutor) prepareModelTurn(ctx context.Context, stepID string) error {
	e := s.engine
	handoffRequestPending := e.handoffRuntimeState().RequestSnapshot() != nil
	if !handoffRequestPending {
		if err := e.materializePendingWorktreeReminder(stepID); err != nil {
			return err
		}
	}
	handoffCompacted, err := e.applyPendingHandoffIfNeeded(ctx, stepID)
	if err != nil {
		return err
	}
	if err := e.requireAskQuestionWhenGoalActive(); err != nil {
		return err
	}
	if handoffCompacted {
		if err := e.materializePendingWorktreeReminder(stepID); err != nil {
			return err
		}
		return newCompactionReminderCoordinator(e).maybeAppend(ctx, stepID)
	}
	if handoffRequestPending {
		if err := e.materializePendingWorktreeReminder(stepID); err != nil {
			return err
		}
	}
	if err := e.autoCompactIfNeeded(ctx, stepID, compactionModeAuto); err != nil {
		return err
	}
	if err := e.materializePendingWorktreeReminder(stepID); err != nil {
		return err
	}
	return newCompactionReminderCoordinator(e).maybeAppend(ctx, stepID)
}

func (s *defaultStepExecutor) publishCommittedAssistantMessage(stepID string, msg llm.Message, coordinate *committedAssistantCoordinate, provenances ...*TranscriptCommittedRowProvenance) error {
	return s.engine.steer(stepID, steerCommittedAssistantMessageIntent(msg, coordinate, provenances...))
}

func committedAssistantMessageFinalizesStreaming(msg llm.Message) bool {
	if msg.Role != llm.RoleAssistant || isBlankFinalAnswer(msg) {
		return false
	}
	for _, entry := range VisibleChatEntriesFromMessage(msg) {
		if strings.TrimSpace(entry.Role) == "assistant" && strings.TrimSpace(entry.Text) != "" {
			return true
		}
	}
	return false
}

func sameVisibleAssistantMessage(a, b llm.Message) bool {
	aEntries := VisibleChatEntriesFromMessage(a)
	bEntries := VisibleChatEntriesFromMessage(b)
	if len(aEntries) != len(bEntries) {
		return false
	}
	for idx := range aEntries {
		if !sameVisibleChatEntryContent(aEntries[idx], bEntries[idx]) {
			return false
		}
	}
	return true
}

func sameVisibleChatEntryContent(a, b ChatEntry) bool {
	return a.Visibility == b.Visibility &&
		a.Role == b.Role &&
		a.Text == b.Text &&
		a.CondensedText == b.CondensedText &&
		a.Phase == b.Phase &&
		strings.TrimSpace(a.ToolCallID) == strings.TrimSpace(b.ToolCallID)
}

func committedStartsForPersistedAssistantMessage(e *Engine, msg llm.Message, executableCallIDs map[string]struct{}) (*committedAssistantCoordinate, map[string]int) {
	if e == nil {
		return nil, nil
	}
	persisted := normalizeMessageForTranscript(msg, e.transcriptWorkingDir())
	entries := VisibleChatEntriesFromMessage(persisted)
	if len(entries) == 0 {
		return nil, nil
	}
	start := e.CommittedTranscriptEntryCount() - len(entries)
	if start < 0 {
		return nil, nil
	}
	toolCallStarts := make(map[string]int)
	for idx, entry := range entries {
		if strings.TrimSpace(entry.Role) != "tool_call" {
			continue
		}
		callID := strings.TrimSpace(entry.ToolCallID)
		if callID == "" {
			continue
		}
		if _, ok := executableCallIDs[callID]; !ok {
			continue
		}
		toolCallStarts[callID] = start + idx
	}
	return &committedAssistantCoordinate{start: start}, toolCallStarts
}

func committedAssistantCoordinateFields(coordinate *committedAssistantCoordinate) (int, bool) {
	if coordinate == nil {
		return 0, false
	}
	return coordinate.start, true
}

func committedAssistantCoordinateFromFields(start int, set bool) *committedAssistantCoordinate {
	if !set {
		return nil
	}
	return &committedAssistantCoordinate{start: start}
}
