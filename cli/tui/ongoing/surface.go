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
	Size         Size
	Theme        string
	SpinnerFrame int
	Sections     []FrameSection
	Cursor       Cursor
}

type Cursor struct {
	Visible bool
	Row     int
	Column  int
	Target  *CursorTarget
}

type CursorTarget struct {
	SectionKind FrameSectionKind
	Row         int
}

type FrameSection struct {
	Kind        FrameSectionKind
	Lines       []string
	StyledLines []transcriptrender.Line
}

type liveBandLine struct {
	text        string
	sectionKind FrameSectionKind
	sectionRow  int
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
)

type Result struct {
	Action ResultAction
	Reason RehydrateReason
}

type ResultAction string

const (
	ResultNoop                      ResultAction = ""
	ResultRequestScratchRehydration ResultAction = "request_scratch_rehydration"
)

type RehydrateReason string

const (
	RehydrateReasonSequenceGap   RehydrateReason = "sequence_gap"
	RehydrateReasonQueueOverflow RehydrateReason = "queue_overflow"
)

type Surface struct {
	writer             io.Writer
	previousBandHeight int
	groupRegister      *clientui.TranscriptRowKind
	activeAssistant    activeAssistantState
}

type activeAssistantState struct {
	streamID               *uuid.UUID
	source                 string
	phase                  clientui.MessagePhase
	phaseSourceStart       int
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
		return s.applyAssistantDelta(message.AssistantDelta.StreamID, message.AssistantDelta.Delta, message.AssistantDelta.Phase, frame)
	}
	if message.Kind == clientui.TranscriptMessageAssistantStreamAbort && message.AssistantStreamAbort != nil {
		return s.abortAssistantStream(message.AssistantStreamAbort.StreamID, frame)
	}
	if isAssistantFinalization(message) {
		return s.finalizeAssistantStream(*message.CommittedRow.Assistant.StreamID, message.CommittedRow.Assistant.Text, frame)
	}
	lines := s.immutableLines(message, frame.Size.Width, frame.Theme)
	if len(lines) == 0 {
		return Result{}, nil
	}
	return s.writeFrameTransaction(frame, lines)
}

