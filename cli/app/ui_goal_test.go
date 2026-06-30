package app

import (
	"strings"
	"testing"
	"time"

	"core/cli/tui"
	"core/server/runtime"
	"core/server/session"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/clientui"
	"core/shared/toolspec"
	"core/shared/transcript"

	tea "github.com/charmbracelet/bubbletea"
)

func applyGoalCmdMessagesForTest(t *testing.T, model *uiModel, cmd tea.Cmd) *uiModel {
	t.Helper()
	for _, msg := range collectCmdMessages(t, cmd) {
		next, nextCmd := model.Update(msg)
		model = next.(*uiModel)
		model = applyGoalCmdMessagesForTest(t, model, nextCmd)
	}
	return model
}

func updateGoalForTest(t *testing.T, model *uiModel, msg tea.Msg) *uiModel {
	t.Helper()
	next, cmd := model.Update(msg)
	return applyGoalCmdMessagesForTest(t, next.(*uiModel), cmd)
}

func TestGoalCommandOpensGoalOverlay(t *testing.T) {
	client := &runtimeControlFakeClient{goal: &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: "active"}}
	m := newSizedProjectedClosedUIModel(client, 100, 20)
	m.input = "/goal"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := applyGoalCmdMessagesForTest(t, next.(*uiModel), cmd)
	if !updated.goal.open {
		t.Fatal("expected /goal to open goal overlay")
	}
	if updated.inputMode() != uiInputModeGoal {
		t.Fatalf("input mode = %q, want goal", updated.inputMode())
	}
	if updated.view.Mode() != tui.ModeOngoing {
		t.Fatalf("view mode = %q, want ongoing", updated.view.Mode())
	}
	if updated.surface() != uiSurfaceGoal {
		t.Fatalf("surface = %q, want goal", updated.surface())
	}
	if cmd == nil {
		t.Fatal("expected /goal to emit overlay transition command")
	}
	plain := stripANSIAndTrimRight(updated.View())
	for _, want := range []string{"Goal", "Status: active", "ship feature"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected goal overlay to contain %q, got %q", want, plain)
		}
	}
}

func TestGoalCommandOpensGoalOverlayWhileBusy(t *testing.T) {
	client := &runtimeControlFakeClient{goal: &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: "active"}}
	m := newSizedProjectedClosedUIModel(client, 100, 20)
	m.setRuntimeActivityBusyForTest(true)
	m.activity = uiActivityRunning
	m.input = "/goal"

	updated := updateGoalForTest(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !updated.goal.open {
		t.Fatal("expected /goal to open goal overlay while busy")
	}
	if updated.inputMode() != uiInputModeGoal {
		t.Fatalf("input mode = %q, want goal", updated.inputMode())
	}
	if plain := stripANSIAndTrimRight(updated.View()); !strings.Contains(plain, "Status: active") || !strings.Contains(plain, "ship feature") {
		t.Fatalf("expected busy goal overlay content, got %q", plain)
	}
}

func TestGoalMutationsCoalesceAfterApplyingInFlightCompletion(t *testing.T) {
	client := &runtimeControlFakeClient{goal: &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusPaused}}
	m := newProjectedClosedUIModel(client)

	firstCmd := m.goalRuntimeCommand(goalRuntimePause, "")
	if firstCmd == nil {
		t.Fatal("expected first goal mutation command")
	}
	secondCmd := m.goalRuntimeCommand(goalRuntimeResume, "")
	if secondCmd != nil {
		t.Fatal("did not expect second goal mutation command while first is in flight")
	}
	firstMsgs := collectCmdMessages(t, firstCmd)
	if client.pauseGoalCalls != 1 || client.resumeGoalCalls != 0 {
		t.Fatalf("unexpected pre-completion goal calls: pause=%d resume=%d", client.pauseGoalCalls, client.resumeGoalCalls)
	}

	var firstDone goalRuntimeDoneMsg
	for _, msg := range firstMsgs {
		if typed, ok := msg.(goalRuntimeDoneMsg); ok {
			firstDone = typed
		}
	}
	next, followUpCmd := m.Update(firstDone)
	updated := next.(*uiModel)
	if followUpCmd == nil {
		t.Fatal("expected follow-up goal mutation command")
	}
	if updated.goal.goal == nil || updated.goal.goal.Status != clientui.RuntimeGoalStatusPaused {
		t.Fatalf("expected pause completion to update goal UI before follow-up, got %+v", updated.goal.goal)
	}
	_ = collectCmdMessages(t, followUpCmd)
	if client.resumeGoalCalls != 1 {
		t.Fatalf("expected serialized resume call after pause completion, got %d", client.resumeGoalCalls)
	}
}

