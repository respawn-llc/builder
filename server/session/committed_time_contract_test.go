package session

import (
	"testing"
	"time"
)

func TestEventPayloadCommittedTimeEligibility(t *testing.T) {
	content := "visible"
	compactionSummary := MessageTypeCompactionSummary
	nonSummary := MessageTypeAgentsMD
	user := MessageRoleUser
	assistant := MessageRoleAssistant
	system := MessageRoleSystem
	developer := MessageRoleDeveloper
	tool := MessageRoleTool

	tests := []struct {
		name    string
		payload EventRecordPayload
		want    bool
	}{
		{
			name:    "untyped visible user",
			payload: MessageRecord{Role: MessageRoleUser, Content: &content},
			want:    true,
		},
		{
			name:    "typed non-summary user",
			payload: MessageRecord{Role: user, MessageType: &nonSummary, Content: &content},
			want:    true,
		},
		{
			name:    "typed compaction-summary user",
			payload: MessageRecord{Role: user, MessageType: &compactionSummary, Content: &content},
			want:    false,
		},
		{
			name:    "assistant with content",
			payload: MessageRecord{Role: assistant, Content: &content},
			want:    true,
		},
		{
			name: "assistant with only tool calls",
			payload: MessageRecord{Role: assistant, ToolCalls: []MessageToolCallRecord{{
				CallID: "call-1", Name: "exec_command", Kind: ToolCallKindFunction,
				Input: []byte(`{}`),
			}}},
			want: false,
		},
		{
			name:    "system",
			payload: MessageRecord{Role: system, Content: &content},
			want:    false,
		},
		{
			name:    "developer",
			payload: MessageRecord{Role: developer, Content: &content},
			want:    false,
		},
		{
			name:    "tool",
			payload: MessageRecord{Role: tool, Content: &content},
			want:    false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := eventPayloadEligibleForCommittedTime(test.payload)
			if err != nil {
				t.Fatalf("eventPayloadEligibleForCommittedTime: %v", err)
			}
			if got != test.want {
				t.Fatalf("eligible = %t, want %t", got, test.want)
			}
		})
	}
}

func TestHistoryReplacementCommittedTimeEligibility(t *testing.T) {
	content := "visible"
	summary := MessageTypeCompactionSummary
	nonSummary := MessageTypeAgentsMD
	user := MessageRoleUser
	assistant := MessageRoleAssistant

	message := func(role *MessageRole, messageType *MessageType) ProviderHistoryItem {
		return ProviderHistoryItem{
			Type:        ProviderHistoryItemTypeMessage,
			Role:        role,
			MessageType: messageType,
			Content:     &content,
			Raw:         []byte(`{"type":"message"}`),
		}
	}
	tests := []struct {
		name  string
		items []ProviderHistoryItem
		want  bool
	}{
		{
			name:  "explicit user typed non-summary",
			items: []ProviderHistoryItem{message(&user, &nonSummary)},
			want:  true,
		},
		{
			name:  "explicit user typed compaction summary",
			items: []ProviderHistoryItem{message(&user, &summary)},
			want:  false,
		},
		{
			name:  "explicit user untyped preserved",
			items: []ProviderHistoryItem{message(&user, nil)},
			want:  false,
		},
		{
			name:  "role absent untyped preserved",
			items: []ProviderHistoryItem{message(nil, nil)},
			want:  false,
		},
		{
			name:  "role absent typed non-summary",
			items: []ProviderHistoryItem{message(nil, &nonSummary)},
			want:  true,
		},
		{
			name:  "assistant content",
			items: []ProviderHistoryItem{message(&assistant, nil)},
			want:  true,
		},
		{
			name: "non-message item carrying user-like facts",
			items: []ProviderHistoryItem{{
				Type:    ProviderHistoryItemTypeOther,
				Role:    &user,
				Content: &content,
				Raw:     []byte(`{"type":"other"}`),
			}},
			want: false,
		},
		{
			name: "only tools and notices",
			items: []ProviderHistoryItem{
				{Type: ProviderHistoryItemTypeFunctionCall, Raw: []byte(`{"type":"function_call"}`)},
				{Type: ProviderHistoryItemTypeOther, Raw: []byte(`{"type":"other"}`)},
			},
			want: false,
		},
		{name: "empty items", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := eventPayloadEligibleForCommittedTime(HistoryReplacementRecord{
				Engine: "local",
				Mode:   CompactionModeAuto,
				Items:  test.items,
			})
			if err != nil {
				t.Fatalf("eventPayloadEligibleForCommittedTime: %v", err)
			}
			if got != test.want {
				t.Fatalf("eligible = %t, want %t", got, test.want)
			}
		})
	}
}

