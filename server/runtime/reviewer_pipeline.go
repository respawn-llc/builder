package runtime

import (
	"context"
	"fmt"
	"strings"

	"core/server/llm"
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

func (r *defaultReviewerPipeline) RunFollowUp(ctx context.Context, stepID string, original llm.Message, originalCommittedStart int, originalCommittedStartSet bool, reviewerClient llm.Client) (reviewerFollowUpResult, error) {
	e := r.engine
	reviewerResult, err := r.RunSuggestions(ctx, stepID, reviewerClient)
	if err != nil {
		if persistErr := e.steer(stepID, steerReviewerErrorIntent(err.Error())); persistErr != nil {
			return reviewerFollowUpResult{}, fmt.Errorf("persist Reviewer error: %w", persistErr)
		}
		status := ReviewerStatus{
			Outcome: "failed",
			Error:   strings.TrimSpace(err.Error()),
		}
		return reviewerFollowUpResult{Message: original, Completion: &status, AssistantCommittedStart: originalCommittedStart, AssistantCommittedStartSet: originalCommittedStartSet}, nil
	}
	suggestions := reviewerResult.Suggestions
	if len(suggestions) == 0 {
		status := ReviewerStatus{Outcome: "no_suggestions"}
		return reviewerFollowUpResult{Message: original, Completion: &status, AssistantCommittedStart: originalCommittedStart, AssistantCommittedStartSet: originalCommittedStartSet}, nil
	}
	visibility := transcript.EntryVisibilityOngoingCollapsed
	if e.cfg.Reviewer.VerboseOutput {
		visibility = transcript.EntryVisibilityOngoing
	}
	if err := e.steer(stepID, steerReviewerFeedbackIntent(suggestions, visibility)); err != nil {
		return reviewerFollowUpResult{}, fmt.Errorf("persist Reviewer feedback: %w", err)
	}
	instruction := formatReviewerDeveloperInstruction(suggestions)
	if err := e.steer(stepID, steerMessagesWithPersistenceIntent(steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeReviewerFeedback), Content: textutil.Value(instruction)}})); err != nil {
		return reviewerFollowUpResult{}, fmt.Errorf("persist Reviewer follow-up instruction: %w", err)
	}
	status := ReviewerStatus{
		Outcome:               "pending",
		SuggestionsCount:      len(suggestions),
		CacheHitPercent:       reviewerResult.CacheHitPercent,
		HasCacheHitPercentage: reviewerResult.HasCacheHitPercentage,
	}
	return reviewerFollowUpResult{
		Message:                    original,
		Completion:                 &status,
		AssistantCommittedStart:    originalCommittedStart,
		AssistantCommittedStartSet: originalCommittedStartSet,
		FollowUpRequired:           true,
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
