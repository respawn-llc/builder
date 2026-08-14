package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/shared/sessioncontract"
	"core/shared/transcript"
)

const expectedEventLogVersionV2 = 2

func TestEventLogVersionMatrixNewAndRecoveredEmptyLogsSelectV2(t *testing.T) {
	t.Run("independent creation", func(t *testing.T) {
		store := newSessionTestStore(t)
		mustMaterializeSessionTestEventLog(t, store)
		assertEventLogVersion(t, filepath.Join(store.Dir(), eventsFile), expectedEventLogVersionV2)
	})

	for _, source := range []struct {
		name      string
		writeFile bool
	}{
		{name: "missing"},
		{name: "zero byte", writeFile: true},
	} {
		t.Run("ordinary recovery "+source.name, func(t *testing.T) {
			store := newSessionTestStore(t)
			path := filepath.Join(store.Dir(), eventsFile)
			if source.writeFile {
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatalf("write empty event log: %v", err)
				}
			} else if err := os.Remove(path); err != nil {
				t.Fatalf("remove event log: %v", err)
			}

			reopened := mustOpenSessionTestStore(t, store)
			mustMaterializeSessionTestEventLog(t, reopened)
			assertEventLogVersion(t, path, expectedEventLogVersionV2)
		})
	}
}

func TestEventLogVersionMatrixReadOnlyOpenDoesNotMaterializeMissingOrEmptyLogs(t *testing.T) {
	for _, source := range []struct {
		name      string
		writeFile bool
	}{
		{name: "missing"},
		{name: "zero byte", writeFile: true},
	} {
		t.Run(source.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), eventsFile)
			if source.writeFile {
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatalf("write empty event log: %v", err)
				}
			}

			if _, err := openCurrentEventLog(path, currentEventLogReadOnly); err == nil {
				t.Fatal("read-only event-log open unexpectedly materialized an absent contract")
			}
			info, err := os.Stat(path)
			if source.writeFile {
				if err != nil {
					t.Fatalf("stat empty event log: %v", err)
				}
				if info.Size() != 0 {
					t.Fatalf("read-only open changed empty event-log size to %d", info.Size())
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("read-only open changed missing event log: %v", err)
			}
		})
	}

	for _, version := range []int{EventLogVersionV1, expectedEventLogVersionV2} {
		t.Run(eventLogVersionTestName(version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), eventsFile)
			writeVersionedEventLog(t, path, version, nil)
			log, err := openCurrentEventLog(path, currentEventLogReadOnly)
			if err != nil {
				t.Fatalf("open v%d header-only event log read-only: %v", version, err)
			}
			window, err := log.readRecentRecords(1, 128)
			if err != nil {
				t.Fatalf("read v%d header-only event log: %v", version, err)
			}
			if len(window.Records) != 0 || !window.ReachedStart || !window.ReachedEnd {
				t.Fatalf("v%d header-only window = %#v", version, window)
			}
		})
	}
}

func TestEventLogVersionMatrixClassification(t *testing.T) {
	for _, version := range []int{EventLogVersionV1, expectedEventLogVersionV2} {
		t.Run(eventLogVersionTestName(version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), eventsFile)
			writeVersionedEventLog(t, path, version, nil)
			classification, err := classifyEventLogSource(path)
			if err != nil {
				t.Fatalf("classify v%d event log: %v", version, err)
			}
			if classification.source != eventLogSourceCurrent ||
				classification.foundVersion == nil ||
				*classification.foundVersion != version {
				t.Fatalf("v%d classification = %#v", version, classification)
			}
		})
	}

	t.Run("unsupported older header", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), eventsFile)
		writeVersionedEventLog(t, path, 0, nil)
		classification, err := classifyEventLogSource(path)
		var malformed MalformedEventLogHeaderError
		if !errors.As(err, &malformed) ||
			classification == nil ||
			classification.source != eventLogSourceMalformed {
			t.Fatalf("older-header classification = %#v, error = %v", classification, err)
		}
	})

	t.Run("future header", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), eventsFile)
		writeVersionedEventLog(t, path, expectedEventLogVersionV2+1, nil)
		classification, err := classifyEventLogSource(path)
		var unsupported UnsupportedEventLogVersionError
		if !errors.As(err, &unsupported) {
			t.Fatalf("future-header error = %v, want UnsupportedEventLogVersionError", err)
		}
		if unsupported.FoundVersion != expectedEventLogVersionV2+1 ||
			unsupported.SupportedVersion != expectedEventLogVersionV2 ||
			classification == nil ||
			classification.source != eventLogSourceNewer {
			t.Fatalf("future-header classification = %#v, error = %#v", classification, unsupported)
		}
	})
}

