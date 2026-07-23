package runtime

import (
	"context"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/textutil"
)

func TestRemoteCompactionFailsNonOverflowProvider400WithoutReplacementOrFallback(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeCompactionClient{
		compactionErrors: []error{&llm.ProviderAPIError{
			ProviderID: "openai",
			StatusCode: 400,
			Code:       llm.UnifiedErrorCodeUnknown,
		}},
	}
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{
		Model:          "gpt-5",
		CompactionMode: "native",
	})
	if err := engine.steer("input", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
		t.Fatalf("persist compaction input: %v", err)
	}

	if err := engine.CompactContext(context.Background(), ""); err == nil {
		t.Fatal("compact context succeeded after non-overflow provider failure")
	}
	if len(client.compactionCalls) != 1 || len(client.calls) != 0 {
		t.Fatalf(
			"remote/local compaction calls = %d/%d, want one/zero",
			len(client.compactionCalls),
			len(client.calls),
		)
	}

	recent, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(4)
	if err != nil {
		t.Fatalf("read bounded compaction records: %v", err)
	}
	for _, record := range recent.Records {
		if _, ok := mustSessionEventPayload(record).(session.HistoryReplacementRecord); ok {
			t.Fatalf("non-overflow provider failure committed history replacement: %+v", record)
		}
	}
}
