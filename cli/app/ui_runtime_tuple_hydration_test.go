package app

import (
	"fmt"
	"reflect"
	"strconv"
	"sync"
	"testing"

	"core/cli/tui/ongoing"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/transcript"
)

func TestStaleContentCompleteHydrationFailsBeforeAnySideEffect(t *testing.T) {
	runtimeClient := newTestSessionRuntimeClient(
		&countingSessionViewClient{},
		newUnavailableRuntimeControlService(),
	)
	current := runtimeTupleTestView(11, runtimeTupleTestIdleActivity())
	current.Status.ThinkingLevel = "current"
	current.Session.SessionName = "current"
	runtimeClient.storeMainView(current)
	m := newProjectedTestUIModel(runtimeClient)
	m.queued = []queuedInputItem{{ID: "existing", Text: "existing"}}
	m.reasoningStatusHeader = "existing reasoning"
	surface := &ongoingSurfaceSpy{}
	controller := newOngoingTranscriptController(
		surface,
		m.ongoingFrameInput,
		runtimeClient.admitTranscriptMessageState,
		m.applyAdmittedTranscriptMessageState,
	)
	beforeView := runtimeClient.MainView()
	beforeActivity := m.runtimeActivityProjection
	beforeQueue := append([]queuedInputItem(nil), m.queued...)
	beforeReasoning := m.reasoningStatusHeader
	beforeLifecycle := m.runtimeLifecycle
	beforeAsk := m.ask

	hydration := runtimeTupleTestRichHydration(10)
	_, cmd, err := controller.Accept(hydration)
	assertRuntimeTupleHydrationError(t, err)
	if cmd != nil {
		t.Fatal("stale hydration returned a state command")
	}
	assertUnchanged(t, "cached main view", runtimeClient.MainView(), beforeView)
	assertUnchanged(t, "UI activity", m.runtimeActivityProjection, beforeActivity)
	assertUnchanged(t, "UI queue", m.queued, beforeQueue)
	assertUnchanged(t, "UI reasoning", m.reasoningStatusHeader, beforeReasoning)
	assertUnchanged(t, "UI lifecycle", m.runtimeLifecycle, beforeLifecycle)
	assertUnchanged(t, "ask controller", m.ask, beforeAsk)
	if controller.hydrated || controller.lastSequence != 0 || len(controller.liveReadModel.sections) != 0 {
		t.Fatalf(
			"stale hydration changed controller state: hydrated=%t sequence=%d sections=%+v",
			controller.hydrated,
			controller.lastSequence,
			controller.liveReadModel.sections,
		)
	}
	if len(surface.calls) != 0 {
		t.Fatalf("stale hydration reached terminal surface: %+v", surface.calls)
	}
}

