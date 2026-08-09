package ongoing

import (
	"fmt"
	"io"
	"strings"

	"core/cli/tui/transcriptrender"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/transcript"
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
	ResultScheduleWidthRehydration  ResultAction = "schedule_width_rehydration"
)

type RehydrateReason string

const (
	RehydrateReasonSequenceGap   RehydrateReason = "sequence_gap"
	RehydrateReasonQueueOverflow RehydrateReason = "queue_overflow"
	RehydrateReasonWidthChange   RehydrateReason = "width_change"
)

type TerminalResizePolicy uint8

const (
	TerminalResizeSemanticPrompt TerminalResizePolicy = iota + 1
	TerminalResizeWidthRehydration
	TerminalResizeTmuxWidthRehydration
)

func (p TerminalResizePolicy) usesWidthRehydration() bool {
	switch p {
	case TerminalResizeWidthRehydration, TerminalResizeTmuxWidthRehydration:
		return true
	case TerminalResizeSemanticPrompt:
		return false
	default:
		panic(fmt.Sprintf("ongoing surface received invalid terminal resize policy %d", p))
	}
}

func (p TerminalResizePolicy) bottomAnchorsVerticalExpansion() bool {
	switch p {
	case TerminalResizeTmuxWidthRehydration:
		return true
	case TerminalResizeSemanticPrompt, TerminalResizeWidthRehydration:
		return false
	default:
		panic(fmt.Sprintf("ongoing surface received invalid terminal resize policy %d", p))
	}
}

type Surface struct {
	writer             io.Writer
	retainedBandHeight int
	groupRegister      *clientui.TranscriptRowKind
	activeAssistant    activeAssistantState
	terminalResize     TerminalResizePolicy
	markdownLinks      transcriptrender.MarkdownLinkPresentation
	lastPaintedSize    *Size
}

type activeAssistantState struct {
	streamID               *runtimeids.AssistantStreamID
	source                 string
	phase                  transcript.AssistantPhase
	phaseSourceStart       int
	promotedSourceBoundary int
	rolePrefixState        assistantRolePrefixState
}

type assistantRolePrefixState uint8

const (
	assistantRolePrefixPending assistantRolePrefixState = iota
	assistantRolePrefixEmitted
)

func NewSurface(writers ...io.Writer) *Surface {
	writer := io.Discard
	if len(writers) > 0 && writers[0] != nil {
		writer = writers[0]
	}
	return NewSurfaceWithOptions(writer, SurfaceOptions{
		TerminalResize: TerminalResizeSemanticPrompt,
		MarkdownLinks:  transcriptrender.MarkdownLinkLabelOnly,
	})
}

type SurfaceOptions struct {
	TerminalResize TerminalResizePolicy
	MarkdownLinks  transcriptrender.MarkdownLinkPresentation
}

func NewSurfaceWithOptions(writer io.Writer, options SurfaceOptions) *Surface {
	if writer == nil {
		writer = io.Discard
	}
	switch options.TerminalResize {
	case TerminalResizeSemanticPrompt, TerminalResizeWidthRehydration, TerminalResizeTmuxWidthRehydration:
	default:
		panic(fmt.Sprintf("ongoing surface received invalid terminal resize policy %d", options.TerminalResize))
	}
	if !options.MarkdownLinks.Valid() {
		panic(fmt.Sprintf("ongoing surface received invalid Markdown link presentation %d", options.MarkdownLinks))
	}
	return &Surface{
		writer:         writer,
		terminalResize: options.TerminalResize,
		markdownLinks:  options.MarkdownLinks,
	}
}

func (s *Surface) ApplyTerminalMessage(message clientui.TranscriptMessage, frame FrameInput) (Result, error) {
	s.validateRenderFrame(frame, "apply_terminal_message")
	if message.Kind() == clientui.TranscriptMessageHydration {
		return s.applyHydration(message, frame)
	}
	if message.Kind() == clientui.TranscriptMessageAssistantDelta {
		payload := message.Payload().(clientui.TranscriptAssistantDelta)
		return s.applyAssistantDelta(payload.StreamID, payload.Delta, payload.Phase, frame)
	}
	if message.Kind() == clientui.TranscriptMessageAssistantStreamAbort {
		payload := message.Payload().(clientui.TranscriptAssistantStreamAbort)
		return s.abortAssistantStream(payload.StreamID, frame)
	}
	if isAssistantFinalization(message) {
		row := message.Payload().(clientui.TranscriptCommittedRow)
		return s.finalizeAssistantStream(*row.Assistant.StreamID, row.Assistant.Text, frame)
	}
	lines := s.immutableLines(message, frame.Size.Width, frame.Theme)
	if len(lines) == 0 {
		return Result{}, nil
	}
	return s.writeFrameTransaction(frame, lines)
}

