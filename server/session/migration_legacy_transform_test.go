package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"core/server/llm/openaiwire"
)

func TestLegacyMigrationStreamsDirectSnapshotsWithoutCorrelationArtifacts(t *testing.T) {
	raw := json.RawMessage(`{ "type" : "function_call_output", "call_id" : "call-1", "output" : "done" }`)
	legacy := []byte(
		`{"seq":1,"timestamp":"2026-07-19T10:00:00Z","kind":"run_started","payload":{}}` + "\n" +
			`{"seq":2,"timestamp":"2026-07-19T10:00:01Z","kind":"tool_completed","payload":{` +
			`"call_id":"call-1","name":"exec_command","is_error":false,"output":"done",` +
			`"provider_items":[{"type":"function_call_output","call_id":"call-1","output":"done","raw":` +
			string(raw) + `}]}}` + "\n",
	)
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatalf("write legacy fixture: %v", err)
	}
	source, err := openRegularSessionFile(path, "legacy transform fixture")
	if err != nil {
		t.Fatalf("open legacy fixture: %v", err)
	}
	defer source.Close()

	ledger := newMigrationResourceLedger()
	var output bytes.Buffer
	result, err := transformLegacyEventLogV0(
		context.Background(),
		source,
		int64(len(legacy)),
		&output,
		dir,
		ledger,
		osMigrationSpoolStorage{},
	)
	if err != nil {
		t.Fatalf("transform direct legacy log: %v", err)
	}
	if result.DirectSnapshots != 1 ||
		result.GeneratedSnapshotRaw != 0 ||
		result.AbsentSnapshots != 0 ||
		result.CorrelationArtifacts != 0 {
		t.Fatalf("direct transform result = %+v", result)
	}
	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte{'\n'})
	if len(lines) != 2 {
		t.Fatalf("transformed line count = %d, want header plus one record", len(lines))
	}
	if _, err := decodeEventLogHeader(lines[0]); err != nil {
		t.Fatalf("decode transformed header: %v", err)
	}
	record, err := decodeEventRecordV1(lines[1])
	if err != nil {
		t.Fatalf("decode transformed record: %v", err)
	}
	completion := mustEventRecordPayload(record).(ToolCompletionRecord)
	if record.Seq() != 2 ||
		len(completion.ProviderItems) != 1 ||
		!bytes.Equal(completion.ProviderItems[0].Raw, raw) {
		t.Fatalf("transformed completion = seq %d payload %#v", record.Seq(), completion)
	}
	stats := ledger.snapshot()
	if stats.MaxOpenSpoolFiles != 0 ||
		stats.PeakSpoolBytes != 0 ||
		stats.CurrentSpoolBytes != 0 {
		t.Fatalf("direct transform used correlation artifacts: %+v", stats)
	}
}

func TestLegacyMigrationRepairsSequenceRegressionWithMinimumCumulativeOffset(t *testing.T) {
	legacy := []byte(
		`{"seq":2,"timestamp":"2026-07-19T10:00:00Z","kind":"message",` +
			`"payload":{"role":"assistant","content":"first"}}` + "\n" +
			`{"seq":1,"timestamp":"2026-07-19T10:00:01Z","kind":"message",` +
			`"payload":{"role":"assistant","content":"second"}}` + "\n",
	)

	output, _, _ := transformLegacyFixture(t, legacy)
	lines := bytes.Split(bytes.TrimSpace(output), []byte{'\n'})
	if len(lines) != 3 {
		t.Fatalf("transformed line count = %d, want header plus two records", len(lines))
	}
	for index, wantSequence := range []int64{2, 3} {
		record, err := decodeEventRecordV1(lines[index+1])
		if err != nil {
			t.Fatalf("decode transformed record %d: %v", index, err)
		}
		if record.Seq() != wantSequence {
			t.Fatalf(
				"transformed record %d sequence = %d, want %d",
				index,
				record.Seq(),
				wantSequence,
			)
		}
	}
}

func TestLegacyMigrationAppliesCumulativeOffsetToOverlappingSuffix(t *testing.T) {
	legacy := []byte(
		`{"seq":1,"timestamp":"2026-07-19T10:00:00Z","kind":"message","payload":{"role":"assistant","content":"one"}}` + "\n" +
			`{"seq":2,"timestamp":"2026-07-19T10:00:01Z","kind":"message","payload":{"role":"assistant","content":"two"}}` + "\n" +
			`{"seq":1,"timestamp":"2026-07-19T10:00:02Z","kind":"message","payload":{"role":"assistant","content":"three"}}` + "\n" +
			`{"seq":2,"timestamp":"2026-07-19T10:00:03Z","kind":"message","payload":{"role":"assistant","content":"four"}}` + "\n",
	)

	assertMigratedSequences(t, legacy, []int64{1, 2, 3, 4})
}