func TestStaleHydrationDeveloperErrorPanicsInEveryModeBeforeSideEffects(t *testing.T) {
	for _, debugMode := range []bool{false, true} {
		t.Run(map[bool]string{false: "release", true: "debug"}[debugMode], func(t *testing.T) {
			runtimeClient := newTestSessionRuntimeClient(
				&countingSessionViewClient{},
				newUnavailableRuntimeControlService(),
			)
			current := runtimeTupleTestView(11, runtimeTupleTestIdleActivity())
			runtimeClient.storeMainView(current)
			m := sizedTestUIModel(newProjectedTestUIModel(runtimeClient, WithUIDebug(debugMode)), 93, 31)
			m.queued = []queuedInputItem{{ID: "queued-1", Text: "do not flush"}}
			surface := &ongoingSurfaceSpy{}
			m.ongoingTranscript = newOngoingTranscriptController(
				surface,
				m.ongoingFrameInput,
				runtimeClient.admitTranscriptMessageState,
				m.applyAdmittedTranscriptMessageState,
			)
			beforeView := runtimeClient.MainView()
			beforeQueue := append([]queuedInputItem(nil), m.queued...)
			hydration := runtimeTupleTestRichHydration(10)

			recovered := capturePanic(func() {
				_ = m.handleOngoingTranscriptEvent(ongoingTranscriptEvent{
					Kind:    ongoingTranscriptEventMessage,
					Message: hydration,
				})
			})
			developerErr, ok := recovered.(ongoing.DeveloperError)
			if !ok {
				t.Fatalf("panic = %T, want ongoing.DeveloperError", recovered)
			}
			if developerErr.Operation != "admit_transcript_hydration_runtime_tuple" {
				t.Fatalf("developer-error operation = %q", developerErr.Operation)
			}
			if got := developerErr.Facts["terminal_size"]; got != (ongoing.Size{Width: 93, Height: 31}) {
				t.Fatalf("terminal size diagnostic = %+v, want 93x31", got)
			}
			wantPayload := strconv.Quote(fmt.Sprintf("%+v", hydration.Payload().(clientui.TranscriptHydration)))
			if got, ok := developerErr.Facts["quoted_payload"].(string); !ok || got != wantPayload {
				t.Fatalf("quoted payload diagnostic = %#v, want %#v", developerErr.Facts["quoted_payload"], wantPayload)
			}
			if developerErr.Stack == "" {
				t.Fatal("developer error omitted stack trace")
			}
			assertUnchanged(t, "cached main view", runtimeClient.MainView(), beforeView)
			assertUnchanged(t, "queued input", m.queued, beforeQueue)
			if len(surface.calls) != 0 {
				t.Fatalf("stale hydration reached terminal surface: %+v", surface.calls)
			}
		})
	}
}

func capturePanic(action func()) (recovered any) {
	defer func() {
		recovered = recover()
	}()
	action()
	return nil
}

func TestNonStaleContentCompleteHydrationAppliesWholeEvent(t *testing.T) {
	runtimeClient := newTestSessionRuntimeClient(
		&countingSessionViewClient{},
		newUnavailableRuntimeControlService(),
	)
	current := runtimeTupleTestView(10, runtimeTupleTestIdleActivity())
	runtimeClient.storeMainView(current)
	m := newProjectedTestUIModel(runtimeClient)
	surface := &ongoingSurfaceSpy{}
	controller := newOngoingTranscriptController(
		surface,
		m.ongoingFrameInput,
		runtimeClient.admitTranscriptMessageState,
		m.applyAdmittedTranscriptMessageState,
	)
	hydration := runtimeTupleTestRichHydration(11)

	if _, _, err := controller.Accept(hydration); err != nil {
		t.Fatalf("accept current hydration: %v", err)
	}

	wantUpdate := hydration.Payload().(clientui.TranscriptHydration).RuntimeReadModelUpdate
	assertRuntimeTupleView(t, runtimeClient.MainView(), runtimeTupleTestView(
		11,
		wantUpdate.Activity,
	))
	if !controller.hydrated || controller.lastSequence != 1 {
		t.Fatalf("delivery state = hydrated=%t sequence=%d, want true/1", controller.hydrated, controller.lastSequence)
	}
	if got, want := surface.appliedKinds(), []clientui.TranscriptMessageKind{clientui.TranscriptMessageHydration}; !reflect.DeepEqual(got, want) {
		t.Fatalf("surface messages = %v, want %v", got, want)
	}
	if m.reasoningStatusHeader != "reasoning" {
		t.Fatalf("reasoning status = %q, want reasoning", m.reasoningStatusHeader)
	}
	if !m.runtimeLifecycle.Reviewer.IsRunning() || !m.runtimeLifecycle.Compaction.IsRunning() {
		t.Fatalf("hydrated lifecycle missing reviewer/compaction: %+v", m.runtimeLifecycle)
	}
	if m.currentRunID == "" || m.currentStepID == "" || !m.runtimeActivityBusy() {
		t.Fatalf("hydrated running state missing: activity=%+v run=%q step=%q", m.runtimeActivityProjection, m.currentRunID, m.currentStepID)
	}
	if len(m.injectedQueue) != 0 {
		t.Fatalf("foreign hydrated queue created local restoration ownership: %+v", m.injectedQueue)
	}
	if len(controller.liveReadModel.sections) == 0 {
		t.Fatal("hydrated tools/prompts/queue did not reach controller live state")
	}
	m.goal.open = true
	m.goal.pending = &clientui.GoalPreview{Objective: "queued", Status: clientui.RuntimeGoalStatusActive}
	goal := runtimeClientTestGoal("goal-1", "hydrated", clientui.RuntimeGoalStatusPaused)
	m.applyAdmittedTranscriptMessageState(hydration, runtimeTupleMergeResult{view: clientui.RuntimeMainView{Status: clientui.RuntimeStatus{Goal: &clientui.RuntimeGoal{Goal: goal}}}})
	if m.goal.pending != nil || *m.goal.goal != *goal {
		t.Fatal("hydration did not replace queued Goal preview")
	}
}

