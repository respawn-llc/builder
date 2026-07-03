package app

import (
	"strings"
	"testing"

	"core/shared/clientui"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

func TestRuntimeReadModelStaleActivityEventDoesNotRelockBusyState(t *testing.T) {
	current := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 2, Sequence: 1}
	stale := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 99}
	m := newProjectedStaticUIModel()
	m.runtimeReadModelVersion = current
	_ = m.applyRuntimeActivityProjection(clientui.MustRuntimeActivity(clientui.RuntimeActivityRegisteredIdle, clientui.RuntimeActivityOptions{}))

	running := clientui.MustRuntimeActivity(clientui.RuntimeActivityRunning, clientui.RuntimeActivityOptions{
		ActiveKind: clientui.RuntimeActivityActiveKindGoalLoop,
		RunID:      "run-stale",
		StepID:     "step-stale",
	})
	next, _ := m.Update(runtimeEventMsg{event: clientui.Event{
		Kind:             clientui.EventRuntimeActivityChanged,
		ReadModelVersion: stale,
		RuntimeActivity:  &running,
	}})
	updated := next.(*uiModel)

	if updated.runtimeActivityBusy() {
		t.Fatalf("stale activity relocked runtime activity: %+v", updated.runtimeLifecycle.Run)
	}
	if updated.runtimeReadModelVersion != current {
		t.Fatalf("accepted version changed to %+v, want %+v", updated.runtimeReadModelVersion, current)
	}
}

func TestRuntimeReadModelSameVersionConflictSurfacesDiagnostic(t *testing.T) {
	version := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 2}
	m := newProjectedStaticUIModel()
	m.runtimeReadModelVersion = version
	_ = m.applyRuntimeActivityProjection(clientui.MustRuntimeActivity(clientui.RuntimeActivityRegisteredIdle, clientui.RuntimeActivityOptions{}))
	running := clientui.MustRuntimeActivity(clientui.RuntimeActivityRunning, clientui.RuntimeActivityOptions{
		ActiveKind: clientui.RuntimeActivityActiveKindGoalLoop,
		RunID:      "run-conflict",
		StepID:     "step-conflict",
	})

	next, _ := m.Update(runtimeEventMsg{event: clientui.Event{
		Kind:             clientui.EventRuntimeActivityChanged,
		ReadModelVersion: version,
		RuntimeActivity:  &running,
	}})
	updated := next.(*uiModel)

	if updated.runtimeActivityBusy() {
		t.Fatalf("same-version conflicting activity was applied: %+v", updated.runtimeLifecycle.Run)
	}
	if updated.transientStatus == "" {
		t.Fatal("expected same-version conflict diagnostic status")
	}
}

func TestRuntimeReadModelSameVersionStateConflictSurfacesDiagnostic(t *testing.T) {
	version := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 3}
	m := newProjectedStaticUIModel()
	m.runtimeReadModelVersion = version
	_ = m.applyRuntimeActivityProjection(clientui.MustRuntimeActivity(clientui.RuntimeActivityRunning, clientui.RuntimeActivityOptions{
		ActiveKind: clientui.RuntimeActivityActiveKindUserTurn,
		RunID:      "run-1",
		StepID:     "step-1",
	}))
	awaiting := clientui.MustRuntimeActivity(clientui.RuntimeActivityAwaitingPrompt, clientui.RuntimeActivityOptions{
		ActiveKind: clientui.RuntimeActivityActiveKindUserTurn,
		RunID:      "run-1",
		StepID:     "step-1",
	})

	next, _ := m.Update(runtimeEventMsg{event: clientui.Event{
		Kind:             clientui.EventRuntimeActivityChanged,
		ReadModelVersion: version,
		RuntimeActivity:  &awaiting,
	}})
	updated := next.(*uiModel)

	if updated.activity == uiActivityQuestion {
		t.Fatal("same-version state conflict was applied")
	}
	if updated.transientStatus == "" {
		t.Fatal("expected same-version state conflict diagnostic status")
	}
}

