package runtime

import (
	"context"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/textutil"
)

func TestCompactNowUsesRuntimeEventStartAndResultBoundaries(t *testing.T) {
	stepID := "11111111-1111-4111-8111-111111111111"
	contextResolutionStarted := make(chan struct{})
	releaseContextResolution := make(chan struct{})
	defer closeSignalOnce(releaseContextResolution)
	client := &blockingCompactionContextClient{
		scriptedGoalLoopClient:   newScriptedGoalLoopClient(),
		contextResolutionStarted: contextResolutionStarted,
		releaseContextResolution: releaseContextResolution,
	}
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		tools.NewRegistry(),
		Config{Model: "runtime-event-compaction", CompactionMode: "local"},
	)
	if err := engine.steer(
		stepID,
		steerMessagesWithPersistenceIntent(
			steeringPriorityNormal,
			steeringMessageEventNone,
			true,
			[]llm.Message{{
				Role:    llm.RoleUser,
				Content: textutil.Value("compact this context"),
			}},
		),
	); err != nil {
		t.Fatalf("seed compaction context: %v", err)
	}

	releaseStartAdmission := blockRuntimeEventAdmission(t, engine.runtimeEvents)
	startBlocked := true
	defer func() {
		if startBlocked {
			releaseStartAdmission()
		}
	}()

	type compactOutcome struct {
		receipt session.CommitReceipt
		err     error
	}
	done := make(chan compactOutcome, 1)
	go func() {
		_, receipt, err := engine.compactNow(
			context.Background(),
			stepID,
			compactionModeAuto,
			compactionInstructionsInput{},
			false,
		)
		done <- compactOutcome{receipt: receipt, err: err}
	}()
	select {
	case <-contextResolutionStarted:
		t.Fatal("compaction context construction started before Runtime Event admission")
	case <-time.After(50 * time.Millisecond):
	}
	client.assertNotStarted(t, 1)

	releaseStartAdmission()
	startBlocked = false
	select {
	case <-contextResolutionStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("compaction context construction did not start after Runtime Event admission")
	}
	closeSignalOnce(releaseContextResolution)
	client.waitStarted(t, 1)

	unrelatedApplied := make(chan struct{})
	if _, err := submitRuntimeEvent(
		engine,
		struct{}{},
		func(runtimeEventAdmission, struct{}) (struct{}, error) {
			close(unrelatedApplied)
			return struct{}{}, nil
		},
	); err != nil {
		t.Fatalf("apply unrelated Runtime Event while compaction is held: %v", err)
	}
	select {
	case <-unrelatedApplied:
	case <-time.After(3 * time.Second):
		t.Fatal("held compaction blocked unrelated Runtime Event admission")
	}

	releaseResultAdmission := blockRuntimeEventAdmission(t, engine.runtimeEvents)
	resultBlocked := true
	defer func() {
		if resultBlocked {
			releaseResultAdmission()
		}
	}()
	client.releaseCall(1)
	select {
	case outcome := <-done:
		t.Fatalf(
			"compaction settled before its terminal result event: receipt=%+v error=%v",
			outcome.receipt,
			outcome.err,
		)
	case <-time.After(50 * time.Millisecond):
	}

	releaseResultAdmission()
	resultBlocked = false
	select {
	case outcome := <-done:
		if outcome.err != nil || !outcome.receipt.Committed {
			t.Fatalf(
				"compact context: receipt=%+v error=%v",
				outcome.receipt,
				outcome.err,
			)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("compaction did not settle after its terminal result event")
	}
}

type blockingCompactionContextClient struct {
	*scriptedGoalLoopClient
	contextResolutionStarted chan struct{}
	releaseContextResolution chan struct{}
}

func (c *blockingCompactionContextClient) ResolveModelContextWindow(
	ctx context.Context,
	_ string,
) (int, error) {
	closeSignalOnce(c.contextResolutionStarted)
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-c.releaseContextResolution:
		return 200_000, nil
	}
}

func TestCompactionReplacementAtomicallyEmbedsReinjectedMetaAndPreservedUserMessage(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeCompactionClient{compactionResponses: []llm.CompactionResponse{
		remoteCompactionReplacement(1_000, 100, 200_000),
	}}
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	if _, err := engine.SetGoal("goal", session.GoalActorUser); err != nil {
		t.Fatalf("set active goal: %v", err)
	}
	mustSetWorktreeReminderState(t, store, testWorktreeReminderState(
		session.WorktreeReminderModeEnter,
		"feature/goal",
		t.TempDir(),
		t.TempDir(),
		t.TempDir(),
	))
	if err := engine.steer("input", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
		t.Fatalf("persist compaction input: %v", err)
	}

	if err := engine.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("compact context: %v", err)
	}

	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read bounded compaction records: %v", err)
	}
	var replacement session.HistoryReplacementRecord
	replacementIndex := -1
	for index, event := range window.Records {
		record, ok := mustSessionEventPayload(event).(session.HistoryReplacementRecord)
		if !ok {
			continue
		}
		if replacementIndex >= 0 {
			t.Fatalf("bounded compaction records contain multiple replacements: %+v", window.Records)
		}
		replacementIndex = index
		replacement = record
	}
	if replacementIndex < 0 {
		t.Fatalf("bounded compaction records contain no history replacement: %+v", window.Records)
	}

	messageTypes := make([]llm.MessageType, 0, len(replacement.Items))
	for _, item := range replacement.Items {
		if item.Type != session.ProviderHistoryItemTypeMessage || item.MessageType == nil {
			continue
		}
		messageTypes = append(messageTypes, llm.MessageType(*item.MessageType))
	}
	assertOrderedReplacementMessageTypes(t, messageTypes, []llm.MessageType{
		llm.MessageTypeCompactionSummary,
		llm.MessageTypeEnvironment,
		llm.MessageTypeActiveGoalContinuation,
		llm.MessageTypeWorktreeMode,
		llm.MessageTypeCompactionPreservedUserMessage,
	})

	for _, event := range window.Records[replacementIndex+1:] {
		message, ok := mustSessionEventPayload(event).(session.MessageRecord)
		if !ok || message.Role != session.MessageRoleDeveloper || message.MessageType == nil {
			continue
		}
		t.Fatalf("replacement followed by typed developer meta record: %+v", event)
	}
}

func assertOrderedReplacementMessageTypes(
	t *testing.T,
	messageTypes []llm.MessageType,
	want []llm.MessageType,
) {
	t.Helper()
	next := 0
	for _, messageType := range messageTypes {
		if next < len(want) && messageType == want[next] {
			next++
		}
	}
	if next != len(want) {
		t.Fatalf("replacement message types = %+v, want ordered subsequence %+v", messageTypes, want)
	}
	if len(messageTypes) == 0 || messageTypes[len(messageTypes)-1] != llm.MessageTypeCompactionPreservedUserMessage {
		t.Fatalf("replacement message types = %+v, want compaction-preserved user message last", messageTypes)
	}
}
