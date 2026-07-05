package ongoing

import (
	"fmt"
	"io"
	"strings"

	"core/cli/tui/transcriptrender"
	"core/shared/clientui"
	"core/shared/transcript"
	"github.com/google/uuid"
)

type Size struct {
	Width  int
	Height int
}

type FrameInput struct {
	Size     Size
	Sections []FrameSection
	Cursor   Cursor
}

type Cursor struct {
	Visible bool
	Row     int
	Column  int
}

type FrameSection struct {
	Kind  FrameSectionKind
	Lines []string
}

type FrameSectionKind string

const (
	FrameSectionRuntimeActivity     FrameSectionKind = "runtime_activity"
	FrameSectionQueuedOrSteered     FrameSectionKind = "queued_or_steered"
	FrameSectionPendingPrompt       FrameSectionKind = "pending_prompt"
	FrameSectionContextUsage        FrameSectionKind = "context_usage"
	FrameSectionGoal                FrameSectionKind = "goal"
	FrameSectionStatus              FrameSectionKind = "status"
	FrameSectionInput               FrameSectionKind = "input"
	FrameSectionPicker              FrameSectionKind = "picker"
	FrameSectionHelp                FrameSectionKind = "help"
	FrameSectionPromptHistory       FrameSectionKind = "prompt_history"
	FrameSectionPendingTools        FrameSectionKind = "pending_tools"
	FrameSectionRunState            FrameSectionKind = "run_state"
	FrameSectionInputReconciliation FrameSectionKind = "input_reconciliation"
	FrameSectionSessionStatus       FrameSectionKind = "session_status"
	FrameSectionSessionIdentity     FrameSectionKind = "session_identity"
	FrameSectionCompaction          FrameSectionKind = "compaction"
	FrameSectionBackgroundActivity  FrameSectionKind = "background_activity"
)

type Result struct {
	Action ResultAction
	Reason RehydrateReason
}

type ResultAction string

const (
	ResultNoop                      ResultAction = ""
	ResultRequestScratchRehydration ResultAction = "request_scratch_rehydration"
	ResultScheduleWidthRehydration  ResultAction = "schedule_width_rehydration"
)

type RehydrateReason string

const (
	RehydrateReasonSequenceGap   RehydrateReason = "sequence_gap"
	RehydrateReasonQueueOverflow RehydrateReason = "queue_overflow"
	RehydrateReasonWidthChange   RehydrateReason = "width_change"
)

type Surface struct {
	writer             io.Writer
	previousBandHeight int
	dividerGroup       *clientui.TranscriptRowKind
	activeAssistant    activeAssistantState
	lastSize           Size
}

type activeAssistantState struct {
	streamID               *uuid.UUID
	source                 string
	promotedSourceBoundary int
}

func NewSurface(writers ...io.Writer) *Surface {
	writer := io.Discard
	if len(writers) > 0 && writers[0] != nil {
		writer = writers[0]
	}
	return &Surface{writer: writer}
}

func (s *Surface) ApplyTerminalMessage(message clientui.TranscriptMessage, frame FrameInput) (Result, error) {
	s.validateRenderFrame(frame, "apply_terminal_message")
	if message.Kind == clientui.TranscriptMessageHydration {
		return s.applyHydration(message, frame)
	}
	if message.Kind == clientui.TranscriptMessageAssistantDelta && message.AssistantDelta != nil {
		return s.applyAssistantDelta(message.AssistantDelta.StreamID, message.AssistantDelta.Delta, frame)
	}
	if message.Kind == clientui.TranscriptMessageAssistantStreamAbort && message.AssistantStreamAbort != nil {
		return s.abortAssistantStream(message.AssistantStreamAbort.StreamID, frame)
	}
	if isAssistantFinalization(message) {
		return s.finalizeAssistantStream(*message.CommittedRow.Assistant.StreamID, message.CommittedRow.Assistant.Text, frame)
	}
	lines := s.immutableLines(message, frame.Size.Width, "")
	if len(lines) == 0 {
		return Result{}, nil
	}
	return s.writeFrameTransaction(frame, lines)
}

