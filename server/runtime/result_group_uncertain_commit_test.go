//go:build darwin || linux

package runtime

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestResultGroupShortWritePreservesCompleteRecordPrefix(t *testing.T) {
	const (
		stepID = "11111111-1111-4111-8111-111111111111"
		callID = "result-group-short-write"
	)
	encodedGroup := measureResultGroupAppendBytes(t, stepID, callID)
	firstTerminator := bytes.IndexByte(encodedGroup, '\n')
	if firstTerminator < 0 || firstTerminator+1 >= len(encodedGroup) {
		t.Fatalf("encoded Result Group has no multi-record boundary: %q", encodedGroup)
	}

	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		newTestToolRegistry(t),
		Config{Model: "gpt-5"},
	)
	prepareSimpleResultGroupCall(t, engine, stepID, callID)
	appendTornEventBytes(t, store, encodedGroup[:firstTerminator+2])
	if err := engine.Close(); err != nil {
		t.Fatalf("close runtime before reopen: %v", err)
	}

	reopened := mustOpenTestSession(t, store.Dir())
	records, err := collectTestEventRecords(reopened)
	if err != nil {
		t.Fatalf("collect repaired Result Group records: %v", err)
	}
	if got := countPersistedToolCompletions(records, callID); got != 1 {
		t.Fatalf("persisted Result Group completions = %d, want complete prefix once", got)
	}
}

func TestLaterSameStepTearPreservesDurableResultGroupAndClosesOnlyDanglingCall(t *testing.T) {
	const (
		stepID        = "22222222-2222-4222-8222-222222222222"
		completedCall = "durable-result"
		danglingCall  = "retained-dangling"
		discardedText = "malformed later suffix must disappear"
	)
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		newTestToolRegistry(t),
		Config{Model: "gpt-5"},
	)
	prepareSimpleResultGroupCall(t, engine, stepID, completedCall)
	collector := testResultGroupCollector(t, completedCall)
	if err := reportAndFlushSimpleResultGroup(
		engine,
		stepID,
		collector,
		completedCall,
	); err != nil {
		t.Fatalf("commit durable Result Group barrier: %v", err)
	}

	dangling := llm.ToolCall{
		ID:    danglingCall,
		Name:  string(toolspec.ToolExecCommand),
		Input: []byte(`{"cmd":"must-not-run"}`),
	}
	callRecord, err := sessionMessageRecordFromLLM(llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{dangling},
	})
	if err != nil {
		t.Fatalf("prepare retained dangling call: %v", err)
	}
	trailingRecord, err := sessionMessageRecordFromLLM(llm.Message{
		Role:    llm.RoleAssistant,
		Content: textutil.Value(discardedText),
	})
	if err != nil {
		t.Fatalf("prepare malformed trailing message: %v", err)
	}
	payloads := []session.EventRecordPayload{callRecord, trailingRecord}
	encodedLaterAppend := measureLaterAppendBytes(
		t,
		stepID,
		completedCall,
		payloads,
	)
	firstTerminator := bytes.IndexByte(encodedLaterAppend, '\n')
	if firstTerminator < 0 || firstTerminator+1 >= len(encodedLaterAppend) {
		t.Fatalf("encoded later append has no multi-record boundary: %q", encodedLaterAppend)
	}
	appendTornEventBytes(t, store, encodedLaterAppend[:firstTerminator+2])
	if err := engine.Close(); err != nil {
		t.Fatalf("close failed runtime generation: %v", err)
	}

	reopenedStore := mustOpenTestSession(t, store.Dir())
	reopened := mustNewTestEngine(
		t,
		reopenedStore,
		&fakeClient{},
		newTestToolRegistry(t),
		Config{Model: "gpt-5"},
	)
	records, err := collectTestEventRecords(reopenedStore)
	if err != nil {
		t.Fatalf("collect reopened durable prefix: %v", err)
	}
	if got := countPersistedToolCompletions(records, completedCall); got != 1 {
		t.Fatalf("durable Result Group completions = %d, want exactly one", got)
	}
	if got := countPersistedToolCompletions(records, danglingCall); got != 1 {
		t.Fatalf("fresh-resource dangling closures = %d, want exactly one", got)
	}
	if _, live := reopened.transcriptRuntimeState().liveToolLedger().Lookup(danglingCall); live {
		t.Fatal("fresh reopen retained a live operation for the closed dangling call")
	}
	for _, event := range records {
		payload := mustSessionEventPayload(event.Record)
		message, ok := payload.(session.MessageRecord)
		if !ok || message.Content == nil {
			continue
		}
		if *message.Content == discardedText {
			t.Fatal("reopen retained the malformed trailing record")
		}
	}
}

