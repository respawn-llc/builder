package runtime

import (
	"context"
	"fmt"
	"strings"

	"core/server/llm"
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

func (r *defaultReviewerPipeline) PrepareFollowUp(
	ctx context.Context,
	stepID string,
	reviewerClient llm.Client,
) (reviewerFollowUpPreparation, error) {
	e := r.engine
	_ = e.steer(stepID, steerEventIntent(Event{Kind: EventReviewerStarted, StepID: stepID}))
	reviewerResult, err := r.RunSuggestions(ctx, stepID, reviewerClient)
	if err != nil {
		return reviewerFollowUpPreparation{
			Completion: &ReviewerStatus{
				Outcome: "failed",
				Error:   strings.TrimSpace(err.Error()),
			},
		}, nil
	}
	if len(reviewerResult.Suggestions) == 0 {
		return reviewerFollowUpPreparation{
			CacheHitPercent:       reviewerResult.CacheHitPercent,
			HasCacheHitPercentage: reviewerResult.HasCacheHitPercentage,
			Completion:            &ReviewerStatus{Outcome: "no_suggestions"},
		}, nil
	}
	suggestions := append([]string(nil), reviewerResult.Suggestions...)
	return reviewerFollowUpPreparation{
		Suggestions:           suggestions,
		SuggestionsText:       reviewerSuggestionsText(suggestions),
		Instruction:           formatReviewerDeveloperInstruction(suggestions),
		CacheHitPercent:       reviewerResult.CacheHitPercent,
		HasCacheHitPercentage: reviewerResult.HasCacheHitPercentage,
	}, nil
}

func (r *defaultReviewerPipeline) RunSuggestions(ctx context.Context, stepID string, reviewerClient llm.Client) (reviewerSuggestionsResult, error) {
	e := r.engine
	if reviewerClient == nil {
		return reviewerSuggestionsResult{}, nil
	}
	req, err := e.buildReviewerRequestForStep(ctx, stepID, reviewerClient)
	if err != nil {
		return reviewerSuggestionsResult{}, fmt.Errorf("build reviewer request: %w", err)
	}
	resp, err := e.generateWithRetryClient(ctx, stepID, reviewerClient, req, nil, nil, nil)
	if err != nil {
		return reviewerSuggestionsResult{}, err
	}
	cachePct, hasCachePct := resp.Usage.CacheHitPercent()
	if resp.Assistant.Content == nil {
		return reviewerSuggestionsResult{
			CacheHitPercent:       cachePct,
			HasCacheHitPercentage: hasCachePct,
		}, nil
	}
	return reviewerSuggestionsResult{
		Suggestions:           parseReviewerSuggestionsObject(*resp.Assistant.Content),
		CacheHitPercent:       cachePct,
		HasCacheHitPercentage: hasCachePct,
	}, nil
}
