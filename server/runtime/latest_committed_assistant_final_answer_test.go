package runtime

import (
	"encoding/json"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/shared/textutil"
)

func appendFinalAnswerTestRecord(
	t *testing.T,
	eventLog session.MaterializedEventLog,
	payload session.EventRecordPayload,
) {
	t.Helper()
	stepID := "step"
	if _, _, err := eventLog.AppendRecord(&stepID, payload); err != nil {
		t.Fatalf("append %T: %v", payload, err)
	}
}

func finalAnswerMessageRecord(t *testing.T, message llm.Message) session.MessageRecord {
	t.Helper()
	record, err := sessionMessageRecordFromLLM(message)
	if err != nil {
		t.Fatalf("adapt message: %v", err)
	}
	return record
}

func finalAnswerCompactionRecord(t *testing.T, answer string) session.HistoryReplacementRecord {
	t.Helper()
	record, err := sessionHistoryReplacementRecordFromRuntime(historyReplacementPayload{
		Engine:                            "local",
		Mode:                              string(compactionModeAuto),
		LastCommittedAssistantFinalAnswer: textutil.OptionalExactString(answer),
	})
	if err != nil {
		t.Fatalf("adapt compaction: %v", err)
	}
	return record
}

func TestLatestCommittedAssistantFinalAnswerReturnsNewestFinalByteForByte(t *testing.T) {
	t.Parallel()
	eventLog := mustMaterializeTestEventLog(t, mustCreateTestSession(t))
	appendFinalAnswerTestRecord(t, eventLog, finalAnswerMessageRecord(t, llm.Message{
		Role:    llm.RoleAssistant,
		Phase:   textutil.Value(llm.MessagePhaseFinal),
		Content: textutil.Value("  exact final answer\n"),
	}))

	answer, err := LatestCommittedAssistantFinalAnswerFromEventLog(eventLog)
	if err != nil {
		t.Fatalf("lookup final answer: %v", err)
	}
	if answer == nil {
		t.Fatal("expected final answer")
	}
	if got, want := *answer, "  exact final answer\n"; got != want {
		t.Fatalf("answer = %q, want %q", got, want)
	}
}

func TestLatestCommittedAssistantFinalAnswerBlankDoesNotReplacePrevious(t *testing.T) {
	t.Parallel()
	eventLog := mustMaterializeTestEventLog(t, mustCreateTestSession(t))
	appendFinalAnswerTestRecord(t, eventLog, finalAnswerMessageRecord(t, llm.Message{
		Role:    llm.RoleAssistant,
		Phase:   textutil.Value(llm.MessagePhaseFinal),
		Content: textutil.Value("committed answer"),
	}))
	appendFinalAnswerTestRecord(t, eventLog, finalAnswerMessageRecord(t, llm.Message{Role: llm.RoleUser, Content: textutil.Value("next task")}))
	appendFinalAnswerTestRecord(t, eventLog, finalAnswerMessageRecord(t, llm.Message{
		Role:    llm.RoleAssistant,
		Phase:   textutil.Value(llm.MessagePhaseCommentary),
		Content: textutil.Value("streaming-style persisted commentary"),
	}))
	appendFinalAnswerTestRecord(t, eventLog, finalAnswerMessageRecord(t, llm.Message{
		Role:    llm.RoleAssistant,
		Phase:   textutil.Value(llm.MessagePhaseFinal),
		Content: textutil.Value(""),
	}))

	answer, err := LatestCommittedAssistantFinalAnswerFromEventLog(eventLog)
	if err != nil {
		t.Fatalf("lookup final answer: %v", err)
	}
	if answer == nil || *answer != "committed answer" {
		t.Fatalf("answer = %v, want committed answer", answer)
	}
}

