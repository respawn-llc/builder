package app

import (
	"errors"
	"testing"

	"core/cli/tui/ongoing"
	"core/shared/clientui"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
)

func TestPendingWorkRefreshHydrationScopesAndCoalescing(t *testing.T) {
	sessionA, sessionB := ongoingTestSessionID(), runtimeids.NewSessionID()
	m := newProjectedStaticUIModel()
	m.pendingWorkRefresh.collection = pendingWorkRefreshTestWork("before hydration")
	for index, sessionID := range []runtimeids.SessionID{sessionA, sessionB, sessionB} {
		oldGeneration := m.pendingWorkRefresh.generation
		if cmd := m.advancePendingWorkRefreshScope(sessionID); cmd == nil || !m.pendingWorkRefresh.inFlight {
			t.Fatalf("hydration %d did not clear and fetch", index)
		}
		assertPendingWorkRefreshTexts(t, m)
		m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{generation: oldGeneration, pendingWork: pendingWorkRefreshTestWork("stale")})
		m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{generation: oldGeneration, err: errors.New("stale failure")})
		assertPendingWorkRefreshTexts(t, m)
		if index < 2 {
			m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{
				generation: m.pendingWorkRefresh.generation, pendingWork: pendingWorkRefreshTestWork("current"),
			})
			assertPendingWorkRefreshTexts(t, m, "current")
		} else {
			m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{
				generation: m.pendingWorkRefresh.generation, err: errors.New("first fetch failed"),
			})
			assertPendingWorkRefreshTexts(t, m)
		}
	}
	generation := m.pendingWorkRefresh.generation
	if m.requestPendingWorkRefresh(sessionB) == nil {
		t.Fatal("current-scope refresh did not start")
	}
	m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{generation: generation, pendingWork: pendingWorkRefreshTestWork("kept")})
	if m.requestPendingWorkRefresh(sessionB) == nil ||
		m.requestPendingWorkRefresh(sessionB) != nil ||
		m.requestPendingWorkRefresh(sessionB) != nil {
		t.Fatal("overlapping triggers did not keep one request in flight")
	}
	followUp := m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{
		generation: generation, pendingWork: pendingWorkRefreshTestWork("latest"),
	})
	if followUp == nil || !m.pendingWorkRefresh.inFlight || m.pendingWorkRefresh.followUpRequired {
		t.Fatal("overlapping triggers did not schedule exactly one follow-up")
	}
	m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{generation: generation, err: errors.New("follow-up failed")})
	assertPendingWorkRefreshTexts(t, m, "latest")
	if m.pendingWorkRefresh.inFlight || m.pendingWorkRefresh.followUpRequired {
		t.Fatal("failed follow-up scheduled another request")
	}
}

func TestPendingWorkRefreshScopeAdvancesOnlyForAcceptedHydration(t *testing.T) {
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
	assertPendingWorkRefreshTexts(t, m)
	generation := m.pendingWorkRefresh.generation
	m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{generation: generation, pendingWork: pendingWorkRefreshTestWork("current")})
	result, cmd, err := controller.Accept(hydration)
	if err != nil || result.Action != ongoing.ResultRequestScratchRehydration || cmd != nil {
		t.Fatalf("reject hydration = %+v/%v/%v", result, cmd, err)
	}
	controller.runtimeAdmission = func(clientui.TranscriptMessage) (runtimeTupleMergeResult, error) {
		return runtimeTupleMergeResult{}, errors.New("stale hydration")
	}
	if _, _, err := controller.Accept(hydration); err == nil {
		t.Fatal("stale hydration was accepted")
	}
	if m.pendingWorkRefresh.generation != generation || m.pendingWorkRefresh.inFlight {
		t.Fatalf("rejected hydration changed refresh state: %+v", m.pendingWorkRefresh)
	}
	assertPendingWorkRefreshTexts(t, m, "current")
	controller.runtimeAdmission = noopOngoingTranscriptRuntimeAdmission
	controller.ResetForScratchHydration()
	if _, cmd, err := controller.Accept(hydration); err != nil || cmd == nil {
		t.Fatalf("accept scratch hydration: cmd=%v err=%v", cmd, err)
	}
	assertPendingWorkRefreshTexts(t, m)
	m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{generation: generation, pendingWork: pendingWorkRefreshTestWork("stale")})
	m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{generation: generation, err: errors.New("stale failure")})
	assertPendingWorkRefreshTexts(t, m)
}

