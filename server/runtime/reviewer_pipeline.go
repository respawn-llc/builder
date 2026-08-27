package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/server/llm"
	"core/shared/runtimeids"
	"core/shared/textutil"
	"core/shared/transcript"
)

type defaultReviewerPipeline struct {
	engine *Engine
}

func (r *defaultReviewerPipeline) ShouldRunTurn(frequency string, reviewerClient llm.Client, patchEditsApplied bool) bool {
	if reviewerClient == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(frequency)) {
	case "all":
		return true
	case "edits":
		return patchEditsApplied
	case "off", "":
		return false
	default:
		return false
	}
}

func (r *defaultReviewerPipeline) Prepare(
	ctx context.Context,
	stepID string,
	reviewerClient llm.Client,
) (preparedReviewerRequest, error) {
	if reviewerClient == nil {
		return preparedReviewerRequest{}, errors.New("Reviewer client is required")
	}
	originStepID, err := runtimeids.ParseStepID(stepID)
	if err != nil {
		return preparedReviewerRequest{}, err
	}
	req, err := r.engine.buildReviewerDispatchRequest(ctx, stepID, reviewerClient)
	if err != nil {
		return preparedReviewerRequest{}, fmt.Errorf("build Reviewer request: %w", err)
	}
	cacheObservation, err := r.engine.modelRequests().RequestCache().Prepare(req)
	if err != nil {
		return preparedReviewerRequest{}, err
	}
	if err := r.engine.observePromptCacheRequest(stepID, cacheObservation); err != nil {
		return preparedReviewerRequest{}, err
	}
	return preparedReviewerRequest{
		originStepID:     originStepID,
		client:           reviewerClient,
		request:          req,
		cacheObservation: cacheObservation,
	}, nil
}

func (r *defaultReviewerPipeline) Run(
	ctx context.Context,
	prepared preparedReviewerRequest,
) reviewerProviderResult {
	resp, err := generateWithRetryClient(ctx, prepared.client, prepared.request, nil, nil, nil, nil)
	if err != nil {
		return reviewerProviderResult{err: err}
	}
	cachePct, hasCachePct := resp.Usage.CacheHitPercent()
	result := reviewerSuggestionsResult{
		CacheHitPercent:       cachePct,
		HasCacheHitPercentage: hasCachePct,
	}
	if resp.Assistant.Content != nil {
		result.Suggestions = parseReviewerSuggestionsObject(
			r.engine.reviewerSuggestionsContract,
			*resp.Assistant.Content,
		)
	}
	return reviewerProviderResult{
		suggestions: result,
		usage:       resp.Usage,
	}
}

func (e *Engine) startReviewer(
	ctx context.Context,
	stepID string,
	reviewerClient llm.Client,
	pipeline reviewerPipeline,
) error {
	if e.ReviewerRunning() {
		return nil
	}
	prepared, prepareErr := pipeline.Prepare(ctx, stepID, reviewerClient)
	started, err := e.startReviewerActivity(stepID)
	if err != nil {
		return fmt.Errorf("start Reviewer activity: %w", err)
	}
	if !started {
		return nil
	}
	if prepareErr != nil {
		prepared.originStepID, err = runtimeids.ParseStepID(stepID)
		if err != nil {
			_ = e.completeReviewerActivity(stepID)
			return err
		}
	}
	if !e.launchLifecycleTask(func(lifecycleCtx context.Context) *resultGroupFatal {
		result := reviewerProviderResult{err: prepareErr}
		if prepareErr == nil {
			result = pipeline.Run(lifecycleCtx, prepared)
		}
		deferred := submitEngineRuntimeOperation(e, func(context.Context) (struct{}, error) {
			return struct{}{}, e.applyReviewerProviderResult(prepared, result)
		})
		_, applyErr := deferred.Await(context.WithoutCancel(lifecycleCtx))
		if applyErr == nil {
			return nil
		}
		_ = e.completeReviewerActivity(stepID)
		if errors.Is(applyErr, ErrEngineClosed) || errors.Is(applyErr, context.Canceled) {
			return nil
		}
		e.surfaceRunError(fmt.Errorf("apply Reviewer result: %w", applyErr))
		return nil
	}) {
		_ = e.completeReviewerActivity(stepID)
		return ErrEngineClosed
	}
	return nil
}