func TestHydrationReplacesStaleBackgroundProcessEntries(t *testing.T) {
	runtimeClient := newTestSessionRuntimeClient(
		&countingSessionViewClient{},
		newUnavailableRuntimeControlService(),
	)
	current := runtimeTupleTestView(10, runtimeTupleTestRunningActivity())
	runtimeClient.storeMainView(current)
	m := newProjectedTestUIModel(runtimeClient)
	m.processList.entries = []clientui.BackgroundProcess{
		{ID: "completed-while-disconnected", OwnerSessionID: ongoingTestSessionID().String(), Running: true, Backgrounded: true},
		{ID: "other-session", OwnerSessionID: "other-session", Running: true, Backgrounded: true},
	}
	m.ongoingTranscript = newOngoingTranscriptController(
		&ongoingSurfaceSpy{},
		m.ongoingFrameInput,
		runtimeClient.admitTranscriptMessageState,
		m.applyAdmittedTranscriptMessageState,
	)
	hydration := runtimeTupleTestRichHydration(11)
	payload := hydration.Payload().(clientui.TranscriptHydration)
	payload.BackgroundActivities = []clientui.TranscriptBackgroundActivity{{
		ActivityID:  runtimeids.NewBackgroundActivityID(),
		ProcessID:   "still-running",
		OwnerRunID:  ongoingTestRunID(),
		OwnerStepID: ongoingTestStepID(),
		Lifecycle:   clientui.BackgroundLifecycleBackgrounded,
		Command:     "sleep 10",
		Workdir:     "/workspace",
	}}
	hydration = clientui.NewTranscriptMessage(1, clientui.NewTranscriptEvent(payload))

	if _, _, err := m.ongoingTranscript.Accept(hydration); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}
	if len(m.processList.entries) != 2 || m.processList.entries[0].ID != "still-running" || m.processList.entries[1].ID != "other-session" {
		t.Fatalf("hydrated process entries = %+v, want fresh current-session process plus other-session cache", m.processList.entries)
	}
}

func TestHydrationReplacesStaleCompletedCompactionCountWithoutActiveCompaction(t *testing.T) {
	runtimeClient := newTestSessionRuntimeClient(
		&countingSessionViewClient{},
		newUnavailableRuntimeControlService(),
	)
	current := runtimeTupleTestView(10, runtimeTupleTestIdleActivity())
	current.Status.CompactionCount = 9
	runtimeClient.storeMainView(current)
	m := newProjectedTestUIModel(runtimeClient)
	surface := &ongoingSurfaceSpy{}
	controller := newOngoingTranscriptController(
		surface,
		m.ongoingFrameInput,
		runtimeClient.admitTranscriptMessageState,
		m.applyAdmittedTranscriptMessageState,
	)
	hydration := runtimeTupleTestRichHydration(11)
	payload := hydration.Payload().(clientui.TranscriptHydration)
	payload.SessionStatus.CompactionCount = 4
	payload.ActiveCompaction = nil
	hydration = clientui.NewTranscriptMessage(1, clientui.NewTranscriptEvent(payload))

	if _, _, err := controller.Accept(hydration); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}
	if got := runtimeClient.MainView().Status.CompactionCount; got != 4 {
		t.Fatalf("hydrated compaction count = %d, want 4", got)
	}
}