func TestEventLogVersionMatrixHeaderlessLegacyTransformsToV1(t *testing.T) {
	store := newSessionTestStore(t)
	writeSessionFixtureEvents(t, store.Dir(), []legacyTestEvent{{
		Seq:       1,
		Timestamp: time.Now().UTC(),
		Kind:      string(EventKindMessage),
		Payload: mustFixtureJSON(t, map[string]any{
			"role":    string(MessageRoleUser),
			"content": "legacy question",
		}),
	}})

	log := mustMaterializeSessionTestEventLog(t, store)
	window, err := log.ReadRecentRecords(1)
	if err != nil {
		t.Fatalf("read transformed legacy event log: %v", err)
	}
	if len(window.Records) != 1 {
		t.Fatalf("transformed legacy records = %d, want 1", len(window.Records))
	}
	assertEventLogVersion(t, filepath.Join(store.Dir(), eventsFile), EventLogVersionV1)
}

func TestEventLogVersionMatrixOpenedLogsRetainVersionAcrossReconcileReadAndAppend(t *testing.T) {
	for _, version := range []int{EventLogVersionV1, expectedEventLogVersionV2} {
		t.Run(eventLogVersionTestName(version), func(t *testing.T) {
			store := newSessionTestStore(t)
			path := filepath.Join(store.Dir(), eventsFile)
			first := mustVersionMatrixRecord(t, 1, MessageRoleUser, "before reopen")
			writeVersionedEventLog(t, path, version, []EventRecord{first})

			meta := storeTestMeta(store)
			meta.LastSequence = 0
			persistence := &testSessionMetadata{records: map[string]PersistedSessionRecord{
				meta.SessionID: {SessionDir: store.Dir(), Meta: &meta},
			}}
			reopened, err := Open(
				store.Dir(),
				WithPersistedSessionResolver(persistence),
				WithPersistenceObserver(persistence),
			)
			if err != nil {
				t.Fatalf("open v%d Session: %v", version, err)
			}
			log := mustMaterializeSessionTestEventLog(t, reopened)
			active, err := log.ReadNewestSegmentBackward(nil)
			if err != nil {
				t.Fatalf("read v%d active segment: %v", version, err)
			}
			if len(active.Records) != 1 || active.Records[0].Seq() != 1 {
				t.Fatalf("v%d active records = %#v", version, active.Records)
			}
			appended, _, err := log.AppendRecord(nil, sessionTestMessage(MessageRoleAssistant, "after reopen"))
			if err != nil {
				t.Fatalf("append v%d event log: %v", version, err)
			}
			if appended.Seq() != 2 {
				t.Fatalf("v%d appended sequence = %d, want 2", version, appended.Seq())
			}
			assertEventLogVersion(t, path, version)
		})
	}
}

func TestEventLogVersionMatrixForkCloneAndDiagnosticCopyPreserveSourceVersion(t *testing.T) {
	for _, version := range []int{EventLogVersionV1, expectedEventLogVersionV2} {
		t.Run(eventLogVersionTestName(version), func(t *testing.T) {
			persistence := &testSessionMetadata{records: map[string]PersistedSessionRecord{}}
			store, err := Create(
				t.TempDir(),
				"workspace",
				"/tmp/work",
				sessioncontract.SessionCategoryMain,
				persistence.options()...,
			)
			if err != nil {
				t.Fatalf("create source Session: %v", err)
			}
			if err := store.EnsureDurable(); err != nil {
				t.Fatalf("persist source Session: %v", err)
			}
			records := []EventRecord{
				mustVersionMatrixRecord(t, 1, MessageRoleUser, "kept"),
				mustVersionMatrixRecord(t, 2, MessageRoleUser, "fork target"),
			}
			writeVersionedEventLog(t, filepath.Join(store.Dir(), eventsFile), version, records)
			parent := mustMaterializeSessionTestEventLog(t, store)

			forked, _, err := ForkAtUserMessage(parent, 2, "fork", sessioncontract.SessionCategoryMain)
			if err != nil {
				t.Fatalf("fork v%d Session: %v", version, err)
			}
			assertEventLogVersion(t, filepath.Join(forked.Dir(), eventsFile), version)

			cloned, err := CloneSession(parent, "clone", sessioncontract.SessionCategoryMain)
			if err != nil {
				t.Fatalf("clone v%d Session: %v", version, err)
			}
			assertEventLogVersion(t, filepath.Join(cloned.Dir(), eventsFile), version)

			inspection, err := OpenDiagnosticSessionCopy(t.Context(), persistence, store.Meta().SessionID)
			if err != nil {
				t.Fatalf("open v%d diagnostic copy: %v", version, err)
			}
			assertEventLogVersion(
				t,
				filepath.Join(inspection.Store().Dir(), eventsFile),
				version,
			)
			if err := inspection.Close(); err != nil {
				t.Fatalf("close v%d diagnostic copy: %v", version, err)
			}
		})
	}
}

