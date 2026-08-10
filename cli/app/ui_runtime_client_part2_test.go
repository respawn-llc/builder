package app

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
	"core/shared/textutil"

	tea "github.com/charmbracelet/bubbletea"
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
	submitCalls      int
	recordCalls      int
	submitRequestID  []string
	recordRequestID  []string
	localEntries     []serverapi.RuntimeAppendCommittedEntryRequest
	showGoalResp     serverapi.RuntimeGoalShowResponse
	setGoalResp      serverapi.RuntimeGoalMutationResponse
	pauseGoalResp    serverapi.RuntimeGoalShowResponse
	resumeGoalResp   serverapi.RuntimeGoalShowResponse
	completeGoalResp serverapi.RuntimeGoalShowResponse
	clearGoalResp    serverapi.RuntimeGoalShowResponse
	interruptResp    serverapi.RuntimeInterruptResponse
	interruptReq     serverapi.RuntimeInterruptRequest
}

func (c *reconnectRetryRuntimeControlClient) submitRequestIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.submitRequestID...)
}

func (c *reconnectRetryRuntimeControlClient) recordRequestIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.recordRequestID...)
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
	c.submitRequestID = append(c.submitRequestID, req.ClientRequestID)
	if c.submitCalls == 1 && c.firstSubmitErr != nil {
		return serverapi.RuntimeSubmitUserTurnResponse{}, c.firstSubmitErr
	}
	return serverapi.RuntimeSubmitUserTurnResponse{Message: textutil.Value("recovered")}, nil
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
	c.recordRequestID = append(c.recordRequestID, req.ClientRequestID)
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

func (c *reconnectRetryRuntimeControlClient) SetGoal(context.Context, serverapi.RuntimeGoalSetRequest) (serverapi.RuntimeGoalMutationResponse, error) {
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
		setGoalResp:  serverapi.RuntimeGoalMutationResponse{Pending: &clientui.GoalPreview{Objective: pending.Objective, Status: pending.Status}, Availability: clientui.GoalAvailabilityAvailable},
		showGoalResp: goalEnvelopeFixture(committed),
	}
	runtimeClient := newTestSessionRuntimeClientWithControls(controls)

	accepted, err := runtimeClient.SetGoal(pending.Objective)
	if err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	assertRuntimeClientGoalCached(t, runtimeClient, accepted, runtimeGoalPendingFixture(pending.Objective, pending.Status))

	shown, err := runtimeClient.ShowGoal()
	if err != nil {
		t.Fatalf("ShowGoal: %v", err)
	}
	if !reflect.DeepEqual(shown, runtimeGoalFixtureFromAPI(committed)) {
		t.Fatalf("shown goal = %+v, want committed goal %+v", shown, committed)
	}
	view, ok := runtimeClient.CachedMainView()
	if !ok {
		t.Fatal("expected cached main view")
	}
	if !reflect.DeepEqual(view.Status.Goal, runtimeGoalPendingFixture(pending.Objective, pending.Status)) {
		t.Fatalf("cached goal = %+v, want accepted pending preview", view.Status.Goal)
	}
}

func runtimeGoalFixture(id, objective string, status clientui.RuntimeGoalStatus, suspended bool) *clientui.RuntimeGoal { return &clientui.RuntimeGoal{Goal: &clientui.Goal{ID: id, Objective: objective, Status: status, CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(1, 0)}, Availability: clientui.GoalAvailabilityAvailable, Suspended: suspended} }

func transcriptGoalFixture(id, objective string, status clientui.RuntimeGoalStatus) *clientui.TranscriptGoal {
	return &clientui.TranscriptGoal{Goal: &clientui.Goal{ID: id, Objective: objective, Status: status, CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(1, 0)}}
}

func goalResponseFixture(goal *serverapi.RuntimeGoal) serverapi.RuntimeGoalMutationResponse { return serverapi.RuntimeGoalMutationResponse{Goal: goal, Availability: clientui.GoalAvailabilityAvailable} }
func goalEnvelopeFixture(goal *serverapi.RuntimeGoal) serverapi.RuntimeGoalShowResponse { return serverapi.RuntimeGoalShowResponse{Goal: goal, Availability: clientui.GoalAvailabilityAvailable} }

