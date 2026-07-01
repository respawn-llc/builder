package app

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"core/shared/clientui"
)

type countRinger struct {
	mu    sync.Mutex
	count int
	last  string
}

func (r *countRinger) Notify(message string) {
	r.mu.Lock()
	r.count++
	r.last = message
	r.mu.Unlock()
}

func (r *countRinger) Bell() {
	r.mu.Lock()
	r.count++
	r.last = terminalBell
	r.mu.Unlock()
}

func (r *countRinger) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

func (r *countRinger) Last() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

func newUnfocusedBellHooks(ringer *countRinger) *bellHooks {
	return newBellHooks(ringer, nil, func() bool { return false })
}

func TestTerminalBellRingerWritesBellCharacter(t *testing.T) {
	var out bytes.Buffer
	notifier := newTerminalNotifier(notificationMethodBEL, &out, nil)
	notifier.Notify("ignored")

	if got := out.String(); got != terminalBell {
		t.Fatalf("bell output = %q, want %q", got, terminalBell)
	}
}

func TestOSC9TerminalNotifierWritesEscapeSequence(t *testing.T) {
	var out bytes.Buffer
	notifier := newTerminalNotifier(notificationMethodOSC9, &out, nil)
	notifier.Notify("done")

	want := osc9Prefix + "done" + terminalBell + terminalBell
	if got := out.String(); got != want {
		t.Fatalf("osc9 output = %q, want %q", got, want)
	}
}

func TestOSC9TerminalNotifierWritesRawBellWithoutNotification(t *testing.T) {
	var out bytes.Buffer
	notifier := newTerminalNotifier(notificationMethodOSC9, &out, nil)
	notifier.Bell()

	if got := out.String(); got != terminalBell {
		t.Fatalf("bell output = %q, want %q", got, terminalBell)
	}
}

func TestAutoNotifierUsesOSC9ForGhostty(t *testing.T) {
	var out bytes.Buffer
	notifier := newTerminalNotifier(notificationMethodAuto, &out, func(key string) (string, bool) {
		switch key {
		case "TERM_PROGRAM":
			return "ghostty", true
		default:
			return "", false
		}
	})
	notifier.Notify("ping")

	want := osc9Prefix + "ping" + terminalBell + terminalBell
	if got := out.String(); got != want {
		t.Fatalf("auto output = %q, want %q", got, want)
	}
}

func TestAutoNotifierFallsBackToBELForWindowsTerminal(t *testing.T) {
	var out bytes.Buffer
	notifier := newTerminalNotifier(notificationMethodAuto, &out, func(key string) (string, bool) {
		switch key {
		case "TERM_PROGRAM":
			return "ghostty", true
		case "WT_SESSION":
			return "1", true
		default:
			return "", false
		}
	})
	notifier.Notify("ping")

	if got := out.String(); got != terminalBell {
		t.Fatalf("auto output = %q, want %q", got, terminalBell)
	}
}

func TestBellHooksRingOnAskRequests(t *testing.T) {
	ringer := &countRinger{}
	hooks := newUnfocusedBellHooks(ringer)

	hooks.OnAsk(clientui.PendingPromptEvent{Question: "question"})
	hooks.OnAsk(clientui.PendingPromptEvent{Question: "approval", Approval: true})

	if got := ringer.Count(); got != 2 {
		t.Fatalf("ring count = %d, want 2", got)
	}
	if got := ringer.Last(); got != "kent: Action required: approval" {
		t.Fatalf("last message = %q, want %q", got, "kent: Action required: approval")
	}
}

func TestBellHooksUseSessionNameAndQuestionTextForAskNotifications(t *testing.T) {
	ringer := &countRinger{}
	hooks := newBellHooks(ringer, func() string { return "incident triage" })

	hooks.OnAsk(clientui.PendingPromptEvent{Question: "Which rollback strategy should I use?"})

	if got := ringer.Last(); got != "incident triage: Question: Which rollback strategy should I use?" {
		t.Fatalf("last message = %q, want %q", got, "incident triage: Question: Which rollback strategy should I use?")
	}
}

