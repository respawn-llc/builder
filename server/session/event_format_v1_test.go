package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"core/shared/rollbacktarget"
	"core/shared/transcript"

	"github.com/google/uuid"
)

func TestEventLogV1HeaderRoundTrip(t *testing.T) {
	line, err := encodeEventLogHeaderV1()
	if err != nil {
		t.Fatalf("encode header: %v", err)
	}

	header, err := decodeEventLogHeader(line)
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	if header.Contract != EventLogContract || header.Version != EventLogVersionV1 {
		t.Fatalf("header = %#v", header)
	}
}

func TestEventLogV1HeaderIgnoresUnknownFields(t *testing.T) {
	header, err := decodeEventLogHeader([]byte(`{
		"contract":"kent.session.events",
		"version":1,
		"future_header_fact":true
	}`))
	if err != nil {
		t.Fatalf("decode header with unknown field: %v", err)
	}
	if header.Contract != EventLogContract || header.Version != EventLogVersionV1 {
		t.Fatalf("header = %#v", header)
	}
}

func TestEventLogV1HeaderRejectsUnsupportedContractAndVersion(t *testing.T) {
	tests := []struct {
		name string
		line []byte
	}{
		{
			name: "contract",
			line: []byte(`{"contract":"other.events","version":1}`),
		},
		{
			name: "version",
			line: []byte(`{"contract":"kent.session.events","version":3}`),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeEventLogHeader(test.line); err == nil {
				t.Fatal("expected unsupported header error")
			}
		})
	}
}

func TestEventLogV1MessageRecordRoundTrip(t *testing.T) {
	stepID := "step-1"
	content := "ship the typed event format"
	record, err := NewEventRecord(1, &stepID, MessageRecord{
		Role:    MessageRoleUser,
		Content: &content,
	})
	if err != nil {
		t.Fatalf("create message record: %v", err)
	}

	line, err := encodeEventRecordV1(record)
	if err != nil {
		t.Fatalf("encode message record: %v", err)
	}
	envelope := decodeJSONObject(t, line)
	if _, ok := envelope["timestamp"]; ok {
		t.Fatalf("encoded record contains removed timestamp: %s", line)
	}

	decoded, err := decodeEventRecordV1(line)
	if err != nil {
		t.Fatalf("decode message record: %v", err)
	}
	if decoded.Seq() != record.Seq() {
		t.Fatalf("sequence = %d, want %d", decoded.Seq(), record.Seq())
	}
	if decoded.StepID() == nil || *decoded.StepID() != stepID {
		t.Fatalf("step identity = %v, want %q", decoded.StepID(), stepID)
	}
	message, ok := mustEventRecordPayload(decoded).(MessageRecord)
	if !ok {
		t.Fatalf("payload type = %T, want MessageRecord", mustEventRecordPayload(decoded))
	}
	if message.Content == nil || *message.Content != content || message.Role != MessageRoleUser {
		t.Fatalf("message = %#v", message)
	}
}

func TestEventLogV1MessageSemanticFieldsRoundTrip(t *testing.T) {
	contextID := uuid.New()
	branch := "feature/session-v1"
	record, err := NewEventRecord(1, nil, MessageRecord{
		Role:                 MessageRoleAssistant,
		MessageType:          messageTypePointer(MessageTypeBackgroundNotice),
		SourcePath:           stringPointer("/tmp/source.go"),
		WorktreeContext:      &WorktreeContext{ContextID: &contextID, Branch: &branch, WorktreePath: "/tmp/worktree", WorkspaceRoot: "/tmp/workspace", EffectiveCwd: "/tmp/worktree"},
		Content:              stringPointer("tool call follows"),
		CompactContent:       stringPointer("tool call"),
		Name:                 stringPointer("assistant"),
		ToolCallID:           stringPointer("parent-call"),
		Phase:                messagePhasePointer(MessagePhaseCommentary),
		BackgroundActivityID: stringPointer("activity-1"),
		BackgroundExitCode:   intPointer(17),
		ToolCalls: []MessageToolCallRecord{{
			CallID:       "call-1",
			Name:         "exec_command",
			Kind:         ToolCallKindFunction,
			Presentation: json.RawMessage(`{"ToolName":"exec_command"}`),
			Input:        json.RawMessage(`{"cmd":"pwd"}`),
		}, {
			CallID:      "call-2",
			Name:        "patch",
			Kind:        ToolCallKindCustom,
			Input:       json.RawMessage(`"*** Begin Patch\n*** End Patch"`),
			CustomInput: stringPointer("*** Begin Patch\n*** End Patch"),
		}},
		ReasoningItems: []MessageReasoningRecord{{
			ID:               "reasoning-1",
			EncryptedContent: "encrypted-1",
		}},
	})
	if err != nil {
		t.Fatalf("create semantic message record: %v", err)
	}

	line, err := encodeEventRecordV1(record)
	if err != nil {
		t.Fatalf("encode semantic message record: %v", err)
	}
	decoded, err := decodeEventRecordV1(line)
	if err != nil {
		t.Fatalf("decode semantic message record: %v", err)
	}
	message, ok := mustEventRecordPayload(decoded).(MessageRecord)
	if !ok {
		t.Fatalf("payload type = %T, want MessageRecord", mustEventRecordPayload(decoded))
	}
	if !reflect.DeepEqual(message, mustEventRecordPayload(record)) {
		t.Fatalf("decoded message = %#v, want %#v", message, mustEventRecordPayload(record))
	}
}