func TestLegacyMigrationPreservesStrictlyIncreasingSequenceGaps(t *testing.T) {
	legacy := []byte(
		`{"seq":2,"timestamp":"2026-07-19T10:00:00Z","kind":"message","payload":{"role":"assistant","content":"two"}}` + "\n" +
			`{"seq":7,"timestamp":"2026-07-19T10:00:01Z","kind":"message","payload":{"role":"assistant","content":"seven"}}` + "\n" +
			`{"seq":11,"timestamp":"2026-07-19T10:00:02Z","kind":"message","payload":{"role":"assistant","content":"eleven"}}` + "\n",
	)

	assertMigratedSequences(t, legacy, []int64{2, 7, 11})
}

func TestLegacyMigrationDroppedRegressionAdvancesOffsetAndPreservesGap(t *testing.T) {
	legacy := []byte(
		`{"seq":5,"timestamp":"2026-07-19T10:00:00Z","kind":"message","payload":{"role":"assistant","content":"first"}}` + "\n" +
			`{"seq":2,"timestamp":"2026-07-19T10:00:01Z","kind":"run_started","payload":{}}` + "\n" +
			`{"seq":4,"timestamp":"2026-07-19T10:00:02Z","kind":"message","payload":{"role":"assistant","content":"second"}}` + "\n",
	)

	assertMigratedSequences(t, legacy, []int64{5, 8})
}

func TestLegacyMigrationRebuildsRollbackLocatorFromNormalizedVisibleUser(t *testing.T) {
	legacy := []byte(
		`{"seq":5,"timestamp":"2026-07-19T10:00:00Z","kind":"message","payload":{"role":"user","content":"first"}}` + "\n" +
			`{"seq":2,"timestamp":"2026-07-19T10:00:01Z","kind":"run_started","payload":{}}` + "\n" +
			`{"seq":4,"timestamp":"2026-07-19T10:00:02Z","kind":"message","payload":{"role":"user","content":"second"}}` + "\n" +
			`{"seq":5,"timestamp":"2026-07-19T10:00:03Z","kind":"history_replaced","payload":{` +
			`"engine":"local","mode":"handoff",` +
			`"latest_rollback_candidate":{"user_message_seq":1,"candidate_page_end_byte":1},` +
			`"items":[]}}` + "\n",
	)

	output, _, _ := transformLegacyFixture(t, legacy)
	lines := bytes.SplitAfter(output, []byte{'\n'})
	if len(lines) != 5 {
		t.Fatalf("transformed line count = %d, want header plus three records", len(lines)-1)
	}
	historyRecord, err := decodeEventRecordV1(bytes.TrimSuffix(lines[3], []byte{'\n'}))
	if err != nil {
		t.Fatalf("decode transformed history replacement: %v", err)
	}
	history := mustEventRecordPayload(historyRecord).(HistoryReplacementRecord)
	wantCursor := int64(len(lines[0]) + len(lines[1]) + len(lines[2]))
	if historyRecord.Seq() != 9 ||
		history.LatestRollbackCandidate == nil ||
		history.LatestRollbackCandidate.UserMessageSeq != 8 ||
		history.LatestRollbackCandidate.CandidatePageEndByte != wantCursor {
		t.Fatalf(
			"rebuilt history locator = seq %d locator %#v, want seq 9 user 8 cursor %d",
			historyRecord.Seq(),
			history.LatestRollbackCandidate,
			wantCursor,
		)
	}
}

func TestLegacyMigrationRebuildsRollbackLocatorAfterFallbackDescriptor(t *testing.T) {
	legacy := []byte(
		`{"seq":10,"timestamp":"2026-07-19T10:00:00Z","kind":"message","payload":{` +
			`"role":"assistant","tool_calls":[{"id":"call-1","name":"exec_command","input":{}}]}}` + "\n" +
			`{"seq":1,"timestamp":"2026-07-19T10:00:01Z","kind":"tool_completed","payload":{` +
			`"call_id":"call-1","name":"exec_command","is_error":false,"output":"done"}}` + "\n" +
			`{"seq":2,"timestamp":"2026-07-19T10:00:02Z","kind":"message","payload":{"role":"user","content":"after tool"}}` + "\n" +
			`{"seq":3,"timestamp":"2026-07-19T10:00:03Z","kind":"history_replaced","payload":{` +
			`"engine":"local","mode":"handoff",` +
			`"latest_rollback_candidate":{"user_message_seq":1,"candidate_page_end_byte":1},` +
			`"items":[]}}` + "\n",
	)

	output, _, _ := transformLegacyFixture(t, legacy)
	lines := bytes.SplitAfter(output, []byte{'\n'})
	historyRecord, err := decodeEventRecordV1(bytes.TrimSuffix(lines[4], []byte{'\n'}))
	if err != nil {
		t.Fatalf("decode transformed history replacement: %v", err)
	}
	history := mustEventRecordPayload(historyRecord).(HistoryReplacementRecord)
	wantCursor := int64(len(lines[0]) + len(lines[1]) + len(lines[2]) + len(lines[3]))
	if history.LatestRollbackCandidate == nil ||
		history.LatestRollbackCandidate.UserMessageSeq != 12 ||
		history.LatestRollbackCandidate.CandidatePageEndByte != wantCursor {
		t.Fatalf(
			"fallback-suffix locator = %#v, want user 12 cursor %d",
			history.LatestRollbackCandidate,
			wantCursor,
		)
	}
}