func (s *Surface) applyHydration(message clientui.TranscriptMessage, frame FrameInput) (Result, error) {
	if message.Hydration == nil {
		return Result{}, nil
	}
	lines := s.hydrationImmutableLines(*message.Hydration, frame.Size.Width, "")
	activeStreamHydrated := s.hydrateActiveAssistantStream(message.Hydration.ActiveAssistantStream)
	if activeStreamHydrated {
		projection := newMarkdownProjector(nil).Project(markdownProjectionInput{
			Source:           s.activeAssistant.source,
			Width:            frameWidthOrDefault(frame),
			PromotedBoundary: s.activeAssistant.promotedSourceBoundary,
		})
		if projection.ProjectionFailure != nil {
			panicOngoingDeveloperError("assistant_hydration", "markdown projection instability", map[string]any{
				"source_boundary":    projection.ProjectionFailure.SourceBoundary,
				"candidate_boundary": projection.ProjectionFailure.CandidateBoundary,
				"row_index":          projection.ProjectionFailure.RowIndex,
				"width":              projection.ProjectionFailure.Width,
			})
		}
		s.activeAssistant.promotedSourceBoundary = projection.PromotedBoundary
		lines = append(lines, s.renderAssistantPromotedRows(projection.PromotedRows, frameWidthOrDefault(frame), "")...)
	}
	if len(lines) == 0 && !activeStreamHydrated {
		return Result{}, nil
	}
	return s.writeFrameTransaction(frame, lines)
}

func (s *Surface) hydrateActiveAssistantStream(stream *clientui.TranscriptAssistantStream) bool {
	if stream == nil {
		s.activeAssistant = activeAssistantState{}
		return false
	}
	streamIDCopy := stream.StreamID
	s.activeAssistant = activeAssistantState{
		streamID: &streamIDCopy,
		source:   stream.Text,
	}
	return stream.Text != ""
}

func (s *Surface) applyAssistantDelta(streamID uuid.UUID, delta string, frame FrameInput) (Result, error) {
	if s.activeAssistant.streamID == nil {
		streamIDCopy := streamID
		s.activeAssistant.streamID = &streamIDCopy
	} else if *s.activeAssistant.streamID != streamID {
		panicOngoingDeveloperError("assistant_delta", "stream id does not match active stream", map[string]any{
			"active_stream_id":  s.activeAssistant.streamID.String(),
			"message_stream_id": streamID.String(),
			"width":             frame.Size.Width,
			"height":            frame.Size.Height,
		})
	}
	s.activeAssistant.source += delta
	projection := newMarkdownProjector(nil).Project(markdownProjectionInput{
		Source:           s.activeAssistant.source,
		Width:            frameWidthOrDefault(frame),
		PromotedBoundary: s.activeAssistant.promotedSourceBoundary,
	})
	if projection.ProjectionFailure != nil {
		panicOngoingDeveloperError("assistant_delta", "markdown projection instability", map[string]any{
			"source_boundary":    projection.ProjectionFailure.SourceBoundary,
			"candidate_boundary": projection.ProjectionFailure.CandidateBoundary,
			"row_index":          projection.ProjectionFailure.RowIndex,
			"width":              projection.ProjectionFailure.Width,
		})
	}
	s.activeAssistant.promotedSourceBoundary = projection.PromotedBoundary
	if len(projection.PromotedRows) == 0 {
		return s.writeFrameTransaction(frame, nil)
	}
	return s.writeFrameTransaction(frame, s.renderAssistantPromotedRows(projection.PromotedRows, frameWidthOrDefault(frame), ""))
}

