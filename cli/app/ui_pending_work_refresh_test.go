package app

import (
	"context"
	"errors"
	"testing"

	"core/cli/tui/ongoing"
	"core/shared/apicontract"
	"core/shared/clientui"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
)

func TestPendingWorkRefreshHydrationScopeAndCoalescing(t *testing.T) {
	sessionID := pendingWorkRefreshTestSessionID(t, "session")
	work := pendingWorkRefreshTestWork("current")
	m := newProjectedStaticUIModel()

	if m.advancePendingWorkRefreshScope(sessionID) == nil {
		t.Fatal("initial hydration did not fetch Pending Work")
	}
	firstGeneration := m.pendingWorkRefresh.generation
	m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{
		generation:  firstGeneration,
		pendingWork: work,
	})
	assertPendingWorkRefreshTexts(t, m, "current")

	if m.advancePendingWorkRefreshScope(sessionID) == nil {
		t.Fatal("same-Session replacement hydration did not fetch Pending Work")
	}
	replacementGeneration := m.pendingWorkRefresh.generation
	assertPendingWorkRefreshTexts(t, m)
	m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{
		generation:  firstGeneration,
		pendingWork: pendingWorkRefreshTestWork("retired"),
	})
	m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{
		generation: replacementGeneration,
		err:        errors.New("replacement unavailable"),
	})
	assertPendingWorkRefreshTexts(t, m)

	if m.requestPendingWorkRefresh(sessionID) == nil {
		t.Fatal("current-scope trigger did not fetch Pending Work")
	}
	if m.requestPendingWorkRefresh(sessionID) != nil || m.requestPendingWorkRefresh(sessionID) != nil {
		t.Fatal("overlapping triggers started another request")
	}
	followUp := m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{
		generation:  replacementGeneration,
		pendingWork: work,
	})
	if followUp == nil || !m.pendingWorkRefresh.inFlight {
		t.Fatal("overlapping triggers did not coalesce into one follow-up")
	}
	assertPendingWorkRefreshTexts(t, m, "current")
	m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{
		generation: replacementGeneration,
		err:        errors.New("follow-up failed"),
	})
	assertPendingWorkRefreshTexts(t, m, "current")
	if m.pendingWorkRefresh.inFlight || m.pendingWorkRefresh.followUpRequired {
		t.Fatal("failed follow-up scheduled another retry")
	}
	if m.requestPendingWorkRefresh(pendingWorkRefreshTestSessionID(t, "other")) != nil {
		t.Fatal("another Session triggered a refresh")
	}
}

func TestPendingWorkRefreshAdvancesOnlyForAcceptedHydrationAndListsExactSession(t *testing.T) {
	sessionID := ongoingTestSessionID()
	m := newProjectedStaticUIModel()
	controller := newOngoingTranscriptController(
		&ongoingSurfaceSpy{},
		m.ongoingFrameInput,
		noopOngoingTranscriptRuntimeAdmission,
		m.applyAdmittedTranscriptMessageState,
	)
	hydration := ongoingHydrationMessage(1)

	_, cmd, err := controller.Accept(hydration)
	if err != nil || cmd == nil {
		t.Fatalf("accept initial hydration: cmd=%v err=%v", cmd, err)
	}
	generation := m.pendingWorkRefresh.generation
	result, rejectedCmd, err := controller.Accept(hydration)
	if err != nil || result.Action != ongoing.ResultRequestScratchRehydration || rejectedCmd != nil {
		t.Fatalf("duplicate hydration = %+v/%v/%v", result, rejectedCmd, err)
	}
	if m.pendingWorkRefresh.generation != generation {
		t.Fatal("rejected hydration advanced Pending Work scope")
	}

	controller.ResetForScratchHydration()
	if _, scratchCmd, err := controller.Accept(hydration); err != nil || scratchCmd == nil {
		t.Fatalf("accept Scratch Rehydration: cmd=%v err=%v", scratchCmd, err)
	}
	if m.pendingWorkRefresh.generation == generation {
		t.Fatal("accepted Scratch Rehydration did not advance Pending Work scope")
	}
	assertPendingWorkRefreshTexts(t, m)

	service := &pendingWorkRouteFake{
		response: serverapi.RuntimeListPendingWorkResponse{
			PendingWork: pendingWorkRefreshTestWork("listed"),
		},
	}
	routeModel := newProjectedTestUIModel(&sessionRuntimeClient{
		sessionID: sessionID.String(),
		controls:  service,
	})
	listCmd := routeModel.advancePendingWorkRefreshScope(sessionID)
	done, ok := listCmd().(pendingWorkRefreshDoneMsg)
	if !ok || done.err != nil {
		t.Fatalf("list completion = %+v", done)
	}
	routeModel.applyPendingWorkRefreshDone(done)
	if service.request.SessionID != sessionID.String() {
		t.Fatalf("list Session = %q, want %q", service.request.SessionID, sessionID)
	}
	assertPendingWorkRefreshTexts(t, routeModel, "listed")
}

