package app

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
)

func TestRuntimeClientMainViewDoesNotRefreshCachedSnapshotBehindUIBack(t *testing.T) {
	reads := &countingSessionViewClient{view: clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{SessionID: "session-1"}}}
	controls := newUnavailableRuntimeControlService()
	runtimeClient := newTestSessionRuntimeClient(reads, controls)
	runtimeClient.storeMainView(clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{SessionID: "session-1"}})
	notified := make(chan error, 1)
	runtimeClient.SetConnectionStateObserver(func(err error) {
		notified <- err
	})

	_ = runtimeClient.MainView()

	if got := reads.count.Load(); got != 0 {
		t.Fatalf("main view read count = %d, want 0", got)
	}
	select {
	case err := <-notified:
		t.Fatalf("did not expect synchronous main-view refresh notification, got %v", err)
	default:
	}
}

type reconnectRetryRuntimeControlClient struct {
	mu               sync.Mutex
	firstSubmitErr   error
	firstRecordErr   error
	appendErr        error
	compactErr       error
	compactCalls     int
	showGoalErr      error
	showGoalCalls    int
	queuedWorkErr    error
	queuedWork       bool
	queuedWorkCalls  int
	submitCalls      int
	recordCalls      int
	localEntries     []serverapi.RuntimeAppendCommittedEntryRequest
	showGoalResp     serverapi.RuntimeGoalShowResponse
	setGoalResp      serverapi.RuntimeGoalShowResponse
	pauseGoalResp    serverapi.RuntimeGoalShowResponse
	resumeGoalResp   serverapi.RuntimeGoalShowResponse
	completeGoalResp serverapi.RuntimeGoalShowResponse
	clearGoalResp    serverapi.RuntimeGoalShowResponse
	interruptResp    serverapi.RuntimeInterruptResponse
	interruptReq     serverapi.RuntimeInterruptRequest
}

func (c *reconnectRetryRuntimeControlClient) appendedLocalEntries() []serverapi.RuntimeAppendCommittedEntryRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]serverapi.RuntimeAppendCommittedEntryRequest(nil), c.localEntries...)
}

func (c *reconnectRetryRuntimeControlClient) SetSessionName(context.Context, serverapi.RuntimeSetSessionNameRequest) error {
	return nil
}

func (c *reconnectRetryRuntimeControlClient) SetThinkingLevel(context.Context, serverapi.RuntimeSetThinkingLevelRequest) error {
	return nil
}

func (c *reconnectRetryRuntimeControlClient) SetFastModeEnabled(context.Context, serverapi.RuntimeSetFastModeEnabledRequest) (serverapi.RuntimeSetFastModeEnabledResponse, error) {
	return serverapi.RuntimeSetFastModeEnabledResponse{}, nil
}

func (c *reconnectRetryRuntimeControlClient) SetReviewerEnabled(context.Context, serverapi.RuntimeSetReviewerEnabledRequest) (serverapi.RuntimeSetReviewerEnabledResponse, error) {
	return serverapi.RuntimeSetReviewerEnabledResponse{}, nil
}

func (c *reconnectRetryRuntimeControlClient) SetAutoCompactionEnabled(context.Context, serverapi.RuntimeSetAutoCompactionEnabledRequest) (serverapi.RuntimeSetAutoCompactionEnabledResponse, error) {
	return serverapi.RuntimeSetAutoCompactionEnabledResponse{}, nil
}

func (c *reconnectRetryRuntimeControlClient) SetQuestionsEnabled(context.Context, serverapi.RuntimeSetQuestionsEnabledRequest) (serverapi.RuntimeSetQuestionsEnabledResponse, error) {
	return serverapi.RuntimeSetQuestionsEnabledResponse{}, nil
}

func (c *reconnectRetryRuntimeControlClient) AppendCommittedEntry(_ context.Context, req serverapi.RuntimeAppendCommittedEntryRequest) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.localEntries = append(c.localEntries, req)
	return c.appendErr
}