func TestEventRecordCommittedTimeEnvelopeContract(t *testing.T) {
	content := "message"
	present := int64(0)
	record, err := newEventRecord(1, nil, MessageRecord{
		Role:    MessageRoleUser,
		Content: &content,
	}, &present)
	if err != nil {
		t.Fatalf("newEventRecord: %v", err)
	}
	line, err := encodeEventRecordV1(record)
	if err != nil {
		t.Fatalf("encode event record: %v", err)
	}
	if string(line) != `{"seq":1,"kind":"message","committed_at_unix_ms":0,"payload":{"role":"user","content":"message"}}` {
		t.Fatalf("encoded line = %s", line)
	}
	decoded, err := decodeEventRecordV1(line)
	if err != nil {
		t.Fatalf("decode event record: %v", err)
	}
	if got := decoded.CommittedAtUnixMs(); got == nil || *got != present {
		t.Fatalf("decoded committed time = %v, want %d", got, present)
	}

	historical, err := decodeEventRecordV1([]byte(
		`{"seq":1,"kind":"message","payload":{"role":"user","content":"old"}}`,
	))
	if err != nil {
		t.Fatalf("decode historical event record: %v", err)
	}
	historicalLine, err := encodeEventRecordV1(historical)
	if err != nil {
		t.Fatalf("re-encode historical event record: %v", err)
	}
	if string(historicalLine) != `{"seq":1,"kind":"message","payload":{"role":"user","content":"old"}}` {
		t.Fatalf("historical encoded line = %s", historicalLine)
	}
}

func TestEventRecordRejectsExplicitNullCommittedTime(t *testing.T) {
	_, err := decodeEventRecordV1([]byte(
		`{"seq":1,"kind":"message","committed_at_unix_ms":null,"payload":{"role":"user","content":"old"}}`,
	))
	if err == nil {
		t.Fatal("explicit null committed time was accepted")
	}
}

func TestEventRecordRejectsMisplacedCommittedTime(t *testing.T) {
	content := "notice"
	committedAt := int64(1)
	if _, err := newEventRecord(1, nil, MessageRecord{
		Role:        MessageRoleUser,
		MessageType: messageTypePointer(MessageTypeCompactionSummary),
		Content:     &content,
	}, &committedAt); err == nil {
		t.Fatal("compaction summary accepted committed time")
	}
}

func TestAppendBatchSamplesOneCommittedTimeForEligibleRecords(t *testing.T) {
	now := time.UnixMilli(1_723_456_789_012).UTC()
	clockCalls := 0
	store, err := Create(
		t.TempDir(),
		"workspace",
		t.TempDir(),
		testSessionCategory,
		append(sessionTestPersistence.options(), WithClock(func() time.Time {
			clockCalls++
			return now
		}))...,
	)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("ensure durable: %v", err)
	}
	log := mustMaterializeSessionTestEventLog(t, store)
	initialClockCalls := clockCalls
	content := "visible"
	records, receipt, err := log.AppendRecordsAtomic(nil, []EventRecordPayload{
		MessageRecord{Role: MessageRoleUser, Content: &content},
		MessageRecord{Role: MessageRoleSystem, Content: &content},
		MessageRecord{Role: MessageRoleAssistant, Content: &content},
	})
	if err != nil {
		t.Fatalf("append records: %v", err)
	}
	if !receipt.Committed {
		t.Fatal("append was not committed")
	}
	if clockCalls != initialClockCalls+1 {
		t.Fatalf("clock calls = %d, want %d", clockCalls, initialClockCalls+1)
	}
	if len(records) != 3 {
		t.Fatalf("records = %d, want 3", len(records))
	}
	for _, index := range []int{0, 2} {
		got := records[index].CommittedAtUnixMs()
		if got == nil || *got != now.UnixMilli() {
			t.Fatalf("record %d committed time = %v, want %d", index, got, now.UnixMilli())
		}
	}
	if got := records[1].CommittedAtUnixMs(); got != nil {
		t.Fatalf("ineligible record committed time = %d, want absent", *got)
	}
}