func TestLegacyMigrationCorrelatesAbsentSnapshotWithPrecedingCustomCall(t *testing.T) {
	legacy := []byte(
		`{"seq":1,"timestamp":"2026-07-19T10:00:00Z","kind":"message","payload":{` +
			`"role":"assistant","tool_calls":[{"id":" call-1 ","name":" patch ","custom":true,` +
			`"custom_input":"*** Begin Patch\n*** End Patch","input":{"patch":"value"}}]}}` + "\n" +
			`{"seq":2,"timestamp":"2026-07-19T10:00:01Z","kind":"tool_completed","payload":{` +
			`"call_id":" call-1 ","name":" patch ","is_error":false,"output":"patched"}}` + "\n",
	)
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatalf("write legacy fixture: %v", err)
	}
	source, err := openRegularSessionFile(path, "legacy transform fixture")
	if err != nil {
		t.Fatalf("open legacy fixture: %v", err)
	}
	defer source.Close()

	ledger := newMigrationResourceLedger()
	var output bytes.Buffer
	result, err := transformLegacyEventLogV0(
		context.Background(),
		source,
		int64(len(legacy)),
		&output,
		dir,
		ledger,
		osMigrationSpoolStorage{},
	)
	if err != nil {
		t.Fatalf("transform fallback legacy log: %v", err)
	}
	if result.AbsentSnapshots != 1 || result.CorrelationArtifacts == 0 {
		t.Fatalf("fallback transform result = %+v", result)
	}
	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte{'\n'})
	if len(lines) != 3 {
		t.Fatalf("transformed line count = %d, want header plus two records", len(lines))
	}
	completionRecord, err := decodeEventRecordV1(lines[2])
	if err != nil {
		t.Fatalf("decode transformed completion: %v", err)
	}
	completion := mustEventRecordPayload(completionRecord).(ToolCompletionRecord)
	if completion.OutputKind != ToolOutputKindCustom ||
		completion.Name != "patch" ||
		len(completion.ProviderItems) != 1 ||
		completion.ProviderItems[0].Type != ProviderInputItemTypeCustomToolOutput {
		t.Fatalf("correlated completion = %#v", completion)
	}
	wantRaw, err := openaiwire.NewCustomToolOutput("call-1", json.RawMessage(`"patched"`))
	if err != nil {
		t.Fatalf("build pre-migration fallback provider input: %v", err)
	}
	if !bytes.Equal(completion.ProviderItems[0].Raw, wantRaw.Bytes()) {
		t.Fatalf(
			"fallback provider input changed: got=%s want=%s",
			completion.ProviderItems[0].Raw,
			wantRaw.Bytes(),
		)
	}
	if stats := ledger.snapshot(); stats.CurrentSpoolBytes != 0 || stats.OpenSpoolFiles != 0 {
		t.Fatalf("fallback artifacts leaked: %+v", stats)
	}
}

