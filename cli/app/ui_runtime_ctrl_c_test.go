package app

import (
	"testing"

	"core/shared/clientui"
	"core/shared/runtimeids"

	tea "github.com/charmbracelet/bubbletea"
)

func TestRuntimeCtrlCInterruptsRestartedAgentLoopInsteadOfQuitting(t *testing.T) {
	client := &runtimeControlFakeClient{}
	model := newProjectedClosedUIModel(client)
	model.setRuntimeActivityBusyForTest(true)

	_, firstInterrupt := model.inputController().handleRuntimeCtrlC(nil)
	_ = collectCmdMessages(t, firstInterrupt)
	if client.interruptCalls != 1 || !model.hasPendingInterrupt() {
		t.Fatalf(
			"first Ctrl+C = interrupt calls %d, pending %t; want one pending interrupt",
			client.interruptCalls,
			model.hasPendingInterrupt(),
		)
	}

	restartedRunID, err := runtimeids.ParseRunID("dddddddd-dddd-4ddd-8ddd-dddddddddddd")
	if err != nil {
		t.Fatalf("parse restarted Run id: %v", err)
	}
	restartedStepID, err := runtimeids.ParseStepID("eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee")
	if err != nil {
		t.Fatalf("parse restarted Step id: %v", err)
	}
	if err := model.applyRuntimeActivityProjection(clientui.RuntimeActivity{
		State: clientui.RuntimeActivityRunning,
		ActiveStep: &clientui.RuntimeActiveStep{
			ActiveKind: clientui.RuntimeActivityActiveKindUserTurn,
			RunID:      restartedRunID,
			StepID:     restartedStepID,
		},
	}); err != nil {
		t.Fatalf("apply restarted runtime activity: %v", err)
	}

	_, secondInterrupt := model.inputController().handleRuntimeCtrlC(nil)
	for _, message := range collectCmdMessages(t, secondInterrupt) {
		if _, quits := message.(tea.QuitMsg); quits {
			t.Fatal("second-run Ctrl+C exited the TUI while the Agent loop was running")
		}
	}
	if client.interruptCalls != 2 {
		t.Fatalf("interrupt calls after second-run Ctrl+C = %d, want 2", client.interruptCalls)
	}
	if model.interruptRunID != restartedRunID.String() || model.interruptStepID != restartedStepID.String() {
		t.Fatalf(
			"pending interrupt target = run %q step %q, want restarted run %q step %q",
			model.interruptRunID,
			model.interruptStepID,
			restartedRunID,
			restartedStepID,
		)
	}
}

func TestRuntimeCtrlCDefersRestartedTurnInterruptUntilExactExecutionIsLive(t *testing.T) {
	client := &runtimeControlFakeClient{}
	model := newProjectedClosedUIModel(client)
	model.setRuntimeActivityBusyForTest(true)

	_, firstInterrupt := model.inputController().handleRuntimeCtrlC(nil)
	_ = collectCmdMessages(t, firstInterrupt)
	if client.interruptCalls != 1 {
		t.Fatalf("first interrupt calls = %d, want 1", client.interruptCalls)
	}
	_ = collectCmdMessages(t, model.acknowledgePendingInterrupt())

	model.activeSubmit = activeSubmitState{token: 2, text: "restart"}
	if err := model.applyRuntimeActivityProjection(clientui.RuntimeActivity{
		State: clientui.RuntimeActivityStarting,
	}); err != nil {
		t.Fatalf("apply restarted starting activity: %v", err)
	}

	_, deferredInterrupt := model.inputController().handleRuntimeCtrlC(nil)
	for _, message := range collectCmdMessages(t, deferredInterrupt) {
		if _, quits := message.(tea.QuitMsg); quits {
			t.Fatal("Ctrl+C exited while the restarted turn was starting")
		}
	}
	if client.interruptCalls != 1 {
		t.Fatalf("starting-phase interrupt calls = %d, want deferred call", client.interruptCalls)
	}
	if !model.hasPendingInterrupt() || !model.interruptPreActive {
		t.Fatalf(
			"starting-phase interrupt state = pending %t pre-active %t, want deferred pending interrupt",
			model.hasPendingInterrupt(),
			model.interruptPreActive,
		)
	}

	restartedRunID, err := runtimeids.ParseRunID("dddddddd-dddd-4ddd-8ddd-dddddddddddd")
	if err != nil {
		t.Fatalf("parse restarted Run id: %v", err)
	}
	restartedStepID, err := runtimeids.ParseStepID("eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee")
	if err != nil {
		t.Fatalf("parse restarted Step id: %v", err)
	}
	running := clientui.RuntimeActivity{
		State: clientui.RuntimeActivityRunning,
		ActiveStep: &clientui.RuntimeActiveStep{
			ActiveKind: clientui.RuntimeActivityActiveKindUserTurn,
			RunID:      restartedRunID,
			StepID:     restartedStepID,
		},
	}
	dispatch := model.applyTranscriptRuntimeReadModelUpdate(runtimeTupleMergeResult{
		project: true,
		view:    clientui.RuntimeMainView{Activity: running},
	})
	_ = collectCmdMessages(t, dispatch)
	if client.interruptCalls != 2 {
		t.Fatalf("interrupt calls after exact execution became live = %d, want 2", client.interruptCalls)
	}
	if model.interruptPreActive {
		t.Fatal("dispatched deferred interrupt remained pre-active")
	}
	if model.interruptRunID != restartedRunID.String() || model.interruptStepID != restartedStepID.String() {
		t.Fatalf(
			"deferred interrupt target = run %q step %q, want run %q step %q",
			model.interruptRunID,
			model.interruptStepID,
			restartedRunID,
			restartedStepID,
		)
	}
}

