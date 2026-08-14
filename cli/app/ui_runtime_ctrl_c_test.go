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