func TestForkAndClonePreserveCommittedTimesAcrossReplayBoundaries(t *testing.T) {
	parent := newSessionTestStore(t)
	parentLog := mustMaterializeSessionTestEventLog(t, parent)
	_, _, err := parentLog.AppendRecord(nil, MessageRecord{
		Role:    MessageRoleUser,
		Content: stringPointer("first"),
	})
	if err != nil {
		t.Fatalf("append first message: %v", err)
	}
	if _, _, err := parentLog.AppendRecord(nil, LocalEntryRecord{
		Visibility: EntryVisibilityHidden,
		Role:       "hidden",
		Text:       stringPointer("historical"),
	}); err != nil {
		t.Fatalf("append historical entry: %v", err)
	}
	sourceReplacement, _, err := parentLog.AppendCompactionHistoryReplacement(nil, HistoryReplacementRecord{
		Engine: "local",
		Mode:   CompactionModeAuto,
		Items: []ProviderHistoryItem{{
			Type:    ProviderHistoryItemTypeMessage,
			Role:    pointerTo(MessageRoleUser),
			Content: stringPointer("replacement"),
			Raw:     []byte(`{"type":"message"}`),
		}},
	})
	if err != nil {
		t.Fatalf("append replacement: %v", err)
	}
	target, _, err := parentLog.AppendRecord(nil, MessageRecord{
		Role:    MessageRoleUser,
		Content: stringPointer("target"),
	})
	if err != nil {
		t.Fatalf("append target: %v", err)
	}

	child, _, err := ForkAtUserMessage(parentLog, target.Seq(), "fork", testSessionCategory)
	if err != nil {
		t.Fatalf("fork session: %v", err)
	}
	childRecords := collectEventsForCommittedTimeTest(t, child)
	parentRecords := collectEventsForCommittedTimeTest(t, parent)
	if len(childRecords) != 3 {
		t.Fatalf("fork records = %d, want 3", len(childRecords))
	}
	if !sameCommittedTime(childRecords[0], parentRecords[0]) {
		t.Fatalf("fork first timestamp changed: child=%v parent=%v", childRecords[0].CommittedAtUnixMs(), parentRecords[0].CommittedAtUnixMs())
	}
	if !sameCommittedTime(childRecords[2], sourceReplacement) {
		t.Fatalf("fork replacement timestamp changed: child=%v source=%v", childRecords[2].CommittedAtUnixMs(), sourceReplacement.CommittedAtUnixMs())
	}
	if childRecords[0].Seq() != 1 || childRecords[2].Seq() != 3 {
		t.Fatalf("fork replay child sequences = [%d %d], want [1 3]", childRecords[0].Seq(), childRecords[2].Seq())
	}

	clone, err := CloneSession(parentLog, "clone", testSessionCategory)
	if err != nil {
		t.Fatalf("clone session: %v", err)
	}
	cloneRecords := collectEventsForCommittedTimeTest(t, clone)
	if len(cloneRecords) != len(parentRecords) {
		t.Fatalf("clone records = %d, want %d", len(cloneRecords), len(parentRecords))
	}
	for index := range cloneRecords {
		if !sameCommittedTime(cloneRecords[index], parentRecords[index]) {
			t.Fatalf("clone record %d timestamp changed: clone=%v parent=%v", index, cloneRecords[index].CommittedAtUnixMs(), parentRecords[index].CommittedAtUnixMs())
		}
	}
}

func collectEventsForCommittedTimeTest(t *testing.T, store *Store) []EventRecord {
	t.Helper()
	log := mustMaterializeSessionTestEventLog(t, store)
	var records []EventRecord
	if err := log.WalkRecords(func(record EventRecord) error {
		records = append(records, record)
		return nil
	}); err != nil {
		t.Fatalf("walk records: %v", err)
	}
	return records
}

func sameCommittedTime(left, right EventRecord) bool {
	leftTime := left.CommittedAtUnixMs()
	rightTime := right.CommittedAtUnixMs()
	if leftTime == nil || rightTime == nil {
		return leftTime == nil && rightTime == nil
	}
	return *leftTime == *rightTime
}

func TestForkPreservesAbsentCommittedTimeOnEligibleHistoricalRecords(t *testing.T) {
	parent := newSessionTestStore(t)
	parentLog := mustMaterializeSessionTestEventLog(t, parent)
	historicalUser, err := decodeEventRecordV1([]byte(
		`{"seq":1,"kind":"message","payload":{"role":"user","content":"historical"}}`,
	))
	if err != nil {
		t.Fatalf("decode historical user: %v", err)
	}
	historicalReplacement, err := decodeEventRecordV1([]byte(
		`{"seq":2,"kind":"history_replaced","payload":{"engine":"local","mode":"auto","items":[{"type":"message","role":"user","message_type":"agents.md","content":"replayed","raw":{"type":"message"}}]}}`,
	))
	if err != nil {
		t.Fatalf("decode historical replacement: %v", err)
	}
	if _, err := parentLog.AppendReplayRecords([]EventRecord{historicalUser, historicalReplacement}); err != nil {
		t.Fatalf("append historical replay records: %v", err)
	}
	target, _, err := parentLog.AppendRecord(nil, MessageRecord{
		Role:    MessageRoleUser,
		Content: stringPointer("target"),
	})
	if err != nil {
		t.Fatalf("append target: %v", err)
	}
	child, _, err := ForkAtUserMessage(parentLog, target.Seq(), "fork", testSessionCategory)
	if err != nil {
		t.Fatalf("fork historical records: %v", err)
	}
	records := collectEventsForCommittedTimeTest(t, child)
	if len(records) != 2 {
		t.Fatalf("fork historical records = %d, want 2", len(records))
	}
	for index, record := range records {
		if got := record.CommittedAtUnixMs(); got != nil {
			t.Fatalf("fork historical record %d committed time = %d, want absent", index, *got)
		}
	}
}
