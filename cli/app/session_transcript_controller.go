package app

import (
	"fmt"
	"strconv"
	"strings"

	"core/cli/tui/ongoing"
	"core/cli/tui/transcriptrender"
	"core/shared/clientui"
	"core/shared/transcript"
)

const ongoingTranscriptQueueLimit = 1000

type ongoingTranscriptSurface interface {
	ApplyTerminalMessage(clientui.TranscriptMessage, ongoing.FrameInput) (ongoing.Result, error)
	Render(ongoing.FrameInput) (ongoing.Result, error)
	Resize(ongoing.Size, ongoing.FrameInput) (ongoing.Result, error)
}

type ongoingFrameProvider func() ongoing.FrameInput

type ongoingTranscriptController struct {
	surface         ongoingTranscriptSurface
	frameProvider   ongoingFrameProvider
	normalOwned     bool
	hydrated        bool
	lastSequence    uint64
	queue           []clientui.TranscriptMessage
	queueOverflowed bool
	liveReadModel   ongoingTranscriptReadModel
}

func newOngoingTranscriptController(surface ongoingTranscriptSurface, frameProvider ongoingFrameProvider) *ongoingTranscriptController {
	if frameProvider == nil {
		panic("ongoing transcript controller requires frame provider")
	}
	return &ongoingTranscriptController{
		surface:       surface,
		frameProvider: frameProvider,
		normalOwned:   true,
		liveReadModel: newOngoingTranscriptReadModel(),
	}
}

func (c *ongoingTranscriptController) Accept(message clientui.TranscriptMessage) (ongoing.Result, error) {
	if result, ok := c.acceptDelivery(message); ok {
		return result, nil
	}
	if !c.normalOwned {
		c.enqueue(message)
		return ongoing.Result{}, nil
	}
	return c.applyNow(message, true)
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
		return ongoing.Result{Action: ongoing.ResultRequestScratchRehydration, Reason: ongoing.RehydrateReasonQueueOverflow}, nil
	}
	return c.drainQueued()
}

