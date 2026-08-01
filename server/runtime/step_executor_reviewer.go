package runtime

import (
	"context"

	"core/server/llm"
	"core/shared/textutil"
)

type reviewerContinuationOutcome struct {
	ran                   bool
	preparation           reviewerFollowUpPreparation
	resolved              llm.Message
	resolvedCoordinate    *committedAssistantCoordinate
	assistantEventEmitted bool
	handledRecursiveRun   bool
	recursiveResult       stepLoopResult
}

func (s *defaultStepExecutor) runReviewerContinuation(
	ctx context.Context,
	stepID string,
	options stepLoopOptions,
	resolved llm.Message,
	resolvedCoordinate *committedAssistantCoordinate,
	assistantEventEmitted bool,
	executedToolCall bool,
	patchEditsApplied bool,
	boundary *agentStepBoundaryFinalizer,
) (reviewerContinuationOutcome, error) {
	e := s.engine
	effectiveFrequency := options.ReviewerFrequency
	effectiveClient := options.ReviewerClient
	if options.RefreshReviewerConfigOnResolve {
		effectiveFrequency, effectiveClient = e.reviewerTurnConfigSnapshot()
	}
	if !s.reviewer.ShouldRunTurn(effectiveFrequency, effectiveClient, patchEditsApplied) {
		return reviewerContinuationOutcome{}, nil
	}
	if !assistantEventEmitted {
		_ = s.publishCommittedAssistantMessage(stepID, resolved, resolvedCoordinate)
		assistantEventEmitted = true
	}
	preparation, err := s.reviewer.PrepareFollowUp(ctx, stepID, effectiveClient)
	if err != nil {
		return reviewerContinuationOutcome{}, err
	}
	outcome := reviewerContinuationOutcome{
		ran:                   true,
		preparation:           preparation,
		resolved:              resolved,
		resolvedCoordinate:    cloneCommittedAssistantCoordinate(resolvedCoordinate),
		assistantEventEmitted: assistantEventEmitted,
	}
	if preparation.Completion != nil {
		return outcome, nil
	}
	if e.cfg.Reviewer.VerboseOutput {
		if err := e.steer(stepID, steerFinalLocalEntryIntent(storedLocalEntry{
			Role:          "reviewer_suggestions",
			Text:          preparation.SuggestionsText,
			CondensedText: textutil.Value(preparation.SuggestionsText),
		})); err != nil {
			return reviewerContinuationOutcome{}, err
		}
	}
	if err := e.drainActiveStepGoalMutations(stepID); err != nil {
		return reviewerContinuationOutcome{}, err
	}
	if err := completeAgentStepBoundary(boundary, stepID); err != nil {
		return reviewerContinuationOutcome{}, err
	}
	if err := e.steer(stepID, steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventDefault,
		true,
		[]llm.Message{{
			Role:        llm.RoleDeveloper,
			MessageType: textutil.Value(llm.MessageTypeReviewerFeedback),
			Content:     textutil.Value(preparation.Instruction),
		}},
	)); err != nil {
		return reviewerContinuationOutcome{
			resolved:              resolved,
			resolvedCoordinate:    cloneCommittedAssistantCoordinate(resolvedCoordinate),
			assistantEventEmitted: assistantEventEmitted,
			handledRecursiveRun:   true,
			recursiveResult:       stepLoopResult{FinalAnswer: textutil.Value(resolved), ExecutedToolCall: executedToolCall},
		}, err
	}
	followUp, followUpErr := e.stepFlow.RunStepLoopWithOptions(ctx, stepID, stepLoopOptions{
		ReviewerFrequency:              "off",
		ReviewerClient:                 nil,
		RefreshReviewerConfigOnResolve: false,
	})
	reviewerCompletion := &ReviewerStatus{
		Outcome:               "applied",
		SuggestionsCount:      len(preparation.Suggestions),
		CacheHitPercent:       preparation.CacheHitPercent,
		HasCacheHitPercentage: preparation.HasCacheHitPercentage,
	}
	if followUpErr == nil {
		if followUp.FinalAnswer == nil || isNoopFinalAnswer(*followUp.FinalAnswer) {
			reviewerCompletion.Outcome = "noop"
		} else {
			resolved = *followUp.FinalAnswer
			outcome.resolvedCoordinate = committedAssistantCoordinateFromFields(
				followUp.AssistantCommittedStart,
				followUp.AssistantCommittedStartSet,
			)
			outcome.assistantEventEmitted = true
		}
	}
	if err := e.steer(stepID, steerFinalLocalEntryIntent(storedLocalEntry{
		Role: reviewerStatusEntryRole(*reviewerCompletion),
		Text: reviewerStatusText(*reviewerCompletion, nil),
	})); err != nil {
		return reviewerContinuationOutcome{}, err
	}
	if followUpErr != nil {
		return reviewerContinuationOutcome{
			resolved:              resolved,
			resolvedCoordinate:    cloneCommittedAssistantCoordinate(outcome.resolvedCoordinate),
			assistantEventEmitted: outcome.assistantEventEmitted,
			handledRecursiveRun:   true,
			recursiveResult:       stepLoopResult{FinalAnswer: textutil.Value(resolved), ExecutedToolCall: executedToolCall},
		}, followUpErr
	}
	_ = e.steer(stepID, steerEventIntent(Event{Kind: EventReviewerCompleted, StepID: stepID, Reviewer: reviewerCompletion}))
	outcome.resolved = resolved
	outcome.handledRecursiveRun = true
	outcome.recursiveResult = stepLoopResult{
		FinalAnswer:                textutil.Value(resolved),
		ExecutedToolCall:           executedToolCall,
		AssistantCommittedStart:    followUp.AssistantCommittedStart,
		AssistantCommittedStartSet: followUp.AssistantCommittedStartSet,
	}
	return outcome, nil
}
