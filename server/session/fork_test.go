package session

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"core/shared/rollbacktarget"
	"core/shared/runtimeids"
	"core/shared/sessioncontract"
)

func forkUserMessageRecord(content string) MessageRecord {
	return MessageRecord{
		Role:    MessageRoleUser,
		Content: forkStringPointer(content),
	}
}

func userMessagePayload(t *testing.T, content string) MessageRecord {
	t.Helper()
	return forkUserMessageRecord(content)
}

func forkStringPointer(value string) *string {
	return &value
}

func forkMessageTypePointer(value MessageType) *MessageType {
	return &value
}

func materializedForkEventLog(t *testing.T, store *Store) MaterializedEventLog {
	t.Helper()
	log, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	return log
}

func appendForkTestRecords(
	t *testing.T,
	log MaterializedEventLog,
	userMessages int,
	perMessageFiller int,
) {
	t.Helper()
	for i := 0; i < userMessages; i++ {
		if _, _, err := log.AppendRecord(
			forkStringPointer("step"),
			forkUserMessageRecord("prompt"),
		); err != nil {
			t.Fatalf("append user message %d: %v", i, err)
		}
		for f := 0; f < perMessageFiller; f++ {
			if _, _, err := log.AppendRecord(forkStringPointer("step"), LocalEntryRecord{
				Visibility: EntryVisibilityHidden,
				Role:       "test",
				Text:       "filler",
			}); err != nil {
				t.Fatalf("append filler %d/%d: %v", i, f, err)
			}
		}
	}
}

func collectForkRecords(t *testing.T, log MaterializedEventLog) []EventRecord {
	t.Helper()
	var records []EventRecord
	if err := log.WalkRecords(func(record EventRecord) error {
		records = append(records, record)
		return nil
	}); err != nil {
		t.Fatalf("walk typed records: %v", err)
	}
	return records
}

type forkReplayCountingPersistence struct {
	base     *testSessionMetadata
	mu       sync.Mutex
	parentID string
	childSeq []int64
}

func newForkReplayCountingPersistence() *forkReplayCountingPersistence {
	return &forkReplayCountingPersistence{
		base: &testSessionMetadata{records: map[string]PersistedSessionRecord{}},
	}
}

func (p *forkReplayCountingPersistence) ObservePersistedStore(
	ctx context.Context,
	snapshot PersistedStoreSnapshot,
) error {
	if err := p.base.ObservePersistedStore(ctx, snapshot); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.parentID != "" &&
		snapshot.Meta.SessionID != p.parentID &&
		snapshot.Meta.LastSequence > 0 {
		if len(p.childSeq) == 0 ||
			p.childSeq[len(p.childSeq)-1] != snapshot.Meta.LastSequence {
			p.childSeq = append(p.childSeq, snapshot.Meta.LastSequence)
		}
	}
	return nil
}

func (p *forkReplayCountingPersistence) ObserveEventLogReconciliation(
	ctx context.Context,
	reconciliation PersistedEventLogReconciliation,
) error {
	return p.base.ObserveEventLogReconciliation(ctx, reconciliation)
}

func (p *forkReplayCountingPersistence) ResolvePersistedSession(
	ctx context.Context,
	sessionID string,
) (PersistedSessionRecord, error) {
	return p.base.ResolvePersistedSession(ctx, sessionID)
}

func (p *forkReplayCountingPersistence) options() []StoreOption {
	return []StoreOption{
		WithPersistenceObserver(p),
		WithPersistedSessionResolver(p),
	}
}

func (p *forkReplayCountingPersistence) startChildCapture(parentID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.parentID = parentID
	p.childSeq = nil
}

func (p *forkReplayCountingPersistence) childSequences() []int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int64(nil), p.childSeq...)
}