func TestLegacyMigrationPreservesLargeStructuredFallbackProviderRaw(t *testing.T) {
	fileData := strings.Repeat("x", migrationInlineValueBudgetBytes+1)
	structuredOutput := []byte(
		`[{"type":"input_file","file_data":"` + fileData + `","filename":" artifact.txt "}]`,
	)
	legacy := []byte(
		`{"seq":1,"timestamp":"2026-07-19T10:00:00Z","kind":"message","payload":{` +
			`"role":"assistant","tool_calls":[{"id":"call-1","name":"exec_command","input":{}}]}}` + "\n" +
			`{"seq":2,"timestamp":"2026-07-19T10:00:01Z","kind":"tool_completed","payload":{` +
			`"call_id":"call-1","name":"exec_command","is_error":false,"output":` +
			string(structuredOutput) + `}}` + "\n",
	)

	output, result, ledger := transformLegacyFixture(t, legacy)
	if result.AbsentSnapshots != 1 || result.CorrelationArtifacts == 0 {
		t.Fatalf("large structured transform result = %+v", result)
	}
	lines := bytes.Split(bytes.TrimSpace(output), []byte{'\n'})
	if len(lines) != 3 {
		t.Fatalf("transformed line count = %d, want header plus two records", len(lines))
	}
	record, err := decodeEventRecordV1(lines[2])
	if err != nil {
		t.Fatalf("decode large structured completion: %v", err)
	}
	completion := mustEventRecordPayload(record).(ToolCompletionRecord)
	if len(completion.ProviderItems) != 1 {
		t.Fatalf("provider items = %d, want one", len(completion.ProviderItems))
	}
	wantRaw, err := openaiwire.NewFunctionCallOutput("call-1", structuredOutput)
	if err != nil {
		t.Fatalf("build provider-wire oracle: %v", err)
	}
	if !bytes.Equal(completion.ProviderItems[0].Raw, wantRaw.Bytes()) {
		t.Fatal("large structured fallback provider Raw differs from shared encoder")
	}
	var providerItem struct {
		Output []json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(completion.ProviderItems[0].Raw, &providerItem); err != nil {
		t.Fatalf("decode provider Raw: %v", err)
	}
	if len(providerItem.Output) != 1 {
		t.Fatalf("structured provider output item count = %d, want one", len(providerItem.Output))
	}
	if stats := ledger.snapshot(); stats.CurrentSpoolBytes != 0 || stats.OpenSpoolFiles != 0 {
		t.Fatalf("large structured artifacts leaked: %+v", stats)
	}
}

func TestLegacyMigrationFallbackCorrelationUsesNormalizedPhysicalOrder(t *testing.T) {
	legacy := []byte(
		`{"seq":10,"timestamp":"2026-07-19T10:00:00Z","kind":"message","payload":{` +
			`"role":"assistant","tool_calls":[{"id":"duplicate","name":"old","input":{}}]}}` + "\n" +
			`{"seq":1,"timestamp":"2026-07-19T10:00:01Z","kind":"message","payload":{` +
			`"role":"assistant","tool_calls":[{"id":"duplicate","name":"new","custom":true,` +
			`"custom_input":"patch","input":{}}]}}` + "\n" +
			`{"seq":2,"timestamp":"2026-07-19T10:00:02Z","kind":"tool_completed","payload":{` +
			`"call_id":"duplicate","name":"","is_error":false,"output":"done"}}` + "\n",
	)

	output, _, _ := transformLegacyFixture(t, legacy)
	lines := bytes.Split(bytes.TrimSpace(output), []byte{'\n'})
	completionRecord, err := decodeEventRecordV1(lines[3])
	if err != nil {
		t.Fatalf("decode transformed completion: %v", err)
	}
	completion := mustEventRecordPayload(completionRecord).(ToolCompletionRecord)
	if completionRecord.Seq() != 12 ||
		completion.OutputKind != ToolOutputKindCustom ||
		completion.Name != "new" {
		t.Fatalf("normalized fallback completion = seq %d payload %#v", completionRecord.Seq(), completion)
	}
}

func TestLegacyMigrationSeedsFallbackCorrelationFromCompactionHistory(t *testing.T) {
	legacy := []byte(
		`{"seq":1,"timestamp":"2026-07-19T10:00:00Z","kind":"history_replaced","payload":{` +
			`"engine":"local","mode":"handoff","items":[{"type":"custom_tool_call","id":" seed-call ",` +
			`"name":" patch ","custom_input":"*** Begin Patch\n*** End Patch",` +
			`"raw":{"type":"custom_tool_call","call_id":"seed-call","name":"patch","input":"patch"}}]}}` + "\n" +
			`{"seq":2,"timestamp":"2026-07-19T10:00:01Z","kind":"history_replaced","payload":{` +
			`"engine":"reviewer_rollback","mode":"manual","items":[]}}` + "\n" +
			`{"seq":3,"timestamp":"2026-07-19T10:00:02Z","kind":"tool_completed","payload":{` +
			`"call_id":" seed-call ","name":"","is_error":false,"output":"patched"}}` + "\n",
	)
	output, result, ledger := transformLegacyFixture(t, legacy)
	if result.AbsentSnapshots != 1 || result.CorrelationArtifacts == 0 {
		t.Fatalf("seeded transform result = %+v", result)
	}
	lines := bytes.Split(bytes.TrimSpace(output), []byte{'\n'})
	if len(lines) != 3 {
		t.Fatalf("transformed line count = %d, want header, seed, completion", len(lines))
	}
	completionRecord, err := decodeEventRecordV1(lines[2])
	if err != nil {
		t.Fatalf("decode seeded completion: %v", err)
	}
	completion := mustEventRecordPayload(completionRecord).(ToolCompletionRecord)
	if completion.OutputKind != ToolOutputKindCustom ||
		completion.Name != "patch" ||
		completion.ProviderItems[0].Type != ProviderInputItemTypeCustomToolOutput {
		t.Fatalf("seeded completion = %#v", completion)
	}
	if stats := ledger.snapshot(); stats.CurrentSpoolBytes != 0 || stats.OpenSpoolFiles != 0 {
		t.Fatalf("seeded transform artifacts leaked: %+v", stats)
	}
}

func TestLegacyMigrationCompactionSeedFallbackUsesNormalizedSuffixOrder(t *testing.T) {
	legacy := []byte(
		`{"seq":20,"timestamp":"2026-07-19T10:00:00Z","kind":"message",` +
			`"payload":{"role":"assistant","content":"before compaction"}}` + "\n" +
			`{"seq":10,"timestamp":"2026-07-19T10:00:01Z","kind":"history_replaced","payload":{` +
			`"engine":"local","mode":"handoff","items":[{"type":"function_call","id":"duplicate",` +
			`"name":"old","arguments":{},"raw":{"type":"function_call","call_id":"duplicate",` +
			`"name":"old","arguments":"{}"}}]}}` + "\n" +
			`{"seq":1,"timestamp":"2026-07-19T10:00:02Z","kind":"message","payload":{` +
			`"role":"assistant","tool_calls":[{"id":"duplicate","name":"new","custom":true,` +
			`"custom_input":"patch","input":{}}]}}` + "\n" +
			`{"seq":2,"timestamp":"2026-07-19T10:00:03Z","kind":"tool_completed","payload":{` +
			`"call_id":"duplicate","name":"","is_error":false,"output":"done"}}` + "\n",
	)

	output, _, _ := transformLegacyFixture(t, legacy)
	lines := bytes.Split(bytes.TrimSpace(output), []byte{'\n'})
	completionRecord, err := decodeEventRecordV1(lines[4])
	if err != nil {
		t.Fatalf("decode transformed completion: %v", err)
	}
	completion := mustEventRecordPayload(completionRecord).(ToolCompletionRecord)
	if completionRecord.Seq() != 23 ||
		completion.OutputKind != ToolOutputKindCustom ||
		completion.Name != "new" {
		t.Fatalf("normalized seeded completion = seq %d payload %#v", completionRecord.Seq(), completion)
	}
}

func TestLegacyMigrationRestoresOriginalOrderAcrossFallbackSuffix(t *testing.T) {
	directRaw := `{ "type" : "function_call_output", "call_id" : "direct", "output" : "direct" }`
	legacy := []byte(
		`{"seq":1,"timestamp":"2026-07-19T10:00:00Z","kind":"message","payload":{` +
			`"role":"assistant","tool_calls":[{"id":"duplicate","name":"first","input":{}}]}}` + "\n" +
			`{"seq":2,"timestamp":"2026-07-19T10:00:01Z","kind":"tool_completed","payload":{` +
			`"call_id":"duplicate","name":"first-result","is_error":false,"output":"one"}}` + "\n" +
			`{"seq":3,"timestamp":"2026-07-19T10:00:02Z","kind":"tool_completed","payload":{` +
			`"call_id":"duplicate","name":"second-result","is_error":false,"output":"two"}}` + "\n" +
			`{"seq":4,"timestamp":"2026-07-19T10:00:03Z","kind":"local_entry","payload":{` +
			`"visibility":"detail","role":"notice","text":"between"}}` + "\n" +
			`{"seq":5,"timestamp":"2026-07-19T10:00:04Z","kind":"tool_completed","payload":{` +
			`"call_id":"direct","name":"direct","is_error":false,"output":"direct",` +
			`"provider_items":[{"type":"function_call_output","call_id":"direct","output":"direct","raw":` +
			directRaw + `}]}}` + "\n" +
			`{"seq":6,"timestamp":"2026-07-19T10:00:05Z","kind":"message","payload":{` +
			`"role":"assistant","tool_calls":[{"id":"duplicate","name":"latest","custom":true,` +
			`"custom_input":"patch","input":{}}]}}` + "\n" +
			`{"seq":7,"timestamp":"2026-07-19T10:00:06Z","kind":"tool_completed","payload":{` +
			`"call_id":"duplicate","name":"latest-result","is_error":false,"output":"three"}}` + "\n" +
			`{"seq":8,"timestamp":"2026-07-19T10:00:07Z","kind":"tool_completed","payload":{` +
			`"call_id":"future","name":"future-result","is_error":false,"output":"early"}}` + "\n" +
			`{"seq":9,"timestamp":"2026-07-19T10:00:08Z","kind":"message","payload":{` +
			`"role":"assistant","tool_calls":[{"id":"future","name":"future","custom":true,` +
			`"custom_input":"later","input":{}}]}}` + "\n",
	)
	output, result, ledger := transformLegacyFixture(t, legacy)
	if result.DirectSnapshots != 1 ||
		result.AbsentSnapshots != 4 ||
		result.CorrelationArtifacts == 0 {
		t.Fatalf("suffix transform result = %+v", result)
	}
	lines := bytes.Split(bytes.TrimSpace(output), []byte{'\n'})
	if len(lines) != 10 {
		t.Fatalf("transformed line count = %d, want header plus nine records", len(lines))
	}
	var records []EventRecord
	for index, line := range lines[1:] {
		record, err := decodeEventRecordV1(line)
		if err != nil {
			t.Fatalf("decode transformed record %d: %v", index, err)
		}
		records = append(records, record)
		if record.Seq() != int64(index+1) {
			t.Fatalf("record %d sequence = %d", index, record.Seq())
		}
	}
	wantKinds := map[int64]ToolOutputKind{
		2: ToolOutputKindFunction,
		3: ToolOutputKindFunction,
		5: ToolOutputKindFunction,
		7: ToolOutputKindCustom,
		8: ToolOutputKindFunction,
	}
	for _, record := range records {
		want, ok := wantKinds[record.Seq()]
		if !ok {
			continue
		}
		completion := mustEventRecordPayload(record).(ToolCompletionRecord)
		if completion.OutputKind != want {
			t.Fatalf(
				"completion %d output kind = %q, want %q",
				record.Seq(),
				completion.OutputKind,
				want,
			)
		}
	}
	direct := mustEventRecordPayload(records[4]).(ToolCompletionRecord)
	if !bytes.Equal(direct.ProviderItems[0].Raw, []byte(directRaw)) {
		t.Fatalf("direct Raw changed: %q", direct.ProviderItems[0].Raw)
	}
	if stats := ledger.snapshot(); stats.CurrentSpoolBytes != 0 || stats.OpenSpoolFiles != 0 {
		t.Fatalf("suffix transform artifacts leaked: %+v", stats)
	}
}

func TestLegacyMigrationGeneratesDirectSnapshotRawWithoutCorrelation(t *testing.T) {
	legacy := []byte(
		`{"seq":1,"timestamp":"2026-07-19T10:00:00Z","kind":"tool_completed","payload":{` +
			`"call_id":"custom-1","name":"patch","is_error":false,"output":"patched",` +
			`"provider_items":[{"type":"custom_tool_call_output","call_id":"custom-1","output":"patched"}]}}` + "\n",
	)
	output, result, ledger := transformLegacyFixture(t, legacy)
	if result.DirectSnapshots != 0 ||
		result.GeneratedSnapshotRaw != 1 ||
		result.AbsentSnapshots != 0 ||
		result.CorrelationArtifacts != 0 {
		t.Fatalf("generated direct transform result = %+v", result)
	}
	lines := bytes.Split(bytes.TrimSpace(output), []byte{'\n'})
	record, err := decodeEventRecordV1(lines[1])
	if err != nil {
		t.Fatalf("decode generated direct completion: %v", err)
	}
	completion := mustEventRecordPayload(record).(ToolCompletionRecord)
	if completion.OutputKind != ToolOutputKindCustom ||
		len(completion.ProviderItems) != 1 ||
		len(completion.ProviderItems[0].Raw) == 0 {
		t.Fatalf("generated direct completion = %#v", completion)
	}
	wantRaw, err := openaiwire.NewCustomToolOutput("custom-1", json.RawMessage(`"patched"`))
	if err != nil {
		t.Fatalf("build pre-migration provider input: %v", err)
	}
	if !bytes.Equal(completion.ProviderItems[0].Raw, wantRaw.Bytes()) {
		t.Fatalf(
			"generated direct provider input changed: got=%s want=%s",
			completion.ProviderItems[0].Raw,
			wantRaw.Bytes(),
		)
	}
	if stats := ledger.snapshot(); stats.MaxOpenSpoolFiles != 0 || stats.PeakSpoolBytes != 0 {
		t.Fatalf("generated direct transform used correlation artifacts: %+v", stats)
	}
}

func TestLegacyMigrationFlushesFallbackEpochBeforeDroppingTornTail(t *testing.T) {
	legacy := []byte(
		`{"seq":1,"timestamp":"2026-07-19T10:00:00Z","kind":"message","payload":{` +
			`"role":"assistant","tool_calls":[{"id":"call-1","name":"exec_command","input":{}}]}}` + "\n" +
			`{"seq":2,"timestamp":"2026-07-19T10:00:01Z","kind":"tool_completed","payload":{` +
			`"call_id":"call-1","name":"exec_command","is_error":false,"output":"done"}}` + "\n" +
			`{"seq":3,"timestamp":"2026-07-19T10:00:02Z","kind":"message","payload":`,
	)
	output, result, ledger := transformLegacyFixture(t, legacy)
	if result.AbsentSnapshots != 1 || result.CorrelationArtifacts == 0 {
		t.Fatalf("torn-tail transform result = %+v", result)
	}
	lines := bytes.Split(bytes.TrimSpace(output), []byte{'\n'})
	if len(lines) != 3 {
		t.Fatalf("transformed line count = %d, want header plus valid prefix", len(lines))
	}
	record, err := decodeEventRecordV1(lines[2])
	if err != nil || record.Seq() != 2 {
		t.Fatalf("valid fallback prefix was lost: record=%#v error=%v", record, err)
	}
	if stats := ledger.snapshot(); stats.CurrentSpoolBytes != 0 || stats.OpenSpoolFiles != 0 {
		t.Fatalf("torn-tail artifacts leaked: %+v", stats)
	}
}

func TestLegacyMigrationCleansFallbackArtifactsAfterLaterDecodeFailure(t *testing.T) {
	legacy := []byte(
		`{"seq":1,"timestamp":"2026-07-19T10:00:00Z","kind":"message","payload":{` +
			`"role":"assistant","tool_calls":[{"id":"call-1","name":"exec_command","input":{}}]}}` + "\n" +
			`{"seq":2,"timestamp":"2026-07-19T10:00:01Z","kind":"tool_completed","payload":{` +
			`"call_id":"call-1","name":"exec_command","is_error":false,"output":"done"}}` + "\n" +
			`{"seq":3,"timestamp":"2026-07-19T10:00:02Z","kind":"message","payload":not-json}` + "\n",
	)
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatalf("write legacy fixture: %v", err)
	}
	source, err := openRegularSessionFile(path, "legacy transform fixture")
	if err != nil {
		t.Fatalf("open legacy fixture: %v", err)
	}
	defer source.Close()
	ledger := newMigrationResourceLedger()
	var output bytes.Buffer
	if _, err := transformLegacyEventLogV0(
		context.Background(),
		source,
		int64(len(legacy)),
		&output,
		dir,
		ledger,
		osMigrationSpoolStorage{},
	); err == nil {
		t.Fatal("malformed complete record succeeded")
	}
	if stats := ledger.snapshot(); stats.CurrentSpoolBytes != 0 || stats.OpenSpoolFiles != 0 {
		t.Fatalf("decode-failure artifacts leaked: %+v", stats)
	}
}

