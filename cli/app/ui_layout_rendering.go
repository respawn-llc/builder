package app

import (
	"fmt"
	"strings"

	"core/cli/tui"

	"github.com/charmbracelet/lipgloss"
)

type queuedPaneEntryKind uint8

const (
	queuedPaneEntryQueued queuedPaneEntryKind = iota
	queuedPaneEntryPending
)

type queuedPaneEntry struct {
	Text string
	Kind queuedPaneEntryKind
}

func (l uiViewLayout) renderChatPanel(width, height int, style uiStyles) []string {
	switch l.model.surface() {
	case uiSurfaceStatus:
		return l.renderStatusOverlay(width, height, style)
	case uiSurfaceGoal:
		return l.renderGoalOverlay(width, height, style)
	case uiSurfaceWorktree:
		return l.renderWorktreeOverlay(width, height, style)
	case uiSurfaceProcessList:
		return l.renderProcessList(width, height, style)
	}
	if width < 1 {
		return []string{padRight("", width)}
	}
	contentLines := append([]string(nil), splitPlainLines(l.model.view.View())...)
	lineKinds := l.model.view.VisibleLineKinds()
	if len(contentLines) < height {
		for len(contentLines) < height {
			contentLines = append(contentLines, "")
			lineKinds = append(lineKinds, tui.VisibleLineContent)
		}
	} else if len(contentLines) > height {
		end := len(contentLines)
		for end > 0 && strings.TrimSpace(contentLines[end-1]) == "" {
			end--
		}
		if end < height {
			end = height
		}
		start := end - height
		if start < 0 {
			start = 0
		}
		contentLines = contentLines[start:end]
		if len(lineKinds) > start {
			if end > len(lineKinds) {
				end = len(lineKinds)
			}
			lineKinds = lineKinds[start:end]
		}
	}
	return l.renderChatContentLines(contentLines, lineKinds, width, style)
}

func (l uiViewLayout) renderChatContentLines(rawLines []string, lineKinds []tui.VisibleLineKind, width int, style uiStyles) []string {
	contentWidth := width
	if contentWidth < 1 {
		contentWidth = 1
	}
	out := make([]string, 0, len(rawLines))
	for idx, line := range rawLines {
		kind := tui.VisibleLineContent
		if idx < len(lineKinds) {
			kind = lineKinds[idx]
		}
		if kind == tui.VisibleLineDivider {
			out = append(out, style.meta.Render(strings.Repeat("─", contentWidth)))
			continue
		}
		out = append(out, style.chat.Render(padANSIRight(line, contentWidth)))
	}
	return out
}

func (l uiViewLayout) renderActivePicker(width int) []string {
	m := l.model
	state := m.activePickerPresentation()
	if !state.visible || width < 1 || state.lineCount <= 0 {
		return nil
	}
	palette := uiPalette(m.theme)
	selectedStyle := lipgloss.NewStyle().Foreground(palette.primary)
	selectedBoldStyle := selectedStyle.Bold(true)
	unselectedStyle := lipgloss.NewStyle()
	unselectedBoldStyle := lipgloss.NewStyle().Bold(true)
	descriptionStyle := lipgloss.NewStyle().Foreground(palette.muted).Faint(true)
	out := make([]string, 0, state.lineCount)
	for row := 0; row < state.lineCount; row++ {
		idx := state.start + row
		line := ""
		if idx < len(state.rows) {
			item := state.rows[idx]
			if item.muted && !item.selectable {
				line = descriptionStyle.Render(truncateQueuedMessageLine(item.primary, width))
			} else {
				rowStyle := unselectedStyle
				if item.boldPrimary {
					rowStyle = unselectedBoldStyle
				}
				if item.selectable && idx == state.selection {
					rowStyle = selectedStyle
					if item.boldPrimary {
						rowStyle = selectedBoldStyle
					}
				}
				primary := item.primary
				if item.secondary == "" {
					primary = truncateQueuedMessageLine(primary, width)
				}
				line = rowStyle.Render(primary)
				if item.secondary != "" {
					line += " - " + descriptionStyle.Render(item.secondary)
				}
			}
		}
		line = truncateANSIRight(line, width)
		out = append(out, padANSIRight(line, width))
	}
	return out
}

func (l uiViewLayout) renderQueuedMessagesPane(width int) []string {
	if width < 1 {
		return nil
	}
	visible, hidden := l.queuedVisibleMessages()
	if len(visible) == 0 {
		return nil
	}
	palette := uiPalette(l.model.theme)
	queueStyle := lipgloss.NewStyle().Foreground(palette.secondary).Faint(true)
	pendingStyle := lipgloss.NewStyle().Foreground(palette.primary)
	out := make([]string, 0, len(visible)+1)
	if hidden > 0 {
		out = append(out, queueStyle.Render(padANSIRight(fmt.Sprintf("%d more messages", hidden), width)))
	}
	for _, entry := range visible {
		line := truncateQueuedMessageLine(entry.displayText(), width)
		style := queueStyle
		if entry.Kind == queuedPaneEntryPending {
			style = pendingStyle
		}
		out = append(out, style.Render(padANSIRight(line, width)))
	}
	return out
}

func (l uiViewLayout) queuedPaneLineCount() int {
	visible, hidden := l.queuedVisibleMessages()
	if len(visible) == 0 {
		return 0
	}
	if hidden > 0 {
		return len(visible) + 1
	}
	return len(visible)
}

func (l uiViewLayout) queuedVisibleMessages() ([]queuedPaneEntry, int) {
	entries := l.queuedMessages()
	total := len(entries)
	if total == 0 {
		return nil, 0
	}
	start := 0
	if total > queuedMessagesLimit {
		start = total - queuedMessagesLimit
	}
	visible := entries[start:]
	return visible, total - len(visible)
}

func (l uiViewLayout) queuedMessages() []queuedPaneEntry {
	deferredPending := l.model.deferredPendingInjectedMessages()
	entries := make([]queuedPaneEntry, 0, len(l.model.queued)+len(deferredPending)+len(l.model.pendingInjected))
	for _, message := range l.model.queued {
		entries = append(entries, queuedPaneEntry{Text: message.Text, Kind: queuedPaneEntryQueued})
	}
	for _, message := range deferredPending {
		entries = append(entries, queuedPaneEntry{Text: message, Kind: queuedPaneEntryPending})
	}
	for _, message := range l.model.pendingInjected {
		entries = append(entries, queuedPaneEntry{Text: message.Text, Kind: queuedPaneEntryPending})
	}
	return entries
}

func (m *uiModel) deferredPendingInjectedMessages() []string {
	if m == nil || len(m.deferredCommittedTail) == 0 {
		return nil
	}
	messages := make([]string, 0, len(m.deferredCommittedTail))
	for _, deferred := range m.deferredCommittedTail {
		messages = append(messages, deferred.pending...)
	}
	if len(messages) == 0 {
		return nil
	}
	return messages
}

func (e queuedPaneEntry) displayText() string {
	if e.Kind == queuedPaneEntryPending {
		return "next: " + e.Text
	}
	return e.Text
}
