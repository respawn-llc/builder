package runtime

import (
	"encoding/json"
	"testing"

	"core/server/llm"
	"core/shared/textutil"
)

func TestSuccessfulRequestCandidateAdjustsUsageBaseline(t *testing.T) {
	reasoning := func(id string) llm.ResponseItem {
		return llm.ResponseItem{Type: llm.ResponseItemTypeReasoning, ID: textutil.Value(id), EncryptedContent: textutil.Value("encrypted-" + id)}
	}
	prior := reasoning("prior")
	request := llm.Request{
		Model: "requested-model", Items: []llm.ResponseItem{
			prior,
			{Type: llm.ResponseItemTypeMessage, Role: textutil.Value(llm.RoleUser), Content: textutil.Value("instruction")},
			reasoning("current"),
			{Type: llm.ResponseItemTypeFunctionCall, CallID: textutil.Value("call-1"), Name: textutil.Value("exec_command"), Arguments: json.RawMessage(`{"cmd":"pwd"}`)},
			{Type: llm.ResponseItemTypeFunctionCallOutput, CallID: textutil.Value("call-1"), Output: json.RawMessage(`"done"`)},
		},
	}
	fullEstimate := estimateItemsTokens(request.Items)
	priorEstimate := estimateItemsTokens([]llm.ResponseItem{prior})
	omitted := newSuccessfulRequestCandidate(request, llm.Response{})
	if omitted.requestedModel != request.Model || omitted.estimatedProviderTokens != fullEstimate-priorEstimate {
		t.Fatalf("candidate = %+v, want requested model and adjusted baseline %d", omitted, fullEstimate-priorEstimate)
	}
}