func runtimeGoalFixtureFromAPI(goal *serverapi.RuntimeGoal) *clientui.RuntimeGoal {
	return &clientui.RuntimeGoal{Goal: goal, Availability: clientui.GoalAvailabilityAvailable}
}
func runtimeGoalPendingFixture(objective string, status clientui.RuntimeGoalStatus) *clientui.RuntimeGoal {
	return &clientui.RuntimeGoal{Pending: &clientui.GoalPreview{Objective: objective, Status: status}, Availability: clientui.GoalAvailabilityAvailable}
}

func TestRuntimeClientShowGoalPreservesSuspendedLiveStatus(t *testing.T) {
	committed := &serverapi.RuntimeGoal{ID: "goal-committed", Objective: "committed goal", Status: "active"}
	controls := &reconnectRetryRuntimeControlClient{showGoalResp: goalEnvelopeFixture(committed)}
	runtimeClient := newTestSessionRuntimeClientWithControls(controls)
	liveGoal := runtimeGoalFixture(committed.ID, committed.Objective, clientui.RuntimeGoalStatusActive, true)
	runtimeClient.storeMainView(clientui.RuntimeMainView{
		Session: clientui.RuntimeSessionView{SessionID: "session-1"},
		Status:  clientui.RuntimeStatus{Goal: liveGoal},
	})

	shown, err := runtimeClient.ShowGoal()
	if err != nil {
		t.Fatalf("ShowGoal: %v", err)
	}
	if !reflect.DeepEqual(shown, runtimeGoalFixtureFromAPI(committed)) {
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
		setGoalResp:      goalResponseFixture(setGoal),
		pauseGoalResp:    goalEnvelopeFixture(pauseGoal),
		resumeGoalResp:   goalEnvelopeFixture(resumeGoal),
		completeGoalResp: goalEnvelopeFixture(completeGoal),
		clearGoalResp:    serverapi.RuntimeGoalShowResponse{Availability: clientui.GoalAvailabilityAvailable},
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
			assertRuntimeClientGoalCached(t, runtimeClient, goal, runtimeGoalFixtureFromAPI(tt.want))
			if tt.want != nil {
				assertRuntimeGoalConversionDropsAPITimestamps(t, goal, tt.want)
			}
		})
	}
}

func TestRuntimeClientInterruptDoesNotCommitRuntimeTuple(t *testing.T) {
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
	assertRuntimeTupleView(t, runtimeClient.MainView(), current)
}

func TestRuntimeClientGoalStatusEventPatchesCachedMainView(t *testing.T) {
	runtimeClient := newTestSessionRuntimeClientWithControls(&reconnectRetryRuntimeControlClient{})
	runtimeClient.storeMainView(clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{SessionID: "session-1"}})

	if _, err := runtimeClient.admitTranscriptMessageState(clientui.NewTranscriptMessage(0, clientui.NewTranscriptEvent(clientui.TranscriptGoalStatus{
		Goal:         transcriptGoalFixture("goal-1", "ship feature", clientui.RuntimeGoalStatusActive),
		Availability: clientui.GoalAvailabilityAvailable,
	})),
	); err != nil {
		t.Fatalf("admit goal status: %v", err)
	}
	assertRuntimeClientGoalCached(
		t,
		runtimeClient,
		runtimeGoalFixture("goal-1", "ship feature", clientui.RuntimeGoalStatusActive, false),
		runtimeGoalFixture("goal-1", "ship feature", clientui.RuntimeGoalStatusActive, false),
	)

	if _, err := runtimeClient.admitTranscriptMessageState(clientui.NewTranscriptMessage(0, clientui.NewTranscriptEvent(clientui.TranscriptGoalStatus{
		Goal:         transcriptGoalFixture("goal-1", "ship feature", clientui.RuntimeGoalStatusPaused),
		Availability: clientui.GoalAvailabilityAvailable,
	})),
	); err != nil {
		t.Fatalf("admit paused goal status: %v", err)
	}
	assertRuntimeClientGoalCached(
		t,
		runtimeClient,
		runtimeGoalFixture("goal-1", "ship feature", clientui.RuntimeGoalStatusPaused, false),
		runtimeGoalFixture("goal-1", "ship feature", clientui.RuntimeGoalStatusPaused, false),
	)

	if _, err := runtimeClient.admitTranscriptMessageState(clientui.NewTranscriptMessage(0, clientui.NewTranscriptEvent(clientui.TranscriptGoalStatus{Availability: clientui.GoalAvailabilityAvailable}))); err != nil {
		t.Fatalf("admit cleared goal status: %v", err)
	}
	assertRuntimeClientGoalCached(t, runtimeClient, runtimeGoalFixtureFromAPI(nil), runtimeGoalFixtureFromAPI(nil))
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
	if got == nil || got.ID != source.ID || got.Objective != source.Objective || got.Status != source.Status || !got.CreatedAt.Equal(source.CreatedAt) || !got.UpdatedAt.Equal(source.UpdatedAt) {
		t.Fatalf("converted goal = %+v, source = %+v", got, source)
	}
}