func TestCloneSessionStreamsLargeHistoryAcrossChunks(t *testing.T) {
	parent := newSessionTestStore(t)
	markSessionTestLocked(t, parent, sessionTestLockedContract())
	parentLog := materializedForkEventLog(t, parent)
	appendForkTestRecords(t, parentLog, 6, 4)
	if _, _, err := parentLog.AppendRecord(forkStringPointer("step"), MessageRecord{
		Role:        MessageRoleDeveloper,
		MessageType: forkMessageTypePointer(MessageTypeHeadlessMode),
		Content:     forkStringPointer("x"),
	}); err != nil {
		t.Fatalf("append headless marker: %v", err)
	}

	parentRecords := collectForkRecords(t, parentLog)
	child, err := CloneSession(parentLog, "clone", testSessionCategory)
	if err != nil {
		t.Fatalf("clone session: %v", err)
	}
	childLog := materializedForkEventLog(t, child)
	childRecords := collectForkRecords(t, childLog)
	if !reflect.DeepEqual(parentRecords, childRecords) {
		t.Fatalf("cloned child must replay the full parent history: parent=%d child=%d", len(parentRecords), len(childRecords))
	}
	parentID, err := runtimeids.ParseSessionID(parent.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID parent: %v", err)
	}
	if child.Meta().PreviousSessionID == nil || *child.Meta().PreviousSessionID != parentID {
		t.Fatalf("child previous id = %v, want %q", child.Meta().PreviousSessionID, parent.Meta().SessionID)
	}
	if !child.Meta().HeadlessActive {
		t.Fatal("expected cloned child to inherit headless-active state derived from replay")
	}
	if locked := child.Meta().Locked; locked == nil ||
		locked.WorkflowCompletionMode == nil ||
		*locked.WorkflowCompletionMode != sessioncontract.WorkflowCompletionModeTool {
		t.Fatalf("cloned workflow completion mode = %+v, want inherited tool mode", locked)
	}
}

func TestStreamedForkAndCloneRequireAndPersistCategory(t *testing.T) {
	parent, err := Create(t.TempDir(), "workspace", "/tmp/work", sessioncontract.SessionCategoryMain, sessionTestPersistence.options()...)
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	parentLog := materializedForkEventLog(t, parent)
	target, _, err := parentLog.AppendRecord(forkStringPointer("step"), forkUserMessageRecord("fork target"))
	if err != nil {
		t.Fatalf("append fork target: %v", err)
	}

	forked, _, err := ForkAtUserMessage(
		parentLog,
		target.Seq(),
		"interactive fork",
		sessioncontract.SessionCategoryMain,
	)
	if err != nil {
		t.Fatalf("fork session: %v", err)
	}
	if got := forked.Meta().Category; got == nil || *got != sessioncontract.SessionCategoryMain {
		t.Fatalf("fork category = %v, want main", got)
	}

	cloned, err := CloneSession(parentLog, "workflow clone", sessioncontract.SessionCategorySubagent)
	if err != nil {
		t.Fatalf("clone session: %v", err)
	}
	if got := cloned.Meta().Category; got == nil || *got != sessioncontract.SessionCategorySubagent {
		t.Fatalf("clone category = %v, want subagent", got)
	}
}

func TestForkAtUserMessageStreamsPrefixAcrossChunks(t *testing.T) {
	parent := newSessionTestStore(t)
	parentLog := materializedForkEventLog(t, parent)
	appendForkTestRecords(t, parentLog, 4, 3)

	parentRecords := collectForkRecords(t, parentLog)
	const forkIndex = 3
	expected := make([]EventRecord, 0)
	visible := 0
	var forkSeq int64
	for _, record := range parentRecords {
		isVisible, err := isForkVisibleUserMessage(record)
		if err != nil {
			t.Fatalf("inspect fork-visible user record: %v", err)
		}
		if isVisible {
			visible++
			if visible == forkIndex {
				forkSeq = record.Seq()
				break
			}
		}
		expected = append(expected, record)
	}
	child, ordinal, err := ForkAtUserMessage(parentLog, forkSeq, "fork", testSessionCategory)
	if err != nil {
		t.Fatalf("fork at user message: %v", err)
	}
	if ordinal != forkIndex {
		t.Fatalf("fork ordinal = %d, want %d", ordinal, forkIndex)
	}
	childLog := materializedForkEventLog(t, child)
	childRecords := collectForkRecords(t, childLog)
	if !reflect.DeepEqual(expected, childRecords) {
		t.Fatalf("fork child must replay the prefix before user message %d: want %d records, got %d", forkIndex, len(expected), len(childRecords))
	}
}

func TestForkAtUserMessageOutOfRangeCleansUpChild(t *testing.T) {
	root := t.TempDir()
	parent := newSessionTestStoreAt(t, root)
	parentLog := materializedForkEventLog(t, parent)
	appendForkTestRecords(t, parentLog, 2, 4)

	if _, _, err := ForkAtUserMessage(parentLog, 999999, "fork", testSessionCategory); err == nil {
		t.Fatal("expected out-of-range fork to fail")
	}
	assertOnlyForkParentSessionDir(t, root, parent)
}