func TestLegacyMigrationCleansActiveFallbackEpochOnCancellation(t *testing.T) {
	first := []byte(
		`{"seq":1,"timestamp":"2026-07-19T10:00:00Z","kind":"message","payload":{` +
			`"role":"assistant","tool_calls":[{"id":"call-1","name":"exec_command","input":{}}]}}` + "\n",
	)
	second := []byte(
		`{"seq":2,"timestamp":"2026-07-19T10:00:01Z","kind":"tool_completed","payload":{` +
			`"call_id":"call-1","name":"exec_command","is_error":false,"output":"done"}}` + "\n",
	)
	third := []byte(
		`{"seq":3,"timestamp":"2026-07-19T10:00:02Z","kind":"local_entry","payload":{` +
			`"visibility":"detail","role":"notice","text":"after fallback"}}` + "\n",
	)
	legacy := append(append(first, second...), third...)
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatalf("write cancellation fixture: %v", err)
	}
	source, err := openRegularSessionFile(path, "legacy cancellation fixture")
	if err != nil {
		t.Fatalf("open cancellation fixture: %v", err)
	}
	defer source.Close()
	ctx, cancel := context.WithCancel(context.Background())
	reader := &cancelingMigrationReaderAt{
		ReaderAt: source,
		cancelAt: int64(len(first) + len(second)),
		cancel:   cancel,
	}
	ledger := newMigrationResourceLedger()
	var output bytes.Buffer
	_, err = transformLegacyEventLogV0(
		ctx,
		reader,
		int64(len(legacy)),
		&output,
		dir,
		ledger,
		osMigrationSpoolStorage{},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v, want context canceled", err)
	}
	if stats := ledger.snapshot(); stats.CurrentSpoolBytes != 0 || stats.OpenSpoolFiles != 0 {
		t.Fatalf("cancellation artifacts leaked: %+v", stats)
	}
}