func (c *reconnectRetryRuntimeControlClient) ShouldCompactBeforeUserMessage(context.Context, serverapi.RuntimeShouldCompactBeforeUserMessageRequest) (serverapi.RuntimeShouldCompactBeforeUserMessageResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.compactCalls++
	if c.compactCalls == 1 && c.compactErr != nil {
		return serverapi.RuntimeShouldCompactBeforeUserMessageResponse{}, c.compactErr
	}
	return serverapi.RuntimeShouldCompactBeforeUserMessageResponse{}, nil
}

func (c *reconnectRetryRuntimeControlClient) SubmitUserTurn(_ context.Context, req serverapi.RuntimeSubmitUserTurnRequest) (serverapi.RuntimeSubmitUserTurnResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.submitCalls++
	if c.submitCalls == 1 && c.firstSubmitErr != nil {
		return serverapi.RuntimeSubmitUserTurnResponse{}, c.firstSubmitErr
	}
	return serverapi.RuntimeSubmitUserTurnResponse{Message: "recovered"}, nil
}

func (c *reconnectRetryRuntimeControlClient) SubmitUserShellCommand(context.Context, serverapi.RuntimeSubmitUserShellCommandRequest) error {
	return nil
}

func (c *reconnectRetryRuntimeControlClient) CompactContext(context.Context, serverapi.RuntimeCompactContextRequest) error {
	return nil
}

func (c *reconnectRetryRuntimeControlClient) Interrupt(_ context.Context, req serverapi.RuntimeInterruptRequest) (serverapi.RuntimeInterruptResponse, error) {
	c.mu.Lock()
	c.interruptReq = req
	c.mu.Unlock()
	return c.interruptResp, nil
}

func (c *reconnectRetryRuntimeControlClient) DiscardQueuedUserMessage(context.Context, serverapi.RuntimeDiscardQueuedUserMessageRequest) (serverapi.RuntimeDiscardQueuedUserMessageResponse, error) {
	return serverapi.RuntimeDiscardQueuedUserMessageResponse{}, nil
}

func (c *reconnectRetryRuntimeControlClient) RecordPromptHistory(_ context.Context, req serverapi.RuntimeRecordPromptHistoryRequest) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recordCalls++
	if c.recordCalls == 1 && c.firstRecordErr != nil {
		return c.firstRecordErr
	}
	return nil
}

func (c *reconnectRetryRuntimeControlClient) ShowGoal(context.Context, serverapi.RuntimeGoalShowRequest) (serverapi.RuntimeGoalShowResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.showGoalCalls++
	if c.showGoalCalls == 1 && c.showGoalErr != nil {
		return serverapi.RuntimeGoalShowResponse{}, c.showGoalErr
	}
	return c.showGoalResp, nil
}

func (c *reconnectRetryRuntimeControlClient) SetGoal(context.Context, serverapi.RuntimeGoalSetRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return c.setGoalResp, nil
}

func (c *reconnectRetryRuntimeControlClient) PauseGoal(context.Context, serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return c.pauseGoalResp, nil
}

func (c *reconnectRetryRuntimeControlClient) ResumeGoal(context.Context, serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return c.resumeGoalResp, nil
}

func (c *reconnectRetryRuntimeControlClient) CompleteGoal(context.Context, serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return c.completeGoalResp, nil
}

func (c *reconnectRetryRuntimeControlClient) ClearGoal(context.Context, serverapi.RuntimeGoalClearRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return c.clearGoalResp, nil
}

func TestRuntimeClientShowGoalDoesNotOverwriteAcceptedPendingGoal(t *testing.T) {
	pending := &serverapi.RuntimeGoal{ID: "goal-pending", Objective: "accepted pending goal", Status: "active"}
	committed := &serverapi.RuntimeGoal{ID: "goal-committed", Objective: "prior committed goal", Status: "paused"}
	controls := &reconnectRetryRuntimeControlClient{
		setGoalResp:  serverapi.RuntimeGoalShowResponse{Goal: pending},
		showGoalResp: serverapi.RuntimeGoalShowResponse{Goal: committed},
	}
	runtimeClient := newTestSessionRuntimeClientWithControls(controls)

	accepted, err := runtimeClient.SetGoal(pending.Objective)
	if err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	assertRuntimeClientGoalCached(t, runtimeClient, accepted, runtimeGoalFromAPI(pending))

	shown, err := runtimeClient.ShowGoal()
	if err != nil {
		t.Fatalf("ShowGoal: %v", err)
	}
	if !reflect.DeepEqual(shown, runtimeGoalFromAPI(committed)) {
		t.Fatalf("shown goal = %+v, want committed goal %+v", shown, committed)
	}
	view, ok := runtimeClient.CachedMainView()
	if !ok {
		t.Fatal("expected cached main view")
	}
	if !reflect.DeepEqual(view.Status.Goal, runtimeGoalFromAPI(pending)) {
		t.Fatalf("cached goal = %+v, want accepted pending goal %+v", view.Status.Goal, pending)
	}
}

