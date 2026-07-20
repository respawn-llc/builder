package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLegacyV0DecoderMapsMessageThroughV1Canonicalizer(t *testing.T) {
	line := []byte(`{
		"seq":7,
		"timestamp":"2026-07-19T10:00:00Z",
		"kind":"message",
		"step_id":" step-7 ",
		"payload":{
			"role":"assistant",
			"message_type":"background_notice",
			"source_path":"/tmp/source.go",
			"content":"message content",
			"compact_content":"compact",
			"phase":"commentary",
			"background_activity_id":"activity-1",
			"background_exit_code":17,
			"tool_calls":[{
				"id":" call-1 ",
				"name":" exec_command ",
				"presentation":{"ToolName":"exec_command"},
				"input":{"cmd":"pwd"}
			}],
			"reasoning_items":[{
				"id":" reasoning-1 ",
				"encrypted_content":" encrypted "
			}],
			"future_payload_fact":true
		},
		"future_envelope_fact":true
	}`)
	path := filepath.Join(t.TempDir(), "legacy.json")
	if err := os.WriteFile(path, line, 0o600); err != nil {
		t.Fatalf("write legacy fixture: %v", err)
	}
	source, err := openRegularSessionFile(path, "legacy fixture")
	if err != nil {
		t.Fatalf("open legacy fixture: %v", err)
	}
	defer source.Close()

	result, err := decodeLegacyEventV0(source, 0, int64(len(line)), newMigrationResourceLedger())
	if err != nil {
		t.Fatalf("decode legacy message: %v", err)
	}
	if result.Record == nil || result.Dropped || result.FallbackCompletion != nil {
		t.Fatalf("legacy message result = %#v", result)
	}
	if result.Record.Seq() != 7 ||
		result.Record.StepID() == nil ||
		*result.Record.StepID() != "step-7" {
		t.Fatalf("legacy message envelope = seq %d step %v", result.Record.Seq(), result.Record.StepID())
	}
	message, ok := mustEventRecordPayload(result.Record).(MessageRecord)
	if !ok {
		t.Fatalf("payload type = %T, want MessageRecord", mustEventRecordPayload(result.Record))
	}
	want := MessageRecord{
		Role:                 MessageRoleAssistant,
		MessageType:          messageTypePointer(MessageTypeBackgroundNotice),
		SourcePath:           stringPointer("/tmp/source.go"),
		Content:              stringPointer("message content"),
		CompactContent:       stringPointer("compact"),
		Phase:                messagePhasePointer(MessagePhaseCommentary),
		BackgroundActivityID: stringPointer("activity-1"),
		BackgroundExitCode:   intPointer(17),
		ToolCalls: []MessageToolCallRecord{{
			CallID:       "call-1",
			Name:         "exec_command",
			Kind:         ToolCallKindFunction,
			Presentation: []byte(`{"ToolName":"exec_command"}`),
			Input:        []byte(`{"cmd":"pwd"}`),
		}},
		ReasoningItems: []MessageReasoningRecord{{
			ID:               "reasoning-1",
			EncryptedContent: "encrypted",
		}},
	}
	if !reflect.DeepEqual(message, want) {
		t.Fatalf("legacy message = %#v, want %#v", message, want)
	}
}

func TestLegacyV0DecoderRejectsMessageToolCallWithoutInput(t *testing.T) {
	_, err := decodeLegacyFixtureError(t, []byte(`{
		"seq":7,
		"timestamp":"2026-07-19T10:00:00Z",
		"kind":"message",
		"payload":{
			"role":"assistant",
			"tool_calls":[{"id":"call-1","name":"exec_command"}]
		}
	}`))
	if err == nil {
		t.Fatal("missing tool-call input succeeded")
	}
}