func (s *Surface) applyHydration(message clientui.TranscriptMessage, frame FrameInput) (Result, error) {
	if message.Hydration == nil {
		return Result{}, nil
	}
	lines := s.hydrationImmutableLines(*message.Hydration, frame.Size.Width, frame.Theme)
	activeStreamHydrated := s.hydrateActiveAssistantStream(message.Hydration.ActiveAssistantStream)
	if activeStreamHydrated && !s.activeAssistantPromotionDeferred() {
		projection := newMarkdownProjector(nil, frame.Theme).Project(markdownProjectionInput{
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
		lines = append(lines, s.renderAssistantPromotedRows(projection.PromotedRows)...)
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
		streamID:         &streamIDCopy,
		source:           stream.Text,
		phase:            stream.Phase,
		phaseSourceStart: 0,
	}
	return stream.Text != ""
}

func (s *Surface) applyAssistantDelta(streamID uuid.UUID, delta string, phase clientui.MessagePhase, frame FrameInput) (Result, error) {
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
	if phase != "" && s.activeAssistant.phase != phase {
		s.activeAssistant.phase = phase
		s.activeAssistant.phaseSourceStart = len(s.activeAssistant.source)
	}
	s.activeAssistant.source += delta
	if s.activeAssistantPromotionDeferred() {
		return s.writeFrameTransaction(frame, nil)
	}
	projection := newMarkdownProjector(nil, frame.Theme).Project(markdownProjectionInput{
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
	return s.writeFrameTransaction(frame, s.renderAssistantPromotedRows(projection.PromotedRows))
}

func (s *Surface) activeAssistantPromotionDeferred() bool {
	return s.activeAssistant.phase == clientui.MessagePhaseFinal &&
		transcript.IsNoopFinalText(s.activeAssistant.source[s.activeAssistant.phaseSourceStart:])
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
		rows = newMarkdownProjector(nil, frame.Theme).renderer.RenderStable(unpromoted, frameWidthOrDefault(frame))
	}
	s.activeAssistant = activeAssistantState{}
	return s.writeFrameTransaction(frame, s.renderAssistantPromotedRows(rows))
}

func (s *Surface) appendAssistantFinalWithoutActiveStream(text string, frame FrameInput) (Result, error) {
	row := clientui.TranscriptCommittedRow{
		Kind:      clientui.TranscriptRowAssistant,
		Assistant: &clientui.TranscriptAssistantRow{Text: text, Phase: transcript.AssistantPhaseFinal},
	}
	return s.writeFrameTransaction(frame, s.renderCommittedRow(row, frameWidthOrDefault(frame), frame.Theme))
}

func isAssistantFinalization(message clientui.TranscriptMessage) bool {
	return message.Kind == clientui.TranscriptMessageCommittedRow &&
		message.CommittedRow != nil &&
		message.CommittedRow.Assistant != nil &&
		message.CommittedRow.Assistant.StreamID != nil
}

func (s *Surface) Render(frame FrameInput) (Result, error) {
	s.validateRenderFrame(frame, "render")
	return s.writeFrameTransaction(frame, nil)
}

func (s *Surface) SetNormalBufferOwned(_ bool, _ FrameInput) (Result, error) {
	return Result{}, nil
}

func (s *Surface) Resize(size Size, frame FrameInput) (Result, error) {
	frame.Size = size
	return s.Render(frame)
}

func (s *Surface) ObserveResize(_ Size) Result {
	return Result{}
}

func (s *Surface) immutableScrollbackProduced() bool {
	return s.groupRegister != nil || s.activeAssistant.promotedSourceBoundary > 0
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
	s.groupRegister = nil
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
		lines = append(lines, s.renderHydratedCommittedRow(row, width, themeName)...)
	}
	return lines
}

func committedRowVisibleInOngoing(row clientui.TranscriptCommittedRow) bool {
	switch row.Visibility {
	case clientui.EntryVisibilityOngoing, clientui.EntryVisibilityOngoingCollapsed:
		return true
	case transcript.EntryVisibilityDetail, transcript.EntryVisibilityHidden:
		return false
	default:
		panic(fmt.Sprintf("ongoing received committed row with unresolved visibility %q", row.Visibility))
	}
}

func (s *Surface) renderCommittedRow(row clientui.TranscriptCommittedRow, width int, themeName string) []string {
	return s.renderCommittedRowWithMode(row, width, themeName, committedRowRenderMode(row))
}

func (s *Surface) renderHydratedCommittedRow(row clientui.TranscriptCommittedRow, width int, themeName string) []string {
	return s.renderCommittedRowWithMode(row, width, themeName, committedRowRenderMode(row))
}

func (s *Surface) renderCommittedRowWithMode(row clientui.TranscriptCommittedRow, width int, themeName string, mode transcriptrender.Mode) []string {
	group, lines := committedRowLines(row, width, themeName, mode)
	return s.renderGroupedRows(group, lines, false)
}

// ongoingRenderMode selects the renderer mode for a committed row in the
// ongoing surface. O rows render their full ongoing preview; OC rows render
// the collapsed/short (single-line) ongoing form per tui-transcript.md
// visibility rules. D and X rows never reach this path.
func ongoingRenderMode(row clientui.TranscriptCommittedRow) transcriptrender.Mode {
	switch row.Visibility {
	case clientui.EntryVisibilityOngoingCollapsed:
		return transcriptrender.ModeOngoingCollapsed
	case clientui.EntryVisibilityOngoing:
		return transcriptrender.ModeOngoing
	default:
		panic(fmt.Sprintf("ongoing render received non-ongoing visibility %q", row.Visibility))
	}
}

func committedRowRenderMode(row clientui.TranscriptCommittedRow) transcriptrender.Mode {
	if row.Kind == clientui.TranscriptRowUser && row.User != nil {
		return transcriptrender.ModeOngoingStable
	}
	if row.Kind != clientui.TranscriptRowAssistant || row.Assistant == nil {
		return ongoingRenderMode(row)
	}
	switch row.Assistant.Phase {
	case transcript.AssistantPhaseFinal, transcript.AssistantPhaseLegacyFinal:
		return transcriptrender.ModeOngoingStable
	case transcript.AssistantPhaseCommentary:
		return ongoingRenderMode(row)
	default:
		panic(fmt.Sprintf("ongoing committed row has unclassified assistant phase %q", row.Assistant.Phase))
	}
}

func (s *Surface) renderAssistantPromotedRows(rows []string) []string {
	return s.renderGroupedRows(clientui.TranscriptRowAssistant, rows, true)
}

func (s *Surface) renderGroupedRows(group clientui.TranscriptRowKind, rows []string, separatorWhenRegisterUnset bool) []string {
	if len(rows) == 0 {
		return nil
	}
	var output []string
	if (s.groupRegister == nil && separatorWhenRegisterUnset) || (s.groupRegister != nil && *s.groupRegister != group) {
		output = append(output, "")
	}
	groupCopy := group
	s.groupRegister = &groupCopy
	output = append(output, rows...)
	return output
}

func committedRowLines(row clientui.TranscriptCommittedRow, width int, themeName string, mode transcriptrender.Mode) (clientui.TranscriptRowKind, []string) {
	switch row.Kind {
	case clientui.TranscriptRowUser, clientui.TranscriptRowAssistant, clientui.TranscriptRowTool, clientui.TranscriptRowNotice:
		rendered := transcriptrender.RenderCommittedRow(row, width, themeName, mode)
		return rendered.Group, encodeTranscriptLines(rendered.Lines, themeName)
	default:
		panic(fmt.Sprintf("ongoing render unknown committed row kind %q", row.Kind))
	}
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
	if line.LeadingSymbol != nil {
		out.WriteString(encodeTranscriptSpan(*line.LeadingSymbol, themeName))
	}
	for _, span := range line.Spans {
		out.WriteString(encodeTranscriptSpan(span, themeName))
	}
	return out.String()
}

func encodeTranscriptSpan(span transcriptrender.Span, themeName string) string {
	if span.Text == "" {
		return ""
	}
	resolved := transcriptrender.ResolveSpanStyle(span, themeName)
	color := resolved.Foreground.TrueColor()
	if color == "" && !resolved.Faint && !resolved.Bold && !resolved.Italic && !resolved.Underline {
		return span.Text
	}
	prefix := ansiTrueColorForeground(color)
	if resolved.Faint {
		prefix += "\x1b[2m"
	}
	if resolved.Bold {
		prefix += "\x1b[1m"
	}
	if resolved.Italic {
		prefix += "\x1b[3m"
	}
	if resolved.Underline {
		prefix += "\x1b[4m"
	}
	return prefix + span.Text + "\x1b[0m"
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
	eraseHeight := min(max(s.previousBandHeight, len(liveLines)), frame.Size.Height)
	var transaction strings.Builder
	transaction.WriteString(resetScrollRegionAndOriginMode())
	if len(liveLines) > s.previousBandHeight && s.immutableScrollbackProduced() {
		writeImmutableRegionScrollForLiveBandGrowth(&transaction, frame.Size.Height, s.previousBandHeight, len(liveLines))
	}
	writeMutableBandErase(&transaction, frame.Size.Height, eraseHeight)
	writeImmutableRowsAboveMutableBand(
		&transaction,
		frame.Size.Height,
		s.previousBandHeight,
		len(liveLines),
		immutableRows,
	)
	writeMutableBandLines(&transaction, frame.Size.Height, liveLines)
	writeCursor(&transaction, frame.Cursor)
	if _, err := io.WriteString(s.writer, transaction.String()); err != nil {
		return Result{}, err
	}
	s.previousBandHeight = len(liveLines)
	return Result{}, nil
}

func (s *Surface) minimumLiveBandFits(frame FrameInput, liveLines []string) bool {
	return enforcedMinimumLiveBandHeight(frame, s.activeAssistant, liveLines) <= frame.Size.Height
}

func (s *Surface) liveBandLines(frame FrameInput) []string {
	return liveBandLineTexts(s.liveBandLayout(frame))
}

func (s *Surface) liveBandLayout(frame FrameInput) []liveBandLine {
	lines := activeAssistantLines(s.activeAssistant, frameWidthOrDefault(frame), frame.Theme)
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
	lines := minimumLiveBandLayout(frame, s.activeAssistant)
	if len(lines) > frame.Size.Height {
		return lines[len(lines)-frame.Size.Height:]
	}
	return lines
}

func minimumLiveBandLines(frame FrameInput, assistant activeAssistantState) []string {
	return liveBandLineTexts(minimumLiveBandLayout(frame, assistant))
}

func minimumLiveBandLayout(frame FrameInput, assistant activeAssistantState) []liveBandLine {
	var lines []string
	if assistant.source != "" {
		assistantLines := activeAssistantLines(assistant, frameWidthOrDefault(frame), frame.Theme)
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

func activeAssistantLines(state activeAssistantState, width int, themeName string) []string {
	if state.source == "" {
		return nil
	}
	projection := newMarkdownProjector(nil, themeName).Project(markdownProjectionInput{
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

func resetScrollRegionAndOriginMode() string {
	return "\x1b[r\x1b[?6l"
}

func writeMutableBandErase(builder *strings.Builder, terminalHeight, bandHeight int) {
	startRow := terminalHeight - bandHeight + 1
	for row := startRow; row <= terminalHeight; row++ {
		fmt.Fprintf(builder, "\x1b[%d;1H\x1b[2K", row)
	}
}

func writeImmutableRegionScrollForLiveBandGrowth(builder *strings.Builder, terminalHeight, previousBandHeight, nextBandHeight int) {
	delta := nextBandHeight - previousBandHeight
	if delta <= 0 {
		return
	}
	oldImmutableBottom := terminalHeight - previousBandHeight
	if oldImmutableBottom < 1 {
		return
	}
	if delta > oldImmutableBottom {
		delta = oldImmutableBottom
	}
	fmt.Fprintf(builder, "\x1b[1;%dr\x1b[%d;1H", oldImmutableBottom, oldImmutableBottom)
	for range delta {
		builder.WriteString("\r\n")
	}
	builder.WriteString(resetScrollRegionAndOriginMode())
}

func writeMutableBandLines(builder *strings.Builder, terminalHeight int, lines []string) {
	startRow := terminalHeight - len(lines) + 1
	for index, line := range lines {
		fmt.Fprintf(builder, "\x1b[%d;1H%s", startRow+index, line)
	}
}

func writeImmutableRowsAboveMutableBand(
	builder *strings.Builder,
	terminalHeight int,
	previousBandHeight int,
	bandHeight int,
	rows []string,
) {
	if len(rows) == 0 {
		return
	}
	bottom := terminalHeight - bandHeight
	if bottom < 1 {
		return
	}
	previousBottom := terminalHeight - min(previousBandHeight, terminalHeight)
	appendRow := bottom
	if previousBottom >= 1 && previousBottom < bottom {
		appendRow = previousBottom
	}
	fmt.Fprintf(builder, "\x1b[1;%dr\x1b[%d;1H", bottom, appendRow)
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