func TestRuntimeClientShowGoalPreservesSuspendedLiveStatus(t *testing.T) {
	committed := &serverapi.RuntimeGoal{ID: "goal-committed", Objective: "committed goal", Status: "active"}
	controls := &reconnectRetryRuntimeControlClient{showGoalResp: serverapi.RuntimeGoalShowResponse{Goal: committed}}
	runtimeClient := newTestSessionRuntimeClientWithControls(controls)
	liveGoal := &clientui.RuntimeGoal{
		ID:        committed.ID,
		Objective: committed.Objective,
		Status:    clientui.RuntimeGoalStatusActive,
		Suspended: true,
	}
	runtimeClient.storeMainView(clientui.RuntimeMainView{
		Session: clientui.RuntimeSessionView{SessionID: "session-1"},
		Status:  clientui.RuntimeStatus{Goal: liveGoal},
	})

	shown, err := runtimeClient.ShowGoal()
	if err != nil {
		t.Fatalf("ShowGoal: %v", err)
	}
	if !reflect.DeepEqual(shown, runtimeGoalFromAPI(committed)) {
		t.Fatalf("shown goal = %+v, want committed goal %+v", shown, committed)
	}
	view, ok := runtimeClient.CachedMainView()
	if !ok {
		t.Fatal("expected cached main view")
	}
	if !reflect.DeepEqual(view.Status.Goal, liveGoal) {
		t.Fatalf("cached goal = %+v, want suspended live goal %+v", view.Status.Goal, liveGoal)
	}
}