func TestLegacyV0DecoderClassifiesToolCompletionSnapshots(t *testing.T) {
	t.Run("authoritative present Raw", func(t *testing.T) {
		raw := json.RawMessage(`{ "type" : "function_call_output", "call_id" : "call-1", "output" : "done" }`)
		result, ledger := decodeLegacyFixtureWithLedger(t, []byte(`{
			"seq":8,"timestamp":"2026-07-19T10:00:00Z","kind":"tool_completed",
			"payload":{
				"call_id":"call-1","name":"exec_command","is_error":false,"output":"done",
				"provider_items":[{
					"type":"function_call_output","name":"exec_command","call_id":"call-1",
					"output":"done","raw":`+string(raw)+`
				}]
			}
		}`))
		if result.SnapshotClass != legacyToolSnapshotAuthoritative || result.Record == nil {
			t.Fatalf("authoritative result = %#v", result)
		}
		completion := mustEventRecordPayload(result.Record).(ToolCompletionRecord)
		if len(completion.ProviderItems) != 1 || !bytes.Equal(completion.ProviderItems[0].Raw, raw) {
			t.Fatalf("authoritative Raw changed: %#v", completion.ProviderItems)
		}
		if got := ledger.snapshot().MaxEncoderMergeBytes; got != migrationCopyBufferBytes {
			t.Fatalf("lexical Raw copy buffer maximum = %d, want %d", got, migrationCopyBufferBytes)
		}
	})

	t.Run("supported missing Raw", func(t *testing.T) {
		result := decodeLegacyFixture(t, []byte(`{
			"seq":9,"timestamp":"2026-07-19T10:00:00Z","kind":"tool_completed",
			"payload":{
				"call_id":"call-2","name":"patch","is_error":false,"output":"patched",
				"provider_items":[{
					"type":"custom_tool_call_output","name":"patch","call_id":"call-2","output":"patched"
				}]
			}
		}`))
		if result.SnapshotClass != legacyToolSnapshotGeneratedRaw || result.Record == nil {
			t.Fatalf("generated result = %#v", result)
		}
		completion := mustEventRecordPayload(result.Record).(ToolCompletionRecord)
		if len(completion.ProviderItems) != 1 || len(completion.ProviderItems[0].Raw) == 0 {
			t.Fatalf("generated provider items = %#v", completion.ProviderItems)
		}
	})

	t.Run("supported missing Raw reports typed invalid facts", func(t *testing.T) {
		result, err := decodeLegacyFixtureError(t, []byte(`{
			"seq":9,"timestamp":"2026-07-19T10:00:00Z","kind":"tool_completed",
			"payload":{
				"call_id":"call-2","name":"patch","is_error":false,"output":"patched",
				"provider_items":[{"type":"custom_tool_call_output","call_id":"call-2"}]
			}
		}`))
		if result.SnapshotClass != legacyToolSnapshotGeneratedRaw {
			t.Fatalf("supported missing Raw class = %d", result.SnapshotClass)
		}
		var itemErr legacyToolSnapshotError
		if !errors.As(err, &itemErr) ||
			itemErr.Sequence != 9 ||
			itemErr.ItemIndex != 0 ||
			itemErr.Type != ProviderHistoryItemTypeCustomToolOutput ||
			itemErr.Reason != ToolCompletionProviderItemInvalidFacts {
			t.Fatalf("supported missing Raw error = %T %v", err, err)
		}
	})

	t.Run("absent snapshot", func(t *testing.T) {
		result := decodeLegacyFixture(t, []byte(`{
			"seq":10,"timestamp":"2026-07-19T10:00:00Z","kind":"tool_completed",
			"step_id":"step-10",
			"payload":{"call_id":"call-3","name":"exec_command","is_error":true,"output":{"error":"failed"}}
		}`))
		if result.SnapshotClass != legacyToolSnapshotAbsent ||
			result.Record != nil ||
			result.FallbackCompletion == nil ||
			result.FallbackCompletion.Sequence != 10 {
			t.Fatalf("absent snapshot result = %#v", result)
		}
	})

	t.Run("absent snapshot validates semantic facts", func(t *testing.T) {
		_, err := decodeLegacyFixtureError(t, []byte(`{
			"seq":10,"timestamp":"2026-07-19T10:00:00Z","kind":"tool_completed",
			"payload":{"call_id":" ","name":"exec_command","is_error":false,"output":"done"}
		}`))
		if err == nil {
			t.Fatal("absent snapshot with invalid semantic facts succeeded")
		}
	})

	t.Run("explicit empty snapshot is invalid", func(t *testing.T) {
		result, err := decodeLegacyFixtureError(t, []byte(`{
			"seq":10,"timestamp":"2026-07-19T10:00:00Z","kind":"tool_completed",
			"payload":{
				"call_id":"call-3","name":"exec_command","is_error":false,"output":"done",
				"provider_items":[]
			}
		}`))
		if err == nil {
			t.Fatal("explicit empty provider snapshot succeeded")
		}
		if result.FallbackCompletion != nil ||
			result.SnapshotClass == legacyToolSnapshotAbsent {
			t.Fatalf("explicit empty provider snapshot classified as absent: %#v", result)
		}
	})

	t.Run("is_error is required", func(t *testing.T) {
		_, err := decodeLegacyFixtureError(t, []byte(`{
			"seq":10,"timestamp":"2026-07-19T10:00:00Z","kind":"tool_completed",
			"payload":{"call_id":"call-3","name":"exec_command","output":"done"}
		}`))
		if err == nil {
			t.Fatal("tool completion without is_error succeeded")
		}
	})

	t.Run("unsupported missing Raw", func(t *testing.T) {
		result, err := decodeLegacyFixtureError(t, []byte(`{
			"seq":11,"timestamp":"2026-07-19T10:00:00Z","kind":"tool_completed",
			"payload":{
				"call_id":"call-4","name":"view_image","is_error":false,"output":"done",
				"provider_items":[{"type":"other","linked_call_id":"call-4","link_kind":"tool_output_attachment"}]
			}
		}`))
		if result.SnapshotClass != legacyToolSnapshotUnsupportedMissingRaw {
			t.Fatalf("unsupported missing Raw class = %d", result.SnapshotClass)
		}
		var itemErr legacyToolSnapshotError
		if !errors.As(err, &itemErr) ||
			itemErr.Sequence != 11 ||
			itemErr.ItemIndex != 0 ||
			itemErr.Type != ProviderHistoryItemTypeOther ||
			itemErr.Reason != ToolCompletionProviderItemMissingRaw {
			t.Fatalf("unsupported missing Raw error = %T %v", err, err)
		}
	})

	t.Run("unsupported type is distinct from missing Raw", func(t *testing.T) {
		result, err := decodeLegacyFixtureError(t, []byte(`{
			"seq":11,"timestamp":"2026-07-19T10:00:00Z","kind":"tool_completed",
			"payload":{
				"call_id":"call-4","name":"view_image","is_error":false,"output":"done",
				"provider_items":[{"type":"message","raw":{"type":"message","content":"done"}}]
			}
		}`))
		if result.SnapshotClass == legacyToolSnapshotUnsupportedMissingRaw {
			t.Fatalf("unsupported present-Raw type classified as missing Raw: %#v", result)
		}
		var itemErr legacyToolSnapshotError
		if !errors.As(err, &itemErr) ||
			itemErr.Sequence != 11 ||
			itemErr.ItemIndex != 0 ||
			itemErr.Type != ProviderHistoryItemTypeMessage ||
			itemErr.Reason != ToolCompletionProviderItemUnsupportedType {
			t.Fatalf("unsupported type error = %T %v", err, err)
		}
	})
}

