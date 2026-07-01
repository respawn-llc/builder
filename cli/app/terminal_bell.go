package app

import (
	"io"
	"os"
	"strings"
	"sync"

	"core/shared/clientui"
)

const terminalBell = "\a"
const osc9Prefix = "\x1b]9;"
const terminalNotificationPreviewLimit = 80

const (
	notificationMethodAuto = "auto"
	notificationMethodOSC9 = "osc9"
	notificationMethodBEL  = "bel"
)

type terminalNotifier interface {
	Bell()
	Notify(message string)
}

type belTerminalNotifier struct {
	mu  sync.Mutex
	out io.Writer
}

type osc9TerminalNotifier struct {
	mu  sync.Mutex
	out io.Writer
}

func newBELTerminalNotifier(out io.Writer) *belTerminalNotifier {
	if out == nil {
		out = io.Discard
	}
	return &belTerminalNotifier{out: out}
}

func newOSC9TerminalNotifier(out io.Writer) *osc9TerminalNotifier {
	if out == nil {
		out = io.Discard
	}
	return &osc9TerminalNotifier{out: out}
}

func newTerminalNotifier(method string, out io.Writer, lookup func(string) (string, bool)) terminalNotifier {
	normalized := strings.ToLower(strings.TrimSpace(method))
	if normalized == "" {
		normalized = notificationMethodAuto
	}
	switch normalized {
	case notificationMethodOSC9:
		return newOSC9TerminalNotifier(out)
	case notificationMethodBEL:
		return newBELTerminalNotifier(out)
	default:
		if supportsOSC9(lookup) {
			return newOSC9TerminalNotifier(out)
		}
		return newBELTerminalNotifier(out)
	}
}

func supportsOSC9(lookup func(string) (string, bool)) bool {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	if _, ok := lookup("WT_SESSION"); ok {
		return false
	}
	if termProgram, ok := lookup("TERM_PROGRAM"); ok {
		switch termProgram {
		case "WezTerm", "ghostty":
			return true
		}
	}
	if _, ok := lookup("ITERM_SESSION_ID"); ok {
		return true
	}
	if term, ok := lookup("TERM"); ok {
		switch term {
		case "xterm-kitty", "wezterm", "wezterm-mux":
			return true
		}
	}
	return false
}

func sanitizeTerminalNotificationMessage(message string) string {
	message = strings.ReplaceAll(message, "\x1b", "")
	message = strings.ReplaceAll(message, terminalBell, "")
	return message
}

func formatAssistantPreview(content string, maxChars int) string {
	normalized := strings.Join(strings.Fields(sanitizeTerminalNotificationMessage(content)), " ")
	trimmed := strings.TrimSpace(normalized)
	if trimmed == "" {
		return ""
	}
	if maxChars <= 0 {
		return trimmed
	}
	runes := []rune(trimmed)
	if len(runes) <= maxChars {
		return trimmed
	}
	if maxChars == 1 {
		return "…"
	}
	return string(runes[:maxChars-1]) + "…"
}

func (r *belTerminalNotifier) Notify(_ string) {
	r.Bell()
}

func (r *belTerminalNotifier) Bell() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, _ = io.WriteString(r.out, terminalBell)
}

func (r *osc9TerminalNotifier) Notify(message string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// The first BEL terminates the OSC 9 sequence. Emit a second BEL so asks and
	// turn-complete notifications still produce an audible bell on OSC-capable terminals.
	_, _ = io.WriteString(r.out, osc9Prefix+sanitizeTerminalNotificationMessage(message)+terminalBell+terminalBell)
}

func (r *osc9TerminalNotifier) Bell() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, _ = io.WriteString(r.out, terminalBell)
}

type bellHooks struct {
	mu                    sync.Mutex
	notifier              terminalNotifier
	title                 func() string
	focused               func() bool
	currentStep           string
	toolCalls             int
	pendingTurnCompletion bool
	pendingCompaction     bool
	lastCompletionMessage string
}

func newBellHooks(notifier terminalNotifier, title func() string, focused ...func() bool) *bellHooks {
	if notifier == nil {
		notifier = newBELTerminalNotifier(io.Discard)
	}
	if title == nil {
		title = func() string { return defaultSessionTitle }
	}
	focusedProvider := func() bool { return false }
	if len(focused) > 0 && focused[0] != nil {
		focusedProvider = focused[0]
	}
	return &bellHooks{notifier: notifier, title: title, focused: focusedProvider}
}

