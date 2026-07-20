package session

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"core/server/llm/openaiwire"
)

func TestMaterializeEventLogMigratesLegacySourceDurably(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	eventsPath := filepath.Join(sessionDir, eventsFile)
	legacy := []byte(
		`{"seq":1,"timestamp":"2026-07-19T10:00:00Z","kind":"message","step_id":"step-1",` +
			`"payload":{"role":"user","content":"continue this session"}}` + "\n",
	)
	writeEventLogPreparationSource(t, eventsPath, legacy)

	meta := reconciliationTestMeta()
	meta.LastSequence = 1
	observer := &eventLogReconciliationTestObserver{}
	store := newEventLogReconciliationStore(t, sessionDir, meta, observer, nil)

	capability, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize legacy event log: %v", err)
	}
	if mustMaterializedRevision(capability) != 1 {
		t.Fatalf("materialized revision = %d, want 1", mustMaterializedRevision(capability))
	}
	window, err := capability.ReadRecentRecords(1)
	if err != nil {
		t.Fatalf("read materialized legacy event log: %v", err)
	}
	if len(window.Records) != 1 || window.Records[0].Seq() != 1 {
		t.Fatalf("materialized records = %#v", window.Records)
	}
	message, ok := mustEventRecordPayload(window.Records[0]).(MessageRecord)
	if !ok || message.Content == nil || *message.Content != "continue this session" {
		t.Fatalf("materialized message = %#v", mustEventRecordPayload(window.Records[0]))
	}
	if len(observer.observations) != 1 ||
		observer.observations[0].LastSequence != 1 ||
		!observer.observations[0].ConversationEstablished {
		t.Fatalf("materialization observations = %+v", observer.observations)
	}
	current, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read installed current event log: %v", err)
	}
	if len(current) == 0 || string(current) == string(legacy) {
		t.Fatalf("legacy source was not replaced: %q", current)
	}
	if snapshot := eventLogPreparationStoreSnapshot(t, store); snapshot.state != eventLogCurrent {
		t.Fatalf("materialization state = %v, want current", snapshot.state)
	}
	if _, err := os.Stat(filepath.Join(sessionDir, eventLogMigrationWorkspaceDir)); !os.IsNotExist(err) {
		t.Fatalf("migration workspace remains after success: %v", err)
	}
}

func TestMaterializeEventLogInstallsHeaderForMissingSourceAndReconcilesMetadata(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	meta := reconciliationTestMeta()
	meta.LastSequence = 9
	meta.ConversationEstablished = true
	meta.UsageState = &UsageState{InputTokens: 42}
	observer := &eventLogReconciliationTestObserver{}
	store := newEventLogReconciliationStore(t, sessionDir, meta, observer, nil)

	capability, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize missing event log: %v", err)
	}
	if mustMaterializedRevision(capability) != 0 {
		t.Fatalf("materialized revision = %d, want 0", mustMaterializedRevision(capability))
	}
	if mustConversationFreshness(capability) != ConversationFreshnessFresh {
		t.Fatalf(
			"materialized freshness = %v, want fresh",
			mustConversationFreshness(capability),
		)
	}
	if len(observer.observations) != 1 ||
		observer.observations[0].ObservedLastSequence != 9 ||
		observer.observations[0].LastSequence != 0 ||
		observer.observations[0].ConversationEstablished {
		t.Fatalf("materialization observations = %+v", observer.observations)
	}
	if mustMaterializedRevision(capability) != 0 ||
		mustConversationFreshness(capability) != ConversationFreshnessFresh {
		t.Fatalf(
			"reconciled event state = revision %d, freshness %v",
			mustMaterializedRevision(capability),
			mustConversationFreshness(capability),
		)
	}
	eventsPath := filepath.Join(sessionDir, eventsFile)
	current, err := openCurrentEventLog(
		eventsPath,
		currentEventLogReadOnly,
		eventLogOptions{},
	)
	if err != nil {
		t.Fatalf("open installed header-only event log: %v", err)
	}
	if current.lastSequence != 0 {
		t.Fatalf("installed header-only sequence = %d, want 0", current.lastSequence)
	}
	if _, err := os.Stat(filepath.Join(sessionDir, eventLogMigrationWorkspaceDir)); !os.IsNotExist(err) {
		t.Fatalf("migration workspace remains after success: %v", err)
	}
}