func TestPendingWorkRefreshTriggers(t *testing.T) {
	sessionID := ongoingTestSessionID()
	tests := []struct {
		name    string
		trigger func(*uiModel)
	}{
		{
			name: "Changed",
			trigger: func(m *uiModel) {
				m.applyAdmittedTranscriptMessageState(
					clientui.NewTranscriptMessage(
						2,
						clientui.NewTranscriptEvent(clientui.TranscriptPendingWorkChanged{}),
					),
					runtimeTupleMergeResult{},
				)
			},
		},
		{
			name: "Send or Steer",
			trigger: func(m *uiModel) {
				m.activeSubmit = activeSubmitState{token: 1}
				m.inputController().handleSubmitDone(submitDoneMsg{token: 1, sessionID: sessionID})
			},
		},
		{
			name: "Queue",
			trigger: func(m *uiModel) {
				m.inputController().handleInjectedQueueCreateDone(injectedQueueCreateDoneMsg{
					token:     1,
					sessionID: sessionID,
					localID:   "already-reconciled",
					completed: true,
				})
			},
		},
		{
			name: "manual compaction",
			trigger: func(m *uiModel) {
				m.inputController().handleCompactDone(compactDoneMsg{
					requestID: runtimeids.NewCompactionRequestID(),
					sessionID: sessionID,
				})
			},
		},
		{
			name: "active Worktree",
			trigger: func(m *uiModel) {
				m.worktrees.switchToken = 1
				m.reduceWorktreeMessage(worktreeSwitchDoneMsg{
					token:      1,
					sessionID:  sessionID,
					transition: runtimeinput.PendingWorkWorktreeTransition{Transition: runtimeinput.PendingWorkWorktreeTransitionLeave},
					ack:        &worktreepb.ScheduledAcknowledgement{OperationId: runtimeids.NewQueueItemID().String()},
				})
			},
		},
		{
			name: "discard",
			trigger: func(m *uiModel) {
				m.inputController().handleInjectedQueueDiscardDone(injectedQueueDiscardDoneMsg{
					token:     1,
					sessionID: sessionID,
					localID:   "already-reconciled",
					discarded: true,
				})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := newProjectedStaticUIModel()
			m.pendingWorkRefresh = pendingWorkRefreshOwner{
				sessionID:       sessionID,
				generation:      1,
				successfulFetch: true,
			}
			test.trigger(m)
			if !m.pendingWorkRefresh.inFlight {
				t.Fatal("successful mutation did not fetch Pending Work")
			}
		})
	}
}

type pendingWorkRouteFake struct {
	apicontract.RuntimeControlService
	request  serverapi.RuntimeListPendingWorkRequest
	response serverapi.RuntimeListPendingWorkResponse
}

func (f *pendingWorkRouteFake) ListPendingWork(
	_ context.Context,
	request serverapi.RuntimeListPendingWorkRequest,
) (serverapi.RuntimeListPendingWorkResponse, error) {
	f.request = request
	return f.response, nil
}

func (f *pendingWorkRouteFake) RemovePendingWork(
	context.Context,
	serverapi.RuntimeRemovePendingWorkRequest,
) (serverapi.RuntimeRemovePendingWorkResponse, error) {
	panic("unexpected Pending Work removal")
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

func pendingWorkRefreshTestSessionID(t *testing.T, raw string) runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("parse Session ID %q: %v", raw, err)
	}
	return id
}