func TestLegacyMigrationExternallyCorrelatesMoreThanRunBudgetOfPrefixCalls(t *testing.T) {
	const callCount = 130
	largePrefix := strings.Repeat("x", 65_537)
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("create large legacy fixture: %v", err)
	}
	encoder := json.NewEncoder(file)
	lastCallID := ""
	for index := 0; index < callCount; index++ {
		callID := largePrefix + fmt.Sprintf("-%03d", index)
		lastCallID = callID
		if err := encoder.Encode(struct {
			Seq       int64           `json:"seq"`
			Timestamp string          `json:"timestamp"`
			Kind      string          `json:"kind"`
			Payload   legacyMessageV0 `json:"payload"`
		}{
			Seq:       int64(index + 1),
			Timestamp: "2026-07-19T10:00:00Z",
			Kind:      "message",
			Payload: legacyMessageV0{
				Role: MessageRoleAssistant,
				ToolCalls: []legacyMessageToolCallV0{{
					ID: callID, Name: "exec_command", Input: json.RawMessage(`{}`),
				}},
			},
		}); err != nil {
			_ = file.Close()
			t.Fatalf("write large legacy call %d: %v", index, err)
		}
	}
	isError := false
	if err := encoder.Encode(struct {
		Seq       int64                  `json:"seq"`
		Timestamp string                 `json:"timestamp"`
		Kind      string                 `json:"kind"`
		Payload   legacyToolCompletionV0 `json:"payload"`
	}{
		Seq:       callCount + 1,
		Timestamp: "2026-07-19T10:00:01Z",
		Kind:      "tool_completed",
		Payload: legacyToolCompletionV0{
			CallID:  lastCallID,
			Name:    "exec_command",
			IsError: &isError,
			Output:  json.RawMessage(`"done"`),
		},
	}); err != nil {
		_ = file.Close()
		t.Fatalf("write large legacy fallback: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close large legacy fixture: %v", err)
	}
	source, err := openRegularSessionFile(path, "large legacy transform fixture")
	if err != nil {
		t.Fatalf("open large legacy fixture: %v", err)
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		t.Fatalf("stat large legacy fixture: %v", err)
	}
	ledger := newMigrationResourceLedger()
	result, err := transformLegacyEventLogV0(
		context.Background(),
		source,
		info.Size(),
		io.Discard,
		dir,
		ledger,
		osMigrationSpoolStorage{},
	)
	if err != nil {
		t.Fatalf("transform large legacy fixture: %v", err)
	}
	if result.AbsentSnapshots != 1 || result.CorrelationArtifacts <= 5 {
		t.Fatalf("large transform result = %+v", result)
	}
	stats := ledger.snapshot()
	if stats.MaxLiveInlineBytes > migrationCorrelationRunBudgetBytes ||
		stats.MaxOpenSpoolFiles > migrationMaxOpenSpoolFiles ||
		stats.MaxEncoderMergeBytes > migrationEncoderMergeBudgetBytes ||
		stats.PeakSpoolBytes <= migrationCorrelationRunBudgetBytes ||
		stats.CurrentSpoolBytes != 0 ||
		stats.OpenSpoolFiles != 0 {
		t.Fatalf("large transform resource stats = %+v", stats)
	}
}