func TestCloneSessionFlushesAtCountAndByteBudgets(t *testing.T) {
	tests := []struct {
		name         string
		payloads     []EventRecordPayload
		wantChildSeq []int64
	}{
		{
			name: "event count",
			payloads: func() []EventRecordPayload {
				payloads := make([]EventRecordPayload, forkReplayFlushEventCount+1)
				for index := range payloads {
					payloads[index] = LocalEntryRecord{
						Visibility: EntryVisibilityHidden,
						Role:       "test",
						Text:       "count bounded",
					}
				}
				return payloads
			}(),
			wantChildSeq: []int64{
				int64(forkReplayFlushEventCount),
				int64(forkReplayFlushEventCount + 1),
			},
		},
		{
			name: "encoded bytes",
			payloads: []EventRecordPayload{
				largeForkLocalEntry(),
				largeForkLocalEntry(),
				largeForkLocalEntry(),
			},
			wantChildSeq: []int64{1, 2, 3},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			persistence := newForkReplayCountingPersistence()
			parent, err := Create(
				t.TempDir(),
				"workspace",
				"/tmp/work",
				testSessionCategory,
				persistence.options()...,
			)
			if err != nil {
				t.Fatalf("create parent: %v", err)
			}
			if err := parent.EnsureDurable(); err != nil {
				t.Fatalf("persist parent: %v", err)
			}
			parentLog := materializedForkEventLog(t, parent)
			stepID := "step"
			if _, receipt, err := parentLog.AppendRecordsAtomic(
				&stepID,
				test.payloads,
			); err != nil || !receipt.Committed {
				t.Fatalf("append parent fixtures: receipt=%+v err=%v", receipt, err)
			}
			persistence.startChildCapture(parent.Meta().SessionID)

			child, err := CloneSession(parentLog, "clone", testSessionCategory)
			if err != nil {
				t.Fatalf("clone session: %v", err)
			}
			if got := persistence.childSequences(); !reflect.DeepEqual(
				got,
				test.wantChildSeq,
			) {
				t.Fatalf(
					"child replay flush revisions = %v, want %v",
					got,
					test.wantChildSeq,
				)
			}
			assertNoForkTemporaryArtifacts(t, parent.Dir())
			assertNoForkTemporaryArtifacts(t, child.Dir())
		})
	}
}

func TestForkReplayBatchReportsDeterministicRetainedMaxima(t *testing.T) {
	batch := newForkReplayBatch()
	first := mustForkRecord(t, 1, largeForkLocalEntry())
	firstBytes, err := replayRecordByteSize(first)
	if err != nil {
		t.Fatalf("size first replay record: %v", err)
	}
	if err := batch.add(first, firstBytes); err != nil {
		t.Fatalf("retain first replay record: %v", err)
	}
	second := mustForkRecord(t, 2, largeForkLocalEntry())
	secondBytes, err := replayRecordByteSize(second)
	if err != nil {
		t.Fatalf("size second replay record: %v", err)
	}
	if !batch.shouldFlushBefore(secondBytes) {
		t.Fatal("replay batch did not require a byte-budget flush")
	}
	batch.reset()
	if err := batch.add(second, secondBytes); err != nil {
		t.Fatalf("retain second replay record: %v", err)
	}
	batch.reset()
	if err := batch.validateDrained(); err != nil {
		t.Fatalf("validate drained replay batch: %v", err)
	}

	stats := batch.snapshot()
	if stats.MaxBufferedRecords != 1 ||
		stats.MaxBufferedEncodedBytes != max(firstBytes, secondBytes) ||
		stats.LargestRecordBytes != max(firstBytes, secondBytes) {
		t.Fatalf("fork replay resource stats = %+v", stats)
	}
	for index, record := range batch.records[:cap(batch.records)] {
		if record.payload != nil {
			t.Fatalf("reset replay batch retained payload at slot %d", index)
		}
	}
}

func mustForkRecord(
	t *testing.T,
	sequence int64,
	payload EventRecordPayload,
) EventRecord {
	t.Helper()
	record, err := NewEventRecord(sequence, forkStringPointer("step"), payload)
	if err != nil {
		t.Fatalf("build fork fixture record %d: %v", sequence, err)
	}
	return record
}

func largeForkLocalEntry() LocalEntryRecord {
	return LocalEntryRecord{
		Visibility: EntryVisibilityHidden,
		Role:       "test",
		Text:       strings.Repeat("x", forkReplayFlushByteBudget*3/5),
	}
}

func assertNoForkTemporaryArtifacts(t *testing.T, sessionDir string) {
	t.Helper()
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		t.Fatalf("read fork session directory %q: %v", sessionDir, err)
	}
	for _, entry := range entries {
		switch entry.Name() {
		case eventsFile, eventLogPersistenceLockFile:
		default:
			t.Fatalf(
				"fork replay left unexpected temporary disk artifact %q in %q",
				entry.Name(),
				sessionDir,
			)
		}
	}
}

