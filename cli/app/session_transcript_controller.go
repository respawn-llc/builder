package app

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"core/cli/tui/ongoing"
	"core/cli/tui/transcriptrender"
	"core/shared/clientui"
	"core/shared/transcript"

	tea "github.com/charmbracelet/bubbletea"
)

const ongoingTranscriptQueueLimit = 1000

type ongoingTranscriptSurface interface {
	ApplyTerminalMessage(clientui.TranscriptMessage, ongoing.FrameInput) (ongoing.Result, error)
	Render(ongoing.FrameInput) (ongoing.Result, error)
	Resize(ongoing.Size, ongoing.FrameInput) (ongoing.Result, error)
}

type ongoingFrameProvider func() ongoing.FrameInput

type ongoingTranscriptRuntimeAdmission func(clientui.TranscriptMessage) (runtimeTupleMergeResult, error)

type ongoingTranscriptAdmittedStateObserver func(clientui.TranscriptMessage, runtimeTupleMergeResult) tea.Cmd

type ongoingTranscriptController struct {
	surface          ongoingTranscriptSurface
	frameProvider    ongoingFrameProvider
	runtimeAdmission ongoingTranscriptRuntimeAdmission
	stateObserver    ongoingTranscriptAdmittedStateObserver
	normalOwned      bool
	hydrated         bool
	lastSequence     uint64
	queue            []clientui.TranscriptMessage
	queueOverflowed  bool
	renderPending    bool
	liveReadModel    ongoingTranscriptReadModel
}

func newOngoingTranscriptController(
	surface ongoingTranscriptSurface,
	frameProvider ongoingFrameProvider,
	runtimeAdmission ongoingTranscriptRuntimeAdmission,
	stateObserver ongoingTranscriptAdmittedStateObserver,
) *ongoingTranscriptController {
	if frameProvider == nil {
		panic("ongoing transcript controller requires frame provider")
	}
	if runtimeAdmission == nil {
		panic("ongoing transcript controller requires runtime admission")
	}
	if stateObserver == nil {
		panic("ongoing transcript controller requires state observer")
	}
	return &ongoingTranscriptController{
		surface:          surface,
		frameProvider:    frameProvider,
		runtimeAdmission: runtimeAdmission,
		stateObserver:    stateObserver,
		normalOwned:      true,
		liveReadModel:    newOngoingTranscriptReadModel(),
	}
}

func (c *ongoingTranscriptController) Accept(message clientui.TranscriptMessage) (ongoing.Result, tea.Cmd, error) {
	if err := message.Validate(); err != nil {
		return ongoing.Result{}, nil, fmt.Errorf("validate ongoing transcript message: %w", err)
	}
	if result, ok := c.classifyDelivery(message); ok {
		c.requestScratchRehydration()
		return result, nil, nil
	}
	admission, err := c.runtimeAdmission(message)
	if err != nil {
		return ongoing.Result{}, nil, c.diagnoseRuntimeAdmissionError(message, err)
	}
	c.commitDelivery(message)
	stateChanged := c.applyState(message)
	stateCmd := c.stateObserver(message, admission)
	if !c.normalOwned {
		if !isAppOwnedOngoingMessage(message.Kind()) {
			c.enqueue(message)
		}
		c.renderPending = c.renderPending || stateChanged
		return ongoing.Result{}, stateCmd, nil
	}
	result, err := c.applyNow(message, stateChanged)
	return result, stateCmd, err
}

func (c *ongoingTranscriptController) diagnoseRuntimeAdmissionError(
	message clientui.TranscriptMessage,
	err error,
) error {
	var conflict hydrationRuntimeTupleConflictError
	if !errors.As(err, &conflict) {
		return err
	}
	frame := c.frameProvider()
	facts := conflict.facts()
	facts["terminal_size"] = frame.Size
	facts["terminal_cursor"] = frame.Cursor
	hydration, _ := message.Payload().(clientui.TranscriptHydration)
	facts["quoted_payload"] = strconv.Quote(fmt.Sprintf("%+v", hydration))
	return ongoing.NewDeveloperError(
		"admit_transcript_hydration_runtime_tuple",
		conflict.Error(),
		facts,
	)
}