func TestGoalCompleteCommandCompletesGoal(t *testing.T) {
	client := &runtimeControlFakeClient{goal: &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusActive}}
	m := newProjectedClosedUIModel(client)
	m.input = "/goal complete"

	updated := updateGoalForTest(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if updated.goal.goal == nil || updated.goal.goal.Status != clientui.RuntimeGoalStatusComplete {
		t.Fatalf("goal = %+v, want complete", updated.goal.goal)
	}
}

func TestActiveSuspendedGoalClearStillRequiresConfirmation(t *testing.T) {
	if !goalRequiresClearConfirmation(&clientui.RuntimeGoal{ID: "goal-1", Objective: "ship", Status: clientui.RuntimeGoalStatusActive, Suspended: true}) {
		t.Fatal("active suspended goal must require destructive clear confirmation")
	}
}

func TestGoalPreflightDoesNotStartMutationAfterLaterMutation(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedClosedUIModel(client)

	checkCmd := m.goalRuntimeCommand(goalRuntimeCheckSet, "older objective")
	if checkCmd == nil {
		t.Fatal("expected goal check command")
	}
	pauseCmd := m.goalRuntimeCommand(goalRuntimePause, "")
	if pauseCmd == nil {
		t.Fatal("expected later goal pause command")
	}

	var pauseDone goalRuntimeDoneMsg
	for _, msg := range collectCmdMessages(t, pauseCmd) {
		if typed, ok := msg.(goalRuntimeDoneMsg); ok {
			pauseDone = typed
		}
	}
	next, _ := m.Update(pauseDone)
	updated := next.(*uiModel)
	if updated.goal.goal == nil || updated.goal.goal.Status != clientui.RuntimeGoalStatusPaused {
		t.Fatalf("expected later pause mutation to apply, got %+v", updated.goal.goal)
	}

	var checkDone goalRuntimeDoneMsg
	for _, msg := range collectCmdMessages(t, checkCmd) {
		if typed, ok := msg.(goalRuntimeDoneMsg); ok {
			checkDone = typed
		}
	}
	next, followCmd := updated.Update(checkDone)
	updated = next.(*uiModel)
	if followCmd != nil {
		t.Fatal("did not expect stale preflight to schedule older set-goal mutation")
	}
	if client.setGoalArg != "" {
		t.Fatalf("stale preflight started set-goal mutation for %q", client.setGoalArg)
	}
	if updated.goal.goal == nil || updated.goal.goal.Status != clientui.RuntimeGoalStatusPaused {
		t.Fatalf("expected stale preflight not to overwrite later pause, got %+v", updated.goal.goal)
	}
}

func TestGoalSetRendersCommittedGoalFeedbackBeforeLaterRuntimeEvents(t *testing.T) {
	runtimeEvents := make(chan clientui.Event, 2)
	runtimeEvents <- clientui.Event{
		Kind:                       clientui.EventConversationUpdated,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         1,
		CommittedEntryStart:        0,
		CommittedEntryStartSet:     true,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:          string(transcript.EntryRoleGoalFeedback),
			Text:          "goal detail",
			CondensedText: `Goal set: "ship feature"`,
			Visibility:    clientui.EntryVisibilityAll,
		}},
	}
	runtimeEvents <- clientui.Event{Kind: clientui.EventAssistantDelta, AssistantDelta: "later model output"}
	client := &runtimeControlFakeClient{}
	m := newSizedProjectedRuntimeEventsUIModel(client, runtimeEvents, 100, 20)
	m.input = "/goal ship feature"

	updated := updateGoalForTest(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if client.setGoalArg != "ship feature" {
		t.Fatalf("set goal arg = %q, want ship feature", client.setGoalArg)
	}
	raw := waitRuntimeEvent(runtimeEvents)()
	batch, ok := raw.(runtimeEventBatchMsg)
	if !ok {
		t.Fatalf("expected runtime event batch, got %T", raw)
	}
	if len(batch.events) != 1 || batch.events[0].AssistantDelta != "" {
		t.Fatalf("expected first runtime batch to contain only goal feedback, got %+v", batch.events)
	}
	next, _ := updated.Update(batch)
	updated = next.(*uiModel)

	if view := stripANSIAndTrimRight(updated.view.OngoingSnapshot()); !strings.Contains(view, `Goal set: "ship feature"`) {
		t.Fatalf("expected goal feedback in ongoing transcript before later event, got %q", view)
	}
	if view := stripANSIAndTrimRight(updated.view.OngoingSnapshot()); strings.Contains(view, "later model output") {
		t.Fatalf("later runtime event rendered before explicit delivery: %q", view)
	}
}