func (e *Engine) applyReviewerProviderResult(
	prepared preparedReviewerRequest,
	result reviewerProviderResult,
) error {
	originStepID := prepared.originStepID.String()
	originProvenance := steeringProvenance{exactStep: &prepared.originStepID}
	if result.err != nil {
		persistErr := e.steerOrdered(
			originProvenance,
			steerReviewerErrorIntent(result.err.Error()),
		)
		return errors.Join(persistErr, e.completeReviewerActivity(originStepID))
	}
	if err := e.observePromptCacheResponseRuntime(
		prepared.cacheObservation,
		result.usage,
	); err != nil {
		_ = e.completeReviewerActivity(originStepID)
		return err
	}
	suggestions := result.suggestions.Suggestions
	if len(suggestions) == 0 {
		return e.completeReviewerActivity(originStepID)
	}

	visibility := transcript.EntryVisibilityOngoingCollapsed
	if e.cfg.Reviewer.VerboseOutput {
		visibility = transcript.EntryVisibilityOngoing
	}
	if err := e.steerOrdered(
		originProvenance,
		steerReviewerFeedbackIntent(suggestions, visibility),
	); err != nil {
		_ = e.completeReviewerActivity(originStepID)
		return fmt.Errorf("persist Reviewer feedback: %w", err)
	}
	instruction := formatReviewerDeveloperInstruction(suggestions)
	if err := e.steerOrdered(sessionSteeringProvenance(), steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventDefault,
		true,
		[]llm.Message{{
			Role:        llm.RoleDeveloper,
			MessageType: textutil.Value(llm.MessageTypeReviewerFeedback),
			Content:     textutil.Value(instruction),
		}},
	)); err != nil {
		_ = e.completeReviewerActivity(originStepID)
		return fmt.Errorf("persist Reviewer follow-up instruction: %w", err)
	}
	if !e.launchLifecycleTask(func(ctx context.Context) *resultGroupFatal {
		return e.runReviewerContinuation(ctx, prepared.originStepID, result.suggestions)
	}) {
		_ = e.completeReviewerActivity(originStepID)
		return ErrEngineClosed
	}
	return nil
}

func (e *Engine) runReviewerContinuation(
	ctx context.Context,
	originStepID runtimeids.StepID,
	reviewerResult reviewerSuggestionsResult,
) *resultGroupFatal {
	var status *ReviewerStatus
	err := e.stepLifecycle.RunNext(
		ctx,
		exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn},
		func(stepCtx context.Context, stepID string) error {
			followUp, runErr := e.stepFlow.RunStepLoopWithOptions(stepCtx, stepID, stepLoopOptions{
				ReviewerFrequency:              "off",
				ReviewerClient:                 nil,
				RefreshReviewerConfigOnResolve: false,
			})
			if runErr != nil {
				return fmt.Errorf("run Reviewer follow-up: %w", runErr)
			}
			if followUp.FinalAnswer == nil && !followUp.SilentFinal {
				return errors.New("Reviewer follow-up returned no answer")
			}
			outcome := "applied"
			if followUp.SilentFinal {
				outcome = "noop"
			}
			status = &ReviewerStatus{
				Outcome:               outcome,
				SuggestionsCount:      len(reviewerResult.Suggestions),
				CacheHitPercent:       reviewerResult.CacheHitPercent,
				HasCacheHitPercentage: reviewerResult.HasCacheHitPercentage,
			}
			return e.steer(stepID, steerLocalEntryIntent(storedLocalEntry{
				Role: reviewerStatusEntryRole(*status),
				Text: reviewerStatusText(*status, nil),
			}))
		},
	)
	completeErr := e.completeReviewerActivity(originStepID.String())
	if errors.Is(err, context.Canceled) || errors.Is(err, ErrEngineClosed) {
		return nil
	}
	if err != nil || completeErr != nil {
		e.surfaceRunError(errors.Join(err, completeErr))
	}
	return nil
}