func (c *ongoingTranscriptController) SetNormalBufferOwned(owned bool) (ongoing.Result, error) {
	if c.normalOwned == owned {
		return ongoing.Result{}, nil
	}
	c.normalOwned = owned
	if !owned {
		return ongoing.Result{}, nil
	}
	if c.queueOverflowed {
		c.queue = nil
		c.queueOverflowed = false
		c.renderPending = false
		return ongoing.Result{Action: ongoing.ResultRequestScratchRehydration, Reason: ongoing.RehydrateReasonQueueOverflow}, nil
	}
	return c.drainQueued()
}

func (c *ongoingTranscriptController) ResetForScratchHydration() {
	c.queue = nil
	c.queueOverflowed = false
	c.hydrated = false
	c.lastSequence = 0
	c.renderPending = false
	c.liveReadModel.reset()
}

func (c *ongoingTranscriptController) Render() (ongoing.Result, error) {
	if c == nil || c.surface == nil {
		return ongoing.Result{}, nil
	}
	return c.surface.Render(c.frameInput())
}

func (c *ongoingTranscriptController) Resize(size ongoing.Size) (ongoing.Result, error) {
	if c == nil || c.surface == nil {
		return ongoing.Result{}, nil
	}
	return c.surface.Resize(size, c.frameInput())
}

func (c *ongoingTranscriptController) HandleSubscriptionLoss() ongoing.Result {
	c.requestScratchRehydration()
	return ongoing.Result{Action: ongoing.ResultRequestScratchRehydration, Reason: ongoing.RehydrateReasonSequenceGap}
}

func (c *ongoingTranscriptController) classifyDelivery(message clientui.TranscriptMessage) (ongoing.Result, bool) {
	if !c.hydrated {
		if message.Sequence != 1 || message.Kind() != clientui.TranscriptMessageHydration {
			return ongoing.Result{Action: ongoing.ResultRequestScratchRehydration, Reason: ongoing.RehydrateReasonSequenceGap}, true
		}
		return ongoing.Result{}, false
	}
	if message.Sequence != c.lastSequence+1 {
		return ongoing.Result{Action: ongoing.ResultRequestScratchRehydration, Reason: ongoing.RehydrateReasonSequenceGap}, true
	}
	if message.Kind() == clientui.TranscriptMessageHydration {
		return ongoing.Result{Action: ongoing.ResultRequestScratchRehydration, Reason: ongoing.RehydrateReasonSequenceGap}, true
	}
	return ongoing.Result{}, false
}

func (c *ongoingTranscriptController) commitDelivery(message clientui.TranscriptMessage) {
	if !c.hydrated {
		c.hydrated = true
	}
	c.lastSequence = message.Sequence
}

func (c *ongoingTranscriptController) acceptedHydration(message clientui.TranscriptMessage) bool {
	return message.Kind() == clientui.TranscriptMessageHydration &&
		c.hydrated &&
		c.lastSequence == message.Sequence
}

func (c *ongoingTranscriptController) requestScratchRehydration() {
	c.queue = nil
	c.queueOverflowed = true
	c.hydrated = false
	c.lastSequence = 0
	c.renderPending = false
	c.liveReadModel.reset()
}

func (c *ongoingTranscriptController) enqueue(message clientui.TranscriptMessage) {
	if c.queueOverflowed {
		return
	}
	if len(c.queue) >= ongoingTranscriptQueueLimit {
		c.queue = nil
		c.queueOverflowed = true
		return
	}
	c.queue = append(c.queue, message)
}

func (c *ongoingTranscriptController) drainQueued() (ongoing.Result, error) {
	queued := c.queue
	c.queue = nil
	needsRender := c.renderPending
	c.renderPending = false
	for _, message := range queued {
		if _, err := c.surface.ApplyTerminalMessage(message, c.frameInput()); err != nil {
			return ongoing.Result{}, err
		}
	}
	if needsRender {
		return c.surface.Render(c.frameInput())
	}
	return ongoing.Result{}, nil
}

func (c *ongoingTranscriptController) applyNow(message clientui.TranscriptMessage, stateChanged bool) (ongoing.Result, error) {
	if isAppOwnedOngoingMessage(message.Kind()) {
		if stateChanged {
			return c.surface.Render(c.frameInput())
		}
		return ongoing.Result{}, nil
	}
	if message.Kind() == clientui.TranscriptMessageHydration {
		hydration := message.Payload().(clientui.TranscriptHydration)
		result, err := c.surface.ApplyTerminalMessage(message, c.frameInput())
		if err != nil {
			return ongoing.Result{}, err
		}
		if stateChanged && hydrationHasNoTerminalRows(&hydration) {
			return c.surface.Render(c.frameInput())
		}
		return result, nil
	}
	return c.surface.ApplyTerminalMessage(message, c.frameInput())
}

