package runtime

import (
	"context"
	"fmt"
	"strings"

	"core/server/llm"
)

type defaultReviewerPipeline struct {
	engine     *Engine
	stepRunner stepLoopRunner
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

func (r *defaultReviewerPipeline) RunFollowUp(ctx context.Context, stepID string, original llm.Message, reviewerClient llm.Client) (reviewerFollowUpResult, error) {
	e := r.engine
	_ = e.steer(stepID, steerEventIntent(Event{Kind: EventReviewerStarted, StepID: stepID}))
	reviewerResult, err := r.RunSuggestions(ctx, stepID, reviewerClient)
	if err != nil {
		status := ReviewerStatus{
			Outcome: "failed",
			Error:   strings.TrimSpace(err.Error()),
		}
		return reviewerFollowUpResult{Message: original, Completion: &status}, nil
	}
	suggestions := reviewerResult.Suggestions
	if len(suggestions) == 0 {
		status := ReviewerStatus{Outcome: "no_suggestions"}
		return reviewerFollowUpResult{Message: original, Completion: &status}, nil
	}
	if e.cfg.Reviewer.VerboseOutput {
		suggestionsText := reviewerSuggestionsText(suggestions)
		_ = e.steer(stepID, steerLocalEntryIntent(storedLocalEntry{Role: "reviewer_suggestions", Text: suggestionsText, CondensedText: suggestionsText}))
	}

	instruction := formatReviewerDeveloperInstruction(suggestions)
	if err := e.steer(stepID, steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeReviewerFeedback, Content: instruction}})); err != nil {
		status := ReviewerStatus{
			Outcome:               "followup_failed",
			SuggestionsCount:      len(suggestions),
			CacheHitPercent:       reviewerResult.CacheHitPercent,
			HasCacheHitPercentage: reviewerResult.HasCacheHitPercentage,
			Error:                 strings.TrimSpace(err.Error()),
		}
		return reviewerFollowUpResult{Message: original, Completion: &status}, nil
	}
	if r.stepRunner == nil {
		status := ReviewerStatus{
			Outcome:          "followup_failed",
			SuggestionsCount: len(suggestions),
			Error:            "reviewer step runner is not configured",
		}
		return reviewerFollowUpResult{Message: original, Completion: &status}, nil
	}

	followUp, err := r.stepRunner.RunStepLoopWithOptions(ctx, stepID, stepLoopOptions{
		ReviewerFrequency:              "off",
		ReviewerClient:                 nil,
		RefreshReviewerConfigOnResolve: false,
	})
	if err != nil {
		status := ReviewerStatus{
			Outcome:               "followup_failed",
			SuggestionsCount:      len(suggestions),
			CacheHitPercent:       reviewerResult.CacheHitPercent,
			HasCacheHitPercentage: reviewerResult.HasCacheHitPercentage,
			Error:                 strings.TrimSpace(err.Error()),
		}
		return reviewerFollowUpResult{Message: original, Completion: &status}, nil
	}
	if followUp.NoopFinalAnswer || isNoopFinalAnswer(followUp.Message) {
		status := ReviewerStatus{
			Outcome:               "noop",
			SuggestionsCount:      len(suggestions),
			CacheHitPercent:       reviewerResult.CacheHitPercent,
			HasCacheHitPercentage: reviewerResult.HasCacheHitPercentage,
		}
		return reviewerFollowUpResult{Message: original, Completion: &status}, nil
	}
	status := ReviewerStatus{
		Outcome:               "applied",
		SuggestionsCount:      len(suggestions),
		CacheHitPercent:       reviewerResult.CacheHitPercent,
		HasCacheHitPercentage: reviewerResult.HasCacheHitPercentage,
	}
	return reviewerFollowUpResult{
		Message:               followUp.Message,
		Completion:            &status,
		AssistantEventEmitted: !followUp.NoopFinalAnswer && !isNoopFinalAnswer(followUp.Message),
	}, nil
}

func (r *defaultReviewerPipeline) RunSuggestions(ctx context.Context, stepID string, reviewerClient llm.Client) (reviewerSuggestionsResult, error) {
	e := r.engine
	if reviewerClient == nil {
		return reviewerSuggestionsResult{}, nil
	}
	req, err := e.buildReviewerRequest(ctx, reviewerClient)
	if err != nil {
		return reviewerSuggestionsResult{}, fmt.Errorf("build reviewer request: %w", err)
	}
	resp, err := e.generateWithRetryClient(ctx, stepID, reviewerClient, req, nil, nil, nil)
	if err != nil {
		return reviewerSuggestionsResult{}, err
	}
	cachePct, hasCachePct := resp.Usage.CacheHitPercent()
	return reviewerSuggestionsResult{
		Suggestions:           parseReviewerSuggestionsObject(resp.Assistant.Content),
		CacheHitPercent:       cachePct,
		HasCacheHitPercentage: hasCachePct,
	}, nil
}
