package runtime

import (
	"context"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/textutil"
)

func TestAutoCompactionRecomputesUsageFromReplacementHistory(t *testing.T) {
	t.Parallel()
	const autoCompactLimit = 190_000

	client := &fakeCompactionClient{compactionResponses: []llm.CompactionResponse{
		remoteCompactionReplacement(autoCompactLimit, 1_000, 200_000),
	}}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(), Config{
		Model:                 "gpt-5",
		ContextWindowTokens:   200_000,
		AutoCompactTokenLimit: autoCompactLimit,
	})
	if _, err := engine.SetGoal(t.Context(), "goal", session.GoalActorUser); err != nil {
		t.Fatalf("set active goal: %v", err)
	}
	if err := steerTestActiveStep(engine, "input", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}})); err != nil {
		t.Fatalf("persist compaction input: %v", err)
	}
	engine.setLastUsage(llm.Usage{InputTokens: autoCompactLimit, WindowTokens: 200_000})

	stepID := runtimeTestStepID("compact")
	if err := runTestActiveStep(engine, stepID, func() error {
		return engine.autoCompactIfNeeded(context.Background(), stepID, compactionModeAuto)
	}); err != nil {
		t.Fatalf("auto compact: %v", err)
	}
	usage := engine.ContextUsage()
	activeGoalContinuations := 0
	preservedUserMessages := 0
	for _, item := range engine.transcriptRuntimeState().SnapshotItems() {
		if item.Type == llm.ResponseItemTypeMessage &&
			item.MessageType != nil &&
			*item.MessageType == llm.MessageTypeActiveGoalContinuation {
			activeGoalContinuations++
		}
		if item.Type == llm.ResponseItemTypeMessage &&
			item.MessageType != nil &&
			*item.MessageType == llm.MessageTypeCompactionPreservedUserMessage {
			preservedUserMessages++
		}
	}
	shouldCompactAgain := engine.shouldAutoCompactWithContext(context.Background())
	if usage.UsedTokens >= autoCompactLimit ||
		shouldCompactAgain ||
		activeGoalContinuations != 1 ||
		preservedUserMessages != 1 ||
		len(client.compactionCalls) != 1 {
		t.Fatalf(
			"post-auto-compaction usage=%+v repeats=%t active-goal-continuations=%d preserved-user-messages=%d remote-calls=%d",
			usage,
			shouldCompactAgain,
			activeGoalContinuations,
			preservedUserMessages,
			len(client.compactionCalls),
		)
	}
	assertCompactionReplacementOrder(t, engine.transcriptRuntimeState().SnapshotItems(), false)
	request, err := engine.buildRequest(context.Background(), "", true)
	if err != nil {
		t.Fatalf("build request after automatic compaction: %v", err)
	}
	if got, want := request.PromptCacheKey, engine.SessionID(); got != want {
		t.Fatalf("automatic compaction prompt cache key = %q, want stable Session ID %q", got, want)
	}
}

func TestAutoCompactionLocalCarriesPreservedUserMessageInOrder(t *testing.T) {
	t.Parallel()
	client := &fakeClient{
		caps: llm.ProviderCapabilities{
			ProviderID:           "local",
			SupportsResponsesAPI: true,
		},
		responses: []llm.Response{{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("local summary"),
			},
			Usage: llm.Usage{InputTokens: 1_000, OutputTokens: 100, WindowTokens: 200_000},
		}},
	}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), Config{
		Model:                 "gpt-5",
		CompactionMode:        "local",
		ContextWindowTokens:   200_000,
		AutoCompactTokenLimit: 190_000,
	})
	if err := steerTestActiveStep(engine, "input", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("local automatic carryover")}},
	)); err != nil {
		t.Fatalf("persist compaction input: %v", err)
	}
	engine.setLastUsage(llm.Usage{InputTokens: 190_000, WindowTokens: 200_000})

	err := withActiveTestRun(t, engine, ActiveKindUserTurn, func(ctx context.Context, stepID string) error {
		return engine.autoCompactIfNeeded(ctx, stepID, compactionModeAuto)
	})
	if err != nil {
		t.Fatalf("auto compact locally: %v", err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("local Generate calls = %d, want one", len(client.calls))
	}
	assertCompactionReplacementOrder(t, engine.transcriptRuntimeState().SnapshotItems(), false)
}

func remoteCompactionReplacement(
	inputTokens int,
	outputTokens int,
	windowTokens int,
) llm.CompactionResponse {
	return llm.CompactionResponse{
		Checkpoint: llm.ResponseItem{
			Type:             llm.ResponseItemTypeCompaction,
			ID:               textutil.Value("compaction-checkpoint"),
			EncryptedContent: textutil.Value("encrypted"),
		},
		Usage: llm.Usage{
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			WindowTokens: windowTokens,
		},
	}
}
