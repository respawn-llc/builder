package app

import (
	"context"
	"errors"
	"slices"
	"testing"

	"core/cli/tui/ongoing"
	"core/shared/apicontract"
	"core/shared/clientui"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
)

func TestPendingWorkRefreshHydrationScopesCollectionAndCompletions(t *testing.T) {
	sessionA := pendingWorkRefreshTestSessionID(t, "session-a")
	sessionB := pendingWorkRefreshTestSessionID(t, "session-b")
	workA := runtimeinput.PendingWork{Items: []runtimeinput.PendingWorkItem{
		pendingWorkMessageForTest(runtimeinput.PendingWorkLaneSteer, "A"),
	}}
	workB := runtimeinput.PendingWork{Items: []runtimeinput.PendingWorkItem{
		pendingWorkMessageForTest(runtimeinput.PendingWorkLaneQueue, "B"),
	}}

	m := newProjectedStaticUIModel()
	first := m.advancePendingWorkRefreshScope(sessionA)
	if first == nil {
		t.Fatal("initial hydration did not start a Pending Work fetch")
	}
	generationA := m.pendingWorkRefresh.generation

	second := m.advancePendingWorkRefreshScope(sessionB)
	if second == nil {
		t.Fatal("replacement hydration did not start a Pending Work fetch")
	}
	if m.pendingWorkRefresh.generation == generationA {
		t.Fatal("replacement hydration did not advance the refresh generation")
	}
	assertPendingWorkRefreshTexts(t, m)

	generationB := m.pendingWorkRefresh.generation
	m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{
		generation:  generationB,
		pendingWork: runtimeinput.PendingWork{},
		err:         errors.New("B fetch failed"),
	})
	m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{
		generation:  generationA,
		pendingWork: workA,
	})
	assertPendingWorkRefreshTexts(t, m)

	m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{
		generation:  generationB,
		pendingWork: workB,
	})
	assertPendingWorkRefreshTexts(t, m, "B")
}

func TestPendingWorkRefreshCoalescesCurrentScopeTriggersAndRetainsOnlyCurrentSuccess(t *testing.T) {
	sessionID := pendingWorkRefreshTestSessionID(t, "session-current")
	first := runtimeinput.PendingWork{Items: []runtimeinput.PendingWorkItem{
		pendingWorkMessageForTest(runtimeinput.PendingWorkLaneSteer, "first"),
	}}

	m := newProjectedStaticUIModel()
	if cmd := m.advancePendingWorkRefreshScope(sessionID); cmd == nil {
		t.Fatal("hydration did not start the initial fetch")
	}
	generation := m.pendingWorkRefresh.generation

	if cmd := m.requestPendingWorkRefresh(sessionID); cmd != nil {
		t.Fatal("overlapping trigger started a second in-flight request")
	}
	if cmd := m.requestPendingWorkRefresh(sessionID); cmd != nil {
		t.Fatal("repeated overlapping trigger started more than one follow-up")
	}
	followUp := m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{
		generation:  generation,
		pendingWork: first,
	})
	if followUp == nil {
		t.Fatal("completion did not start the required follow-up")
	}
	assertPendingWorkRefreshTexts(t, m, "first")

	_ = m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{
		generation: generation,
		err:        errors.New("follow-up failed"),
	})
	if m.pendingWorkRefresh.inFlight || m.pendingWorkRefresh.followUpRequired {
		t.Fatal("failed follow-up scheduled a timer retry")
	}
	assertPendingWorkRefreshTexts(t, m, "first")

	if cmd := m.requestPendingWorkRefresh(pendingWorkRefreshTestSessionID(t, "other-session")); cmd != nil {
		t.Fatal("mismatched-session trigger started a fetch")
	}
}

func TestPendingWorkRefreshSameSessionHydrationNeverCarriesRetiredRuntimeRows(t *testing.T) {
	sessionID := pendingWorkRefreshTestSessionID(t, "session-same")
	oldWork := runtimeinput.PendingWork{Items: []runtimeinput.PendingWorkItem{
		pendingWorkMessageForTest(runtimeinput.PendingWorkLaneSteer, "retired Runtime"),
	}}

	m := newProjectedStaticUIModel()
	_ = m.advancePendingWorkRefreshScope(sessionID)
	m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{
		generation:  m.pendingWorkRefresh.generation,
		pendingWork: oldWork,
	})
	assertPendingWorkRefreshTexts(t, m, "retired Runtime")

	_ = m.advancePendingWorkRefreshScope(sessionID)
	replacementGeneration := m.pendingWorkRefresh.generation
	assertPendingWorkRefreshTexts(t, m)
	m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{
		generation: replacementGeneration,
		err:        errors.New("replacement Runtime unavailable"),
	})
	assertPendingWorkRefreshTexts(t, m)
}

