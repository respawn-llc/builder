package app

import (
	"testing"

	"core/cli/app/commands"
	"core/shared/clientui"
	"core/shared/runtimeinput"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDefaultRegistryBusyContract(t *testing.T) {
	registry := commands.NewDefaultRegistry()
	want := map[string]commands.ActiveRunPolicy{
		"exit": commands.ActiveRunPolicyAllowed, "login": commands.ActiveRunPolicyRequiresIdle,
		"new": commands.ActiveRunPolicyAllowed, "resume": commands.ActiveRunPolicyAllowed,
		"logout": commands.ActiveRunPolicyRequiresIdle, "compact": commands.ActiveRunPolicyAllowed,
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
			testSetMainInput(model, test.input)
			if test.setup != nil {
				test.setup(model)
			}
			next, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			updated := next.(*uiModel)
			if testMainInput(updated) != "" || updated.sessionName != test.sessionName || updated.thinkingLevel != test.thinkingLevel || updated.fastModeEnabled != test.fast {
				t.Fatalf("updated model = input %q, name %q, thinking %q, fast %t", testMainInput(updated), updated.sessionName, updated.thinkingLevel, updated.fastModeEnabled)
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
			model.sessionID = "busy-navigation-session"
			testSetMainInput(model, input)
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
	for _, input := range []string{"/worktree list", "/wt switch feature"} {
		t.Run(input, func(t *testing.T) {
			model := busyCommandTestModel()
			testSetMainInput(model, input)
			next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			updated := next.(*uiModel)
			if cmd == nil || testMainInput(updated) != "" {
				t.Fatalf("blocked command result = cmd %v, input %q", cmd, testMainInput(updated))
			}
			requireBusyCommandQueuesEmpty(t, updated)
		})
	}
}

func TestBusyEnterOpensBareWorktreePickerAliases(t *testing.T) {
	for _, input := range []string{"/worktree", "/wt"} {
		t.Run(input, func(t *testing.T) {
			client := &worktreeCommandTestClient{listResp: testMainWorktreeListResponse()}
			model := busyCommandTestModel()
			model.worktreeClient = client
			testSetMainInput(model, input)

			next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			updated := next.(*uiModel)
			if cmd == nil || updated.inputMode() != uiInputModeWorktree {
				t.Fatalf("bare picker result = cmd %v mode %v, want worktree overlay", cmd, updated.inputMode())
			}
			if len(client.enterRequests) != 0 || len(client.deleteRequests) != 0 || len(client.createRequests) != 0 {
				t.Fatalf(
					"bare picker performed a worktree mutation: enter=%+v delete=%+v create=%+v",
					client.enterRequests,
					client.deleteRequests,
					client.createRequests,
				)
			}
		})
	}
}

func TestPromptCommandRemainsTypedRuntimeSubmission(t *testing.T) {
	client := &runtimeControlFakeClient{}
	model := newProjectedTestUIModel(
		client,
		WithUIConversationFreshness(clientui.ConversationFreshnessEstablished),
		WithUIPromptCommandCatalogEntries([]commands.PromptCommandCatalogEntry{{
			Name:    "prompt:inspect",
			Preview: "Inspect the requested scope",
		}}),
	)
	model.commandRegistry = commands.NewDefaultRegistryWithPromptCatalog(model.promptCatalogEntries)
	submitted := "/prompt:inspect cli/app"
	testSetMainInput(model, submitted)

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(*uiModel)
	for _, msg := range collectCmdMessages(t, cmd) {
		model = updateUIModel(t, model, msg)
	}

	if client.submitCalls != 1 {
		t.Fatalf("typed prompt steering calls = %d, want 1", client.submitCalls)
	}
	if client.submitInput.Kind != runtimeinput.KindPromptCommand ||
		client.submitInput.PromptCommand == nil ||
		client.submitInput.PromptCommand.Name != "prompt:inspect" ||
		client.submitInput.PromptCommand.Arguments != "cli/app" {
		t.Fatalf("typed prompt steering input = %+v", client.submitInput)
	}
}

func TestBusyEnterDispatchesCompact(t *testing.T) {
	model := busyCommandTestModel()
	testSetMainInput(model, "/compact now")
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil || testMainInput(updated) != "" || len(updated.queued) != 0 || !updated.isCompacting() {
		t.Fatalf("compact dispatch = cmd %v, input %q, queued %+v, compacting %t", cmd, testMainInput(updated), updated.queued, updated.isCompacting())
	}
	testSetMainInput(updated, "/compact again")
	next, repeatCmd := updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	if repeatCmd != nil || !updated.isCompacting() || len(updated.queued) != 0 || len(updated.injectedQueue) != 0 {
		t.Fatalf("repeat compact = cmd %v compacting %t queued %+v injected %+v", repeatCmd, updated.isCompacting(), updated.queued, updated.injectedQueue)
	}
}

func TestBusyNavigationCommandsStartTheirExistingTransitions(t *testing.T) {
	for input, action := range map[string]UIAction{
		"/exit": UIActionExit, "/new": UIActionNewSession, "/resume": UIActionResume,
		"/review": UIActionNewSession, "/init": UIActionNewSession,
	} {
		t.Run(input, func(t *testing.T) {
			model := busyCommandTestModel()
			testSetMainInput(model, input)
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
			testSetMainInput(model, test.input)
			if test.setup != nil {
				test.setup(model)
			}
			next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyTab})
			updated := next.(*uiModel)
			if cmd != nil || testMainInput(updated) != "" || len(updated.queued) != 1 || updated.queued[0].Text != test.input || len(updated.injectedQueue) != 0 {
				t.Fatalf("queued command result = cmd %v, input %q, queued %+v, injected %+v", cmd, testMainInput(updated), updated.queued, updated.injectedQueue)
			}
		})
	}
}

func TestBusyTabRejectsInvalidCommands(t *testing.T) {
	for _, input := range []string{"/fast on", "/back", "/ps kill proc-1", "/worktree list"} {
		t.Run(input, func(t *testing.T) {
			model := busyCommandTestModel()
			testSetMainInput(model, input)
			next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyTab})
			updated := next.(*uiModel)
			if cmd == nil || testMainInput(updated) != input {
				t.Fatalf("rejected command result = cmd %v, input %q", cmd, testMainInput(updated))
			}
			requireBusyCommandQueuesEmpty(t, updated)
		})
	}
}

func TestBusyQueuedCompactStartsCompactionAfterTurnDrains(t *testing.T) {
	model := busyCommandTestModel()
	testSetMainInput(model, "/compact tighten summary")
	want := testMainInput(model)
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
	client := &runtimeControlFakeClient{submitQueuedID: "server-queue-1"}
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
	if queueCmd == nil || testMainInput(updated) != "" || len(updated.injectedQueue) != 1 || updated.injectedQueue[0].Text != "steer during compaction" || !updated.isCompacting() {
		t.Fatalf("queued steering = cmd %v, input %q, injected %+v, compacting %t", queueCmd, testMainInput(updated), updated.injectedQueue, updated.isCompacting())
	}
}

func busyCommandTestModel() *uiModel {
	model := newProjectedStaticUIModel(WithUIConversationFreshness(clientui.ConversationFreshnessEstablished))
	model.sessionID = "busy-navigation-session"
	model.setRuntimeActivityBusyForTest(true)
	model.activity = uiActivityRunning
	return model
}

func requireBusyCommandQueuesEmpty(t *testing.T, model *uiModel) {
	t.Helper()
	if len(model.queued) != 0 || len(model.injectedQueue) != 0 {
		t.Fatalf("queued = %+v, injected = %+v", model.queued, model.injectedQueue)
	}
}