func (s *Surface) abortAssistantStream(streamID uuid.UUID, frame FrameInput) (Result, error) {
	if s.activeAssistant.streamID != nil && *s.activeAssistant.streamID != streamID {
		panicOngoingDeveloperError("assistant_abort", "stream id does not match active stream", map[string]any{
			"active_stream_id":  s.activeAssistant.streamID.String(),
			"message_stream_id": streamID.String(),
			"width":             frame.Size.Width,
			"height":            frame.Size.Height,
		})
	}
	s.activeAssistant = activeAssistantState{}
	return s.Render(frame)
}

func (s *Surface) finalizeAssistantStream(streamID uuid.UUID, text string, frame FrameInput) (Result, error) {
	if s.activeAssistant.streamID == nil {
		return s.appendAssistantFinalWithoutActiveStream(text, frame)
	}
	if *s.activeAssistant.streamID != streamID {
		panicOngoingDeveloperError("assistant_final", "stream id does not match active stream", map[string]any{
			"active_stream_id":  s.activeAssistant.streamID.String(),
			"message_stream_id": streamID.String(),
			"width":             frame.Size.Width,
			"height":            frame.Size.Height,
		})
	}
	source := s.activeAssistant.source
	if !strings.HasPrefix(text, source) {
		panicOngoingDeveloperError("assistant_final", "final text does not extend active stream source", map[string]any{
			"stream_id":   streamID.String(),
			"source_len":  len(source),
			"final_len":   len(text),
			"promoted_at": s.activeAssistant.promotedSourceBoundary,
			"width":       frame.Size.Width,
			"height":      frame.Size.Height,
		})
	}
	if suffix := text[len(source):]; suffix != "" {
		s.activeAssistant.source += suffix
	}
	unpromoted := s.activeAssistant.source[s.activeAssistant.promotedSourceBoundary:]
	var rows []string
	if unpromoted != "" {
		rows = newMarkdownProjector(nil).renderer.Render(unpromoted, frameWidthOrDefault(frame))
	}
	s.activeAssistant = activeAssistantState{}
	return s.writeFrameTransaction(frame, s.renderAssistantPromotedRows(rows, frameWidthOrDefault(frame), ""))
}

func (s *Surface) appendAssistantFinalWithoutActiveStream(text string, frame FrameInput) (Result, error) {
	row := clientui.TranscriptCommittedRow{
		Kind:      clientui.TranscriptRowAssistant,
		Assistant: &clientui.TranscriptAssistantRow{Text: text, Phase: clientui.MessagePhaseFinal},
	}
	return s.writeFrameTransaction(frame, s.renderCommittedRow(row, frameWidthOrDefault(frame), ""))
}

func isAssistantFinalization(message clientui.TranscriptMessage) bool {
	return message.Kind == clientui.TranscriptMessageCommittedRow &&
		message.CommittedRow != nil &&
		message.CommittedRow.Assistant != nil &&
		message.CommittedRow.Assistant.StreamID != nil
}

func (s *Surface) Render(frame FrameInput) (Result, error) {
	lines := s.liveBandLines(frame)
	s.validateRenderFrame(frame, "render")
	if !s.minimumLiveBandFits(frame, lines) {
		lines = nil
		frame.Cursor = Cursor{}
	} else {
		lines = s.shrinkLiveBandLinesToFrame(frame, lines)
	}
	eraseHeight := min(max(s.previousBandHeight, len(lines)), frame.Size.Height)
	var transaction strings.Builder
	transaction.WriteString(resetScrollRegionAndOriginMode())
	writeMutableBandErase(&transaction, frame.Size.Height, eraseHeight)
	writeMutableBandLines(&transaction, frame.Size.Height, lines)
	writeCursor(&transaction, frame.Cursor)
	if _, err := io.WriteString(s.writer, transaction.String()); err != nil {
		return Result{}, err
	}
	s.previousBandHeight = len(lines)
	s.lastSize = frame.Size
	return Result{}, nil
}