func TestEventLogV1RejectsBackgroundNoticeWithPartialIdentity(t *testing.T) {
	tests := []struct {
		name     string
		message  *string
		activity *string
	}{
		{
			name:    "process only",
			message: stringPointer("4345"),
		},
		{
			name:     "activity only",
			activity: stringPointer("b2a700e9-1d0b-42bb-86b9-d8912f0b4119"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewEventRecord(1, nil, MessageRecord{
				Role:                 MessageRoleDeveloper,
				MessageType:          messageTypePointer(MessageTypeBackgroundNotice),
				Content:              stringPointer("background completed"),
				Name:                 test.message,
				BackgroundActivityID: test.activity,
			})
			if err == nil {
				t.Fatal("expected partial background identity rejection")
			}
		})
	}
}

func TestEventLogV1MessageKeepsAbsentContentAbsent(t *testing.T) {
	record, err := NewEventRecord(1, nil, MessageRecord{Role: MessageRoleAssistant})
	if err != nil {
		t.Fatalf("create message record: %v", err)
	}

	line, err := encodeEventRecordV1(record)
	if err != nil {
		t.Fatalf("encode message record: %v", err)
	}
	envelope := decodeJSONObject(t, line)
	payload := decodeJSONObject(t, envelope["payload"])
	if _, ok := payload["content"]; ok {
		t.Fatalf("encoded absent content: %s", line)
	}

	decoded, err := decodeEventRecordV1(line)
	if err != nil {
		t.Fatalf("decode message record: %v", err)
	}
	message, ok := mustEventRecordPayload(decoded).(MessageRecord)
	if !ok {
		t.Fatalf("payload type = %T, want MessageRecord", mustEventRecordPayload(decoded))
	}
	if message.Content != nil {
		t.Fatalf("content = %q, want absent", *message.Content)
	}
}

func TestEventLogV1MessageOmitsAbsentSemanticFields(t *testing.T) {
	record, err := NewEventRecord(1, nil, MessageRecord{Role: MessageRoleAssistant})
	if err != nil {
		t.Fatalf("create message record: %v", err)
	}
	line, err := encodeEventRecordV1(record)
	if err != nil {
		t.Fatalf("encode message record: %v", err)
	}
	envelope := decodeJSONObject(t, line)
	payload := decodeJSONObject(t, envelope["payload"])
	for _, field := range []string{
		"message_type",
		"source_path",
		"worktree_context",
		"content",
		"compact_content",
		"name",
		"tool_call_id",
		"phase",
		"background_activity_id",
		"background_exit_code",
		"tool_calls",
		"reasoning_items",
	} {
		if _, ok := payload[field]; ok {
			t.Fatalf("encoded absent %s: %s", field, line)
		}
	}
}

func TestEventLogV1AcceptsSystemMessageRole(t *testing.T) {
	content := "system context"
	record, err := NewEventRecord(1, nil, MessageRecord{
		Role:    MessageRoleSystem,
		Content: &content,
	})
	if err != nil {
		t.Fatalf("create system message record: %v", err)
	}
	if mustEventRecordKind(record) != EventKindMessage {
		t.Fatalf("kind = %q, want %q", mustEventRecordKind(record), EventKindMessage)
	}
}

func TestEventLogV1ToolCompletionRecordRoundTrip(t *testing.T) {
	record, err := NewEventRecord(2, nil, ToolCompletionRecord{
		CallID:     "call-1",
		Name:       "shell",
		OutputKind: ToolOutputKindFunction,
		Output:     json.RawMessage(`{"exit_code":0}`),
	})
	if err != nil {
		t.Fatalf("create tool completion record: %v", err)
	}

	line, err := encodeEventRecordV1(record)
	if err != nil {
		t.Fatalf("encode tool completion record: %v", err)
	}
	decoded, err := decodeEventRecordV1(line)
	if err != nil {
		t.Fatalf("decode tool completion record: %v", err)
	}
	completion, ok := mustEventRecordPayload(decoded).(ToolCompletionRecord)
	if !ok {
		t.Fatalf("payload type = %T, want ToolCompletionRecord", mustEventRecordPayload(decoded))
	}
	if completion.CallID != "call-1" || completion.Name != "shell" ||
		!bytes.Equal(completion.Output, json.RawMessage(`{"exit_code":0}`)) {
		t.Fatalf("completion = %#v", completion)
	}
}