func TestPendingWorkRefreshScopeAdvancesOnlyForAcceptedHydration(t *testing.T) {
	m := newProjectedStaticUIModel()
	controller := newOngoingTranscriptController(
		&ongoingSurfaceSpy{},
		m.ongoingFrameInput,
		noopOngoingTranscriptRuntimeAdmission,
		m.applyAdmittedTranscriptMessageState,
	)
	hydration := ongoingHydrationMessage(1)
	if _, cmd, err := controller.Accept(hydration); err != nil {
		t.Fatalf("accept initial hydration: %v", err)
	} else if cmd == nil {
		t.Fatal("accepted hydration did not start a Pending Work fetch")
	}
	generation := m.pendingWorkRefresh.generation
	m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{
		generation: generation,
		pendingWork: runtimeinput.PendingWork{Items: []runtimeinput.PendingWorkItem{
			pendingWorkMessageForTest(runtimeinput.PendingWorkLaneSteer, "current"),
		}},
	})

	if result, cmd, err := controller.Accept(hydration); err != nil {
		t.Fatalf("reject duplicate hydration: %v", err)
	} else if result.Action != ongoing.ResultRequestScratchRehydration || cmd != nil {
		t.Fatalf("duplicate hydration result=%+v cmd=%v, want scratch rehydration without state command", result, cmd)
	}
	if m.pendingWorkRefresh.generation != generation {
		t.Fatal("rejected hydration advanced the Pending Work generation")
	}
	assertPendingWorkRefreshTexts(t, m, "current")

	controller.ResetForScratchHydration()
	if _, cmd, err := controller.Accept(hydration); err != nil {
		t.Fatalf("accept scratch rehydration: %v", err)
	} else if cmd == nil {
		t.Fatal("accepted scratch rehydration did not start a Pending Work fetch")
	}
	if m.pendingWorkRefresh.generation == generation {
		t.Fatal("accepted scratch rehydration did not advance the generation")
	}
	assertPendingWorkRefreshTexts(t, m)
	m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{
		generation: m.pendingWorkRefresh.generation,
		pendingWork: runtimeinput.PendingWork{Items: []runtimeinput.PendingWorkItem{
			pendingWorkMessageForTest(runtimeinput.PendingWorkLaneSteer, "refetched"),
		}},
	})
	assertPendingWorkRefreshTexts(t, m, "refetched")
}

func TestPendingWorkChangedRequestsCurrentHydrationRefresh(t *testing.T) {
	sessionID := ongoingTestSessionID()
	m := newProjectedStaticUIModel()
	_ = m.advancePendingWorkRefreshScope(sessionID)
	generation := m.pendingWorkRefresh.generation
	m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{
		generation: generation,
		pendingWork: runtimeinput.PendingWork{Items: []runtimeinput.PendingWorkItem{
			pendingWorkMessageForTest(runtimeinput.PendingWorkLaneSteer, "before"),
		}},
	})

	cmd := m.applyAdmittedTranscriptMessageState(
		clientui.NewTranscriptMessage(2, clientui.NewTranscriptEvent(clientui.TranscriptPendingWorkChanged{})),
		runtimeTupleMergeResult{},
	)
	if cmd == nil || !m.pendingWorkRefresh.inFlight {
		t.Fatal("Pending Work Changed did not start a current-scope fetch")
	}
	assertPendingWorkRefreshTexts(t, m, "before")
	m.applyPendingWorkRefreshDone(pendingWorkRefreshDoneMsg{
		generation: m.pendingWorkRefresh.generation,
		pendingWork: runtimeinput.PendingWork{Items: []runtimeinput.PendingWorkItem{
			pendingWorkMessageForTest(runtimeinput.PendingWorkLaneQueue, "after"),
		}},
	})
	assertPendingWorkRefreshTexts(t, m, "after")
}