func (c *ongoingTranscriptController) applyState(message clientui.TranscriptMessage) bool {
	if message.Kind() == clientui.TranscriptMessageHydration {
		hydration := message.Payload().(clientui.TranscriptHydration)
		return c.applyHydrationAppOwnedFacts(&hydration)
	}
	if message.Kind() == clientui.TranscriptMessageCommittedRow {
		row := message.Payload().(clientui.TranscriptCommittedRow)
		if row.Tool != nil {
			c.liveReadModel.removePendingTool(string(row.Tool.ToolCallID))
			return true
		}
	}
	if !isAppOwnedOngoingMessage(message.Kind()) {
		return false
	}
	return c.applyAppOwnedMessage(message)
}

func (c *ongoingTranscriptController) applyAppOwnedMessage(message clientui.TranscriptMessage) bool {
	switch message.Kind() {
	case clientui.TranscriptMessageStepState,
		clientui.TranscriptMessageRuntimeReadModelUpdate:
		// Canonical runtime state is represented by the app status line.
	case clientui.TranscriptMessageUserMessageFlushed:
		// The state observer owns input reconciliation. Re-render the client-local
		// queue after it removes the flushed operation identities.
	case clientui.TranscriptMessageQueuedMessageState:
		// Queue lifecycle is reconciled by the state observer. Pending Work is
		// the sole server membership projection.
	case clientui.TranscriptMessagePendingWorkChanged:
		// The hydration-scoped Pending Work refresh owner handles invalidation.
	case clientui.TranscriptMessagePendingWorkRestored:
		// Composer restoration is owned by the transcript state observer.
		return false
	case clientui.TranscriptMessageSessionSettingFeedback:
		// Typed setting feedback is owned by the app transient-status surface.
		return false
	case clientui.TranscriptMessageSessionStatus:
		// Session status is already represented by the app status line.
	case clientui.TranscriptMessageSessionIdentity:
		// Session identity is already represented by the app status line.
	case clientui.TranscriptMessageCompactionStatus:
		// Compaction status is already represented by the app status line.
	case clientui.TranscriptMessagePrompt:
		payload := message.Payload().(clientui.TranscriptPrompt)
		c.liveReadModel.applyPendingPrompt(&payload)
		return true
	case clientui.TranscriptMessageContextUsage:
		// Context usage is already represented by the app status line.
	case clientui.TranscriptMessageGoalStatus:
		// Goal status is already represented by the app status line.
	case clientui.TranscriptMessageBackgroundActivity:
		// Backgrounding ends the mutable foreground tool lifecycle. The immutable
		// tool row and later completion notice own its ongoing presentation.
	case clientui.TranscriptMessageToolStart:
		payload := message.Payload().(clientui.TranscriptToolStart)
		c.liveReadModel.addPendingTool(payload)
		return true
	case clientui.TranscriptMessageToolAbort:
		payload := message.Payload().(clientui.TranscriptToolAbort)
		c.liveReadModel.removePendingTool(string(payload.ToolCallID))
		return true
	default:
		panic(fmt.Sprintf("unsupported app-owned transcript message kind %q", message.Kind()))
	}
	return true
}

func (c *ongoingTranscriptController) applyHydrationAppOwnedFacts(hydration *clientui.TranscriptHydration) bool {
	if hydration == nil {
		return false
	}
	changed := false
	if len(hydration.InFlightTools) > 0 {
		for _, tool := range hydration.InFlightTools {
			c.liveReadModel.addPendingTool(tool)
		}
		changed = true
	}
	if len(hydration.PendingPrompts) > 0 {
		for _, prompt := range hydration.PendingPrompts {
			c.liveReadModel.applyPendingPrompt(&prompt)
		}
		changed = true
	}
	return changed
}

