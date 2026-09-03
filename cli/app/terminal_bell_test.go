package app

import (
	"bytes"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"core/cli/tui/transcriptrender"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/transcript"
)

type countRinger struct {
	notifications int
	bells         int
	messages      []string
}

func (r *countRinger) Notify(message string) {
	r.notifications++
	r.messages = append(r.messages, message)
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

func TestOSC9NotifierEncodesFormattedMessageVerbatim(t *testing.T) {
	var out bytes.Buffer
	notifier := newOSC9TerminalNotifier(&out)
	formatted := "dynamic  formatted notification"

	notifier.Notify(formatted)

	want := osc9Prefix + formatted + terminalBell + terminalBell
	if got := out.String(); got != want {
		t.Fatalf("OSC 9 output = %q, want opaque formatted payload %q", got, want)
	}
}

func TestBellHooksAttentionNotificationPolicy(t *testing.T) {
	unfocused := &countRinger{}
	hooks := newUnfocusedBellHooks(unfocused)
	hooks.OnAttentionNotification(testAttentionPendingEvent("question-1", clientui.AttentionNotificationKindQuestion, "question"))
	hooks.OnAttentionNotification(testAttentionPendingEvent("approval-1", clientui.AttentionNotificationKindApproval, "approval"))
	hooks.OnAttentionNotification(testAttentionPendingEvent("interrupted-1", clientui.AttentionNotificationKindInterruptedCurrentNode, "interrupted"))
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

func TestBellHooksAttentionNotificationsPropagateDynamicMarkdownPreview(t *testing.T) {
	ringer := &countRinger{}
	hooks := newUnfocusedBellHooks(ringer)
	questionPreview := "dynamic-question-preview"
	approvalPreview := "dynamic-approval-preview"

	hooks.OnAttentionNotification(testAttentionPendingEvent(
		"question-dynamic",
		clientui.AttentionNotificationKindQuestion,
		"**"+questionPreview+"**",
	))
	hooks.OnAttentionNotification(testAttentionPendingEvent(
		"approval-dynamic",
		clientui.AttentionNotificationKindApproval,
		"`"+approvalPreview+"`",
	))

	if ringer.notifications != 2 || len(ringer.messages) != 2 {
		t.Fatalf("dynamic attention events = notifications %d, messages %d", ringer.notifications, len(ringer.messages))
	}
	questionFields := strings.Fields(ringer.messages[0])
	if got := questionFields[len(questionFields)-1]; got != questionPreview {
		t.Fatalf("question notification dynamic preview = %q, want %q", got, questionPreview)
	}
	approvalFields := strings.Fields(ringer.messages[1])
	if got := approvalFields[len(approvalFields)-1]; got != approvalPreview {
		t.Fatalf("approval notification dynamic preview = %q, want %q", got, approvalPreview)
	}
}

func TestBellHooksTextNotificationsHaveNonEmptyStructuralFallbacks(t *testing.T) {
	ringer := &countRinger{}
	title := "dynamic-session"
	hooks := newBellHooks(ringer, func() string { return title }, func() bool { return false })

	hooks.OnAttentionNotification(testAttentionPendingEvent(
		"question-empty",
		clientui.AttentionNotificationKindQuestion,
		"",
	))
	hooks.OnAttentionNotification(testAttentionPendingEvent(
		"approval-empty",
		clientui.AttentionNotificationKindApproval,
		"",
	))
	hooks.OnTranscriptMessage(bellToolStartMessage(1))
	hooks.OnTranscriptMessage(bellToolStartMessage(1))
	hooks.OnTranscriptMessage(bellAssistantFinalMessageWithText(1, " \n\t "))
	hooks.OnTranscriptMessage(bellStepFinishedMessage(1))
	hooks.OnTurnQueueDrained()

	if len(ringer.messages) != 3 {
		t.Fatalf("fallback notification count = %d, want three", len(ringer.messages))
	}
	for _, message := range ringer.messages {
		if strings.TrimSpace(message) == "" || message == title+":" || message == title+": " {
			t.Fatalf("notification lacks structural fallback: %q", message)
		}
		if normalized := strings.Join(strings.Fields(message), " "); normalized != message {
			t.Fatalf("fallback notification = %q, want normalized single line %q", message, normalized)
		}
	}
}

func TestBellHooksCapsCompleteNotificationAtNotifierBoundary(t *testing.T) {
	ringer := &countRinger{}
	title := strings.Repeat("session-", 4)
	hooks := newBellHooks(ringer, func() string { return title }, func() bool { return false })

	hooks.OnTranscriptMessage(bellToolStartMessage(1))
	hooks.OnTranscriptMessage(bellToolStartMessage(1))
	hooks.OnTranscriptMessage(bellAssistantFinalMessageWithText(1, strings.Repeat("answer", 30)))
	hooks.OnTranscriptMessage(bellStepFinishedMessage(1))
	hooks.OnTurnQueueDrained()

	if len(ringer.messages) != 1 {
		t.Fatalf("formatted notification count = %d, want one", len(ringer.messages))
	}
	message := ringer.messages[0]
	if got := len([]rune(message)); got != terminalNotificationPreviewLimit {
		t.Fatalf("formatted notification length = %d, want %d", got, terminalNotificationPreviewLimit)
	}
	composed := terminalNotificationSingleLine(title + ": " + strings.Repeat("answer", 30))
	want := string([]rune(composed)[:terminalNotificationPreviewLimit-3]) + "..."
	if message != want {
		t.Fatalf("formatted notification = %q, want one final truncation %q", message, want)
	}
}

func TestNotificationMarkdownPreviewUsesVisibleSingleLineText(t *testing.T) {
	first := "dynamic-bold"
	label := "dynamic-link"
	destination := "https://example.invalid/dynamic-target"
	last := "dynamic-tail"
	source := "**" + first + "**\n\n[" + label + "](" + destination + ")\t" + last

	preview := notificationMarkdownPreview(
		source,
		transcriptrender.MarkdownLinkLabelAndDestination,
	)

	want := strings.Join([]string{first, label, destination, last}, " ")
	if preview != want {
		t.Fatalf("Markdown preview = %q, want visible single-line text %q", preview, want)
	}
}

func TestNotificationMarkdownPreviewAdaptsLinkPresentation(t *testing.T) {
	label := "dynamic-link-label"
	destination := "https://example.invalid/dynamic-link-destination"
	source := "[" + label + "](" + destination + ")"

	labelOnly := notificationMarkdownPreview(source, transcriptrender.MarkdownLinkLabelOnly)
	withDestination := notificationMarkdownPreview(source, transcriptrender.MarkdownLinkLabelAndDestination)

	if labelOnly != label {
		t.Fatalf("label-only preview = %q, want %q", labelOnly, label)
	}
	if want := label + " " + destination; withDestination != want {
		t.Fatalf("fallback preview = %q, want %q", withDestination, want)
	}
}

func TestNotificationMarkdownPreviewIsBoundedWithoutVisibleTruncation(t *testing.T) {
	preview := notificationMarkdownPreview(
		strings.Repeat("dynamic-preview", terminalNotificationPreviewLimit),
		transcriptrender.MarkdownLinkLabelOnly,
	)

	if got := len([]rune(preview)); got != terminalNotificationPreviewLimit {
		t.Fatalf("retained preview length = %d, want %d", got, terminalNotificationPreviewLimit)
	}
	runes := []rune(preview)
	if last := runes[len(runes)-1]; last == '.' || last == '…' {
		t.Fatalf("retained preview ends with visible truncation rune %q: %q", last, preview)
	}
}

func TestTerminalNotificationFormattingSlicesUnicodeSafely(t *testing.T) {
	title := strings.Repeat("界", 12)
	body := strings.Repeat("λ", terminalNotificationPreviewLimit)

	message := formatTerminalNotificationMessage(title, body)

	if !utf8.ValidString(message) {
		t.Fatalf("formatted notification is invalid UTF-8: %q", message)
	}
	if got := len([]rune(message)); got != terminalNotificationPreviewLimit {
		t.Fatalf("formatted Unicode notification length = %d, want %d", got, terminalNotificationPreviewLimit)
	}
	runes := []rune(message)
	if got := string(runes[len(runes)-3:]); got != "..." {
		t.Fatalf("formatted Unicode notification suffix = %q, want terminal ellipsis", got)
	}
}

func TestBellHooksCompactionUsesCompleteMessageFormatter(t *testing.T) {
	ringer := &countRinger{}
	title := strings.Repeat("dynamic-session-title", 6)
	hooks := newBellHooks(ringer, func() string { return title }, func() bool { return false })

	hooks.OnUserCompactionCompleted(true)

	if len(ringer.messages) != 1 {
		t.Fatalf("compaction notification count = %d, want one", len(ringer.messages))
	}
	if got := len([]rune(ringer.messages[0])); got != terminalNotificationPreviewLimit {
		t.Fatalf("compaction notification length = %d, want %d", got, terminalNotificationPreviewLimit)
	}
	runes := []rune(ringer.messages[0])
	if got := string(runes[len(runes)-3:]); got != "..." {
		t.Fatalf("compaction notification suffix = %q, want terminal ellipsis", got)
	}
}

func TestUIAskLifecycleDoesNotDuplicateAttentionNotifications(t *testing.T) {
	ringer := &countRinger{}
	model := newProjectedStaticUIModel(WithUITurnQueueHook(newUnfocusedBellHooks(ringer)))

	next, _ := model.Update(askEventMsg{event: askEvent{prompt: bellTestPrompt("ask-1", "First?")}})
	model = next.(*uiModel)
	next, _ = model.Update(askEventMsg{event: askEvent{prompt: bellTestPrompt("ask-2", "Second?")}})
	model = next.(*uiModel)
	_, _ = model.Update(askEventMsg{event: askEvent{resolvedToolCallID: "ask-1"}})

	if ringer.total() != 0 {
		t.Fatalf("UI ask lifecycle emitted %d notification events", ringer.total())
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

func TestBellHooksQueuedTurnsUseLatestPreviewAndEarlierEligibility(t *testing.T) {
	ringer := &countRinger{}
	hooks := newUnfocusedBellHooks(ringer)
	latest := "latest observed queued answer"

	recordToolHeavyBellTurn(hooks, 1)
	hooks.OnTranscriptMessage(bellToolStartMessage(2))
	hooks.OnTranscriptMessage(bellAssistantFinalMessageWithText(2, latest))
	hooks.OnTranscriptMessage(bellStepFinishedMessage(2))
	hooks.OnTurnQueueDrained()

	if ringer.notifications != 1 {
		t.Fatalf("queued turns emitted %d notifications, want one", ringer.notifications)
	}
	if got := ringer.messages[0]; got != defaultSessionTitle+": "+latest {
		t.Fatalf("queued notification = %q, want latest observed preview", got)
	}
}

func TestBellHooksDrainUsesOnlyObservedFacts(t *testing.T) {
	ringer := &countRinger{}
	hooks := newUnfocusedBellHooks(ringer)

	hooks.OnTranscriptMessage(bellToolStartMessage(1))
	hooks.OnTranscriptMessage(bellToolStartMessage(1))
	hooks.OnTranscriptMessage(bellAssistantFinalMessage(1))
	hooks.OnTurnQueueDrained()
	if ringer.total() != 0 {
		t.Fatalf("drain emitted %d events before step finish was observed", ringer.total())
	}

	hooks.OnTranscriptMessage(bellStepFinishedMessage(1))
	if ringer.total() != 0 {
		t.Fatalf("late step finish scheduled %d delayed events", ringer.total())
	}
	hooks.OnTurnQueueDrained()
	if ringer.notifications != 1 {
		t.Fatalf("later ordinary drain emitted %d notifications, want one", ringer.notifications)
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

func TestBellHooksBlankFinalDoesNotCreateNotification(t *testing.T) {
	ringer := &countRinger{}
	hooks := newUnfocusedBellHooks(ringer)
	hooks.OnTurnQueueAborted()
	hooks.OnTurnQueueDrained()
	if ringer.total() != 0 {
		t.Fatalf("blank final emitted %d events", ringer.total())
	}
}

func TestBellHooksNoFinalLiveRunClearsEarlierPendingCompletion(t *testing.T) {
	ringer := &countRinger{}
	hooks := newUnfocusedBellHooks(ringer)

	recordToolHeavyBellTurn(hooks, 1)
	hooks.OnTranscriptMessage(bellLiveRunNoFinalMessage())
	hooks.OnTranscriptMessage(bellStepFinishedMessage(2))
	hooks.OnTurnQueueDrained()

	if ringer.total() != 0 {
		t.Fatalf("no-final live run emitted %d stale completion events", ringer.total())
	}
}

func TestBellHooksCorrelateQueuedTurnSteps(t *testing.T) {
	t.Run("mismatched final is ignored", func(t *testing.T) {
		ringer := &countRinger{}
		hooks := newUnfocusedBellHooks(ringer)
		hooks.OnTranscriptMessage(bellToolStartMessage(1))
		hooks.OnTranscriptMessage(bellToolStartMessage(1))
		hooks.OnTranscriptMessage(bellAssistantFinalMessage(2))
		hooks.OnTranscriptMessage(bellStepFinishedMessage(1))
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
	} else if kind == clientui.AttentionNotificationKindInterruptedCurrentNode {
		notification.InterruptedCurrentNode = &clientui.AttentionNotificationInterruptedCurrentNodeState{Message: body}
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

func bellTestPrompt(id, question string) clientui.TranscriptPrompt {
	prompt := ongoingTranscriptMessage(2, clientui.TranscriptMessagePrompt).Payload().(clientui.TranscriptPrompt)
	prompt.ToolCallID = clientui.ToolCallID(id)
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
	return clientui.NewTranscriptMessage(2, clientui.NewTranscriptEvent(clientui.TranscriptToolStart{
		StepID: bellTestStepID(step), ToolCallID: "tool-call", ToolName: "exec_command",
	}))

}

func bellAssistantFinalMessage(step int) clientui.TranscriptMessage {
	return bellAssistantFinalMessageWithText(step, "turn complete")
}

func bellAssistantFinalMessageWithText(step int, text string) clientui.TranscriptMessage {
	return clientui.NewTranscriptMessage(2, clientui.NewTranscriptEvent(clientui.TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityOngoing,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowAssistant,
		Assistant: &clientui.TranscriptAssistantRow{
			StepID: bellTestStepID(step), Text: text, Phase: transcript.AssistantPhaseFinal,
		},
	}))

}

func bellStepFinishedMessage(step int) clientui.TranscriptMessage {
	return clientui.NewTranscriptMessage(2, clientui.NewTranscriptEvent(clientui.TranscriptStepState{
		StepID:    bellTestStepID(step),
		Lifecycle: clientui.StepLifecycleFinished,
	}))

}

func bellAssistantDeltaMessage(step int, delta string) clientui.TranscriptMessage {
	return clientui.NewTranscriptMessage(2, clientui.NewTranscriptEvent(clientui.TranscriptAssistantDelta{
		StepID: bellTestStepID(step), StreamID: runtimeids.NewAssistantStreamID(), Delta: delta, Phase: transcript.AssistantPhaseFinal,
	}))

}

func bellLiveRunNoFinalMessage() clientui.TranscriptMessage {
	startedAt := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	return clientui.NewTranscriptMessage(2, clientui.NewTranscriptEvent(clientui.TranscriptLiveRunResult{
		Status:     clientui.LiveRunStatusCompleted,
		ResultKind: clientui.LiveRunResultNoFinalAnswer,
		StartedAt:  startedAt,
		FinishedAt: startedAt.Add(time.Second),
	}))
}

func recordToolHeavyBellTurn(hooks *bellHooks, step int) {
	hooks.OnTranscriptMessage(bellToolStartMessage(step))
	hooks.OnTranscriptMessage(bellToolStartMessage(step))
	hooks.OnTranscriptMessage(bellAssistantFinalMessage(step))
	hooks.OnTranscriptMessage(bellStepFinishedMessage(step))
}
