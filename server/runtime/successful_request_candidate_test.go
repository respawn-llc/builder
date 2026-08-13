package runtime

import (
	"testing"

	"core/server/llm"
	"core/shared/textutil"
)

func TestSuccessfulRequestCandidateAdjustsUsageBaseline(t *testing.T) {
	prior := llm.ResponseItem{
		Type:             llm.ResponseItemTypeReasoning,
		ID:               textutil.Value("prior"),
		EncryptedContent: textutil.Value("encrypted-prior-reasoning"),
	}
	current := llm.ResponseItem{
		Type:             llm.ResponseItemTypeReasoning,
		ID:               textutil.Value("current"),
		EncryptedContent: textutil.Value("encrypted-current-reasoning"),
	}
	request := llm.Request{
		Model: "requested-model",
		Items: []llm.ResponseItem{
			prior,
			{Type: llm.ResponseItemTypeMessage, Role: textutil.Value(llm.RoleUser), Content: textutil.Value("instruction")},
			current,
		},
	}
	fullEstimate := estimateItemsTokens(request.Items)
	priorEstimate := estimateItemsTokens([]llm.ResponseItem{prior})

	included := newSuccessfulRequestCandidate(request, llm.Response{ReasoningIncluded: true})
	if included.requestedModel != "requested-model" {
		t.Fatalf("requested model = %q, want requested-model", included.requestedModel)
	}
	if included.estimatedProviderTokens != fullEstimate {
		t.Fatalf("included baseline = %d, want full estimate %d", included.estimatedProviderTokens, fullEstimate)
	}

	omitted := newSuccessfulRequestCandidate(request, llm.Response{})
	if omitted.estimatedProviderTokens != fullEstimate-priorEstimate {
		t.Fatalf("omitted baseline = %d, want %d", omitted.estimatedProviderTokens, fullEstimate-priorEstimate)
	}
}

func TestSuccessfulRequestCandidateBaselineClampsAtZero(t *testing.T) {
	request := llm.Request{
		Items: []llm.ResponseItem{
			{
				Type:             llm.ResponseItemTypeReasoning,
				ID:               textutil.Value("reasoning"),
				EncryptedContent: textutil.Value("encrypted"),
			},
			{Type: llm.ResponseItemTypeMessage, Role: textutil.Value(llm.RoleUser), Content: textutil.Value("instruction")},
		},
	}
	candidate := newSuccessfulRequestCandidate(request, llm.Response{})
	if candidate.estimatedProviderTokens < 0 {
		t.Fatalf("baseline = %d, want nonnegative", candidate.estimatedProviderTokens)
	}
}
