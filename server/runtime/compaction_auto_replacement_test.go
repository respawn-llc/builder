package runtime

import (
	"context"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/shared/textutil"
)

func TestAutoCompactionRecomputesUsageFromReplacementHistory(t *testing.T) {
	t.Parallel()
	const autoCompactLimit = 190_000

	client := &fakeCompactionClient{compactionResponses: []llm.CompactionResponse{
		remoteCompactionReplacement(autoCompactLimit, 1_000, 200_000),
	}}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), Config{
		Model:                 "gpt-5",
		ContextWindowTokens:   200_000,
		AutoCompactTokenLimit: autoCompactLimit,
	})
	if _, err := engine.SetGoal("goal", session.GoalActorUser); err != nil {
		t.Fatalf("set active goal: %v", err)
	}
	if err := engine.steer("input", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
		t.Fatalf("persist compaction input: %v", err)
	}
	engine.setLastUsage(llm.Usage{InputTokens: autoCompactLimit, WindowTokens: 200_000})

	err := withActiveTestRun(t, engine, ActiveKindUserTurn, func(ctx context.Context, stepID string) error {
		return engine.autoCompactIfNeeded(ctx, stepID, compactionModeAuto)
	})
	if err != nil {
		t.Fatalf("auto compact: %v", err)
	}
	usage := engine.ContextUsage()
	activeGoalContinuations := 0
	for _, item := range engine.transcriptRuntimeState().SnapshotItems() {
		if item.Type == llm.ResponseItemTypeMessage &&
			item.MessageType != nil &&
			*item.MessageType == llm.MessageTypeActiveGoalContinuation {
			activeGoalContinuations++
		}
	}
	shouldCompactAgain := engine.shouldAutoCompactWithContext(context.Background())
	if usage.UsedTokens >= autoCompactLimit ||
		shouldCompactAgain ||
		activeGoalContinuations != 1 ||
		len(client.compactionCalls) != 1 {
		t.Fatalf(
			"post-auto-compaction usage=%+v repeats=%t active-goal-continuations=%d remote-calls=%d",
			usage,
			shouldCompactAgain,
			activeGoalContinuations,
			len(client.compactionCalls),
		)
	}
}

func remoteCompactionReplacement(
	inputTokens int,
	outputTokens int,
	windowTokens int,
) llm.CompactionResponse {
	return llm.CompactionResponse{
		OutputItems: []llm.ResponseItem{
			{
				Type:        llm.ResponseItemTypeMessage,
				Role:        textutil.Value(llm.RoleUser),
				MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
				Content:     textutil.Value("summary"),
			},
			{
				Type:             llm.ResponseItemTypeCompaction,
				ID:               textutil.Value("compaction-checkpoint"),
				EncryptedContent: textutil.Value("encrypted"),
			},
		},
		Usage: llm.Usage{
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			WindowTokens: windowTokens,
		},
	}
}
