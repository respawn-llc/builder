package ongoing

import (
	"io"
	"strings"

	"core/cli/tui/transcriptrender"
)

type terminalRowRange struct {
	start int
	end   int
}

// previousMutableBandHeightAtBottom is the part of the previous band that
// still occupies the target bottom edge. On expansion terminal reflow owns the
// previous screen, while the logical band height remains unchanged so new rows
// never become mutable-band padding.
func (s *Surface) previousMutableBandHeightAtBottom(target Size) int {
	if s.lastPaintedSize == nil || s.retainedBandHeight <= 0 {
		return 0
	}
	if target.Height >= s.lastPaintedSize.Height {
		return min(s.retainedBandHeight, target.Height)
	}
	previous := s.visiblePreviousMutableBand(target)
	if previous == nil {
		return 0
	}
	return previous.end - previous.start + 1
}

func (s *Surface) visiblePreviousMutableBand(target Size) *terminalRowRange {
	if s.lastPaintedSize == nil || s.retainedBandHeight <= 0 {
		return nil
	}
	start := s.lastPaintedSize.Height - s.retainedBandHeight + 1
	end := min(s.lastPaintedSize.Height, target.Height)
	if start > end {
		return nil
	}
	return &terminalRowRange{start: start, end: end}
}

func (s *Surface) terminalHeightExpanded(target Size) bool {
	return s.lastPaintedSize != nil && target.Height > s.lastPaintedSize.Height
}

func (s *Surface) validateRenderFrame(frame FrameInput, operation string) {
	if frame.Size.Width <= 0 || frame.Size.Height <= 0 {
		panicOngoingDeveloperError(operation, "invalid terminal geometry", map[string]any{
			"width":  frame.Size.Width,
			"height": frame.Size.Height,
		})
	}
	if frame.Cursor.Visible {
		if frame.Cursor.Row <= 0 || frame.Cursor.Row > frame.Size.Height || frame.Cursor.Column <= 0 || frame.Cursor.Column > frame.Size.Width {
			panicOngoingDeveloperError(operation, "invalid cursor", map[string]any{
				"row":    frame.Cursor.Row,
				"column": frame.Cursor.Column,
				"width":  frame.Size.Width,
				"height": frame.Size.Height,
			})
		}
	}
}

func (s *Surface) writeFrameTransaction(frame FrameInput, immutableRows []string) (Result, error) {
	liveLayout := s.liveBandLayout(frame)
	liveLines := liveBandLineTexts(liveLayout)
	if !s.minimumLiveBandFits(frame, liveLines) {
		liveLayout = nil
		frame.Cursor = Cursor{}
	} else {
		liveLayout = s.shrinkLiveBandLayoutToFrame(frame, liveLayout)
	}
	liveLines = liveBandLineTexts(liveLayout)
	frame.Cursor = cursorForVisibleLiveBand(frame.Cursor, frame.Size.Height, liveLayout)
	if len(immutableRows) > 0 && len(liveLines) >= frame.Size.Height {
		liveLines = nil
		frame.Cursor = Cursor{}
	}
	previousHeight := s.previousMutableBandHeightAtBottom(frame.Size)
	contentHeight := len(liveLines)
	releasableHeight := max(0, previousHeight-contentHeight)
	releasedHeight := min(len(immutableRows), releasableHeight)
	nextHeight := max(contentHeight, previousHeight-releasedHeight)
	validateFrameTransactionGeometry(frame, previousHeight, contentHeight, nextHeight, len(immutableRows))
	eraseHeight := max(previousHeight, nextHeight)
	var transaction strings.Builder
	transaction.WriteString(resetScrollRegionAndOriginMode())
	transaction.WriteString(semanticOutputSequence())
	if s.terminalHeightExpanded(frame.Size) {
		previous := s.visiblePreviousMutableBand(frame.Size)
		if s.terminalResize.usesWidthRehydration() && previous != nil {
			writeMutableRowsErase(&transaction, previous.start, previous.end)
		}
		if s.terminalResize.bottomAnchorsVerticalExpansion() && s.immutableScrollbackProduced() {
			writeImmutableRegionScrollForTerminalExpansion(
				&transaction,
				s.lastPaintedSize.Height,
				frame.Size.Height,
				previousHeight,
			)
		}
	}
	if nextHeight > previousHeight && s.immutableScrollbackProduced() {
		writeImmutableRegionScrollForLiveBandGrowth(&transaction, frame.Size.Height, previousHeight, nextHeight)
	}
	writeMutableBandErase(&transaction, frame.Size.Height, eraseHeight)
	writeImmutableRowsAboveMutableBand(
		&transaction,
		frame.Size.Height,
		previousHeight,
		nextHeight,
		immutableRows,
	)
	writeRetainedMutableBand(
		&transaction,
		frame.Size.Height,
		nextHeight,
		liveLines,
		s.terminalResize == TerminalResizeSemanticPrompt,
	)
	writeCursor(&transaction, frame.Cursor)
	if _, err := io.WriteString(s.writer, transaction.String()); err != nil {
		return Result{}, err
	}
	s.retainedBandHeight = nextHeight
	paintedSize := frame.Size
	s.lastPaintedSize = &paintedSize
	return Result{}, nil
}

