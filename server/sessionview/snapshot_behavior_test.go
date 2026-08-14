package sessionview

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"core/server/session"
	"core/server/sessionruntime"
	"core/shared/clientui"
	"core/shared/rollbacktarget"
	"core/shared/serverapi"
)

func TestServiceDormantTranscriptPagesPreserveRollbackLocatorAcrossCandidateFreeCompactions(t *testing.T) {
	const (
		userStepID    = "11111111-1111-4111-8111-111111111111"
		compactStepID = "22222222-2222-4222-8222-222222222222"
	)
	dir := t.TempDir()
	store, _ := newSessionViewParentAgentChild(t, dir, "ws", dir)
	appended := appendSessionViewRecordWithCursor(t, store, userStepID, session.MessageRecord{
		Role:    session.MessageRoleUser,
		Content: sessionViewStringPointer("candidate before dormant compactions"),
	})
	if appended.EndByteCursor == nil {
		t.Fatal("rollback candidate append did not return a page cursor")
	}
	locator := rollbacktarget.CandidateLocator{
		UserMessageSeq:       appended.Record.Seq(),
		CandidatePageEndByte: *appended.EndByteCursor,
	}
	for index := 0; index < 3; index++ {
		appendSessionViewHistoryReplacement(t, store, compactStepID, session.HistoryReplacementRecord{
			Engine:                  "local",
			Mode:                    session.CompactionModeAuto,
			LatestRollbackCandidate: &locator,
		})
	}
	dormant := NewService(newTestSessionResolver(store), nil, nil)

	dormantNewest := mustTranscriptPage(t, dormant, store.Meta().SessionID, nil, nil)
	if dormantNewest.LatestRollbackCandidate == nil || *dormantNewest.LatestRollbackCandidate != locator {
		t.Fatalf("dormant newest locator = %#v, want %#v", dormantNewest.LatestRollbackCandidate, locator)
	}

	cursor := locator.CandidatePageEndByte
	dormantCandidate := mustTranscriptPage(t, dormant, store.Meta().SessionID, &cursor, nil)
	if dormantCandidate.LatestRollbackCandidate == nil || *dormantCandidate.LatestRollbackCandidate != locator {
		t.Fatalf("dormant candidate-page locator = %#v, want %#v", dormantCandidate.LatestRollbackCandidate, locator)
	}
	wantTarget := rollbacktarget.EncodeUserMessageSeq(locator.UserMessageSeq)
	found := false
	for _, row := range dormantCandidate.Entries {
		if row.User != nil && row.User.RollbackTargetID != nil && *row.User.RollbackTargetID == wantTarget {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("dormant direct candidate page did not contain rollback target %q", wantTarget)
	}

	dormantNewer := mustTranscriptPage(t, dormant, store.Meta().SessionID, nil, &cursor)
	if dormantNewer.LatestRollbackCandidate == nil || *dormantNewer.LatestRollbackCandidate != locator {
		t.Fatalf("dormant newer-page locator = %#v, want %#v", dormantNewer.LatestRollbackCandidate, locator)
	}
}

func TestServiceTranscriptReadsHonorCanceledContext(t *testing.T) {
	store := newSessionViewStore(t, t.TempDir(), "ws", t.TempDir())
	service := NewService(newTestSessionResolver(store), nil, nil)
	cursor := int64(1)
	requests := map[string]serverapi.SessionTranscriptPageRequest{
		"newest page": {SessionID: store.Meta().SessionID},
		"older page":  {SessionID: store.Meta().SessionID, Cursor: &cursor},
		"newer page":  {SessionID: store.Meta().SessionID, NewerCursor: &cursor},
	}
	for name, request := range requests {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err := service.GetSessionTranscriptPage(ctx, request); !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context canceled", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := service.SessionTranscriptTailEntries(ctx, store.Meta().SessionID); !errors.Is(err, context.Canceled) {
		t.Fatalf("tail error = %v, want context canceled", err)
	}
}

func TestPublishedRuntimeSnapshotReturnsActiveRunWithoutSessionStore(t *testing.T) {
	store, fixture, release, handle := startBlockingRuntimeRun(t)
	live := NewService(nil, fixture.activity, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := live.GetSessionMainView(ctx, serverapi.SessionMainViewRequest{SessionID: store.Meta().SessionID}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled live main-view error = %v, want context canceled", err)
	}
	liveMain := mustMainView(t, live, store.Meta().SessionID)
	if liveMain.Activity.State != clientui.RuntimeActivityRunning {
		t.Fatalf("expected running activity, got %+v", liveMain.Activity)
	}

	release()
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatalf("submit user message: %v", err)
	}
}

func startBlockingRuntimeRun(t *testing.T) (*session.Store, sessionViewRuntimeFixture, func(), sessionruntime.ExecutionHandle) {
	t.Helper()
	dir := t.TempDir()
	store := newSessionViewStore(t, dir, "ws", dir)
	started := make(chan struct{})
	releaseModel := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseModel) })
	}
	fixture := newSessionViewRuntimeFixture(t, store, &serviceBlockingLLM{started: started, release: releaseModel})
	t.Cleanup(release)
	handle := fixture.startUserTurn(t, "run tools")
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for active run")
	}
	return store, fixture, release, handle
}

func mustMainView(t *testing.T, svc *Service, sessionID string) clientui.RuntimeMainView {
	t.Helper()
	resp, err := svc.GetSessionMainView(context.Background(), serverapi.SessionMainViewRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("get main view: %v", err)
	}
	return resp.MainView
}

func mustTranscriptPage(t *testing.T, svc *Service, sessionID string, cursor, newerCursor *int64) clientui.TranscriptPage {
	t.Helper()
	response, err := svc.GetSessionTranscriptPage(context.Background(), serverapi.SessionTranscriptPageRequest{
		SessionID:   sessionID,
		Cursor:      cursor,
		NewerCursor: newerCursor,
	})
	if err != nil {
		t.Fatalf("get transcript page: %v", err)
	}
	return response.Transcript
}
