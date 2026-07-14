package runtime

import (
	"context"
	"errors"
	"strings"

	"core/prompts"
	"core/server/llm"
	"core/server/tools"
	"core/server/workflowruntime"
	"core/shared/toolspec"
	"core/shared/transcript"
)

type defaultStepExecutor struct {
	engine   *Engine
	phase    phaseProtocolEnforcer
	reviewer reviewerPipeline
	messages messageLifecycle
	tools    toolExecutor
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
	localToolCalls               []llm.ToolCall
	hostedToolExecutions         []hostedToolExecution
	noopFinalAnswer              bool
	assistantCommittedCoordinate *committedAssistantCoordinate
	executedToolCall             bool
	patchEditsApplied            bool
	preflightRejection           *completedResponsePreflightRejection
}

type completedResponsePreflightRejection struct {
	cause error
}

func (s *defaultStepExecutor) RunStepLoopWithOptions(ctx context.Context, stepID string, options stepLoopOptions) (stepLoopResult, error) {
	e := s.engine
	// The engine owns the queued user-injection scope for the in-flight top-level
	// step; derive it here so reviewer follow-ups inherit it without the supervisor
	// threading injection IDs through stepLoopOptions.
	options.PendingUserInjectionIDs = e.activeUserInjectionScopeSnapshot()
	executedToolCall := false
	patchEditsApplied := false
	deferredFinal := llm.Message{}
	var deferredFinalCommittedCoordinate *committedAssistantCoordinate
	deferredFinalEventEmitted := false
	hasDeferredFinal := false
	for {
		if err := ctx.Err(); err != nil {
			return stepLoopResult{}, err
		}
		if err := e.drainActiveStepGoalMutations(stepID); err != nil {
			return stepLoopResult{}, err
		}
		if terminal, err := s.workflowDurableCompletionTerminal(ctx, stepID); err != nil {
			return stepLoopResult{}, err
		} else if terminal {
			e.cascadeCompleteActiveGoalOnWorkflowCompletion()
			return stepLoopResult{ExecutedToolCall: executedToolCall}, nil
		}
		if err := s.prepareModelTurn(ctx, stepID); err != nil {
			return stepLoopResult{}, err
		}

		resp, err := e.generateWithMissingToolOutputRepair(
			ctx,
			stepID,
			func() (llm.Request, error) {
				requestPlan, buildErr := e.buildRequestPlanWithExtraItems(ctx, stepID, nil, true)
				if buildErr != nil {
					return llm.Request{}, buildErr
				}
				return requestPlan.Request, nil
			},
			func(delta llm.AssistantDelta) {
				_ = e.steer(stepID, steerAssistantDeltaIntent(delta))
			},
			func(delta llm.ReasoningSummaryDelta) {
				_ = e.steer(stepID, steerReasoningDeltaIntent(delta))
			},
			func() {
				_ = e.steer(stepID, steerClearStreamingStateIntent())
			},
		)
		if err != nil {
			return stepLoopResult{}, err
		}
		if err := e.recordLastUsage(resp.Usage); err != nil {
			return stepLoopResult{}, err
		}

		prepared, err := s.prepareCompletedResponse(ctx, stepID, resp)
		if err != nil {
			return stepLoopResult{}, err
		}
		if prepared.executedToolCall {
			executedToolCall = true
		}
		patchEditsApplied = patchEditsApplied || prepared.patchEditsApplied
		resolution, err := e.resolveCompletedResponseStream(stepID, prepared.resolution)
		if err != nil {
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
				return stepLoopResult{Message: prepared.assistant, ExecutedToolCall: executedToolCall}, nil
			}
			continue
		case completedResponseNextFinalAnswerToolsTerminal:
			e.cascadeCompleteActiveGoalOnWorkflowCompletion()
			return stepLoopResult{Message: prepared.assistant, ExecutedToolCall: true}, nil
		case completedResponseNextAccepted:
		default:
			return stepLoopResult{}, errors.New("completed response preparation produced an invalid next action")
		}

		resp = prepared.response
		phaseTurn := prepared.phaseTurn
		assistantMsg := prepared.assistant
		localToolCalls := prepared.localToolCalls
		hostedToolExecutions := prepared.hostedToolExecutions
		noopFinalAnswer := prepared.noopFinalAnswer
		assistantCommittedCoordinate := prepared.assistantCommittedCoordinate
		assistantEventEmitted := resolution.committedAssistantEventPublished

		if !noopFinalAnswer {
			for _, entry := range resp.Reasoning {
				if err := e.steer(stepID, steerLocalEntryIntent(storedLocalEntry{
					Visibility: transcript.EntryVisibilityDetail,
					Role:       string(transcript.EntryRoleReasoning),
					Text:       entry.Text,
				})); err != nil {
					return stepLoopResult{}, err
				}
			}
			if phaseTurn.MissingAssistantPhase {
				if err := e.steer(stepID, steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeErrorFeedback, Content: missingAssistantPhaseWarning}})); err != nil {
					return stepLoopResult{}, err
				}
			}
		}
		if err := s.appendHostedToolExecutionResults(stepID, hostedToolExecutions); err != nil {
			return stepLoopResult{}, err
		}

		if responseOutputIsReasoningOnly(resp.OutputItems) {
			continue
		}

		if len(localToolCalls) == 0 &&
			len(hostedToolExecutions) == 0 &&
			phaseTurn.EffectivePhase.Is(llm.MessagePhaseFinal) &&
			strings.TrimSpace(assistantMsg.Content) != "" {
			handled, terminal, err := s.handleWorkflowCompletionSubmission(ctx, stepID, assistantMsg.Content)
			if err != nil {
				return stepLoopResult{}, err
			}
			if terminal {
				return stepLoopResult{Message: assistantMsg, ExecutedToolCall: executedToolCall}, nil
			}
			if handled {
				continue
			}
		}

		if len(localToolCalls) == 0 {
			if phaseTurn.MissingAssistantPhase {
				if len(hostedToolExecutions) > 0 {
					_ = e.steer(stepID, steerEventIntent(Event{Kind: EventConversationUpdated, StepID: stepID, CommittedTranscriptChanged: true}))
				}
				if _, err := s.messages.FlushPendingUserInjections(stepID, options.PendingUserInjectionIDs); err != nil {
					return stepLoopResult{}, err
				}
				continue
			}
			if phaseTurn.EnforcePhaseProtocol && assistantMsg.Phase != llm.MessagePhaseFinal {
				if err := e.steer(stepID, steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeErrorFeedback, Content: commentaryWithoutToolCallsWarning}})); err != nil {
					return stepLoopResult{}, err
				}
				if _, err := s.messages.FlushPendingUserInjections(stepID, options.PendingUserInjectionIDs); err != nil {
					return stepLoopResult{}, err
				}
				continue
			}
			if !e.workflowRunActive() && phaseTurn.EnforcePhaseProtocol && assistantMsg.Phase == llm.MessagePhaseFinal && strings.TrimSpace(assistantMsg.Content) == "" && !noopFinalAnswer {
				if err := e.steer(stepID, steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeErrorFeedback, Content: finalWithoutContentWarning}})); err != nil {
					return stepLoopResult{}, err
				}
				if _, err := s.messages.FlushPendingUserInjections(stepID, options.PendingUserInjectionIDs); err != nil {
					return stepLoopResult{}, err
				}
				continue
			}

			flushed, err := s.messages.FlushPendingUserInjections(stepID, options.PendingUserInjectionIDs)
			if err != nil {
				return stepLoopResult{}, err
			}
			if flushed > 0 {
				if assistantMsg.Phase == llm.MessagePhaseFinal && strings.TrimSpace(assistantMsg.Content) != "" && !noopFinalAnswer {
					deferredFinal = assistantMsg
					deferredFinalCommittedCoordinate = cloneCommittedAssistantCoordinate(assistantCommittedCoordinate)
					deferredFinalEventEmitted = assistantEventEmitted
					hasDeferredFinal = true
				}
				continue
			}
			if len(hostedToolExecutions) > 0 {
				_ = e.steer(stepID, steerEventIntent(Event{Kind: EventConversationUpdated, StepID: stepID, CommittedTranscriptChanged: true}))
				continue
			}
			if e.workflowRunActive() && e.cfg.WorkflowRun.Controller != nil {
				content := strings.TrimSpace(assistantMsg.Content)
				if content == "" && !noopFinalAnswer {
					if err := e.steer(stepID, steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeErrorFeedback, Content: finalWithoutContentWarning}})); err != nil {
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
						return stepLoopResult{Message: assistantMsg, ExecutedToolCall: executedToolCall}, nil
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
			resolvedNoopFinalAnswer := noopFinalAnswer
			resolvedCommittedCoordinate := cloneCommittedAssistantCoordinate(assistantCommittedCoordinate)
			var reviewerCompletion *ReviewerStatus
			if hasDeferredFinal {
				if resolvedNoopFinalAnswer {
					resolved = deferredFinal
					resolvedNoopFinalAnswer = isNoopFinalAnswer(resolved)
					resolvedCommittedCoordinate = cloneCommittedAssistantCoordinate(deferredFinalCommittedCoordinate)
					assistantEventEmitted = deferredFinalEventEmitted
				}
				hasDeferredFinal = false
				deferredFinalCommittedCoordinate = nil
				deferredFinalEventEmitted = false
			}
			if resolvedNoopFinalAnswer {
				resolvedCommittedStart, resolvedCommittedStartSet := committedAssistantCoordinateFields(resolvedCommittedCoordinate)
				if e.goalActive() {
					if err := e.steer(stepID, steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeErrorFeedback, Content: goalNoopFinalWarning}})); err != nil {
						return stepLoopResult{}, err
					}
					continue
				}
				return stepLoopResult{Message: resolved, ExecutedToolCall: executedToolCall, NoopFinalAnswer: true, AssistantCommittedStart: resolvedCommittedStart, AssistantCommittedStartSet: resolvedCommittedStartSet}, nil
			}

			resolvedCommittedStart, resolvedCommittedStartSet := committedAssistantCoordinateFields(resolvedCommittedCoordinate)
			effectiveReviewerFrequency := options.ReviewerFrequency
			effectiveReviewerClient := options.ReviewerClient
			if options.RefreshReviewerConfigOnResolve {
				effectiveReviewerFrequency, effectiveReviewerClient = e.reviewerTurnConfigSnapshot()
			}
			if s.reviewer.ShouldRunTurn(effectiveReviewerFrequency, effectiveReviewerClient, patchEditsApplied) {
				if !assistantEventEmitted {
					// The answer is already committed before supervisor entries are appended.
					// Publish it first so live clients never see supervisor entries as a gap
					// after an unannounced committed assistant message.
					_ = s.publishCommittedAssistantMessage(stepID, resolved, resolvedCommittedCoordinate)
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
			}
			if !assistantEventEmitted {
				_ = s.publishCommittedAssistantMessage(stepID, resolved, resolvedCommittedCoordinate)
			}
			if reviewerCompletion != nil {
				if err := e.steer(stepID, steerLocalEntryIntent(storedLocalEntry{Role: reviewerStatusEntryRole(*reviewerCompletion), Text: reviewerStatusText(*reviewerCompletion, nil)})); err != nil {
					return stepLoopResult{}, err
				}
				_ = e.steer(stepID, steerEventIntent(Event{Kind: EventReviewerCompleted, StepID: stepID, Reviewer: reviewerCompletion}))
			}
			if err := e.drainActiveStepGoalMutations(stepID); err != nil {
				return stepLoopResult{}, err
			}
			resolvedCommittedStart, resolvedCommittedStartSet = committedAssistantCoordinateFields(resolvedCommittedCoordinate)
			return stepLoopResult{Message: resolved, ExecutedToolCall: executedToolCall, AssistantCommittedStart: resolvedCommittedStart, AssistantCommittedStartSet: resolvedCommittedStartSet}, nil
		}

		applied, terminal, err := s.executeLocalToolCallsAndAppendResults(ctx, stepID, localToolCalls)
		if err != nil {
			return stepLoopResult{}, err
		}
		patchEditsApplied = patchEditsApplied || applied
		if terminal {
			e.cascadeCompleteActiveGoalOnWorkflowCompletion()
			return stepLoopResult{Message: assistantMsg, ExecutedToolCall: true}, nil
		}
		if _, err := s.messages.FlushPendingUserInjections(stepID, options.PendingUserInjectionIDs); err != nil {
			return stepLoopResult{}, err
		}
	}
}