func TestEventLogV1ToolCompletionProviderSnapshotRoundTrip(t *testing.T) {
	providerRaw := json.RawMessage(`{ "type" : "function_call_output", "call_id" : "call-1", "output" : "done" }`)
	attachmentRaw := json.RawMessage(`{ "type" : "message", "role" : "user", "content" : [ { "type" : "input_file", "file_id" : "file-1" } ] }`)
	record, err := NewEventRecord(2, nil, ToolCompletionRecord{
		CallID:        "call-1",
		Name:          "view_image",
		OutputKind:    ToolOutputKindFunction,
		IsError:       true,
		Output:        json.RawMessage(`[{"type":"input_file","file_id":"file-1"}]`),
		Summary:       stringPointer("opened image"),
		CondensedText: stringPointer("image"),
		Presentation:  json.RawMessage(`{"ToolName":"view_image"}`),
		ProviderItems: []ToolCompletionProviderItem{{
			Type:   ProviderInputItemTypeFunctionCallOutput,
			Name:   stringPointer("view_image"),
			CallID: stringPointer("call-1"),
			Raw:    providerRaw,
		}, {
			Type:         ProviderInputItemTypeOther,
			Name:         stringPointer("view_image"),
			CallID:       stringPointer("call-1"),
			Raw:          attachmentRaw,
			LinkedCallID: stringPointer("call-1"),
			LinkKind:     providerItemLinkKindPointer(ProviderItemLinkToolOutputAttachment),
		}},
	})
	if err != nil {
		t.Fatalf("create tool completion snapshot: %v", err)
	}

	line, err := encodeEventRecordV1(record)
	if err != nil {
		t.Fatalf("encode tool completion snapshot: %v", err)
	}
	if !bytes.Contains(line, providerRaw) || !bytes.Contains(line, attachmentRaw) {
		t.Fatalf("encoded provider Raw changed lexical bytes: %s", line)
	}
	decoded, err := decodeEventRecordV1(line)
	if err != nil {
		t.Fatalf("decode tool completion snapshot: %v", err)
	}
	completion, ok := mustEventRecordPayload(decoded).(ToolCompletionRecord)
	if !ok {
		t.Fatalf("payload type = %T, want ToolCompletionRecord", mustEventRecordPayload(decoded))
	}
	if !reflect.DeepEqual(completion, mustEventRecordPayload(record)) {
		t.Fatalf("decoded completion = %#v, want %#v", completion, mustEventRecordPayload(record))
	}
}

func TestEventLogV1ToolCompletionGeneratesSupportedMissingProviderRaw(t *testing.T) {
	tests := []struct {
		name       string
		outputKind ToolOutputKind
		itemType   ProviderInputItemType
		wantRaw    json.RawMessage
	}{
		{
			name:       "function",
			outputKind: ToolOutputKindFunction,
			itemType:   ProviderInputItemTypeFunctionCallOutput,
			wantRaw:    json.RawMessage(`{"type":"function_call_output","call_id":"call-1","output":"{\"ok\":true}"}`),
		},
		{
			name:       "custom",
			outputKind: ToolOutputKindCustom,
			itemType:   ProviderInputItemTypeCustomToolOutput,
			wantRaw:    json.RawMessage(`{"type":"custom_tool_call_output","call_id":"call-1","output":"{\"ok\":true}"}`),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record, err := NewEventRecord(1, nil, ToolCompletionRecord{
				CallID:     "call-1",
				Name:       "tool",
				OutputKind: test.outputKind,
				Output:     json.RawMessage(`{"ok":true}`),
				ProviderItems: []ToolCompletionProviderItem{{
					Type:   test.itemType,
					CallID: stringPointer("call-1"),
				}},
			})
			if err != nil {
				t.Fatalf("create completion: %v", err)
			}
			completion := mustEventRecordPayload(record).(ToolCompletionRecord)
			if len(completion.ProviderItems) != 1 ||
				!bytes.Equal(completion.ProviderItems[0].Raw, test.wantRaw) {
				t.Fatalf("generated Raw = %s, want %s", completion.ProviderItems[0].Raw, test.wantRaw)
			}
		})
	}
}

func TestEventLogV1ToolCompletionRejectsMissingRawOutsideSupportedOutputs(t *testing.T) {
	tests := []struct {
		name string
		item ToolCompletionProviderItem
	}{
		{
			name: "linked attachment",
			item: ToolCompletionProviderItem{
				Type:         ProviderInputItemTypeOther,
				LinkedCallID: stringPointer("call-1"),
				LinkKind:     providerItemLinkKindPointer(ProviderItemLinkToolOutputAttachment),
			},
		},
		{
			name: "opaque other",
			item: ToolCompletionProviderItem{Type: ProviderInputItemTypeOther},
		},
		{
			name: "unsupported message",
			item: ToolCompletionProviderItem{Type: "message"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewEventRecord(1, nil, ToolCompletionRecord{
				CallID:        "call-1",
				Name:          "tool",
				OutputKind:    ToolOutputKindFunction,
				Output:        json.RawMessage(`{"ok":true}`),
				ProviderItems: []ToolCompletionProviderItem{test.item},
			})
			var itemErr ToolCompletionProviderItemError
			if !errors.As(err, &itemErr) {
				t.Fatalf("error = %T %v, want ToolCompletionProviderItemError", err, err)
			}
			wantReason := ToolCompletionProviderItemMissingRaw
			if test.name == "unsupported message" {
				wantReason = ToolCompletionProviderItemUnsupportedType
			}
			if itemErr.Reason != wantReason {
				t.Fatalf("reason = %q, want %q", itemErr.Reason, wantReason)
			}
		})
	}
}

