package runtime

import (
	"context"
	"strings"
	"testing"

	"core/server/llm"
	"core/shared/textutil"
)

func TestShouldAutoCompactAccountsForMessagesAppendedAfterLastUsage(t *testing.T) {
	t.Parallel()
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, newTestToolRegistry(t), Config{
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

func TestShouldCompactBeforeUserMessageUsesEstimatedPromptGrowth(t *testing.T) {
	t.Parallel()
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, newTestToolRegistry(t), Config{
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
	if !shouldCompact {
		t.Fatal("estimated pending prompt growth did not trigger pre-submit compaction")
	}
}

func TestShouldAutoCompactPrefersConfiguredThresholdOverResolvedContextWindow(t *testing.T) {
	t.Parallel()
	client := &contextWindowClient{contextWindow: 1_000}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), Config{
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
	if engine.shouldAutoCompactWithContext(context.Background()) || client.resolveCalls != 0 {
		t.Fatalf("configured threshold unexpectedly resolved model context window: calls=%d", client.resolveCalls)
	}
}

func TestShouldAutoCompactAccountsForReservedOutputBudget(t *testing.T) {
	t.Parallel()
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, newTestToolRegistry(t), Config{
		Model:                 "gpt-5",
		ContextWindowTokens:   2_000,
		AutoCompactTokenLimit: 900,
		MaxTokens:             100,
	})
	engine.setLastUsage(llm.Usage{InputTokens: 850, WindowTokens: 2_000})
	if !engine.shouldAutoCompactWithContext(context.Background()) {
		t.Fatal("reserved output budget did not trigger auto compaction")
	}
}

func TestShouldAutoCompactStaysFalseFarBelowThreshold(t *testing.T) {
	t.Parallel()
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, newTestToolRegistry(t), Config{
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
	if engine.shouldAutoCompactWithContext(context.Background()) {
		t.Fatal("small estimated context unexpectedly triggered auto compaction")
	}
}