func TestForkAtUserMessageRebasesTypedHistoryReplacementRollbackCandidate(t *testing.T) {
	root := t.TempDir()
	parent := newSessionTestStoreAt(t, root)
	parentLog := materializedForkEventLog(t, parent)
	first, _, err := parentLog.AppendRecord(
		forkStringPointer("step"),
		forkUserMessageRecord("before compaction"),
	)
	if err != nil {
		t.Fatalf("append first user message: %v", err)
	}
	if _, _, err := parentLog.AppendRecord(forkStringPointer("step"), MessageRecord{
		Role:        MessageRoleDeveloper,
		MessageType: forkMessageTypePointer(MessageTypeCompactionSoonReminder),
		Content:     forkStringPointer("compact now"),
	}); err != nil {
		t.Fatalf("append compaction reminder: %v", err)
	}
	if _, _, err := parentLog.AppendCompactionHistoryReplacement(
		forkStringPointer("step"),
		HistoryReplacementRecord{
			Engine: "local",
			Mode:   CompactionModeAuto,
			LatestRollbackCandidate: &rollbacktarget.CandidateLocator{
				UserMessageSeq:       first.Seq(),
				CandidatePageEndByte: 1,
			},
		},
	); err != nil {
		t.Fatalf("append history replacement: %v", err)
	}
	target, _, err := parentLog.AppendRecord(forkStringPointer("step"), forkUserMessageRecord("fork target"))
	if err != nil {
		t.Fatalf("append fork target: %v", err)
	}

	child, _, err := ForkAtUserMessage(parentLog, target.Seq(), "fork", testSessionCategory)
	if err != nil {
		t.Fatalf("fork session: %v", err)
	}
	childLog := materializedForkEventLog(t, child)
	childRecords := collectForkRecords(t, childLog)
	if len(childRecords) != 3 {
		t.Fatalf("child record count = %d, want 3", len(childRecords))
	}
	replacement, ok := mustEventRecordPayload(childRecords[2]).(HistoryReplacementRecord)
	if !ok {
		t.Fatalf("child record payload = %T, want HistoryReplacementRecord", mustEventRecordPayload(childRecords[1]))
	}
	if replacement.LatestRollbackCandidate == nil {
		t.Fatal("rebased history replacement must retain its rollback candidate")
	}
	reminderWindow, err := childLog.ReadSegmentForward(0, func(record EventRecord) bool {
		return record.Seq() == childRecords[2].Seq()
	})
	if err != nil {
		t.Fatalf("read child reminder cursor: %v", err)
	}
	if replacement.LatestRollbackCandidate.UserMessageSeq != childRecords[0].Seq() ||
		replacement.LatestRollbackCandidate.CandidatePageEndByte != reminderWindow.EndOffset {
		t.Fatalf(
			"rebased rollback candidate = %+v, child user record = %+v, reminder end cursor = %d",
			replacement.LatestRollbackCandidate,
			childRecords[0],
			reminderWindow.EndOffset,
		)
	}
	if child.Meta().CompactionSoonReminderIssued {
		t.Fatal("history replacement must clear replay-derived compaction reminder state")
	}
}