func TestLegacyV0DecoderMapsHistoryLocalAndCacheFacts(t *testing.T) {
	t.Run("history replacement", func(t *testing.T) {
		raw := json.RawMessage(`{ "type" : "future_provider_item", "number" : 1.2300 }`)
		result, ledger := decodeLegacyFixtureWithLedger(t, []byte(`{
			"seq":12,"timestamp":"2026-07-19T10:00:00Z","kind":"history_replaced",
			"payload":{
				"engine":"local","mode":"handoff","workflow_run_id":"run-1","compaction_number":2,
				"committed_entry_start":7,"pending_handoff_future_message":"continue",
				"last_committed_assistant_final_answer":"answer",
				"latest_rollback_candidate":{"user_message_seq":5,"candidate_page_end_byte":4096},
				"items":[{"type":"other","raw":`+string(raw)+`}]
			}
		}`))
		history := mustEventRecordPayload(result.Record).(HistoryReplacementRecord)
		if history.WorkflowRunID == nil || *history.WorkflowRunID != "run-1" ||
			history.LatestRollbackCandidate == nil ||
			!bytes.Equal(history.Items[0].Raw, raw) {
			t.Fatalf("history replacement = %#v", history)
		}
		if got := ledger.snapshot().MaxEncoderMergeBytes; got != migrationCopyBufferBytes {
			t.Fatalf("history lexical Raw copy buffer maximum = %d, want %d", got, migrationCopyBufferBytes)
		}
	})

	t.Run("history replacement requires items", func(t *testing.T) {
		_, err := decodeLegacyFixtureError(t, []byte(`{
			"seq":12,"timestamp":"2026-07-19T10:00:00Z","kind":"history_replaced",
			"payload":{"engine":"local","mode":"handoff"}
		}`))
		if err == nil {
			t.Fatal("history replacement without items succeeded")
		}
	})

	for _, test := range []struct {
		name       string
		visibility string
		want       EntryVisibility
	}{
		{name: "all", visibility: "all", want: EntryVisibilityOngoing},
		{name: "verbose", visibility: "verbose", want: EntryVisibilityDetail},
		{name: "compact ongoing", visibility: "O", want: EntryVisibilityOngoing},
		{name: "compact ongoing collapsed", visibility: "OC", want: EntryVisibilityOngoingCollapsed},
		{name: "compact detail", visibility: "D", want: EntryVisibilityDetail},
		{name: "compact hidden", visibility: "X", want: EntryVisibilityHidden},
	} {
		t.Run("legacy "+test.name+" local-entry visibility", func(t *testing.T) {
			result := decodeLegacyFixture(t, []byte(`{
				"seq":13,"timestamp":"2026-07-19T10:00:00Z","kind":"local_entry",
				"payload":{"visibility":"`+test.visibility+`","role":"notice","text":"legacy row"}
			}`))
			local := mustEventRecordPayload(result.Record).(LocalEntryRecord)
			if local.Visibility != test.want {
				t.Fatalf(
					"legacy %s visibility = %q, want %q",
					test.name,
					local.Visibility,
					test.want,
				)
			}
		})
	}

	t.Run("local entry", func(t *testing.T) {
		result := decodeLegacyFixture(t, []byte(`{
			"seq":13,"timestamp":"2026-07-19T10:00:00Z","kind":"local_entry",
			"payload":{
				"visibility":"detail","role":"error","text":"failed","condensed_text":"fail",
				"diagnostic_key":"diag-1","notice_id":"notice-1","after_tool_call_id":"call-1"
			}
		}`))
		entry := mustEventRecordPayload(result.Record).(LocalEntryRecord)
		if entry.CondensedText == nil || entry.DiagnosticKey == nil ||
			entry.NoticeID == nil || entry.AfterToolCallID == nil {
			t.Fatalf("local entry lost facts: %#v", entry)
		}
	})

	terminalHash := strings.Repeat("a", 64)
	t.Run("cache defaults and token presence", func(t *testing.T) {
		request := mustEventRecordPayload(decodeLegacyFixture(t, []byte(`{
			"seq":14,"timestamp":"2026-07-19T10:00:00Z","kind":"cache_request_observed",
			"payload":{"cache_key":"cache","chunk_count":2,"terminal_hash":"`+terminalHash+`"}
		}`)).Record).(CacheRequestObservationRecord)
		if request.DigestVersion != CacheDigestV1 || request.Scope != CacheScopeConversation {
			t.Fatalf("cache request defaults = %#v", request)
		}
		response := mustEventRecordPayload(decodeLegacyFixture(t, []byte(`{
			"seq":15,"timestamp":"2026-07-19T10:00:00Z","kind":"cache_response_observed",
			"payload":{
				"cache_key":"cache","chunk_count":2,"terminal_hash":"`+terminalHash+`",
				"has_cached_input_tokens":true,"cached_input_tokens":0
			}
		}`)).Record).(CacheResponseObservationRecord)
		if response.CachedInputTokens == nil || *response.CachedInputTokens != 0 {
			t.Fatalf("cache response token presence = %#v", response)
		}
	})

	t.Run("readerless and reviewer rollback dropped", func(t *testing.T) {
		readerless := decodeLegacyFixture(t, []byte(`{
			"seq":16,"timestamp":"2026-07-19T10:00:00Z","kind":"run_started","payload":{}
		}`))
		if !readerless.Dropped {
			t.Fatalf("readerless result = %#v", readerless)
		}
		reviewer := decodeLegacyFixture(t, []byte(`{
			"seq":17,"timestamp":"2026-07-19T10:00:00Z","kind":"history_replaced",
			"payload":{"engine":"reviewer_rollback","mode":"manual","items":[]}
		}`))
		if !reviewer.Dropped {
			t.Fatalf("reviewer rollback result = %#v", reviewer)
		}
	})
}