func TestPendingWorkRefreshTriggersUseCapturedSession(t *testing.T) {
	sessionID := ongoingTestSessionID()
	changed := pendingWorkRefreshTestModel(sessionID)
	if cmd := changed.applyAdmittedTranscriptMessageState(
		clientui.NewTranscriptMessage(2, clientui.NewTranscriptEvent(clientui.TranscriptPendingWorkChanged{})),
		runtimeTupleMergeResult{},
	); cmd == nil || !changed.pendingWorkRefresh.inFlight || changed.pendingWorkRefresh.generation != 1 {
		t.Fatal("Changed did not refresh the hydrated Session")
	}
	tests := []struct {
		name    string
		trigger func(*uiModel, runtimeids.SessionID)
	}{
		{"Send or Steer", func(m *uiModel, id runtimeids.SessionID) {
			m.activeSubmit = activeSubmitState{token: 1}
			m.inputController().handleSubmitDone(submitDoneMsg{token: 1, sessionID: id})
		}},
		{"Queue", func(m *uiModel, id runtimeids.SessionID) {
			m.injectedQueue = []injectedRuntimeQueueItem{{LocalID: "local", State: injectedRuntimeQueuePendingCreate, CreateToken: 1}}
			m.inputController().handleInjectedQueueCreateDone(injectedQueueCreateDoneMsg{token: 1, sessionID: id, localID: "local", completed: true})
		}},
		{"manual compaction", func(m *uiModel, id runtimeids.SessionID) {
			m.inputController().handleCompactDone(compactDoneMsg{requestID: runtimeids.NewCompactionRequestID(), sessionID: id})
		}},
		{"active Worktree", func(m *uiModel, id runtimeids.SessionID) {
			m.worktrees.switchToken = 1
			m.reduceWorktreeMessage(worktreeSwitchDoneMsg{
				token: 1, sessionID: id,
				transition: runtimeinput.PendingWorkWorktreeTransition{Transition: runtimeinput.PendingWorkWorktreeTransitionLeave},
				ack:        &worktreepb.ScheduledAcknowledgement{OperationId: runtimeids.NewQueueItemID().String()},
			})
		}},
		{"removal", func(m *uiModel, id runtimeids.SessionID) {
			m.injectedQueue = []injectedRuntimeQueueItem{{LocalID: "local", State: injectedRuntimeQueueDiscardPending, DiscardToken: 1}}
			m.inputController().handleInjectedQueueDiscardDone(injectedQueueDiscardDoneMsg{token: 1, sessionID: id, localID: "local", discarded: true})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, captured := range []runtimeids.SessionID{sessionID, runtimeids.NewSessionID()} {
				m := pendingWorkRefreshTestModel(sessionID)
				test.trigger(m, captured)
				if got, want := m.pendingWorkRefresh.inFlight, captured == sessionID; got != want || m.pendingWorkRefresh.generation != 1 {
					t.Fatalf("refresh in flight = %t, want %t", got, want)
				}
			}
		})
	}
}

func pendingWorkRefreshTestModel(sessionID runtimeids.SessionID) *uiModel {
	m := newProjectedStaticUIModel()
	m.pendingWorkRefresh = pendingWorkRefreshOwner{sessionID: sessionID, generation: 1}
	return m
}

func assertPendingWorkRefreshTexts(t *testing.T, m *uiModel, want ...string) {
	t.Helper()
	entries := m.layout().queuedMessages()
	if len(entries) != len(want) {
		t.Fatalf("Pending Work count = %d, want %d", len(entries), len(want))
	}
	for index := range want {
		if entries[index].Text != want[index] {
			t.Fatalf("Pending Work[%d] = %q, want %q", index, entries[index].Text, want[index])
		}
	}
}

func pendingWorkRefreshTestWork(text string) runtimeinput.PendingWork {
	return runtimeinput.PendingWork{Items: []runtimeinput.PendingWorkItem{
		pendingWorkMessageForTest(runtimeinput.PendingWorkLaneSteer, text),
	}}
}
