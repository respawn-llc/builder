package session

import (
	"testing"
	"time"

	"core/shared/rollbacktarget"
	"core/shared/transcript"
)

func TestCommittedTimeRangeAndEligibility(t *testing.T) {
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
	for _, test := range []struct {
		name  string
		items []ProviderHistoryItem
	}{
		{"replacement user-like non-message", []ProviderHistoryItem{{Type: ProviderHistoryItemTypeOther, Role: &user, Content: &content}}},
		{"replacement tools and notices only", []ProviderHistoryItem{{Type: ProviderHistoryItemTypeFunctionCall, Content: &content}, {Type: ProviderHistoryItemTypeOther, Role: &user, Content: &content}}},
		{"replacement empty", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := eventPayloadEligibleForCommittedTime(HistoryReplacementRecord{Items: test.items})
			if err != nil || got {
				t.Fatalf("eligible=%t err=%v want=false", got, err)
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
	for _, value := range []int64{transcript.MinCommittedAtUnixMs - 1, transcript.MaxCommittedAtUnixMs + 1} {
		outOfRange := transcript.CommittedAtUnixMs(value)
		if _, err := newEventRecord(1, nil, MessageRecord{Role: MessageRoleUser, Content: &content}, &outOfRange); err == nil {
			t.Fatalf("out-of-range timestamp %d accepted by event constructor", value)
		}
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
	if _, err := newEventRecord(1, nil, MessageRecord{
		Role: MessageRoleUser, MessageType: messageTypePointer(MessageTypeCompactionSummary), Content: &content,
	}, &one); err == nil {
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
func TestReplayPreservesPresentTimeThroughForkCloneAndReplacementRebase(t *testing.T) {
	parent := newSessionTestStore(t)
	log := mustMaterializeSessionTestEventLog(t, parent)
	absent, err := decodeEventRecordV1([]byte(`{"seq":1,"kind":"message","payload":{"role":"user","content":"old"}}`))
	if err != nil {
		t.Fatalf("decode absent source user: %v", err)
	}
	present := transcript.CommittedAtUnixMs(123)
	user, err := newEventRecord(2, nil, MessageRecord{Role: MessageRoleUser, Content: stringPointer("present")}, &present)
	if err != nil {
		t.Fatalf("create source user: %v", err)
	}
	replacement, err := newEventRecord(3, nil, HistoryReplacementRecord{
		Engine: "local",
		Mode:   CompactionModeAuto,
		LatestRollbackCandidate: &rollbacktarget.CandidateLocator{
			UserMessageSeq: 2, CandidatePageEndByte: 1,
		},
		Items: []ProviderHistoryItem{{
			Type:    ProviderHistoryItemTypeMessage,
			Role:    pointerTo(MessageRoleAssistant),
			Content: stringPointer("replacement"),
			Raw:     []byte(`{"type":"message","role":"assistant","content":"replacement"}`),
		}},
	}, &present)
	if err != nil {
		t.Fatalf("create source replacement: %v", err)
	}
	if _, err := log.AppendReplayRecords([]EventRecord{absent, user, replacement}); err != nil {
		t.Fatalf("replay source records: %v", err)
	}
	fillers := make([]EventRecordPayload, forkReplayFlushEventCount-2)
	for index := range fillers {
		fillers[index] = LocalEntryRecord{
			Visibility: EntryVisibilityHidden,
			Role:       "replay filler",
			Text:       stringPointer("bounded"),
		}
	}
	if _, receipt, err := log.AppendRecordsAtomic(nil, fillers); err != nil || !receipt.Committed {
		t.Fatalf("append replay fillers: receipt=%+v err=%v", receipt, err)
	}
	postFlush, err := newEventRecord(514, nil, MessageRecord{
		Role: MessageRoleAssistant, Content: stringPointer("post-flush"),
	}, &present)
	if err != nil {
		t.Fatalf("create post-flush source message: %v", err)
	}
	if _, err := log.AppendReplayRecords([]EventRecord{postFlush}); err != nil {
		t.Fatalf("replay post-flush source message: %v", err)
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
		if len(records) < forkReplayFlushEventCount {
			t.Fatalf("%s records=%d", name, len(records))
		}
		if records[0].CommittedAtUnixMs() != nil {
			t.Fatalf("%s absent record timestamp=%v", name, records[0].CommittedAtUnixMs())
		}
		replacement, ok := mustEventRecordPayload(records[2]).(HistoryReplacementRecord)
		if !ok || replacement.LatestRollbackCandidate == nil ||
			replacement.LatestRollbackCandidate.UserMessageSeq != records[1].Seq() ||
			replacement.LatestRollbackCandidate.CandidatePageEndByte <= 0 {
			t.Fatalf("%s replacement rebase = %+v", name, mustEventRecordPayload(records[2]))
		}
		for _, index := range []int{1, 2, forkReplayFlushEventCount + 1} {
			if records[index].CommittedAtUnixMs() == nil || records[index].CommittedAtUnixMs().UnixMs() != present.UnixMs() {
				t.Fatalf("%s record %d timestamp=%v want=%d", name, index, records[index].CommittedAtUnixMs(), present.UnixMs())
			}
		}
	}
}