func TestLatestCommittedAssistantFinalAnswerCompactionBoundaryReturnsAbsence(t *testing.T) {
	t.Parallel()
	eventLog := mustMaterializeTestEventLog(t, mustCreateTestSession(t))
	appendFinalAnswerTestRecord(t, eventLog, finalAnswerMessageRecord(t, llm.Message{
		Role:    llm.RoleAssistant,
		Phase:   textutil.Value(llm.MessagePhaseFinal),
		Content: textutil.Value("pre-compaction answer"),
	}))
	appendFinalAnswerTestRecord(t, eventLog, finalAnswerCompactionRecord(t, "carried pre-compaction answer"))

	answer, err := LatestCommittedAssistantFinalAnswerFromEventLog(eventLog)
	if err != nil {
		t.Fatalf("lookup final answer: %v", err)
	}
	if answer != nil {
		t.Fatalf("answer = %q, want absence at compaction boundary", *answer)
	}
}

func TestLatestCommittedAssistantFinalAnswerReturnsPostCompactionAnswer(t *testing.T) {
	t.Parallel()
	eventLog := mustMaterializeTestEventLog(t, mustCreateTestSession(t))
	appendFinalAnswerTestRecord(t, eventLog, finalAnswerMessageRecord(t, llm.Message{
		Role:    llm.RoleAssistant,
		Phase:   textutil.Value(llm.MessagePhaseFinal),
		Content: textutil.Value("pre-compaction answer"),
	}))
	appendFinalAnswerTestRecord(t, eventLog, finalAnswerCompactionRecord(t, ""))
	appendFinalAnswerTestRecord(t, eventLog, finalAnswerMessageRecord(t, llm.Message{
		Role:    llm.RoleAssistant,
		Phase:   textutil.Value(llm.MessagePhaseFinal),
		Content: textutil.Value("post-compaction answer"),
	}))

	answer, err := LatestCommittedAssistantFinalAnswerFromEventLog(eventLog)
	if err != nil {
		t.Fatalf("lookup final answer: %v", err)
	}
	if answer == nil || *answer != "post-compaction answer" {
		t.Fatalf("answer = %v, want post-compaction answer", answer)
	}
}

func TestLatestCommittedAssistantFinalAnswerReturnsAbsenceWithoutFinal(t *testing.T) {
	t.Parallel()
	eventLog := mustMaterializeTestEventLog(t, mustCreateTestSession(t))
	appendFinalAnswerTestRecord(t, eventLog, finalAnswerMessageRecord(t, llm.Message{Role: llm.RoleUser, Content: textutil.Value("task")}))

	answer, err := LatestCommittedAssistantFinalAnswerFromEventLog(eventLog)
	if err != nil {
		t.Fatalf("lookup final answer: %v", err)
	}
	if answer != nil {
		t.Fatalf("answer = %q, want absence", *answer)
	}
}

func TestLatestCommittedAssistantFinalAnswerFailsOnMalformedRelevantEvents(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	eventLog := mustMaterializeTestEventLog(t, store)
	appendFinalAnswerTestRecord(t, eventLog, finalAnswerMessageRecord(t, llm.Message{
		Role:    llm.RoleAssistant,
		Phase:   textutil.Value(llm.MessagePhaseFinal),
		Content: textutil.Value("final answer before malformed record"),
	}))
	line, err := json.Marshal(struct {
		Seq     int64                            `json:"seq"`
		Kind    session.EventKind                `json:"kind"`
		Payload session.HistoryReplacementRecord `json:"payload"`
	}{
		Seq:  2,
		Kind: session.EventKindHistoryReplace,
		Payload: session.HistoryReplacementRecord{
			Engine: "local",
			Mode:   session.CompactionModeManual,
			Items: []session.ProviderHistoryItem{{
				Type: session.ProviderHistoryItemTypeOther,
			}},
		},
	})
	if err != nil {
		t.Fatalf("marshal malformed relevant event: %v", err)
	}
	appendRawCurrentEventLine(t, store, line)

	answer, err := LatestCommittedAssistantFinalAnswerFromEventLog(eventLog)
	var itemErr session.ProviderHistoryItemError
	if answer != nil ||
		!errors.Is(err, session.ErrProviderHistoryItem) ||
		!errors.As(err, &itemErr) ||
		itemErr.Type != session.ProviderHistoryItemTypeOther ||
		itemErr.Reason != session.ProviderHistoryItemMissingRaw {
		t.Fatalf("latest final answer malformed-event result = answer:%+v error:%v item:%+v", answer, err, itemErr)
	}
}