func hydrationHasNoTerminalRows(hydration *clientui.TranscriptHydration) bool {
	if hydration == nil {
		return true
	}
	if hydration.ActiveAssistant != nil {
		return false
	}
	for _, row := range hydration.CommittedRows {
		switch transcript.NormalizeEntryVisibility(transcript.EntryVisibility(row.Visibility)) {
		case transcript.EntryVisibilityOngoing, transcript.EntryVisibilityOngoingCollapsed:
			return false
		}
	}
	return true
}

func (c *ongoingTranscriptController) frameInput() ongoing.FrameInput {
	frame := c.frameProvider()
	c.liveReadModel.refreshPendingToolSection(frame.Size.Width, frame.SpinnerFrame, frame.Theme)
	c.liveReadModel.refreshPendingPromptSection(frame.Size.Width)
	cursorSectionRow, cursorTargetsInput := ongoingFrameInputCursorSectionRow(frame)
	sections := make([]ongoing.FrameSection, 0, len(c.liveReadModel.sectionOrder))
	for _, kind := range c.liveReadModel.sectionOrder {
		sections = append(sections, c.liveReadModel.sections[kind])
	}
	frame.Sections = append(sections, frame.Sections...)
	if cursorTargetsInput {
		frame.Cursor.Row = ongoingFrameInputCursorTerminalRow(frame, cursorSectionRow)
	}
	return frame
}

func queuedOrSteeredText(state *clientui.TranscriptQueuedMessageState) string {
	if state == nil {
		return ""
	}
	if state.Text != nil && strings.TrimSpace(*state.Text) != "" {
		return *state.Text
	}
	failureReason := ""
	if state.FailureReason != nil {
		failureReason = string(*state.FailureReason)
	}
	lines := joinFacts(compactNonEmptyStrings(humanizeTranscriptFact(string(state.Status)), humanizeTranscriptFact(failureReason)))
	if len(lines) == 0 {
		return ""
	}
	return lines[0]
}

func pendingPromptLines(prompt *clientui.TranscriptPrompt) []string {
	if prompt == nil || prompt.Status != clientui.TranscriptPromptStatusPending {
		return nil
	}
	return joinFacts(compactNonEmptyStrings(prompt.Question))
}

func pendingPromptListLines(prompts []clientui.TranscriptPrompt) []string {
	lines := make([]string, 0, len(prompts))
	for _, prompt := range prompts {
		lines = append(lines, pendingPromptLines(&prompt)...)
	}
	return lines
}

func compactNonEmptyStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func joinFacts(parts []string) []string {
	if len(parts) == 0 {
		return nil
	}
	return []string{strings.Join(parts, " · ")}
}

func terminalSafeFrameLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if safe := ongoing.TerminalSafeSingleLine(line); safe != "" {
			out = append(out, safe)
		}
	}
	return out
}

func terminalSafeFrameLinesForWidth(lines []string, width int) []string {
	if width <= 0 {
		width = 80
	}
	out := make([]string, 0, len(lines))
	for _, line := range terminalSafeFrameLines(lines) {
		truncated := transcriptrender.TruncateLine(transcriptrender.Line{
			Spans: []transcriptrender.Span{transcriptrender.SemanticSpan(line, transcriptrender.StyleRoleNotice)},
		}, width, false).Plain()
		if truncated != "" {
			out = append(out, truncated)
		}
	}
	return out
}

func humanizeTranscriptFact(value string) string {
	return strings.ReplaceAll(strings.TrimSpace(value), "_", " ")
}

func isAppOwnedOngoingMessage(kind clientui.TranscriptMessageKind) bool {
	switch kind {
	case clientui.TranscriptMessageStepState,
		clientui.TranscriptMessageRuntimeReadModelUpdate,
		clientui.TranscriptMessageQueuedMessageState,
		clientui.TranscriptMessagePendingWorkChanged,
		clientui.TranscriptMessagePendingWorkRestored,
		clientui.TranscriptMessageSessionSettingFeedback,
		clientui.TranscriptMessageUserMessageFlushed,
		clientui.TranscriptMessageSessionStatus,
		clientui.TranscriptMessageSessionIdentity,
		clientui.TranscriptMessageCompactionStatus,
		clientui.TranscriptMessageContextUsage,
		clientui.TranscriptMessageGoalStatus,
		clientui.TranscriptMessageBackgroundActivity,
		clientui.TranscriptMessagePrompt,
		clientui.TranscriptMessageToolStart,
		clientui.TranscriptMessageToolAbort:
		return true
	default:
		return false
	}
}