func (s *defaultStepExecutor) prepareCompletedResponse(ctx context.Context, stepID string, resp llm.Response) (preparedCompletedResponse, error) {
	e := s.engine
	if completed, err := e.observeWorkflowDurableCompletion(ctx); err != nil {
		return preparedCompletedResponse{}, err
	} else if completed {
		return preparedCompletedResponse{
			next:       completedResponseNextExternalWorkflowTerminal,
			resolution: completedResponseDiscardInstruction(),
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
	localToolCalls = phaseTurn.LocalToolCalls
	hostedToolExecutions = phaseTurn.HostedToolExecutions
	executedToolCall := len(localToolCalls) > 0 || len(hostedToolExecutions) > 0
	noopFinalAnswer := isNoopFinalAnswer(assistantMsg)

	if rejection := classifyCompletedResponsePreflightRejection(e.workflowRunActive(), localToolCalls, hostedToolExecutions); rejection != nil {
		return preparedCompletedResponse{
			next:                 completedResponseNextWorkflowPreflightRejected,
			resolution:           completedResponseDiscardInstruction(),
			response:             resp,
			phaseTurn:            phaseTurn,
			assistant:            assistantMsg,
			localToolCalls:       localToolCalls,
			hostedToolExecutions: hostedToolExecutions,
			noopFinalAnswer:      noopFinalAnswer,
			executedToolCall:     executedToolCall,
			preflightRejection:   rejection,
		}, nil
	}

	finalAnswerWithToolCalls := assistantMsg.Phase == llm.MessagePhaseFinal &&
		strings.TrimSpace(assistantMsg.Content) != "" &&
		executedToolCall
	patchEditsApplied := false
	if finalAnswerWithToolCalls {
		if err := e.markProviderVisibleModelRecovery(stepID); err != nil {
			return preparedCompletedResponse{}, err
		}
		applied, terminal, err := s.materializeFinalAnswerToolCalls(ctx, stepID, localToolCalls, hostedToolExecutions)
		if err != nil {
			return preparedCompletedResponse{}, err
		}
		patchEditsApplied = applied
		if terminal {
			return preparedCompletedResponse{
				next:              completedResponseNextFinalAnswerToolsTerminal,
				resolution:        completedResponseDiscardInstruction(),
				response:          resp,
				phaseTurn:         phaseTurn,
				assistant:         assistantMsg,
				noopFinalAnswer:   noopFinalAnswer,
				executedToolCall:  true,
				patchEditsApplied: patchEditsApplied,
			}, nil
		}
		assistantMsg.ToolCalls = nil
		localToolCalls = nil
		hostedToolExecutions = nil
		phaseTurn.Assistant = assistantMsg
		phaseTurn.LocalToolCalls = nil
		phaseTurn.HostedToolExecutions = nil
		if err := e.steer(stepID, steerEventIntent(Event{Kind: EventConversationUpdated, StepID: stepID, CommittedTranscriptChanged: true})); err != nil {
			return preparedCompletedResponse{}, err
		}
	}

	if !noopFinalAnswer {
		if err := e.steer(stepID, steerEventIntent(Event{
			Kind:   EventModelResponse,
			StepID: stepID,
			ModelResponse: &ModelResponseTrace{
				AssistantPhase:   assistantMsg.Phase,
				AssistantChars:   len(assistantMsg.Content),
				ToolCallsCount:   len(resp.ToolCalls),
				OutputItemsCount: len(resp.OutputItems),
				OutputItemTypes:  summarizeOutputItemTypes(resp.OutputItems),
			},
		})); err != nil {
			return preparedCompletedResponse{}, err
		}
	}
	if !noopFinalAnswer && !finalAnswerWithToolCalls {
		if err := e.markProviderVisibleModelRecovery(stepID); err != nil {
			return preparedCompletedResponse{}, err
		}
	}
	if err := e.steer(stepID, steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{assistantMsg})); err != nil {
		return preparedCompletedResponse{}, err
	}

	var assistantCommittedCoordinate *committedAssistantCoordinate
	if !noopFinalAnswer {
		executableCallIDs := make(map[string]struct{}, len(localToolCalls))
		for _, call := range localToolCalls {
			if callID := strings.TrimSpace(call.ID); callID != "" {
				executableCallIDs[callID] = struct{}{}
			}
		}
		var toolCallStarts map[string]int
		assistantCommittedCoordinate, toolCallStarts = committedStartsForPersistedAssistantMessage(e, assistantMsg, executableCallIDs)
		e.rememberPendingToolCallStarts(toolCallStarts)
	}

	resolution := completedResponseDiscardInstruction()
	if committedAssistantMessageFinalizesStreaming(assistantMsg) {
		if assistantCommittedCoordinate == nil {
			return preparedCompletedResponse{}, errors.New("persisted assistant text row has no committed transcript coordinate")
		}
		resolution = completedResponseFinalizeInstruction(assistantMsg, assistantCommittedCoordinate)
	} else if len(VisibleChatEntriesFromMessage(assistantMsg)) > 0 {
		if err := s.publishCommittedAssistantMessage(stepID, assistantMsg, assistantCommittedCoordinate); err != nil {
			return preparedCompletedResponse{}, err
		}
	}

	return preparedCompletedResponse{
		next:                         completedResponseNextAccepted,
		resolution:                   resolution,
		response:                     resp,
		phaseTurn:                    phaseTurn,
		assistant:                    assistantMsg,
		localToolCalls:               localToolCalls,
		hostedToolExecutions:         hostedToolExecutions,
		noopFinalAnswer:              noopFinalAnswer,
		assistantCommittedCoordinate: assistantCommittedCoordinate,
		executedToolCall:             executedToolCall,
		patchEditsApplied:            patchEditsApplied,
	}, nil
}

func classifyCompletedResponsePreflightRejection(workflowActive bool, localToolCalls []llm.ToolCall, hostedToolExecutions []hostedToolExecution) *completedResponsePreflightRejection {
	err := workflowPreflightError(workflowActive, localToolCalls, hostedToolExecutions)
	if err == nil {
		return nil
	}
	return &completedResponsePreflightRejection{cause: err}
}

func (s *defaultStepExecutor) materializeFinalAnswerToolCalls(ctx context.Context, stepID string, localToolCalls []llm.ToolCall, hostedToolExecutions []hostedToolExecution) (bool, bool, error) {
	e := s.engine
	toolCallMessage := llm.Message{
		Role:      llm.RoleAssistant,
		Phase:     llm.MessagePhaseCommentary,
		ToolCalls: append([]llm.ToolCall(nil), localToolCalls...),
	}
	for _, hosted := range hostedToolExecutions {
		toolCallMessage.ToolCalls = append(toolCallMessage.ToolCalls, hosted.Call)
	}
	if err := e.steer(stepID, steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{toolCallMessage})); err != nil {
		return false, false, err
	}

	executableCallIDs := make(map[string]struct{}, len(localToolCalls))
	for _, call := range localToolCalls {
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

	patchEditsApplied, terminal, err := s.executeLocalToolCallsAndAppendResults(ctx, stepID, localToolCalls)
	if err != nil {
		return false, false, err
	}
	if terminal {
		if err := s.appendHostedToolExecutionResults(stepID, hostedToolExecutions); err != nil {
			return false, false, err
		}
		return patchEditsApplied, true, nil
	}
	if err := s.appendHostedToolExecutionResults(stepID, hostedToolExecutions); err != nil {
		return false, false, err
	}
	return patchEditsApplied, terminal, nil
}

func (s *defaultStepExecutor) executeLocalToolCallsAndAppendResults(ctx context.Context, stepID string, localToolCalls []llm.ToolCall) (bool, bool, error) {
	if len(localToolCalls) == 0 {
		return false, false, nil
	}
	e := s.engine
	results, err := s.tools.ExecuteToolCalls(ctx, stepID, localToolCalls)
	if err != nil {
		return false, false, err
	}
	patchEditsApplied := false
	terminal := hasWorkflowTerminalResult(results)
	customToolCalls := customToolCallIDs(localToolCalls)
	for _, result := range results {
		if !result.IsError && (result.Name == toolspec.ToolPatch || result.Name == toolspec.ToolEdit) {
			patchEditsApplied = true
		}
		msg := llm.Message{Role: llm.RoleTool, Content: string(result.Output), ToolCallID: result.CallID, Name: string(result.Name)}
		msg.MessageType = llm.ToolOutputMessageType(customToolCalls[result.CallID])
		if err := e.steer(stepID, steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{msg})); err != nil {
			return false, false, err
		}
	}
	durableTerminal, err := s.engine.observeWorkflowDurableCompletion(ctx)
	if err != nil {
		return false, false, err
	}
	return patchEditsApplied, terminal || durableTerminal, nil
}

