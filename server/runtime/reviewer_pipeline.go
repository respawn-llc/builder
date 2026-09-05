package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/shared/runtimeids"
	"core/shared/transcript"
)

type defaultReviewerPipeline struct {
	engine *Engine
}

func completeReviewerActivityError(e *Engine, stepID string) error {
	if err := e.completeReviewerActivity(stepID); err != nil {
		return fmt.Errorf("complete Reviewer activity: %w", err)
	}
	return nil
}

func (r *defaultReviewerPipeline) ShouldRunTurn(frequency string, reviewerClient *observedModelClient, patchEditsApplied bool) bool {
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
	reviewerClient *observedModelClient,
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
	observed, err := r.engine.prepareCacheObservedRequest(stepID, req, cacheResponseObservationRuntime)
	if err != nil {
		return preparedReviewerRequest{}, err
	}
	return preparedReviewerRequest{
		originStepID: originStepID,
		client:       reviewerClient,
		request:      observed,
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
	result := reviewerSuggestionsResult{}
	if resp.Assistant.Content != nil {
		result.Suggestions = parseReviewerSuggestionsObject(
			r.engine.reviewerSuggestionsContract,
			*resp.Assistant.Content,
		)
	}
	return reviewerProviderResult{
		suggestions: result,
	}
}

func (e *Engine) startReviewer(
	ctx context.Context,
	stepID string,
	reviewerClient *observedModelClient,
	pipeline reviewerPipeline,
) error {
	if e.ReviewerActive() {
		return nil
	}
	prepared, prepareErr := pipeline.Prepare(ctx, stepID, reviewerClient)
	if prepareErr != nil {
		var err error
		prepared.originStepID, err = runtimeids.ParseStepID(stepID)
		if err != nil {
			return err
		}
	}
	if prepareErr == nil && !e.reserveReviewerActivity(stepID) {
		if e.closed.Load() {
			return ErrEngineClosed
		}
		return nil
	}
	if !e.launchLifecycleTask(func(lifecycleCtx context.Context) *resultGroupFatal {
		result := reviewerProviderResult{err: prepareErr}
		if prepareErr == nil {
			started, err := e.startReviewerActivity(stepID)
			if err != nil {
				e.releaseReviewerActivity(stepID)
				e.surfaceRunError(fmt.Errorf("start Reviewer activity: %w", err))
				return nil
			}
			if !started {
				e.releaseReviewerActivity(stepID)
				return nil
			}
			result = pipeline.Run(lifecycleCtx, prepared)
		}
		deferred := submitEngineRuntimeOperation(e, func(context.Context) (struct{}, error) {
			return struct{}{}, e.applyReviewerProviderResult(prepared, result)
		})
		_, applyErr := deferred.Await(context.WithoutCancel(lifecycleCtx))
		if applyErr == nil && result.err == nil && len(result.suggestions.Suggestions) != 0 {
			if e.cfg.SubmitAgentSteer == nil {
				applyErr = errors.New("Agent steer submission is not configured")
			} else {
				applyErr = e.cfg.SubmitAgentSteer(lifecycleCtx, supervisorSteer(result.suggestions.Suggestions))
			}
		}
		completionErr := completeReviewerActivityError(e, stepID)
		if applyErr == nil {
			if completionErr != nil {
				e.surfaceRunError(completionErr)
			}
			return nil
		}
		if errors.Is(applyErr, ErrEngineClosed) || errors.Is(applyErr, context.Canceled) {
			if completionErr != nil {
				e.surfaceRunError(completionErr)
			}
			return nil
		}
		e.surfaceRunError(fmt.Errorf("apply Reviewer result: %w", errors.Join(applyErr, completionErr)))
		return nil
	}) {
		if prepareErr == nil {
			e.releaseReviewerActivity(stepID)
		}
		return ErrEngineClosed
	}
	return nil
}

func (e *Engine) applyReviewerProviderResult(
	prepared preparedReviewerRequest,
	result reviewerProviderResult,
) error {
	originProvenance := steeringProvenance{exactStep: &prepared.originStepID}
	if result.err != nil {
		return e.steerOrdered(
			originProvenance,
			steerReviewerErrorIntent(result.err.Error()),
		)
	}
	suggestions := result.suggestions.Suggestions
	if len(suggestions) == 0 {
		return nil
	}

	visibility := transcript.EntryVisibilityOngoingCollapsed
	if e.cfg.Reviewer.VerboseOutput {
		visibility = transcript.EntryVisibilityOngoing
	}
	if err := e.steerOrdered(
		originProvenance,
		steerReviewerFeedbackIntent(suggestions, visibility),
	); err != nil {
		return fmt.Errorf("persist Reviewer feedback: %w", err)
	}
	return nil
}
