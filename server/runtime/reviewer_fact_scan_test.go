package runtime

import (
	"errors"
	"testing"

	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/runtimeids"
	"core/shared/transcript"
)

func TestPersistedTranscriptScanReconstructsTypedReviewerFactsInBoundedWindows(t *testing.T) {
	feedback := session.ReviewerFeedbackRecord{
		ID:          runtimeids.NewReviewerFeedbackID(),
		Suggestions: []string{"  **first**  ", "second\nline"},
		Visibility:  session.EntryVisibilityOngoingCollapsed,
	}
	reviewerError := session.ReviewerErrorRecord{
		ID:     runtimeids.NewReviewerErrorID(),
		Detail: "raw failure detail",
	}
	feedbackEvent, err := session.NewEventRecord(1, stringPointer("11111111-1111-4111-8111-111111111111"), feedback)
	if err != nil {
		t.Fatalf("create feedback event: %v", err)
	}
	errorEvent, err := session.NewEventRecord(2, stringPointer("22222222-2222-4222-8222-222222222222"), reviewerError)
	if err != nil {
		t.Fatalf("create error event: %v", err)
	}

	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{Offset: 1, Limit: 1, TrackRecentTail: true, TailLimit: 1})
	for _, event := range []session.EventRecord{feedbackEvent, errorEvent} {
		if err := scan.ApplyPersistedEvent(event); err != nil {
			t.Fatalf("apply typed Reviewer event: %v", err)
		}
	}

	if got := scan.TotalEntries(); got != 2 {
		t.Fatalf("total entries = %d, want 2", got)
	}
	page := scan.CollectedPageSnapshot()
	if len(page.Entries) != 1 || page.Entries[0].ReviewerError == nil {
		t.Fatalf("bounded page = %+v, want only Reviewer error", page.Entries)
	}
	if page.Entries[0].ReviewerError.ID != reviewerError.ID ||
		page.Entries[0].ReviewerError.Detail != reviewerError.Detail ||
		page.Entries[0].StepID != "22222222-2222-4222-8222-222222222222" ||
		page.Entries[0].Visibility != transcript.EntryVisibilityOngoing {
		t.Fatalf("projected Reviewer error = %+v", page.Entries[0])
	}
	tail := scan.RecentTailSnapshot()
	if len(tail.Snapshot.Entries) != 1 || tail.Snapshot.Entries[0].ReviewerError == nil {
		t.Fatalf("bounded tail = %+v, want only Reviewer error", tail.Snapshot.Entries)
	}
	facts := TranscriptCommittedRowFactsFromSnapshot(page)
	if len(facts) != 1 || facts[0].Kind != TranscriptCommittedRowFactReviewerError ||
		facts[0].ReviewerError == nil || facts[0].ReviewerError.ID != reviewerError.ID {
		t.Fatalf("snapshot Reviewer facts = %+v", facts)
	}
}

func TestChatStoreDeliverySnapshotKeepsTypedReviewerFactsProjected(t *testing.T) {
	chat := newChatStore()
	chat.appendLocalEntryRecord(reviewerFeedbackChatEntryFromSessionRecord(session.ReviewerFeedbackRecord{
		ID:          runtimeids.NewReviewerFeedbackID(),
		Suggestions: []string{"first", "second"},
		Visibility:  session.EntryVisibilityOngoingCollapsed,
	}, "11111111-1111-4111-8111-111111111111"), nil)
	chat.appendLocalEntryRecord(reviewerErrorChatEntryFromSessionRecord(session.ReviewerErrorRecord{
		ID: runtimeids.NewReviewerErrorID(), Detail: "failure",
	}, "22222222-2222-4222-8222-222222222222"), nil)
	snapshot := chat.deliverySnapshot()
	if len(snapshot.Rows) != 2 || snapshot.Rows[0].ReviewerFeedback == nil || snapshot.Rows[1].ReviewerError == nil {
		t.Fatalf("typed Reviewer delivery rows = %+v", snapshot.Rows)
	}
}