func (s *Surface) applyHydration(message clientui.TranscriptMessage, frame FrameInput) (Result, error) {
	hydration := message.Payload().(clientui.TranscriptHydration)
	lines := s.hydrationImmutableLines(hydration, frame.Size.Width, frame.Theme)
	activeStreamHydrated := s.hydrateActiveAssistantStream(hydration.ActiveAssistant)
	if activeStreamHydrated && !s.activeAssistantPromotionDeferred() {
		projection := newMarkdownProjector(nil, frame.Theme, s.markdownLinks).Project(markdownProjectionInput{
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
		promotedRows := s.renderAssistantPromotedRows(projection.PromotedRows, frame.Theme)
		s.activeAssistant.promotedSourceBoundary = projection.PromotedBoundary
		lines = append(lines, promotedRows...)
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

func (s *Surface) applyAssistantDelta(streamID runtimeids.AssistantStreamID, delta string, phase transcript.AssistantPhase, frame FrameInput) (Result, error) {
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
	projection := newMarkdownProjector(nil, frame.Theme, s.markdownLinks).Project(markdownProjectionInput{
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
	promotedRows := s.renderAssistantPromotedRows(projection.PromotedRows, frame.Theme)
	return s.writeFrameTransaction(frame, promotedRows)
}

func (s *Surface) activeAssistantPromotionDeferred() bool {
	if s.activeAssistant.phase != transcript.AssistantPhaseFinal {
		return false
	}
	content := s.activeAssistant.source[s.activeAssistant.phaseSourceStart:]
	return transcript.IsBlankAssistantFinal(transcript.AssistantFinalCandidate{
		IsAssistant: true,
		IsFinal:     true,
		Content:     &content,
	})
}

func (s *Surface) abortAssistantStream(streamID runtimeids.AssistantStreamID, frame FrameInput) (Result, error) {
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

func (s *Surface) finalizeAssistantStream(streamID runtimeids.AssistantStreamID, text string, frame FrameInput) (Result, error) {
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
		rows = newMarkdownProjector(nil, frame.Theme, s.markdownLinks).renderer.RenderStable(unpromoted, frameWidthOrDefault(frame))
	}
	promotedRows := s.renderAssistantPromotedRows(rows, frame.Theme)
	s.activeAssistant = activeAssistantState{}
	return s.writeFrameTransaction(frame, promotedRows)
}

func (s *Surface) appendAssistantFinalWithoutActiveStream(text string, frame FrameInput) (Result, error) {
	if transcript.IsBlankAssistantFinal(transcript.AssistantFinalCandidate{
		IsAssistant: true,
		IsFinal:     true,
		Content:     &text,
	}) {
		return s.writeFrameTransaction(frame, nil)
	}
	row := clientui.TranscriptCommittedRow{
		Kind:      clientui.TranscriptRowAssistant,
		Assistant: &clientui.TranscriptAssistantRow{Text: text, Phase: transcript.AssistantPhaseFinal},
	}
	return s.writeFrameTransaction(frame, s.renderCommittedRow(row, frameWidthOrDefault(frame), frame.Theme))
}

func isAssistantFinalization(message clientui.TranscriptMessage) bool {
	if message.Kind() != clientui.TranscriptMessageCommittedRow {
		return false
	}
	row := message.Payload().(clientui.TranscriptCommittedRow)
	return row.Assistant != nil && row.Assistant.StreamID != nil
}

func (s *Surface) Render(frame FrameInput) (Result, error) {
	s.validateRenderFrame(frame, "render")
	return s.writeFrameTransaction(frame, nil)
}

func (s *Surface) SetNormalBufferOwned(_ bool, _ FrameInput) (Result, error) {
	return Result{}, nil
}

func (s *Surface) Resize(size Size, frame FrameInput) (Result, error) {
	if result := s.observeResize(size); result.Action != ResultNoop {
		return result, nil
	}
	frame.Size = size
	return s.Render(frame)
}

func (s *Surface) ObserveResize(size Size) Result {
	return s.observeResize(size)
}

func (s *Surface) observeResize(size Size) Result {
	if !s.terminalResize.usesWidthRehydration() {
		return Result{}
	}
	if s.lastPaintedSize != nil &&
		size.Width > 0 &&
		size.Width != s.lastPaintedSize.Width &&
		s.immutableScrollbackProduced() {
		return Result{Action: ResultScheduleWidthRehydration, Reason: RehydrateReasonWidthChange}
	}
	return Result{}
}

func (s *Surface) immutableScrollbackProduced() bool {
	return s.groupRegister != nil || s.activeAssistant.promotedSourceBoundary > 0
}

func (s *Surface) ResetForScratchHydration(reason RehydrateReason, frame FrameInput) (Result, error) {
	s.validateRenderFrame(frame, "reset_for_scratch_hydration")
	previousMutableBand := s.visiblePreviousMutableBand(frame.Size)
	linesToErase := s.previousMutableBandHeightAtBottom(frame.Size)
	var transaction strings.Builder
	transaction.WriteString(resetScrollRegionAndOriginMode())
	transaction.WriteString(semanticOutputSequence())
	if s.terminalResize.usesWidthRehydration() &&
		s.terminalHeightExpanded(frame.Size) &&
		previousMutableBand != nil {
		writeMutableRowsErase(&transaction, previousMutableBand.start, previousMutableBand.end)
	}
	writeMutableBandErase(&transaction, frame.Size.Height, linesToErase)
	writeCursor(&transaction, Cursor{})
	if _, err := io.WriteString(s.writer, transaction.String()); err != nil {
		return Result{}, err
	}
	s.retainedBandHeight = 0
	s.activeAssistant = activeAssistantState{}
	s.groupRegister = nil
	paintedSize := frame.Size
	s.lastPaintedSize = &paintedSize
	return Result{Action: ResultRequestScratchRehydration, Reason: reason}, nil
}

func (s *Surface) immutableLines(message clientui.TranscriptMessage, width int, themeName string) []string {
	switch message.Kind() {
	case clientui.TranscriptMessageHydration:
		return s.hydrationImmutableLines(message.Payload().(clientui.TranscriptHydration), width, themeName)
	case clientui.TranscriptMessageCommittedRow:
		row := message.Payload().(clientui.TranscriptCommittedRow)
		if !committedRowVisibleInOngoing(row) {
			return nil
		}
		return s.renderCommittedRow(row, width, themeName)
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
	group, lines := committedRowLines(row, width, themeName, mode, s.markdownLinks)
	return s.renderGroupedRows(group, lines, false)
}

// ongoingRenderMode selects the renderer mode for a committed row in the
// ongoing surface. O rows render their full ongoing preview; OC rows render
// the collapsed/short ongoing form per tui-transcript.md visibility rules.
// Verbose supervisor suggestions are an O-row exception: their complete
// typed suggestion list belongs in native scrollback. Answered questions are
// the other typed multi-line exception. D and X rows never reach this path.
func ongoingRenderMode(row clientui.TranscriptCommittedRow) transcriptrender.Mode {
	if isFullOngoingNoticeRow(row) {
		return transcriptrender.ModeOngoingFull
	}
	switch row.Visibility {
	case clientui.EntryVisibilityOngoingCollapsed:
		return transcriptrender.ModeOngoingCollapsed
	case clientui.EntryVisibilityOngoing:
		return transcriptrender.ModeOngoing
	default:
		panic(fmt.Sprintf("ongoing render received non-ongoing visibility %q", row.Visibility))
	}
}

func isFullOngoingNoticeRow(row clientui.TranscriptCommittedRow) bool {
	if row.Kind != clientui.TranscriptRowNotice || row.Notice == nil || row.Notice.Diagnostic == nil {
		return false
	}
	if row.Notice.MessageType != nil && *row.Notice.MessageType == clientui.TranscriptMessageAgentSteer {
		return true
	}
	return transcript.EntryRole(row.Notice.Diagnostic.Code) == transcript.EntryRoleReviewerSuggestions
}

func committedRowRenderMode(row clientui.TranscriptCommittedRow) transcriptrender.Mode {
	if row.Kind == clientui.TranscriptRowUser && row.User != nil {
		return transcriptrender.ModeOngoingStable
	}
	if row.Kind != clientui.TranscriptRowAssistant || row.Assistant == nil {
		return ongoingRenderMode(row)
	}
	switch row.Assistant.Phase {
	case transcript.AssistantPhaseFinal:
		return transcriptrender.ModeOngoingStable
	case transcript.AssistantPhaseCommentary:
		return ongoingRenderMode(row)
	default:
		panic(fmt.Sprintf("ongoing committed row has unclassified assistant phase %q", row.Assistant.Phase))
	}
}

func (s *Surface) renderAssistantPromotedRows(rows []string, themeName string) []string {
	if len(rows) == 0 {
		return nil
	}
	decorated := rows
	if s.activeAssistant.rolePrefixState == assistantRolePrefixPending {
		decorated = append([]string(nil), rows...)
		decorated[0] = encodeTranscriptSpan(
			transcriptrender.SemanticSpan(
				transcriptrender.AssistantSymbol+" ",
				transcriptrender.StyleRoleAssistant,
			),
			themeName,
		) + rows[0]
		s.activeAssistant.rolePrefixState = assistantRolePrefixEmitted
	}
	return s.renderGroupedRows(clientui.TranscriptRowAssistant, decorated, true)
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

func committedRowLines(
	row clientui.TranscriptCommittedRow,
	width int,
	themeName string,
	mode transcriptrender.Mode,
	linkPresentation transcriptrender.MarkdownLinkPresentation,
) (clientui.TranscriptRowKind, []string) {
	switch row.Kind {
	case clientui.TranscriptRowUser, clientui.TranscriptRowAssistant, clientui.TranscriptRowTool, clientui.TranscriptRowNotice, clientui.TranscriptRowReviewerFeedback, clientui.TranscriptRowReviewerError:
		rendered := transcriptrender.RenderCommittedRowWithLinkPresentation(
			row,
			width,
			themeName,
			mode,
			linkPresentation,
		)
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
	rendered := span.Text
	if color == "" && !resolved.Faint && !resolved.Bold && !resolved.Italic && !resolved.Underline && !resolved.Strikethrough {
		return transcriptrender.EncodeSpanHyperlink(span, rendered)
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
	if resolved.Strikethrough {
		prefix += "\x1b[9m"
	}
	rendered = prefix + span.Text + "\x1b[0m"
	return transcriptrender.EncodeSpanHyperlink(span, rendered)
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
