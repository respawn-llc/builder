package session

import (
	"testing"
	"time"

	"core/shared/transcript"
)

func TestCommittedTimeRangeAndEligibility(t *testing.T) {
	for _, value := range []int64{
		transcript.MinCommittedAtUnixMs, -1, 0, 1, transcript.MaxCommittedAtUnixMs,
	} {
		typed := transcript.CommittedAtUnixMs(value)
		if err := transcript.ValidateCommittedAtUnixMs(&typed); err != nil {
			t.Fatalf("valid committed time %d: %v", value, err)
		}
	}
	for _, value := range []int64{
		transcript.MinCommittedAtUnixMs - 1, transcript.MaxCommittedAtUnixMs + 1,
	} {
		typed := transcript.CommittedAtUnixMs(value)
		if err := transcript.ValidateCommittedAtUnixMs(&typed); err == nil {
			t.Fatalf("out-of-range committed time %d accepted", value)
		}
	}
	content := "visible"
	summary := MessageTypeCompactionSummary
	tests := []struct {
		name string
		p    EventRecordPayload
		want bool
	}{
		{"user", MessageRecord{Role: MessageRoleUser, Content: &content}, true},
		{"typed user", MessageRecord{Role: MessageRoleUser, MessageType: messageTypePointer(MessageTypeAgentsMD), Content: &content}, true},
		{"summary user", MessageRecord{Role: MessageRoleUser, MessageType: &summary, Content: &content}, false},
		{"assistant", MessageRecord{Role: MessageRoleAssistant, Content: &content}, true},
		{"assistant tools", MessageRecord{Role: MessageRoleAssistant, ToolCalls: []MessageToolCallRecord{{CallID: "c", Name: "shell", Kind: ToolCallKindFunction, Input: []byte(`{}`)}}}, false},
		{"system", MessageRecord{Role: MessageRoleSystem, Content: &content}, false},
		{"developer", MessageRecord{Role: MessageRoleDeveloper, Content: &content}, false},
		{"tool", MessageRecord{Role: MessageRoleTool, Content: &content}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := eventPayloadEligibleForCommittedTime(test.p)
			if err != nil || got != test.want {
				t.Fatalf("eligible=%t err=%v want=%t", got, err, test.want)
			}
		})
	}
	user := MessageRoleUser
	assistant := MessageRoleAssistant
	replacement := func(role *MessageRole, typ *MessageType) ProviderHistoryItem {
		return ProviderHistoryItem{Type: ProviderHistoryItemTypeMessage, Role: role, MessageType: typ, Content: &content, Raw: []byte(`{"type":"message"}`)}
	}
	for _, test := range []struct {
		name string
		item ProviderHistoryItem
		want bool
	}{
		{"replacement typed user", replacement(&user, messageTypePointer(MessageTypeAgentsMD)), true},
		{"replacement summary", replacement(&user, &summary), false},
		{"replacement untyped", replacement(&user, nil), false},
		{"replacement absent role", replacement(nil, nil), false},
		{"replacement absent role typed", replacement(nil, messageTypePointer(MessageTypeAgentsMD)), true},
		{"replacement assistant", replacement(&assistant, nil), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := eventPayloadEligibleForCommittedTime(HistoryReplacementRecord{Engine: "local", Mode: CompactionModeAuto, Items: []ProviderHistoryItem{test.item}})
			if err != nil || got != test.want {
				t.Fatalf("eligible=%t err=%v want=%t", got, err, test.want)
			}
		})
	}
}

func TestCommittedTimeEventEnvelopeAndNullRejection(t *testing.T) {
	content := "message"
	zero := transcript.CommittedAtUnixMs(0)
	record, err := newEventRecord(1, nil, MessageRecord{Role: MessageRoleUser, Content: &content}, &zero)
	if err != nil {
		t.Fatalf("create record: %v", err)
	}
	line, err := encodeEventRecordV1(record)
	if err != nil {
		t.Fatalf("encode record: %v", err)
	}
	decoded, err := decodeEventRecordV1(line)
	if err != nil || decoded.CommittedAtUnixMs() == nil || decoded.CommittedAtUnixMs().UnixMs() != 0 {
		t.Fatalf("decoded record time=%v err=%v", decoded.CommittedAtUnixMs(), err)
	}
	historical, err := decodeEventRecordV1([]byte(`{"seq":1,"kind":"message","payload":{"role":"user","content":"old"}}`))
	if err != nil {
		t.Fatalf("decode historical: %v", err)
	}
	if line, err := encodeEventRecordV1(historical); err != nil || string(line) != `{"seq":1,"kind":"message","payload":{"role":"user","content":"old"}}` {
		t.Fatalf("historical re-encode line=%s err=%v", line, err)
	}
	for _, field := range []string{"committed_at_unix_ms", "COMMITTED_AT_UNIX_MS"} {
		if _, err := decodeEventRecordV1([]byte(`{"seq":1,"kind":"message","` + field + `":null,"payload":{"role":"user","content":"old"}}`)); err == nil {
			t.Fatalf("null field %q accepted", field)
		}
	}
	one := transcript.CommittedAtUnixMs(1)
	if _, err := newEventRecord(1, nil, MessageRecord{Role: MessageRoleUser, MessageType: messageTypePointer(MessageTypeCompactionSummary), Content: &content}, &one); err == nil {
		t.Fatal("ineligible timestamp accepted")
	}
}

