package app

import (
	"errors"
	"testing"

	"core/shared/clientui"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
)

func TestPendingWorkRefreshHydrationScopesAndCoalescing(t *testing.T) {
	sessionA, sessionB := ongoingTestSessionID(), runtimeids.NewSessionID()
	m := newProjectedStaticUIModel()
	m.pendingWorkRefresh.collection = pendingWorkRefreshTestWork("before hydration")
	if cmd := m.advancePendingWorkRefreshScope(sessionA); cmd == nil {
		t.Fatal("hydration did not fetch")
	}
	assertUnchanged(t, "owner", m.pendingWorkRefresh, pendingWorkRefreshTestOwner(sessionA, 1, true, false, false))
	m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{pendingWork: pendingWorkRefreshTestWork("stale")})
	m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{err: errors.New("stale failure")})
	assertUnchanged(t, "owner", m.pendingWorkRefresh, pendingWorkRefreshTestOwner(sessionA, 1, true, false, false))
	current := pendingWorkRefreshTestWork("current")
	m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{generation: 1, pendingWork: current})
	assertUnchanged(t, "owner", m.pendingWorkRefresh, pendingWorkRefreshTestOwner(sessionA, 1, false, false, true, current))

	if m.requestPendingWorkRefresh(sessionB) != nil {
		t.Fatal("foreign Session refresh started")
	}
	if m.requestPendingWorkRefresh(sessionA) == nil {
		t.Fatal("current-scope refresh did not start")
	}
	if m.requestPendingWorkRefresh(sessionA) != nil || m.requestPendingWorkRefresh(sessionA) != nil {
		t.Fatal("overlapping refresh started another request")
	}
	latest := pendingWorkRefreshTestWork("latest")
	followUp := m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{generation: 1, pendingWork: latest})
	if followUp == nil {
		t.Fatal("overlapping refresh did not schedule one follow-up")
	}
	assertUnchanged(t, "owner", m.pendingWorkRefresh, pendingWorkRefreshTestOwner(sessionA, 1, true, false, true, latest))
	m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{generation: 1, err: errors.New("follow-up failed")})
	assertUnchanged(t, "owner", m.pendingWorkRefresh, pendingWorkRefreshTestOwner(sessionA, 1, false, false, true, latest))

	for generation := uint64(2); generation <= 3; generation++ {
		if cmd := m.advancePendingWorkRefreshScope(sessionB); cmd == nil {
			t.Fatal("replacement hydration did not fetch")
		}
		assertUnchanged(t, "owner", m.pendingWorkRefresh, pendingWorkRefreshTestOwner(sessionB, generation, true, false, false))
	}
	m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{generation: 3, err: errors.New("first fetch failed")})
	assertUnchanged(t, "owner", m.pendingWorkRefresh, pendingWorkRefreshTestOwner(sessionB, 3, false, false, false))
}

func TestAcceptedHydrationAdvancesPendingWorkRefreshScope(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.pendingWorkRefresh.collection = pendingWorkRefreshTestWork("prior")
	controller := newOngoingTranscriptController(
		&ongoingSurfaceSpy{}, m.ongoingFrameInput,
		noopOngoingTranscriptRuntimeAdmission, m.applyAdmittedTranscriptMessageState,
	)
	hydration := ongoingHydrationMessage(1)
	if _, cmd, err := controller.Accept(hydration); err != nil || cmd == nil {
		t.Fatalf("accept initial hydration: cmd=%v err=%v", cmd, err)
	}
	assertUnchanged(t, "owner", m.pendingWorkRefresh, pendingWorkRefreshTestOwner(ongoingTestSessionID(), 1, true, false, false))
}

func TestPendingWorkRefreshTriggersUseCapturedSession(t *testing.T) {
	sessionID := ongoingTestSessionID()
	changed := newProjectedStaticUIModel()
	changed.pendingWorkRefresh = pendingWorkRefreshOwner{sessionID: sessionID, generation: 1}
	if cmd := changed.applyAdmittedTranscriptMessageState(
		clientui.NewTranscriptMessage(2, clientui.NewTranscriptEvent(clientui.TranscriptPendingWorkChanged{})),
		runtimeTupleMergeResult{},
	); cmd == nil || !changed.pendingWorkRefresh.inFlight || changed.pendingWorkRefresh.generation != 1 {
		t.Fatal("Changed did not refresh the hydrated Session")
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

func pendingWorkRefreshTestOwner(
	sessionID runtimeids.SessionID, generation uint64, inFlight, followUp, successful bool,
	collection ...runtimeinput.PendingWork,
) pendingWorkRefreshOwner {
	owner := pendingWorkRefreshOwner{
		sessionID: sessionID, generation: generation, inFlight: inFlight,
		followUpRequired: followUp, successfulFetch: successful,
	}
	if len(collection) != 0 {
		owner.collection = collection[0]
	}
	return owner
}

func pendingWorkRefreshTestWork(text string) runtimeinput.PendingWork {
	return runtimeinput.PendingWork{Items: []runtimeinput.PendingWorkItem{
		pendingWorkMessageForTest(runtimeinput.PendingWorkLaneSteer, text),
	}}
}
