package runtime

import (
	"context"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/textutil"
)

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
	if err := steerTestActiveStep(engine, "input", steerMessagesWithPersistenceIntent(steeringMessageEventNone,
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