func TestRuntimeReadModelGoalStatusEventWithoutVersionUpdatesCachedGoal(t *testing.T) {
	current := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 2, Sequence: 1}
	runtimeClient := newTestSessionRuntimeClientWithControls(&reconnectRetryRuntimeControlClient{})
	runtimeClient.storeMainView(clientui.RuntimeMainView{
		Version: current,
		Status:  clientui.RuntimeStatus{},
	})
	m := newProjectedTestUIModel(runtimeClient, closedProjectedRuntimeEvents(), closedAskEvents())
	m.runtimeReadModelVersion = current

	next, _ := m.Update(runtimeEventMsg{event: clientui.Event{
		Kind: clientui.EventGoalStatusUpdated,
		GoalStatus: &clientui.RuntimeGoalStatusUpdate{
			ID:        "goal-unstamped",
			Objective: "raw goal",
			Status:    clientui.RuntimeGoalStatusActive,
		},
	}})
	_ = next.(*uiModel)

	view, ok := runtimeClient.CachedMainView()
	if !ok {
		t.Fatal("expected cached main view")
	}
	if view.Status.Goal == nil || view.Status.Goal.ID != "goal-unstamped" {
		t.Fatalf("unstamped goal status did not update cached goal: %+v", view.Status.Goal)
	}
	updated := next.(*uiModel)
	if updated.isGoalRun() {
		t.Fatalf("goal status update must not synthesize runtime activity")
	}
}

func TestNonRunRuntimeActivityBlocksInputWithoutRunLifecycle(t *testing.T) {
	m := newProjectedTestUIModel(&runtimeControlFakeClient{}, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil

	if err := m.applyRuntimeActivityProjection(clientui.MustRuntimeActivity(clientui.RuntimeActivityStarting, clientui.RuntimeActivityOptions{})); err != nil {
		t.Fatalf("apply starting activity: %v", err)
	}

	if m.runtimeActivityBusy() {
		t.Fatal("starting runtime activity must not create a fake running run lifecycle")
	}
	if !m.runtimeActivityBlocksControl() || !m.blocksRuntimeInput() {
		t.Fatal("starting runtime activity should still block runtime control/input")
	}
	if m.statusLineSpinning() {
		t.Fatal("starting runtime activity without an active step must not run the model/goal spinner")
	}
}

func TestRuntimeReadModelMainViewHydrationAcceptsNewGenerationReset(t *testing.T) {
	current := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 9}
	nextVersion := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 2, Sequence: 1}
	m := newProjectedStaticUIModel()
	m.runtimeReadModelVersion = current
	_ = m.applyRuntimeActivityProjection(clientui.MustRuntimeActivity(clientui.RuntimeActivityRunning, clientui.RuntimeActivityOptions{
		ActiveKind: clientui.RuntimeActivityActiveKindGoalLoop,
		RunID:      "run-1",
		StepID:     "step-1",
	}))

	m.applyRuntimeMainViewState(clientui.RuntimeMainView{
		Version:  nextVersion,
		Activity: clientui.MustRuntimeActivity(clientui.RuntimeActivityRegisteredIdle, clientui.RuntimeActivityOptions{QueueAccepting: true}),
	})

	if m.runtimeActivityBusy() {
		t.Fatalf("new generation hydration did not reset runtime activity: %+v", m.runtimeLifecycle.Run)
	}
	if m.runtimeReadModelVersion != nextVersion {
		t.Fatalf("accepted version = %+v, want %+v", m.runtimeReadModelVersion, nextVersion)
	}
}
func TestRuntimeReadModelInvalidActivityEventFailsClosedWithHydration(t *testing.T) {
	activity := clientui.MustRuntimeActivity(clientui.RuntimeActivityRunning, clientui.RuntimeActivityOptions{
		ActiveKind: clientui.RuntimeActivityActiveKindUserTurn,
		RunID:      "run-invalid",
		StepID:     "step-invalid",
	})
	client := &runtimeControlFakeClient{mainView: clientui.RuntimeMainView{
		Version:  clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 1},
		Activity: clientui.MustRuntimeActivity(clientui.RuntimeActivityRegisteredIdle, clientui.RuntimeActivityOptions{}),
	}}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())

	next, cmd := m.Update(runtimeEventMsg{event: clientui.Event{
		Kind:            clientui.EventRuntimeActivityChanged,
		RuntimeActivity: &activity,
	}})
	updated := next.(*uiModel)
	if updated.runtimeActivityBusy() {
		t.Fatal("invalid read-model activity event was applied")
	}
	if cmd == nil {
		t.Fatal("expected invalid read-model event to request hydration")
	}
}