func TestAcceptedHydrationDoesNotAdvanceCacheWithUnaryRead(t *testing.T) {
	v12 := runtimeTupleTestView(12, runtimeTupleTestIdleActivity())
	reads := &countingSessionViewClient{view: v12}
	runtimeClient := newTestSessionRuntimeClient(reads, newUnavailableRuntimeControlService())
	runtimeClient.storeMainView(runtimeTupleTestView(
		10,
		runtimeTupleTestIdleActivity(),
	))
	m := newProjectedTestUIModel(runtimeClient)
	surface := &ongoingSurfaceSpy{}
	m.ongoingTranscript = newOngoingTranscriptController(
		surface,
		m.ongoingFrameInput,
		runtimeClient.admitTranscriptMessageState,
		m.applyAdmittedTranscriptMessageState,
	)
	hydration := runtimeTupleTestRichHydration(11)

	cmd := m.handleOngoingTranscriptEvent(ongoingTranscriptEvent{
		Kind:    ongoingTranscriptEventMessage,
		Message: hydration,
	})

	if got := reads.mainViewCount.Load(); got != 0 {
		t.Fatalf("main-view reads before hydration consumption returned = %d, want 0", got)
	}
	if got := surface.appliedKinds(); !reflect.DeepEqual(got, []clientui.TranscriptMessageKind{clientui.TranscriptMessageHydration}) {
		t.Fatalf("surface messages before refresh execution = %v", got)
	}
	_ = collectCmdMessages(t, cmd)
	if got := reads.mainViewCount.Load(); got != 0 {
		t.Fatalf("main-view reads after accepted hydration = %d, want 0", got)
	}
	if m.runtimeMainViewBusy || m.runtimeMainViewPendingSet {
		t.Fatalf(
			"accepted hydration scheduled unary refresh: busy=%t pending=%t",
			m.runtimeMainViewBusy,
			m.runtimeMainViewPendingSet,
		)
	}
	assertRuntimeTupleView(t, runtimeClient.MainView(), runtimeTupleTestView(
		11,
		hydration.Payload().(clientui.TranscriptHydration).RuntimeReadModelUpdate.Activity,
	))
}

func TestRejectedDuplicateHydrationDoesNotStartUnaryRefresh(t *testing.T) {
	v12 := runtimeTupleTestView(12, runtimeTupleTestIdleActivity())
	reads := &countingSessionViewClient{view: v12}
	runtimeClient := newTestSessionRuntimeClient(reads, newUnavailableRuntimeControlService())
	runtimeClient.storeMainView(runtimeTupleTestView(
		10,
		runtimeTupleTestIdleActivity(),
	))
	m := newProjectedTestUIModel(runtimeClient)
	m.ongoingTranscript = newOngoingTranscriptController(
		&ongoingSurfaceSpy{},
		m.ongoingFrameInput,
		runtimeClient.admitTranscriptMessageState,
		m.applyAdmittedTranscriptMessageState,
	)
	hydration := runtimeTupleTestRichHydration(11)
	if _, _, err := m.ongoingTranscript.Accept(hydration); err != nil {
		t.Fatalf("accept initial hydration: %v", err)
	}

	cmd := m.handleOngoingTranscriptEvent(ongoingTranscriptEvent{
		Kind:    ongoingTranscriptEventMessage,
		Message: hydration,
	})
	_ = collectCmdMessages(t, cmd)

	if got := reads.mainViewCount.Load(); got != 0 {
		t.Fatalf("rejected duplicate hydration main-view reads = %d, want 0", got)
	}
	if m.runtimeMainViewBusy || m.runtimeMainViewPendingSet {
		t.Fatalf(
			"rejected duplicate hydration scheduled refresh: busy=%t pending=%t",
			m.runtimeMainViewBusy,
			m.runtimeMainViewPendingSet,
		)
	}
}