func TestBellHooksAskUsesBellOnlyWhileFocused(t *testing.T) {
	ringer := &countRinger{}
	hooks := newBellHooks(ringer, nil, func() bool { return true })

	hooks.OnAsk(clientui.PendingPromptEvent{Question: "question"})

	if got := ringer.Count(); got != 1 {
		t.Fatalf("ring count while focused = %d, want 1", got)
	}
	if got := ringer.Last(); got != terminalBell {
		t.Fatalf("last message while focused = %q, want raw bell", got)
	}
}

func TestBellHooksAskUsesRawBellOnlyWithOSC9NotifierWhileFocused(t *testing.T) {
	var out bytes.Buffer
	hooks := newBellHooks(newOSC9TerminalNotifier(&out), nil, func() bool { return true })

	hooks.OnAsk(clientui.PendingPromptEvent{Question: "question"})

	if got := out.String(); got != terminalBell {
		t.Fatalf("focused OSC9 ask output = %q, want raw bell", got)
	}
}

func TestUIAskEventNotifiesBellHookForActiveAndQueuedPrompts(t *testing.T) {
	ringer := &countRinger{}
	hooks := newUnfocusedBellHooks(ringer)
	m := newProjectedStaticUIModel(WithUIAskNotificationHook(hooks))

	next, _ := m.Update(askEventMsg{event: askEvent{req: clientui.PendingPromptEvent{PromptID: "ask-1", Question: "First?"}}})
	m = next.(*uiModel)
	next, _ = m.Update(askEventMsg{event: askEvent{req: clientui.PendingPromptEvent{PromptID: "ask-2", Question: "Second?"}}})
	_ = next.(*uiModel)

	if got := ringer.Count(); got != 2 {
		t.Fatalf("ask notification count = %d, want 2", got)
	}
	if got := ringer.Last(); got != "kent: Question: Second?" {
		t.Fatalf("last ask notification = %q, want %q", got, "kent: Question: Second?")
	}
}

func TestUIResolvedAskEventDoesNotNotifyBellHook(t *testing.T) {
	ringer := &countRinger{}
	hooks := newUnfocusedBellHooks(ringer)
	m := newProjectedStaticUIModel(WithUIAskNotificationHook(hooks))

	next, _ := m.Update(askEventMsg{event: askEvent{resolvedPromptID: "ask-1"}})
	_ = next.(*uiModel)

	if got := ringer.Count(); got != 0 {
		t.Fatalf("resolved ask notification count = %d, want 0", got)
	}
}

func TestBellHooksRingOnToolHeavyTurnEnd(t *testing.T) {
	ringer := &countRinger{}
	hooks := newUnfocusedBellHooks(ringer)

	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-1"})
	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventAssistantMessage, StepID: "step-1"})
	if got := ringer.Count(); got != 0 {
		t.Fatalf("ring count = %d after single tool call turn, want 0", got)
	}

	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-2"})
	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-2"})
	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventAssistantMessage, StepID: "step-2"})
	if got := ringer.Count(); got != 0 {
		t.Fatalf("ring count = %d before queue drain, want 0", got)
	}
	hooks.OnTurnQueueDrained()
	if got := ringer.Count(); got != 1 {
		t.Fatalf("ring count = %d after queue drain, want 1", got)
	}
	if got := ringer.Last(); got != "kent: turn complete" {
		t.Fatalf("last message = %q, want %q", got, "kent: turn complete")
	}

	hooks.OnTurnQueueDrained()
	if got := ringer.Count(); got != 1 {
		t.Fatalf("ring count = %d after duplicate queue drain, want 1", got)
	}
}

func TestBellHooksSuppressTurnCompletionWhileFocused(t *testing.T) {
	ringer := &countRinger{}
	hooks := newBellHooks(ringer, nil, func() bool { return true })

	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-1"})
	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-1"})
	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventAssistantMessage, StepID: "step-1", TranscriptEntries: []clientui.ChatEntry{{Role: "assistant", Text: "done"}}})
	hooks.OnTurnQueueDrained()

	if got := ringer.Count(); got != 0 {
		t.Fatalf("ring count while focused = %d, want 0", got)
	}
}

func TestBellHooksRingOnToolHeavyTurnEndWithUnknownFocus(t *testing.T) {
	ringer := &countRinger{}
	focus := newTerminalFocusState()
	hooks := newBellHooks(ringer, nil, focus.FocusedForAttention)

	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-1"})
	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-1"})
	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventAssistantMessage, StepID: "step-1", TranscriptEntries: []clientui.ChatEntry{{Role: "assistant", Text: "done"}}})
	hooks.OnTurnQueueDrained()

	if got := ringer.Count(); got != 1 {
		t.Fatalf("ring count with unknown focus = %d, want 1", got)
	}
}

