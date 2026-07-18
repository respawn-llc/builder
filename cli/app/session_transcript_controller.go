package app

import (
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"strconv"
	"strings"

	"core/cli/tui/ongoing"
	"core/cli/tui/transcriptrender"
	"core/shared/clientui"
	"core/shared/runtimeids"
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
	promptOwner      ongoingTranscriptPromptOwner
	logger           uiLogger
}

func newOngoingTranscriptController(
	surface ongoingTranscriptSurface,
	frameProvider ongoingFrameProvider,
	runtimeAdmission ongoingTranscriptRuntimeAdmission,
	stateObserver ongoingTranscriptAdmittedStateObserver,
	promptOwner ongoingTranscriptPromptOwner,
	loggers ...uiLogger,
) *ongoingTranscriptController {
	if frameProvider == nil {
		panic("ongoing transcript controller requires frame provider")
	}
	if runtimeAdmission == nil {
		panic("ongoing transcript controller requires runtime admission")
	}
	if stateObserver == nil {
		panic("ongoing transcript controller requires admitted state observer")
	}
	if promptOwner == nil {
		panic("ongoing transcript controller requires prompt owner")
	}
	controller := &ongoingTranscriptController{
		surface:          surface,
		frameProvider:    frameProvider,
		runtimeAdmission: runtimeAdmission,
		stateObserver:    stateObserver,
		normalOwned:      true,
		liveReadModel:    newOngoingTranscriptReadModel(),
		promptOwner:      promptOwner,
	}
	if len(loggers) > 0 {
		controller.logger = loggers[0]
	}
	return controller
}