func TestForkRebasesLatestCandidateAcrossMultipleCandidatesAndFiller(t *testing.T) {
	parent := newSessionTestStore(t)
	parentLog := materializedForkEventLog(t, parent)
	payloads := []EventRecordPayload{
		forkUserMessageRecord("first candidate"),
		LocalEntryRecord{
			Visibility: EntryVisibilityHidden,
			Role:       "test",
			Text:       "after first",
		},
		forkUserMessageRecord("latest candidate"),
		LocalEntryRecord{
			Visibility: EntryVisibilityHidden,
			Role:       "test",
			Text:       "after latest",
		},
		HistoryReplacementRecord{
			Engine: "local",
			Mode:   CompactionModeAuto,
		},
		forkUserMessageRecord("fork target"),
	}
	stepID := "step"
	appended, receipt, err := parentLog.AppendRecordsAtomic(&stepID, payloads)
	if err != nil || !receipt.Committed {
		t.Fatalf("append fork fixtures: receipt=%+v err=%v", receipt, err)
	}

	child, ordinal, err := ForkAtUserMessage(
		parentLog,
		appended[len(appended)-1].Seq(),
		"fork",
		testSessionCategory,
	)
	if err != nil {
		t.Fatalf("fork session: %v", err)
	}
	if ordinal != 3 {
		t.Fatalf("fork ordinal = %d, want 3", ordinal)
	}
	childLog := materializedForkEventLog(t, child)
	records := collectForkRecords(t, childLog)
	if len(records) != 5 {
		t.Fatalf("child record count = %d, want 5", len(records))
	}
	replacement, ok := mustEventRecordPayload(records[4]).(HistoryReplacementRecord)
	if !ok || replacement.LatestRollbackCandidate == nil {
		t.Fatalf(
			"rebased replacement = %#v, want rollback candidate",
			mustEventRecordPayload(records[4]),
		)
	}
	candidatePage, err := childLog.ReadSegmentForward(
		0,
		func(record EventRecord) bool {
			return record.Seq() == records[4].Seq()
		},
	)
	if err != nil {
		t.Fatalf("read candidate page: %v", err)
	}
	if replacement.LatestRollbackCandidate.UserMessageSeq != records[2].Seq() ||
		replacement.LatestRollbackCandidate.CandidatePageEndByte != candidatePage.EndOffset {
		t.Fatalf(
			"rebased latest candidate = %+v, latest user seq=%d page end=%d",
			replacement.LatestRollbackCandidate,
			records[2].Seq(),
			candidatePage.EndOffset,
		)
	}
}

func TestCloneSessionDerivesLatestHeadlessAndReminderState(t *testing.T) {
	parent := newSessionTestStore(t)
	parentLog := materializedForkEventLog(t, parent)
	payloads := []EventRecordPayload{
		forkTypedMessage(
			MessageTypeHeadlessMode,
			"enter headless",
		),
		forkTypedMessage(
			MessageTypeCompactionSoonReminder,
			"first reminder",
		),
		HistoryReplacementRecord{
			Engine: "local",
			Mode:   CompactionModeAuto,
		},
		forkTypedMessage(
			MessageTypeCompactionSoonReminder,
			"reminder after compaction",
		),
		forkTypedMessage(
			MessageTypeHeadlessModeExit,
			"exit headless",
		),
	}
	stepID := "step"
	if _, receipt, err := parentLog.AppendRecordsAtomic(
		&stepID,
		payloads,
	); err != nil || !receipt.Committed {
		t.Fatalf("append state fixtures: receipt=%+v err=%v", receipt, err)
	}

	child, err := CloneSession(parentLog, "clone", testSessionCategory)
	if err != nil {
		t.Fatalf("clone session: %v", err)
	}
	if child.Meta().HeadlessActive {
		t.Fatal("headless exit was not reflected in cloned state")
	}
	if !child.Meta().CompactionSoonReminderIssued {
		t.Fatal("post-compaction reminder was not reflected in cloned state")
	}
}

func forkTypedMessage(messageType MessageType, content string) MessageRecord {
	return MessageRecord{
		Role:        MessageRoleDeveloper,
		MessageType: forkMessageTypePointer(messageType),
		Content:     forkStringPointer(content),
	}
}

func assertOnlyForkParentSessionDir(t *testing.T, root string, parent *Store) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read container dir: %v", err)
	}
	sessionDirs := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			sessionDirs = append(sessionDirs, entry.Name())
		}
	}
	if len(sessionDirs) != 1 || sessionDirs[0] != parent.Meta().SessionID {
		t.Fatalf("expected only the parent session dir to remain, got %v", sessionDirs)
	}
	if _, err := os.Stat(filepath.Join(root, parent.Meta().SessionID)); err != nil {
		t.Fatalf("parent session dir must survive a failed fork: %v", err)
	}
}

func TestCloneSessionWithoutEventsPersistsEmptyChild(t *testing.T) {
	parent := newSessionTestStore(t)
	child, err := CloneSession(materializedForkEventLog(t, parent), "clone", testSessionCategory)
	if err != nil {
		t.Fatalf("clone empty session: %v", err)
	}
	records := collectForkRecords(t, materializedForkEventLog(t, child))
	if len(records) != 0 {
		t.Fatalf("expected empty cloned child, got %d records", len(records))
	}
	if _, err := os.Stat(filepath.Join(child.Dir(), eventsFile)); err != nil {
		t.Fatalf("empty cloned child must be durable: %v", err)
	}
	parentID, err := runtimeids.ParseSessionID(parent.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID parent: %v", err)
	}
	if child.Meta().PreviousSessionID == nil || *child.Meta().PreviousSessionID != parentID {
		t.Fatalf("child previous id = %v, want %q", child.Meta().PreviousSessionID, parent.Meta().SessionID)
	}
}