func (s *Surface) SetNormalBufferOwned(_ bool, _ FrameInput) (Result, error) {
	return Result{}, nil
}

func (s *Surface) Resize(size Size, frame FrameInput) (Result, error) {
	if s.widthChanged(size) && s.immutableScrollbackProduced() {
		s.lastSize = size
		return Result{Action: ResultScheduleWidthRehydration, Reason: RehydrateReasonWidthChange}, nil
	}
	frame.Size = size
	return s.Render(frame)
}

func (s *Surface) widthChanged(size Size) bool {
	return s.lastSize.Width > 0 && size.Width > 0 && size.Width != s.lastSize.Width
}

func (s *Surface) immutableScrollbackProduced() bool {
	return s.dividerGroup != nil || s.activeAssistant.promotedSourceBoundary > 0
}

func (s *Surface) ResetForScratchHydration(reason RehydrateReason, frame FrameInput) (Result, error) {
	s.validateRenderFrame(frame, "reset_for_scratch_hydration")
	linesToErase := min(s.previousBandHeight, max(0, frame.Size.Height))
	var transaction strings.Builder
	transaction.WriteString(resetScrollRegionAndOriginMode())
	writeMutableBandErase(&transaction, frame.Size.Height, linesToErase)
	writeCursor(&transaction, Cursor{})
	if _, err := io.WriteString(s.writer, transaction.String()); err != nil {
		return Result{}, err
	}
	s.previousBandHeight = 0
	s.activeAssistant = activeAssistantState{}
	s.dividerGroup = nil
	return Result{Action: ResultRequestScratchRehydration, Reason: reason}, nil
}

func (s *Surface) immutableLines(message clientui.TranscriptMessage, width int, themeName string) []string {
	switch message.Kind {
	case clientui.TranscriptMessageHydration:
		if message.Hydration == nil {
			return nil
		}
		return s.hydrationImmutableLines(*message.Hydration, width, themeName)
	case clientui.TranscriptMessageCommittedRow:
		if message.CommittedRow == nil {
			return nil
		}
		if !committedRowVisibleInOngoing(*message.CommittedRow) {
			return nil
		}
		return s.renderCommittedRow(*message.CommittedRow, width, themeName)
	default:
		return nil
	}
}

func (s *Surface) hydrationImmutableLines(hydration clientui.TranscriptHydration, width int, themeName string) []string {
	lines := make([]string, 0, len(hydration.CommittedRows))
	for _, row := range hydration.CommittedRows {
		if !committedRowVisibleInOngoing(row) {
			continue
		}
		lines = append(lines, s.renderCommittedRow(row, width, themeName)...)
	}
	return lines
}

func committedRowVisibleInOngoing(row clientui.TranscriptCommittedRow) bool {
	switch transcript.NormalizeEntryVisibility(transcript.EntryVisibility(row.Visibility)) {
	case transcript.EntryVisibilityDetail, transcript.EntryVisibilityHidden:
		return false
	default:
		return true
	}
}

func (s *Surface) renderCommittedRow(row clientui.TranscriptCommittedRow, width int, themeName string) []string {
	group, lines := committedRowLines(row, width, themeName)
	return s.renderGroupedRows(group, lines, width, themeName, false)
}

func (s *Surface) renderAssistantPromotedRows(rows []string, width int, themeName string) []string {
	return s.renderGroupedRows(clientui.TranscriptRowAssistant, rows, width, themeName, true)
}

func (s *Surface) renderGroupedRows(group clientui.TranscriptRowKind, rows []string, width int, themeName string, dividerWhenRegisterUnset bool) []string {
	if len(rows) == 0 {
		return nil
	}
	var output []string
	if (s.dividerGroup == nil && dividerWhenRegisterUnset) || (s.dividerGroup != nil && *s.dividerGroup != group) {
		output = append(output, dividerLine(group, width, themeName))
	}
	groupCopy := group
	s.dividerGroup = &groupCopy
	output = append(output, rows...)
	return output
}