func TestHydrationAdmissionSerializesUnaryAndInterruptTupleCommitsUntilWholeEventCompletes(t *testing.T) {
	v12 := runtimeTupleTestView(12, runtimeTupleTestIdleActivity())
	v12.Session.SessionID = ongoingTestSessionID().String()
	v12.Status.ThinkingLevel = "captured unary"
	reads := &countingSessionViewClient{view: v12}
	controls := &reconnectRetryRuntimeControlClient{interruptResp: serverapi.RuntimeInterruptResponse{
		Version:  clientui.ReadModelVersion{Epoch: "runtime-tuple-test", Generation: 1, Sequence: 13},
		Activity: runtimeTupleTestIdleActivity(),
	}}
	runtimeClient := newTestSessionRuntimeClient(reads, controls)
	v10 := runtimeTupleTestView(10, runtimeTupleTestIdleActivity())
	v10.Session.SessionID = ongoingTestSessionID().String()
	runtimeClient.storeMainView(v10)
	m := newProjectedTestUIModel(runtimeClient)
	surface := newBlockingOngoingSurface()
	controller := newOngoingTranscriptController(
		surface,
		m.ongoingFrameInput,
		runtimeClient.admitTranscriptMessageState,
		m.applyAdmittedTranscriptMessageState,
	)
	hydration := runtimeTupleTestRichHydration(11)
	acceptDone := make(chan error, 1)
	go func() {
		_, _, err := controller.Accept(hydration)
		acceptDone <- err
	}()
	<-surface.started

	wantV11 := runtimeTupleTestView(
		11,
		hydration.Payload().(clientui.TranscriptHydration).RuntimeReadModelUpdate.Activity,
	)
	assertRuntimeTupleView(t, runtimeClient.MainView(), wantV11)
	if !m.runtimeActivityBusy() || controller.lastSequence != 1 || !controller.hydrated {
		t.Fatalf(
			"hydration admission was not coherent before terminal completion: busy=%t hydrated=%t sequence=%d",
			m.runtimeActivityBusy(),
			controller.hydrated,
			controller.lastSequence,
		)
	}
	if surface.completedApplyCount() != 0 {
		t.Fatal("terminal hydration completed before release")
	}

	refresh := m.startRuntimeMainViewRefreshRequest(runtimeMainViewRefreshRequestForCause(runtimeMainViewRefreshCauseManual))
	refreshMessage, ok := refresh.cmd().(runtimeMainViewRefreshedMsg)
	if !ok {
		t.Fatalf("refresh command returned an unexpected message")
	}
	if refreshMessage.view.Version != v12.Version {
		t.Fatalf("refresh candidate version = %+v, want %+v", refreshMessage.view.Version, v12.Version)
	}
	assertRuntimeTupleView(t, runtimeClient.MainView(), wantV11)

	interruptCommand := m.runtimeControlCommand(runtimeControlInterrupt, "", false, "")
	interruptMessage, ok := interruptCommand().(runtimeControlDoneMsg)
	if !ok {
		t.Fatalf("interrupt command returned an unexpected message")
	}
	if interruptMessage.runtimeTuple == nil || interruptMessage.runtimeTuple.Version.Sequence != 13 {
		t.Fatalf("interrupt candidate = %+v, want V13", interruptMessage.runtimeTuple)
	}
	assertRuntimeTupleView(t, runtimeClient.MainView(), wantV11)
	if surface.completedApplyCount() != 0 {
		t.Fatal("worker completion partially replaced terminal hydration")
	}

	close(surface.release)
	if err := <-acceptDone; err != nil {
		t.Fatalf("finish hydration: %v", err)
	}
	if surface.completedApplyCount() != 1 {
		t.Fatalf("completed terminal hydration count = %d, want 1", surface.completedApplyCount())
	}

	next, _ := m.Update(refreshMessage)
	m = next.(*uiModel)
	assertRuntimeTupleView(t, runtimeClient.MainView(), v12)
	next, _ = m.Update(interruptMessage)
	m = next.(*uiModel)
	wantV13 := runtimeTupleTestView(13, controls.interruptResp.Activity)
	assertRuntimeTupleView(t, runtimeClient.MainView(), wantV13)
	if m.runtimeActivityBusy() || m.runtimeActivityBlocksInput() {
		t.Fatalf("reduced monotonic candidates left runtime busy: %+v", m.runtimeActivityProjection)
	}
}

type blockingOngoingSurface struct {
	started   chan struct{}
	release   chan struct{}
	startOnce sync.Once
	mu        sync.Mutex
	applied   int
}