func measureResultGroupAppendBytes(
	t *testing.T,
	stepID string,
	callID string,
) []byte {
	t.Helper()
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		newTestToolRegistry(t),
		Config{Model: "gpt-5"},
	)
	prepareSimpleResultGroupCall(t, engine, stepID, callID)
	before := eventLogSize(t, store)
	collector := testResultGroupCollector(t, callID)
	if err := reportAndFlushSimpleResultGroup(engine, stepID, collector, callID); err != nil {
		t.Fatalf("measure Result Group append: %v", err)
	}
	return appendedEventBytes(t, store, before)
}

func measureLaterAppendBytes(
	t *testing.T,
	stepID string,
	completedCall string,
	payloads []session.EventRecordPayload,
) []byte {
	t.Helper()
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		newTestToolRegistry(t),
		Config{Model: "gpt-5"},
	)
	prepareSimpleResultGroupCall(t, engine, stepID, completedCall)
	collector := testResultGroupCollector(t, completedCall)
	if err := reportAndFlushSimpleResultGroup(
		engine,
		stepID,
		collector,
		completedCall,
	); err != nil {
		t.Fatalf("measure durable Result Group: %v", err)
	}
	log := mustMaterializeTestEventLog(t, store)
	before := eventLogSize(t, store)
	if _, receipt, err := log.AppendRecordsAtomic(&stepID, payloads); err != nil {
		t.Fatalf("measure append records: %v", err)
	} else if !receipt.Committed {
		t.Fatal("measured append did not commit")
	}
	return appendedEventBytes(t, store, before)
}

func appendedEventBytes(t testing.TB, store *session.Store, before int64) []byte {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(store.Dir(), "events.jsonl"))
	if err != nil {
		t.Fatalf("read measured event log: %v", err)
	}
	if before < 0 || before >= int64(len(contents)) {
		t.Fatalf("measured event-log offset = %d for %d bytes", before, len(contents))
	}
	return append([]byte(nil), contents[before:]...)
}

func eventLogSize(t testing.TB, store *session.Store) int64 {
	t.Helper()
	info, err := os.Stat(filepath.Join(store.Dir(), "events.jsonl"))
	if err != nil {
		t.Fatalf("stat event log: %v", err)
	}
	return info.Size()
}

func appendTornEventBytes(t testing.TB, store *session.Store, encoded []byte) {
	t.Helper()
	fp, err := os.OpenFile(
		filepath.Join(store.Dir(), "events.jsonl"),
		os.O_APPEND|os.O_WRONLY,
		0,
	)
	if err != nil {
		t.Fatalf("open event log for torn write: %v", err)
	}
	if _, err := fp.Write(encoded); err != nil {
		_ = fp.Close()
		t.Fatalf("write torn event bytes: %v", err)
	}
	if err := fp.Sync(); err != nil {
		_ = fp.Close()
		t.Fatalf("sync torn event bytes: %v", err)
	}
	if err := fp.Close(); err != nil {
		t.Fatalf("close torn event bytes: %v", err)
	}
}

func countPersistedToolCompletions(events []testPersistedEvent, callID string) int {
	count := 0
	for _, event := range events {
		record, ok := mustSessionEventPayload(event.Record).(session.ToolCompletionRecord)
		if !ok {
			continue
		}
		if storedToolCompletionFromSessionRecord(record).CallID == callID {
			count++
		}
	}
	return count
}