func TestGoalResumeSlashCommandViaRuntimeRouteRendersGoalFeedbackOnce(t *testing.T) {
	runtimeEvents := make(chan clientui.Event, 16)
	_, eng := newAppRuntimeEngine(t, statusLineFakeClient{}, runtime.Config{
		WorkflowRun: &workflowruntime.Config{
			Contract: workflowruntime.CompletionContract{RunID: workflow.RunID("run-1")},
		},
		EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
		OnEvent: func(evt runtime.Event) {
			runtimeEvents <- projectRuntimeEvent(evt)
		},
	})
	t.Cleanup(func() { _ = eng.Close() })
	if _, err := eng.SetGoal("ship native scrollback", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if _, err := eng.SetGoalStatus(session.GoalStatusPaused, session.GoalActorUser); err != nil {
		t.Fatalf("SetGoalStatus paused: %v", err)
	}
	drainRuntimeEvents(runtimeEvents)

	m := newSizedProjectedRuntimeEventsUIModel(newUIRuntimeClient(eng), runtimeEvents, 120, 30)
	m.input = "/goal resume"
	updated := updateGoalForTest(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if goal := eng.Goal(); goal == nil || goal.Status != session.GoalStatusActive {
		t.Fatalf("goal after /goal resume = %+v, want active", goal)
	}

	goalEvent := waitForGoalFeedbackEvent(t, runtimeEvents)
	next, cmd := updated.Update(runtimeEventMsg{event: goalEvent})
	updated = next.(*uiModel)
	_ = collectCmdMessages(t, cmd)

	view := stripANSIAndTrimRight(updated.view.OngoingSnapshot())
	if got := strings.Count(view, "Goal resumed:"); got != 1 {
		t.Fatalf("goal resumed feedback count = %d, want 1 in %q", got, view)
	}
	if !strings.Contains(view, `Goal resumed: "ship native scrollback"`) {
		t.Fatalf("expected resumed goal feedback in ongoing transcript, got %q", view)
	}
}

func drainRuntimeEvents(events <-chan clientui.Event) {
	for {
		select {
		case <-events:
		default:
			return
		}
	}
}

func waitForGoalFeedbackEvent(t *testing.T, events <-chan clientui.Event) clientui.Event {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case evt := <-events:
			for _, entry := range evt.TranscriptEntries {
				if entry.Role == string(transcript.EntryRoleGoalFeedback) && strings.Contains(entry.CondensedText, "Goal resumed:") {
					return evt
				}
			}
		case <-timer.C:
			t.Fatal("timed out waiting for /goal resume goal_feedback event")
		}
	}
}

func TestGoalLifecycleCommandsDoNotAppendDuplicateLocalFeedback(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		initialGoal *clientui.RuntimeGoal
		drive       func(*testing.T, *uiModel, *runtimeControlFakeClient) *uiModel
		check       func(*testing.T, *runtimeControlFakeClient)
	}{
		{
			name:        "set",
			input:       "/goal ship feature",
			initialGoal: nil,
			check: func(t *testing.T, client *runtimeControlFakeClient) {
				t.Helper()
				if client.setGoalArg != "ship feature" {
					t.Fatalf("set goal arg = %q, want ship feature", client.setGoalArg)
				}
			},
		},
		{
			name:        "pause",
			input:       "/goal pause",
			initialGoal: &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: "active"},
			check: func(t *testing.T, client *runtimeControlFakeClient) {
				t.Helper()
				if client.pauseGoalCalls != 1 {
					t.Fatalf("pause calls = %d, want 1", client.pauseGoalCalls)
				}
			},
		},
		{
			name:        "resume",
			input:       "/goal resume",
			initialGoal: &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: "paused"},
			check: func(t *testing.T, client *runtimeControlFakeClient) {
				t.Helper()
				if client.resumeGoalCalls != 1 {
					t.Fatalf("resume calls = %d, want 1", client.resumeGoalCalls)
				}
			},
		},
		{
			name:        "clear",
			input:       "/goal clear",
			initialGoal: &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: "active"},
			drive: func(t *testing.T, m *uiModel, client *runtimeControlFakeClient) *uiModel {
				t.Helper()
				updated := updateGoalForTest(t, m, tea.KeyMsg{Type: tea.KeyEnter})
				return updateGoalForTest(t, updated, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
			},
			check: func(t *testing.T, client *runtimeControlFakeClient) {
				t.Helper()
				if client.clearGoalCalls != 1 {
					t.Fatalf("clear calls = %d, want 1", client.clearGoalCalls)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &runtimeControlFakeClient{goal: cloneRuntimeGoal(tt.initialGoal)}
			m := newSizedProjectedClosedUIModel(client, 100, 20)
			m.input = tt.input

			var updated *uiModel
			if tt.drive != nil {
				updated = tt.drive(t, m, client)
			} else {
				updated = updateGoalForTest(t, m, tea.KeyMsg{Type: tea.KeyEnter})
			}
			tt.check(t, client)
			if client.appendedRole != "" || client.appendedText != "" {
				t.Fatalf("did not expect duplicate local goal feedback, got role=%q text=%q", client.appendedRole, client.appendedText)
			}
			status := stripANSIAndTrimRight(updated.layout().renderStatusLine(120, uiThemeStyles("dark")))
			for _, forbidden := range []string{"Goal set", "Goal paused", "Goal resumed", "Goal cleared"} {
				if strings.Contains(status, forbidden) {
					t.Fatalf("did not expect duplicate transient goal status %q, got %q", forbidden, status)
				}
			}
		})
	}
}

