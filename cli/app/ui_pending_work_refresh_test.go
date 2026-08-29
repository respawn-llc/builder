package app

import (
	"errors"
	"testing"

	"core/shared/clientui"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
)

func TestPendingWorkRefreshHydrationScopeAndCoalescing(t *testing.T) {
	sessionA, sessionB := ongoingTestSessionID(), runtimeids.NewSessionID()
	m := newProjectedStaticUIModel()
	m.pendingWorkRefresh.collection = pendingWorkRefreshTestWork("prior")
	if m.advancePendingWorkRefreshScope(sessionA) == nil {
		t.Fatal("hydration did not request Pending Work")
	}
	assertPendingWorkTexts(t, m)
	m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{pendingWork: pendingWorkRefreshTestWork("stale")})
	assertPendingWorkTexts(t, m)
	m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{generation: 1, pendingWork: pendingWorkRefreshTestWork("current")})
	assertPendingWorkTexts(t, m, "current")

	if m.requestPendingWorkRefresh(sessionB) != nil || m.requestPendingWorkRefresh(sessionA) == nil {
		t.Fatal("refresh did not filter by hydrated Session")
	}
	if m.requestPendingWorkRefresh(sessionA) != nil || m.requestPendingWorkRefresh(sessionA) != nil {
		t.Fatal("overlapping refresh started another request")
	}
	followUp := m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{
		generation: 1, pendingWork: pendingWorkRefreshTestWork("latest"),
	})
	if followUp == nil {
		t.Fatal("overlapping refresh did not schedule one follow-up")
	}
	m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{generation: 1, err: errors.New("follow-up failed")})
	assertPendingWorkTexts(t, m, "latest")

	m.advancePendingWorkRefreshScope(sessionB)
	m.advancePendingWorkRefreshScope(sessionB)
	assertPendingWorkTexts(t, m)
	m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{generation: 3, err: errors.New("initial fetch failed")})
	assertPendingWorkTexts(t, m)
}

func TestAcceptedHydrationClearsAndRefreshesPendingWork(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.pendingWorkRefresh.collection = pendingWorkRefreshTestWork("prior")
	controller := newOngoingTranscriptController(
		&ongoingSurfaceSpy{}, m.ongoingFrameInput,
		noopOngoingTranscriptRuntimeAdmission, m.applyAdmittedTranscriptMessageState,
	)
	if _, cmd, err := controller.Accept(ongoingHydrationMessage(1)); err != nil || cmd == nil {
		t.Fatalf("accepted hydration = cmd %v, error %v", cmd, err)
	}
	assertPendingWorkTexts(t, m)
}

func TestPendingWorkRefreshTriggersUseCapturedSession(t *testing.T) {
	sessionID := ongoingTestSessionID()
	changed := newProjectedStaticUIModel()
	changed.pendingWorkRefresh = pendingWorkRefreshOwner{sessionID: sessionID, generation: 1}
	if cmd := changed.applyAdmittedTranscriptMessageState(
		clientui.NewTranscriptMessage(2, clientui.NewTranscriptEvent(clientui.TranscriptPendingWorkChanged{})),
		runtimeTupleMergeResult{},
	); cmd == nil {
		t.Fatal("Changed did not refresh Pending Work")
	}
	triggers := []func(*uiModel, runtimeids.SessionID){
		func(m *uiModel, id runtimeids.SessionID) {
			m.activeSubmit = activeSubmitState{token: 1}
			m.inputController().handleSubmitDone(submitDoneMsg{token: 1, sessionID: id})
		},
		func(m *uiModel, id runtimeids.SessionID) {
			m.injectedQueue = []injectedRuntimeQueueItem{{LocalID: "local", State: injectedRuntimeQueuePendingCreate, CreateToken: 1}}
			m.inputController().handleInjectedQueueCreateDone(injectedQueueCreateDoneMsg{token: 1, sessionID: id, localID: "local", completed: true})
		},
		func(m *uiModel, id runtimeids.SessionID) {
			m.inputController().handleCompactDone(compactDoneMsg{requestID: runtimeids.NewCompactionRequestID(), sessionID: id})
		},
		func(m *uiModel, id runtimeids.SessionID) {
			m.worktrees.switchToken = 1
			m.reduceWorktreeMessage(worktreeSwitchDoneMsg{
				token: 1, sessionID: id,
				transition: runtimeinput.PendingWorkWorktreeTransition{Transition: runtimeinput.PendingWorkWorktreeTransitionLeave},
				ack:        &worktreepb.ScheduledAcknowledgement{OperationId: runtimeids.NewQueueItemID().String()},
			})
		},
		func(m *uiModel, id runtimeids.SessionID) {
			m.injectedQueue = []injectedRuntimeQueueItem{{LocalID: "local", State: injectedRuntimeQueueDiscardPending, DiscardToken: 1}}
			m.inputController().handleInjectedQueueDiscardDone(injectedQueueDiscardDoneMsg{token: 1, sessionID: id, localID: "local", discarded: true})
		},
	}
	for index, trigger := range triggers {
		for _, captured := range []runtimeids.SessionID{sessionID, runtimeids.NewSessionID()} {
			m := newProjectedStaticUIModel()
			m.pendingWorkRefresh = pendingWorkRefreshOwner{sessionID: sessionID, generation: 1}
			trigger(m, captured)
			if got, want := m.pendingWorkRefresh.inFlight, captured == sessionID; got != want {
				t.Fatalf("trigger %d refresh in flight = %t, want %t", index, got, want)
			}
		}
	}
}

func assertPendingWorkTexts(t *testing.T, m *uiModel, want ...string) {
	t.Helper()
	got := m.layout().queuedMessages()
	if len(got) != len(want) {
		t.Fatalf("Pending Work count = %d, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index].Text != want[index] {
			t.Fatalf("Pending Work[%d] = %q, want %q", index, got[index].Text, want[index])
		}
	}
}

func pendingWorkRefreshTestWork(text string) runtimeinput.PendingWork {
	return runtimeinput.PendingWork{Items: []runtimeinput.PendingWorkItem{
		pendingWorkMessageForTest(runtimeinput.PendingWorkLaneSteer, text),
	}}
}