func transformLegacyFixture(
	t *testing.T,
	legacy []byte,
) ([]byte, legacyMigrationTransformResult, *migrationResourceLedger) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatalf("write legacy fixture: %v", err)
	}
	source, err := openRegularSessionFile(path, "legacy transform fixture")
	if err != nil {
		t.Fatalf("open legacy fixture: %v", err)
	}
	defer source.Close()
	ledger := newMigrationResourceLedger()
	var output bytes.Buffer
	result, err := transformLegacyEventLogV0(
		context.Background(),
		source,
		int64(len(legacy)),
		&output,
		dir,
		ledger,
		osMigrationSpoolStorage{},
	)
	if err != nil {
		t.Fatalf("transform legacy fixture: %v", err)
	}
	return output.Bytes(), result, ledger
}

func assertMigratedSequences(t *testing.T, legacy []byte, want []int64) {
	t.Helper()
	output, _, _ := transformLegacyFixture(t, legacy)
	lines := bytes.Split(bytes.TrimSpace(output), []byte{'\n'})
	if len(lines) != len(want)+1 {
		t.Fatalf(
			"transformed line count = %d, want header plus %d records",
			len(lines),
			len(want),
		)
	}
	for index, wantSequence := range want {
		record, err := decodeEventRecordV1(lines[index+1])
		if err != nil {
			t.Fatalf("decode transformed record %d: %v", index, err)
		}
		if record.Seq() != wantSequence {
			t.Fatalf(
				"transformed record %d sequence = %d, want %d",
				index,
				record.Seq(),
				wantSequence,
			)
		}
	}
}

type cancelingMigrationReaderAt struct {
	io.ReaderAt
	cancelAt int64
	cancel   context.CancelFunc
	done     bool
}

func (r *cancelingMigrationReaderAt) ReadAt(payload []byte, offset int64) (int, error) {
	if !r.done && offset >= r.cancelAt {
		r.done = true
		r.cancel()
	}
	return r.ReaderAt.ReadAt(payload, offset)
}
