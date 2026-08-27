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
		State:    clientui.RuntimeActivityRunning,
		Reviewer: clientui.ReviewerActivityInactive,
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

func TestRuntimeCtrlCExitsWhenInterruptIsPendingForCurrentRun(t *testing.T) {
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

	_, secondInterrupt := model.inputController().handleRuntimeCtrlC(nil)
	messages := collectCmdMessages(t, secondInterrupt)
	if len(messages) != 1 {
		t.Fatalf("second Ctrl+C messages = %d, want one Quit message", len(messages))
	}
	if _, quits := messages[0].(tea.QuitMsg); !quits {
		t.Fatalf("second Ctrl+C message = %T, want tea.QuitMsg", messages[0])
	}
	if !model.forcedLocalExit {
		t.Fatal("second Ctrl+C did not mark the exit as forced")
	}
	if client.interruptCalls != 1 {
		t.Fatalf("second Ctrl+C interrupt calls = %d, want no duplicate", client.interruptCalls)
	}
}

func TestRuntimeCtrlCUsesServerRunningLifecycleWithoutActiveKindPolicy(t *testing.T) {
	for _, kind := range []clientui.RuntimeActivityActiveKind{
		clientui.RuntimeActivityActiveKindUserTurn,
		clientui.RuntimeActivityActiveKindWorkflowTurn,
		clientui.RuntimeActivityActiveKindGoalLoop,
		clientui.RuntimeActivityActiveKindCompaction,
	} {
		t.Run(string(kind), func(t *testing.T) {
			client := &runtimeControlFakeClient{}
			model := newProjectedClosedUIModel(client)
			if err := model.applyRuntimeActivityProjection(clientui.RuntimeActivity{
				State:    clientui.RuntimeActivityRunning,
				Reviewer: clientui.ReviewerActivityInactive,
				ActiveStep: &clientui.RuntimeActiveStep{
					ActiveKind: kind,
					RunID:      ongoingTestRunID(),
					StepID:     ongoingTestStepID(),
				},
			}); err != nil {
				t.Fatalf("apply running %s activity: %v", kind, err)
			}
			if !model.runtimeLifecycle.Run.IsRunning() {
				t.Fatalf("server running lifecycle for %s was not running", kind)
			}

			_, command := model.inputController().handleRuntimeCtrlC(nil)
			for _, message := range collectCmdMessages(t, command) {
				if _, quits := message.(tea.QuitMsg); quits {
					t.Fatalf("Ctrl+C exited during server running lifecycle %s", kind)
				}
			}
			if client.interruptCalls != 1 {
				t.Fatalf("%s interrupt calls = %d, want one", kind, client.interruptCalls)
			}
		})
	}
}

func TestRuntimeCtrlCExitsWheneverServerRuntimeIsNotRunning(t *testing.T) {
	for _, state := range []clientui.RuntimeActivityState{
		clientui.RuntimeActivityRegisteredIdle,
		clientui.RuntimeActivityStarting,
		clientui.RuntimeActivityDraining,
		clientui.RuntimeActivityClosing,
	} {
		t.Run(string(state), func(t *testing.T) {
			model := newProjectedClosedUIModel(&runtimeControlFakeClient{})
			if err := model.applyRuntimeActivityProjection(clientui.RuntimeActivity{
				State:    state,
				Reviewer: clientui.ReviewerActivityInactive,
			}); err != nil {
				t.Fatalf("apply %s activity: %v", state, err)
			}

			_, command := model.inputController().handleRuntimeCtrlC(nil)
			messages := collectCmdMessages(t, command)
			if len(messages) != 1 {
				t.Fatalf("%s Ctrl+C messages = %d, want one Quit message", state, len(messages))
			}
			if _, quits := messages[0].(tea.QuitMsg); !quits {
				t.Fatalf("%s Ctrl+C message = %T, want tea.QuitMsg", state, messages[0])
			}
		})
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
		State:    clientui.RuntimeActivityAwaitingPrompt,
		Reviewer: clientui.ReviewerActivityInactive,
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
