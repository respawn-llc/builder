package app

import (
	"testing"

	"core/cli/app/commands"
	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDefaultRegistryBusyContract(t *testing.T) {
	registry := commands.NewDefaultRegistry()
	want := map[string]commands.ActiveRunPolicy{
		"exit": commands.ActiveRunPolicyAllowed, "login": commands.ActiveRunPolicyRequiresIdle,
		"new": commands.ActiveRunPolicyAllowed, "resume": commands.ActiveRunPolicyAllowed,
		"logout": commands.ActiveRunPolicyRequiresIdle, "compact": commands.ActiveRunPolicyQueueUntilIdle,
		"name": commands.ActiveRunPolicyAllowed, "thinking": commands.ActiveRunPolicyAllowed,
		"fast": commands.ActiveRunPolicyAllowed, "supervisor": commands.ActiveRunPolicyAllowed,
		"autocompaction": commands.ActiveRunPolicyAllowed, "questions": commands.ActiveRunPolicyAllowed,
		"status": commands.ActiveRunPolicyAllowed, "goal": commands.ActiveRunPolicyAllowed,
		"ps": commands.ActiveRunPolicyAllowed, "worktree": commands.ActiveRunPolicyRequiresIdle,
		"copy": commands.ActiveRunPolicyAllowed, "back": commands.ActiveRunPolicyAllowed,
		"review": commands.ActiveRunPolicyAllowed, "init": commands.ActiveRunPolicyAllowed,
	}
	for _, command := range registry.Commands() {
		policy, ok := want[command.Name]
		if !ok || command.ActiveRunPolicy != policy {
			t.Fatalf("command %q policy = %v, want %v", command.Name, command.ActiveRunPolicy, policy)
		}
		delete(want, command.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing built-in commands: %+v", want)
	}
}

func TestBusyEnterAppliesImmediateSettings(t *testing.T) {
	tests := []struct {
		input         string
		setup         func(*uiModel)
		sessionName   string
		thinkingLevel string
		fast          bool
	}{
		{input: "/name queued title", sessionName: "queued title"},
		{input: "/thinking low", thinkingLevel: "low"},
		{
			input: "/fast on",
			setup: func(model *uiModel) {
				model.fastModeAvailable = true
			},
			fast: true,
		},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			model := busyCommandTestModel()
			model.input = test.input
			if test.setup != nil {
				test.setup(model)
			}
			next, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			updated := next.(*uiModel)
			if updated.input != "" || updated.sessionName != test.sessionName || updated.thinkingLevel != test.thinkingLevel || updated.fastModeEnabled != test.fast {
				t.Fatalf("updated model = input %q, name %q, thinking %q, fast %t", updated.input, updated.sessionName, updated.thinkingLevel, updated.fastModeEnabled)
			}
			requireBusyCommandQueuesEmpty(t, updated)
		})
	}
}

func TestBusyEnterOpensReadOverlays(t *testing.T) {
	for input, mode := range map[string]uiInputMode{
		"/status": uiInputModeStatus,
		"/goal":   uiInputModeGoal,
		"/ps":     uiInputModeProcessList,
	} {
		t.Run(input, func(t *testing.T) {
			model := busyCommandTestModel()
			model.input = input
			next, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			updated := next.(*uiModel)
			if updated.inputMode() != mode || !updated.isBusy() {
				t.Fatalf("input mode = %v, busy = %t; want %v/true", updated.inputMode(), updated.isBusy(), mode)
			}
			requireBusyCommandQueuesEmpty(t, updated)
		})
	}
}

func TestBusyEnterBlocksIdleOnlyCommands(t *testing.T) {
	for _, input := range []string{"/worktree list"} {
		t.Run(input, func(t *testing.T) {
			model := busyCommandTestModel()
			model.input = input
			next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			updated := next.(*uiModel)
			if cmd == nil || updated.input != "" {
				t.Fatalf("blocked command result = cmd %v, input %q", cmd, updated.input)
			}
			requireBusyCommandQueuesEmpty(t, updated)
		})
	}
}

func TestBusyEnterQueuesCompact(t *testing.T) {
	model := busyCommandTestModel()
	model.input = "/compact now"
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd != nil || updated.input != "" || len(updated.queued) != 1 || updated.queued[0].Text != "/compact now" {
		t.Fatalf("queued compact result = cmd %v, input %q, queued %+v", cmd, updated.input, updated.queued)
	}
	if len(updated.pendingInjected) != 0 {
		t.Fatalf("pending injected = %+v, want empty", updated.pendingInjected)
	}
}