func validateFrameTransactionGeometry(
	frame FrameInput,
	previousHeight int,
	contentHeight int,
	nextHeight int,
	immutableRowCount int,
) {
	valid := previousHeight >= 0 &&
		contentHeight >= 0 &&
		contentHeight <= nextHeight &&
		nextHeight <= frame.Size.Height
	if immutableRowCount > 0 {
		valid = valid && nextHeight < frame.Size.Height
	}
	if frame.Cursor.Visible {
		valid = valid &&
			frame.Cursor.Row > 0 &&
			frame.Cursor.Row <= frame.Size.Height &&
			frame.Cursor.Column > 0 &&
			frame.Cursor.Column <= frame.Size.Width
	}
	if valid {
		return
	}
	sectionLineCounts := make(map[FrameSectionKind]int, len(frame.Sections))
	for _, section := range frame.Sections {
		sectionLineCounts[section.Kind] += len(section.Lines) + len(section.StyledLines)
	}
	panicOngoingDeveloperError("write_frame_transaction", "invalid frame transaction geometry", map[string]any{
		"width":               frame.Size.Width,
		"height":              frame.Size.Height,
		"previous_height":     previousHeight,
		"content_height":      contentHeight,
		"next_height":         nextHeight,
		"immutable_row_count": immutableRowCount,
		"section_line_counts": sectionLineCounts,
		"cursor":              frame.Cursor,
	})
}

func (s *Surface) minimumLiveBandFits(frame FrameInput, liveLines []string) bool {
	return enforcedMinimumLiveBandHeight(frame, s.activeAssistant, liveLines) <= frame.Size.Height
}

func (s *Surface) liveBandLayout(frame FrameInput) []liveBandLine {
	lines := activeAssistantLinesWithLinkPresentation(
		s.activeAssistant,
		frameWidthOrDefault(frame),
		frame.Theme,
		s.markdownLinks,
	)
	layout := make([]liveBandLine, 0, len(lines)+len(frame.Sections))
	for _, line := range lines {
		layout = append(layout, liveBandLine{text: line})
	}
	layout = append(layout, frameLines(frame)...)
	return layout
}

func (s *Surface) shrinkLiveBandLayoutToFrame(frame FrameInput, liveLayout []liveBandLine) []liveBandLine {
	if len(liveLayout) <= frame.Size.Height {
		return liveLayout
	}
	lines := minimumLiveBandLayoutWithLinkPresentation(frame, s.activeAssistant, s.markdownLinks)
	if len(lines) > frame.Size.Height {
		return lines[len(lines)-frame.Size.Height:]
	}
	return lines
}

