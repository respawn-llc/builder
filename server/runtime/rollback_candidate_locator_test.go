package runtime

import (
	"context"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/rollbacktarget"
)

func TestLatestRollbackCandidateLocatorSurvivesCandidateFreeCompactionsAndRestart(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{})

	if err := eng.steer(
		"user-step",
		steerMessagesWithPersistenceIntent(
			steeringPriorityUser,
			steeringMessageEventDefault,
			true,
			[]llm.Message{{Role: llm.RoleUser, Content: "candidate before several compactions"}},
		),
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
		if err := newCompactionPersistence(eng).replaceHistory(
			"compact-step",
			"local",
			compactionModeManual,
			llm.ItemsFromMessages([]llm.Message{{
				Role:        llm.RoleUser,
				MessageType: llm.MessageTypeCompactionSummary,
				Content:     "candidate-free summary",
			}}),
		); err != nil {
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

	if err := reopened.steer(
		"fork-target-step",
		steerMessagesWithPersistenceIntent(
			steeringPriorityUser,
			steeringMessageEventDefault,
			true,
			[]llm.Message{{Role: llm.RoleUser, Content: "new prompt to replace in fork"}},
		),
	); err != nil {
		t.Fatalf("persist fork target: %v", err)
	}
	forkTargetPage := mustEngineNewestSegmentPage(t, reopened)
	if forkTargetPage.LatestRollbackCandidate == nil {
		t.Fatal("fork target did not establish a newer rollback locator")
	}
	forkedStore, _, err := session.ForkAtUserMessage(
		reopened.store,
		forkTargetPage.LatestRollbackCandidate.UserMessageSeq,
		"rollback locator fork",
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

func TestQueuedUserSubmissionUpdatesLatestRollbackCandidateLocator(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "queued answer"},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{})

	eng.QueueUserMessage("queued rollback candidate")
	if _, err := eng.SubmitQueuedUserMessages(context.Background()); err != nil {
		t.Fatalf("submit queued user message: %v", err)
	}

	page := mustEngineNewestSegmentPage(t, eng)
	if page.LatestRollbackCandidate == nil {
		t.Fatal("queued user submission did not establish a rollback candidate locator")
	}
	wantTarget := rollbacktarget.EncodeUserMessageSeq(page.LatestRollbackCandidate.UserMessageSeq)
	found := false
	for _, entry := range page.Snapshot.Entries {
		if entry.RollbackTargetID != nil && *entry.RollbackTargetID == wantTarget {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("queued locator target %q was not present in newest segment", wantTarget)
	}
}

func TestRuntimeRestoreRejectsMalformedPersistedRollbackCandidateLocator(t *testing.T) {
	store := mustCreateTestSession(t)
	event, _, err := store.AppendEvent("compact-step", "history_replaced", historyReplacementPayload{
		Engine: "local",
		LatestRollbackCandidate: &rollbacktarget.CandidateLocator{
			UserMessageSeq: 7,
		},
		Items: llm.ItemsFromMessages([]llm.Message{{
			Role:        llm.RoleUser,
			MessageType: llm.MessageTypeCompactionSummary,
			Content:     "summary",
		}}),
	})
	if err != nil {
		t.Fatalf("append malformed history replacement: %v", err)
	}
	if !isCompactionSegmentBoundary(event) {
		t.Fatal("malformed locator made a structurally valid history replacement stop acting as a segment boundary")
	}

	if _, err := New(store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"}); err == nil {
		t.Fatal("runtime restore accepted a nonpositive rollback candidate page cursor")
	}
}