func TestBellHooksIncludeAssistantPreviewInTurnCompleteNotification(t *testing.T) {
	ringer := &countRinger{}
	hooks := newUnfocusedBellHooks(ringer)

	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-1"})
	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-1"})
	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventAssistantMessage, StepID: "step-1", TranscriptEntries: []clientui.ChatEntry{{Role: "assistant", Text: "  First line\n\nSecond line with details  "}}})
	hooks.OnTurnQueueDrained()

	if got := ringer.Last(); got != "kent: First line Second line with details" {
		t.Fatalf("last message = %q, want %q", got, "kent: First line Second line with details")
	}
}

func TestBellHooksFallbackToTurnCompleteForWhitespacePreview(t *testing.T) {
	ringer := &countRinger{}
	hooks := newUnfocusedBellHooks(ringer)

	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-1"})
	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-1"})
	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventAssistantMessage, StepID: "step-1", TranscriptEntries: []clientui.ChatEntry{{Role: "assistant", Text: "\n\t  "}}})
	hooks.OnTurnQueueDrained()

	if got := ringer.Last(); got != "kent: turn complete" {
		t.Fatalf("last message = %q, want %q", got, "kent: turn complete")
	}
}

func TestBellHooksNoopAssistantDeltaClearsPendingTurnCompletion(t *testing.T) {
	ringer := &countRinger{}
	hooks := newUnfocusedBellHooks(ringer)

	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-1"})
	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-1"})
	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventAssistantMessage, StepID: "step-1", TranscriptEntries: []clientui.ChatEntry{{Role: "assistant", Text: "working"}}})
	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventAssistantDelta, StepID: "step-2", AssistantDelta: uiNoopFinalToken})
	hooks.OnTurnQueueDrained()

	if got := ringer.Count(); got != 0 {
		t.Fatalf("ring count = %d after NO_OP delta drain, want 0", got)
	}
}

func TestBellHooksNoopAssistantMessageClearsPendingTurnCompletion(t *testing.T) {
	ringer := &countRinger{}
	hooks := newUnfocusedBellHooks(ringer)

	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-1"})
	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-1"})
	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventAssistantMessage, StepID: "step-1", TranscriptEntries: []clientui.ChatEntry{{Role: "assistant", Text: "working"}}})
	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventAssistantMessage, StepID: "step-2", TranscriptEntries: []clientui.ChatEntry{{Role: "assistant", Text: "NO_OP"}}})
	hooks.OnTurnQueueDrained()

	if got := ringer.Count(); got != 0 {
		t.Fatalf("ring count = %d after NO_OP assistant message drain, want 0", got)
	}
}

func TestBellHooksNoopAssistantEventPreservesUnrelatedActiveTurn(t *testing.T) {
	ringer := &countRinger{}
	hooks := newUnfocusedBellHooks(ringer)

	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-1"})
	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-1"})
	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventAssistantMessage, StepID: "step-1", TranscriptEntries: []clientui.ChatEntry{{Role: "assistant", Text: "first"}}})
	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-2"})
	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventAssistantDelta, StepID: "step-3", AssistantDelta: uiNoopFinalToken})
	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-2"})
	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventAssistantMessage, StepID: "step-2", TranscriptEntries: []clientui.ChatEntry{{Role: "assistant", Text: "second"}}})
	hooks.OnTurnQueueDrained()

	if got := ringer.Count(); got != 1 {
		t.Fatalf("ring count = %d after unrelated active turn completes, want 1", got)
	}
	if got := ringer.Last(); got != "kent: second" {
		t.Fatalf("last message = %q, want %q", got, "kent: second")
	}
}

