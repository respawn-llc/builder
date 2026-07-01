package app

import (
	"context"
	"errors"
	"testing"

	"core/server/runtime"
	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSubmitDoneDefersTurnCompletionBellUntilQueuedTurnsFinish(t *testing.T) {
	ringer := &countRinger{}
	bells := newUnfocusedBellHooks(ringer)
	m := newProjectedStaticUIModel(WithUITurnQueueHook(bells))
	m.setBusy(true)
	m.queued = queuedInputsForTest("follow up")

	next, _ := m.Update(runtimeEventMsg{event: clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-1"}})
	updated := next.(*uiModel)
	next, _ = updated.Update(runtimeEventMsg{event: clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-1"}})
	updated = next.(*uiModel)
	next, _ = updated.Update(runtimeEventMsg{event: clientui.Event{Kind: clientui.EventAssistantMessage, StepID: "step-1", TranscriptEntries: []clientui.ChatEntry{{Role: "assistant", Text: "first"}}}})
	updated = next.(*uiModel)

	next, cmd := updated.Update(submitDoneMsg{message: "first"})
	updated = next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected queued follow-up to start when first turn completes")
	}
	if got := ringer.Count(); got != 0 {
		t.Fatalf("ring count = %d after first queued turn completion, want 0", got)
	}
	if !updated.isBusy() {
		t.Fatal("expected queued follow-up submission to be running")
	}

	next, _ = updated.Update(runtimeEventMsg{event: clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-2"}})
	updated = next.(*uiModel)
	next, _ = updated.Update(runtimeEventMsg{event: clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-2"}})
	updated = next.(*uiModel)
	next, _ = updated.Update(runtimeEventMsg{event: clientui.Event{Kind: clientui.EventAssistantMessage, StepID: "step-2", TranscriptEntries: []clientui.ChatEntry{{Role: "assistant", Text: "second"}}}})
	updated = next.(*uiModel)

	next, _ = updated.Update(submitDoneMsg{message: "second"})
	updated = next.(*uiModel)
	if got := ringer.Count(); got != 1 {
		t.Fatalf("ring count = %d after queued turns drain, want 1", got)
	}
	if got := ringer.Last(); got != "kent: second" {
		t.Fatalf("last message = %q, want %q", got, "kent: second")
	}
	if updated.isBusy() {
		t.Fatal("expected UI idle after queued turns drain")
	}
}

func TestSubmitErrorAbortsPendingTurnCompletionBell(t *testing.T) {
	_, eng := newAppRuntimeEngine(t, &runtimeAdapterFakeClient{}, runtime.Config{})

	ringer := &countRinger{}
	bells := newUnfocusedBellHooks(ringer)
	m := newProjectedEngineUIModel(eng, WithUITurnQueueHook(bells))

	next, _ := m.Update(runtimeEventMsg{event: clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-1"}})
	updated := next.(*uiModel)
	next, _ = updated.Update(runtimeEventMsg{event: clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-1"}})
	updated = next.(*uiModel)
	next, _ = updated.Update(runtimeEventMsg{event: clientui.Event{Kind: clientui.EventAssistantMessage, StepID: "step-1", TranscriptEntries: []clientui.ChatEntry{{Role: "assistant", Text: "first"}}}})
	updated = next.(*uiModel)

	updated.input = "continue"
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)

	next, _ = updated.Update(submitDoneMsg{token: updated.activeSubmit.token, submittedText: "continue", err: errors.New("submit failed")})
	updated = next.(*uiModel)
	if updated.activity != uiActivityError {
		t.Fatalf("expected error activity after submit failure, got %v", updated.activity)
	}

	bells.OnTurnQueueDrained()
	if got := ringer.Count(); got != 0 {
		t.Fatalf("ring count = %d after submit abort, want 0", got)
	}
}

func TestNoopFinalAbortsPendingTurnCompletionBell(t *testing.T) {
	ringer := &countRinger{}
	bells := newUnfocusedBellHooks(ringer)
	m := newProjectedStaticUIModel(WithUITurnQueueHook(bells))
	m.setBusy(true)

	next, _ := m.Update(runtimeEventMsg{event: clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-1"}})
	updated := next.(*uiModel)
	next, _ = updated.Update(runtimeEventMsg{event: clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-1"}})
	updated = next.(*uiModel)
	next, _ = updated.Update(runtimeEventMsg{event: clientui.Event{Kind: clientui.EventAssistantMessage, StepID: "step-1", TranscriptEntries: []clientui.ChatEntry{{Role: "assistant", Text: "working"}}}})
	updated = next.(*uiModel)

	next, _ = updated.Update(newSubmitDoneMsg(0, uiNoopFinalToken, "", nil))
	updated = next.(*uiModel)
	if got := ringer.Count(); got != 0 {
		t.Fatalf("ring count = %d after NO_OP final, want 0", got)
	}
	bells.OnTurnQueueDrained()
	if got := ringer.Count(); got != 0 {
		t.Fatalf("ring count = %d after forced drain following NO_OP final, want 0", got)
	}
	if updated.isBusy() {
		t.Fatal("expected UI idle after NO_OP final")
	}
}

func TestQueuedFollowUpAfterNoopFinalDoesNotLeakTurnCompletionBell(t *testing.T) {
	ringer := &countRinger{}
	bells := newUnfocusedBellHooks(ringer)
	m := newProjectedStaticUIModel(WithUITurnQueueHook(bells))
	m.setBusy(true)
	m.queued = queuedInputsForTest("follow up")

	next, _ := m.Update(runtimeEventMsg{event: clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-1"}})
	updated := next.(*uiModel)
	next, _ = updated.Update(runtimeEventMsg{event: clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-1"}})
	updated = next.(*uiModel)
	next, _ = updated.Update(runtimeEventMsg{event: clientui.Event{Kind: clientui.EventAssistantMessage, StepID: "step-1", TranscriptEntries: []clientui.ChatEntry{{Role: "assistant", Text: "working"}}}})
	updated = next.(*uiModel)

	next, cmd := updated.Update(newSubmitDoneMsg(0, uiNoopFinalToken, "", nil))
	updated = next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected queued follow-up to start after NO_OP final")
	}
	if got := ringer.Count(); got != 0 {
		t.Fatalf("ring count = %d after NO_OP final queued follow-up, want 0", got)
	}
	if !updated.isBusy() {
		t.Fatal("expected queued follow-up submission to be running")
	}
	bells.OnTurnQueueDrained()
	if got := ringer.Count(); got != 0 {
		t.Fatalf("ring count = %d after forced drain following queued NO_OP final, want 0", got)
	}
}

func TestManualCompactRingsWhenIdleAfterCompaction(t *testing.T) {
	ringer := &countRinger{}
	bells := newUnfocusedBellHooks(ringer)
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents(), WithUITurnQueueHook(bells))
	m.startupCmds = nil
	m.input = "/compact keep API details"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected manual compaction command")
	}
	if !updated.isBusy() || !updated.isCompacting() {
		t.Fatalf("expected manual compaction to set busy/compacting, busy=%t compacting=%t", updated.isBusy(), updated.isCompacting())
	}
	msgs := collectCmdMessages(t, cmd)
	var done compactDoneMsg
	foundDone := false
	for _, msg := range msgs {
		if typed, ok := msg.(compactDoneMsg); ok {
			done = typed
			foundDone = true
		}
	}
	if !foundDone {
		t.Fatalf("expected compactDoneMsg, got %+v", msgs)
	}

	next, cmd = updated.Update(done)
	updated = next.(*uiModel)
	updated, _ = applyQueuedRuntimeWorkCheckForTest(t, updated, cmd)
	if updated.isBusy() || updated.isCompacting() {
		t.Fatalf("expected idle after manual compaction, busy=%t compacting=%t", updated.isBusy(), updated.isCompacting())
	}
	if got := ringer.Count(); got != 1 {
		t.Fatalf("ring count = %d after idle manual compaction, want 1", got)
	}
	if got := ringer.Last(); got != "kent: Compaction finished" {
		t.Fatalf("last ring = %q, want compaction completion", got)
	}
}

func TestQueuedCompactRingsAfterCompactionWhenQueueIsDrained(t *testing.T) {
	ringer := &countRinger{}
	bells := newUnfocusedBellHooks(ringer)
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents(), WithUITurnQueueHook(bells))
	m.startupCmds = nil
	m.setBusy(true)
	m.activity = uiActivityRunning
	m.input = "/compact queued"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated := next.(*uiModel)
	if len(updated.queued) != 1 {
		t.Fatalf("expected queued compact command, got %+v", updated.queued)
	}
	next, cmd := updated.Update(submitDoneMsg{message: "done"})
	updated = next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected queued compact to start after turn done")
	}
	msgs := collectCmdMessages(t, cmd)
	var done compactDoneMsg
	foundDone := false
	for _, msg := range msgs {
		if typed, ok := msg.(compactDoneMsg); ok {
			done = typed
			foundDone = true
		}
	}
	if !foundDone {
		t.Fatalf("expected compactDoneMsg from queued compact, got %+v", msgs)
	}

	next, cmd = updated.Update(done)
	updated = next.(*uiModel)
	updated, _ = applyQueuedRuntimeWorkCheckForTest(t, updated, cmd)
	if updated.isBusy() || updated.isCompacting() {
		t.Fatalf("expected idle after queued compaction, busy=%t compacting=%t", updated.isBusy(), updated.isCompacting())
	}
	if got := ringer.Count(); got != 1 {
		t.Fatalf("ring count = %d after queued compact, want 1", got)
	}
	if got := ringer.Last(); got != "kent: Compaction finished" {
		t.Fatalf("last ring = %q, want compaction completion", got)
	}
}

func TestQueuedCompactDefersBellUntilFollowingQueuedMessageDrains(t *testing.T) {
	ringer := &countRinger{}
	bells := newUnfocusedBellHooks(ringer)
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents(), WithUITurnQueueHook(bells))
	m.startupCmds = nil
	m.setBusy(true)
	m.activity = uiActivityRunning
	m.input = "/compact queued"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated := next.(*uiModel)
	updated.input = "follow up"
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated = next.(*uiModel)
	if len(updated.queued) != 2 {
		t.Fatalf("expected queued compact plus follow-up, got %+v", updated.queued)
	}

	next, cmd := updated.Update(submitDoneMsg{message: "done"})
	updated = next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected queued drain hydration after turn done")
	}
	msgs := collectCmdMessages(t, cmd)
	var refresh runtimeTranscriptRefreshedMsg
	foundRefresh := false
	for _, msg := range msgs {
		if typed, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			refresh = typed
			foundRefresh = true
		}
	}
	if !foundRefresh {
		t.Fatalf("expected queued drain transcript refresh, got %+v", msgs)
	}

	next, cmd = updated.Update(refresh)
	updated = next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected queued compact to start after hydration")
	}
	msgs = collectCmdMessages(t, cmd)
	var compactDone compactDoneMsg
	foundCompactDone := false
	for _, msg := range msgs {
		if typed, ok := msg.(compactDoneMsg); ok {
			compactDone = typed
			foundCompactDone = true
		}
	}
	if !foundCompactDone {
		t.Fatalf("expected compactDoneMsg from queued compact, got %+v", msgs)
	}

	next, cmd = updated.Update(compactDone)
	updated = next.(*uiModel)
	if got := ringer.Count(); got != 0 {
		t.Fatalf("ring count after compact before following queued message = %d, want 0", got)
	}
	if cmd == nil {
		t.Fatal("expected following queued message to start after compact")
	}
	msgs = collectCmdMessages(t, cmd)
	var submitDone submitDoneMsg
	foundSubmitDone := false
	for _, msg := range msgs {
		if typed, ok := msg.(submitDoneMsg); ok {
			submitDone = typed
			foundSubmitDone = true
		}
	}
	if !foundSubmitDone {
		t.Fatalf("expected following queued submitDoneMsg, got %+v", msgs)
	}

	next, _ = updated.Update(submitDone)
	if got := ringer.Count(); got != 1 {
		t.Fatalf("ring count after following queued message drained = %d, want 1", got)
	}
	if got := ringer.Last(); got != "kent: Compaction finished" {
		t.Fatalf("last ring = %q, want compaction completion", got)
	}
}

func TestManualCompactWithQueuedSteeringDoesNotRing(t *testing.T) {
	ringer := &countRinger{}
	bells := newUnfocusedBellHooks(ringer)
	client := &runtimeControlFakeClient{hasQueuedUserWork: true, submitQueuedResult: "resumed"}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents(), WithUITurnQueueHook(bells))
	m.startupCmds = nil
	m.setBusy(true)
	m.setCompacting(true)
	m.activity = uiActivityRunning
	m.compactionOrigin = uiCompactionOriginManual
	m.input = "steer after compact"

	next, createCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	updated = applyFirstInjectedQueueCreateDoneForTest(t, updated, createCmd)
	if len(updated.pendingInjected) != 1 {
		t.Fatalf("expected queued steering, got %+v", updated.pendingInjected)
	}
	next, cmd := updated.Update(compactDoneMsg{})
	updated = next.(*uiModel)
	updated, cmd = applyQueuedRuntimeWorkCheckForTest(t, updated, cmd)
	if cmd == nil {
		t.Fatal("expected queued steering to resume after compact")
	}
	if got := ringer.Count(); got != 0 {
		t.Fatalf("ring count = %d after compact resumed steering, want 0", got)
	}

	msgs := collectCmdMessages(t, cmd)
	var done submitDoneMsg
	foundDone := false
	for _, msg := range msgs {
		if typed, ok := msg.(submitDoneMsg); ok {
			done = typed
			foundDone = true
		}
	}
	if !foundDone {
		t.Fatalf("expected resumed steering submitDoneMsg, got %+v", msgs)
	}
	next, _ = updated.Update(done)
	if got := ringer.Count(); got != 1 {
		t.Fatalf("ring count = %d after queued steering drain, want 1", got)
	}
	if got := ringer.Last(); got != "kent: Compaction finished" {
		t.Fatalf("last ring = %q, want compaction completion", got)
	}
}

func TestManualCompactRingsAfterQueuedLocalCommandDrains(t *testing.T) {
	ringer := &countRinger{}
	bells := newUnfocusedBellHooks(ringer)
	m := newProjectedStaticUIModel(WithUITurnQueueHook(bells))
	m.setBusy(true)
	m.setCompacting(true)
	m.activity = uiActivityRunning
	m.compactionOrigin = uiCompactionOriginManual
	m.queued = queuedInputsForTest("/status")

	next, cmd := m.Update(compactDoneMsg{})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected queued local command to run after compaction")
	}
	if updated.isBusy() || len(updated.queued) != 0 {
		t.Fatalf("expected queued local command to drain to idle, busy=%t queued=%+v", updated.isBusy(), updated.queued)
	}
	if got := ringer.Count(); got != 1 {
		t.Fatalf("ring count after queued local command drain = %d, want 1", got)
	}
	if got := ringer.Last(); got != "kent: Compaction finished" {
		t.Fatalf("last ring = %q, want compaction completion", got)
	}
}

func TestFailedManualCompactClearsCompactionBell(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "failure", err: errors.New("compact failed")},
		{name: "canceled", err: context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ringer := &countRinger{}
			bells := newUnfocusedBellHooks(ringer)
			m := newProjectedStaticUIModel(WithUITurnQueueHook(bells))
			m.setBusy(true)
			m.setCompacting(true)
			m.activity = uiActivityRunning
			m.compactionOrigin = uiCompactionOriginManual

			next, _ := m.Update(compactDoneMsg{err: tt.err})
			updated := next.(*uiModel)
			if updated.compactionOrigin != uiCompactionOriginNone {
				t.Fatalf("expected compaction origin cleared after %s, got %v", tt.name, updated.compactionOrigin)
			}
			if got := ringer.Count(); got != 0 {
				t.Fatalf("ring count after %s = %d, want 0", tt.name, got)
			}

			next, _ = updated.Update(compactDoneMsg{})
			if got := ringer.Count(); got != 0 {
				t.Fatalf("delayed ring count after %s = %d, want 0", tt.name, got)
			}
		})
	}
}

func TestManualCompactWithPendingQueuedDrainHydrationDoesNotRing(t *testing.T) {
	ringer := &countRinger{}
	bells := newUnfocusedBellHooks(ringer)
	m := newProjectedStaticUIModel(WithUITurnQueueHook(bells))
	m.setBusy(true)
	m.setCompacting(true)
	m.activity = uiActivityRunning
	m.compactionOrigin = uiCompactionOriginManual
	m.pendingQueuedDrainAfterHydration = true

	next, _ := m.Update(compactDoneMsg{})
	updated := next.(*uiModel)
	if updated.compactionOrigin != uiCompactionOriginNone {
		t.Fatalf("expected compaction origin cleared, got %v", updated.compactionOrigin)
	}
	if got := ringer.Count(); got != 0 {
		t.Fatalf("ring count after compact with queued drain hydration = %d, want 0", got)
	}

	updated.inputController().notifyTurnQueueDrainedIfIdle()
	if got := ringer.Count(); got != 1 {
		t.Fatalf("ring count after later queued drain = %d, want 1", got)
	}
	if got := ringer.Last(); got != "kent: Compaction finished" {
		t.Fatalf("last ring = %q, want compaction completion", got)
	}
}
