package runtime

import (
	"context"
	"strings"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
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

func TestShouldAutoCompactUsesPreciseRequestInputTokenCountWhenAvailable(t *testing.T) {
	client := &preciseCompactionClient{inputTokenCount: 960}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(), Config{
		Model:                 "gpt-5",
		ContextWindowTokens:   2_000,
		AutoCompactTokenLimit: 900,
	})
	if err := engine.steer("input", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
		t.Fatalf("persist input: %v", err)
	}
	if usage := engine.ContextUsage(); usage.UsedTokens >= 900 {
		t.Fatalf("estimated context usage = %+v, want below compaction threshold", usage)
	}
	if !engine.shouldAutoCompactWithContext(context.Background()) {
		t.Fatal("precise input token count did not trigger auto compaction")
	}
	if client.countCalls != 1 {
		t.Fatalf("precise token count calls = %d, want one", client.countCalls)
	}
}

func TestShouldCompactBeforeUserMessageUsesPromptGrowthBelowPreSubmitBand(t *testing.T) {
	sawPromptGrowthRequest := false
	client := &fakeCompactionClient{
		inputTokenCountFn: func(request llm.Request) int {
			ordinaryUsers := 0
			for _, item := range request.Items {
				if item.Type != llm.ResponseItemTypeMessage ||
					item.Role == nil ||
					*item.Role != llm.RoleUser ||
					item.MessageType != nil {
					continue
				}
				ordinaryUsers++
			}
			if ordinaryUsers >= 2 {
				sawPromptGrowthRequest = true
				return 960
			}
			return 850
		},
	}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(), Config{
		Model:                         "gpt-5",
		ContextWindowTokens:           1_000,
		AutoCompactTokenLimit:         950,
		PreSubmitCompactionLeadTokens: 50,
	})
	if err := engine.steer("existing", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("existing")}},
	)); err != nil {
		t.Fatalf("persist existing input: %v", err)
	}

	shouldCompact, err := engine.ShouldCompactBeforeUserMessage(
		context.Background(),
		strings.Repeat("next ", 1_000),
	)
	if err != nil {
		t.Fatalf("evaluate pre-submit compaction: %v", err)
	}
	if !shouldCompact || !sawPromptGrowthRequest {
		t.Fatalf(
			"pre-submit prompt-growth decision=%t exact-prompt-request=%t, want both true",
			shouldCompact,
			sawPromptGrowthRequest,
		)
	}
}

func TestShouldCompactBeforeUserMessageFallsBackWhenExactCountUnsupported(t *testing.T) {
	supported := false
	client := &preciseCompactionClient{
		inputTokenCount: 960,
		countSupported:  &supported,
	}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(), Config{
		Model:                         "gpt-5",
		ContextWindowTokens:           1_000,
		AutoCompactTokenLimit:         950,
		PreSubmitCompactionLeadTokens: 50,
	})
	if err := engine.steer("existing", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value(strings.Repeat("existing ", 300))}},
	)); err != nil {
		t.Fatalf("persist existing input: %v", err)
	}

	shouldCompact, err := engine.ShouldCompactBeforeUserMessage(
		context.Background(),
		strings.Repeat("next ", 1_000),
	)
	if err != nil {
		t.Fatalf("evaluate fallback pre-submit compaction: %v", err)
	}
	if !shouldCompact || client.countCalls != 0 {
		t.Fatalf(
			"fallback pre-submit decision=%t exact-count-calls=%d, want true and zero",
			shouldCompact,
			client.countCalls,
		)
	}
}

func TestShouldAutoCompactRechecksProviderBeforeCompactingOnLargeEstimate(t *testing.T) {
	client := &preciseCompactionClient{inputTokenCount: 1}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(), Config{
		Model:                 "gpt-5",
		ContextWindowTokens:   2_000,
		AutoCompactTokenLimit: 2,
	})
	if err := engine.steer("image", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{
			Role:       llm.RoleTool,
			ToolCallID: textutil.Value("image-call"),
			Name:       textutil.Value(string(toolspec.ToolViewImage)),
			Content: textutil.Value(
				`[{"type":"input_image","image_url":"data:image/png;base64,` +
					strings.Repeat("A", 24_000) +
					`"}]`,
			),
		}},
	)); err != nil {
		t.Fatalf("persist multimodal tool result: %v", err)
	}
	if usage := engine.ContextUsage(); usage.UsedTokens < 2 {
		t.Fatalf("large local estimate = %+v, want at least compaction threshold", usage)
	}
	shouldCompact := engine.shouldAutoCompactWithContext(context.Background())
	if shouldCompact || client.countCalls != 1 {
		t.Fatalf(
			"provider recheck compact=%t count-calls=%d, want false and one",
			shouldCompact,
			client.countCalls,
		)
	}
}

func TestShouldAutoCompactPrefersConfiguredThresholdOverResolvedContextWindow(t *testing.T) {
	client := &preciseCompactionClient{inputTokenCount: 950, contextWindow: 1_000}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(), Config{
		Model:                 "gpt-5",
		ContextWindowTokens:   400_000,
		AutoCompactTokenLimit: 360_000,
	})
	if err := engine.steer("input", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
		t.Fatalf("persist input: %v", err)
	}
	if usage := engine.ContextUsage(); usage.WindowTokens != 400_000 {
		t.Fatalf("configured context window = %d, want 400000", usage.WindowTokens)
	}
	shouldCompact := engine.shouldAutoCompactWithContext(context.Background())
	if shouldCompact || client.resolveCalls != 0 {
		t.Fatalf(
			"configured auto-compaction decision/resolution calls = %t/%d, want false/zero",
			shouldCompact,
			client.resolveCalls,
		)
	}
}

func TestShouldAutoCompactAccountsForReservedOutputBudget(t *testing.T) {
	client := &preciseCompactionClient{inputTokenCount: 850}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(), Config{
		Model:                 "gpt-5",
		ContextWindowTokens:   2_000,
		AutoCompactTokenLimit: 900,
		MaxTokens:             100,
	})
	if err := engine.steer("input", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
		t.Fatalf("persist input: %v", err)
	}
	shouldCompact := engine.shouldAutoCompactWithContext(context.Background())
	if !shouldCompact || client.countCalls != 1 {
		t.Fatalf(
			"reserved-output auto-compaction decision/count-calls = %t/%d, want true/one",
			shouldCompact,
			client.countCalls,
		)
	}
}

func TestShouldAutoCompactSkipsPreciseCountWhenFarBelowThreshold(t *testing.T) {
	client := &preciseCompactionClient{inputTokenCount: 999_999}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(), Config{
		Model:                 "gpt-5",
		ContextWindowTokens:   400_000,
		AutoCompactTokenLimit: 100_000,
	})
	if err := engine.steer("input", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
		t.Fatalf("persist input: %v", err)
	}
	shouldCompact := engine.shouldAutoCompactWithContext(context.Background())
	if shouldCompact || client.countCalls != 0 {
		t.Fatalf(
			"far-below auto-compaction decision/count-calls = %t/%d, want false/zero",
			shouldCompact,
			client.countCalls,
		)
	}
}