func (c *ongoingTranscriptController) ResetForScratchHydration() {
	c.queue = nil
	c.queueOverflowed = false
	c.hydrated = false
	c.lastSequence = 0
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

func (c *ongoingTranscriptController) acceptDelivery(message clientui.TranscriptMessage) (ongoing.Result, bool) {
	if !c.hydrated {
		if message.Sequence != 1 || message.Kind != clientui.TranscriptMessageHydration {
			c.requestScratchRehydration()
			return ongoing.Result{Action: ongoing.ResultRequestScratchRehydration, Reason: ongoing.RehydrateReasonSequenceGap}, true
		}
		c.hydrated = true
		c.lastSequence = message.Sequence
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
	c.lastSequence = message.Sequence
	return ongoing.Result{}, false
}

func (c *ongoingTranscriptController) requestScratchRehydration() {
	c.queue = nil
	c.queueOverflowed = true
	c.hydrated = false
	c.lastSequence = 0
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
	needsRender := false
	for _, message := range queued {
		if isAppOwnedOngoingMessage(message.Kind) {
			c.applyAppOwnedMessage(message)
			needsRender = true
			continue
		}
		if _, err := c.applyNow(message, false); err != nil {
			return ongoing.Result{}, err
		}
	}
	if needsRender {
		return c.surface.Render(c.frameInput())
	}
	return ongoing.Result{}, nil
}

func (c *ongoingTranscriptController) applyNow(message clientui.TranscriptMessage, renderAppOwned bool) (ongoing.Result, error) {
	if isAppOwnedOngoingMessage(message.Kind) {
		c.applyAppOwnedMessage(message)
		if renderAppOwned {
			return c.surface.Render(c.frameInput())
		}
		return ongoing.Result{}, nil
	}
	if message.Kind == clientui.TranscriptMessageHydration {
		liveFactsChanged := c.applyHydrationAppOwnedFacts(message.Hydration)
		result, err := c.surface.ApplyTerminalMessage(message, c.frameInput())
		if err != nil {
			return ongoing.Result{}, err
		}
		if liveFactsChanged && hydrationHasNoTerminalRows(message.Hydration) {
			return c.surface.Render(c.frameInput())
		}
		return result, nil
	}
	if message.Kind == clientui.TranscriptMessageCommittedRow && message.CommittedRow != nil && message.CommittedRow.Tool != nil {
		c.liveReadModel.removePendingTool(message.CommittedRow.Tool.ToolCallID)
	}
	return c.surface.ApplyTerminalMessage(message, c.frameInput())
}

func (c *ongoingTranscriptController) applyAppOwnedMessage(message clientui.TranscriptMessage) {
	switch message.Kind {
	case clientui.TranscriptMessageRunState:
		// Run state is already represented by the app status line.
	case clientui.TranscriptMessageRuntimeActivity:
		// Runtime activity is already represented by the app status line/spinner.
	case clientui.TranscriptMessageInputReconciliation:
		// Input reconciliation is operator metadata; do not add a separate live-band row.
	case clientui.TranscriptMessageQueuedOrSteeredMessageState:
		c.liveReadModel.applyQueuedOrSteered(message.QueuedOrSteeredMessageState)
	case clientui.TranscriptMessageSessionStatus:
		// Session status is already represented by the app status line.
	case clientui.TranscriptMessageSessionIdentity:
		// Session identity is already represented by the app status line.
	case clientui.TranscriptMessageCompactionStatus:
		// Compaction status is already represented by the app status line.
	case clientui.TranscriptMessagePendingSessionPrompt:
		c.liveReadModel.applyPendingPrompt(message.PendingSessionPrompt)
	case clientui.TranscriptMessageContextUsage:
		// Context usage is already represented by the app status line.
	case clientui.TranscriptMessageGoalStatus:
		// Goal status is already represented by the app status line.
	case clientui.TranscriptMessageBackgroundActivity:
		c.liveReadModel.applyBackgroundActivity(message.BackgroundActivity)
	case clientui.TranscriptMessageToolStart:
		if message.ToolStart != nil {
			c.liveReadModel.addPendingTool(*message.ToolStart)
		}
	case clientui.TranscriptMessageToolAbort:
		if message.ToolAbort != nil {
			c.liveReadModel.removePendingTool(message.ToolAbort.ToolCallID)
		}
	default:
		panic(fmt.Sprintf("unsupported app-owned transcript message kind %q", message.Kind))
	}
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
	if len(hydration.QueuedOrSteeredMessages) > 0 {
		for _, state := range hydration.QueuedOrSteeredMessages {
			c.liveReadModel.applyQueuedOrSteered(&state)
		}
		changed = true
	}
	if len(hydration.BackgroundActivities) > 0 {
		for _, activity := range hydration.BackgroundActivities {
			c.liveReadModel.applyBackgroundActivity(&activity)
		}
		changed = true
	}
	if len(hydration.PendingSessionPrompts) > 0 {
		for _, prompt := range hydration.PendingSessionPrompts {
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
	if hydration.ActiveAssistantStream != nil {
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
	c.liveReadModel.refreshPendingToolSection(frame.Size.Width)
	c.liveReadModel.refreshQueuedOrSteeredSection(frame.Size.Width)
	c.liveReadModel.refreshBackgroundActivitySection(frame.Size.Width)
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

func runStateLines(state *clientui.RunState) []string {
	if state == nil {
		return nil
	}
	parts := compactNonEmptyStrings(humanizeTranscriptFact(string(state.Status)), humanizeTranscriptFact(string(state.Lifecycle.Phase)), humanizeTranscriptFact(string(state.Lifecycle.Mode)), humanizeTranscriptFact(string(state.ActiveKind)))
	return joinFacts(parts)
}

func runtimeActivityLines(activity *clientui.RuntimeActivity) []string {
	if activity == nil {
		return nil
	}
	parts := compactNonEmptyStrings(humanizeTranscriptFact(string(activity.State)), humanizeTranscriptFact(string(activity.ActiveKind)))
	if activity.DiagnosticRecovery {
		parts = append(parts, "diagnostic recovery")
	}
	return joinFacts(parts)
}

func inputReconciliationLines(snapshot *clientui.RuntimeInputReconciliationSnapshot) []string {
	if snapshot == nil {
		return nil
	}
	lines := make([]string, 0, len(snapshot.Operations))
	for _, op := range snapshot.Operations {
		if op.RestoreRecommended() || op.Ambiguous() {
			lines = append(lines, humanizeTranscriptFact(string(op.State)))
		}
	}
	return lines
}

func queuedOrSteeredLines(state *clientui.TranscriptQueuedOrSteeredMessageState) []string {
	if state == nil {
		return nil
	}
	if text := strings.TrimSpace(state.UserText); text != "" {
		return []string{text}
	}
	return joinFacts(compactNonEmptyStrings(humanizeTranscriptFact(string(state.Status)), humanizeTranscriptFact(string(state.FailureReason))))
}

func queuedOrSteeredListLines(states []clientui.TranscriptQueuedOrSteeredMessageState) []string {
	lines := make([]string, 0, len(states))
	for _, state := range states {
		lines = append(lines, queuedOrSteeredLines(&state)...)
	}
	return lines
}

func sessionStatusLines(status *clientui.TranscriptSessionStatus) []string {
	if status == nil {
		return nil
	}
	parts := compactNonEmptyStrings(status.ThinkingLevel, status.CompactionMode)
	if status.FastModeEnabled {
		parts = append(parts, "fast")
	}
	if status.ReviewerEnabled {
		parts = append(parts, "reviewer")
	}
	return joinFacts(parts)
}

func sessionIdentityLines(identity *clientui.TranscriptSessionIdentity) []string {
	if identity == nil {
		return nil
	}
	worktreeName := ""
	if identity.ExecutionTarget.Worktree != nil {
		worktreeName = identity.ExecutionTarget.Worktree.Name
	}
	return joinFacts(compactNonEmptyStrings(identity.SessionName, worktreeName, identity.ExecutionTarget.WorkspaceName))
}

func compactionStatusLines(status *clientui.TranscriptCompactionStatus) []string {
	if status == nil {
		return nil
	}
	parts := compactNonEmptyStrings(humanizeTranscriptFact(status.State), humanizeTranscriptFact(status.Mode))
	if status.Count > 0 {
		parts = append(parts, strconv.Itoa(status.Count))
	}
	return joinFacts(parts)
}

func contextUsageLines(usage *clientui.RuntimeContextUsage) []string {
	if usage == nil || usage.WindowTokens <= 0 {
		return nil
	}
	parts := []string{strconv.Itoa(usage.UsedTokens) + "/" + strconv.Itoa(usage.WindowTokens)}
	if usage.HasCacheHitPercentage {
		parts = append(parts, strconv.Itoa(usage.CacheHitPercent)+"% cache")
	}
	return joinFacts(parts)
}

func goalStatusLines(goal *clientui.TranscriptGoalStatus) []string {
	if goal == nil || goal.Cleared {
		return nil
	}
	return joinFacts(compactNonEmptyStrings(goal.Objective, humanizeTranscriptFact(string(goal.Status))))
}

func backgroundActivityLines(activity *clientui.TranscriptBackgroundActivity) []string {
	if activity == nil || activity.Removed {
		return nil
	}
	return joinFacts(compactNonEmptyStrings(activity.Preview, activity.Command, humanizeTranscriptFact(activity.State)))
}

func backgroundActivityListLines(activities []clientui.TranscriptBackgroundActivity) []string {
	lines := make([]string, 0, len(activities))
	for _, activity := range activities {
		lines = append(lines, backgroundActivityLines(&activity)...)
	}
	return lines
}

func pendingPromptLines(prompt *clientui.TranscriptPendingSessionPrompt) []string {
	if prompt == nil || prompt.State != clientui.TranscriptPromptPending {
		return nil
	}
	return joinFacts(compactNonEmptyStrings(prompt.Data.Question, prompt.Data.ToolName, humanizeTranscriptFact(string(prompt.Kind))))
}

func pendingPromptListLines(prompts []clientui.TranscriptPendingSessionPrompt) []string {
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
			Spans: []transcriptrender.Span{{Text: line}},
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
	case clientui.TranscriptMessageRunState,
		clientui.TranscriptMessageRuntimeActivity,
		clientui.TranscriptMessageInputReconciliation,
		clientui.TranscriptMessageQueuedOrSteeredMessageState,
		clientui.TranscriptMessageSessionStatus,
		clientui.TranscriptMessageSessionIdentity,
		clientui.TranscriptMessageCompactionStatus,
		clientui.TranscriptMessageContextUsage,
		clientui.TranscriptMessageGoalStatus,
		clientui.TranscriptMessageBackgroundActivity,
		clientui.TranscriptMessagePendingSessionPrompt,
		clientui.TranscriptMessageToolStart,
		clientui.TranscriptMessageToolAbort:
		return true
	default:
		return false
	}
}