func TestReopenedEngineHydratesTypedReviewerFacts(t *testing.T) {
	store := mustCreateTestSession(t)
	stepID := "11111111-1111-4111-8111-111111111111"
	if _, _, err := appendTestEvent(t, store, stepID, session.ReviewerFeedbackRecord{
		ID: runtimeids.NewReviewerFeedbackID(), Suggestions: []string{"reopened"},
		Visibility: session.EntryVisibilityOngoingCollapsed,
	}); err != nil {
		t.Fatalf("append feedback: %v", err)
	}
	if _, _, err := appendTestEvent(t, store, stepID, session.ReviewerErrorRecord{
		ID: runtimeids.NewReviewerErrorID(), Detail: "reopened error",
	}); err != nil {
		t.Fatalf("append error: %v", err)
	}
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	rows := mustTranscriptHydrationSnapshot(t, engine).CommittedRows
	if len(rows) != 2 || rows[0].ReviewerFeedback == nil || rows[1].ReviewerError == nil {
		t.Fatalf("reopened typed Reviewer rows = %+v", rows)
	}
}

func TestReviewerFactSteeringCommitFenceMatrix(t *testing.T) {
	cases := []struct {
		name   string
		intent func() steeringIntent
		assert func(t *testing.T, rows []TranscriptCommittedRowFact)
	}{
		{
			name: "feedback",
			intent: func() steeringIntent {
				return steerReviewerFeedbackIntent([]string{"feedback"}, transcript.EntryVisibilityOngoingCollapsed)
			},
			assert: func(t *testing.T, rows []TranscriptCommittedRowFact) {
				if len(rows) != 1 || rows[0].ReviewerFeedback == nil {
					t.Fatalf("feedback rows = %+v", rows)
				}
			},
		},
		{
			name: "error",
			intent: func() steeringIntent {
				return steerReviewerErrorIntent("raw failure")
			},
			assert: func(t *testing.T, rows []TranscriptCommittedRowFact) {
				if len(rows) != 1 || rows[0].ReviewerError == nil {
					t.Fatalf("error rows = %+v", rows)
				}
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Run("uncommitted", func(t *testing.T) {
				store := mustCreateTestSession(t)
				engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
				mustBlockTestEventLogAppends(t, store)
				err := engine.steer("11111111-1111-4111-8111-111111111111", testCase.intent())
				if err == nil {
					t.Fatal("uncommitted typed Reviewer append succeeded")
				}
				if rows := mustTranscriptHydrationSnapshot(t, engine).CommittedRows; len(rows) != 0 {
					t.Fatalf("uncommitted typed Reviewer rows = %+v", rows)
				}
			})
			t.Run("committed observer error", func(t *testing.T) {
				observerErr := errors.New("typed Reviewer observer failed")
				gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
				store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
				engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
				gate.FailNext(observerErr)
				err := engine.steer("22222222-2222-4222-8222-222222222222", testCase.intent())
				if !errors.Is(err, observerErr) {
					t.Fatalf("committed typed Reviewer error = %v, want %v", err, observerErr)
				}
				rows := mustTranscriptHydrationSnapshot(t, engine).CommittedRows
				testCase.assert(t, rows)
			})
		})
	}
}

func TestPersistedTranscriptScanKeepsHistoricalReviewerLocalEntriesGeneric(t *testing.T) {
	// TODO(KENT-405): delete this compatibility fixture with the legacy reader in 2.7.0.
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{})
	events := []session.EventRecord{
		mustPersistedScanEvent(t, "local_entry", storedLocalEntry{
			Visibility: transcript.EntryVisibilityOngoing,
			Role:       "reviewer_suggestions",
			Text:       "legacy Markdown\n\n- item",
		}),
		mustPersistedScanEvent(t, "local_entry", storedLocalEntry{
			Visibility: transcript.EntryVisibilityOngoing,
			Role:       "reviewer_error",
			Text:       "legacy error detail",
		}),
	}
	for _, event := range events {
		if err := scan.ApplyPersistedEvent(event); err != nil {
			t.Fatalf("apply historical Reviewer local entry: %v", err)
		}
	}
	entries := scan.CollectedPageSnapshot().Entries
	if len(entries) != 2 || entries[0].Role != "reviewer_suggestions" || entries[0].Text != "legacy Markdown\n\n- item" ||
		entries[1].Role != "reviewer_error" || entries[1].Text != "legacy error detail" {
		t.Fatalf("historical Reviewer local entry = %+v", entries)
	}
}

func stringPointer(value string) *string {
	return &value
}