func decodeLegacyFixture(t *testing.T, line []byte) legacyEventV0DecodeResult {
	t.Helper()
	result, _ := decodeLegacyFixtureWithLedger(t, line)
	return result
}

func decodeLegacyFixtureWithLedger(
	t *testing.T,
	line []byte,
) (legacyEventV0DecodeResult, *migrationResourceLedger) {
	t.Helper()
	result, ledger, err := decodeLegacyFixtureErrorWithLedger(t, line)
	if err != nil {
		t.Fatalf("decode legacy fixture: %v", err)
	}
	return result, ledger
}

func decodeLegacyFixtureError(
	t *testing.T,
	line []byte,
) (legacyEventV0DecodeResult, error) {
	t.Helper()
	result, _, err := decodeLegacyFixtureErrorWithLedger(t, line)
	return result, err
}

func decodeLegacyFixtureErrorWithLedger(
	t *testing.T,
	line []byte,
) (legacyEventV0DecodeResult, *migrationResourceLedger, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy.json")
	if err := os.WriteFile(path, line, 0o600); err != nil {
		t.Fatalf("write legacy fixture: %v", err)
	}
	source, err := openRegularSessionFile(path, "legacy fixture")
	if err != nil {
		t.Fatalf("open legacy fixture: %v", err)
	}
	defer source.Close()
	ledger := newMigrationResourceLedger()
	result, err := decodeLegacyEventV0(source, 0, int64(len(line)), ledger)
	return result, ledger, err
}