func (c *ongoingTranscriptController) AcceptFrom(sourceSessionID runtimeids.SessionID, message clientui.TranscriptMessage) (ongoing.Result, tea.Cmd, error) {
	c.validateSourceSession(sourceSessionID, message)
	if result, ok := c.rejectDelivery(message); ok {
		return result, nil, nil
	}
	var (
		promptReconciliation    transcriptPromptReconciliation
		hasPromptReconciliation bool
		promptStateChanged      bool
		promptErr               error
	)
	if transcriptMessageCanPreparePromptReconciliation(message) {
		ownership := c.snapshotTranscriptPromptOwnership()
		promptReconciliation, hasPromptReconciliation, promptErr = prepareTranscriptPromptReconciliation(
			ownership,
			message,
		)
		promptStateChanged = len(ownership.events) > 0 || len(promptReconciliation.events) > 0
		if promptErr != nil {
			c.panicPromptReconciliationError(sourceSessionID, message, promptErr)
		}
	}
	if err := message.Validate(); err != nil {
		return ongoing.Result{}, nil, fmt.Errorf("validate ongoing transcript message: %w", err)
	}
	admission, err := c.runtimeAdmission(message)
	if err != nil {
		return ongoing.Result{}, nil, c.diagnoseRuntimeAdmissionError(message, err)
	}
	c.advanceDelivery(message)
	stateChanged := c.applyState(message)
	stateCmd := c.stateObserver(message, admission)
	if hasPromptReconciliation {
		c.liveReadModel.replacePendingPrompts(promptReconciliation.pendingPrompts())
		c.promptOwner.commitTranscriptPromptReconciliation(promptReconciliation)
		stateChanged = stateChanged || promptStateChanged
	}
	if !c.normalOwned {
		if !isAppOwnedOngoingMessage(message.Kind) {
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
	facts["quoted_payload"] = strconv.Quote(fmt.Sprintf("%+v", message.Payload.Hydration))
	return ongoing.NewDeveloperError(
		"admit_transcript_hydration_runtime_tuple",
		conflict.Error(),
		facts,
	)
}

func transcriptMessageCanPreparePromptReconciliation(message clientui.TranscriptMessage) bool {
	switch message.Kind {
	case clientui.TranscriptMessageHydration:
		return message.Payload.Hydration != nil
	case clientui.TranscriptMessagePromptPending:
		return message.Payload.PromptPending != nil
	case clientui.TranscriptMessagePromptResolved:
		return message.Payload.PromptResolved != nil
	default:
		return false
	}
}

type ongoingTranscriptDeveloperError struct {
	Operation         string
	Geometry          ongoing.Size
	Payload           string
	SourceSessionID   runtimeids.SessionID
	PayloadSessionID  runtimeids.SessionID
	MessageKind       clientui.TranscriptMessageKind
	Sequence          uint64
	PromptID          *clientui.PromptID
	OldPromptContract *transcriptPromptContract
	NewPromptContract *transcriptPromptContract
	Stack             string
}

func (e *ongoingTranscriptDeveloperError) Error() string {
	var promptID any
	if e.PromptID != nil {
		promptID = *e.PromptID
	}
	return fmt.Sprintf(
		"ongoing transcript developer error: operation=%s geometry=%dx%d source_session_id=%q payload_session_id=%q message_kind=%q sequence=%d prompt_id=%v payload=%s",
		e.Operation,
		e.Geometry.Width,
		e.Geometry.Height,
		e.SourceSessionID.String(),
		e.PayloadSessionID.String(),
		e.MessageKind,
		e.Sequence,
		promptID,
		e.Payload,
	)
}

func (c *ongoingTranscriptController) validateSourceSession(sourceSessionID runtimeids.SessionID, message clientui.TranscriptMessage) {
	payloadSessionID, offendingPrompt := transcriptMessageForeignSession(sourceSessionID, message)
	if !sourceSessionID.IsZero() && payloadSessionID.IsZero() {
		return
	}
	diagnostic := c.newDeveloperError(sourceSessionID, message)
	diagnostic.PayloadSessionID = payloadSessionID
	if offendingPrompt != nil {
		c.decorateDeveloperErrorPromptContractForPrompt(diagnostic, *offendingPrompt)
	} else {
		c.decorateDeveloperErrorPromptContract(diagnostic, message)
	}
	c.panicDeveloperError(diagnostic)
}

func transcriptMessageForeignSession(
	sourceSessionID runtimeids.SessionID,
	message clientui.TranscriptMessage,
) (runtimeids.SessionID, *clientui.TranscriptPrompt) {
	if sourceSessionID.IsZero() {
		return transcriptMessagePayloadSessionID(message), transcriptMessagePrompt(message)
	}
	switch message.Kind {
	case clientui.TranscriptMessageHydration:
		if message.Payload.Hydration == nil {
			return runtimeids.SessionID{}, nil
		}
		if identity := message.Payload.Hydration.SessionIdentity.SessionID; identity != sourceSessionID {
			return identity, nil
		}
		for index := range message.Payload.Hydration.PendingPrompts {
			prompt := &message.Payload.Hydration.PendingPrompts[index]
			if prompt.SessionID != sourceSessionID {
				return prompt.SessionID, prompt
			}
		}
	case clientui.TranscriptMessagePromptPending:
		if prompt := message.Payload.PromptPending; prompt != nil && prompt.SessionID != sourceSessionID {
			return prompt.SessionID, prompt
		}
	case clientui.TranscriptMessagePromptResolved:
		if prompt := message.Payload.PromptResolved; prompt != nil && prompt.SessionID != sourceSessionID {
			return prompt.SessionID, prompt
		}
	case clientui.TranscriptMessageSessionIdentity:
		if identity := message.Payload.SessionIdentity; identity != nil && identity.SessionID != sourceSessionID {
			return identity.SessionID, nil
		}
	}
	return runtimeids.SessionID{}, nil
}

func (c *ongoingTranscriptController) panicPromptReconciliationError(
	sourceSessionID runtimeids.SessionID,
	message clientui.TranscriptMessage,
	err error,
) {
	mismatch, ok := err.(*transcriptPromptContractMismatch)
	if !ok {
		panic(err)
	}
	diagnostic := c.newDeveloperError(sourceSessionID, message)
	diagnostic.Operation = "reconcile_transcript_prompt"
	promptID := mismatch.PromptID
	diagnostic.PromptID = &promptID
	diagnostic.OldPromptContract = &mismatch.Old
	diagnostic.NewPromptContract = &mismatch.New
	c.panicDeveloperError(diagnostic)
}

func (c *ongoingTranscriptController) newDeveloperError(
	sourceSessionID runtimeids.SessionID,
	message clientui.TranscriptMessage,
) *ongoingTranscriptDeveloperError {
	return &ongoingTranscriptDeveloperError{
		Operation:        "accept_transcript_delivery",
		Geometry:         c.frameProvider().Size,
		Payload:          fmt.Sprintf("%#v", message),
		SourceSessionID:  sourceSessionID,
		PayloadSessionID: transcriptMessagePayloadSessionID(message),
		MessageKind:      message.Kind,
		Sequence:         message.Sequence,
		PromptID:         transcriptMessagePromptID(message),
		Stack:            string(debug.Stack()),
	}
}

func (c *ongoingTranscriptController) decorateDeveloperErrorPromptContract(
	diagnostic *ongoingTranscriptDeveloperError,
	message clientui.TranscriptMessage,
) {
	newPrompt := transcriptMessagePrompt(message)
	if newPrompt == nil {
		return
	}
	c.decorateDeveloperErrorPromptContractForPrompt(diagnostic, *newPrompt)
}

func (c *ongoingTranscriptController) decorateDeveloperErrorPromptContractForPrompt(
	diagnostic *ongoingTranscriptDeveloperError,
	newPrompt clientui.TranscriptPrompt,
) {
	promptID := newPrompt.PromptID
	diagnostic.PromptID = &promptID
	for _, event := range c.snapshotTranscriptPromptOwnership().events {
		if event.prompt.PromptID != newPrompt.PromptID {
			continue
		}
		oldContract := newTranscriptPromptContract(event.prompt)
		newContract := newTranscriptPromptContract(newPrompt)
		diagnostic.OldPromptContract = &oldContract
		diagnostic.NewPromptContract = &newContract
		return
	}
}

func (c *ongoingTranscriptController) panicDeveloperError(diagnostic *ongoingTranscriptDeveloperError) {
	if c.logger != nil {
		c.logger.Logf("ongoing.transcript.developer_error diagnostic=%+v stack=%s", diagnostic, diagnostic.Stack)
	} else {
		log.Printf("ongoing.transcript.developer_error diagnostic=%+v stack=%s", diagnostic, diagnostic.Stack)
	}
	panic(diagnostic)
}

func transcriptMessagePayloadSessionID(message clientui.TranscriptMessage) runtimeids.SessionID {
	switch message.Kind {
	case clientui.TranscriptMessageHydration:
		if message.Payload.Hydration != nil {
			return message.Payload.Hydration.SessionIdentity.SessionID
		}
	case clientui.TranscriptMessagePromptPending:
		if message.Payload.PromptPending != nil {
			return message.Payload.PromptPending.SessionID
		}
	case clientui.TranscriptMessagePromptResolved:
		if message.Payload.PromptResolved != nil {
			return message.Payload.PromptResolved.SessionID
		}
	case clientui.TranscriptMessageSessionIdentity:
		if message.Payload.SessionIdentity != nil {
			return message.Payload.SessionIdentity.SessionID
		}
	}
	return runtimeids.SessionID{}
}

func transcriptMessagePromptID(message clientui.TranscriptMessage) *clientui.PromptID {
	if prompt := transcriptMessagePrompt(message); prompt != nil {
		promptID := prompt.PromptID
		return &promptID
	}
	return nil
}

func transcriptMessagePrompt(message clientui.TranscriptMessage) *clientui.TranscriptPrompt {
	switch message.Kind {
	case clientui.TranscriptMessagePromptPending:
		if message.Payload.PromptPending != nil {
			return message.Payload.PromptPending
		}
	case clientui.TranscriptMessagePromptResolved:
		if message.Payload.PromptResolved != nil {
			return message.Payload.PromptResolved
		}
	case clientui.TranscriptMessageHydration:
		if message.Payload.Hydration != nil && len(message.Payload.Hydration.PendingPrompts) > 0 {
			return &message.Payload.Hydration.PendingPrompts[0]
		}
	}
	return nil
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

func (c *ongoingTranscriptController) rejectDelivery(message clientui.TranscriptMessage) (ongoing.Result, bool) {
	if !c.hydrated {
		if message.Sequence != 1 || message.Kind != clientui.TranscriptMessageHydration {
			c.requestScratchRehydration()
			return ongoing.Result{Action: ongoing.ResultRequestScratchRehydration, Reason: ongoing.RehydrateReasonSequenceGap}, true
		}
		return ongoing.Result{}, false
	}
	if message.Sequence != c.lastSequence+1 {
		c.requestScratchRehydration()
		return ongoing.Result{Action: ongoing.ResultRequestScratchRehydration, Reason: ongoing.RehydrateReasonSequenceGap}, true
	}
	if message.Kind == clientui.TranscriptMessageHydration {
		c.requestScratchRehydration()
		return ongoing.Result{Action: ongoing.ResultRequestScratchRehydration, Reason: ongoing.RehydrateReasonSequenceGap}, true
	}
	return ongoing.Result{}, false
}

func (c *ongoingTranscriptController) advanceDelivery(message clientui.TranscriptMessage) {
	c.hydrated = true
	c.lastSequence = message.Sequence
}

func (c *ongoingTranscriptController) acceptedHydration(message clientui.TranscriptMessage) bool {
	return message.Kind == clientui.TranscriptMessageHydration &&
		c.hydrated &&
		c.lastSequence == message.Sequence
}

func (c *ongoingTranscriptController) snapshotTranscriptPromptOwnership() transcriptPromptOwnershipSnapshot {
	return c.promptOwner.snapshotTranscriptPromptOwnership()
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
	if isAppOwnedOngoingMessage(message.Kind) {
		if stateChanged {
			return c.surface.Render(c.frameInput())
		}
		return ongoing.Result{}, nil
	}
	if message.Kind == clientui.TranscriptMessageHydration {
		result, err := c.surface.ApplyTerminalMessage(message, c.frameInput())
		if err != nil {
			return ongoing.Result{}, err
		}
		if stateChanged && hydrationHasNoTerminalRows(message.Payload.Hydration) {
			return c.surface.Render(c.frameInput())
		}
		return result, nil
	}
	return c.surface.ApplyTerminalMessage(message, c.frameInput())
}

func (c *ongoingTranscriptController) applyState(message clientui.TranscriptMessage) bool {
	if message.Kind == clientui.TranscriptMessageHydration {
		return c.applyHydrationAppOwnedFacts(message.Payload.Hydration)
	}
	if message.Kind == clientui.TranscriptMessageCommittedRow &&
		message.Payload.CommittedRow != nil &&
		message.Payload.CommittedRow.Tool != nil {
		c.liveReadModel.removePendingTool(string(message.Payload.CommittedRow.Tool.ToolCallID))
		return true
	}
	if !isAppOwnedOngoingMessage(message.Kind) {
		return false
	}
	return c.applyAppOwnedMessage(message)
}

func (c *ongoingTranscriptController) applyAppOwnedMessage(message clientui.TranscriptMessage) bool {
	switch message.Kind {
	case clientui.TranscriptMessageStepState,
		clientui.TranscriptMessageReviewerState,
		clientui.TranscriptMessageRuntimeReadModelUpdate:
		// Canonical runtime state is represented by the app status line.
	case clientui.TranscriptMessageUserMessageFlushed:
		// The state observer owns input reconciliation. Re-render the client-local
		// queue after it removes the flushed operation identities.
	case clientui.TranscriptMessageQueuedMessageState:
		c.liveReadModel.applyQueuedOrSteered(message.Payload.QueuedMessageState)
		return true
	case clientui.TranscriptMessageSessionStatus:
		// Session status is already represented by the app status line.
	case clientui.TranscriptMessageSessionIdentity:
		// Session identity is already represented by the app status line.
	case clientui.TranscriptMessageCompactionStatus:
		// Compaction status is already represented by the app status line.
	case clientui.TranscriptMessagePromptPending:
		// Prompt ownership and projection commit through one prepared reconciliation.
	case clientui.TranscriptMessagePromptResolved:
		// Prompt ownership and projection commit through one prepared reconciliation.
	case clientui.TranscriptMessageContextUsage:
		// Context usage is already represented by the app status line.
	case clientui.TranscriptMessageGoalStatus:
		// Goal status is already represented by the app status line.
	case clientui.TranscriptMessageBackgroundActivity:
		// Backgrounding ends the mutable foreground tool lifecycle. The immutable
		// tool row and later completion notice own its ongoing presentation.
	case clientui.TranscriptMessageToolStart:
		if message.Payload.ToolStart != nil {
			c.liveReadModel.addPendingTool(*message.Payload.ToolStart)
			return true
		}
	case clientui.TranscriptMessageToolAbort:
		if message.Payload.ToolAbort != nil {
			c.liveReadModel.removePendingTool(string(message.Payload.ToolAbort.ToolCallID))
			return true
		}
	default:
		panic(fmt.Sprintf("unsupported app-owned transcript message kind %q", message.Kind))
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
	if len(hydration.QueuedMessages) > 0 {
		for _, state := range hydration.QueuedMessages {
			c.liveReadModel.applyQueuedOrSteered(&state)
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
	c.liveReadModel.refreshQueuedOrSteeredSection(frame.Size.Width)
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
	if prompt == nil || prompt.State != clientui.TranscriptPromptStatePending {
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
		clientui.TranscriptMessageReviewerState,
		clientui.TranscriptMessageRuntimeReadModelUpdate,
		clientui.TranscriptMessageQueuedMessageState,
		clientui.TranscriptMessageUserMessageFlushed,
		clientui.TranscriptMessageSessionStatus,
		clientui.TranscriptMessageSessionIdentity,
		clientui.TranscriptMessageCompactionStatus,
		clientui.TranscriptMessageContextUsage,
		clientui.TranscriptMessageGoalStatus,
		clientui.TranscriptMessageBackgroundActivity,
		clientui.TranscriptMessagePromptPending,
		clientui.TranscriptMessagePromptResolved,
		clientui.TranscriptMessageToolStart,
		clientui.TranscriptMessageToolAbort:
		return true
	default:
		return false
	}
}
