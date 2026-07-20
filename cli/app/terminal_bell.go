package app

import (
	"io"
	"os"
	"strings"
	"sync"

	"core/cli/tui/transcriptrender"
	"core/shared/clientui"
	"core/shared/runtimeids"

	xansi "github.com/charmbracelet/x/ansi"
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

type nativeToolObservation uint8

const (
	nativeToolObservationNone nativeToolObservation = iota
	nativeToolObservationOne
	nativeToolObservationMultiple
)

func (o nativeToolObservation) record() nativeToolObservation {
	switch o {
	case nativeToolObservationNone:
		return nativeToolObservationOne
	default:
		return nativeToolObservationMultiple
	}
}

type nativeObservedTurn struct {
	stepID  runtimeids.StepID
	tools   nativeToolObservation
	preview *nativeTurnCompletionPreview
}

type nativeTurnCompletionPreview struct {
	body string
}

type nativePendingDrain struct {
	preview  nativeTurnCompletionPreview
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

type nativeTurnNotificationObserver struct {
	notifier              terminalNotifier
	title                 func() string
	focused               func() bool
	observedTurn          *nativeObservedTurn
	pendingTurnCompletion *nativePendingDrain
	pendingCompaction     bool
	reviewerStep          *runtimeids.StepID
}

type nativeNotificationInput interface {
	nativeNotificationInput()
}

type nativeTurnQueueDrainedInput struct{}

func (nativeTurnQueueDrainedInput) nativeNotificationInput() {}

type nativeTurnQueueAbortedInput struct{}

func (nativeTurnQueueAbortedInput) nativeNotificationInput() {}

type nativeUserCompactionCompletedInput struct {
	queueDrained bool
}

func (nativeUserCompactionCompletedInput) nativeNotificationInput() {}

func newNativeTurnNotificationObserver(notifier terminalNotifier, title func() string, focused ...func() bool) *nativeTurnNotificationObserver {
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
	return &nativeTurnNotificationObserver{notifier: notifier, title: title, focused: focusedProvider}
}

func (h *nativeTurnNotificationObserver) OnAttentionNotification(evt clientui.AttentionNotificationEvent) {
	h.onAttentionNotification(evt, nil)
}

func (h *nativeTurnNotificationObserver) OnAttentionFact(fact attentionFact) {
	if h == nil {
		return
	}
	var kind clientui.AttentionNotificationKind
	switch fact.kind {
	case attentionFactKindQuestion:
		kind = clientui.AttentionNotificationKindQuestion
	case attentionFactKindApproval:
		kind = clientui.AttentionNotificationKindApproval
	default:
		return
	}
	body := notificationMarkdownPreview(
		fact.summary,
		currentTerminalCapabilities().MarkdownLinks,
	)
	h.notifyAttention(kind, body)
}

func (h *nativeTurnNotificationObserver) onAttentionNotification(evt clientui.AttentionNotificationEvent, projectedBody *string) {
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
	body := ""
	if projectedBody != nil {
		body = strings.TrimSpace(*projectedBody)
	} else {
		body = notificationMarkdownPreview(
			attentionNotificationBody(*notification),
			currentTerminalCapabilities().MarkdownLinks,
		)
	}
	h.notifyAttention(notification.Kind, body)
}

func (h *nativeTurnNotificationObserver) notifyAttention(kind clientui.AttentionNotificationKind, body string) {
	if body == "" {
		body = attentionNotificationFallbackBody(kind)
	}
	message := h.formatMessage(attentionNotificationTitle(kind) + ": " + body)
	if h.focusedForAttention() {
		h.notifier.Bell()
		return
	}
	h.notifier.Notify(message)
}

func projectedQuestionNotificationPreview(rows []string) string {
	plainRows := make([]string, 0, len(rows))
	for _, row := range rows {
		plainRows = append(plainRows, xansi.Strip(row))
	}
	normalized := terminalNotificationSingleLine(strings.Join(plainRows, " "))
	runes := []rune(normalized)
	if len(runes) <= terminalNotificationPreviewLimit {
		return normalized
	}
	return string(runes[:terminalNotificationPreviewLimit-3]) + "..."
}

func attentionNotificationTitle(kind clientui.AttentionNotificationKind) string {
	if kind == clientui.AttentionNotificationKindApproval {
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

func attentionNotificationFallbackBody(kind clientui.AttentionNotificationKind) string {
	if kind == clientui.AttentionNotificationKindApproval {
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

func (h *nativeTurnNotificationObserver) recordToolCall(stepID runtimeids.StepID) {
	if h.observedTurn == nil || h.observedTurn.stepID != stepID {
		h.observedTurn = &nativeObservedTurn{stepID: stepID}
	}
	h.observedTurn.tools = h.observedTurn.tools.record()
}

func (h *nativeTurnNotificationObserver) recordTurnCompletion(stepID runtimeids.StepID, assistantContent string) {
	message := turnCompletionNotificationMessage(assistantContent)
	if h.reviewerStep != nil && *h.reviewerStep == stepID {
		return
	}
	if h.observedTurn == nil {
		h.observedTurn = &nativeObservedTurn{stepID: stepID}
	}
	if h.observedTurn.stepID != stepID {
		return
	}
	h.observedTurn.preview = &nativeTurnCompletionPreview{body: message}
}

func (h *nativeTurnNotificationObserver) recordStepFinished(stepID runtimeids.StepID) {
	if h.observedTurn != nil && h.observedTurn.stepID == stepID {
		if h.observedTurn.preview != nil {
			eligible := h.observedTurn.tools == nativeToolObservationMultiple
			if h.pendingTurnCompletion != nil {
				eligible = eligible || h.pendingTurnCompletion.eligible
			}
			h.pendingTurnCompletion = &nativePendingDrain{
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

func (h *nativeTurnNotificationObserver) recordReviewerState(state clientui.TranscriptReviewerState) {
	switch state.State {
	case clientui.ReviewerStateRunning:
		stepID := state.StepID
		h.reviewerStep = &stepID
	case clientui.ReviewerStateCompleted:
		return
	}
}

func (h *nativeTurnNotificationObserver) clearPendingTurnCompletionForSilentFinal(stepID runtimeids.StepID) {
	if h.reviewerStep != nil && *h.reviewerStep == stepID {
		return
	}
	h.pendingTurnCompletion = nil
	if h.observedTurn != nil && h.observedTurn.stepID == stepID {
		h.observedTurn = nil
	}
}

func (h *nativeTurnNotificationObserver) ReduceNativeInput(input nativeNotificationInput) {
	if h == nil {
		return
	}
	switch input := input.(type) {
	case nativeTurnQueueDrainedInput:
		h.reduceTurnQueueDrained()
	case nativeTurnQueueAbortedInput:
		h.reduceTurnQueueAborted()
	case nativeUserCompactionCompletedInput:
		h.reduceUserCompactionCompleted(input.queueDrained)
	}
}

func (h *nativeTurnNotificationObserver) reduceTurnQueueDrained() {
	if h.pendingCompaction {
		h.pendingCompaction = false
		h.pendingTurnCompletion = nil
		h.notifyIfUnfocused(compactionCompletionNotificationMessage)
		return
	}
	if h.pendingTurnCompletion == nil || !h.pendingTurnCompletion.eligible {
		return
	}
	message := h.pendingTurnCompletion.preview.body
	h.pendingTurnCompletion = nil
	h.notifyIfUnfocused(message)
}

func (h *nativeTurnNotificationObserver) reduceTurnQueueAborted() {
	if h == nil {
		return
	}
	h.observedTurn = nil
	h.pendingTurnCompletion = nil
	h.pendingCompaction = false
	h.reviewerStep = nil
}

const compactionCompletionNotificationMessage = "Compaction finished"

func (h *nativeTurnNotificationObserver) reduceUserCompactionCompleted(queueDrained bool) {
	if h == nil {
		return
	}
	if !queueDrained {
		h.pendingCompaction = true
		return
	}
	h.pendingCompaction = false
	h.pendingTurnCompletion = nil
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

func (h *nativeTurnNotificationObserver) formatMessage(message string) string {
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

func (h *nativeTurnNotificationObserver) notifyIfUnfocused(message string) {
	if h == nil {
		return
	}
	if h.focusedForAttention() {
		return
	}
	h.notifier.Notify(h.formatMessage(message))
}

func (h *nativeTurnNotificationObserver) focusedForAttention() bool {
	if h == nil {
		return false
	}
	return h.focused != nil && h.focused()
}
