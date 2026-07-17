package app

import (
	"bytes"
	"strings"
	"testing"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/transcript"
)

type countRinger struct {
	notifications int
	bells         int
}

func (r *countRinger) Notify(string) {
	r.notifications++
}

func (r *countRinger) Bell() {
	r.bells++
}

func (r *countRinger) total() int {
	return r.notifications + r.bells
}

func newUnfocusedBellHooks(ringer *countRinger) *bellHooks {
	return newBellHooks(ringer, nil, func() bool { return false })
}

func TestTerminalNotifierProtocolOutput(t *testing.T) {
	tests := []struct {
		name   string
		method string
		env    map[string]string
		bell   bool
		want   string
	}{
		{name: "BEL notification", method: notificationMethodBEL, want: terminalBell},
		{name: "OSC 9 notification", method: notificationMethodOSC9, want: osc9Prefix + "done" + terminalBell + terminalBell},
		{name: "OSC 9 raw bell", method: notificationMethodOSC9, bell: true, want: terminalBell},
		{name: "auto Ghostty", method: notificationMethodAuto, env: map[string]string{"TERM_PROGRAM": "ghostty"}, want: osc9Prefix + "done" + terminalBell + terminalBell},
		{name: "auto Windows Terminal", method: notificationMethodAuto, env: map[string]string{"TERM_PROGRAM": "ghostty", "WT_SESSION": "1"}, want: terminalBell},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			notifier := newTerminalNotifier(test.method, &out, func(key string) (string, bool) {
				value, ok := test.env[key]
				return value, ok
			})
			if test.bell {
				notifier.Bell()
			} else {
				notifier.Notify("done")
			}
			if got := out.String(); got != test.want {
				t.Fatalf("terminal output = %q, want %q", got, test.want)
			}
		})
	}
}

func TestBellHooksAttentionNotificationPolicy(t *testing.T) {
	unfocused := &countRinger{}
	hooks := newUnfocusedBellHooks(unfocused)
	hooks.OnAttentionNotification(testAttentionPendingEvent("question-1", clientui.AttentionNotificationKindQuestion, "question"))
	hooks.OnAttentionNotification(testAttentionPendingEvent("approval-1", clientui.AttentionNotificationKindApproval, "approval"))
	hooks.OnAttentionNotification(testAttentionPendingEvent("interrupted-1", clientui.AttentionNotificationKindInterruptedRun, "interrupted"))
	if unfocused.notifications != 2 || unfocused.bells != 0 {
		t.Fatalf("unfocused events = notifications %d, bells %d", unfocused.notifications, unfocused.bells)
	}

	focused := &countRinger{}
	newBellHooks(focused, nil, func() bool { return true }).OnAttentionNotification(
		testAttentionPendingEvent("question-2", clientui.AttentionNotificationKindQuestion, "question"),
	)
	if focused.notifications != 0 || focused.bells != 1 {
		t.Fatalf("focused events = notifications %d, bells %d", focused.notifications, focused.bells)
	}
}

func TestUIAskLifecycleDoesNotDuplicateAttentionNotifications(t *testing.T) {
	ringer := &countRinger{}
	model := newProjectedStaticUIModel(WithUITurnQueueHook(newUnfocusedBellHooks(ringer)))

	next, _ := model.Update(askEventMsg{event: askEvent{prompt: bellTestPrompt("ask-1", "First?")}})
	model = next.(*uiModel)
	next, _ = model.Update(askEventMsg{event: askEvent{prompt: bellTestPrompt("ask-2", "Second?")}})
	model = next.(*uiModel)
	_, _ = model.Update(askEventMsg{event: askEvent{resolvedPromptID: "ask-1"}})

	if ringer.total() != 0 {
		t.Fatalf("UI ask lifecycle emitted %d notification events", ringer.total())
	}
}

