package app

import (
	"fmt"
	"strings"

	"core/cli/tui/transcriptrender"

	"github.com/charmbracelet/lipgloss"
)

type queuedPaneEntry struct {
	ongoingLiveInput
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
	if len(contentLines) < height {
		for len(contentLines) < height {
			contentLines = append(contentLines, "")
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
	}
	return l.renderChatContentLines(contentLines, width, style)
}

func (l uiViewLayout) renderChatContentLines(rawLines []string, width int, style uiStyles) []string {
	contentWidth := width
	if contentWidth < 1 {
		contentWidth = 1
	}
	out := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
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
	lines := l.renderQueuedMessageLines(width)
	if len(lines) == 0 {
		return nil
	}
	palette := uiPalette(l.model.theme)
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		rendered := ""
		for _, span := range line.Spans {
			role, semantic := span.Style.Role()
			if !semantic {
				panic("queued pane received explicit RGB transcript span")
			}
			style := lipgloss.NewStyle()
			switch transcriptrender.ColorRoleForStyle(role) {
			case transcriptrender.ColorRolePrimary:
				style = style.Foreground(palette.primary)
			case transcriptrender.ColorRoleSecondary:
				style = style.Foreground(palette.secondary)
			default:
				panic(fmt.Sprintf("queued pane received unsupported transcript style role %d", role))
			}
			if span.Style.Has(transcriptrender.SpanAttributeFaint) {
				style = style.Faint(true)
			}
			if span.Style.Has(transcriptrender.SpanAttributeBold) {
				style = style.Bold(true)
			}
			if span.Style.Has(transcriptrender.SpanAttributeItalic) {
				style = style.Italic(true)
			}
			if span.Style.Has(transcriptrender.SpanAttributeUnderline) {
				style = style.Underline(true)
			}
			rendered += style.Render(span.Text)
		}
		out = append(out, padANSIRight(rendered, width))
	}
	return out
}

func (l uiViewLayout) renderQueuedMessageLines(width int) []transcriptrender.Line {
	if width < 1 {
		return nil
	}
	visible, hidden := l.queuedVisibleMessages()
	if len(visible) == 0 {
		return nil
	}
	inputs := make([]ongoingLiveInput, 0, len(visible)+1)
	if hidden > 0 {
		inputs = append(inputs, ongoingLiveInput{Text: fmt.Sprintf("%d more messages", hidden), Disposition: ongoingLiveInputQueued})
	}
	for _, entry := range visible {
		inputs = append(inputs, entry.ongoingLiveInput)
	}
	return renderOngoingLiveInputLines(inputs, width)
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
	entries := make([]queuedPaneEntry, 0, len(l.model.queued)+len(l.model.injectedQueue))
	for _, message := range l.model.queued {
		entries = append(entries, queuedPaneEntry{ongoingLiveInput: ongoingLiveInput{Text: message.Text, Disposition: ongoingLiveInputQueued}})
	}
	for _, message := range l.model.injectedQueue {
		if !l.model.injectedQueueItemNeedsLocalPane(message) {
			continue
		}
		entries = append(entries, queuedPaneEntry{ongoingLiveInput: ongoingLiveInput{Text: message.Text, Disposition: ongoingLiveInputSteering}})
	}
	return entries
}

func (m *uiModel) injectedQueueItemNeedsLocalPane(item injectedRuntimeQueueItem) bool {
	switch item.State {
	case injectedRuntimeQueuePendingCreate, injectedRuntimeQueueCreateFailed, injectedRuntimeQueueDiscardFailed:
		return true
	case injectedRuntimeQueueEnqueued:
		return m != nil && !m.hasRuntimeClient()
	default:
		return false
	}
}

func (e queuedPaneEntry) displayText() string {
	return e.Text
}