func TestCommittedTimeAtomicAppendSamplesClockOnce(t *testing.T) {
	now := time.UnixMilli(1_723_456_789_012).UTC()
	calls := 0
	options := append(sessionTestPersistence.options(), WithClock(func() time.Time { calls++; return now }))
	store, err := Create(t.TempDir(), "workspace", t.TempDir(), testSessionCategory, options...)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("durable store: %v", err)
	}
	log := mustMaterializeSessionTestEventLog(t, store)
	before := calls
	content := "visible"
	records, _, err := log.AppendRecordsAtomic(nil, []EventRecordPayload{
		MessageRecord{Role: MessageRoleUser, Content: &content},
		MessageRecord{Role: MessageRoleSystem, Content: &content},
		MessageRecord{Role: MessageRoleAssistant, Content: &content},
	})
	if err != nil || calls != before+1 {
		t.Fatalf("append err=%v clock calls=%d want=%d", err, calls, before+1)
	}
	for _, index := range []int{0, 2} {
		if records[index].CommittedAtUnixMs() == nil || records[index].CommittedAtUnixMs().UnixMs() != now.UnixMilli() {
			t.Fatalf("record %d timestamp=%v", index, records[index].CommittedAtUnixMs())
		}
	}
	if records[1].CommittedAtUnixMs() != nil {
		t.Fatalf("ineligible timestamp=%v", records[1].CommittedAtUnixMs())
	}
}

func TestReplayPreservesEligibleAbsentCommittedTime(t *testing.T) {
	parent := newSessionTestStore(t)
	log := mustMaterializeSessionTestEventLog(t, parent)
	user, err := decodeEventRecordV1([]byte(`{"seq":1,"kind":"message","payload":{"role":"user","content":"old"}}`))
	if err != nil {
		t.Fatalf("decode user: %v", err)
	}
	if _, err := log.AppendReplayRecords([]EventRecord{user}); err != nil {
		t.Fatalf("replay user: %v", err)
	}
	target, _, err := log.AppendRecord(nil, MessageRecord{Role: MessageRoleUser, Content: stringPointer("target")})
	if err != nil {
		t.Fatalf("append target: %v", err)
	}
	child, _, err := ForkAtUserMessage(log, target.Seq(), "fork", testSessionCategory)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	childLog := mustMaterializeSessionTestEventLog(t, child)
	var records []EventRecord
	if err := childLog.WalkRecords(func(record EventRecord) error { records = append(records, record); return nil }); err != nil {
		t.Fatalf("walk child: %v", err)
	}
	if len(records) != 1 || records[0].CommittedAtUnixMs() != nil {
		t.Fatalf("child replay records=%d time=%v", len(records), records[0].CommittedAtUnixMs())
	}
}

func TestReplayPreservesPresentTimeThroughForkCloneAndReplacementRebase(t *testing.T) {
	parent := newSessionTestStore(t)
	log := mustMaterializeSessionTestEventLog(t, parent)
	present := transcript.CommittedAtUnixMs(123)
	user, err := newEventRecord(1, nil, MessageRecord{Role: MessageRoleUser, Content: stringPointer("old")}, &present)
	if err != nil {
		t.Fatalf("create source user: %v", err)
	}
	replacement, err := newEventRecord(2, nil, HistoryReplacementRecord{
		Engine: "local",
		Mode:   CompactionModeAuto,
		Items: []ProviderHistoryItem{{
			Type:    ProviderHistoryItemTypeMessage,
			Role:    messageRolePointer(MessageRoleAssistant),
			Content: stringPointer("replacement"),
			Raw:     []byte(`{"type":"message","role":"assistant","content":"replacement"}`),
		}},
	}, &present)
	if err != nil {
		t.Fatalf("create source replacement: %v", err)
	}
	if _, err := log.AppendReplayRecords([]EventRecord{user, replacement}); err != nil {
		t.Fatalf("replay source records: %v", err)
	}
	target, _, err := log.AppendRecord(nil, MessageRecord{Role: MessageRoleUser, Content: stringPointer("target")})
	if err != nil {
		t.Fatalf("append target: %v", err)
	}
	forked, _, err := ForkAtUserMessage(log, target.Seq(), "fork", testSessionCategory)
	if err != nil {
		t.Fatalf("fork source records: %v", err)
	}
	cloned, err := CloneSession(log, "clone", testSessionCategory)
	if err != nil {
		t.Fatalf("clone source records: %v", err)
	}
	for name, store := range map[string]*Store{"fork": forked, "clone": cloned} {
		var records []EventRecord
		if err := mustMaterializeSessionTestEventLog(t, store).WalkRecords(func(record EventRecord) error {
			records = append(records, record)
			return nil
		}); err != nil {
			t.Fatalf("%s walk: %v", name, err)
		}
		if len(records) < 2 {
			t.Fatalf("%s records=%d", name, len(records))
		}
		for _, record := range records[:2] {
			if record.CommittedAtUnixMs() == nil || record.CommittedAtUnixMs().UnixMs() != present.UnixMs() {
				t.Fatalf("%s record timestamp=%v want=%d", name, record.CommittedAtUnixMs(), present.UnixMs())
			}
		}
	}
}
func messageRolePointer(value MessageRole) *MessageRole {
	return &value
}
