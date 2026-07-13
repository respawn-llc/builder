package runtime

import (
	"testing"

	"core/server/llm"
)

func TestBuildTokenCountRequestForItemsUsesAutomaticToolChoice(t *testing.T) {
	req, ok := buildTokenCountRequestForItems("gpt-5", "instructions", []llm.ResponseItem{{
		Type:    llm.ResponseItemTypeMessage,
		Role:    llm.RoleUser,
		Content: "hello",
	}})
	if !ok {
		t.Fatal("expected standalone token-count request")
	}
	if req.ToolChoiceMode != llm.ToolChoiceModeAutomatic {
		t.Fatalf("tool choice mode = %q, want automatic", req.ToolChoiceMode)
	}
	if llm.HasEffectiveAdvertisedTools(req.Tools, req.EnableNativeWebSearch) {
		t.Fatalf("standalone token-count request advertised tools: %+v", req)
	}
}
