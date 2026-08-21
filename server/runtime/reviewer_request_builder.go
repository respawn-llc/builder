package runtime

import (
	"context"
	"fmt"

	"core/server/llm"
	"core/shared/jsoncontract"
	"core/shared/transcript"
)

type reviewerSuggestionsPayload struct {
	Suggestions []string `json:"suggestions"`
}

func prepareReviewerSuggestionsContract(
	preparer jsoncontract.Preparer,
) (jsoncontract.Structured, error) {
	return preparer.Structured("reviewer suggestions", reviewerSuggestionsPayload{})
}

func reviewerSuggestionsStructuredOutput(contract jsoncontract.Structured) *llm.StructuredOutput {
	return &llm.StructuredOutput{
		Name:   "reviewer_suggestions",
		Schema: contract,
	}
}

func (e *Engine) buildReviewerRequest(ctx context.Context, reviewerClient llm.Client) (llm.Request, error) {
	reviewerCfg := e.reviewerRequestConfigSnapshot()
	reviewerItems, err := buildReviewerRequestItemsWithBuilder(e.transcriptRuntimeState().SnapshotItems(), newActiveMetaContextBuilder(e.store.Meta(), e.transcriptWorkingDir(), e.cfg.Model, e.ThinkingLevel(), e.cfg.GlobalConfigDir, e.cfg.SkillPolicy, e.reviewerMetaTimestamp()), e.cfg.HeadlessMode)
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
		Items:                   reviewerItems,
		Tools:                   []llm.Tool{},
		ToolChoiceMode:          llm.ToolChoiceModeAutomatic,
		StructuredOutput:        reviewerSuggestionsStructuredOutput(e.reviewerSuggestionsContract),
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

func (e *Engine) buildReviewerDispatchRequest(
	ctx context.Context,
	stepID string,
	reviewerClient llm.Client,
) (llm.Request, error) {
	req, err := e.buildReviewerRequest(ctx, reviewerClient)
	if err != nil {
		return llm.Request{}, err
	}
	runID := activeRunIDForStep(e, stepID)
	if runID == "" {
		return llm.Request{}, fmt.Errorf(
			"%w: enclosing Agent Turn Run identity is required for Reviewer dispatch",
			llm.ErrInvalidRequest,
		)
	}
	factory, err := newDispatchRequestFactory(dispatchRequestIdentity{
		SessionID:            reviewerSessionID(e.store.Meta().SessionID),
		RunID:                runID,
		CompactionGeneration: e.compactionRuntimeState().Count(),
		RequestKind:          llm.CodexRequestKindTurn.Optional(),
	})
	if err != nil {
		return llm.Request{}, err
	}
	return factory.generation(req)
}
