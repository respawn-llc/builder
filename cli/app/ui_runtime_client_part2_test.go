package app

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"core/server/registry"
	"core/server/runtimecontrol"
	"core/shared/clientui"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

func TestRuntimeClientMainViewDoesNotRefreshCachedSnapshotBehindUIBack(t *testing.T) {
	reads := &countingSessionViewClient{view: clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{SessionID: "session-1"}}}
	controls := runtimecontrol.NewService(registry.NewRuntimeRegistry())
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
	firstQueueErr    error
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
	queueCalls       int
	recordCalls      int
	submitRequestID  []string
	submitRefs       []clientui.RuntimeOperationRef
	queueRequestID   []string
	recordRequestID  []string
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

func (c *reconnectRetryRuntimeControlClient) submitRequestIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.submitRequestID...)
}

func (c *reconnectRetryRuntimeControlClient) queueRequestIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.queueRequestID...)
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
	c.submitRefs = append(c.submitRefs, req.OperationRef)
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

func (c *reconnectRetryRuntimeControlClient) CompactContextForPreSubmit(context.Context, serverapi.RuntimeCompactContextForPreSubmitRequest) error {
	return nil
}

func (c *reconnectRetryRuntimeControlClient) HasQueuedUserWork(context.Context, serverapi.RuntimeHasQueuedUserWorkRequest) (serverapi.RuntimeHasQueuedUserWorkResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queuedWorkCalls++
	if c.queuedWorkCalls == 1 && c.queuedWorkErr != nil {
		return serverapi.RuntimeHasQueuedUserWorkResponse{}, c.queuedWorkErr
	}
	return serverapi.RuntimeHasQueuedUserWorkResponse{HasQueuedUserWork: c.queuedWork}, nil
}

func (c *reconnectRetryRuntimeControlClient) SubmitQueuedUserMessages(context.Context, serverapi.RuntimeSubmitQueuedUserMessagesRequest) (serverapi.RuntimeSubmitQueuedUserMessagesResponse, error) {
	return serverapi.RuntimeSubmitQueuedUserMessagesResponse{}, nil
}

func (c *reconnectRetryRuntimeControlClient) Interrupt(_ context.Context, req serverapi.RuntimeInterruptRequest) (serverapi.RuntimeInterruptResponse, error) {
	c.mu.Lock()
	c.interruptReq = req
	c.mu.Unlock()
	return c.interruptResp, nil
}

