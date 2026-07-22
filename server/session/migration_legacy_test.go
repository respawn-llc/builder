package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/shared/rollbacktarget"
)

func TestMaterializeEventLogMigratesLegacySessionOnce(t *testing.T) {
	store := newSessionTestStore(t)
	writeSessionFixtureEvents(t, store.Dir(), []legacyTestEvent{{
		Seq:       1,
		Timestamp: time.Now().UTC(),
		Kind:      string(EventKindMessage),
		StepID:    "step-1",
		Payload: mustFixtureJSON(t, map[string]any{
			"role":    string(MessageRoleUser),
			"content": "hello from legacy",
		}),
	}})

	eventLog := mustMaterializeSessionTestEventLog(t, store)
	window, err := eventLog.ReadSegmentForward(0, nil)
	if err != nil {
		t.Fatalf("read migrated event log: %v", err)
	}
	if len(window.Records) != 1 {
		t.Fatalf("migrated records = %d, want 1", len(window.Records))
	}
	payload, err := window.Records[0].Payload()
	if err != nil {
		t.Fatalf("read migrated payload: %v", err)
	}
	message, ok := payload.(MessageRecord)
	if !ok || message.Content == nil || *message.Content != "hello from legacy" {
		t.Fatalf("migrated message = %#v", payload)
	}

	eventsPath := filepath.Join(store.Dir(), eventsFile)
	firstMigrationInfo, err := os.Stat(eventsPath)
	if err != nil {
		t.Fatalf("stat migrated event log: %v", err)
	}
	migrated, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read migrated event log: %v", err)
	}
	headerEnd := bytes.IndexByte(migrated, '\n')
	if headerEnd < 0 {
		t.Fatal("migrated event log has no header line")
	}
	if _, err := decodeEventLogHeader(migrated[:headerEnd]); err != nil {
		t.Fatalf("decode migrated header: %v", err)
	}

	reopened := mustOpenSessionTestStore(t, store)
	if _, err := reopened.MaterializeEventLog(); err != nil {
		t.Fatalf("rematerialize migrated event log: %v", err)
	}
	secondMigrationInfo, err := os.Stat(eventsPath)
	if err != nil {
		t.Fatalf("stat rematerialized event log: %v", err)
	}
	if !os.SameFile(firstMigrationInfo, secondMigrationInfo) {
		t.Fatal("current v1 event log was replaced during reopen")
	}
	afterReopen, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read rematerialized event log: %v", err)
	}
	if !bytes.Equal(afterReopen, migrated) {
		t.Fatal("current v1 event log changed during reopen")
	}
}

func TestMaterializeEventLogMigratesLegacySessionAfterLeadingBlankLine(t *testing.T) {
	store := newSessionTestStore(t)
	writeSessionFixtureEvents(t, store.Dir(), []legacyTestEvent{{
		Seq:       1,
		Timestamp: time.Now().UTC(),
		Kind:      string(EventKindMessage),
		StepID:    "step-1",
		Payload: mustFixtureJSON(t, map[string]any{
			"role":    string(MessageRoleUser),
			"content": "preserved after leading blank line",
		}),
	}})

	eventsPath := filepath.Join(store.Dir(), eventsFile)
	legacy, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read legacy event log: %v", err)
	}
	if err := os.WriteFile(eventsPath, append([]byte("\n"), legacy...), 0o600); err != nil {
		t.Fatalf("prepend blank line to legacy event log: %v", err)
	}

	eventLog := mustMaterializeSessionTestEventLog(t, store)
	window, err := eventLog.ReadSegmentForward(0, nil)
	if err != nil {
		t.Fatalf("read migrated event log: %v", err)
	}
	if len(window.Records) != 1 {
		t.Fatalf("migrated records = %d, want 1", len(window.Records))
	}
	message, ok := mustEventRecordPayload(window.Records[0]).(MessageRecord)
	if !ok || message.Content == nil || *message.Content != "preserved after leading blank line" {
		t.Fatalf("migrated message = %#v", mustEventRecordPayload(window.Records[0]))
	}
}

