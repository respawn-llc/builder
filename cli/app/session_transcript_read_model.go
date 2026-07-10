package app

import (
	"fmt"
	"log"
	"runtime/debug"
	"strings"

	"core/cli/tui/ongoing"
	"core/cli/tui/transcriptrender"
	"core/shared/clientui"

	"github.com/google/uuid"
)

type ongoingTranscriptReadModel struct {
	sectionOrder     []ongoing.FrameSectionKind
	sections         map[ongoing.FrameSectionKind]ongoing.FrameSection
	pendingTools     []ongoingPendingTool
	pendingToolIndex map[string]int
	queuedMessages   keyedOngoingLiveItems[ongoingLiveItemID, ongoingLiveInput]
	pendingPrompts   keyedOngoingLiveItems[ongoingPromptID, clientui.TranscriptPendingSessionPrompt]
	backgroundTasks  keyedOngoingLiveItems[ongoingLiveItemID, clientui.TranscriptBackgroundActivity]
}

type ongoingPendingTool struct {
	id   string
	tool clientui.TranscriptToolStart
}

type keyedOngoingLiveItems[K comparable, T any] struct {
	order []K
	items map[K]T
}

type ongoingLiveItemID uuid.UUID

type ongoingPromptID string

func newOngoingTranscriptReadModel() ongoingTranscriptReadModel {
	return ongoingTranscriptReadModel{
		sections:         map[ongoing.FrameSectionKind]ongoing.FrameSection{},
		pendingToolIndex: map[string]int{},
		queuedMessages:   newKeyedOngoingLiveItems[ongoingLiveItemID, ongoingLiveInput](),
		pendingPrompts:   newKeyedOngoingLiveItems[ongoingPromptID, clientui.TranscriptPendingSessionPrompt](),
		backgroundTasks:  newKeyedOngoingLiveItems[ongoingLiveItemID, clientui.TranscriptBackgroundActivity](),
	}
}

func (m *ongoingTranscriptReadModel) reset() {
	*m = newOngoingTranscriptReadModel()
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

func (m *ongoingTranscriptReadModel) setStyledSection(kind ongoing.FrameSectionKind, lines []transcriptrender.Line) {
	if len(lines) == 0 {
		m.removeSection(kind)
		return
	}
	if _, exists := m.sections[kind]; !exists {
		m.sectionOrder = append(m.sectionOrder, kind)
	}
	m.sections[kind] = ongoing.FrameSection{Kind: kind, StyledLines: append([]transcriptrender.Line(nil), lines...)}
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
		panicOngoingTranscriptReadModelDeveloperError("pending_tool_start", "missing tool call id", map[string]any{
			"tool_name": tool.ToolName,
		})
	}
	if index, exists := m.pendingToolIndex[tool.ToolCallID]; exists {
		m.pendingTools[index].tool = tool
		m.refreshPendingToolSection(80, 0)
		return
	}
	m.pendingToolIndex[tool.ToolCallID] = len(m.pendingTools)
	m.pendingTools = append(m.pendingTools, ongoingPendingTool{id: tool.ToolCallID, tool: tool})
	m.refreshPendingToolSection(80, 0)
}