func TestRuntimeClientSubmitUserMessageRecoversRuntimeUnavailableAndReusesRequestID(t *testing.T) {
	controls := &reconnectRetryRuntimeControlClient{firstSubmitErr: serverapi.ErrRuntimeUnavailable}
	runtimeClient := newTestSessionRuntimeClientWithControls(controls)
	reactivator := newRuntimeReactivator()
	recoveryCalls := 0
	reactivator.SetReactivateFunc(func(context.Context) error {
		recoveryCalls++
		return nil
	})
	runtimeClient.SetRuntimeReactivator(reactivator)

	submission, err := runtimeClient.SubmitRuntimeInput(context.Background(), clientui.RuntimeSubmitRequest{
		ClientRequestID: runtimeids.NewRuntimeClientRequestID(),
		Input:           runtimeinput.Text("hello"),
	})
	message := ""
	if submission.Message != nil {
		message = *submission.Message
	}
	if err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	if message != "recovered" {
		t.Fatalf("SubmitUserMessage message = %q, want recovered", message)
	}
	if recoveryCalls != 1 {
		t.Fatalf("recovery call count = %d, want 1", recoveryCalls)
	}
	if got := controls.submitRequestIDs(); len(got) != 2 || got[0] == "" || got[0] != got[1] {
		t.Fatalf("submit request ids = %+v, want same non-empty id across retry", got)
	}
}

func TestRuntimeClientRecordPromptHistoryReusesRequestIDAcrossReconnect(t *testing.T) {
	controls := &reconnectRetryRuntimeControlClient{firstRecordErr: serverapi.ErrRuntimeUnavailable}
	runtimeClient := newTestSessionRuntimeClientWithControls(controls)
	reactivator := newRuntimeReactivator()
	reactivator.SetReactivateFunc(func(context.Context) error { return nil })
	runtimeClient.SetRuntimeReactivator(reactivator)

	if err := runtimeClient.RecordPromptHistory("/status"); err != nil {
		t.Fatalf("RecordPromptHistory: %v", err)
	}
	if got := controls.recordRequestIDs(); len(got) != 2 || got[0] == "" || got[0] != got[1] {
		t.Fatalf("record request ids = %+v, want same non-empty id across retry", got)
	}
}