func TestAttentionNotificationLedgerLifecycle(t *testing.T) {
	ringer := &countRinger{}
	hooks := newUnfocusedBellHooks(ringer)
	surfaced := map[string]struct{}{}
	pending := testAttentionPendingEvent("question-batch-1", clientui.AttentionNotificationKindQuestion, "question")

	applyAttentionNotificationEvent(pending, surfaced, hooks)
	applyAttentionNotificationEvent(pending, surfaced, hooks)
	if ringer.notifications != 1 || len(surfaced) != 1 {
		t.Fatalf("duplicate pending events = notifications %d, surfaced %+v", ringer.notifications, surfaced)
	}

	applyAttentionNotificationEvent(clientui.AttentionNotificationEvent{
		Type: clientui.AttentionNotificationEventResolved,
		ID:   attentionNotificationIDPtr(clientui.AttentionNotificationKindQuestion, "question-batch-1"),
	}, surfaced, hooks)
	applyAttentionNotificationEvent(pending, surfaced, hooks)
	if ringer.notifications != 2 || len(surfaced) != 1 {
		t.Fatalf("reopened pending event = notifications %d, surfaced %+v", ringer.notifications, surfaced)
	}

	applyAttentionNotificationEvent(testAttentionPendingEvent("interrupted-1", clientui.AttentionNotificationKindInterruptedRun, "interrupted"), surfaced, hooks)
	applyAttentionNotificationEvent(clientui.AttentionNotificationEvent{Type: clientui.AttentionNotificationEventSnapshotComplete}, surfaced, hooks)
	applyAttentionNotificationEvent(clientui.AttentionNotificationEvent{
		Type: clientui.AttentionNotificationEventResolved,
		ID:   attentionNotificationIDPtr(clientui.AttentionNotificationKindQuestion, "missing"),
	}, surfaced, hooks)
	if ringer.notifications != 2 || len(surfaced) != 1 {
		t.Fatalf("unsupported/no-op events changed ledger: notifications %d, surfaced %+v", ringer.notifications, surfaced)
	}
}

func TestBellHooksToolHeavyTurnCompletion(t *testing.T) {
	ringer := &countRinger{}
	hooks := newUnfocusedBellHooks(ringer)

	hooks.OnTranscriptMessage(bellToolStartMessage(1))
	hooks.OnTranscriptMessage(bellAssistantFinalMessage(1))
	hooks.OnTurnQueueDrained()
	if ringer.notifications != 0 {
		t.Fatalf("single-tool turn emitted %d notifications", ringer.notifications)
	}

	recordToolHeavyBellTurn(hooks, 2)
	if ringer.notifications != 0 {
		t.Fatalf("tool-heavy turn notified before queue drain")
	}
	hooks.OnTurnQueueDrained()
	hooks.OnTurnQueueDrained()
	if ringer.notifications != 1 {
		t.Fatalf("tool-heavy queue drain emitted %d notifications, want one", ringer.notifications)
	}
}

func TestBellHooksTurnCompletionFocusPolicy(t *testing.T) {
	t.Run("focused suppresses", func(t *testing.T) {
		ringer := &countRinger{}
		hooks := newBellHooks(ringer, nil, func() bool { return true })
		recordToolHeavyBellTurn(hooks, 1)
		hooks.OnTurnQueueDrained()
		if ringer.total() != 0 {
			t.Fatalf("focused completion emitted %d events", ringer.total())
		}
	})
	t.Run("unknown focus notifies", func(t *testing.T) {
		ringer := &countRinger{}
		focus := newTerminalFocusState()
		hooks := newBellHooks(ringer, nil, focus.FocusedForAttention)
		recordToolHeavyBellTurn(hooks, 1)
		hooks.OnTurnQueueDrained()
		if ringer.notifications != 1 {
			t.Fatalf("unknown-focus completion emitted %d notifications", ringer.notifications)
		}
	})
}