func (c *reconnectRetryRuntimeControlClient) QueueUserMessage(_ context.Context, req serverapi.RuntimeQueueUserMessageRequest) (serverapi.RuntimeQueueUserMessageResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queueCalls++
	c.queueRequestID = append(c.queueRequestID, req.ClientRequestID)
	if c.queueCalls == 1 && c.firstQueueErr != nil {
		return serverapi.RuntimeQueueUserMessageResponse{}, c.firstQueueErr
	}
	return serverapi.RuntimeQueueUserMessageResponse{QueueItemID: "queue-1", Text: req.Text, ClientRequestID: req.ClientRequestID}, nil
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

func TestRuntimeClientGoalMethodsPatchCachedMainView(t *testing.T) {
	showGoal := &serverapi.RuntimeGoal{ID: "goal-show", Objective: "show goal", Status: "paused", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	setGoal := &serverapi.RuntimeGoal{ID: "goal-set", Objective: "set goal", Status: "active", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	pauseGoal := &serverapi.RuntimeGoal{ID: "goal-pause", Objective: "pause goal", Status: "paused", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	resumeGoal := &serverapi.RuntimeGoal{ID: "goal-resume", Objective: "resume goal", Status: "active", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	completeGoal := &serverapi.RuntimeGoal{ID: "goal-complete", Objective: "complete goal", Status: "complete", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	controls := &reconnectRetryRuntimeControlClient{
		showGoalResp:     serverapi.RuntimeGoalShowResponse{Goal: showGoal},
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

	goal, err := runtimeClient.ShowGoal()
	if err != nil {
		t.Fatalf("ShowGoal: %v", err)
	}
	assertRuntimeClientGoalCached(t, runtimeClient, goal, runtimeGoalFromAPI(showGoal))
	assertRuntimeGoalConversionDropsAPITimestamps(t, goal, showGoal)

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
		})
	}
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

	runtimeClient.observeRuntimeEventStatus(clientui.Event{
		Kind: clientui.EventGoalStatusUpdated,
		GoalStatus: &clientui.RuntimeGoalStatusUpdate{
			ID:        "goal-1",
			Objective: "ship feature",
			Status:    clientui.RuntimeGoalStatusActive,
		},
	})
	assertRuntimeClientGoalCached(t, runtimeClient, &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusActive}, &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusActive})

	runtimeClient.observeRuntimeEventStatus(clientui.Event{
		Kind: clientui.EventGoalStatusUpdated,
		GoalStatus: &clientui.RuntimeGoalStatusUpdate{
			ID:        "goal-1",
			Objective: "ship feature",
			Status:    clientui.RuntimeGoalStatusPaused,
		},
	})
	assertRuntimeClientGoalCached(t, runtimeClient, &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusPaused}, &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusPaused})

	runtimeClient.observeRuntimeEventStatus(clientui.Event{
		Kind:       clientui.EventGoalStatusUpdated,
		GoalStatus: &clientui.RuntimeGoalStatusUpdate{Cleared: true},
	})
	assertRuntimeClientGoalCached(t, runtimeClient, nil, nil)
}

func TestRuntimeClientGoalStatusEventNormalizesSuspendedCache(t *testing.T) {
	tests := []struct {
		name     string
		existing *clientui.RuntimeGoal
		update   clientui.RuntimeGoalStatusUpdate
		want     *clientui.RuntimeGoal
	}{
		{
			name:     "non active does not preserve suspended",
			existing: &clientui.RuntimeGoal{ID: "goal-1", Objective: "old", Status: clientui.RuntimeGoalStatusActive, Suspended: true},
			update:   clientui.RuntimeGoalStatusUpdate{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusComplete},
			want:     &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusComplete},
		},
		{
			name:     "new active id does not preserve suspended",
			existing: &clientui.RuntimeGoal{ID: "goal-1", Objective: "old", Status: clientui.RuntimeGoalStatusActive, Suspended: true},
			update:   clientui.RuntimeGoalStatusUpdate{ID: "goal-2", Objective: "next", Status: clientui.RuntimeGoalStatusActive},
			want:     &clientui.RuntimeGoal{ID: "goal-2", Objective: "next", Status: clientui.RuntimeGoalStatusActive},
		},
		{
			name:     "paused to active does not preserve suspended",
			existing: &clientui.RuntimeGoal{ID: "goal-1", Objective: "old", Status: clientui.RuntimeGoalStatusPaused, Suspended: true},
			update:   clientui.RuntimeGoalStatusUpdate{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusActive},
			want:     &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusActive},
		},
		{
			name:     "same active id preserves suspended",
			existing: &clientui.RuntimeGoal{ID: "goal-1", Objective: "old", Status: clientui.RuntimeGoalStatusActive, Suspended: true},
			update:   clientui.RuntimeGoalStatusUpdate{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusActive},
			want:     &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusActive, Suspended: true},
		},
		{
			name:     "clear removes suspended goal",
			existing: &clientui.RuntimeGoal{ID: "goal-1", Objective: "old", Status: clientui.RuntimeGoalStatusActive, Suspended: true},
			update:   clientui.RuntimeGoalStatusUpdate{Cleared: true},
			want:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runtimeClient := newTestSessionRuntimeClientWithControls(&reconnectRetryRuntimeControlClient{})
			runtimeClient.storeMainView(clientui.RuntimeMainView{
				Session: clientui.RuntimeSessionView{SessionID: "session-1"},
				Status:  clientui.RuntimeStatus{Goal: cloneRuntimeGoal(tt.existing)},
			})

			runtimeClient.observeRuntimeEventStatus(clientui.Event{Kind: clientui.EventGoalStatusUpdated, GoalStatus: &tt.update})

			view, ok := runtimeClient.CachedMainView()
			if !ok {
				t.Fatal("expected cached main view")
			}
			if !reflect.DeepEqual(view.Status.Goal, tt.want) {
				t.Fatalf("cached goal = %+v, want %+v", view.Status.Goal, tt.want)
			}
		})
	}
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
	if got == nil || got.ID != source.ID || got.Objective != source.Objective || string(got.Status) != source.Status || got.Suspended != source.Suspended {
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
		OperationRef: clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "submit-reconnect"},
		Text:         "hello",
	})
	message := submission.Message
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