func TestRuntimeReadModelInterruptResponseNewGenerationRequestsHydrationBeforeCleanup(t *testing.T) {
	current := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 9}
	nextVersion := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 2, Sequence: 1}
	idle := clientui.MustRuntimeActivity(clientui.RuntimeActivityRegisteredIdle, clientui.RuntimeActivityOptions{QueueAccepting: true})
	controls := &reconnectRetryRuntimeControlClient{
		interruptResp: serverapi.RuntimeInterruptResponse{
			Version:             nextVersion,
			Activity:            idle,
			InputReconciliation: clientui.NewEmptyRuntimeInputReconciliationSnapshot(nextVersion),
		},
	}
	runtimeClient := newUIRuntimeClientWithReads("session-1", &countingSessionViewClient{view: clientui.RuntimeMainView{
		Version:  nextVersion,
		Activity: idle,
		Session:  clientui.RuntimeSessionView{SessionID: "session-1"},
	}}, controls).(*sessionRuntimeClient)
	runtimeClient.storeMainView(clientui.RuntimeMainView{
		Version: current,
		Session: clientui.RuntimeSessionView{SessionID: "session-1"},
		Activity: clientui.MustRuntimeActivity(clientui.RuntimeActivityRunning, clientui.RuntimeActivityOptions{
			ActiveKind: clientui.RuntimeActivityActiveKindGoalLoop,
			RunID:      "run-1",
			StepID:     "step-1",
		}),
		InputReconciliation: clientui.NewEmptyRuntimeInputReconciliationSnapshot(current),
	})
	m := newProjectedTestUIModel(runtimeClient, closedProjectedRuntimeEvents(), closedAskEvents())
	m.runtimeReadModelVersion = current
	_ = m.applyRuntimeActivityProjection(runtimeClient.MainView().Activity)
	m.setPendingInterrupt(true)

	cmd := m.runtimeControlCommand(runtimeControlInterrupt, "", false, "")
	if cmd == nil {
		t.Fatal("expected interrupt command")
	}
	msg, ok := cmd().(runtimeControlDoneMsg)
	if !ok {
		t.Fatalf("interrupt command returned %T", cmd())
	}
	next, hydrationCmd := m.Update(msg)
	updated := next.(*uiModel)
	if !updated.hasPendingInterrupt() {
		t.Fatal("interrupt response from new generation cleared pending interrupt before hydration")
	}
	if updated.runtimeReadModelVersion != current {
		t.Fatalf("accepted version = %+v, want still %+v before hydration", updated.runtimeReadModelVersion, current)
	}
	if hydrationCmd == nil {
		t.Fatal("expected interrupt response from new generation to request hydration")
	}
	hydrationMsg, ok := hydrationCmd().(runtimeMainViewRefreshedMsg)
	if !ok {
		t.Fatalf("hydration command returned %T", hydrationCmd())
	}
	next, _ = updated.Update(hydrationMsg)
	updated = next.(*uiModel)
	if updated.runtimeReadModelVersion != nextVersion {
		t.Fatalf("hydrated version = %+v, want %+v", updated.runtimeReadModelVersion, nextVersion)
	}
	if updated.hasPendingInterrupt() {
		t.Fatal("pending interrupt was not cleared after hydration")
	}
}