func TestGoalCommandWithoutGoalShowsLocalHint(t *testing.T) {
	m := newSizedProjectedClosedUIModel(&runtimeControlFakeClient{}, 100, 20)
	m.input = "/goal"

	updated := updateGoalForTest(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	plain := stripANSIAndTrimRight(updated.View())
	if !strings.Contains(plain, noGoalHint) {
		t.Fatalf("expected no-goal hint %q, got %q", noGoalHint, plain)
	}
}

func TestWorkflowSessionGoalCommandReachesRuntime(t *testing.T) {
	client := &runtimeControlFakeClient{status: clientui.RuntimeStatus{WorkflowSession: &clientui.WorkflowSessionStatus{RunID: "run-1"}}}
	m := newSizedProjectedClosedUIModel(client, 100, 20)
	m.input = "/goal ship workflow"

	updateGoalForTest(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if client.setGoalArg != "ship workflow" {
		t.Fatalf("set goal arg = %q, want ship workflow (no client-side workflow block)", client.setGoalArg)
	}
}

func TestGoalClearActiveGoalRequiresConfirmation(t *testing.T) {
	client := &runtimeControlFakeClient{goal: &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: "active"}}
	m := newSizedProjectedClosedUIModel(client, 100, 20)
	m.input = "/goal clear"

	updated := updateGoalForTest(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !updated.goal.open || updated.goal.confirmMode != "clear" {
		t.Fatalf("expected clear confirmation overlay, got %+v", updated.goal)
	}
	if client.clearGoalCalls != 0 {
		t.Fatalf("clear calls before confirm = %d, want 0", client.clearGoalCalls)
	}
	if plain := stripANSIAndTrimRight(updated.View()); !strings.Contains(plain, "Clear active goal?") || !strings.Contains(plain, "[ Confirm ]") {
		t.Fatalf("expected clear confirmation with choice-group buttons, got %q", plain)
	}

	updated = updateGoalForTest(t, updated, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if updated.goal.open {
		t.Fatal("expected goal overlay closed after confirm")
	}
	if client.clearGoalCalls != 1 {
		t.Fatalf("clear calls after confirm = %d, want 1", client.clearGoalCalls)
	}
}

func TestGoalClearSuspendedActiveGoalRequiresConfirmation(t *testing.T) {
	client := &runtimeControlFakeClient{goal: &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: "active", Suspended: true}}
	m := newSizedProjectedClosedUIModel(client, 100, 20)
	m.input = "/goal clear"

	updated := updateGoalForTest(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !updated.goal.open || updated.goal.confirmMode != "clear" {
		t.Fatalf("expected suspended active clear confirmation, got %+v", updated.goal)
	}
	if client.clearGoalCalls != 0 {
		t.Fatalf("clear calls before confirm = %d, want 0", client.clearGoalCalls)
	}
}

func TestGoalConfirmationEnterUsesSelectedAction(t *testing.T) {
	client := &runtimeControlFakeClient{goal: &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: "active"}}
	m := newSizedProjectedClosedUIModel(client, 100, 20)
	m.input = "/goal clear"

	updated := updateGoalForTest(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	updated = updateGoalForTest(t, updated, tea.KeyMsg{Type: tea.KeyEnter})
	if updated.goal.open {
		t.Fatal("expected default cancel selection to close overlay")
	}
	if client.clearGoalCalls != 0 {
		t.Fatalf("clear calls after cancel selection = %d, want 0", client.clearGoalCalls)
	}

	m = newSizedProjectedClosedUIModel(client, 100, 20)
	m.input = "/goal clear"
	updated = updateGoalForTest(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	updated = updateGoalForTest(t, updated, tea.KeyMsg{Type: tea.KeyTab})
	updated = updateGoalForTest(t, updated, tea.KeyMsg{Type: tea.KeyEnter})
	if updated.goal.open {
		t.Fatal("expected confirm selection to close overlay")
	}
	if client.clearGoalCalls != 1 {
		t.Fatalf("clear calls after confirm selection = %d, want 1", client.clearGoalCalls)
	}
}

func TestGoalSetWhileBusyCanReplaceActiveGoalWithConfirmation(t *testing.T) {
	client := &runtimeControlFakeClient{goal: &clientui.RuntimeGoal{ID: "goal-1", Objective: "old goal", Status: "active"}}
	m := newSizedProjectedClosedUIModel(client, 100, 20)
	m.setRuntimeActivityBusyForTest(true)
	m.activity = uiActivityRunning
	m.input = "/goal new goal"

	updated := updateGoalForTest(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !updated.goal.open || updated.goal.confirmMode != "replace" || updated.goal.pendingObjective != "new goal" {
		t.Fatalf("expected busy replace confirmation overlay, got %+v", updated.goal)
	}
	if client.setGoalArg != "" {
		t.Fatalf("set goal before confirm = %q, want empty", client.setGoalArg)
	}

	updated = updateGoalForTest(t, updated, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if updated.goal.open {
		t.Fatal("expected goal overlay closed after confirm")
	}
	if client.setGoalArg != "new goal" {
		t.Fatalf("set goal after confirm = %q, want new goal", client.setGoalArg)
	}
}

func TestGoalOverlayRendersObjectiveMarkdown(t *testing.T) {
	client := &runtimeControlFakeClient{goal: &clientui.RuntimeGoal{ID: "goal-1", Objective: "Ship **bold** goal\n\n- one\n- two", Status: "active"}}
	m := newSizedProjectedClosedUIModel(client, 100, 20)
	m.input = "/goal"

	updated := updateGoalForTest(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	plain := stripANSIAndTrimRight(updated.View())
	if strings.Contains(plain, "**bold**") {
		t.Fatalf("expected markdown markers rendered away, got %q", plain)
	}
	if !strings.Contains(plain, "bold") || !strings.Contains(plain, "one") || !strings.Contains(plain, "two") {
		t.Fatalf("expected rendered markdown content, got %q", plain)
	}
}

func TestGoalOverlayWrapsLongMarkdownObjective(t *testing.T) {
	objective := "This **important** goal objective must keep the final tail phrase visible after wrapping."
	client := &runtimeControlFakeClient{goal: &clientui.RuntimeGoal{ID: "goal-1", Objective: objective, Status: "active"}}
	m := newSizedProjectedClosedUIModel(client, 32, 20)
	m.input = "/goal"

	updated := updateGoalForTest(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	plain := stripANSIAndTrimRight(updated.View())
	if !strings.Contains(plain, "visible after wrapping") {
		t.Fatalf("expected long markdown objective tail to survive wrapping, got %q", plain)
	}
}

func TestGoalOverlayWrapsLongMarkdownListItem(t *testing.T) {
	objective := "- This **important** list item must keep the final list tail visible after wrapping."
	client := &runtimeControlFakeClient{goal: &clientui.RuntimeGoal{ID: "goal-1", Objective: objective, Status: "active"}}
	m := newSizedProjectedClosedUIModel(client, 34, 20)
	m.input = "/goal"

	updated := updateGoalForTest(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	plain := stripANSIAndTrimRight(updated.View())
	if !strings.Contains(plain, "after wrapping") {
		t.Fatalf("expected long markdown list item tail to survive wrapping, got %q", plain)
	}
}

func TestGoalOverlayMarkdownCacheRewrapsAfterWidthChange(t *testing.T) {
	objective := "This **important** goal objective must change wrapping when the overlay width changes."
	client := &runtimeControlFakeClient{goal: &clientui.RuntimeGoal{ID: "goal-1", Objective: objective, Status: "active"}}
	m := newSizedProjectedClosedUIModel(client, 80, 20)
	m.input = "/goal"

	updated := updateGoalForTest(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	wide := stripANSIAndTrimRight(updated.View())
	if !strings.Contains(wide, "important goal objective must change wrapping") {
		t.Fatalf("expected wide render to keep phrase on one line, got %q", wide)
	}

	updated.termWidth = 32
	narrow := stripANSIAndTrimRight(updated.View())
	if strings.Contains(narrow, "important goal objective must change wrapping") {
		t.Fatalf("expected narrow render to rewrap cached markdown, got %q", narrow)
	}
	if !strings.Contains(narrow, "overlay width changes") {
		t.Fatalf("expected narrow render to preserve tail after rewrap, got %q", narrow)
	}
}

func TestGoalConfirmRendersChoiceGroupButtons(t *testing.T) {
	client := &runtimeControlFakeClient{goal: &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: "active"}}
	m := newSizedProjectedClosedUIModel(client, 100, 20)
	m.input = "/goal clear"

	updated := updateGoalForTest(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if updated.goal.confirmMode != "clear" {
		t.Fatalf("expected clear confirmation, got %+v", updated.goal)
	}
	plain := stripANSIAndTrimRight(updated.View())
	for _, want := range []string{"[ Cancel ]", "[ Confirm ]"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected confirm to reuse choice-group button %q, got %q", want, plain)
		}
	}
}

func TestGoalConfirmScrollsBodyWhileHorizontalKeysSelect(t *testing.T) {
	objective := strings.Repeat("long objective line that forces the confirmation body to overflow. ", 40)
	client := &runtimeControlFakeClient{goal: &clientui.RuntimeGoal{ID: "goal-1", Objective: objective, Status: "active"}}
	m := newSizedProjectedClosedUIModel(client, 60, 8)
	m.input = "/goal new objective"

	updated := updateGoalForTest(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if updated.goal.confirmMode != "replace" {
		t.Fatalf("expected replace confirmation, got %+v", updated.goal)
	}
	if updated.goal.confirmSelection != goalConfirmSelectionCancel {
		t.Fatalf("expected default cancel selection, got %d", updated.goal.confirmSelection)
	}

	scrollBeforeDown := updated.goal.scroll
	scrolled := updateGoalForTest(t, updated, tea.KeyMsg{Type: tea.KeyDown})
	if scrolled.goal.scroll == scrollBeforeDown {
		t.Fatalf("expected Down to scroll confirm body, scroll stayed %d", scrolled.goal.scroll)
	}
	if scrolled.goal.confirmSelection != goalConfirmSelectionCancel {
		t.Fatalf("expected Down to leave selection on cancel, got %d", scrolled.goal.confirmSelection)
	}

	scrollBeforeTab := scrolled.goal.scroll
	selected := updateGoalForTest(t, scrolled, tea.KeyMsg{Type: tea.KeyTab})
	if selected.goal.confirmSelection != goalConfirmSelectionConfirm {
		t.Fatalf("expected Tab to move selection to confirm, got %d", selected.goal.confirmSelection)
	}
	if selected.goal.scroll != scrollBeforeTab {
		t.Fatalf("expected Tab to leave scroll unchanged, was %d now %d", scrollBeforeTab, selected.goal.scroll)
	}
}

func TestGoalReplaceActiveGoalRequiresConfirmation(t *testing.T) {
	client := &runtimeControlFakeClient{goal: &clientui.RuntimeGoal{ID: "goal-1", Objective: "old goal", Status: "active"}}
	m := newSizedProjectedClosedUIModel(client, 100, 20)
	m.input = "/goal new goal"

	updated := updateGoalForTest(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !updated.goal.open || updated.goal.confirmMode != "replace" || updated.goal.pendingObjective != "new goal" {
		t.Fatalf("expected replace confirmation overlay, got %+v", updated.goal)
	}
	if client.setGoalArg != "" {
		t.Fatalf("set goal before confirm = %q, want empty", client.setGoalArg)
	}
	if plain := stripANSIAndTrimRight(updated.View()); !strings.Contains(plain, "Replace active goal?") || !strings.Contains(plain, "New: new goal") {
		t.Fatalf("expected replace confirmation text, got %q", plain)
	}

	updated = updateGoalForTest(t, updated, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if updated.goal.open {
		t.Fatal("expected goal overlay closed after confirm")
	}
	if client.setGoalArg != "new goal" {
		t.Fatalf("set goal after confirm = %q, want new goal", client.setGoalArg)
	}
}