func TestRuntimeClientQueueUserMessageReusesRequestIDAcrossReconnect(t *testing.T) {
	controls := &reconnectRetryRuntimeControlClient{firstQueueErr: serverapi.ErrRuntimeUnavailable}
	runtimeClient := newTestSessionRuntimeClientWithControls(controls)
	reactivator := newRuntimeReactivator()
	reactivator.SetReactivateFunc(func(context.Context) error { return nil })
	runtimeClient.SetRuntimeReactivator(reactivator)

	item, err := runtimeClient.QueueRuntimeUserMessage(clientui.RuntimeQueueUserMessageRequest{
		OperationRef: clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindQueuedMessage, ClientRequestID: "queue-reconnect"},
		Text:         "queued",
	})
	if err != nil {
		t.Fatalf("QueueUserMessage: %v", err)
	}
	if item.ID != "queue-1" || item.Text != "queued" {
		t.Fatalf("queued item = %+v, want server response", item)
	}
	if got := controls.queueRequestIDs(); len(got) != 2 || got[0] == "" || got[0] != got[1] {
		t.Fatalf("queue request ids = %+v, want same non-empty id across retry", got)
	} else if item.ClientRequestID != got[0] {
		t.Fatalf("queued item client request id = %q, want %q", item.ClientRequestID, got[0])
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
		OperationRef: clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "submit-warning-fallback"},
		Text:         "hello",
	})
	message := submission.Message
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
	if _, err := runtimeClient.refreshMainViewSync(uiRuntimeReadTimeout, nil); !errors.Is(err, context.DeadlineExceeded) {
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

func TestRuntimeClientHasQueuedUserWorkRecoversRuntimeUnavailableSilently(t *testing.T) {
	controls := &reconnectRetryRuntimeControlClient{
		queuedWorkErr: serverapi.ErrRuntimeUnavailable,
		queuedWork:    true,
	}
	runtimeClient := newTestSessionRuntimeClientWithControls(controls)
	reactivator := newRuntimeReactivator()
	recoveryCalls := 0
	reactivator.SetReactivateFunc(func(context.Context) error {
		recoveryCalls++
		return nil
	})
	runtimeClient.SetRuntimeReactivator(reactivator)

	hasWork, err := runtimeClient.HasQueuedUserWork()
	if err != nil {
		t.Fatalf("HasQueuedUserWork: %v", err)
	}
	if !hasWork {
		t.Fatal("HasQueuedUserWork = false, want true")
	}
	if recoveryCalls != 1 {
		t.Fatalf("recovery call count = %d, want 1", recoveryCalls)
	}
	if controls.queuedWorkCalls != 2 {
		t.Fatalf("queued-work call count = %d, want 2", controls.queuedWorkCalls)
	}
	if entries := controls.appendedLocalEntries(); len(entries) != 0 {
		t.Fatalf("did not expect visible recovery warning during queued-work read, got %+v", entries)
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
		OperationRef: clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "submit-warning-does-not-block"},
		Text:         "hello",
	})
	message := submission.Message
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