func TestRuntimeStatusLineUsesRuntimeActivityForSpinnerStates(t *testing.T) {
	tests := []struct {
		name     string
		activity clientui.RuntimeActivity
		goal     *clientui.RuntimeGoal
		spin     bool
		label    string
	}{
		{
			name: "running turn spins",
			activity: clientui.MustRuntimeActivity(clientui.RuntimeActivityRunning, clientui.RuntimeActivityOptions{
				ActiveKind: clientui.RuntimeActivityActiveKindUserTurn,
				RunID:      "run-1",
				StepID:     "step-1",
			}),
			spin: true,
		},
		{
			name: "running goal spins",
			activity: clientui.MustRuntimeActivity(clientui.RuntimeActivityRunning, clientui.RuntimeActivityOptions{
				ActiveKind: clientui.RuntimeActivityActiveKindGoalLoop,
				RunID:      "run-1",
				StepID:     "step-1",
			}),
			goal:  &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship", Status: clientui.RuntimeGoalStatusActive},
			spin:  true,
			label: "goal",
		},
		{
			name: "awaiting prompt suppresses runtime spinner",
			activity: clientui.MustRuntimeActivity(clientui.RuntimeActivityAwaitingPrompt, clientui.RuntimeActivityOptions{
				ActiveKind: clientui.RuntimeActivityActiveKindUserTurn,
				RunID:      "run-1",
				StepID:     "step-1",
			}),
			spin: false,
		},
		{
			name:     "unavailable stays idle",
			activity: clientui.MustRuntimeActivity(clientui.RuntimeActivityUnavailable, clientui.RuntimeActivityOptions{}),
			spin:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newSizedProjectedClosedUIModel(&runtimeControlFakeClient{status: clientui.RuntimeStatus{Goal: tt.goal}}, 100, 20)
			m.applyRuntimeMainViewState(clientui.RuntimeMainView{
				Version:  clientui.ReadModelVersion{Epoch: "epoch-" + strings.ReplaceAll(tt.name, " ", "-"), Generation: 1, Sequence: 1},
				Status:   clientui.RuntimeStatus{Goal: tt.goal},
				Activity: tt.activity,
			})
			if got := m.statusLineSpinning(); got != tt.spin {
				t.Fatalf("spinning = %t, want %t", got, tt.spin)
			}
			if tt.label != "" && m.statusLineLabel() != tt.label {
				t.Fatalf("label = %q, want %q", m.statusLineLabel(), tt.label)
			}
			rendered := stripANSIAndTrimRight(uiViewLayout{model: m}.renderStatusLine(100, uiThemeStyles(m.theme)))
			hasSpinner := !strings.HasPrefix(rendered, statusStateCircleGlyph+" ")
			if hasSpinner != tt.spin {
				t.Fatalf("rendered spinning = %t, want %t, status=%q", hasSpinner, tt.spin, rendered)
			}
		})
	}
}

func TestLocalDispatchPendingRoutesCtrlCBeforeServerActivity(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.activeSubmit = activeSubmitState{token: 1, text: "say hi", restoreOnInterrupt: true}
	_ = m.applyRuntimeActivityProjection(clientui.MustRuntimeActivity(clientui.RuntimeActivityRegisteredIdle, clientui.RuntimeActivityOptions{}))

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	updated := next.(*uiModel)
	if updated.exitAction == UIActionExit || !updated.hasPendingInterrupt() {
		t.Fatalf("ctrl+c did not route through interrupt path, exit=%q pending=%t", updated.exitAction, updated.hasPendingInterrupt())
	}
	if cmd == nil {
		t.Fatal("expected interrupt command")
	}
	_ = collectCmdMessages(t, cmd)
	if client.interruptCalls != 1 {
		t.Fatalf("interrupt calls = %d, want 1", client.interruptCalls)
	}
}