func TestRuntimeClientSubmitUserMessageRecoversRuntimeUnavailable(t *testing.T) {
	controls := &reconnectRetryRuntimeControlClient{firstSubmitErr: serverapi.ErrRuntimeUnavailable}
	runtimeClient := newTestSessionRuntimeClientWithControls(controls)
	reactivator := newRuntimeReactivator()
	recoveryCalls := 0
	reactivator.SetReactivateFunc(func(context.Context) error {
		recoveryCalls++
		return nil
	})
	runtimeClient.SetRuntimeReactivator(reactivator)

	submission, err := runtimeClient.SubmitRuntimeInput(context.Background(), clientui.RuntimeSubmitRequest{
		ClientRequestID: runtimeids.NewRuntimeClientRequestID(),
		Input:           runtimeinput.Text("hello"),
	})
	message := ""
	if submission.Message != nil {
		message = *submission.Message
	}
	if err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	if message != "recovered" {
		t.Fatalf("SubmitUserMessage message = %q, want recovered", message)
	}
	if recoveryCalls != 1 {
		t.Fatalf("recovery call count = %d, want 1", recoveryCalls)
	}
	entries := controls.appendedLocalEntries()
	if len(entries) != 1 {
		t.Fatalf("warning entry count = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Role != "warning" || entry.Visibility != string(clientui.EntryVisibilityOngoing) {
		t.Fatalf("warning entry = %+v, want recovery warning", entry)
	}
}

func TestRuntimeClientSubmitTurnRecoveryContinuesFirstPrompt(t *testing.T) {
	controls := &reconnectRetryRuntimeControlClient{firstSubmitErr: serverapi.ErrRuntimeUnavailable}
	runtimeClient := newTestSessionRuntimeClientWithControls(controls)
	reactivator := newRuntimeReactivator()
	reactivator.SetReactivateFunc(func(context.Context) error { return nil })
	runtimeClient.SetRuntimeReactivator(reactivator)
	model := newProjectedClosedUIModel(runtimeClient)
	model.startupCmds = nil

	submitCmd := model.inputController().startSubmissionWithPromptHistoryAndQueuePositionAndID("hello after restart", preSubmitQueueBack, "")
	if submitCmd == nil {
		t.Fatal("expected submit command")
	}
	next := tea.Model(model)
	updated := next.(*uiModel)
	submitMsgs := collectCmdMessages(t, submitCmd)
	var done submitDoneMsg
	foundDone := false
	for _, msg := range submitMsgs {
		if typed, ok := msg.(submitDoneMsg); ok {
			done = typed
			foundDone = true
		}
	}
	if !foundDone {
		t.Fatalf("expected submit result, got %+v", submitMsgs)
	}
	if done.err != nil || done.message != "recovered" {
		t.Fatalf("submit result = %+v, want recovered first prompt", done)
	}
	next, _ = updated.Update(done)
	updated = next.(*uiModel)
	if updated.activity == uiActivityError {
		t.Fatal("did not expect pre-submit recovery to surface operator error")
	}
	plain := stripANSIAndTrimRight(updated.view.View())
	if strings.Contains(plain, serverapi.ErrRuntimeUnavailable.Error()) || strings.Contains(plain, "runtime for session") {
		t.Fatalf("did not expect recovery diagnostics in ongoing transcript, got %q", plain)
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

func TestRuntimeClientShowGoalRecoversRuntimeUnavailableSilently(t *testing.T) {
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
	if err != nil {
		t.Fatalf("ShowGoal: %v", err)
	}
	if recoveryCalls != 1 {
		t.Fatalf("recovery call count = %d, want 1", recoveryCalls)
	}
	if controls.showGoalCalls != 2 {
		t.Fatalf("show goal call count = %d, want 2", controls.showGoalCalls)
	}
	if got == nil || got.ID != "goal-1" || got.Objective != "ship" || got.Status != clientui.RuntimeGoalStatusActive {
		t.Fatalf("goal = %+v, want recovered active goal", got)
	}
	if entries := controls.appendedLocalEntries(); len(entries) != 0 {
		t.Fatalf("did not expect visible recovery warning during goal read, got %+v", entries)
	}
}

func TestRuntimeClientReconnectWarningFailureDoesNotBlockSubmit(t *testing.T) {
	controls := &reconnectRetryRuntimeControlClient{firstSubmitErr: serverapi.ErrRuntimeUnavailable, appendErr: serverapi.ErrRuntimeUnavailable}
	runtimeClient := newTestSessionRuntimeClientWithControls(controls)
	warnings := make(chan runtimeReconnectWarningMsg, 1)
	runtimeClient.SetRuntimeReconnectWarningObserver(func(text string, visibility clientui.EntryVisibility) {
		warnings <- runtimeReconnectWarningMsg{text: text, visibility: visibility}
	})
	reactivator := newRuntimeReactivator()
	reactivator.SetReactivateFunc(func(context.Context) error { return nil })
	runtimeClient.SetRuntimeReactivator(reactivator)

	submission, err := runtimeClient.SubmitRuntimeInput(context.Background(), clientui.RuntimeSubmitRequest{
		ClientRequestID: runtimeids.NewRuntimeClientRequestID(),
		Input:           runtimeinput.Text("hello"),
	})
	message := ""
	if submission.Message != nil {
		message = *submission.Message
	}
	if err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	if message != "recovered" {
		t.Fatalf("SubmitUserMessage message = %q, want recovered", message)
	}
	if entries := controls.appendedLocalEntries(); len(entries) != 1 {
		t.Fatalf("warning append attempts = %d, want 1", len(entries))
	}
	select {
	case warning := <-warnings:
		if warning.visibility != clientui.EntryVisibilityOngoing {
			t.Fatalf("warning = %+v, want lease recovery warning", warning)
		}
	default:
		t.Fatal("expected warning fallback notification")
	}
}