func TestFormatAssistantPreview(t *testing.T) {
	if got := formatAssistantPreview("\n  hello\tworld  ", 80); got != "hello world" {
		t.Fatalf("preview = %q, want %q", got, "hello world")
	}

	if got := formatAssistantPreview("", 80); got != "" {
		t.Fatalf("preview = %q, want empty", got)
	}

	if got := formatAssistantPreview("abcdef", 4); got != "abc…" {
		t.Fatalf("preview = %q, want %q", got, "abc…")
	}

	long := strings.Repeat("a", terminalNotificationPreviewLimit+5)
	want := strings.Repeat("a", terminalNotificationPreviewLimit-1) + "…"
	if got := formatAssistantPreview(long, terminalNotificationPreviewLimit); got != want {
		t.Fatalf("preview = %q, want %q", got, want)
	}

	if got := formatAssistantPreview("ab\x1bcd\a ef", 80); got != "abcd ef" {
		t.Fatalf("preview = %q, want %q", got, "abcd ef")
	}
}

func TestBellHooksIgnoresMismatchedTurnEndStep(t *testing.T) {
	ringer := &countRinger{}
	hooks := newUnfocusedBellHooks(ringer)

	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-1"})
	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-1"})
	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventAssistantMessage, StepID: "step-2"})
	hooks.OnTurnQueueDrained()

	if got := ringer.Count(); got != 0 {
		t.Fatalf("ring count = %d, want 0", got)
	}
}

func TestBellHooksRingOnceAfterQueuedTurnsDrain(t *testing.T) {
	ringer := &countRinger{}
	hooks := newUnfocusedBellHooks(ringer)

	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-1"})
	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-1"})
	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventAssistantMessage, StepID: "step-1", TranscriptEntries: []clientui.ChatEntry{{Role: "assistant", Text: "first"}}})
	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-2"})
	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-2"})
	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventAssistantMessage, StepID: "step-2", TranscriptEntries: []clientui.ChatEntry{{Role: "assistant", Text: "second"}}})

	if got := ringer.Count(); got != 0 {
		t.Fatalf("ring count = %d before queue drain, want 0", got)
	}
	hooks.OnTurnQueueDrained()
	if got := ringer.Count(); got != 1 {
		t.Fatalf("ring count = %d after queue drain, want 1", got)
	}
	if got := ringer.Last(); got != "kent: second" {
		t.Fatalf("last message = %q, want %q", got, "kent: second")
	}
}

func TestBellHooksClearPendingTurnCompletionAfterAbort(t *testing.T) {
	ringer := &countRinger{}
	hooks := newUnfocusedBellHooks(ringer)

	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-1"})
	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventToolCallStarted, StepID: "step-1"})
	hooks.OnProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventAssistantMessage, StepID: "step-1", TranscriptEntries: []clientui.ChatEntry{{Role: "assistant", Text: "first"}}})
	hooks.OnTurnQueueAborted()
	hooks.OnTurnQueueDrained()

	if got := ringer.Count(); got != 0 {
		t.Fatalf("ring count = %d after aborted queue, want 0", got)
	}
}

func TestBellHooksRingOnUserCompactionCompletion(t *testing.T) {
	ringer := &countRinger{}
	hooks := newUnfocusedBellHooks(ringer)

	hooks.OnUserCompactionCompleted(true)

	if got := ringer.Count(); got != 1 {
		t.Fatalf("ring count = %d after user compaction completion, want 1", got)
	}
	if got := ringer.Last(); got != "kent: Compaction finished" {
		t.Fatalf("last message = %q, want %q", got, "kent: Compaction finished")
	}
}

func TestBellHooksSuppressUserCompactionCompletionWhileFocused(t *testing.T) {
	ringer := &countRinger{}
	hooks := newBellHooks(ringer, nil, func() bool { return true })

	hooks.OnUserCompactionCompleted(true)

	if got := ringer.Count(); got != 0 {
		t.Fatalf("ring count while focused = %d, want 0", got)
	}
}

func TestBellHooksDeferUserCompactionCompletionUntilQueueDrains(t *testing.T) {
	ringer := &countRinger{}
	hooks := newUnfocusedBellHooks(ringer)

	hooks.OnUserCompactionCompleted(false)
	if got := ringer.Count(); got != 0 {
		t.Fatalf("ring count before queue drain = %d, want 0", got)
	}
	hooks.OnTurnQueueDrained()

	if got := ringer.Count(); got != 1 {
		t.Fatalf("ring count after queue drain = %d, want 1", got)
	}
	if got := ringer.Last(); got != "kent: Compaction finished" {
		t.Fatalf("last message = %q, want %q", got, "kent: Compaction finished")
	}
}