func TestEventLogVersionMatrixMigrationWorkspaceRecoveryInstallsStagedV1(t *testing.T) {
	assertMigrationWorkspaceRecovery(t, EventLogVersionV1)
}

func TestEventLogVersionMatrixMigrationWorkspaceRecoveryPreservesStagedV2(t *testing.T) {
	assertMigrationWorkspaceRecovery(t, expectedEventLogVersionV2)
}

func TestEventLogV2QuestionCompletionRoundTrip(t *testing.T) {
	const output = `"User selected option 2. User also said: preserve multiline"`
	const presentation = `{"ToolName":"ask_question","Question":"Choose","Suggestions":["first","second"],"RecommendedOptionIndex":2}`
	v2Line := []byte(`{"seq":1,"kind":"tool_completed","committed_at_unix_ms":1723456789012,"payload":{"call_id":"call-question","name":"ask_question","output_kind":"function","is_error":false,"output":` +
		output + `,"presentation":` + presentation + `,"question_answer":{"selected_option_number":2,"freeform":"preserve\nmultiline"}}}`)
	path := filepath.Join(t.TempDir(), eventsFile)
	writeRawVersionedEventLog(t, path, expectedEventLogVersionV2, [][]byte{v2Line})

	log, err := openCurrentEventLog(path, currentEventLogAuthoritative)
	if err != nil {
		t.Fatalf("open v2 Question completion: %v", err)
	}
	window, err := log.readRecentRecords(1, 1024)
	if err != nil {
		t.Fatalf("read v2 Question completion: %v", err)
	}
	if len(window.Records) != 1 {
		t.Fatalf("v2 Question completion records = %d, want 1", len(window.Records))
	}
	payload := assertV2QuestionCompletion(
		t,
		window.Records[0],
		1,
		"preserve\nmultiline",
	)
	replayed, err := newEventRecord(
		2,
		nil,
		payload,
		window.Records[0].CommittedAtUnixMs(),
	)
	if err != nil {
		t.Fatalf("create second v2 Question completion: %v", err)
	}
	if _, err := log.appendRecords([]EventRecord{replayed}); err != nil {
		t.Fatalf("append second v2 Question completion: %v", err)
	}
	reopened, err := openCurrentEventLog(path, currentEventLogReadOnly)
	if err != nil {
		t.Fatalf("reopen v2 Question completion log: %v", err)
	}
	roundTrip, err := reopened.readRecentRecords(1, 1024)
	if err != nil {
		t.Fatalf("read appended v2 Question completion: %v", err)
	}
	roundTripPayload, err := roundTrip.Records[0].Payload()
	if err != nil {
		t.Fatalf("read appended v2 Question completion payload: %v", err)
	}
	_ = roundTripPayload
	assertV2QuestionCompletion(
		t,
		roundTrip.Records[0],
		2,
		"preserve\nmultiline",
	)
}