func committedRowLines(row clientui.TranscriptCommittedRow, width int, themeName string) (clientui.TranscriptRowKind, []string) {
	switch row.Kind {
	case clientui.TranscriptRowUser, clientui.TranscriptRowAssistant, clientui.TranscriptRowTool, clientui.TranscriptRowNotice:
		rendered := transcriptrender.RenderCommittedRow(row, width, themeName, transcriptrender.ModeOngoing)
		return rendered.Group, encodeTranscriptLines(rendered.Lines, themeName)
	default:
		panic(fmt.Sprintf("ongoing render unknown committed row kind %q", row.Kind))
	}
}

func dividerLine(group clientui.TranscriptRowKind, width int, themeName string) string {
	return encodeTranscriptLine(transcriptrender.RenderDivider(group, width), themeName)
}

func encodeTranscriptLines(lines []transcriptrender.Line, themeName string) []string {
	if len(lines) == 0 {
		return nil
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, encodeTranscriptLine(line, themeName))
	}
	return out
}

func encodeTranscriptLine(line transcriptrender.Line, themeName string) string {
	var out strings.Builder
	for _, span := range line.Spans {
		out.WriteString(encodeTranscriptSpan(span, themeName))
	}
	return out.String()
}

func encodeTranscriptSpan(span transcriptrender.Span, themeName string) string {
	if span.Text == "" {
		return ""
	}
	color := transcriptRoleColor(span.Role, themeName)
	if color == "" && !span.Faint {
		return span.Text
	}
	prefix := ansiTrueColorForeground(color)
	if span.Faint {
		prefix += "\x1b[2m"
	}
	return prefix + span.Text + "\x1b[0m"
}

func transcriptRoleColor(role transcriptrender.StyleRole, themeName string) string {
	return transcriptrender.ColorForRole(transcriptrender.ColorRoleForStyle(role), themeName).TrueColor
}

func ansiTrueColorForeground(hex string) string {
	r, g, b, ok := parseHexColor(hex)
	if !ok {
		return ""
	}
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b)
}

func parseHexColor(hex string) (int, int, int, bool) {
	hex = strings.TrimPrefix(strings.TrimSpace(hex), "#")
	if len(hex) != 6 {
		return 0, 0, 0, false
	}
	var values [3]int
	for idx := 0; idx < 3; idx++ {
		part := hex[idx*2 : idx*2+2]
		var value int
		if _, err := fmt.Sscanf(part, "%02x", &value); err != nil {
			return 0, 0, 0, false
		}
		values[idx] = value
	}
	return values[0], values[1], values[2], true
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
	liveLines := s.liveBandLines(frame)
	if !s.minimumLiveBandFits(frame, liveLines) {
		liveLines = nil
		frame.Cursor = Cursor{}
	} else {
		liveLines = s.shrinkLiveBandLinesToFrame(frame, liveLines)
	}
	if len(immutableRows) > 0 && len(liveLines) >= frame.Size.Height {
		liveLines = nil
		frame.Cursor = Cursor{}
	}
	eraseHeight := min(max(s.previousBandHeight, len(liveLines)), frame.Size.Height)
	var transaction strings.Builder
	transaction.WriteString(resetScrollRegionAndOriginMode())
	writeMutableBandErase(&transaction, frame.Size.Height, eraseHeight)
	writeImmutableRowsAboveMutableBand(&transaction, frame.Size.Height, len(liveLines), immutableRows)
	writeMutableBandLines(&transaction, frame.Size.Height, liveLines)
	writeCursor(&transaction, frame.Cursor)
	if _, err := io.WriteString(s.writer, transaction.String()); err != nil {
		return Result{}, err
	}
	s.previousBandHeight = len(liveLines)
	s.lastSize = frame.Size
	return Result{}, nil
}