func TestBellHooksNoopFinalizationScope(t *testing.T) {
	t.Run("clears pending completion", func(t *testing.T) {
		ringer := &countRinger{}
		hooks := newUnfocusedBellHooks(ringer)
		recordToolHeavyBellTurn(hooks, 1)
		hooks.OnTranscriptMessage(bellAssistantDeltaMessage(2, uiNoopFinalToken))
		hooks.OnTurnQueueDrained()
		if ringer.total() != 0 {
			t.Fatalf("NO_OP finalization emitted %d events", ringer.total())
		}
	})
	t.Run("preserves unrelated active turn", func(t *testing.T) {
		ringer := &countRinger{}
		hooks := newUnfocusedBellHooks(ringer)
		recordToolHeavyBellTurn(hooks, 1)
		hooks.OnTranscriptMessage(bellToolStartMessage(2))
		hooks.OnTranscriptMessage(bellAssistantDeltaMessage(3, uiNoopFinalToken))
		recordToolHeavyBellTurn(hooks, 2)
		hooks.OnTurnQueueDrained()
		if ringer.notifications != 1 {
			t.Fatalf("unrelated active turn emitted %d notifications", ringer.notifications)
		}
	})
}

func TestFormatAssistantPreview(t *testing.T) {
	long := strings.Repeat("a", terminalNotificationPreviewLimit+5)
	for _, test := range []struct {
		content string
		limit   int
		want    string
	}{
		{content: "\n  hello\tworld  ", limit: 80, want: "hello world"},
		{content: "", limit: 80, want: ""},
		{content: "abcdef", limit: 4, want: "abc…"},
		{content: long, limit: terminalNotificationPreviewLimit, want: strings.Repeat("a", terminalNotificationPreviewLimit-1) + "…"},
		{content: "ab\x1bcd\a ef", limit: 80, want: "abcd ef"},
	} {
		if got := formatAssistantPreview(test.content, test.limit); got != test.want {
			t.Fatalf("formatAssistantPreview(%q, %d) = %q, want %q", test.content, test.limit, got, test.want)
		}
	}
}

func TestBellHooksCorrelateQueuedTurnSteps(t *testing.T) {
	t.Run("mismatched final is ignored", func(t *testing.T) {
		ringer := &countRinger{}
		hooks := newUnfocusedBellHooks(ringer)
		hooks.OnTranscriptMessage(bellToolStartMessage(1))
		hooks.OnTranscriptMessage(bellToolStartMessage(1))
		hooks.OnTranscriptMessage(bellAssistantFinalMessage(2))
		hooks.OnTurnQueueDrained()
		if ringer.total() != 0 {
			t.Fatalf("mismatched final emitted %d events", ringer.total())
		}
	})
	t.Run("multiple queued turns notify once", func(t *testing.T) {
		ringer := &countRinger{}
		hooks := newUnfocusedBellHooks(ringer)
		recordToolHeavyBellTurn(hooks, 1)
		recordToolHeavyBellTurn(hooks, 2)
		hooks.OnTurnQueueDrained()
		if ringer.notifications != 1 {
			t.Fatalf("queued turns emitted %d notifications", ringer.notifications)
		}
	})
}

func TestBellHooksAbortClearsPendingCompletion(t *testing.T) {
	ringer := &countRinger{}
	hooks := newUnfocusedBellHooks(ringer)
	recordToolHeavyBellTurn(hooks, 1)
	hooks.OnTurnQueueAborted()
	hooks.OnTurnQueueDrained()
	if ringer.total() != 0 {
		t.Fatalf("aborted queue emitted %d events", ringer.total())
	}
}

func TestBellHooksCompactionCompletionPolicy(t *testing.T) {
	t.Run("unfocused immediate", func(t *testing.T) {
		ringer := &countRinger{}
		newUnfocusedBellHooks(ringer).OnUserCompactionCompleted(true)
		if ringer.notifications != 1 {
			t.Fatalf("immediate compaction emitted %d notifications", ringer.notifications)
		}
	})
	t.Run("focused suppressed", func(t *testing.T) {
		ringer := &countRinger{}
		newBellHooks(ringer, nil, func() bool { return true }).OnUserCompactionCompleted(true)
		if ringer.total() != 0 {
			t.Fatalf("focused compaction emitted %d events", ringer.total())
		}
	})
	t.Run("deferred until drain", func(t *testing.T) {
		ringer := &countRinger{}
		hooks := newUnfocusedBellHooks(ringer)
		hooks.OnUserCompactionCompleted(false)
		if ringer.total() != 0 {
			t.Fatalf("deferred compaction emitted before drain")
		}
		hooks.OnTurnQueueDrained()
		if ringer.notifications != 1 {
			t.Fatalf("deferred compaction emitted %d notifications", ringer.notifications)
		}
	})
}