func TestMaterializeEventLogReplacesEmptySourceWithHeaderOnlyCurrentLog(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	eventsPath := filepath.Join(sessionDir, eventsFile)
	writeEventLogPreparationSource(t, eventsPath, nil)
	observer := &eventLogReconciliationTestObserver{}
	store := newEventLogReconciliationStore(
		t,
		sessionDir,
		reconciliationTestMeta(),
		observer,
		nil,
	)

	capability, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize empty event log: %v", err)
	}
	if mustMaterializedRevision(capability) != 0 {
		t.Fatalf("materialized revision = %d, want 0", mustMaterializedRevision(capability))
	}
	current, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read installed current event log: %v", err)
	}
	if !bytes.Equal(current, currentEventLogHeaderBytes(t)) {
		t.Fatalf("installed header-only event log = %q", current)
	}
	if observer.calls != 1 {
		t.Fatalf("reconciliation calls = %d, want 1", observer.calls)
	}
}

func TestMaterializeEventLogPreservesUnsupportedNewerSource(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	eventsPath := filepath.Join(sessionDir, eventsFile)
	source := []byte(`{"contract":"kent.session.events","version":2}` + "\n")
	writeEventLogPreparationSource(t, eventsPath, source)
	before := eventLogFingerprint(t, eventsPath)
	beforeChecksum := eventLogSHA256(t, eventsPath)
	store := newEventLogReconciliationStore(
		t,
		sessionDir,
		reconciliationTestMeta(),
		&eventLogReconciliationTestObserver{},
		nil,
	)

	_, err := store.MaterializeEventLog()
	var unsupported UnsupportedEventLogVersionError
	if !errors.As(err, &unsupported) {
		t.Fatalf("materialization error = %v, want unsupported version", err)
	}
	if unsupported.FoundVersion != 2 || unsupported.SupportedVersion != EventLogVersionV1 {
		t.Fatalf("unsupported version facts = %+v", unsupported)
	}
	after := eventLogFingerprint(t, eventsPath)
	afterChecksum := eventLogSHA256(t, eventsPath)
	if string(after.contents) != string(before.contents) ||
		after.size != before.size ||
		!after.modTime.Equal(before.modTime) ||
		afterChecksum != beforeChecksum {
		t.Fatalf("unsupported source changed: before=%+v after=%+v", before, after)
	}
}

func TestMaterializeEventLogRepairsLegacySequenceRegressionBeforeCommit(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	eventsPath := filepath.Join(sessionDir, eventsFile)
	legacy := []byte(
		`{"seq":2,"timestamp":"2026-07-19T10:00:00Z","kind":"message",` +
			`"payload":{"role":"assistant","content":"newer"}}` + "\n" +
			`{"seq":1,"timestamp":"2026-07-19T10:00:01Z","kind":"message",` +
			`"payload":{"role":"assistant","content":"older"}}` + "\n",
	)
	writeEventLogPreparationSource(t, eventsPath, legacy)
	observer := &eventLogReconciliationTestObserver{}
	store := newEventLogReconciliationStore(
		t,
		sessionDir,
		reconciliationTestMeta(),
		observer,
		nil,
	)

	capability, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize regressing legacy log: %v", err)
	}
	window, err := capability.ReadRecentRecords(2)
	if err != nil {
		t.Fatalf("read repaired current log: %v", err)
	}
	if len(window.Records) != 2 ||
		window.Records[0].Seq() != 2 ||
		window.Records[1].Seq() != 3 {
		t.Fatalf("repaired records = %#v", window.Records)
	}
	if len(observer.observations) != 1 ||
		observer.observations[0].LastSequence != 3 {
		t.Fatalf("repaired reconciliation observations = %#v", observer.observations)
	}
	current := eventLogFingerprint(t, eventsPath)
	if bytes.Equal(current.contents, legacy) {
		t.Fatal("legacy source remained installed after successful migration")
	}
	lines := bytes.Split(bytes.TrimSpace(current.contents), []byte{'\n'})
	if len(lines) != 3 {
		t.Fatalf("installed current line count = %d, want header plus two records", len(lines))
	}
	if _, err := decodeEventLogHeader(lines[0]); err != nil {
		t.Fatalf("installed event log header: %v", err)
	}
}