func (s *defaultStepExecutor) workflowDurableCompletionTerminal(ctx context.Context, stepID string) (bool, error) {
	completed, err := s.engine.observeWorkflowDurableCompletion(ctx)
	if err != nil || !completed {
		return false, err
	}
	if err := s.engine.steer(stepID, steerClearStreamingStateIntent()); err != nil {
		return false, err
	}
	return true, nil
}

func (s *defaultStepExecutor) appendHostedToolExecutionResults(stepID string, hostedToolExecutions []hostedToolExecution) error {
	e := s.engine
	for _, hosted := range hostedToolExecutions {
		if err := s.publishHostedToolStart(stepID, hosted.Call); err != nil {
			return err
		}
		if err := e.steer(stepID, steerToolCompletionIntent(hosted.Result)); err != nil {
			return err
		}
		msg := llm.Message{Role: llm.RoleTool, Content: string(hosted.Result.Output), ToolCallID: hosted.Result.CallID, Name: string(hosted.Result.Name)}
		if err := e.steer(stepID, steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{msg})); err != nil {
			return err
		}
	}
	return nil
}

func (s *defaultStepExecutor) publishHostedToolStart(stepID string, call llm.ToolCall) error {
	normalized := normalizeToolCallForTranscript(call, s.engine.transcriptWorkingDir())
	return s.engine.steer(stepID, steerEventIntent(Event{
		Kind:                       EventToolCallStarted,
		StepID:                     stepID,
		ToolCall:                   &normalized,
		CommittedTranscriptChanged: true,
	}))
}