func TestRuntimeClientGoalMutationMethodsPatchCachedMainView(t *testing.T) {
	setGoal := &serverapi.RuntimeGoal{ID: "goal-set", Objective: "set goal", Status: "active", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	pauseGoal := &serverapi.RuntimeGoal{ID: "goal-pause", Objective: "pause goal", Status: "paused", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	resumeGoal := &serverapi.RuntimeGoal{ID: "goal-resume", Objective: "resume goal", Status: "active", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	completeGoal := &serverapi.RuntimeGoal{ID: "goal-complete", Objective: "complete goal", Status: "complete", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	controls := &reconnectRetryRuntimeControlClient{
		setGoalResp:      serverapi.RuntimeGoalShowResponse{Goal: setGoal},
		pauseGoalResp:    serverapi.RuntimeGoalShowResponse{Goal: pauseGoal},
		resumeGoalResp:   serverapi.RuntimeGoalShowResponse{Goal: resumeGoal},
		completeGoalResp: serverapi.RuntimeGoalShowResponse{Goal: completeGoal},
		clearGoalResp:    serverapi.RuntimeGoalShowResponse{},
	}
	runtimeClient := newTestSessionRuntimeClientWithControls(controls)
	reactivator := newRuntimeReactivator()
	reactivator.SetReactivateFunc(func(context.Context) error { return nil })
	runtimeClient.SetRuntimeReactivator(reactivator)

	for _, tt := range []struct {
		name string
		call func() (*clientui.RuntimeGoal, error)
		want *serverapi.RuntimeGoal
	}{
		{name: "set", call: func() (*clientui.RuntimeGoal, error) { return runtimeClient.SetGoal("set goal") }, want: setGoal},
		{name: "pause", call: runtimeClient.PauseGoal, want: pauseGoal},
		{name: "resume", call: runtimeClient.ResumeGoal, want: resumeGoal},
		{name: "complete", call: runtimeClient.CompleteGoal, want: completeGoal},
		{name: "clear", call: runtimeClient.ClearGoal, want: nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			goal, err := tt.call()
			if err != nil {
				t.Fatalf("%s goal: %v", tt.name, err)
			}
			assertRuntimeClientGoalCached(t, runtimeClient, goal, runtimeGoalFromAPI(tt.want))
			if tt.want != nil {
				assertRuntimeGoalConversionDropsAPITimestamps(t, goal, tt.want)
			}
		})
	}
}

func TestRuntimeClientPublicInterruptCommitsAuthoritativeRuntimeTuple(t *testing.T) {
	current := runtimeTupleTestView(
		10,
		runtimeTupleTestIdleActivity(),
	)
	controls := &reconnectRetryRuntimeControlClient{interruptResp: serverapi.RuntimeInterruptResponse{
		Version:  clientui.ReadModelVersion{Epoch: current.Version.Epoch, Generation: current.Version.Generation, Sequence: 11},
		Activity: runtimeTupleTestRunningActivity(),
	}}
	runtimeClient := newTestSessionRuntimeClientWithControls(controls)
	runtimeClient.storeMainView(current)

	if err := runtimeClient.Interrupt(); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	assertRuntimeTupleView(
		t,
		runtimeClient.MainView(),
		runtimeTupleTestView(11, runtimeTupleTestRunningActivity()),
	)
}

func TestCloneRuntimeGoalReturnsIndependentCopy(t *testing.T) {
	original := &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship", Status: clientui.RuntimeGoalStatusActive, Suspended: true}
	cloned := cloneRuntimeGoal(original)
	original.ID = "goal-2"
	original.Objective = "mutated"
	original.Status = clientui.RuntimeGoalStatusPaused
	original.Suspended = false

	want := &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship", Status: clientui.RuntimeGoalStatusActive, Suspended: true}
	if !reflect.DeepEqual(cloned, want) {
		t.Fatalf("clone = %+v, want %+v", cloned, want)
	}
}

func TestRuntimeClientGoalStatusEventPatchesCachedMainView(t *testing.T) {
	runtimeClient := newTestSessionRuntimeClientWithControls(&reconnectRetryRuntimeControlClient{})
	runtimeClient.storeMainView(clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{SessionID: "session-1"}})

	if _, err := runtimeClient.admitTranscriptMessageState(clientui.NewTranscriptMessage(0, clientui.NewTranscriptEvent(clientui.TranscriptGoalStatus{
		Goal: &clientui.TranscriptGoal{
			ID:        "goal-1",
			Objective: "ship feature",
			Status:    clientui.RuntimeGoalStatusActive,
		},
	})),
	); err != nil {
		t.Fatalf("admit goal status: %v", err)
	}
	assertRuntimeClientGoalCached(
		t,
		runtimeClient,
		&clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusActive},
		&clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusActive},
	)

	if _, err := runtimeClient.admitTranscriptMessageState(clientui.NewTranscriptMessage(0, clientui.NewTranscriptEvent(clientui.TranscriptGoalStatus{
		Goal: &clientui.TranscriptGoal{
			ID:        "goal-1",
			Objective: "ship feature",
			Status:    clientui.RuntimeGoalStatusPaused,
		},
	})),
	); err != nil {
		t.Fatalf("admit paused goal status: %v", err)
	}
	assertRuntimeClientGoalCached(
		t,
		runtimeClient,
		&clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusPaused},
		&clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusPaused},
	)

	if _, err := runtimeClient.admitTranscriptMessageState(clientui.NewTranscriptMessage(0, clientui.NewTranscriptEvent(clientui.TranscriptGoalStatus{}))); err != nil {
		t.Fatalf("admit cleared goal status: %v", err)
	}
	assertRuntimeClientGoalCached(t, runtimeClient, nil, nil)
}

