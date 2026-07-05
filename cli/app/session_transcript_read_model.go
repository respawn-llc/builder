package app

import (
	"strings"

	"core/cli/tui/ongoing"
	"core/cli/tui/transcriptrender"
	"core/shared/clientui"
)

type ongoingTranscriptReadModel struct {
	sectionOrder     []ongoing.FrameSectionKind
	sections         map[ongoing.FrameSectionKind]ongoing.FrameSection
	pendingTools     []ongoingPendingTool
	pendingToolIndex map[string]int
	queuedMessages   keyedOngoingLiveItems[clientui.TranscriptQueuedOrSteeredMessageState]
	pendingPrompts   keyedOngoingLiveItems[clientui.TranscriptPendingSessionPrompt]
	backgroundTasks  keyedOngoingLiveItems[clientui.TranscriptBackgroundActivity]
}

type ongoingPendingTool struct {
	id   string
	tool clientui.TranscriptToolStart
}

type keyedOngoingLiveItems[T any] struct {
	order []ongoingLiveItemID
	items map[ongoingLiveItemID]T
}

type ongoingLiveItemID string

func newOngoingTranscriptReadModel() ongoingTranscriptReadModel {
	return ongoingTranscriptReadModel{
		sections:         map[ongoing.FrameSectionKind]ongoing.FrameSection{},
		pendingToolIndex: map[string]int{},
		queuedMessages:   newKeyedOngoingLiveItems[clientui.TranscriptQueuedOrSteeredMessageState](),
		pendingPrompts:   newKeyedOngoingLiveItems[clientui.TranscriptPendingSessionPrompt](),
		backgroundTasks:  newKeyedOngoingLiveItems[clientui.TranscriptBackgroundActivity](),
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
	if index, exists := m.pendingToolIndex[tool.ToolCallID]; exists {
		m.pendingTools[index].tool = tool
		m.refreshPendingToolSection(80)
		return
	}
	m.pendingToolIndex[tool.ToolCallID] = len(m.pendingTools)
	m.pendingTools = append(m.pendingTools, ongoingPendingTool{id: tool.ToolCallID, tool: tool})
	m.refreshPendingToolSection(80)
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
	m.refreshPendingToolSection(80)
}

func (m *ongoingTranscriptReadModel) refreshPendingToolSection(width int) {
	if len(m.pendingTools) == 0 {
		m.removeSection(ongoing.FrameSectionPendingTools)
		return
	}
	if width <= 0 {
		width = 80
	}
	lines := make([]string, 0, len(m.pendingTools))
	for _, tool := range m.pendingTools {
		lines = append(lines, transcriptrender.RenderPendingTool(tool.tool, width).Plain())
	}
	m.setSection(ongoing.FrameSectionPendingTools, lines)
}

func (m *ongoingTranscriptReadModel) applyQueuedOrSteered(state *clientui.TranscriptQueuedOrSteeredMessageState) {
	if state == nil {
		return
	}
	id := queuedOrSteeredStateID(*state)
	if state.Status != clientui.QueuedUserMessageAccepted {
		m.queuedMessages.remove(id)
		m.refreshQueuedOrSteeredSection()
		return
	}
	m.queuedMessages.set(id, *state)
	m.refreshQueuedOrSteeredSection()
}

func (m *ongoingTranscriptReadModel) refreshQueuedOrSteeredSection() {
	m.setSection(ongoing.FrameSectionQueuedOrSteered, queuedOrSteeredListLines(m.queuedMessages.values()))
}

func queuedOrSteeredStateID(state clientui.TranscriptQueuedOrSteeredMessageState) ongoingLiveItemID {
	if id := strings.TrimSpace(state.QueueItemID); id != "" {
		return ongoingLiveItemID(id)
	}
	if id := strings.TrimSpace(state.ClientRequestID); id != "" {
		return ongoingLiveItemID(id)
	}
	panic("ongoing queued or steered message missing item id")
}

func (m *ongoingTranscriptReadModel) applyPendingPrompt(prompt *clientui.TranscriptPendingSessionPrompt) {
	if prompt == nil {
		return
	}
	id := parseOngoingLiveItemID(prompt.ID, "pending prompt")
	if prompt.State != clientui.TranscriptPromptPending {
		m.pendingPrompts.remove(id)
		m.refreshPendingPromptSection()
		return
	}
	m.pendingPrompts.set(id, *prompt)
	m.refreshPendingPromptSection()
}

func (m *ongoingTranscriptReadModel) refreshPendingPromptSection() {
	m.setSection(ongoing.FrameSectionPendingPrompt, pendingPromptListLines(m.pendingPrompts.values()))
}

func (m *ongoingTranscriptReadModel) applyBackgroundActivity(activity *clientui.TranscriptBackgroundActivity) {
	if activity == nil {
		return
	}
	id := parseOngoingLiveItemID(activity.ID, "background activity")
	if activity.Removed {
		m.backgroundTasks.remove(id)
		m.refreshBackgroundActivitySection()
		return
	}
	m.backgroundTasks.set(id, *activity)
	m.refreshBackgroundActivitySection()
}

func (m *ongoingTranscriptReadModel) refreshBackgroundActivitySection() {
	m.setSection(ongoing.FrameSectionBackgroundActivity, backgroundActivityListLines(m.backgroundTasks.values()))
}

func newKeyedOngoingLiveItems[T any]() keyedOngoingLiveItems[T] {
	return keyedOngoingLiveItems[T]{items: map[ongoingLiveItemID]T{}}
}

func (items *keyedOngoingLiveItems[T]) set(id ongoingLiveItemID, value T) {
	if _, exists := items.items[id]; !exists {
		items.order = append(items.order, id)
	}
	items.items[id] = value
}

func (items *keyedOngoingLiveItems[T]) remove(id ongoingLiveItemID) {
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

func (items keyedOngoingLiveItems[T]) values() []T {
	out := make([]T, 0, len(items.order))
	for _, id := range items.order {
		if value, exists := items.items[id]; exists {
			out = append(out, value)
		}
	}
	return out
}

func parseOngoingLiveItemID(raw string, label string) ongoingLiveItemID {
	id := strings.TrimSpace(raw)
	if id == "" {
		panic("ongoing " + label + " missing id")
	}
	return ongoingLiveItemID(id)
}
