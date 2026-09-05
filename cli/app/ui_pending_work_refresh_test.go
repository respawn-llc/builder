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
	controller := newOngoingTranscriptController(
		&ongoingSurfaceSpy{}, m.ongoingFrameInput,
		noopOngoingTranscriptRuntimeAdmission, m.applyAdmittedTranscriptMessageState,
	)
	if _, cmd, err := controller.Accept(ongoingHydrationMessage(1)); err != nil || cmd == nil {
		t.Fatalf("accepted hydration = cmd %v, error %v", cmd, err)
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

func TestPendingWorkRefreshTriggersUseCapturedSession(t *testing.T) {
	sessionID := ongoingTestSessionID()
	triggers := []struct {
		name            string
		capturesSession bool
		apply           func(*uiModel, runtimeids.SessionID)
	}{
		{"Changed", false, func(m *uiModel, _ runtimeids.SessionID) {
			m.applyAdmittedTranscriptMessageState(
				clientui.NewTranscriptMessage(2, clientui.NewTranscriptEvent(clientui.TranscriptPendingWorkChanged{})),
				runtimeTupleMergeResult{},
			)
		}},
		{"Send/Steer", true, func(m *uiModel, id runtimeids.SessionID) {
			m.activeSubmit = activeSubmitState{token: 1}
			m.inputController().handleSubmitDone(submitDoneMsg{token: 1, sessionID: id})
		}},
		{"Queue", true, func(m *uiModel, id runtimeids.SessionID) {
			m.injectedQueue = []injectedRuntimeQueueItem{{LocalID: "local", State: injectedRuntimeQueuePendingCreate, CreateToken: 1}}
			m.inputController().handleInjectedQueueCreateDone(injectedQueueCreateDoneMsg{token: 1, sessionID: id, localID: "local", completed: true})
		}},
		{"compact", true, func(m *uiModel, id runtimeids.SessionID) {
			m.inputController().handleCompactDone(compactDoneMsg{requestID: runtimeids.NewCompactionRequestID(), sessionID: id})
		}},
		{"active Worktree", true, func(m *uiModel, id runtimeids.SessionID) {
			m.worktrees.switchToken = 1
			m.reduceWorktreeMessage(worktreeSwitchDoneMsg{
				token: 1, sessionID: id,
				transition: runtimeinput.PendingWorkWorktreeTransition{Transition: runtimeinput.PendingWorkWorktreeTransitionLeave},
				ack:        &worktreepb.ScheduledAcknowledgement{OperationId: runtimeids.NewQueueItemID().String()},
			})
		}},
		{"remove", true, func(m *uiModel, id runtimeids.SessionID) {
			m.injectedQueue = []injectedRuntimeQueueItem{{LocalID: "local", State: injectedRuntimeQueueDiscardPending, DiscardToken: 1}}
			m.inputController().handleInjectedQueueDiscardDone(injectedQueueDiscardDoneMsg{token: 1, sessionID: id, localID: "local", discarded: true})
		}},
	}
	for _, trigger := range triggers {
		for _, captured := range []runtimeids.SessionID{sessionID, runtimeids.NewSessionID()} {
			m := newProjectedStaticUIModel()
			m.pendingWorkRefresh = pendingWorkRefreshOwner{sessionID: sessionID, generation: 1}
			trigger.apply(m, captured)
			want := !trigger.capturesSession || captured == sessionID
			if got := m.pendingWorkRefresh.inFlight; got != want {
				t.Fatalf("%s refresh in flight = %t, want %t", trigger.name, got, want)
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