func TestRuntimeClientCanonicalGoalStatusReplacesCachedGoal(t *testing.T) {
	runtimeClient := newTestSessionRuntimeClientWithControls(&reconnectRetryRuntimeControlClient{})
	runtimeClient.storeMainView(clientui.RuntimeMainView{
		Session: clientui.RuntimeSessionView{SessionID: "session-1"},
		Status: clientui.RuntimeStatus{Goal: &clientui.RuntimeGoal{
			ID:        "goal-old",
			Objective: "old",
			Status:    clientui.RuntimeGoalStatusActive,
			Suspended: true,
		}},
	})

	if _, err := runtimeClient.admitTranscriptMessageState(clientui.NewTranscriptMessage(0, clientui.NewTranscriptEvent(clientui.TranscriptGoalStatus{
		Goal: &clientui.TranscriptGoal{
			ID:        "goal-new",
			Objective: "new",
			Status:    clientui.RuntimeGoalStatusActive,
		},
	})),
	); err != nil {
		t.Fatalf("admit replacement goal status: %v", err)
	}
	assertRuntimeClientGoalCached(
		t,
		runtimeClient,
		&clientui.RuntimeGoal{ID: "goal-new", Objective: "new", Status: clientui.RuntimeGoalStatusActive},
		&clientui.RuntimeGoal{ID: "goal-new", Objective: "new", Status: clientui.RuntimeGoalStatusActive},
	)
}

func assertRuntimeClientGoalCached(t *testing.T, runtimeClient *sessionRuntimeClient, got *clientui.RuntimeGoal, want *clientui.RuntimeGoal) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("goal = %+v, want %+v", got, want)
	}
	view, ok := runtimeClient.CachedMainView()
	if !ok {
		t.Fatal("expected cached main view")
	}
	if !reflect.DeepEqual(view.Status.Goal, want) {
		t.Fatalf("cached goal = %+v, want %+v", view.Status.Goal, want)
	}
}

func assertRuntimeGoalConversionDropsAPITimestamps(t *testing.T, got *clientui.RuntimeGoal, source *serverapi.RuntimeGoal) {
	t.Helper()
	if source == nil || source.CreatedAt.IsZero() || source.UpdatedAt.IsZero() {
		t.Fatal("test source goal must include timestamps")
	}
	if got == nil || got.ID != source.ID || got.Objective != source.Objective || string(got.Status) != source.Status {
		t.Fatalf("converted goal = %+v, source = %+v", got, source)
	}
}

func TestRuntimeClientSubmitUserMessageDoesNotReplayRuntimeUnavailable(t *testing.T) {
	controls := &reconnectRetryRuntimeControlClient{firstSubmitErr: serverapi.ErrRuntimeUnavailable}
	runtimeClient := newTestSessionRuntimeClientWithControls(controls)
	reactivator := newRuntimeReactivator()
	recoveryCalls := 0
	reactivator.SetReactivateFunc(func(context.Context) error {
		recoveryCalls++
		return nil
	})
	runtimeClient.SetRuntimeReactivator(reactivator)

	_, err := runtimeClient.SubmitRuntimeInput(context.Background(), clientui.RuntimeSubmitRequest{
		Input: runtimeinput.Text("hello"),
	})
	if !errors.Is(err, serverapi.ErrRuntimeUnavailable) {
		t.Fatalf("SubmitUserMessage error = %v, want runtime unavailable", err)
	}
	if recoveryCalls != 0 || controls.submitCalls != 1 {
		t.Fatalf("runtime unavailable replayed submit: recovery=%d submit=%d", recoveryCalls, controls.submitCalls)
	}
}

func TestRuntimeClientRecordPromptHistoryDoesNotReplayRuntimeUnavailable(t *testing.T) {
	controls := &reconnectRetryRuntimeControlClient{firstRecordErr: serverapi.ErrRuntimeUnavailable}
	runtimeClient := newTestSessionRuntimeClientWithControls(controls)
	reactivator := newRuntimeReactivator()
	reactivator.SetReactivateFunc(func(context.Context) error { return nil })
	runtimeClient.SetRuntimeReactivator(reactivator)

	if err := runtimeClient.RecordPromptHistory("/status"); !errors.Is(err, serverapi.ErrRuntimeUnavailable) {
		t.Fatalf("RecordPromptHistory error = %v, want runtime unavailable", err)
	}
	if controls.recordCalls != 1 {
		t.Fatalf("record prompt history calls = %d, want one", controls.recordCalls)
	}
}