func minimumLiveBandLayoutWithLinkPresentation(
	frame FrameInput,
	assistant activeAssistantState,
	linkPresentation transcriptrender.MarkdownLinkPresentation,
) []liveBandLine {
	var lines []string
	if assistant.source != "" {
		assistantLines := activeAssistantLinesWithLinkPresentation(
			assistant,
			frameWidthOrDefault(frame),
			frame.Theme,
			linkPresentation,
		)
		if len(assistantLines) > 0 {
			lines = append(lines, assistantLines[len(assistantLines)-1])
		}
	}
	layout := make([]liveBandLine, 0, len(lines)+len(frame.Sections))
	for _, line := range lines {
		layout = append(layout, liveBandLine{text: line})
	}
	for _, section := range frame.Sections {
		totalLines := len(section.StyledLines) + len(section.Lines)
		if totalLines == 0 {
			continue
		}
		limit := 1
		switch section.Kind {
		case FrameSectionQueuedOrSteered:
			limit = min(2, totalLines)
		case FrameSectionInput:
			limit = min(3, totalLines)
		}
		start := 0
		if section.Kind == FrameSectionInput && totalLines > limit {
			start = totalLines - limit
		}
		for index := start; index < start+limit; index++ {
			text := ""
			if index < len(section.StyledLines) {
				text = encodeTranscriptLine(section.StyledLines[index], frame.Theme)
			} else {
				text = section.Lines[index-len(section.StyledLines)]
			}
			layout = append(layout, liveBandLine{
				text:        text,
				sectionKind: section.Kind,
				sectionRow:  index + 1,
			})
		}
	}
	return layout
}

func activeAssistantLinesWithLinkPresentation(
	state activeAssistantState,
	width int,
	themeName string,
	linkPresentation transcriptrender.MarkdownLinkPresentation,
) []string {
	if state.source == "" {
		return nil
	}
	projection := newMarkdownProjector(nil, themeName, linkPresentation).Project(markdownProjectionInput{
		Source:           state.source,
		Width:            width,
		PromotedBoundary: state.promotedSourceBoundary,
	})
	if projection.ProjectionFailure != nil {
		panicOngoingDeveloperError("assistant_tail_render", "markdown projection instability", map[string]any{
			"source_boundary":    projection.ProjectionFailure.SourceBoundary,
			"candidate_boundary": projection.ProjectionFailure.CandidateBoundary,
			"row_index":          projection.ProjectionFailure.RowIndex,
			"width":              projection.ProjectionFailure.Width,
		})
	}
	return projection.VolatileRows
}

func enforcedMinimumLiveBandHeight(frame FrameInput, assistant activeAssistantState, liveLines []string) int {
	if len(liveLines) == 0 {
		return 0
	}
	total := 0
	if assistant.source != "" {
		total++
	}
	for _, section := range frame.Sections {
		totalLines := len(section.StyledLines) + len(section.Lines)
		if totalLines == 0 {
			continue
		}
		switch section.Kind {
		case FrameSectionPendingTools:
			total++
		case FrameSectionQueuedOrSteered:
			total += min(2, totalLines)
		case FrameSectionInput:
			total += min(3, totalLines)
		case FrameSectionStatus:
			total++
		default:
			total++
		}
	}
	if total == 0 {
		return len(liveLines)
	}
	return total
}

func frameWidthOrDefault(frame FrameInput) int {
	if frame.Size.Width > 0 {
		return frame.Size.Width
	}
	return 80
}

func frameLines(frame FrameInput) []liveBandLine {
	var lines []liveBandLine
	for _, section := range frame.Sections {
		for index, line := range section.StyledLines {
			lines = append(lines, liveBandLine{
				text:        encodeTranscriptLine(line, frame.Theme),
				sectionKind: section.Kind,
				sectionRow:  index + 1,
			})
		}
		for index, line := range section.Lines {
			lines = append(lines, liveBandLine{
				text:        line,
				sectionKind: section.Kind,
				sectionRow:  len(section.StyledLines) + index + 1,
			})
		}
	}
	return lines
}

func liveBandLineTexts(layout []liveBandLine) []string {
	if len(layout) == 0 {
		return nil
	}
	lines := make([]string, 0, len(layout))
	for _, line := range layout {
		lines = append(lines, line.text)
	}
	return lines
}

func cursorForVisibleLiveBand(cursor Cursor, terminalHeight int, layout []liveBandLine) Cursor {
	if !cursor.Visible || cursor.Target == nil {
		return cursor
	}
	for index, line := range layout {
		if line.sectionKind == cursor.Target.SectionKind && line.sectionRow == cursor.Target.Row {
			cursor.Row = terminalHeight - len(layout) + index + 1
			return cursor
		}
	}
	cursor.Visible = false
	return cursor
}
