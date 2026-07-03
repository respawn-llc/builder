package tui

// Deprecated emergency path: this file survives tui-redesign demolition only
// as the temporary non-native transcript snapshot helper for this branch. Do not extend.

import (
	"strings"
)

type PendingSpinnerFrameFunc func(entry TranscriptEntry, entryIndex int) string

func RenderPendingOngoingSnapshotLinesWithSpinnerFrames(entries []TranscriptEntry, theme string, width int, spinnerForEntry PendingSpinnerFrameFunc) []TranscriptProjectionLine {
	return renderPendingOngoingSnapshotProjection(entries, theme, width, spinnerForEntry).Lines(TranscriptDivider)
}

func uniformPendingSpinnerFrame(spinner string) PendingSpinnerFrameFunc {
	return func(TranscriptEntry, int) string {
		return spinner
	}
}

func renderPendingOngoingSnapshotProjection(entries []TranscriptEntry, theme string, width int, spinnerForEntry PendingSpinnerFrameFunc) TranscriptProjection {
	if len(entries) == 0 {
		return TranscriptProjection{}
	}
	if width <= 0 {
		width = 120
	}
	model := transcriptProjectionRenderer(theme, width, 0)
	model.toolSymbolGap = 2
	model.transcriptInput.Entries = append([]TranscriptEntry(nil), entries...)
	blocks := model.buildTranscriptBlocks(transcriptBlockOptions{
		mode:             transcriptBlockModeOngoing,
		includeStreaming: false,
		applySelection:   true,
	})
	blocks = model.applyPendingSpinner(blocks, entries, spinnerForEntry)
	if len(blocks) == 0 {
		return TranscriptProjection{}
	}
	return projectionFromOngoingBlocks(blocks)
}

func (m Model) applyPendingSpinner(blocks []ongoingBlock, entries []TranscriptEntry, spinnerForEntry PendingSpinnerFrameFunc) []ongoingBlock {
	if spinnerForEntry == nil {
		return blocks
	}
	consumedResults := make(map[int]struct{})
	resultIndex := buildToolResultIndex(entries)
	out := make([]ongoingBlock, 0, len(blocks))
	for _, block := range blocks {
		if !m.shouldRenderPendingSpinner(block, entries, consumedResults, resultIndex) {
			out = append(out, block)
			continue
		}
		if block.entryIndex < 0 || block.entryIndex >= len(entries) {
			out = append(out, block)
			continue
		}
		spinner := spinnerForEntry(entries[block.entryIndex], block.entryIndex)
		if strings.TrimSpace(spinner) == "" {
			if isAskQuestionToolCall(entries[block.entryIndex].ToolCall) {
				if rebuilt, ok := m.renderPendingSpinnerBlock(block, entries, ""); ok {
					out = append(out, rebuilt)
					continue
				}
			}
			out = append(out, block)
			continue
		}
		spinnerSymbol := styleForRole(block.role, m.palette()).Render(spinner) + " "
		rebuilt, ok := m.renderPendingSpinnerBlock(block, entries, spinnerSymbol)
		if !ok {
			out = append(out, block)
			continue
		}
		out = append(out, rebuilt)
	}
	return out
}

func (m Model) renderPendingSpinnerBlock(block ongoingBlock, entries []TranscriptEntry, spinnerSymbol string) (ongoingBlock, bool) {
	if block.entryIndex < 0 || block.entryIndex >= len(entries) {
		return ongoingBlock{}, false
	}
	entry := entries[block.entryIndex]
	if TranscriptRoleFromWire(string(entry.Role)) != TranscriptRoleToolCall {
		return ongoingBlock{}, false
	}
	lines := block.lines
	if isAskQuestionToolCall(entry.ToolCall) {
		question, _, _ := askQuestionDisplay(entry.ToolCall, entry.Text)
		lines = m.flattenPendingAskQuestionEntryWithSymbol(block.role, question, spinnerSymbol)
	} else {
		combined := m.toolCallDisplayText(entry, block.role, transcriptBlockOptions{mode: transcriptBlockModeOngoing})
		lines = m.flattenEntryWithMetaAndSymbol(block.role, combined, true, entry.ToolCall, spinnerSymbol)
	}
	lines = m.ongoingToolWithTreeGuideWithSymbol(block.role, lines, spinnerSymbol)
	return ongoingBlock{role: block.role, lines: lines, entryIndex: block.entryIndex, entryEnd: block.entryEnd}, true
}

func (m Model) flattenPendingAskQuestionEntryWithSymbol(role RenderIntent, question string, symbolOverride string) []string {
	renderWidth := m.entryRenderWidth(role, symbolOverride)
	if renderWidth < 1 {
		renderWidth = 1
	}
	question = strings.TrimSpace(question)
	if question == "" {
		question = "ask question"
	}
	lines := RenderInlineAskQuestionMarkdownLines(question, m.theme, renderWidth)
	text := firstNonEmptyLine(lines)
	if text == "" {
		text = "ask question"
	}
	forceEllipsis := len(lines) > 1
	text = truncateRenderedLineToWidthWithEllipsis(text, renderWidth, forceEllipsis)
	prefix := m.entryPrefix(role, symbolOverride)
	if prefix == "" {
		return []string{text}
	}
	return []string{prefix + text}
}

func firstNonEmptyLine(lines []string) string {
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	if len(lines) > 0 {
		return lines[0]
	}
	return ""
}

func (m Model) shouldRenderPendingSpinner(block ongoingBlock, entries []TranscriptEntry, consumedResults map[int]struct{}, resultIndex toolResultIndex) bool {
	if !block.role.IsToolHeadline() || len(block.lines) == 0 {
		return false
	}
	if block.entryIndex < 0 || block.entryIndex >= len(entries) {
		return false
	}
	entry := entries[block.entryIndex]
	if TranscriptRoleFromWire(string(entry.Role)) != TranscriptRoleToolCall {
		return false
	}
	resultIdx := resultIndex.findMatchingToolResultIndex(entries, block.entryIndex, consumedResults)
	if resultIdx < 0 {
		return true
	}
	consumedResults[resultIdx] = struct{}{}
	return false
}