func TestSuccessfulPendingWorkMutationsRequestCurrentHydrationRefresh(t *testing.T) {
	sessionID := ongoingTestSessionID()
	tests := []struct {
		name  string
		apply func(*uiModel)
	}{
		{
			name: "Send or Steer",
			apply: func(m *uiModel) {
				m.activeSubmit = activeSubmitState{token: 1}
				_, _ = m.inputController().handleSubmitDone(submitDoneMsg{
					token:     1,
					sessionID: sessionID,
				})
			},
		},
		{
			name: "Queue",
			apply: func(m *uiModel) {
				m.injectedQueue = []injectedRuntimeQueueItem{{
					LocalID:     "local",
					State:       injectedRuntimeQueuePendingCreate,
					CreateToken: 1,
				}}
				_, _ = m.inputController().handleInjectedQueueCreateDone(injectedQueueCreateDoneMsg{
					token:     1,
					sessionID: sessionID,
					localID:   "local",
					item:      clientui.QueuedUserMessage{ID: runtimeids.NewQueueItemID().String()},
				})
			},
		},
		{
			name: "manual compaction",
			apply: func(m *uiModel) {
				_, _ = m.inputController().handleCompactDone(compactDoneMsg{
					requestID: runtimeids.NewCompactionRequestID(),
					sessionID: sessionID,
				})
			},
		},
		{
			name: "active Worktree",
			apply: func(m *uiModel) {
				m.worktrees.switchToken = 1
				_ = m.reduceWorktreeMessage(worktreeSwitchDoneMsg{
					token:      1,
					sessionID:  sessionID,
					transition: runtimeinput.PendingWorkWorktreeTransition{Transition: runtimeinput.PendingWorkWorktreeTransitionLeave},
					ack:        &worktreepb.ScheduledAcknowledgement{OperationId: runtimeids.NewQueueItemID().String()},
				})
			},
		},
		{
			name: "removal",
			apply: func(m *uiModel) {
				m.injectedQueue = []injectedRuntimeQueueItem{{
					LocalID:      "local",
					ServerID:     runtimeids.NewQueueItemID().String(),
					State:        injectedRuntimeQueueDiscardPending,
					DiscardToken: 1,
				}}
				_, _ = m.inputController().handleInjectedQueueDiscardDone(injectedQueueDiscardDoneMsg{
					token:     1,
					sessionID: sessionID,
					localID:   "local",
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
			test.apply(m)
			if !m.pendingWorkRefresh.inFlight {
				t.Fatal("successful mutation did not start a Pending Work refresh")
			}
		})
	}
}

func TestSuccessfulPendingWorkMutationRefreshDoesNotDependOnRetainedLocalRequestState(t *testing.T) {
	sessionID := ongoingTestSessionID()
	m := newProjectedStaticUIModel()
	m.pendingWorkRefresh = pendingWorkRefreshOwner{
		sessionID:       sessionID,
		generation:      1,
		successfulFetch: true,
	}

	_, _ = m.inputController().handleInjectedQueueCreateDone(injectedQueueCreateDoneMsg{
		token:     99,
		sessionID: sessionID,
		localID:   "already-reconciled",
		completed: true,
	})

	if !m.pendingWorkRefresh.inFlight {
		t.Fatal("successful response did not refresh after its local request state was reconciled")
	}
}

func TestPendingWorkRefreshReadsTheHydratedSessionFromTheListRoute(t *testing.T) {
	sessionID := ongoingTestSessionID()
	pending := runtimeinput.PendingWork{Items: []runtimeinput.PendingWorkItem{
		pendingWorkMessageForTest(runtimeinput.PendingWorkLaneSteer, "listed"),
	}}
	service := &pendingWorkRouteFake{response: serverapi.RuntimeListPendingWorkResponse{PendingWork: pending}}
	client := &sessionRuntimeClient{
		sessionID: sessionID.String(),
		controls:  service,
	}
	m := newProjectedTestUIModel(client)

	cmd := m.advancePendingWorkRefreshScope(sessionID)
	if cmd == nil {
		t.Fatal("hydration did not start a Pending Work list request")
	}
	done, ok := cmd().(pendingWorkRefreshDoneMsg)
	if !ok {
		t.Fatalf("list command completion = %T, want pendingWorkRefreshDoneMsg", cmd())
	}
	if done.err != nil {
		t.Fatalf("list Pending Work: %v", done.err)
	}
	if service.request.SessionID != sessionID.String() {
		t.Fatalf("list Session ID = %q, want %q", service.request.SessionID, sessionID.String())
	}
	_ = m.applyPendingWorkRefreshDone(done)
	assertPendingWorkRefreshTexts(t, m, "listed")
}

type pendingWorkRouteFake struct {
	apicontract.RuntimeControlService
	request  serverapi.RuntimeListPendingWorkRequest
	response serverapi.RuntimeListPendingWorkResponse
	err      error
}

func (f *pendingWorkRouteFake) ListPendingWork(
	_ context.Context,
	request serverapi.RuntimeListPendingWorkRequest,
) (serverapi.RuntimeListPendingWorkResponse, error) {
	f.request = request
	return f.response, f.err
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
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Text)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Pending Work texts = %q, want %q", got, want)
	}
}

func pendingWorkRefreshTestSessionID(t *testing.T, raw string) runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("parse Session ID %q: %v", raw, err)
	}
	return id
}
