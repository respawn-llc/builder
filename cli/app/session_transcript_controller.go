package app

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"core/cli/tui/ongoing"
	"core/shared/clientui"
)

const ongoingTranscriptQueueLimit = 1000

type ongoingTranscriptSurface interface {
	ApplyTerminalMessage(clientui.TranscriptMessage, ongoing.FrameInput) (ongoing.Result, error)
	Render(ongoing.FrameInput) (ongoing.Result, error)
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

type ongoingTranscriptReadModel struct {
	sectionOrder     []ongoing.FrameSectionKind
	sections         map[ongoing.FrameSectionKind]ongoing.FrameSection
	pendingTools     []ongoingPendingTool
	pendingToolIndex map[string]int
}

type ongoingPendingTool struct {
	id    string
	label string
}

func newOngoingTranscriptController(surface ongoingTranscriptSurface, frameProvider ongoingFrameProvider) *ongoingTranscriptController {
	if frameProvider == nil {
		panic("ongoing transcript controller requires frame provider")
	}
	return &ongoingTranscriptController{
		surface:       surface,
		frameProvider: frameProvider,
		normalOwned:   true,
		liveReadModel: ongoingTranscriptReadModel{
			sections:         map[ongoing.FrameSectionKind]ongoing.FrameSection{},
			pendingToolIndex: map[string]int{},
		},
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
		c.applyHydrationAppOwnedFacts(message.Hydration)
	}
	if message.Kind == clientui.TranscriptMessageCommittedRow && message.CommittedRow != nil && message.CommittedRow.Tool != nil {
		c.liveReadModel.removePendingTool(message.CommittedRow.Tool.ToolCallID)
	}
	return c.surface.ApplyTerminalMessage(message, c.frameInput())
}

func (c *ongoingTranscriptController) applyAppOwnedMessage(message clientui.TranscriptMessage) {
	switch message.Kind {
	case clientui.TranscriptMessageRunState:
		c.liveReadModel.setSection(ongoing.FrameSectionRunState, runStateLines(message.RunState))
	case clientui.TranscriptMessageRuntimeActivity:
		c.liveReadModel.setSection(ongoing.FrameSectionRuntimeActivity, runtimeActivityLines(message.RuntimeActivity))
	case clientui.TranscriptMessageInputReconciliation:
		c.liveReadModel.setSection(ongoing.FrameSectionInputReconciliation, inputReconciliationLines(message.InputReconciliation))
	case clientui.TranscriptMessageQueuedOrSteeredMessageState:
		c.liveReadModel.setSection(ongoing.FrameSectionQueuedOrSteered, queuedOrSteeredLines(message.QueuedOrSteeredMessageState))
	case clientui.TranscriptMessageSessionStatus:
		c.liveReadModel.setSection(ongoing.FrameSectionSessionStatus, sessionStatusLines(message.SessionStatus))
	case clientui.TranscriptMessageSessionIdentity:
		c.liveReadModel.setSection(ongoing.FrameSectionSessionIdentity, sessionIdentityLines(message.SessionIdentity))
	case clientui.TranscriptMessageCompactionStatus:
		c.liveReadModel.setSection(ongoing.FrameSectionCompaction, compactionStatusLines(message.CompactionStatus))
	case clientui.TranscriptMessagePendingSessionPrompt:
		c.liveReadModel.setSection(ongoing.FrameSectionPendingPrompt, pendingPromptLines(message.PendingSessionPrompt))
	case clientui.TranscriptMessageContextUsage:
		c.liveReadModel.setSection(ongoing.FrameSectionContextUsage, contextUsageLines(message.ContextUsage))
	case clientui.TranscriptMessageGoalStatus:
		c.liveReadModel.setSection(ongoing.FrameSectionGoal, goalStatusLines(message.GoalStatus))
	case clientui.TranscriptMessageBackgroundActivity:
		c.liveReadModel.setSection(ongoing.FrameSectionBackgroundActivity, backgroundActivityLines(message.BackgroundActivity))
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

func (c *ongoingTranscriptController) applyHydrationAppOwnedFacts(hydration *clientui.TranscriptHydration) {
	if hydration == nil {
		return
	}
	if len(hydration.InFlightTools) > 0 {
		for _, tool := range hydration.InFlightTools {
			c.liveReadModel.addPendingTool(tool)
		}
	}
	if len(hydration.QueuedOrSteeredMessages) > 0 {
		c.liveReadModel.setSection(ongoing.FrameSectionQueuedOrSteered, queuedOrSteeredListLines(hydration.QueuedOrSteeredMessages))
	}
	if hydration.RunState != nil {
		c.liveReadModel.setSection(ongoing.FrameSectionRunState, runStateLines(hydration.RunState))
	}
	if hydration.RuntimeActivity != nil {
		c.liveReadModel.setSection(ongoing.FrameSectionRuntimeActivity, runtimeActivityLines(hydration.RuntimeActivity))
	}
	if hydration.InputReconciliation != nil {
		c.liveReadModel.setSection(ongoing.FrameSectionInputReconciliation, inputReconciliationLines(hydration.InputReconciliation))
	}
	if !reflect.DeepEqual(hydration.SessionStatus, clientui.TranscriptSessionStatus{}) {
		c.liveReadModel.setSection(ongoing.FrameSectionSessionStatus, sessionStatusLines(&hydration.SessionStatus))
	}
	if !reflect.DeepEqual(hydration.SessionIdentity, clientui.TranscriptSessionIdentity{}) {
		c.liveReadModel.setSection(ongoing.FrameSectionSessionIdentity, sessionIdentityLines(&hydration.SessionIdentity))
	}
	if hydration.CompactionStatus != nil {
		c.liveReadModel.setSection(ongoing.FrameSectionCompaction, compactionStatusLines(hydration.CompactionStatus))
	}
	if hydration.ContextUsage != nil {
		c.liveReadModel.setSection(ongoing.FrameSectionContextUsage, contextUsageLines(hydration.ContextUsage))
	}
	if hydration.GoalStatus != nil {
		c.liveReadModel.setSection(ongoing.FrameSectionGoal, goalStatusLines(hydration.GoalStatus))
	}
	if len(hydration.BackgroundActivities) > 0 {
		c.liveReadModel.setSection(ongoing.FrameSectionBackgroundActivity, backgroundActivityListLines(hydration.BackgroundActivities))
	}
	if len(hydration.PendingSessionPrompts) > 0 {
		c.liveReadModel.setSection(ongoing.FrameSectionPendingPrompt, pendingPromptListLines(hydration.PendingSessionPrompts))
	}
}

func (m *ongoingTranscriptReadModel) setSection(kind ongoing.FrameSectionKind, lines []string) {
	lines = terminalSafeFrameLines(lines)
	if len(lines) == 0 {
		m.removeSection(kind)
		return
	}
	if _, exists := m.sections[kind]; !exists {
		m.sectionOrder = append(m.sectionOrder, kind)
	}
	m.sections[kind] = ongoing.FrameSection{Kind: kind, Lines: append([]string(nil), lines...)}
}

func (m *ongoingTranscriptReadModel) removeSection(kind ongoing.FrameSectionKind) {
	if _, exists := m.sections[kind]; !exists {
		return
	}
	delete(m.sections, kind)
	filtered := m.sectionOrder[:0]
	for _, current := range m.sectionOrder {
		if current != kind {
			filtered = append(filtered, current)
		}
	}
	m.sectionOrder = filtered
}

func (m *ongoingTranscriptReadModel) addPendingTool(tool clientui.TranscriptToolStart) {
	if tool.ToolCallID == "" {
		panic("ongoing pending tool start missing tool call id")
	}
	label := strings.TrimSpace(tool.ToolName)
	if label == "" {
		label = strings.TrimSpace(tool.ToolCallID)
	}
	if index, exists := m.pendingToolIndex[tool.ToolCallID]; exists {
		m.pendingTools[index].label = label
		m.refreshPendingToolSection()
		return
	}
	m.pendingToolIndex[tool.ToolCallID] = len(m.pendingTools)
	m.pendingTools = append(m.pendingTools, ongoingPendingTool{id: tool.ToolCallID, label: label})
	m.refreshPendingToolSection()
}

func (m *ongoingTranscriptReadModel) removePendingTool(toolCallID string) {
	if toolCallID == "" {
		panic("ongoing pending tool removal missing tool call id")
	}
	index, exists := m.pendingToolIndex[toolCallID]
	if !exists {
		return
	}
	delete(m.pendingToolIndex, toolCallID)
	m.pendingTools = append(m.pendingTools[:index], m.pendingTools[index+1:]...)
	for shifted := index; shifted < len(m.pendingTools); shifted++ {
		m.pendingToolIndex[m.pendingTools[shifted].id] = shifted
	}
	m.refreshPendingToolSection()
}

func (m *ongoingTranscriptReadModel) refreshPendingToolSection() {
	if len(m.pendingTools) == 0 {
		m.removeSection(ongoing.FrameSectionPendingTools)
		return
	}
	lines := make([]string, 0, len(m.pendingTools))
	for _, tool := range m.pendingTools {
		lines = append(lines, tool.label)
	}
	m.setSection(ongoing.FrameSectionPendingTools, lines)
}

func (c *ongoingTranscriptController) frameInput() ongoing.FrameInput {
	frame := c.frameProvider()
	sections := make([]ongoing.FrameSection, 0, len(c.liveReadModel.sectionOrder))
	for _, kind := range c.liveReadModel.sectionOrder {
		sections = append(sections, c.liveReadModel.sections[kind])
	}
	frame.Sections = append(sections, frame.Sections...)
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