func TestEventLogV1QuestionCompletionProjectionDropsTypedAnswer(t *testing.T) {
	const output = `"User selected option 2. User also said: preserve multiline"`
	const presentation = `{"ToolName":"ask_question","Question":"Choose","Suggestions":["first","second"],"RecommendedOptionIndex":2}`
	var canonical ToolCompletionRecord
	if err := json.Unmarshal([]byte(`{"call_id":"call-question","name":"ask_question","output_kind":"function","is_error":false,"output":`+
		output+`,"presentation":`+presentation+`,"question_answer":{"selected_option_number":2,"freeform":"must be projected away"}}`), &canonical); err != nil {
		t.Fatalf("decode canonical Question completion: %v", err)
	}
	canonicalJSON, err := json.Marshal(canonical)
	if err != nil {
		t.Fatalf("encode canonical Question completion: %v", err)
	}
	assertQuestionAnswerWireFacts(
		t,
		canonicalJSON,
		"canonical Question completion",
		"must be projected away",
	)
	v1Record, err := NewEventRecord(1, nil, canonical)
	if err != nil {
		t.Fatalf("create canonical Question completion event: %v", err)
	}
	v1Line, err := encodeEventRecordV1(v1Record)
	if err != nil {
		t.Fatalf("re-encode v1 Question completion: %v", err)
	}
	var envelope struct {
		CommittedAt *int64                     `json:"committed_at_unix_ms"`
		Payload     map[string]json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(v1Line, &envelope); err != nil {
		t.Fatalf("decode projected v1 Question completion: %v", err)
	}
	if envelope.CommittedAt != nil {
		t.Fatalf("v1 Question completion timestamp = %d, want absent", *envelope.CommittedAt)
	}
	if _, exists := envelope.Payload["question_answer"]; exists {
		t.Fatal("v1 Question completion retained v2 typed Question-answer facts")
	}
	if !bytes.Equal(envelope.Payload["output"], json.RawMessage(output)) {
		t.Fatalf("v1 Question completion output = %s, want verbatim %s", envelope.Payload["output"], output)
	}
}

func TestEventLogV2QuestionCompletionPreservesAuthoredFreeformWhitespace(t *testing.T) {
	const authored = " \n  preserve this commentary \n "
	record, err := newEventRecord(
		1,
		nil,
		ToolCompletionRecord{
			CallID:     "call-question",
			Name:       askQuestionToolName,
			OutputKind: ToolOutputKindFunction,
			Output:     json.RawMessage(`"done"`),
			Presentation: json.RawMessage(
				`{"ToolName":"ask_question","Question":"Choose","Suggestions":["first","second"]}`,
			),
			QuestionAnswer: &QuestionAnswerRecord{
				SelectedOptionNumber: intPointer(2),
				Freeform:             stringPointer(authored),
			},
		},
		func() *transcript.CommittedAtUnixMs {
			value := transcript.CommittedAtUnixMs(1)
			return &value
		}(),
	)
	if err != nil {
		t.Fatalf("create v2 Question completion: %v", err)
	}
	line, err := encodeEventRecordV2(record)
	if err != nil {
		t.Fatalf("encode v2 Question completion: %v", err)
	}
	decoded, err := decodeEventRecordV2(line)
	if err != nil {
		t.Fatalf("decode v2 Question completion: %v", err)
	}
	payload, err := decoded.Payload()
	if err != nil {
		t.Fatalf("read v2 Question completion payload: %v", err)
	}
	completion := payload.(ToolCompletionRecord)
	if completion.QuestionAnswer == nil ||
		completion.QuestionAnswer.Freeform == nil ||
		*completion.QuestionAnswer.Freeform != authored {
		t.Fatalf("freeform = %#v, want exact authored text %q", completion.QuestionAnswer, authored)
	}
}

func assertMigrationWorkspaceRecovery(t *testing.T, version int) {
	t.Helper()
	store := newSessionTestStore(t)
	path := filepath.Join(store.Dir(), eventsFile)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remove source event log: %v", err)
	}
	workspace := eventLogMigrationWorkspacePath(store.Dir())
	if err := ensureOwnedEventLogMigrationWorkspace(workspace); err != nil {
		t.Fatalf("create migration workspace: %v", err)
	}
	stagedPath := filepath.Join(workspace, eventLogMigrationStagedLogFile)
	writeVersionedEventLog(t, stagedPath, version, []EventRecord{
		mustVersionMatrixRecord(t, 1, MessageRoleUser, "staged"),
	})
	if err := markStagedCurrentEventLogReady(workspace); err != nil {
		t.Fatalf("mark staged event log ready: %v", err)
	}

	log := mustMaterializeSessionTestEventLog(t, store)
	window, err := log.ReadRecentRecords(1)
	if err != nil {
		t.Fatalf("read recovered v%d staged log: %v", version, err)
	}
	if len(window.Records) != 1 || window.Records[0].Seq() != 1 {
		t.Fatalf("recovered v%d staged records = %#v", version, window.Records)
	}
	assertEventLogVersion(t, path, version)
}