func TestRuntimeCtrlCForcesExitWhenDeferredInterruptIsStillWaiting(t *testing.T) {
	model := newProjectedClosedUIModel(&runtimeControlFakeClient{})
	model.activeSubmit = activeSubmitState{token: 1, text: "starting turn"}
	if err := model.applyRuntimeActivityProjection(clientui.RuntimeActivity{
		State: clientui.RuntimeActivityStarting,
	}); err != nil {
		t.Fatalf("apply starting activity: %v", err)
	}

	_, first := model.inputController().handleRuntimeCtrlC(nil)
	for _, message := range collectCmdMessages(t, first) {
		if _, quits := message.(tea.QuitMsg); quits {
			t.Fatal("first Ctrl+C exited instead of deferring the interrupt")
		}
	}

	_, second := model.inputController().handleRuntimeCtrlC(nil)
	messages := collectCmdMessages(t, second)
	if len(messages) != 1 {
		t.Fatalf("second Ctrl+C messages = %d, want one Quit message", len(messages))
	}
	if _, quits := messages[0].(tea.QuitMsg); !quits {
		t.Fatalf("second Ctrl+C message = %T, want tea.QuitMsg", messages[0])
	}
	if !model.forcedLocalExit {
		t.Fatal("second Ctrl+C did not mark the exit as forced")
	}
}

func TestRuntimeCtrlCInterruptsAwaitingQuestionInsteadOfQuitting(t *testing.T) {
	client := &runtimeControlFakeClient{}
	model := newProjectedClosedUIModel(client)
	runID, err := runtimeids.ParseRunID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("parse Run id: %v", err)
	}
	stepID, err := runtimeids.ParseStepID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	if err != nil {
		t.Fatalf("parse Step id: %v", err)
	}
	if err := model.applyRuntimeActivityProjection(clientui.RuntimeActivity{
		State: clientui.RuntimeActivityAwaitingPrompt,
		ActiveStep: &clientui.RuntimeActiveStep{
			ActiveKind: clientui.RuntimeActivityActiveKindUserTurn,
			RunID:      runID,
			StepID:     stepID,
		},
	}); err != nil {
		t.Fatalf("apply awaiting-Question activity: %v", err)
	}

	_, command := model.inputController().handleRuntimeCtrlC(nil)
	for _, message := range collectCmdMessages(t, command) {
		if _, quits := message.(tea.QuitMsg); quits {
			t.Fatal("Ctrl+C exited the TUI while an Agent Turn awaited a Question")
		}
	}
	if client.interruptCalls != 1 {
		t.Fatalf("interrupt calls = %d, want 1", client.interruptCalls)
	}
}

func TestRuntimeCtrlCExitsWhenNoAgentLoopIsRunning(t *testing.T) {
	model := newProjectedClosedUIModel(&runtimeControlFakeClient{})

	_, command := model.inputController().handleRuntimeCtrlC(nil)
	messages := collectCmdMessages(t, command)
	if len(messages) != 1 {
		t.Fatalf("idle Ctrl+C messages = %d, want one Quit message", len(messages))
	}
	if _, ok := messages[0].(tea.QuitMsg); !ok {
		t.Fatalf("idle Ctrl+C message = %T, want tea.QuitMsg", messages[0])
	}
}