func TestMaterializeEventLogRetriesCommittedReconciliationWithoutRemigration(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	eventsPath := filepath.Join(sessionDir, eventsFile)
	legacy := []byte(
		`{"seq":1,"timestamp":"2026-07-19T10:00:00Z","kind":"message",` +
			`"payload":{"role":"user","content":"retry reconciliation"}}` + "\n",
	)
	writeEventLogPreparationSource(t, eventsPath, legacy)
	meta := reconciliationTestMeta()
	meta.LastSequence = 1
	observer := &eventLogReconciliationTestObserver{err: context.DeadlineExceeded}
	store := newEventLogReconciliationStore(t, sessionDir, meta, observer, nil)

	_, err := store.MaterializeEventLog()
	var materializationErr *EventLogMaterializationError
	if !errors.As(err, &materializationErr) ||
		!materializationErr.Committed ||
		!materializationErr.PendingRepair {
		t.Fatalf("first materialization error = %+v / %v", materializationErr, err)
	}
	installed := eventLogFingerprint(t, eventsPath)
	if string(installed.contents) == string(legacy) {
		t.Fatal("legacy source remained installed after committed reconciliation failure")
	}

	observer.err = nil
	capability, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("retry committed reconciliation: %v", err)
	}
	if mustMaterializedRevision(capability) != 1 {
		t.Fatalf("retried capability revision = %d, want 1", mustMaterializedRevision(capability))
	}
	retried := eventLogFingerprint(t, eventsPath)
	if string(retried.contents) != string(installed.contents) ||
		retried.size != installed.size ||
		!retried.modTime.Equal(installed.modTime) {
		t.Fatalf("retry remigrated current source: installed=%+v retried=%+v", installed, retried)
	}
	if observer.calls != 2 {
		t.Fatalf("reconciliation calls = %d, want failure plus retry", observer.calls)
	}
}

func TestMaterializeEventLogPreservesLegacySourceOnMalformedRecord(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	eventsPath := filepath.Join(sessionDir, eventsFile)
	legacy := []byte(
		`{"seq":1,"timestamp":"2026-07-19T10:00:00Z","kind":"message",` +
			`"payload":{"role":"user","content":"valid"}}` + "\n" +
			`{"seq":2,"timestamp":"2026-07-19T10:00:01Z","kind":"message","payload":not-json}` + "\n",
	)
	writeEventLogPreparationSource(t, eventsPath, legacy)
	before := eventLogFingerprint(t, eventsPath)
	beforeChecksum := eventLogSHA256(t, eventsPath)
	store := newEventLogReconciliationStore(
		t,
		sessionDir,
		reconciliationTestMeta(),
		&eventLogReconciliationTestObserver{},
		nil,
	)

	_, err := store.MaterializeEventLog()
	var materializationErr *EventLogMaterializationError
	if !errors.As(err, &materializationErr) {
		t.Fatalf("materialization error is not typed: %v", err)
	}
	if materializationErr.Committed || materializationErr.PendingRepair ||
		materializationErr.Stage != EventLogMaterializationStagePreparation {
		t.Fatalf("malformed legacy commit facts = %+v", materializationErr)
	}
	after := eventLogFingerprint(t, eventsPath)
	afterChecksum := eventLogSHA256(t, eventsPath)
	if string(after.contents) != string(before.contents) ||
		after.size != before.size ||
		!after.modTime.Equal(before.modTime) ||
		afterChecksum != beforeChecksum {
		t.Fatalf("malformed legacy source changed: before=%+v after=%+v", before, after)
	}
	if _, err := os.Stat(filepath.Join(sessionDir, eventLogMigrationWorkspaceDir)); !os.IsNotExist(err) {
		t.Fatalf("migration workspace remains after pre-commit failure: %v", err)
	}
}

