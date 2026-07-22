package runtime

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"

	"github.com/google/uuid"
)

const chatStoreTestStepID = "11111111-1111-4111-8111-111111111111"

func TestChatStoreProviderHistoryUsesLatestCompactionCheckpoint(t *testing.T) {
	s := newChatStore()
	s.appendMessage(chatStoreTestStepID, llm.Message{Role: llm.RoleUser, Content: textutil.Value("before")})
	s.replaceHistory(chatStoreTestStepID, []llm.ResponseItem{{
		Type:    llm.ResponseItemTypeMessage,
		Role:    textutil.Value(llm.RoleUser),
		Content: textutil.Value("summary-1"),
	}})
	s.appendMessage(chatStoreTestStepID, llm.Message{Role: llm.RoleUser, Content: textutil.Value("between")})

	s.replaceHistory(chatStoreTestStepID, []llm.ResponseItem{
		{Type: llm.ResponseItemTypeMessage, Role: textutil.Value(llm.RoleDeveloper), Content: textutil.Value("context")},
		{Type: llm.ResponseItemTypeMessage, Role: textutil.Value(llm.RoleUser), Content: textutil.Value("summary-2")},
	})
	s.appendMessage(chatStoreTestStepID, llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("after")})

	items := s.snapshotItems()
	if len(items) != 3 {
		t.Fatalf("provider items = %+v, want latest checkpoint plus active tail", items)
	}
	if !textutil.EqualOptional(items[0].Role, textutil.Value(llm.RoleDeveloper)) || !textutil.EqualOptional(items[0].Content, textutil.Value("context")) ||
		!textutil.EqualOptional(items[1].Role, textutil.Value(llm.RoleUser)) || !textutil.EqualOptional(items[1].Content, textutil.Value("summary-2")) ||
		!textutil.EqualOptional(items[2].Role, textutil.Value(llm.RoleAssistant)) || !textutil.EqualOptional(items[2].Content, textutil.Value("after")) {
		t.Fatalf("provider items = %+v, want only latest checkpoint plus active tail", items)
	}
}

func TestChatStoreCompactionPrunesWorkingStateAndPreservesCommittedCount(t *testing.T) {
	s := newChatStore()
	for i := 0; i < 3; i++ {
		callID := fmt.Sprintf("call-%d", i)
		s.appendMessage(chatStoreTestStepID, llm.Message{
			Role: llm.RoleAssistant,
			ToolCalls: []llm.ToolCall{{
				ID:    callID,
				Name:  string(toolspec.ToolExecCommand),
				Input: json.RawMessage(`{"command":"pwd"}`),
			}},
		})
		s.recordToolCompletionWithProviderItems(tools.Result{
			CallID: callID,
			Name:   toolspec.ToolExecCommand,
			Output: json.RawMessage(`{"output":"/tmp"}`),
		}, nil)
		s.appendLocalEntryRecord(ChatEntry{
			Visibility: transcript.EntryVisibilityAuto,
			Role:       "system",
			Text:       "note",
		}, nil)
	}
	committedBeforeCompaction := s.committedEntryCount()

	s.replaceHistory(chatStoreTestStepID, []llm.ResponseItem{{
		Type:        llm.ResponseItemTypeMessage,
		Role:        textutil.Value(llm.RoleUser),
		MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
		Content:     textutil.Value("summary"),
	}})
	s.appendMessage(chatStoreTestStepID, llm.Message{Role: llm.RoleUser, Content: textutil.Value("after")})

	if len(s.messageRecords) != 1 || len(s.local) != 1 {
		t.Fatalf("retained cache state after compaction: messages=%d local=%d, want one active message and one projected checkpoint row", len(s.messageRecords), len(s.local))
	}
	if len(s.toolCompletions) != 0 ||
		len(s.toolCompletionProviderItems) != 0 ||
		len(s.assistantToolCalls) != 0 ||
		len(s.materializedToolResults) != 0 ||
		len(s.synthesizedToolResults) != 0 {
		t.Fatalf(
			"retained tool working state after compaction: completions=%d provider_items=%d calls=%d materialized=%d synthesized=%d",
			len(s.toolCompletions),
			len(s.toolCompletionProviderItems),
			len(s.assistantToolCalls),
			len(s.materializedToolResults),
			len(s.synthesizedToolResults),
		)
	}
	if got := s.committedEntryCount(); got != committedBeforeCompaction+2 {
		t.Fatalf("committed entry count = %d, want cumulative count %d", got, committedBeforeCompaction+2)
	}
}