func (s *defaultStepExecutor) handleWorkflowCompletionSubmission(ctx context.Context, stepID string, content string) (bool, bool, error) {
	e := s.engine
	if !e.workflowRunActive() || e.cfg.WorkflowRun.Controller == nil {
		return false, false, nil
	}
	outcome, err := s.workflowCompletionAdapter().Evaluate(ctx, content)
	if err != nil {
		return false, false, err
	}
	if outcome.Applicable {
		if !outcome.Done {
			terminal, nudgeErr := s.appendWorkflowInvalidCompletionNudge(ctx, stepID, outcome.Continue)
			return true, terminal, nudgeErr
		}
		if completeErr := outcome.Complete(ctx); completeErr != nil {
			terminal, nudgeErr := s.appendWorkflowInvalidCompletionNudge(ctx, stepID, completeErr)
			return true, terminal, nudgeErr
		}
		return true, true, nil
	}
	mode, err := e.workflowCompletionMode(ctx)
	if err != nil {
		return false, false, err
	}
	content = strings.TrimSpace(content)
	if mode == workflowruntime.CompletionModeShellCommand {
		terminal, nudgeErr := s.appendWorkflowInvalidCompletionNudge(ctx, stepID, errors.New("normal final answers do not complete shell-command workflow nodes"))
		return true, terminal, nudgeErr
	}
	if mode == workflowruntime.CompletionModeTool {
		record, recordErr := e.recordWorkflowProtocolViolation(ctx, workflowruntime.ViolationKindInvalidCompletion, content)
		if recordErr != nil {
			return true, false, recordErr
		}
		if record.Interrupted {
			return true, true, nil
		}
		if err := e.steer(stepID, steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeErrorFeedback, Content: workflowFinalAnswerNudge}})); err != nil {
			return true, false, err
		}
		return true, false, nil
	}
	return false, false, nil
}