func (s *Surface) minimumLiveBandFits(frame FrameInput, liveLines []string) bool {
	return enforcedMinimumLiveBandHeight(frame, s.activeAssistant, liveLines) <= frame.Size.Height
}

func (s *Surface) liveBandLines(frame FrameInput) []string {
	lines := activeAssistantLines(s.activeAssistant, frameWidthOrDefault(frame))
	lines = append(lines, frameLines(frame)...)
	return lines
}

func (s *Surface) shrinkLiveBandLinesToFrame(frame FrameInput, liveLines []string) []string {
	if len(liveLines) <= frame.Size.Height {
		return liveLines
	}
	lines := minimumLiveBandLines(frame, s.activeAssistant)
	if len(lines) > frame.Size.Height {
		return lines[len(lines)-frame.Size.Height:]
	}
	return lines
}

func minimumLiveBandLines(frame FrameInput, assistant activeAssistantState) []string {
	var lines []string
	if assistant.source != "" {
		assistantLines := activeAssistantLines(assistant, frameWidthOrDefault(frame))
		if len(assistantLines) > 0 {
			lines = append(lines, assistantLines[len(assistantLines)-1])
		}
	}
	for _, section := range frame.Sections {
		if len(section.Lines) == 0 {
			continue
		}
		limit := 1
		switch section.Kind {
		case FrameSectionQueuedOrSteered:
			limit = min(2, len(section.Lines))
		case FrameSectionInput:
			limit = min(3, len(section.Lines))
		}
		if section.Kind == FrameSectionInput && len(section.Lines) > limit {
			lines = append(lines, section.Lines[len(section.Lines)-limit:]...)
			continue
		}
		lines = append(lines, section.Lines[:limit]...)
	}
	return lines
}

func activeAssistantLines(state activeAssistantState, width int) []string {
	if state.source == "" {
		return nil
	}
	projection := newMarkdownProjector(nil).Project(markdownProjectionInput{
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
		if len(section.Lines) == 0 {
			continue
		}
		switch section.Kind {
		case FrameSectionPendingTools:
			total++
		case FrameSectionQueuedOrSteered:
			total += min(2, len(section.Lines))
		case FrameSectionInput:
			total += min(3, len(section.Lines))
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

func frameLines(frame FrameInput) []string {
	var lines []string
	for _, section := range frame.Sections {
		lines = append(lines, section.Lines...)
	}
	return lines
}

func resetScrollRegionAndOriginMode() string {
	return "\x1b[r\x1b[?6l"
}

func writeMutableBandErase(builder *strings.Builder, terminalHeight, bandHeight int) {
	startRow := terminalHeight - bandHeight + 1
	for row := startRow; row <= terminalHeight; row++ {
		fmt.Fprintf(builder, "\x1b[%d;1H\x1b[2K", row)
	}
}

func writeMutableBandLines(builder *strings.Builder, terminalHeight int, lines []string) {
	startRow := terminalHeight - len(lines) + 1
	for index, line := range lines {
		fmt.Fprintf(builder, "\x1b[%d;1H%s", startRow+index, line)
	}
}

func writeImmutableRowsAboveMutableBand(builder *strings.Builder, terminalHeight, bandHeight int, rows []string) {
	if len(rows) == 0 {
		return
	}
	bottom := terminalHeight - bandHeight
	if bottom < 1 {
		return
	}
	fmt.Fprintf(builder, "\x1b[1;%dr\x1b[%d;1H", bottom, bottom)
	for _, row := range rows {
		builder.WriteString("\r\n")
		builder.WriteString(row)
	}
	builder.WriteString(resetScrollRegionAndOriginMode())
}

func writeCursor(builder *strings.Builder, cursor Cursor) {
	if !cursor.Visible {
		builder.WriteString("\x1b[?25l")
		return
	}
	fmt.Fprintf(builder, "\x1b[%d;%dH\x1b[?25h", cursor.Row, cursor.Column)
}