func (h *bellHooks) OnAsk(req clientui.PendingPromptEvent) {
	if h == nil {
		return
	}
	question := formatAssistantPreview(req.Question, terminalNotificationPreviewLimit)
	if question == "" {
		if req.Approval {
			question = "action required"
		} else {
			question = "question from agent"
		}
	}
	label := "Question"
	if req.Approval {
		label = "Action required"
	}
	message := h.formatMessage(label + ": " + question)
	if h.focusedForAttention() {
		h.notifier.Bell()
		return
	}
	h.notifier.Notify(message)
}

func (h *bellHooks) OnProjectedRuntimeEvent(evt clientui.Event) {
	if isNoopProjectedAssistantEvent(evt) {
		h.clearPendingTurnCompletionForSilentFinal(evt.StepID)
		return
	}
	switch evt.Kind {
	case clientui.EventToolCallStarted:
		h.recordToolCall(evt.StepID)
	case clientui.EventAssistantMessage:
		h.recordTurnCompletion(evt.StepID, projectedAssistantMessageContent(evt.TranscriptEntries))
	}
}

func (h *bellHooks) recordToolCall(stepID string) {
	stepID = strings.TrimSpace(stepID)
	if stepID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.currentStep != stepID {
		h.currentStep = stepID
		h.toolCalls = 0
	}
	h.toolCalls++
}

func (h *bellHooks) recordTurnCompletion(stepID, assistantContent string) {
	stepID = strings.TrimSpace(stepID)
	message := turnCompletionNotificationMessage(assistantContent)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastCompletionMessage = message
	if stepID == "" || h.currentStep != stepID {
		return
	}
	if h.toolCalls >= 2 {
		h.pendingTurnCompletion = true
	}
	h.currentStep = ""
	h.toolCalls = 0
}

func (h *bellHooks) clearPendingTurnCompletionForSilentFinal(stepID string) {
	stepID = strings.TrimSpace(stepID)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pendingTurnCompletion = false
	h.lastCompletionMessage = ""
	if stepID != "" && h.currentStep == stepID {
		h.currentStep = ""
		h.toolCalls = 0
	}
}

func (h *bellHooks) OnTurnQueueDrained() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.pendingCompaction {
		h.pendingCompaction = false
		h.pendingTurnCompletion = false
		h.lastCompletionMessage = ""
		h.mu.Unlock()
		h.notifyIfUnfocused(compactionCompletionNotificationMessage)
		return
	}
	if !h.pendingTurnCompletion {
		h.mu.Unlock()
		return
	}
	message := h.lastCompletionMessage
	h.pendingTurnCompletion = false
	h.lastCompletionMessage = ""
	h.mu.Unlock()
	h.notifyIfUnfocused(message)
}

func (h *bellHooks) OnTurnQueueAborted() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.currentStep = ""
	h.toolCalls = 0
	h.pendingTurnCompletion = false
	h.pendingCompaction = false
	h.lastCompletionMessage = ""
	h.mu.Unlock()
}

const compactionCompletionNotificationMessage = "Compaction finished"

func (h *bellHooks) OnUserCompactionCompleted(queueDrained bool) {
	if h == nil {
		return
	}
	h.mu.Lock()
	if !queueDrained {
		h.pendingCompaction = true
		h.mu.Unlock()
		return
	}
	h.pendingCompaction = false
	h.pendingTurnCompletion = false
	h.lastCompletionMessage = ""
	h.mu.Unlock()
	h.notifyIfUnfocused(compactionCompletionNotificationMessage)
}

func turnCompletionNotificationMessage(assistantContent string) string {
	if preview := formatAssistantPreview(assistantContent, terminalNotificationPreviewLimit); preview != "" {
		return preview
	}
	return "turn complete"
}

func projectedAssistantMessageContent(entries []clientui.ChatEntry) string {
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Role != "assistant" {
			continue
		}
		return entries[i].Text
	}
	return ""
}

func (h *bellHooks) formatMessage(message string) string {
	title := defaultSessionTitle
	if h != nil && h.title != nil {
		title = sessionTitle(h.title())
	}
	return title + ": " + sanitizeTerminalNotificationMessage(message)
}

func (h *bellHooks) notifyIfUnfocused(message string) {
	if h == nil {
		return
	}
	if h.focusedForAttention() {
		return
	}
	h.notifier.Notify(h.formatMessage(message))
}

func (h *bellHooks) focusedForAttention() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	focused := false
	if h.focused != nil {
		focused = h.focused()
	}
	h.mu.Unlock()
	return focused
}