func TestMaterializeEventLogPreservesPresentProviderRawLexically(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	eventsPath := filepath.Join(sessionDir, eventsFile)
	raw := json.RawMessage(
		`{ "type" : "function_call_output", "call_id" : "call-1", "output" : "\u0064one" }`,
	)
	legacy := []byte(
		`{"seq":1,"timestamp":"2026-07-19T10:00:00Z","kind":"tool_completed","payload":{` +
			`"call_id":"call-1","name":"exec_command","is_error":false,"output":"done",` +
			`"provider_items":[{"type":"function_call_output","call_id":"call-1","output":"done","raw":` +
			string(raw) + `}]}}` + "\n",
	)
	writeEventLogPreparationSource(t, eventsPath, legacy)
	meta := reconciliationTestMeta()
	meta.LastSequence = 1
	store := newEventLogReconciliationStore(
		t,
		sessionDir,
		meta,
		&eventLogReconciliationTestObserver{},
		nil,
	)

	capability, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize legacy provider Raw: %v", err)
	}
	window, err := capability.ReadRecentRecords(1)
	if err != nil {
		t.Fatalf("read materialized provider Raw: %v", err)
	}
	completion, ok := mustEventRecordPayload(window.Records[0]).(ToolCompletionRecord)
	if !ok || len(completion.ProviderItems) != 1 {
		t.Fatalf("materialized completion = %#v", mustEventRecordPayload(window.Records[0]))
	}
	if !bytes.Equal(completion.ProviderItems[0].Raw, raw) {
		t.Fatalf(
			"materialized provider Raw changed: got=%s want=%s",
			completion.ProviderItems[0].Raw,
			raw,
		)
	}
}

func TestMaterializeEventLogGeneratesMissingSupportedProviderRaw(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	eventsPath := filepath.Join(sessionDir, eventsFile)
	legacy := []byte(
		`{"seq":1,"timestamp":"2026-07-19T10:00:00Z","kind":"tool_completed","payload":{` +
			`"call_id":"custom-1","name":"patch","is_error":false,"output":"patched",` +
			`"provider_items":[{"type":"custom_tool_call_output","call_id":"custom-1","output":"patched"}]}}` + "\n",
	)
	writeEventLogPreparationSource(t, eventsPath, legacy)
	meta := reconciliationTestMeta()
	meta.LastSequence = 1
	store := newEventLogReconciliationStore(
		t,
		sessionDir,
		meta,
		&eventLogReconciliationTestObserver{},
		nil,
	)

	capability, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize generated provider Raw: %v", err)
	}
	window, err := capability.ReadRecentRecords(1)
	if err != nil {
		t.Fatalf("read generated provider Raw: %v", err)
	}
	completion, ok := mustEventRecordPayload(window.Records[0]).(ToolCompletionRecord)
	if !ok || len(completion.ProviderItems) != 1 {
		t.Fatalf("materialized completion = %#v", mustEventRecordPayload(window.Records[0]))
	}
	want, err := openaiwire.NewCustomToolOutput(
		"custom-1",
		json.RawMessage(`"patched"`),
	)
	if err != nil {
		t.Fatalf("build provider Raw oracle: %v", err)
	}
	if !bytes.Equal(completion.ProviderItems[0].Raw, want.Bytes()) {
		t.Fatalf(
			"generated provider Raw changed: got=%s want=%s",
			completion.ProviderItems[0].Raw,
			want.Bytes(),
		)
	}
}