func TestRuntimeClientMainViewRefreshRecoversRuntimeUnavailableSilently(t *testing.T) {
	controls := &reconnectRetryRuntimeControlClient{}
	authoritativeView := clientui.RuntimeMainView{
		Session: clientui.RuntimeSessionView{SessionID: "session-1", SessionName: "restored"},
		Status:  clientui.RuntimeStatus{ThinkingLevel: "high"},
	}
	reads := &flakySessionViewClient{
		errs:      []error{serverapi.ErrRuntimeUnavailable, nil},
		responses: []serverapi.SessionMainViewResponse{{}, {MainView: authoritativeView}},
	}
	runtimeClient := newTestSessionRuntimeClient(reads, controls)
	reactivator := newRuntimeReactivator()
	recoveryCalls := 0
	reactivator.SetReactivateFunc(func(context.Context) error {
		recoveryCalls++
		return nil
	})
	runtimeClient.SetRuntimeReactivator(reactivator)

	view, err := runtimeClient.RefreshMainView()
	if err != nil {
		t.Fatalf("RefreshMainView: %v", err)
	}
	if recoveryCalls != 1 {
		t.Fatalf("recovery call count = %d, want 1", recoveryCalls)
	}
	if reads.count != 2 {
		t.Fatalf("main-view read count = %d, want 2", reads.count)
	}
	if view.Session.SessionName != "restored" || view.Status.ThinkingLevel != "high" {
		t.Fatalf("main view = %+v, want %+v", view, authoritativeView)
	}
	if entries := controls.appendedLocalEntries(); len(entries) != 0 {
		t.Fatalf("did not expect visible recovery warning during main-view refresh, got %+v", entries)
	}
}

func TestRuntimeClientMainViewRecoveryPreservesReadDeadline(t *testing.T) {
	controls := &reconnectRetryRuntimeControlClient{}
	reads := &flakySessionViewClient{errs: []error{serverapi.ErrRuntimeUnavailable}}
	runtimeClient := newTestSessionRuntimeClient(reads, controls)
	reactivator := newRuntimeReactivator()
	reactivationStarted := make(chan struct{})
	reactivator.SetReactivateFunc(func(ctx context.Context) error {
		close(reactivationStarted)
		<-ctx.Done()
		return ctx.Err()
	})
	runtimeClient.SetRuntimeReactivator(reactivator)

	start := time.Now()
	if _, err := runtimeClient.refreshMainViewSync(uiRuntimeReadTimeout); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("refreshMainViewSync error = %v, want reactivation deadline error", err)
	}
	if elapsed := time.Since(start); elapsed > uiRuntimeReadTimeout+500*time.Millisecond {
		t.Fatalf("refreshMainViewSync elapsed = %s, want bounded by read timeout %s", elapsed, uiRuntimeReadTimeout)
	}
	select {
	case <-reactivationStarted:
	case <-time.After(time.Second):
		t.Fatal("reactivation did not start")
	}
}

func TestRuntimeClientShowGoalSurfacesRuntimeUnavailableWithoutReplay(t *testing.T) {
	goal := &serverapi.RuntimeGoal{ID: "goal-1", Objective: "ship", Status: "active"}
	controls := &reconnectRetryRuntimeControlClient{
		showGoalErr:  serverapi.ErrRuntimeUnavailable,
		showGoalResp: serverapi.RuntimeGoalShowResponse{Goal: goal},
	}
	runtimeClient := newTestSessionRuntimeClientWithControls(controls)
	reactivator := newRuntimeReactivator()
	recoveryCalls := 0
	reactivator.SetReactivateFunc(func(context.Context) error {
		recoveryCalls++
		return nil
	})
	runtimeClient.SetRuntimeReactivator(reactivator)

	got, err := runtimeClient.ShowGoal()
	if !errors.Is(err, serverapi.ErrRuntimeUnavailable) {
		t.Fatalf("ShowGoal error = %v, want runtime unavailable", err)
	}
	if recoveryCalls != 0 {
		t.Fatalf("recovery call count = %d, want 0", recoveryCalls)
	}
	if controls.showGoalCalls != 1 {
		t.Fatalf("show goal call count = %d, want 1", controls.showGoalCalls)
	}
	if got != nil {
		t.Fatalf("goal = %+v, want no replayed result", got)
	}
	if entries := controls.appendedLocalEntries(); len(entries) != 0 {
		t.Fatalf("did not expect visible recovery warning during goal read, got %+v", entries)
	}
}
