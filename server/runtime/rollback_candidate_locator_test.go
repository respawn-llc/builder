package runtime

import (
	"encoding/json"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/rollbacktarget"
	"core/shared/sessioncontract"
	"core/shared/textutil"
)

func TestLatestRollbackCandidateLocatorSurvivesCandidateFreeCompactionsAndRestart(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{})

	if err := steerTestActiveStep(eng,
		"user-step",
		steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("candidate before several compactions")}}),
	); err != nil {
		t.Fatalf("persist rollback candidate: %v", err)
	}
	beforeCompaction := mustEngineNewestSegmentPage(t, eng)
	locator := beforeCompaction.LatestRollbackCandidate
	if locator == nil {
		t.Fatal("persisted user message did not establish a rollback candidate locator")
	}
	if err := locator.Validate(); err != nil {
		t.Fatalf("live rollback candidate locator is invalid: %v", err)
	}

	for index := 0; index < 3; index++ {
		if err := runTestActiveStep(eng, "compact-step", func() error {
			_, err := newCompactionPersistence(eng).replaceHistory(
				"compact-step",
				"local",
				compactionModeManual,
				llm.ItemsFromMessages([]llm.Message{{
					Role:        llm.RoleUser,
					MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
					Content:     textutil.Value("candidate-free summary"),
				}}),
			)
			return err
		}); err != nil {
			t.Fatalf("replace history %d: %v", index, err)
		}
	}

	newest := mustEngineNewestSegmentPage(t, eng)
	if newest.LatestRollbackCandidate == nil || *newest.LatestRollbackCandidate != *locator {
		t.Fatalf(
			"candidate-free compactions changed locator: got %#v want %#v",
			newest.LatestRollbackCandidate,
			locator,
		)
	}
	for _, entry := range newest.Snapshot.Entries {
		if entry.RollbackTargetID != nil {
			t.Fatalf("newest candidate-free segment unexpectedly contains rollback target %q", *entry.RollbackTargetID)
		}
	}

	if err := eng.Close(); err != nil {
		t.Fatalf("close compacted engine: %v", err)
	}
	reopened := mustNewTestEngine(t, mustOpenTestSession(t, store.Dir()), &fakeClient{}, tools.NewRegistry(), Config{})
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("close reopened engine: %v", err)
		}
	})
	restoredNewest := mustEngineNewestSegmentPage(t, reopened)
	if restoredNewest.LatestRollbackCandidate == nil || *restoredNewest.LatestRollbackCandidate != *locator {
		t.Fatalf(
			"restored locator = %#v, want %#v carried through history replacements",
			restoredNewest.LatestRollbackCandidate,
			locator,
		)
	}

	candidatePage := mustEngineSegmentPage(t, reopened, locator.CandidatePageEndByte)
	wantTarget := rollbacktarget.EncodeUserMessageSeq(locator.UserMessageSeq)
	found := false
	for _, entry := range candidatePage.Snapshot.Entries {
		if entry.RollbackTargetID != nil && *entry.RollbackTargetID == wantTarget {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("direct locator page did not contain rollback target %q", wantTarget)
	}

	if err := steerTestActiveStep(reopened,
		"fork-target-step",
		steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("new prompt to replace in fork")}}),
	); err != nil {
		t.Fatalf("persist fork target: %v", err)
	}
	forkTargetPage := mustEngineNewestSegmentPage(t, reopened)
	if forkTargetPage.LatestRollbackCandidate == nil {
		t.Fatal("fork target did not establish a newer rollback locator")
	}
	forkedStore, _, err := session.ForkAtUserMessage(
		reopened.eventLog,
		forkTargetPage.LatestRollbackCandidate.UserMessageSeq,
		"rollback locator fork",
		sessioncontract.SessionCategoryMain,
	)
	if err != nil {
		t.Fatalf("fork at newer rollback target: %v", err)
	}
	forked := mustNewTestEngine(t, forkedStore, &fakeClient{}, tools.NewRegistry(), Config{})
	t.Cleanup(func() {
		if err := forked.Close(); err != nil {
			t.Errorf("close forked engine: %v", err)
		}
	})
	forkedNewest := mustEngineNewestSegmentPage(t, forked)
	if forkedNewest.LatestRollbackCandidate == nil ||
		forkedNewest.LatestRollbackCandidate.UserMessageSeq != locator.UserMessageSeq {
		t.Fatalf(
			"forked locator = %#v, want prior candidate sequence %d",
			forkedNewest.LatestRollbackCandidate,
			locator.UserMessageSeq,
		)
	}
	forkedCandidatePage := mustEngineSegmentPage(
		t,
		forked,
		forkedNewest.LatestRollbackCandidate.CandidatePageEndByte,
	)
	found = false
	for _, entry := range forkedCandidatePage.Snapshot.Entries {
		if entry.RollbackTargetID != nil && *entry.RollbackTargetID == wantTarget {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("fork-rebased locator did not resolve prior rollback target %q", wantTarget)
	}
}

func TestRuntimeRestoreRejectsMalformedPersistedRollbackCandidateLocator(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	eventLog := mustMaterializeTestEventLog(t, store)
	appendMalformedRollbackCandidateHistoryReplacement(t, store)

	engine, err := New(store, eventLog, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	if engine != nil || !errors.Is(err, rollbacktarget.ErrInvalidCandidateLocator) {
		t.Fatalf("runtime restore result = engine:%+v error:%v", engine, err)
	}
}

func appendMalformedRollbackCandidateHistoryReplacement(t *testing.T, store *session.Store) {
	t.Helper()
	line, err := json.Marshal(struct {
		Seq     int64                            `json:"seq"`
		Kind    session.EventKind                `json:"kind"`
		Payload session.HistoryReplacementRecord `json:"payload"`
	}{
		Seq:  1,
		Kind: session.EventKindHistoryReplace,
		Payload: session.HistoryReplacementRecord{
			Engine: "local",
			Mode:   session.CompactionModeManual,
			LatestRollbackCandidate: &rollbacktarget.CandidateLocator{
				UserMessageSeq: 1,
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal malformed rollback candidate history replacement: %v", err)
	}
	appendRawCurrentEventLine(t, store, line)
}