func (m *ongoingTranscriptReadModel) removePendingTool(toolCallID string) {
	if toolCallID == "" {
		panicOngoingTranscriptReadModelDeveloperError("pending_tool_remove", "missing tool call id", nil)
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
	m.refreshPendingToolSection(80, 0)
}

func (m *ongoingTranscriptReadModel) refreshPendingToolSection(width int, spinnerFrame int) {
	if len(m.pendingTools) == 0 {
		m.removeSection(ongoing.FrameSectionPendingTools)
		return
	}
	if width <= 0 {
		width = 80
	}
	lines := make([]transcriptrender.Line, 0, len(m.pendingTools))
	for _, tool := range m.pendingTools {
		lines = append(lines, transcriptrender.RenderPendingTool(tool.tool, width, padSpinnerIndicator(pendingToolSpinnerFrame(spinnerFrame))))
	}
	m.setStyledSection(ongoing.FrameSectionPendingTools, lines)
}

func (m *ongoingTranscriptReadModel) applyQueuedOrSteered(state *clientui.TranscriptQueuedOrSteeredMessageState) {
	if state == nil {
		return
	}
	id := queuedOrSteeredStateID(*state)
	if state.Status != clientui.QueuedUserMessageAccepted {
		m.queuedMessages.remove(id)
		m.refreshQueuedOrSteeredSection(80)
		return
	}
	m.queuedMessages.set(id, ongoingLiveInput{
		Text:        queuedOrSteeredText(state),
		Disposition: ongoingLiveInputSteering,
	})
	m.refreshQueuedOrSteeredSection(80)
}

func (m *ongoingTranscriptReadModel) refreshQueuedOrSteeredSection(width int) {
	m.setStyledSection(ongoing.FrameSectionQueuedOrSteered, renderOngoingLiveInputLines(m.queuedMessages.values(), width))
}

func queuedOrSteeredStateID(state clientui.TranscriptQueuedOrSteeredMessageState) ongoingLiveItemID {
	if strings.TrimSpace(state.QueueItemID) != "" {
		return parseOngoingLiveItemID(state.QueueItemID, "queued message queue item")
	}
	if strings.TrimSpace(state.ClientRequestID) != "" {
		return parseOngoingLiveItemID(state.ClientRequestID, "queued message client request")
	}
	return parseOngoingLiveItemID("", "queued message queue item or client request")
}

func (m *ongoingTranscriptReadModel) applyPendingPrompt(prompt *clientui.TranscriptPendingSessionPrompt) {
	if prompt == nil {
		return
	}
	id := parseOngoingPromptID(prompt.ID)
	if prompt.State != clientui.TranscriptPromptPending {
		m.pendingPrompts.remove(id)
		m.refreshPendingPromptSection(80)
		return
	}
	m.pendingPrompts.set(id, *prompt)
	m.refreshPendingPromptSection(80)
}

func (m *ongoingTranscriptReadModel) refreshPendingPromptSection(width int) {
	m.setSection(ongoing.FrameSectionPendingPrompt, terminalSafeFrameLinesForWidth(pendingPromptListLines(m.pendingPrompts.values()), width))
}

func (m *ongoingTranscriptReadModel) applyBackgroundActivity(activity *clientui.TranscriptBackgroundActivity) {
	if activity == nil {
		return
	}
	id := parseOngoingLiveItemID(activity.ID, "background activity")
	if activity.Removed {
		m.backgroundTasks.remove(id)
		m.refreshBackgroundActivitySection(80)
		return
	}
	m.backgroundTasks.set(id, *activity)
	m.refreshBackgroundActivitySection(80)
}

func (m *ongoingTranscriptReadModel) refreshBackgroundActivitySection(width int) {
	activities := m.backgroundTasks.values()
	lines := make([]transcriptrender.Line, 0, len(activities))
	for _, activity := range activities {
		command := strings.TrimSpace(activity.Command)
		if command == "" {
			command = strings.TrimSpace(activity.Preview)
		}
		lines = append(lines, transcriptrender.RenderBackgroundedShell(command, width))
	}
	m.setStyledSection(ongoing.FrameSectionBackgroundActivity, lines)
}

func newKeyedOngoingLiveItems[K comparable, T any]() keyedOngoingLiveItems[K, T] {
	return keyedOngoingLiveItems[K, T]{items: map[K]T{}}
}

func (items *keyedOngoingLiveItems[K, T]) set(id K, value T) {
	if _, exists := items.items[id]; !exists {
		items.order = append(items.order, id)
	}
	items.items[id] = value
}

func (items *keyedOngoingLiveItems[K, T]) remove(id K) {
	if _, exists := items.items[id]; !exists {
		return
	}
	delete(items.items, id)
	filtered := items.order[:0]
	for _, current := range items.order {
		if current != id {
			filtered = append(filtered, current)
		}
	}
	items.order = filtered
}

func (items keyedOngoingLiveItems[K, T]) values() []T {
	out := make([]T, 0, len(items.order))
	for _, id := range items.order {
		if value, exists := items.items[id]; exists {
			out = append(out, value)
		}
	}
	return out
}

func parseOngoingPromptID(raw string) ongoingPromptID {
	id := strings.TrimSpace(raw)
	if id == "" {
		panicOngoingTranscriptReadModelDeveloperError("pending_prompt_id", "missing id", nil)
	}
	return ongoingPromptID(id)
}

func parseOngoingLiveItemID(raw string, label string) ongoingLiveItemID {
	id := strings.TrimSpace(raw)
	if id == "" {
		panicOngoingTranscriptReadModelDeveloperError("live_item_id", "missing id", map[string]any{
			"label": label,
		})
	}
	parsed, err := uuid.Parse(id)
	if err != nil || parsed == uuid.Nil || parsed.Version() != 4 {
		panicOngoingTranscriptReadModelDeveloperError("live_item_id", "invalid UUIDv4 id", map[string]any{
			"label": label,
			"id":    id,
		})
	}
	return ongoingLiveItemID(parsed)
}

func panicOngoingTranscriptReadModelDeveloperError(operation, reason string, facts map[string]any) {
	err := fmt.Errorf("ongoing transcript read model developer error: operation=%s reason=%s facts=%v\n%s", operation, reason, facts, debug.Stack())
	log.Printf("%s", err.Error())
	panic(err)
}