func TestMaterializeEventLogCorrelatesAbsentProviderSnapshot(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	eventsPath := filepath.Join(sessionDir, eventsFile)
	legacy := []byte(
		`{"seq":1,"timestamp":"2026-07-19T10:00:00Z","kind":"message","payload":{` +
			`"role":"assistant","tool_calls":[{"id":"call-1","name":"patch","custom":true,` +
			`"custom_input":"*** Begin Patch\n*** End Patch","input":{"patch":"value"}}]}}` + "\n" +
			`{"seq":2,"timestamp":"2026-07-19T10:00:01Z","kind":"tool_completed","payload":{` +
			`"call_id":"call-1","name":"patch","is_error":false,"output":"patched"}}` + "\n",
	)
	writeEventLogPreparationSource(t, eventsPath, legacy)
	meta := reconciliationTestMeta()
	meta.LastSequence = 2
	store := newEventLogReconciliationStore(
		t,
		sessionDir,
		meta,
		&eventLogReconciliationTestObserver{},
		nil,
	)

	capability, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize absent provider snapshot: %v", err)
	}
	window, err := capability.ReadRecentRecords(2)
	if err != nil {
		t.Fatalf("read correlated provider snapshot: %v", err)
	}
	if len(window.Records) != 2 {
		t.Fatalf("materialized record count = %d, want 2", len(window.Records))
	}
	completion, ok := mustEventRecordPayload(window.Records[1]).(ToolCompletionRecord)
	if !ok || completion.OutputKind != ToolOutputKindCustom ||
		len(completion.ProviderItems) != 1 {
		t.Fatalf("materialized completion = %#v", mustEventRecordPayload(window.Records[1]))
	}
	want, err := openaiwire.NewCustomToolOutput(
		"call-1",
		json.RawMessage(`"patched"`),
	)
	if err != nil {
		t.Fatalf("build fallback provider Raw oracle: %v", err)
	}
	if !bytes.Equal(completion.ProviderItems[0].Raw, want.Bytes()) {
		t.Fatalf(
			"fallback provider Raw changed: got=%s want=%s",
			completion.ProviderItems[0].Raw,
			want.Bytes(),
		)
	}
	if _, err := os.Stat(filepath.Join(sessionDir, eventLogMigrationWorkspaceDir)); !os.IsNotExist(err) {
		t.Fatalf("migration workspace remains after fallback success: %v", err)
	}
}

func TestMaterializeEventLogRefreshesConflictWithoutRemigratingInstalledLog(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	eventsPath := filepath.Join(sessionDir, eventsFile)
	legacy := []byte(
		`{"seq":1,"timestamp":"2026-07-19T10:00:00Z","kind":"message",` +
			`"payload":{"role":"user","content":"refresh metadata"}}` + "\n",
	)
	writeEventLogPreparationSource(t, eventsPath, legacy)
	meta := reconciliationTestMeta()
	meta.LastSequence = 1
	resolver := &eventLogReconciliationTestResolver{record: PersistedSessionRecord{
		SessionDir: sessionDir,
		Meta:       &meta,
	}}
	var installed eventLogFileFingerprint
	observer := &eventLogReconciliationTestObserver{
		conflictFirst: true,
		onConflict: func() {
			installed = eventLogFingerprint(t, eventsPath)
			refreshed := meta
			resolver.record.Meta = &refreshed
		},
	}
	store := newEventLogReconciliationStore(t, sessionDir, meta, observer, resolver)

	capability, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize after reconciliation conflict: %v", err)
	}
	if mustMaterializedRevision(capability) != 1 {
		t.Fatalf("materialized revision = %d, want 1", mustMaterializedRevision(capability))
	}
	if observer.calls != 2 {
		t.Fatalf("reconciliation calls = %d, want conflict plus retry", observer.calls)
	}
	after := eventLogFingerprint(t, eventsPath)
	if string(after.contents) != string(installed.contents) ||
		after.size != installed.size ||
		!after.modTime.Equal(installed.modTime) {
		t.Fatalf("conflict retry remigrated installed log: installed=%+v after=%+v", installed, after)
	}
}

func TestMaterializeEventLogCapabilityFailureDoesNotClaimPendingMetadataRepair(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	eventsPath := filepath.Join(sessionDir, eventsFile)
	createCurrentEventLogPreparationSource(t, eventsPath)
	observer := eventLogReconciliationObserverFunc(func(
		_ context.Context,
		_ PersistedEventLogReconciliation,
	) error {
		return os.Remove(eventsPath)
	})
	store := newEventLogReconciliationStore(
		t,
		sessionDir,
		reconciliationTestMeta(),
		observer,
		nil,
	)

	_, err := store.MaterializeEventLog()
	var materializationErr *EventLogMaterializationError
	if !errors.As(err, &materializationErr) {
		t.Fatalf("capability issuance error is not typed: %v", err)
	}
	if !materializationErr.Committed ||
		materializationErr.PendingRepair ||
		materializationErr.Stage != EventLogMaterializationStageReconciliation {
		t.Fatalf("capability issuance facts = %+v", materializationErr)
	}
}

