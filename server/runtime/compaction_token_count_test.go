package runtime

import (
	"context"
	"encoding/json"
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

func TestCriticalExactCountRefreshesAfterCommittedToolCompletion(t *testing.T) {
	store := mustCreateTestSession(t)
	call := llm.ToolCall{
		ID:    "tool-call",
		Name:  string(toolspec.ToolExecCommand),
		Input: json.RawMessage(`{"cmd":"pwd"}`),
	}
	client := &fakeCompactionClient{
		inputTokenCountFn: func(request llm.Request) int {
			for _, item := range request.Items {
				if item.Type == llm.ResponseItemTypeFunctionCallOutput &&
					item.CallID != nil &&
					*item.CallID == call.ID {
					return 200
				}
			}
			return 100
		},
	}
	engine := mustNewTestEngine(
		t,
		store,
		client,
		tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolExecCommand,
			Handler: fakeTool{name: toolspec.ToolExecCommand},
		}),
		Config{Model: "gpt-5", ContextWindowTokens: 400_000},
	)
	if err := engine.steer("step", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call}}},
	)); err != nil {
		t.Fatalf("persist assistant tool call: %v", err)
	}

	if precise, ok := engine.currentInputTokensPrecisely(context.Background()); !ok || precise != 100 {
		t.Fatalf("initial exact count = (%d, %t), want (100, true)", precise, ok)
	}
	if client.countInputTokenCalls != 1 {
		t.Fatalf("initial exact count calls = %d, want one", client.countInputTokenCalls)
	}
	if results, err := engine.executeToolCalls(context.Background(), "step", []llm.ToolCall{call}); err != nil {
		t.Fatalf("execute tool call: %v", err)
	} else if len(results) != 1 {
		t.Fatalf("tool results = %+v, want one", results)
	}

	request, err := engine.buildRequest(context.Background(), "", true)
	if err != nil {
		t.Fatalf("build request after tool completion: %v", err)
	}
	outputPresent := false
	for _, item := range request.Items {
		if item.Type == llm.ResponseItemTypeFunctionCallOutput &&
			item.CallID != nil &&
			*item.CallID == call.ID {
			outputPresent = true
			break
		}
	}
	if !outputPresent {
		t.Fatalf("request omitted committed tool output: %+v", request.Items)
	}

	if precise, ok := engine.currentInputTokensPreciselyIfDueWithPriority(context.Background(), 1_000, true); !ok || precise != 200 {
		t.Fatalf("critical exact count = (%d, %t), want (200, true)", precise, ok)
	}
	if client.countInputTokenCalls != 2 {
		t.Fatalf("exact count calls = %d, want two", client.countInputTokenCalls)
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

func TestShouldAutoCompactMemoizesPreciseCountForUnchangedRequest(t *testing.T) {
	client := &preciseCompactionClient{inputTokenCount: 96_000}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(), Config{
		Model:                 "gpt-5",
		ContextWindowTokens:   400_000,
		AutoCompactTokenLimit: 100_000,
	})
	engine.setLastUsage(llm.Usage{InputTokens: 95_000, WindowTokens: 400_000})

	first := engine.shouldAutoCompactWithContext(context.Background())
	second := engine.shouldAutoCompactWithContext(context.Background())
	if first || second || client.countCalls != 1 {
		t.Fatalf(
			"unchanged auto-compaction decisions/count-calls = %t/%t/%d, want false/false/one",
			first,
			second,
			client.countCalls,
		)
	}
}