func assertV2QuestionCompletion(
	t *testing.T,
	record EventRecord,
	wantSequence int64,
	wantFreeform string,
) EventRecordPayload {
	t.Helper()
	if record.Seq() != wantSequence {
		t.Fatalf("v2 Question completion sequence = %d, want %d", record.Seq(), wantSequence)
	}
	if record.CommittedAtUnixMs() == nil ||
		record.CommittedAtUnixMs().UnixMs() != 1_723_456_789_012 {
		t.Fatalf("v2 Question completion committed time = %v", record.CommittedAtUnixMs())
	}
	payload, err := record.Payload()
	if err != nil {
		t.Fatalf("read v2 Question completion payload: %v", err)
	}
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode v2 Question completion payload: %v", err)
	}
	assertQuestionAnswerWireFacts(
		t,
		encodedPayload,
		"v2 Question completion",
		wantFreeform,
	)
	var completion struct {
		Presentation json.RawMessage `json:"presentation"`
	}
	if err := json.Unmarshal(encodedPayload, &completion); err != nil {
		t.Fatalf("decode v2 Question completion presentation: %v", err)
	}
	var presented struct {
		Question               string   `json:"Question"`
		Suggestions            []string `json:"Suggestions"`
		RecommendedOptionIndex int      `json:"RecommendedOptionIndex"`
	}
	if err := json.Unmarshal(completion.Presentation, &presented); err != nil {
		t.Fatalf("decode persisted Question presentation: %v", err)
	}
	if presented.Question != "Choose" ||
		!equalStrings(presented.Suggestions, []string{"first", "second"}) ||
		presented.RecommendedOptionIndex != 2 {
		t.Fatalf("persisted Question presentation = %#v", presented)
	}
	return payload
}

func assertQuestionAnswerWireFacts(
	t *testing.T,
	raw []byte,
	owner string,
	wantFreeform string,
) {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("decode %s: %v", owner, err)
	}
	answerRaw := object["question_answer"]
	if len(answerRaw) == 0 {
		t.Fatalf("%s lost typed Question-answer facts", owner)
	}
	var answer struct {
		SelectedOptionNumber *int    `json:"selected_option_number"`
		Freeform             *string `json:"freeform"`
	}
	if err := json.Unmarshal(answerRaw, &answer); err != nil {
		t.Fatalf("decode %s typed Question answer: %v", owner, err)
	}
	if answer.SelectedOptionNumber == nil ||
		*answer.SelectedOptionNumber != 2 ||
		answer.Freeform == nil ||
		*answer.Freeform != wantFreeform {
		t.Fatalf("%s typed Question answer = %#v", owner, answer)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func mustVersionMatrixRecord(
	t *testing.T,
	sequence int64,
	role MessageRole,
	content string,
) EventRecord {
	t.Helper()
	record, err := NewEventRecord(sequence, nil, MessageRecord{Role: role, Content: &content})
	if err != nil {
		t.Fatalf("create version-matrix record: %v", err)
	}
	return record
}

func writeVersionedEventLog(
	t *testing.T,
	path string,
	version int,
	records []EventRecord,
) {
	t.Helper()
	lines := make([][]byte, 0, len(records))
	for _, record := range records {
		line, err := encodeEventRecordForVersion(version, record)
		if err != nil {
			t.Fatalf("encode v%d fixture record: %v", version, err)
		}
		lines = append(lines, line)
	}
	writeRawVersionedEventLog(t, path, version, lines)
}

func writeRawVersionedEventLog(t *testing.T, path string, version int, lines [][]byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create event-log fixture directory: %v", err)
	}
	header, err := json.Marshal(EventLogHeader{Contract: EventLogContract, Version: version})
	if err != nil {
		t.Fatalf("encode v%d fixture header: %v", version, err)
	}
	content := append(header, '\n')
	for _, line := range lines {
		content = append(content, line...)
		content = append(content, '\n')
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write v%d event-log fixture: %v", version, err)
	}
}

func assertEventLogVersion(t *testing.T, path string, want int) {
	t.Helper()
	fp, err := os.Open(path)
	if err != nil {
		t.Fatalf("open event log header: %v", err)
	}
	defer func() {
		if err := fp.Close(); err != nil {
			t.Fatalf("close event log header: %v", err)
		}
	}()
	line, err := bufio.NewReader(fp).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read event log header: %v", err)
	}
	var header EventLogHeader
	if err := json.Unmarshal(bytes.TrimSpace(line), &header); err != nil {
		t.Fatalf("decode event log header: %v", err)
	}
	if header.Contract != EventLogContract || header.Version != want {
		t.Fatalf("event log header = %#v, want contract %q version %d", header, EventLogContract, want)
	}
}

func eventLogVersionTestName(version int) string {
	return "v" + string(rune('0'+version))
}