func TestBusyNavigationCommandsStartTheirExistingTransitions(t *testing.T) {
	for input, action := range map[string]UIAction{
		"/exit": UIActionExit, "/new": UIActionNewSession, "/resume": UIActionResume,
		"/review": UIActionNewSession, "/init": UIActionNewSession,
	} {
		t.Run(input, func(t *testing.T) {
			model := busyCommandTestModel()
			model.input = input
			next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			updated := next.(*uiModel)
			if cmd == nil || updated.exitAction != action {
				t.Fatalf("transition = cmd %v, action %q; want action %q", cmd, updated.exitAction, action)
			}
		})
	}
}

func TestBusyTabQueuesValidatedCommands(t *testing.T) {
	tests := []struct {
		input string
		setup func(*uiModel)
	}{
		{input: "/compact now"},
		{input: "/review cli/app"},
		{
			input: "/fast on",
			setup: func(model *uiModel) {
				model.fastModeAvailable = true
			},
		},
		{input: "/goal resume"},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			model := busyCommandTestModel()
			model.input = test.input
			if test.setup != nil {
				test.setup(model)
			}
			next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyTab})
			updated := next.(*uiModel)
			if cmd != nil || updated.input != "" || len(updated.queued) != 1 || updated.queued[0].Text != test.input || len(updated.pendingInjected) != 0 {
				t.Fatalf("queued command result = cmd %v, input %q, queued %+v, pending %+v", cmd, updated.input, updated.queued, updated.pendingInjected)
			}
		})
	}
}

func TestBusyTabRejectsInvalidCommands(t *testing.T) {
	for _, input := range []string{"/fast on", "/back", "/ps kill proc-1", "/worktree list"} {
		t.Run(input, func(t *testing.T) {
			model := busyCommandTestModel()
			model.input = input
			next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyTab})
			updated := next.(*uiModel)
			if cmd == nil || updated.input != input {
				t.Fatalf("rejected command result = cmd %v, input %q", cmd, updated.input)
			}
			requireBusyCommandQueuesEmpty(t, updated)
		})
	}
}

func TestBusyQueuedCompactStartsCompactionAfterTurnDrains(t *testing.T) {
	model := busyCommandTestModel()
	model.input = "/compact tighten summary"
	want := model.input
	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated := next.(*uiModel)
	if len(updated.queued) != 1 || updated.queued[0].Text != want {
		t.Fatalf("queued commands = %+v", updated.queued)
	}

	next, cmd := updated.Update(submitDoneMsg{message: "done"})
	updated = next.(*uiModel)
	if cmd == nil || !updated.isBusy() || !updated.isCompacting() || len(updated.queued) != 0 {
		t.Fatalf("compact drain = cmd %v, busy %t, compacting %t, queued %+v", cmd, updated.isBusy(), updated.isCompacting(), updated.queued)
	}
}

func TestCompactionKeepsInputEditableAndQueuesSteering(t *testing.T) {
	client := &runtimeControlFakeClient{queueUserMessageID: "server-queue-1"}
	model := newProjectedTestUIModel(client)
	model.startupCmds = nil

	if cmd := model.inputController().startCompactionWithOrigin("", uiCompactionOriginManual); cmd == nil {
		t.Fatal("expected compaction command")
	}
	if !model.isCompacting() || !model.blocksRuntimeInput() || model.layout().mainInputPrefix() != "› " {
		t.Fatalf("compaction state = compacting %t, blocked %t, prefix %q", model.isCompacting(), model.blocksRuntimeInput(), model.layout().mainInputPrefix())
	}

	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("steer during compaction")})
	updated := next.(*uiModel)
	next, queueCmd := updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	if queueCmd == nil || updated.input != "" || len(updated.pendingInjected) != 1 || updated.pendingInjected[0].Text != "steer during compaction" || !updated.isCompacting() {
		t.Fatalf("queued steering = cmd %v, input %q, pending %+v, compacting %t", queueCmd, updated.input, updated.pendingInjected, updated.isCompacting())
	}
}

func busyCommandTestModel() *uiModel {
	model := newProjectedStaticUIModel(WithUIConversationFreshness(clientui.ConversationFreshnessEstablished))
	model.setRuntimeActivityBusyForTest(true)
	model.activity = uiActivityRunning
	return model
}

func requireBusyCommandQueuesEmpty(t *testing.T, model *uiModel) {
	t.Helper()
	if len(model.queued) != 0 || len(model.pendingInjected) != 0 {
		t.Fatalf("queued = %+v, pending injected = %+v", model.queued, model.pendingInjected)
	}
}