func TestChatStoreProviderItemsOrderMixedMaterializedAndPendingToolOutputs(t *testing.T) {
	s := newChatStore()
	s.appendMessage(chatStoreTestStepID, llm.Message{
		Role: llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{
			{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)},
			{ID: "call-2", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"ls"}`)},
		},
	})
	s.recordToolCompletionWithProviderItems(tools.Result{
		CallID: "call-1",
		Name:   toolspec.ToolExecCommand,
		Output: json.RawMessage(`{"output":"/tmp"}`),
	}, nil)
	s.recordToolCompletionWithProviderItems(tools.Result{
		CallID: "call-2",
		Name:   toolspec.ToolExecCommand,
		Output: json.RawMessage(`{"output":"a.txt"}`),
	}, nil)
	if got := s.committedEntryCount(); got != 4 {
		t.Fatalf("committed entry count after two calls and completions = %d, want 4", got)
	}

	s.appendMessage(chatStoreTestStepID, llm.Message{
		Role:       llm.RoleTool,
		ToolCallID: textutil.Value("call-1"),
		Name:       textutil.Value(string(toolspec.ToolExecCommand)),
		Content:    textutil.Value(`{"output":"/tmp"}`),
	})
	if got := s.committedEntryCount(); got != 4 {
		t.Fatalf("materialized tool result double-counted: committed entry count = %d, want 4", got)
	}

	items := s.snapshotItems()
	want := []struct {
		itemType llm.ResponseItemType
		callID   string
	}{
		{llm.ResponseItemTypeFunctionCall, "call-1"},
		{llm.ResponseItemTypeFunctionCall, "call-2"},
		{llm.ResponseItemTypeFunctionCallOutput, "call-1"},
		{llm.ResponseItemTypeFunctionCallOutput, "call-2"},
	}
	if len(items) != len(want) {
		t.Fatalf("provider items = %+v, want %d ordered call/output items", items, len(want))
	}
	for i, expected := range want {
		if items[i].Type != expected.itemType || !textutil.EqualOptional(items[i].CallID, textutil.Value(expected.callID)) {
			t.Fatalf("provider item[%d] = %+v, want type=%q call_id=%q", i, items[i], expected.itemType, expected.callID)
		}
	}
}

func TestChatStoreFinalizedStreamIdentityUsesActiveSegmentCoordinates(t *testing.T) {
	s := newChatStore()
	s.appendMessage(chatStoreTestStepID, llm.Message{
		Role: llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{
			ID:   "call-1",
			Name: string(toolspec.ToolExecCommand),
		}},
	})
	s.appendMessage(chatStoreTestStepID, llm.Message{
		Role:    llm.RoleAssistant,
		Content: textutil.Value("streamed before compaction"),
		Phase:   textutil.Value(llm.MessagePhaseFinal),
	})
	preCompactionStreamID := uuid.New()
	s.recordAssistantStreamFinalization(1, &preCompactionStreamID)

	beforeCompaction := s.deliverySnapshot()
	if len(beforeCompaction.Rows) != 1 ||
		beforeCompaction.Rows[0].Assistant == nil ||
		beforeCompaction.Rows[0].Assistant.StreamID == nil ||
		*beforeCompaction.Rows[0].Assistant.StreamID != preCompactionStreamID {
		t.Fatalf("pre-compaction delivery rows = %+v, want finalized assistant stream identity after tool row", beforeCompaction.Rows)
	}

	activeSegmentStart := s.committedEntryCount()
	s.replaceHistoryAtCommittedEntryStart(chatStoreTestStepID, nil, &activeSegmentStart)
	if len(s.assistantStreamIDsByEntry) != 0 {
		t.Fatalf("pre-compaction stream identities survived active-segment trim: %+v", s.assistantStreamIDsByEntry)
	}
	s.appendMessage(chatStoreTestStepID, llm.Message{
		Role:    llm.RoleAssistant,
		Content: textutil.Value("streamed after compaction"),
		Phase:   textutil.Value(llm.MessagePhaseFinal),
	})
	postCompactionStreamID := uuid.New()
	s.recordAssistantStreamFinalization(activeSegmentStart, &postCompactionStreamID)

	afterCompaction := s.deliverySnapshot()
	if len(afterCompaction.Rows) != 1 ||
		afterCompaction.Rows[0].Assistant == nil ||
		afterCompaction.Rows[0].Assistant.StreamID == nil ||
		*afterCompaction.Rows[0].Assistant.StreamID != postCompactionStreamID {
		t.Fatalf("post-compaction delivery rows = %+v, want stream identity at active-segment origin", afterCompaction.Rows)
	}
}

func TestChatStoreDeliveryIncludesCompleteActiveSegment(t *testing.T) {
	s := newChatStore()
	const count = 650
	for i := 0; i < count; i++ {
		s.appendMessage(chatStoreTestStepID, llm.Message{
			Role:    llm.RoleUser,
			Content: textutil.Value(fmt.Sprintf("message-%03d", i)),
		})
	}

	rows := s.deliverySnapshot().Rows
	if len(rows) != count {
		t.Fatalf("delivery rows = %d, want complete active segment %d", len(rows), count)
	}
	if rows[0].User == nil || rows[0].User.Text != "message-000" {
		t.Fatalf("first delivery row = %+v, want oldest active row", rows[0])
	}
	if last := rows[len(rows)-1]; last.User == nil || last.User.Text != "message-649" {
		t.Fatalf("last delivery row = %+v, want newest active row", last)
	}
}

func TestChatStoreDeliveryPreservesTypedAndLegacyLocalRowsAfterCompaction(t *testing.T) {
	s := newChatStore()
	s.appendMessage(chatStoreTestStepID, llm.Message{Role: llm.RoleUser, Content: textutil.Value("before")})
	activeSegmentStart := s.committedEntryCount()
	s.replaceHistoryAtCommittedEntryStart(chatStoreTestStepID, nil, &activeSegmentStart)

	typed := ChatEntry{
		Visibility:    transcript.EntryVisibilityAuto,
		Role:          "reviewer_status",
		Text:          "Supervisor ran: 1 suggestion.",
		CondensedText: "1 suggestion",
		NoticeID:      "0d4ad314-f5f9-4b32-a13d-4b8c1d9a2e61",
	}
	s.appendLocalEntryRecord(typed, nil)
	s.appendLocalEntryRecord(ChatEntry{Text: "ancient untyped row"}, nil)

	rows := s.deliverySnapshot().Rows
	if len(rows) != 2 {
		t.Fatalf("delivery rows = %+v, want typed and legacy notices", rows)
	}
	liveTyped := TranscriptCommittedRowFactsFromEvent(Event{Kind: EventLocalEntryAdded, LocalEntry: &typed})
	if len(liveTyped) != 1 || !reflect.DeepEqual(rows[0], liveTyped[0]) {
		t.Fatalf("hydrated typed row = %+v, live row = %+v", rows[0], liveTyped)
	}
	if rows[1].Notice == nil ||
		rows[1].Notice.Reason != "legacy_untyped_notice" ||
		rows[1].Notice.LegacyText == nil ||
		*rows[1].Notice.LegacyText != "ancient untyped row" {
		t.Fatalf("legacy row = %+v, want fossilized untyped notice", rows[1])
	}
}

func TestTranscriptFactsNormalizeLegacyProjectedLocalEntryVisibility(t *testing.T) {
	for _, test := range []struct {
		legacy transcript.EntryVisibility
		want   transcript.EntryVisibility
	}{
		{legacy: transcript.EntryVisibility("all"), want: transcript.EntryVisibilityOngoing},
		{legacy: transcript.EntryVisibility("verbose"), want: transcript.EntryVisibilityDetail},
	} {
		facts := TranscriptCommittedRowFactsFromEvent(Event{
			Kind:                EventLocalEntryAdded,
			LocalEntryProjected: true,
			LocalEntry: &ChatEntry{
				Visibility: test.legacy,
				Role:       "system",
				Text:       "legacy visibility",
			},
		})
		if len(facts) != 1 || facts[0].Visibility != test.want {
			t.Fatalf("legacy %q facts = %+v, want one row with visibility %q", test.legacy, facts, test.want)
		}
	}
}

func TestTranscriptCacheWarningFactsPreserveAbsentTokenLoss(t *testing.T) {
	facts := TranscriptCommittedRowFactsFromEvent(Event{
		Kind: EventCacheWarning,
		CacheWarning: &transcript.CacheWarning{
			Scope:  transcript.CacheWarningScopeConversation,
			Reason: transcript.CacheWarningReasonCompaction,
		},
		CacheWarningVisibility: transcript.EntryVisibilityOngoing,
	})
	if len(facts) != 1 || facts[0].Notice == nil || facts[0].Notice.CacheWarning == nil {
		t.Fatalf("cache-warning facts = %+v, want one typed notice", facts)
	}
	if facts[0].Notice.CacheWarning.LostInputTokens != nil {
		t.Fatalf("cache-warning absent token loss = %v, want nil", *facts[0].Notice.CacheWarning.LostInputTokens)
	}
}

func TestChatStoreDeliveryMatchesLiveProjectedCompactionRows(t *testing.T) {
	s := newChatStore()
	activeSegmentStart := 7
	items := llm.ItemsFromMessages([]llm.Message{
		{Role: llm.RoleUser, Content: textutil.Value("user text")},
		{
			Role:    llm.RoleAssistant,
			Content: textutil.Value("assistant text"),
			Phase:   textutil.Value(llm.MessagePhaseFinal),
			ToolCalls: []llm.ToolCall{{
				ID:   "call-1",
				Name: string(toolspec.ToolExecCommand),
			}},
		},
		{
			Role:       llm.RoleTool,
			ToolCallID: textutil.Value("call-1"),
			Name:       textutil.Value(string(toolspec.ToolExecCommand)),
			Content:    textutil.Value(`{"output":"done"}`),
		},
	})
	s.replaceHistoryAtCommittedEntryStart(chatStoreTestStepID, items, &activeSegmentStart)

	hydrated := s.deliverySnapshot().Rows
	live := make([]TranscriptCommittedRowFact, 0, len(hydrated))
	for _, entry := range transcriptEntriesFromHistoryReplacement(llm.PrepareOpenAIInputItems(items)) {
		entry.StepID = chatStoreTestStepID
		live = append(live, TranscriptCommittedRowFactsFromEvent(Event{
			Kind:                EventLocalEntryAdded,
			LocalEntry:          &entry,
			LocalEntryProjected: true,
		})...)
	}
	if !reflect.DeepEqual(hydrated, live) {
		t.Fatalf("hydrated rows = %+v, live rows = %+v, want identical projections", hydrated, live)
	}
}