func TestEventLogV1ToolCompletionRejectsAttachmentLinkedToAnotherCall(t *testing.T) {
	_, err := NewEventRecord(1, nil, ToolCompletionRecord{
		CallID:     "call-1",
		Name:       "view_image",
		OutputKind: ToolOutputKindFunction,
		Output:     json.RawMessage(`[{"type":"input_file","file_id":"file-1"}]`),
		ProviderItems: []ToolCompletionProviderItem{{
			Type:         ProviderInputItemTypeOther,
			Raw:          json.RawMessage(`{"type":"message"}`),
			LinkedCallID: stringPointer("call-2"),
			LinkKind:     providerItemLinkKindPointer(ProviderItemLinkToolOutputAttachment),
		}},
	})
	var itemErr ToolCompletionProviderItemError
	if !errors.As(err, &itemErr) {
		t.Fatalf("error = %T %v, want ToolCompletionProviderItemError", err, err)
	}
	if itemErr.Reason != ToolCompletionProviderItemInvalidFacts {
		t.Fatalf("reason = %q, want %q", itemErr.Reason, ToolCompletionProviderItemInvalidFacts)
	}
}

func TestEventLogV1LocalEntryRecordRoundTrip(t *testing.T) {
	durationMs := int64(1234)
	record, err := NewEventRecord(3, nil, LocalEntryRecord{
		Visibility: EntryVisibilityDetail,
		Role:       "error",
		Text:       stringPointer(" compaction failed "),
		DurationMs: &durationMs,
	})
	if err != nil {
		t.Fatalf("create local entry record: %v", err)
	}

	line, err := encodeEventRecordV1(record)
	if err != nil {
		t.Fatalf("encode local entry record: %v", err)
	}
	decoded, err := decodeEventRecordV1(line)
	if err != nil {
		t.Fatalf("decode local entry record: %v", err)
	}
	entry, ok := mustEventRecordPayload(decoded).(LocalEntryRecord)
	if !ok {
		t.Fatalf("payload type = %T, want LocalEntryRecord", mustEventRecordPayload(decoded))
	}
	if entry.Visibility != EntryVisibilityDetail || entry.Role != "error" ||
		entry.Text == nil || *entry.Text != "compaction failed" ||
		entry.DurationMs == nil || *entry.DurationMs != durationMs {
		t.Fatalf("entry = %#v", entry)
	}
	negativeDurationMs := int64(-1)
	if _, err := NewEventRecord(1, nil, LocalEntryRecord{
		Visibility: EntryVisibilityDetail,
		Role:       "reasoning",
		Text:       stringPointer("trace"),
		DurationMs: &negativeDurationMs,
	}); err == nil {
		t.Fatal("negative local-entry duration was accepted")
	}
}