func TestMaterializeEventLogPreservesCacheContinuityFacts(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	eventsPath := filepath.Join(sessionDir, eventsFile)
	terminalHash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	historyRaw := json.RawMessage(
		`{ "type" : "future_provider_item", "cache_fact" : "\u006bent" }`,
	)
	legacy := []byte(
		`{"seq":1,"timestamp":"2026-07-19T10:00:00Z","kind":"history_replaced","payload":{` +
			`"engine":"local","mode":"handoff","items":[{"type":"other","raw":` +
			string(historyRaw) + `}]}}` + "\n" +
			`{"seq":2,"timestamp":"2026-07-19T10:00:01Z","kind":"cache_request_observed","payload":{` +
			`"cache_key":"cache-1","chunk_count":2,"terminal_hash":"` + terminalHash + `"}}` + "\n" +
			`{"seq":3,"timestamp":"2026-07-19T10:00:02Z","kind":"cache_response_observed","payload":{` +
			`"cache_key":"cache-1","chunk_count":2,"terminal_hash":"` + terminalHash + `",` +
			`"has_cached_input_tokens":true,"cached_input_tokens":0}}` + "\n" +
			`{"seq":4,"timestamp":"2026-07-19T10:00:03Z","kind":"cache_warning","payload":{` +
			`"reason":"compaction","cache_key":"cache-1","lost_input_tokens":7}}` + "\n",
	)
	writeEventLogPreparationSource(t, eventsPath, legacy)
	meta := reconciliationTestMeta()
	meta.LastSequence = 4
	store := newEventLogReconciliationStore(
		t,
		sessionDir,
		meta,
		&eventLogReconciliationTestObserver{},
		nil,
	)

	capability, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize cache continuity facts: %v", err)
	}
	window, err := capability.ReadRecentRecords(4)
	if err != nil {
		t.Fatalf("read cache continuity facts: %v", err)
	}
	if len(window.Records) != 4 {
		t.Fatalf("materialized record count = %d, want 4", len(window.Records))
	}
	history := mustEventRecordPayload(window.Records[0]).(HistoryReplacementRecord)
	if len(history.Items) != 1 || !bytes.Equal(history.Items[0].Raw, historyRaw) {
		t.Fatalf("materialized history cache Raw = %#v", history)
	}
	request := mustEventRecordPayload(window.Records[1]).(CacheRequestObservationRecord)
	if request.DigestVersion != CacheDigestV1 ||
		request.Scope != CacheScopeConversation ||
		request.CacheKey != "cache-1" ||
		request.TerminalHash != terminalHash {
		t.Fatalf("materialized cache request = %#v", request)
	}
	response := mustEventRecordPayload(window.Records[2]).(CacheResponseObservationRecord)
	if response.DigestVersion != CacheDigestV1 ||
		response.Scope != CacheScopeConversation ||
		response.CachedInputTokens == nil ||
		*response.CachedInputTokens != 0 {
		t.Fatalf("materialized cache response = %#v", response)
	}
	warning := mustEventRecordPayload(window.Records[3]).(CacheWarningRecord)
	if warning.Scope != CacheScopeConversation ||
		warning.Reason != CacheWarningReasonCompaction ||
		warning.CacheKey == nil ||
		*warning.CacheKey != "cache-1" ||
		warning.LostInputTokens == nil ||
		*warning.LostInputTokens != 7 {
		t.Fatalf("materialized cache warning = %#v", warning)
	}
}

func eventLogSHA256(t *testing.T, path string) [sha256.Size]byte {
	t.Helper()
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read event log for checksum: %v", err)
	}
	return sha256.Sum256(source)
}
