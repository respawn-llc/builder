package app

import (
	"io"
	"os"
	"strings"
	"sync"

	"core/cli/tui/transcriptrender"
	"core/shared/clientui"
	"core/shared/runtimeids"
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

type observedNotificationTurn struct {
	stepID    runtimeids.StepID
	toolCalls int
	preview   *turnCompletionPreview
}

type turnCompletionPreview struct {
	stepID runtimeids.StepID
	body   string
}

type queuedTurnCompletion struct {
	preview  turnCompletionPreview
	eligible bool
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
	_, _ = io.WriteString(r.out, osc9Prefix+message+terminalBell+terminalBell)
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
	observedTurn          *observedNotificationTurn
	pendingTurnCompletion *queuedTurnCompletion
	pendingCompaction     bool
	reviewerStep          *runtimeids.StepID
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

func (h *bellHooks) OnAttentionNotification(evt clientui.AttentionNotificationEvent) {
	if h == nil {
		return
	}
	if evt.Type != clientui.AttentionNotificationEventPending || evt.Pending == nil {
		return
	}
	notification := evt.Pending
	if !tuiSupportsAttentionNotification(*notification) {
		return
	}
	body := notificationMarkdownPreview(
		attentionNotificationBody(*notification),
		currentTerminalCapabilities().MarkdownLinks,
	)
	if body == "" {
		body = attentionNotificationFallbackBody(*notification)
	}
	message := h.formatMessage(attentionNotificationTitle(*notification) + ": " + body)
	if h.focusedForAttention() {
		h.notifier.Bell()
		return
	}
	h.notifier.Notify(message)
}

func attentionNotificationTitle(notification clientui.AttentionNotification) string {
	if notification.Kind == clientui.AttentionNotificationKindApproval {
		return "Action required"
	}
	return "Question"
}

func attentionNotificationBody(notification clientui.AttentionNotification) string {
	for _, candidate := range []string{
		attentionNotificationQuestionPreview(notification),
		attentionNotificationApprovalMessage(notification),
	} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func attentionNotificationFallbackBody(notification clientui.AttentionNotification) string {
	if notification.Kind == clientui.AttentionNotificationKindApproval {
		return "action required"
	}
	return "question from agent"
}

func attentionNotificationQuestionPreview(notification clientui.AttentionNotification) string {
	if notification.Question == nil {
		return ""
	}
	return notification.Question.Preview
}

func attentionNotificationApprovalMessage(notification clientui.AttentionNotification) string {
	if notification.Approval == nil {
		return ""
	}
	return notification.Approval.Message
}

func (h *bellHooks) recordToolCall(stepID runtimeids.StepID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.observedTurn == nil || h.observedTurn.stepID != stepID {
		h.observedTurn = &observedNotificationTurn{stepID: stepID}
	}
	h.observedTurn.toolCalls++
}

func (h *bellHooks) recordTurnCompletion(stepID runtimeids.StepID, assistantContent string) {
	message := turnCompletionNotificationMessage(assistantContent)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.reviewerStep != nil && *h.reviewerStep == stepID {
		return
	}
	if h.observedTurn == nil {
		h.observedTurn = &observedNotificationTurn{stepID: stepID}
	}
	if h.observedTurn.stepID != stepID {
		return
	}
	h.observedTurn.preview = &turnCompletionPreview{stepID: stepID, body: message}
}

func (h *bellHooks) recordStepFinished(stepID runtimeids.StepID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.observedTurn != nil && h.observedTurn.stepID == stepID {
		if h.observedTurn.preview != nil {
			eligible := h.observedTurn.toolCalls >= 2
			if h.pendingTurnCompletion != nil {
				eligible = eligible || h.pendingTurnCompletion.eligible
			}
			h.pendingTurnCompletion = &queuedTurnCompletion{
				preview:  *h.observedTurn.preview,
				eligible: eligible,
			}
		}
		h.observedTurn = nil
	}
	if h.reviewerStep != nil && *h.reviewerStep == stepID {
		h.reviewerStep = nil
	}
}

func (h *bellHooks) recordReviewerState(state clientui.TranscriptReviewerState) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch state.State {
	case clientui.ReviewerStateRunning:
		stepID := state.StepID
		h.reviewerStep = &stepID
	case clientui.ReviewerStateCompleted:
		return
	}
}

func (h *bellHooks) clearPendingTurnCompletionForSilentFinal(stepID runtimeids.StepID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.reviewerStep != nil && *h.reviewerStep == stepID {
		return
	}
	h.pendingTurnCompletion = nil
	if h.observedTurn != nil && h.observedTurn.stepID == stepID {
		h.observedTurn = nil
	}
}

func (h *bellHooks) OnTurnQueueDrained() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.pendingCompaction {
		h.pendingCompaction = false
		h.pendingTurnCompletion = nil
		h.mu.Unlock()
		h.notifyIfUnfocused(compactionCompletionNotificationMessage)
		return
	}
	if h.pendingTurnCompletion == nil || !h.pendingTurnCompletion.eligible {
		h.mu.Unlock()
		return
	}
	message := h.pendingTurnCompletion.preview.body
	h.pendingTurnCompletion = nil
	h.mu.Unlock()
	h.notifyIfUnfocused(message)
}

func (h *bellHooks) OnTurnQueueAborted() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.observedTurn = nil
	h.pendingTurnCompletion = nil
	h.pendingCompaction = false
	h.reviewerStep = nil
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
	h.pendingTurnCompletion = nil
	h.mu.Unlock()
	h.notifyIfUnfocused(compactionCompletionNotificationMessage)
}

func turnCompletionNotificationMessage(assistantContent string) string {
	if preview := notificationMarkdownPreview(
		assistantContent,
		currentTerminalCapabilities().MarkdownLinks,
	); preview != "" {
		return preview
	}
	return "turn complete"
}

func notificationMarkdownPreview(
	content string,
	linkPresentation transcriptrender.MarkdownLinkPresentation,
) string {
	lines := transcriptrender.RenderMarkdownStableLinesWithLinkPresentation(
		transcriptrender.StyleRoleAssistant,
		content,
		terminalNotificationPreviewLimit,
		linkPresentation,
	)
	plain := strings.Join(transcriptrender.PlainLines(lines), " ")
	normalized := terminalNotificationSingleLine(plain)
	runes := []rune(normalized)
	if len(runes) <= terminalNotificationPreviewLimit {
		return normalized
	}
	return string(runes[:terminalNotificationPreviewLimit])
}

func (h *bellHooks) formatMessage(message string) string {
	title := defaultSessionTitle
	if h != nil && h.title != nil {
		title = sessionTitle(h.title())
	}
	return formatTerminalNotificationMessage(title, message)
}

func formatTerminalNotificationMessage(title, message string) string {
	composed := terminalNotificationSingleLine(sessionTitle(title) + ": " + message)
	runes := []rune(composed)
	if len(runes) <= terminalNotificationPreviewLimit {
		return composed
	}
	return string(runes[:terminalNotificationPreviewLimit-3]) + "..."
}

func terminalNotificationSingleLine(text string) string {
	return strings.Join(
		strings.Fields(transcriptrender.TerminalSafeSingleLine(text)),
		" ",
	)
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
	focusedProvider := h.focused
	h.mu.Unlock()
	return focusedProvider != nil && focusedProvider()
}