func (s *defaultStepExecutor) completeWorkflowRunFromParsed(ctx context.Context, parsed workflowruntime.ParsedCompletion) error {
	e := s.engine
	_, completeErr := e.cfg.WorkflowRun.Controller.CompleteWorkflowRun(ctx, workflowruntime.CompletionRequest{
		RunID:              e.cfg.WorkflowRun.Contract.RunID,
		ExpectedGeneration: e.cfg.WorkflowRun.Contract.ExpectedGeneration,
		RequireGeneration:  e.cfg.WorkflowRun.Contract.RequireGeneration,
		TransitionID:       parsed.TransitionID,
		OutputValues:       parsed.OutputValues,
		Commentary:         parsed.Commentary,
	})
	return completeErr
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
	return false, e.steer(stepID, steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeErrorFeedback, Content: content}}))
}

func (e *Engine) currentWorkflowCompletionInstructions(ctx context.Context) (string, error) {
	if !e.workflowRunActive() {
		return "", nil
	}
	mode, err := e.workflowCompletionMode(ctx)
	if err != nil {
		return "", err
	}
	workflowShortID := ""
	if e.cfg.WorkflowRun != nil {
		workflowShortID = e.cfg.WorkflowRun.Instructions.WorkflowShortID
	}
	return workflowCompletionInstructionsFragment(mode, workflowShortID, e.cfg.WorkflowRun.Contract)
}

func customToolCallIDs(calls []llm.ToolCall) map[string]bool {
	if len(calls) == 0 {
		return nil
	}
	out := make(map[string]bool, len(calls))
	for _, call := range calls {
		if call.Custom && strings.TrimSpace(call.ID) != "" {
			out[call.ID] = true
		}
	}
	return out
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

func (s *defaultStepExecutor) publishCommittedAssistantMessage(stepID string, msg llm.Message, coordinate *committedAssistantCoordinate) error {
	return s.engine.steer(stepID, steerCommittedAssistantMessageIntent(msg, coordinate))
}

func committedAssistantMessageFinalizesStreaming(msg llm.Message) bool {
	if msg.Role != llm.RoleAssistant || isNoopFinalAnswer(msg) {
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