func newBlockingOngoingSurface() *blockingOngoingSurface {
	return &blockingOngoingSurface{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (s *blockingOngoingSurface) ApplyTerminalMessage(clientui.TranscriptMessage, ongoing.FrameInput) (ongoing.Result, error) {
	s.startOnce.Do(func() { close(s.started) })
	<-s.release
	s.mu.Lock()
	s.applied++
	s.mu.Unlock()
	return ongoing.Result{}, nil
}

func (s *blockingOngoingSurface) Render(ongoing.FrameInput) (ongoing.Result, error) {
	return ongoing.Result{}, nil
}

func (s *blockingOngoingSurface) Resize(ongoing.Size, ongoing.FrameInput) (ongoing.Result, error) {
	return ongoing.Result{}, nil
}

func (s *blockingOngoingSurface) completedApplyCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applied
}

func runtimeTupleTestRichHydration(runtimeSequence uint64) clientui.TranscriptMessage {
	message := runtimeTupleTestHydration(
		runtimeSequence,
		runtimeTupleTestRunningActivity(),
	)
	hydration := message.Payload().(clientui.TranscriptHydration)
	stepID := ongoingTestStepID()
	runID := ongoingTestRunID()
	hydration.RuntimeReadModelUpdate.Activity.ActiveStep = &clientui.RuntimeActiveStep{
		RunID:      runID,
		StepID:     stepID,
		ActiveKind: clientui.RuntimeActivityActiveKindUserTurn,
	}
	row := ongoingTranscriptMessage(2, clientui.TranscriptMessageCommittedRow).Payload().(clientui.TranscriptCommittedRow)
	row.User.Text = "committed hydration row"
	hydration.CommittedRows = []clientui.TranscriptCommittedRow{row}
	hydration.ActiveAssistant = &clientui.TranscriptAssistantStream{
		StepID:   stepID,
		StreamID: runtimeids.NewAssistantStreamID(),
		Text:     "assistant hydration",
		Phase:    transcript.AssistantPhaseCommentary,
	}
	hydration.ActiveThinkingStatus = &clientui.TranscriptThinkingStatusUpdate{
		StepID: stepID,
		Text:   "reasoning",
	}
	traceID := runtimeids.NewReasoningTraceID()
	hydration.ActiveReasoningTraces = []clientui.TranscriptReasoningTraceUpdate{{
		StepID: stepID,
		Identity: clientui.TranscriptReasoningTraceIdentity{
			Kent: &traceID,
		},
		CompactText: "reasoning body",
		Text:        "reasoning body",
	}}
	hydration.ActiveStep = &clientui.TranscriptStepState{
		RunID:      runID,
		StepID:     stepID,
		Lifecycle:  clientui.StepLifecycleStarted,
		ActiveKind: clientui.RuntimeActivityActiveKindUserTurn,
		Status:     clientui.RunStatusRunning,
	}
	hydration.ActiveReviewer = &clientui.TranscriptReviewerState{
		StepID: stepID,
		State:  clientui.ReviewerStateRunning,
	}
	hydration.ActiveCompaction = &clientui.TranscriptCompactionStatus{
		StepID: stepID,
		State:  clientui.CompactionStarted,
		Mode:   "auto",
		Count:  1,
	}
	hydration.InFlightTools = []clientui.TranscriptToolStart{{
		StepID:     stepID,
		ToolCallID: "tool-1",
		ToolName:   "shell",
	}}
	queuedText := "queued hydration"
	hydration.QueuedMessages = []clientui.TranscriptQueuedMessageState{{
		ClientRequestID: runtimeids.NewRuntimeClientRequestID(),
		QueueItemID:     runtimeids.NewQueueItemID(),
		Status:          clientui.QueuedUserMessageAccepted,
		Text:            &queuedText,
	}}
	hydration.PendingPrompts = []clientui.TranscriptPrompt{
		testQuestionPrompt("prompt-1", "Approve hydration?", "yes"),
	}
	return clientui.NewTranscriptMessage(1, clientui.NewTranscriptEvent(hydration))
}