func TestMaterializeEventLogMigratesLegacyToolCompletionWithoutSnapshot(t *testing.T) {
	store := newSessionTestStore(t)
	now := time.Now().UTC()
	writeSessionFixtureEvents(t, store.Dir(), []legacyTestEvent{
		{
			Seq:       1,
			Timestamp: now,
			Kind:      string(EventKindMessage),
			StepID:    "step-1",
			Payload: mustFixtureJSON(t, map[string]any{
				"role": string(MessageRoleAssistant),
				"tool_calls": []map[string]any{{
					"id":    "call-1",
					"name":  "exec",
					"input": map[string]any{"command": "echo done"},
				}},
			}),
		},
		{
			Seq:       2,
			Timestamp: now.Add(time.Millisecond),
			Kind:      string(EventKindToolCompletion),
			StepID:    "step-1",
			Payload: mustFixtureJSON(t, map[string]any{
				"call_id":  "call-1",
				"name":     "exec",
				"is_error": false,
				"output":   "done",
			}),
		},
	})

	eventLog := mustMaterializeSessionTestEventLog(t, store)
	window, err := eventLog.ReadSegmentForward(0, nil)
	if err != nil {
		t.Fatalf("read migrated event log: %v", err)
	}
	if len(window.Records) != 2 {
		t.Fatalf("migrated records = %d, want 2", len(window.Records))
	}
	payload, err := window.Records[1].Payload()
	if err != nil {
		t.Fatalf("read migrated tool completion: %v", err)
	}
	completion, ok := payload.(ToolCompletionRecord)
	if !ok {
		t.Fatalf("migrated tool completion = %#v", payload)
	}
	if completion.OutputKind != ToolOutputKindFunction ||
		len(completion.ProviderItems) != 1 ||
		completion.ProviderItems[0].Type != ProviderInputItemTypeFunctionCallOutput {
		t.Fatalf("migrated tool completion = %#v", completion)
	}
	var providerOutput struct {
		Type   string          `json:"type"`
		CallID string          `json:"call_id"`
		Output json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(completion.ProviderItems[0].Raw, &providerOutput); err != nil {
		t.Fatalf("decode migrated provider output: %v", err)
	}
	if providerOutput.Type != string(ProviderInputItemTypeFunctionCallOutput) ||
		providerOutput.CallID != "call-1" ||
		string(providerOutput.Output) != `"done"` {
		t.Fatalf("migrated provider output = %#v", providerOutput)
	}
}

func TestMaterializeEventLogMigratesLegacyNoticeAndCacheRecords(t *testing.T) {
	store := newSessionTestStore(t)
	now := time.Now().UTC()
	writeSessionFixtureEvents(t, store.Dir(), []legacyTestEvent{
		{
			Seq:       1,
			Timestamp: now,
			Kind:      string(EventKindLocalEntry),
			Payload: mustFixtureJSON(t, map[string]any{
				"visibility": "all",
				"role":       "system",
				"text":       "legacy notice",
			}),
		},
		{
			Seq:       2,
			Timestamp: now.Add(time.Millisecond),
			Kind:      string(EventKindCacheRequest),
			Payload: mustFixtureJSON(t, map[string]any{
				"cache_key":     "cache-1",
				"chunk_count":   2,
				"terminal_hash": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			}),
		},
		{
			Seq:       3,
			Timestamp: now.Add(2 * time.Millisecond),
			Kind:      string(EventKindCacheResponse),
			Payload: mustFixtureJSON(t, map[string]any{
				"cache_key":               "cache-1",
				"chunk_count":             2,
				"terminal_hash":           "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"has_cached_input_tokens": true,
				"cached_input_tokens":     12,
			}),
		},
		{
			Seq:       4,
			Timestamp: now.Add(3 * time.Millisecond),
			Kind:      string(EventKindCacheWarning),
			Payload: mustFixtureJSON(t, map[string]any{
				"reason": string(CacheWarningReasonNonPostfix),
			}),
		},
	})

	eventLog := mustMaterializeSessionTestEventLog(t, store)
	window, err := eventLog.ReadSegmentForward(0, nil)
	if err != nil {
		t.Fatalf("read migrated event log: %v", err)
	}
	if len(window.Records) != 4 {
		t.Fatalf("migrated records = %d, want 4", len(window.Records))
	}
	local, ok := mustEventRecordPayload(window.Records[0]).(LocalEntryRecord)
	if !ok || local.Visibility != EntryVisibilityOngoing || local.Text != "legacy notice" {
		t.Fatalf("migrated local entry = %#v", mustEventRecordPayload(window.Records[0]))
	}
	request, ok := mustEventRecordPayload(window.Records[1]).(CacheRequestObservationRecord)
	if !ok || request.DigestVersion != CacheDigestV1 || request.Scope != CacheScopeConversation {
		t.Fatalf("migrated cache request = %#v", mustEventRecordPayload(window.Records[1]))
	}
	response, ok := mustEventRecordPayload(window.Records[2]).(CacheResponseObservationRecord)
	if !ok || response.CachedInputTokens == nil || *response.CachedInputTokens != 12 {
		t.Fatalf("migrated cache response = %#v", mustEventRecordPayload(window.Records[2]))
	}
	warning, ok := mustEventRecordPayload(window.Records[3]).(CacheWarningRecord)
	if !ok || warning.Scope != CacheScopeConversation || warning.LostInputTokens != nil {
		t.Fatalf("migrated cache warning = %#v", mustEventRecordPayload(window.Records[3]))
	}
}

func TestMaterializeEventLogMigratesLegacyCompactionAndCustomToolContinuation(t *testing.T) {
	store := newSessionTestStore(t)
	now := time.Now().UTC()
	writeSessionFixtureEvents(t, store.Dir(), []legacyTestEvent{
		{
			Seq:       1,
			Timestamp: now,
			Kind:      string(EventKindMessage),
			Payload: mustFixtureJSON(t, map[string]any{
				"role":    string(MessageRoleUser),
				"content": "before compaction",
			}),
		},
		{
			Seq:       2,
			Timestamp: now.Add(time.Millisecond),
			Kind:      string(EventKindHistoryReplace),
			Payload: mustFixtureJSON(t, map[string]any{
				"engine":            "local",
				"mode":              string(CompactionModeAuto),
				"compaction_number": 1,
				"latest_rollback_candidate": rollbacktarget.CandidateLocator{
					UserMessageSeq:       999,
					CandidatePageEndByte: 999,
				},
				"items": []map[string]any{{
					"type":    string(ProviderHistoryItemTypeCustomToolCall),
					"id":      "call-custom",
					"call_id": "call-custom",
					"name":    "custom_tool",
					"raw": json.RawMessage(
						`{"type":"custom_tool_call","id":"call-custom","call_id":"call-custom","name":"custom_tool","input":"hello"}`,
					),
				}},
			}),
		},
		{
			Seq:       3,
			Timestamp: now.Add(2 * time.Millisecond),
			Kind:      string(EventKindToolCompletion),
			Payload: mustFixtureJSON(t, map[string]any{
				"call_id":  "call-custom",
				"is_error": false,
				"output":   "custom result",
			}),
		},
	})

	eventLog := mustMaterializeSessionTestEventLog(t, store)
	window, err := eventLog.ReadSegmentForward(0, nil)
	if err != nil {
		t.Fatalf("read migrated event log: %v", err)
	}
	if len(window.Records) != 3 {
		t.Fatalf("migrated records = %d, want 3", len(window.Records))
	}
	history, ok := mustEventRecordPayload(window.Records[1]).(HistoryReplacementRecord)
	if !ok || history.LatestRollbackCandidate == nil {
		t.Fatalf("migrated history = %#v", mustEventRecordPayload(window.Records[1]))
	}
	if history.LatestRollbackCandidate.UserMessageSeq != 1 ||
		history.LatestRollbackCandidate.CandidatePageEndByte <= 0 {
		t.Fatalf("migrated rollback candidate = %#v", history.LatestRollbackCandidate)
	}
	completion, ok := mustEventRecordPayload(window.Records[2]).(ToolCompletionRecord)
	if !ok ||
		completion.Name != "custom_tool" ||
		completion.OutputKind != ToolOutputKindCustom ||
		len(completion.ProviderItems) != 1 ||
		completion.ProviderItems[0].Type != ProviderInputItemTypeCustomToolOutput {
		t.Fatalf("migrated custom completion = %#v", mustEventRecordPayload(window.Records[2]))
	}
}

func TestMaterializeEventLogPreservesLegacyProviderSnapshotRaw(t *testing.T) {
	store := newSessionTestStore(t)
	raw := json.RawMessage(
		`{"type":"function_call_output","call_id":"call-1","output":"snapshot result"}`,
	)
	writeSessionFixtureEvents(t, store.Dir(), []legacyTestEvent{{
		Seq:       1,
		Timestamp: time.Now().UTC(),
		Kind:      string(EventKindToolCompletion),
		Payload: mustFixtureJSON(t, map[string]any{
			"call_id":  "call-1",
			"name":     "exec",
			"is_error": false,
			"output":   "snapshot result",
			"provider_items": []map[string]any{{
				"type":    string(ProviderHistoryItemTypeFunctionCallOutput),
				"name":    "exec",
				"call_id": "call-1",
				"output":  "snapshot result",
				"raw":     raw,
			}},
		}),
	}})

	eventLog := mustMaterializeSessionTestEventLog(t, store)
	window, err := eventLog.ReadSegmentForward(0, nil)
	if err != nil {
		t.Fatalf("read migrated event log: %v", err)
	}
	completion, ok := mustEventRecordPayload(window.Records[0]).(ToolCompletionRecord)
	if !ok || len(completion.ProviderItems) != 1 {
		t.Fatalf("migrated tool completion = %#v", mustEventRecordPayload(window.Records[0]))
	}
	if !bytes.Equal(completion.ProviderItems[0].Raw, raw) {
		t.Fatalf(
			"migrated provider Raw = %s, want %s",
			completion.ProviderItems[0].Raw,
			raw,
		)
	}
}

func TestMaterializeEventLogLeavesMalformedLegacySourceUntouched(t *testing.T) {
	store := newSessionTestStore(t)
	eventsPath := filepath.Join(store.Dir(), eventsFile)
	valid := mustFixtureJSON(t, legacyTestEvent{
		Seq:       1,
		Timestamp: time.Now().UTC(),
		Kind:      string(EventKindMessage),
		Payload: mustFixtureJSON(t, map[string]any{
			"role":    string(MessageRoleUser),
			"content": "preserve me",
		}),
	})
	source := append(append([]byte(nil), valid...), '\n')
	source = append(source, []byte("not-json\n")...)
	if err := os.WriteFile(eventsPath, source, 0o600); err != nil {
		t.Fatalf("write malformed legacy event log: %v", err)
	}

	if _, err := store.MaterializeEventLog(); err == nil {
		t.Fatal("malformed legacy event log materialized")
	} else {
		var materializationErr *EventLogMaterializationError
		if !errors.As(err, &materializationErr) || materializationErr.Committed {
			t.Fatalf("materialization error = %v, want uncommitted typed error", err)
		}
	}
	after, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read preserved legacy event log: %v", err)
	}
	if !bytes.Equal(after, source) {
		t.Fatal("malformed legacy source changed before migration commit")
	}
}

func TestMaterializeEventLogDropsTornLegacyTail(t *testing.T) {
	store := newSessionTestStore(t)
	eventsPath := filepath.Join(store.Dir(), eventsFile)
	valid := mustFixtureJSON(t, legacyTestEvent{
		Seq:       1,
		Timestamp: time.Now().UTC(),
		Kind:      string(EventKindMessage),
		Payload: mustFixtureJSON(t, map[string]any{
			"role":    string(MessageRoleUser),
			"content": "complete",
		}),
	})
	source := append(append([]byte(nil), valid...), '\n')
	source = append(source, []byte(`{"seq":2,"timestamp":"2026-07-22T00:00:00Z"`)...)
	if err := os.WriteFile(eventsPath, source, 0o600); err != nil {
		t.Fatalf("write torn legacy event log: %v", err)
	}

	eventLog := mustMaterializeSessionTestEventLog(t, store)
	window, err := eventLog.ReadSegmentForward(0, nil)
	if err != nil {
		t.Fatalf("read migrated event log: %v", err)
	}
	if len(window.Records) != 1 {
		t.Fatalf("migrated records = %d, want only complete record", len(window.Records))
	}
}