func testAttentionPendingEvent(id string, kind clientui.AttentionNotificationKind, body string) clientui.AttentionNotificationEvent {
	notification := clientui.AttentionNotification{ID: attentionNotificationID(kind, id), Kind: kind}
	if kind == clientui.AttentionNotificationKindApproval {
		notification.Approval = &clientui.AttentionNotificationApprovalState{Message: body}
	} else {
		notification.Question = &clientui.AttentionNotificationQuestionState{
			PreparedAskIDs:          []string{id},
			MaterializedAskIDs:      []string{id},
			CurrentUnresolvedAskIDs: []string{id},
			Preview:                 body,
			DisplayCount:            1,
			MaterializedCount:       1,
		}
	}
	return clientui.AttentionNotificationEvent{Type: clientui.AttentionNotificationEventPending, Pending: &notification}
}

func attentionNotificationID(kind clientui.AttentionNotificationKind, uuid string) clientui.AttentionNotificationID {
	return clientui.AttentionNotificationID{Kind: kind, UUID: uuid}
}

func attentionNotificationIDPtr(kind clientui.AttentionNotificationKind, uuid string) *clientui.AttentionNotificationID {
	id := attentionNotificationID(kind, uuid)
	return &id
}

func bellTestPrompt(id, question string) clientui.TranscriptPrompt {
	prompt := *ongoingTranscriptMessage(2, clientui.TranscriptMessagePromptPending).Payload.PromptPending
	prompt.PromptID = clientui.PromptID(id)
	prompt.Question = question
	return prompt
}

func bellTestStepID(index int) runtimeids.StepID {
	values := []string{
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333",
	}
	if index < 1 || index > len(values) {
		panic("bell test step index is out of range")
	}
	id, err := runtimeids.ParseStepID(values[index-1])
	if err != nil {
		panic(err)
	}
	return id
}

func bellToolStartMessage(step int) clientui.TranscriptMessage {
	return clientui.TranscriptMessage{
		Sequence: 2,
		Kind:     clientui.TranscriptMessageToolStart,
		Payload: clientui.TranscriptPayload{ToolStart: &clientui.TranscriptToolStart{
			StepID: bellTestStepID(step), ToolCallID: "tool-call", ToolName: "exec_command",
		}},
	}
}

func bellAssistantFinalMessage(step int) clientui.TranscriptMessage {
	return clientui.TranscriptMessage{
		Sequence: 2,
		Kind:     clientui.TranscriptMessageCommittedRow,
		Payload: clientui.TranscriptPayload{CommittedRow: &clientui.TranscriptCommittedRow{
			Visibility: transcript.EntryVisibilityOngoing,
			Integrity:  transcript.RowIntegrityValid,
			Kind:       clientui.TranscriptRowAssistant,
			Assistant: &clientui.TranscriptAssistantRow{
				StepID: bellTestStepID(step), Text: "turn complete", Phase: transcript.AssistantPhaseFinal,
			},
		}},
	}
}

func bellAssistantDeltaMessage(step int, delta string) clientui.TranscriptMessage {
	return clientui.TranscriptMessage{
		Sequence: 2,
		Kind:     clientui.TranscriptMessageAssistantDelta,
		Payload: clientui.TranscriptPayload{AssistantDelta: &clientui.TranscriptAssistantDelta{
			StepID: bellTestStepID(step), StreamID: runtimeids.NewAssistantStreamID(), Delta: delta, Phase: transcript.AssistantPhaseFinal,
		}},
	}
}

func recordToolHeavyBellTurn(hooks *bellHooks, step int) {
	hooks.OnTranscriptMessage(bellToolStartMessage(step))
	hooks.OnTranscriptMessage(bellToolStartMessage(step))
	hooks.OnTranscriptMessage(bellAssistantFinalMessage(step))
}
