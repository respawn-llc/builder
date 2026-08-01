package runtime

import (
	"context"

	"core/server/llm"
	"core/shared/transcript"
)

func reviewerSuggestionsStructuredOutput() *llm.StructuredOutput {
	return &llm.StructuredOutput{
		Name: "reviewer_suggestions",
		Schema: mustJSON(map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"suggestions": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "string",
					},
				},
			},
			"required": []string{"suggestions"},
		}),
		Strict: true,
	}
}

func (e *Engine) buildReviewerRequest(ctx context.Context, reviewerClient llm.Client) (llm.Request, error) {
	return e.buildReviewerRequestForStep(ctx, "", reviewerClient)
}

func (e *Engine) buildReviewerRequestForStep(ctx context.Context, stepID string, reviewerClient llm.Client) (llm.Request, error) {
	reviewerCfg := e.reviewerRequestConfigSnapshot()
	items := e.transcriptRuntimeState().SnapshotItems()
	if boundary := e.agentStepBoundary(stepID); boundary != nil {
		if finalAssistant := boundary.StagedFinalAssistantMessage(); finalAssistant != nil {
			items = append(items, llm.ItemsFromMessages([]llm.Message{*finalAssistant})...)
		}
	}
	reviewerItems, err := buildReviewerRequestItemsWithBuilder(items, newActiveMetaContextBuilder(e.store.Meta(), e.transcriptWorkingDir(), e.cfg.Model, e.ThinkingLevel(), e.cfg.GlobalConfigDir, e.cfg.SkillPolicy, e.reviewerMetaTimestamp()), e.cfg.HeadlessMode)
	if err != nil {
		return llm.Request{}, err
	}
	systemPrompt, err := e.reviewerSystemPrompt(ctx)
	if err != nil {
		return llm.Request{}, err
	}
	req := llm.Request{
		Model:                   reviewerCfg.Model,
		Temperature:             1,
		MaxTokens:               0,
		FastMode:                e.FastModeEnabled(),
		ReasoningEffort:         reviewerCfg.ThinkingLevel,
		SupportsReasoningEffort: reviewerCfg.ModelCapabilities.SupportsReasoningEffort,
		SystemPrompt:            systemPrompt,
		SessionID:               reviewerSessionID(e.store.Meta().SessionID),
		Items:                   reviewerItems,
		Tools:                   []llm.Tool{},
		ToolChoiceMode:          llm.ToolChoiceModeAutomatic,
		StructuredOutput:        reviewerSuggestionsStructuredOutput(),
	}
	if supportsPromptCacheKeyForClient(ctx, reviewerClient) {
		if cacheKey := e.conversationPromptCacheKey(reviewerSessionID(e.store.Meta().SessionID)); cacheKey != "" {
			req.PromptCacheKey = cacheKey
			req.PromptCacheScope = transcript.CacheWarningScopeReviewer
		}
	}
	if err := req.Validate(); err != nil {
		return llm.Request{}, err
	}
	return req, nil
}
