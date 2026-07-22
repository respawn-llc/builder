package runtime

import (
	"context"
	"strings"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
)

func TestBuildTokenCountRequestForItemsUsesAutomaticToolChoice(t *testing.T) {
	req, ok := buildTokenCountRequestForItems("gpt-5", "instructions", []llm.ResponseItem{{
		Type:    llm.ResponseItemTypeMessage,
		Role:    textutil.Value(llm.RoleUser),
		Content: textutil.Value("hello"),
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

func TestShouldAutoCompactAccountsForMessagesAppendedAfterLastUsage(t *testing.T) {
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(), Config{
		Model:                 "gpt-5",
		ContextWindowTokens:   2_000,
		AutoCompactTokenLimit: 300,
	})
	engine.setLastUsage(llm.Usage{InputTokens: 120, WindowTokens: 2_000})
	if err := engine.steer("active-tail", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value(strings.Repeat("tail ", 320))}},
	)); err != nil {
		t.Fatalf("persist active-tail message: %v", err)
	}
	if usage := engine.ContextUsage(); usage.UsedTokens < 300 {
		t.Fatalf("active-tail usage = %+v, want at least compaction threshold", usage)
	}
	if !engine.shouldAutoCompactWithContext(context.Background()) {
		t.Fatal("active-tail growth after the usage checkpoint did not trigger auto compaction")
	}
}