func TestEventLogV1TypedRepairNoticePersistsNullText(t *testing.T) {
	record, err := NewEventRecord(1, nil, LocalEntryRecord{
		Visibility: EntryVisibilityOngoing,
		Role:       "error",
		ToolOutputRepair: &transcript.ToolOutputRepairNotice{
			Kind:  transcript.ToolOutputRepairFreshResource,
			Count: 1,
		},
	})
	if err != nil {
		t.Fatalf("create typed repair notice record: %v", err)
	}

	line, err := encodeEventRecordV1(record)
	if err != nil {
		t.Fatalf("encode typed repair notice record: %v", err)
	}
	var encoded struct {
		Payload map[string]json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(line, &encoded); err != nil {
		t.Fatalf("decode encoded repair notice envelope: %v", err)
	}
	text, ok := encoded.Payload["text"]
	if !ok {
		t.Fatal("encoded repair notice omitted explicit text absence")
	}
	var nullableText *string
	if err := json.Unmarshal(text, &nullableText); err != nil {
		t.Fatalf("decode encoded repair notice text: %v", err)
	}
	if nullableText != nil {
		t.Fatalf("encoded repair notice text = %q, want null", *nullableText)
	}
	if _, err := NewEventRecord(1, nil, LocalEntryRecord{
		Visibility: EntryVisibilityOngoing,
		Role:       "error",
		Text:       stringPointer(" \t "),
		ToolOutputRepair: &transcript.ToolOutputRepairNotice{
			Kind:  transcript.ToolOutputRepairFreshResource,
			Count: 1,
		},
	}); err == nil {
		t.Fatal("typed repair notice accepted present blank text")
	}
}

func TestEventLogV1LocalEntryRejectsMultipleTypedNoticeFacts(t *testing.T) {
	_, err := NewEventRecord(1, nil, LocalEntryRecord{
		Visibility: EntryVisibilityDetail, Role: "warning",
		ToolOutputRepair:      &transcript.ToolOutputRepairNotice{Kind: transcript.ToolOutputRepairFreshResource, Count: 1},
		ProviderModelMismatch: &transcript.ProviderModelMismatchNotice{RequestedModel: "requested", ServedModel: "served"},
	})
	if err == nil {
		t.Fatal("local entry with multiple typed notice facts was accepted")
	}
}

func TestEventLogV1ProviderModelMismatchNoticeRoundTrip(t *testing.T) {
	record, err := NewEventRecord(1, nil, LocalEntryRecord{
		Visibility: EntryVisibilityDetail,
		Role:       "warning",
		ProviderModelMismatch: &transcript.ProviderModelMismatchNotice{
			RequestedModel: " requested-model ",
			ServedModel:    " served-model ",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	line, err := encodeEventRecordV1(record)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeEventRecordV1(line)
	if err != nil {
		t.Fatal(err)
	}
	entry := mustEventRecordPayload(decoded).(LocalEntryRecord)
	if entry.Text != nil || entry.ProviderModelMismatch == nil ||
		entry.ProviderModelMismatch.RequestedModel != "requested-model" ||
		entry.ProviderModelMismatch.ServedModel != "served-model" {
		t.Fatalf("provider-model mismatch entry = %+v", entry)
	}
}

func TestEventLogV1HistoryReplacementRecordRoundTrip(t *testing.T) {
	messageRaw := json.RawMessage(`{ "type" : "message", "role" : "user", "content" : [ { "type" : "input_text", "text" : "\u0068ello" } ] }`)
	callRaw := json.RawMessage(`{ "type" : "function_call", "call_id" : "call-1", "name" : "exec_command", "arguments" : "{\"cmd\":\"pwd\"}" }`)
	outputRaw := json.RawMessage(`{ "type" : "function_call_output", "call_id" : "call-1", "output" : "{\"cwd\":\"\\/tmp\"}" }`)
	reasoningRaw := json.RawMessage(`{ "type" : "reasoning", "id" : "reasoning-1", "summary" : [ { "type" : "summary_text", "text" : "kept" } ], "encrypted_content" : "enc" }`)
	compactionRaw := json.RawMessage(`{ "type" : "compaction", "id" : "compaction-1", "encrypted_content" : "compact-enc" }`)
	opaqueRaw := json.RawMessage(`{ "type" : "future_provider_item", "number" : 1.2300 }`)
	record, err := NewEventRecord(4, nil, HistoryReplacementRecord{
		Engine:                            "local",
		Mode:                              CompactionModeAuto,
		CompactionNumber:                  intPointer(3),
		CommittedEntryStart:               intPointer(12),
		PendingHandoffFutureMessage:       stringPointer("continue with the cache-safe history"),
		LastCommittedAssistantFinalAnswer: stringPointer("previous final answer"),
		LatestRollbackCandidate: &rollbacktarget.CandidateLocator{
			UserMessageSeq:       7,
			CandidatePageEndByte: 4096,
		},
		Items: []ProviderHistoryItem{
			{
				Type:           ProviderHistoryItemTypeMessage,
				Role:           pointerTo(MessageRoleUser),
				MessageType:    messageTypePointer(MessageTypeCompactionSummary),
				SourcePath:     stringPointer("/tmp/source.md"),
				Phase:          messagePhasePointer(MessagePhaseCommentary),
				Content:        stringPointer("hello"),
				CompactContent: stringPointer("hi"),
				Raw:            messageRaw,
			},
			{
				Type:             ProviderHistoryItemTypeFunctionCall,
				ID:               stringPointer("item-call-1"),
				Name:             stringPointer("exec_command"),
				CallID:           stringPointer("call-1"),
				ToolPresentation: json.RawMessage(`{ "ToolName" : "exec_command" }`),
				Arguments:        json.RawMessage(`{ "cmd" : "pwd" }`),
				Raw:              callRaw,
			},
			{
				Type:   ProviderHistoryItemTypeFunctionCallOutput,
				Name:   stringPointer("exec_command"),
				CallID: stringPointer("call-1"),
				Output: json.RawMessage(`{ "cwd" : "/tmp" }`),
				Raw:    outputRaw,
			},
			{
				Type: ProviderHistoryItemTypeReasoning,
				ID:   stringPointer("reasoning-1"),
				ReasoningSummary: []ProviderHistoryReasoningEntry{{
					Role: stringPointer("assistant"),
					Text: "kept",
				}},
				EncryptedContent: stringPointer("enc"),
				Raw:              reasoningRaw,
			},
			{
				Type:             ProviderHistoryItemTypeCompaction,
				ID:               stringPointer("compaction-1"),
				EncryptedContent: stringPointer("compact-enc"),
				Raw:              compactionRaw,
			},
			{
				Type:         ProviderHistoryItemTypeOther,
				Name:         stringPointer("view_image"),
				CallID:       stringPointer("call-1"),
				Raw:          opaqueRaw,
				LinkedCallID: stringPointer("call-1"),
				LinkKind:     providerItemLinkKindPointer(ProviderItemLinkToolOutputAttachment),
			},
		},
	})
	if err != nil {
		t.Fatalf("create history replacement record: %v", err)
	}

	line, err := encodeEventRecordV1(record)
	if err != nil {
		t.Fatalf("encode history replacement record: %v", err)
	}
	decoded, err := decodeEventRecordV1(line)
	if err != nil {
		t.Fatalf("decode history replacement record: %v", err)
	}
	replacement, ok := mustEventRecordPayload(decoded).(HistoryReplacementRecord)
	if !ok {
		t.Fatalf("payload type = %T, want HistoryReplacementRecord", mustEventRecordPayload(decoded))
	}
	if !reflect.DeepEqual(replacement, mustEventRecordPayload(record)) {
		t.Fatalf("replacement = %#v, want %#v", replacement, mustEventRecordPayload(record))
	}
	for _, raw := range []json.RawMessage{messageRaw, callRaw, outputRaw, reasoningRaw, compactionRaw, opaqueRaw} {
		if !bytes.Contains(line, raw) {
			t.Fatalf("encoded history Raw changed lexical bytes: missing %s in %s", raw, line)
		}
	}
}

func TestEventLogV1HistoryReplacementRejectsMissingRawOutsideDerivableOutputs(t *testing.T) {
	tests := []ProviderHistoryItemType{
		ProviderHistoryItemTypeMessage,
		ProviderHistoryItemTypeFunctionCall,
		ProviderHistoryItemTypeCustomToolCall,
		ProviderHistoryItemTypeReasoning,
		ProviderHistoryItemTypeCompaction,
		ProviderHistoryItemTypeOther,
	}
	for _, itemType := range tests {
		t.Run(string(itemType), func(t *testing.T) {
			_, err := NewEventRecord(1, nil, HistoryReplacementRecord{
				Engine: "local",
				Mode:   CompactionModeAuto,
				Items:  []ProviderHistoryItem{{Type: itemType}},
			})
			var itemErr ProviderHistoryItemError
			if !errors.As(err, &itemErr) {
				t.Fatalf("error = %T %v, want ProviderHistoryItemError", err, err)
			}
			if itemErr.Reason != ProviderHistoryItemMissingRaw {
				t.Fatalf("reason = %q, want %q", itemErr.Reason, ProviderHistoryItemMissingRaw)
			}
		})
	}
}

func TestEventLogV1HistoryReplacementGeneratesMissingOutputRaw(t *testing.T) {
	tests := []struct {
		name     string
		itemType ProviderHistoryItemType
		wantRaw  json.RawMessage
	}{
		{
			name:     "function",
			itemType: ProviderHistoryItemTypeFunctionCallOutput,
			wantRaw:  json.RawMessage(`{"type":"function_call_output","call_id":"call-1","output":"{\"ok\":true}"}`),
		},
		{
			name:     "custom",
			itemType: ProviderHistoryItemTypeCustomToolOutput,
			wantRaw:  json.RawMessage(`{"type":"custom_tool_call_output","call_id":"call-1","output":"{\"ok\":true}"}`),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record, err := NewEventRecord(1, nil, HistoryReplacementRecord{
				Engine: "local",
				Mode:   CompactionModeAuto,
				Items: []ProviderHistoryItem{{
					Type:   test.itemType,
					CallID: stringPointer("call-1"),
					Output: json.RawMessage(`{"ok":true}`),
				}},
			})
			if err != nil {
				t.Fatalf("create history replacement: %v", err)
			}
			replacement := mustEventRecordPayload(record).(HistoryReplacementRecord)
			if !bytes.Equal(replacement.Items[0].Raw, test.wantRaw) {
				t.Fatalf("generated Raw = %s, want %s", replacement.Items[0].Raw, test.wantRaw)
			}
		})
	}
}

func TestEventLogV1CacheRequestObservationRoundTrip(t *testing.T) {
	terminalHash := strings.Repeat("a", 64)
	record, err := NewEventRecord(5, nil, CacheRequestObservationRecord{
		DigestVersion: 1,
		CacheKey:      "session-cache-key",
		Scope:         CacheScopeConversation,
		ChunkCount:    4,
		TerminalHash:  terminalHash,
	})
	if err != nil {
		t.Fatalf("create cache request observation: %v", err)
	}

	line, err := encodeEventRecordV1(record)
	if err != nil {
		t.Fatalf("encode cache request observation: %v", err)
	}
	decoded, err := decodeEventRecordV1(line)
	if err != nil {
		t.Fatalf("decode cache request observation: %v", err)
	}
	observation, ok := mustEventRecordPayload(decoded).(CacheRequestObservationRecord)
	if !ok {
		t.Fatalf("payload type = %T, want CacheRequestObservationRecord", mustEventRecordPayload(decoded))
	}
	if observation.CacheKey != "session-cache-key" || observation.Scope != CacheScopeConversation ||
		observation.ChunkCount != 4 || observation.TerminalHash != terminalHash {
		t.Fatalf("observation = %#v", observation)
	}
}

func TestEventLogV1CacheResponseObservationRoundTrip(t *testing.T) {
	cachedInputTokens := 1_024
	terminalHash := strings.Repeat("b", 64)
	record, err := NewEventRecord(6, nil, CacheResponseObservationRecord{
		DigestVersion:     1,
		CacheKey:          "session-cache-key",
		Scope:             CacheScopeConversation,
		ChunkCount:        4,
		TerminalHash:      terminalHash,
		CachedInputTokens: &cachedInputTokens,
	})
	if err != nil {
		t.Fatalf("create cache response observation: %v", err)
	}

	line, err := encodeEventRecordV1(record)
	if err != nil {
		t.Fatalf("encode cache response observation: %v", err)
	}
	decoded, err := decodeEventRecordV1(line)
	if err != nil {
		t.Fatalf("decode cache response observation: %v", err)
	}
	observation, ok := mustEventRecordPayload(decoded).(CacheResponseObservationRecord)
	if !ok {
		t.Fatalf("payload type = %T, want CacheResponseObservationRecord", mustEventRecordPayload(decoded))
	}
	if observation.CachedInputTokens == nil || *observation.CachedInputTokens != cachedInputTokens {
		t.Fatalf("observation = %#v", observation)
	}
}

func TestEventLogV1CacheWarningRecordRoundTrip(t *testing.T) {
	cacheKey := "session-cache-key"
	lostInputTokens := 2_048
	record, err := NewEventRecord(7, nil, CacheWarningRecord{
		Scope:           CacheScopeConversation,
		Reason:          CacheWarningReasonReuseDropped,
		CacheKey:        &cacheKey,
		LostInputTokens: &lostInputTokens,
	})
	if err != nil {
		t.Fatalf("create cache warning record: %v", err)
	}

	line, err := encodeEventRecordV1(record)
	if err != nil {
		t.Fatalf("encode cache warning record: %v", err)
	}
	decoded, err := decodeEventRecordV1(line)
	if err != nil {
		t.Fatalf("decode cache warning record: %v", err)
	}
	warning, ok := mustEventRecordPayload(decoded).(CacheWarningRecord)
	if !ok {
		t.Fatalf("payload type = %T, want CacheWarningRecord", mustEventRecordPayload(decoded))
	}
	if warning.CacheKey == nil || *warning.CacheKey != cacheKey ||
		warning.LostInputTokens == nil || *warning.LostInputTokens != lostInputTokens ||
		warning.Reason != CacheWarningReasonReuseDropped {
		t.Fatalf("warning = %#v", warning)
	}
}

func TestEventLogV1CacheWarningOmitsUnknownTokenLoss(t *testing.T) {
	record, err := NewEventRecord(1, nil, CacheWarningRecord{
		Scope:  CacheScopeConversation,
		Reason: CacheWarningReasonCompaction,
	})
	if err != nil {
		t.Fatalf("create cache warning record: %v", err)
	}

	line, err := encodeEventRecordV1(record)
	if err != nil {
		t.Fatalf("encode cache warning record: %v", err)
	}
	envelope := decodeJSONObject(t, line)
	payload := decodeJSONObject(t, envelope["payload"])
	if _, ok := payload["lost_input_tokens"]; ok {
		t.Fatalf("encoded absent token loss: %s", line)
	}
}

func TestEventLogV1RejectsInvalidRecordContracts(t *testing.T) {
	terminalHash := strings.Repeat("c", 64)
	invalidJSON := json.RawMessage(`{"broken"`)
	negativeTokens := -1
	tests := []struct {
		name    string
		seq     int64
		stepID  *string
		payload EventRecordPayload
	}{
		{name: "non-positive sequence", seq: 0, payload: MessageRecord{Role: MessageRoleUser}},
		{name: "blank step identity", seq: 1, stepID: stringPointer("  "), payload: MessageRecord{Role: MessageRoleUser}},
		{name: "unknown message role", seq: 1, payload: MessageRecord{Role: "operator"}},
		{name: "unknown message type", seq: 1, payload: MessageRecord{
			Role: MessageRoleDeveloper, MessageType: messageTypePointer("future"),
		}},
		{name: "unknown message phase", seq: 1, payload: MessageRecord{
			Role: MessageRoleAssistant, Phase: messagePhasePointer("draft"),
		}},
		{name: "tool call with blank custom input", seq: 1, payload: MessageRecord{
			Role: MessageRoleAssistant,
			ToolCalls: []MessageToolCallRecord{{
				CallID: "call", Name: "patch", Kind: ToolCallKindCustom, Input: json.RawMessage(`{}`),
				CustomInput: stringPointer(" "),
			}},
		}},
		{name: "reasoning item without encrypted content", seq: 1, payload: MessageRecord{
			Role:           MessageRoleAssistant,
			ReasoningItems: []MessageReasoningRecord{{ID: "reasoning"}},
		}},
		{name: "malformed tool output", seq: 1, payload: ToolCompletionRecord{
			CallID: "call", Name: "shell", OutputKind: ToolOutputKindFunction, Output: invalidJSON,
		}},
		{name: "unknown local visibility", seq: 1, payload: LocalEntryRecord{Visibility: "wide", Role: "error", Text: stringPointer("failure")}},
		{name: "unknown compaction engine", seq: 1, payload: HistoryReplacementRecord{Engine: "reviewer", Mode: CompactionModeAuto}},
		{name: "unknown compaction mode", seq: 1, payload: HistoryReplacementRecord{Engine: "local", Mode: "scheduled"}},
		{name: "unknown cache digest", seq: 1, payload: CacheRequestObservationRecord{
			DigestVersion: 2,
			CacheKey:      "cache",
			Scope:         CacheScopeConversation,
			ChunkCount:    1,
			TerminalHash:  terminalHash,
		}},
		{name: "invalid cache terminal hash", seq: 1, payload: CacheRequestObservationRecord{
			DigestVersion: 1,
			CacheKey:      "cache",
			Scope:         CacheScopeConversation,
			ChunkCount:    1,
			TerminalHash:  "not-a-sha256-digest",
		}},
		{name: "missing cache chunks", seq: 1, payload: CacheRequestObservationRecord{
			DigestVersion: 1,
			CacheKey:      "cache",
			Scope:         CacheScopeConversation,
			TerminalHash:  terminalHash,
		}},
		{name: "negative cached tokens", seq: 1, payload: CacheResponseObservationRecord{
			DigestVersion:     1,
			CacheKey:          "cache",
			Scope:             CacheScopeConversation,
			ChunkCount:        1,
			TerminalHash:      terminalHash,
			CachedInputTokens: &negativeTokens,
		}},
		{name: "unknown cache warning reason", seq: 1, payload: CacheWarningRecord{
			Scope:           CacheScopeConversation,
			Reason:          "evicted",
			LostInputTokens: intPointer(1),
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewEventRecord(test.seq, test.stepID, test.payload); err == nil {
				t.Fatal("expected contract validation error")
			}
		})
	}
}

func TestEventLogV1IgnoresAndDropsUnknownFields(t *testing.T) {
	line := []byte(`{
		"seq":1,
		"kind":"message",
		"future_envelope_fact":true,
		"payload":{
			"role":"user",
			"content":"hello",
			"future_payload_fact":{"nested":true}
		}
	}`)

	record, err := decodeEventRecordV1(line)
	if err != nil {
		t.Fatalf("decode record with unknown fields: %v", err)
	}
	encoded, err := encodeEventRecordV1(record)
	if err != nil {
		t.Fatalf("re-encode record: %v", err)
	}
	envelope := decodeJSONObject(t, encoded)
	if _, ok := envelope["future_envelope_fact"]; ok {
		t.Fatalf("unknown fields survived canonical encoding: %s", encoded)
	}
	payload := decodeJSONObject(t, envelope["payload"])
	if _, ok := payload["future_payload_fact"]; ok {
		t.Fatalf("unknown payload fields survived canonical encoding: %s", encoded)
	}
}

func TestEventLogV1RejectsUnknownEventKind(t *testing.T) {
	line := []byte(`{"seq":1,"kind":"future_kind","payload":{}}`)
	if _, err := decodeEventRecordV1(line); err == nil {
		t.Fatal("expected unknown event kind error")
	}
}

func TestEventLogV1RejectsMissingRequiredBoolean(t *testing.T) {
	line := []byte(`{
		"seq":1,
		"kind":"tool_completed",
		"payload":{"call_id":"call-1","name":"shell","output":{"exit_code":0}}
	}`)
	if _, err := decodeEventRecordV1(line); err == nil {
		t.Fatal("expected missing is_error validation error")
	}
}

func TestEventLogV1GoldenFixture(t *testing.T) {
	file, err := os.Open("testdata/events-v1.jsonl")
	if err != nil {
		t.Fatalf("open golden fixture: %v", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			t.Errorf("close golden fixture: %v", closeErr)
		}
	}()

	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		t.Fatalf("read golden header: %v", scanner.Err())
	}
	if _, err := decodeEventLogHeader(scanner.Bytes()); err != nil {
		t.Fatalf("decode golden header: %v", err)
	}

	wantKinds := []EventKind{
		EventKindMessage,
		EventKindToolCompletion,
		EventKindLocalEntry,
		EventKindHistoryReplace,
		EventKindCacheRequest,
		EventKindCacheResponse,
		EventKindCacheWarning,
	}
	for index, wantKind := range wantKinds {
		if !scanner.Scan() {
			t.Fatalf("read golden event %d: %v", index+1, scanner.Err())
		}
		record, err := decodeEventRecordV1(scanner.Bytes())
		if err != nil {
			t.Fatalf("decode golden event %d: %v", index+1, err)
		}
		if record.Seq() != int64(index+1) || mustEventRecordKind(record) != wantKind {
			t.Fatalf("golden event %d = seq %d kind %q, want seq %d kind %q",
				index+1, record.Seq(), mustEventRecordKind(record), index+1, wantKind)
		}
	}
	if scanner.Scan() {
		t.Fatalf("unexpected extra golden line: %s", scanner.Bytes())
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan golden fixture: %v", err)
	}
}

func stringPointer(value string) *string {
	return &value
}

func intPointer(value int) *int {
	return &value
}

func pointerTo[T any](value T) *T {
	return &value
}

func messageTypePointer(value MessageType) *MessageType {
	return &value
}

func messagePhasePointer(value MessagePhase) *MessagePhase {
	return &value
}

func providerItemLinkKindPointer(value ProviderItemLinkKind) *ProviderItemLinkKind {
	return &value
}

func decodeJSONObject(t *testing.T, encoded []byte) map[string]json.RawMessage {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatalf("decode JSON object: %v", err)
	}
	return object
}
