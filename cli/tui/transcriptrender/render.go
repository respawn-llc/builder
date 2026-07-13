package transcriptrender

import (
	"fmt"
	"strings"

	"core/shared/clientui"
	"core/shared/transcript"

	"github.com/charmbracelet/lipgloss"
	"github.com/rivo/uniseg"
)

const BackgroundedShellSuffix = "· backgrounded"

func RenderCommittedRow(row clientui.TranscriptCommittedRow, width int, themeName string, mode Mode) Row {
	var syntax *syntaxProjector
	if row.Kind == clientui.TranscriptRowTool {
		configured := newSyntaxProjector(themeName)
		syntax = &configured
	}
	return renderCommittedRow(row, width, mode, syntax)
}

func renderCommittedRow(
	row clientui.TranscriptCommittedRow,
	width int,
	mode Mode,
	syntax *syntaxProjector,
) Row {
	switch row.Kind {
	case clientui.TranscriptRowUser:
		return Row{Group: clientui.TranscriptRowUser, Lines: renderUserAssistantTextBlock(StyleRoleUser, userAssistantDisplayText(row.User.Text, row.User.CondensedText, mode), width, mode)}
	case clientui.TranscriptRowAssistant:
		return Row{Group: clientui.TranscriptRowAssistant, Lines: renderUserAssistantTextBlock(StyleRoleAssistant, userAssistantDisplayText(row.Assistant.Text, row.Assistant.CondensedText, mode), width, mode)}
	case clientui.TranscriptRowTool:
		return Row{Group: clientui.TranscriptRowTool, Lines: renderToolRow(*row.Tool, width, mode, syntax)}
	case clientui.TranscriptRowNotice:
		role, text := noticeRoleAndText(row.Notice, row.Visibility, mode)
		meta := toolMeta{}
		group := clientui.TranscriptRowNotice
		if row.Notice != nil && row.Notice.Data.MessageType == clientui.MessageTypeBackgroundNotice {
			symbolRole := StyleRoleNoticePrimary
			meta.SymbolStyleRole = &symbolRole
			group = clientui.TranscriptRowTool
		}
		return Row{Group: group, Lines: renderTextBlockWithInlineMeta(role, text, "", width, mode, meta)}
	default:
		return Row{Group: clientui.TranscriptRowNotice, Lines: renderTextBlock(StyleRoleNotice, "unknown transcript row", width, mode)}
	}
}

// userAssistantDisplayText selects compact vs full source for user/assistant
// rows. Normal ongoing and collapsed detail prefer the server-provided
// CondensedText when present. Stable ongoing and detail-expanded rendering use
// the full Markdown source.
func userAssistantDisplayText(text, condensed string, mode Mode) string {
	if modeUsesFullUserAssistantText(mode) {
		return text
	}
	if compact := strings.TrimSpace(condensed); compact != "" {
		return compact
	}
	return text
}

func renderTextBlock(role StyleRole, text string, width int, mode Mode) []Line {
	return renderTextBlockWithInlineMeta(role, text, "", width, mode, toolMeta{})
}

func renderUserAssistantTextBlock(role StyleRole, text string, width int, mode Mode) []Line {
	text = safeTranscriptText(text)
	text = strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if text == "" {
		text = labelForRole(role)
	}
	if mode == ModeOngoingStable {
		return attachPrefixWithMeta(role, RenderMarkdownStableLines(role, text, contentWidth(role, width)), width, false, mode, toolMeta{})
	}
	if modeUsesCompactTextBlock(mode) {
		if mode == ModeDetailCollapsed && roleAllowsThreeLinePreview(role) {
			lines := RenderMarkdownLines(role, text, contentWidth(role, width))
			if len(lines) > 3 {
				lines = lines[:3]
			}
			return attachPrefixWithMeta(role, lines, width, false, mode, toolMeta{})
		}
		return attachPrefixWithMeta(role, RenderMarkdownLines(role, firstDisplayLine(text), contentWidth(role, width)), width, len(strings.Split(text, "\n")) > 1, mode, toolMeta{})
	}
	return attachPrefixWithMeta(role, RenderMarkdownLines(role, text, contentWidth(role, width)), width, false, mode, toolMeta{})
}

func renderTextBlockWithInlineMeta(role StyleRole, text string, inlineMeta string, width int, mode Mode, meta toolMeta) []Line {
	text = safeTranscriptText(text)
	inlineMeta = strings.TrimSpace(safeTranscriptText(inlineMeta))
	text = strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if text == "" {
		text = labelForRole(role)
	}
	if modeUsesCompactTextBlock(mode) {
		first := firstDisplayLine(text)
		if mode == ModeDetailCollapsed && roleAllowsThreeLinePreview(role) {
			return attachPrefixWithMeta(role, textLines(role, firstNWrapped(text, contentWidth(role, width), 3), meta, mode), width, false, mode, meta)
		}
		return attachPrefixWithFirstLineMeta(role, textLines(role, []string{first}, meta, mode), width, len(strings.Split(text, "\n")) > 1, inlineMeta, mode, meta)
	}
	return attachPrefixWithMeta(role, textLines(role, wrapLines(text, contentWidth(role, width)), meta, mode), width, false, mode, meta)
}

func RenderBackgroundedShell(command string, width int) Line {
	if width <= 0 {
		return Line{}
	}
	command = strings.TrimSpace(firstDisplayLine(safeTranscriptText(command)))
	symbol := SemanticSpan("$", StyleRoleToolShellSecondary)
	fixed := []Span{
		SemanticSpan("  ", StyleRoleToolShell, SpanAttributeFaint),
		SemanticSpan(BackgroundedShellSuffix, StyleRoleNoticeForegroundFaint, SpanAttributeFaint),
	}
	fixedWidth := lipgloss.Width(symbol.Text) + spansWidth(fixed)
	if command == "" || width <= fixedWidth {
		return TruncateLine(Line{LeadingSymbol: &symbol, Spans: fixed}, width, false)
	}

	commandWidth := width - fixedWidth - 1
	commandLine := TruncateLine(Line{Spans: []Span{
		SemanticSpan(command, StyleRoleToolShell, SpanAttributeFaint),
	}}, commandWidth, false)
	spans := []Span{SemanticSpan(" ", StyleRoleToolShell, SpanAttributeFaint)}
	spans = append(spans, commandLine.Spans...)
	spans = append(spans,
		SemanticSpan("  ", StyleRoleToolShell, SpanAttributeFaint),
		SemanticSpan(BackgroundedShellSuffix, StyleRoleNoticeForegroundFaint, SpanAttributeFaint),
	)
	return Line{LeadingSymbol: &symbol, Spans: spans}
}

func spansWidth(spans []Span) int {
	width := 0
	for _, span := range spans {
		width += lipgloss.Width(span.Text)
	}
	return width
}

func textLines(role StyleRole, lines []string, meta toolMeta, mode Mode) []Line {
	if len(lines) == 0 {
		return []Line{{Spans: []Span{contentRoleSpan("", role, mode)}}}
	}
	out := make([]Line, 0, len(lines))
	for _, line := range lines {
		if role == StyleRoleToolShell {
			out = append(out, Line{Spans: shellInputSpans(line, meta, mode)})
			continue
		}
		out = append(out, Line{Spans: []Span{contentRoleSpan(line, role, mode)}})
	}
	return out
}

func attachPrefix(role StyleRole, lines []Line, width int, forceEllipsis bool, mode Mode) []Line {
	return attachPrefixWithMeta(role, lines, width, forceEllipsis, mode, toolMeta{})
}

func attachPrefixWithMeta(role StyleRole, lines []Line, width int, forceEllipsis bool, mode Mode, meta toolMeta) []Line {
	return attachPrefixWithFirstLineMeta(role, lines, width, forceEllipsis, "", mode, meta)
}

func attachPrefixWithFirstLineMeta(role StyleRole, lines []Line, width int, forceEllipsis bool, firstLineMeta string, mode Mode, meta toolMeta) []Line {
	return attachPrefixWithFirstLineMetaAndContinuation(
		role,
		lines,
		width,
		forceEllipsis,
		firstLineMeta,
		mode,
		meta,
		continuationForMode,
	)
}

type continuationLayout uint8

const (
	continuationForMode continuationLayout = iota
	continuationTree
)

func attachPrefixWithTree(role StyleRole, lines []Line, width int, mode Mode, meta toolMeta) []Line {
	return attachPrefixWithFirstLineMetaAndContinuation(
		role,
		lines,
		width,
		false,
		"",
		mode,
		meta,
		continuationTree,
	)
}

func attachPrefixWithFirstLineMetaAndContinuation(
	role StyleRole,
	lines []Line,
	width int,
	forceEllipsis bool,
	firstLineMeta string,
	mode Mode,
	meta toolMeta,
	continuation continuationLayout,
) []Line {
	if len(lines) == 0 {
		lines = []Line{{Spans: []Span{SemanticSpan("", role)}}}
	}
	firstLineMeta = strings.TrimSpace(safeTranscriptText(firstLineMeta))
	symbolText := roleSymbolText(role, meta)
	prefixWidth := lipgloss.Width(symbolText + " ")
	bodyWidth := max(1, width-prefixWidth)
	out := make([]Line, 0, len(lines))
	lastIndex := len(lines) - 1
	for idx, line := range lines {
		command := strings.TrimSpace(line.Plain())
		inlineMeta := ""
		spans := line.Spans
		if idx == 0 && firstLineMeta != "" {
			inlineMeta = firstLineMeta
			spans = inlineMetaCommandSpans(spans, role)
		}
		if inlineMeta != "" {
			if modeUsesOngoingContinuationPrefix(mode) {
				spans = ongoingInlineMetaSpans(spans, inlineMeta, bodyWidth)
			} else {
				gap := bodyWidth - lipgloss.Width(command) - lipgloss.Width(inlineMeta)
				if gap < 1 {
					gap = 1
				}
				spans = append(spans, contentRoleSpan(strings.Repeat(" ", gap), role, mode))
				spans = append(spans, SemanticSpan(inlineMeta, StyleRoleNotice, SpanAttributeFaint))
			}
		}
		if idx == 0 {
			symbolRole := roleSymbolStyleRole(role, meta)
			symbol := SemanticSpan(symbolText, symbolRole)
			if mode != ModeDetailExpanded && roleSymbolFaint(role, symbolRole) {
				symbol.Style = symbol.Style.With(SpanAttributeFaint)
			}
			spans = append([]Span{contentRoleSpan(" ", role, mode)}, spans...)
			line = Line{LeadingSymbol: &symbol, Spans: spans, Background: line.Background}
		} else {
			spans = append(continuationPrefix(mode, prefixWidth, idx == lastIndex, continuation), spans...)
			line = Line{Spans: spans, Background: line.Background}
		}
		if forceEllipsis || (mode != ModeOngoingStable && lipgloss.Width(line.Plain()) > max(1, width)) {
			line = TruncateLine(line, max(1, width), forceEllipsis)
		}
		out = append(out, line)
	}
	return out
}

func ongoingInlineMetaSpans(command []Span, inlineMeta string, bodyWidth int) []Span {
	separator := SemanticSpan("  · ", StyleRoleNotice, SpanAttributeFaint)
	meta := SemanticSpan(inlineMeta, StyleRoleNotice, SpanAttributeFaint)
	suffix := []Span{separator, meta}
	suffixWidth := spansWidth(suffix)
	if suffixWidth >= bodyWidth {
		return TruncateLine(Line{Spans: suffix}, max(1, bodyWidth), false).Spans
	}
	commandWidth := bodyWidth - suffixWidth
	command = TruncateLine(Line{Spans: command}, commandWidth, false).Spans
	return append(command, suffix...)
}

func continuationPrefix(mode Mode, prefixWidth int, isLast bool, layout continuationLayout) []Span {
	switch layout {
	case continuationForMode:
		if modeUsesOngoingContinuationPrefix(mode) {
			return []Span{SemanticSpan(strings.Repeat(" ", max(0, prefixWidth)), StyleRoleNotice, SpanAttributeFaint)}
		}
	case continuationTree:
	default:
		panic(fmt.Sprintf("render transcript continuation with invalid layout %d", layout))
	}
	// Detail continuations form a real tree: middle lines use the vertical "│"
	// guide, the last continuation line of the entry closes the tree with "└".
	guide := DetailContinuationGuide
	if isLast {
		guide = DetailContinuationClosingGuide
	}
	return []Span{
		SemanticSpan(guide, StyleRoleNotice, SpanAttributeFaint),
		SemanticSpan(strings.Repeat(" ", max(0, prefixWidth-1)), StyleRoleNotice, SpanAttributeFaint),
	}
}

func inlineMetaCommandSpans(spans []Span, role StyleRole) []Span {
	if role != StyleRoleToolShell {
		return spans
	}
	out := append([]Span(nil), spans...)
	for idx := range out {
		if spanRole, ok := out[idx].Style.Role(); ok && spanRole == role {
			out[idx].Style = out[idx].Style.With(SpanAttributeFaint)
		}
	}
	return out
}

func roleSymbolStyleRole(role StyleRole, meta toolMeta) StyleRole {
	if meta.IsError {
		return StyleRoleToolError
	}
	if meta.SymbolStyleRole != nil {
		return *meta.SymbolStyleRole
	}
	switch role {
	case StyleRoleToolError:
		return StyleRoleToolError
	case StyleRoleToolShell:
		if meta.RawOutputRequested {
			return StyleRoleWarning
		}
		return StyleRoleToolSuccess
	case StyleRoleTool,
		StyleRoleToolPatch,
		StyleRoleToolQuestion,
		StyleRoleToolWebSearch:
		return StyleRoleToolSuccess
	default:
		return role
	}
}

func roleSymbolFaint(role StyleRole, symbolRole StyleRole) bool {
	switch role {
	case StyleRoleTool,
		StyleRoleToolShell,
		StyleRoleToolPatch,
		StyleRoleToolQuestion,
		StyleRoleToolWebSearch,
		StyleRoleToolError:
		return false
	default:
		return roleDefaultFaint(symbolRole)
	}
}

func roleDefaultFaint(role StyleRole) bool {
	switch role {
	case StyleRoleTool,
		StyleRoleToolShell,
		StyleRoleToolShellSecondary,
		StyleRoleToolQuestion,
		StyleRoleToolWebSearch,
		StyleRoleNotice,
		StyleRoleNoticeForegroundFaint:
		return true
	default:
		return false
	}
}

func roleSpan(text string, role StyleRole) Span {
	return contentRoleSpan(text, role, ModeOngoing)
}

func contentRoleSpan(text string, role StyleRole, mode Mode) Span {
	span := SemanticSpan(text, role)
	if mode != ModeDetailExpanded && roleDefaultFaint(role) {
		span.Style = span.Style.With(SpanAttributeFaint)
	}
	return span
}

func roleSymbol(role StyleRole) string {
	return roleSymbolText(role, toolMeta{})
}

func roleSymbolText(role StyleRole, meta toolMeta) string {
	if role == StyleRoleToolPatch {
		return "⇄"
	}
	if meta.IsError && role != StyleRoleToolShell {
		return "!"
	}
	symbol := "•"
	switch role {
	case StyleRoleUser:
		symbol = "❯"
	case StyleRoleAssistant:
		symbol = AssistantSymbol
	case StyleRoleToolShell,
		StyleRoleToolShellSecondary:
		symbol = "$"
	case StyleRoleToolQuestion:
		symbol = "?"
	case StyleRoleToolWebSearch:
		symbol = "@"
	case StyleRoleNotice, StyleRoleNoticeForeground, StyleRoleNoticeForegroundFaint, StyleRoleNoticePrimary, StyleRoleNoticeSecondary:
		symbol = "ℹ"
	case StyleRoleNoticeReviewer:
		symbol = "§"
	case StyleRoleWarning:
		symbol = "⚠"
	case StyleRoleError, StyleRoleToolError:
		symbol = "!"
	}
	return symbol
}

func noticeRoleAndText(row *clientui.TranscriptNoticeRow, visibility clientui.EntryVisibility, mode Mode) (StyleRole, string) {
	if row == nil {
		return StyleRoleNotice, "notice"
	}
	cacheWarningText := cacheWarningNoticeText(row.Data.CacheWarning)
	typedCompactText := firstNonEmpty(row.Data.CompactLabel, row.Data.CondensedText, noticeLegacyText(row), cacheWarningText, row.Data.SourcePath)
	compactText := firstNonEmpty(typedCompactText, string(row.Reason), "notice")
	text := compactText
	if mode == ModeDetailExpanded {
		text = firstNonBlankPreservingWhitespace(noticeLegacyText(row), row.Data.CondensedText, row.Data.CompactLabel, cacheWarningText, row.Data.SourcePath)
		if strings.TrimSpace(text) == "" {
			text = firstNonEmpty(string(row.Reason), "notice")
		}
	}
	if row.Diagnostic != nil && (mode == ModeDetailExpanded || typedCompactText == "") {
		text = firstNonEmpty(row.Diagnostic.Detail, row.Diagnostic.Code, text)
	}
	return noticeStyleRole(row), text
}

func noticeStyleRole(row *clientui.TranscriptNoticeRow) StyleRole {
	if row == nil {
		return StyleRoleNotice
	}
	if row.Severity == clientui.TranscriptNoticeError {
		return StyleRoleError
	}
	if row.Severity == clientui.TranscriptNoticeWarning || row.Reason == clientui.TranscriptNoticeCacheWarning {
		return StyleRoleWarning
	}
	if row.Data.MessageType == clientui.MessageTypeReviewerFeedback || noticeDiagnosticHasReviewerRole(row) {
		return StyleRoleNoticeReviewer
	}
	switch row.Data.MessageType {
	case clientui.MessageTypeInterruption, clientui.MessageTypeErrorFeedback:
		return StyleRoleError
	case clientui.MessageTypeCompactionSoonReminder:
		return StyleRoleWarning
	case clientui.MessageTypeCompactionSummary,
		clientui.MessageTypeManualCompactionCarryover:
		return StyleRoleNoticeSecondary
	case clientui.MessageTypeHandoffFutureMessage,
		clientui.MessageTypeWorktreeMode,
		clientui.MessageTypeSubagents:
		return StyleRoleNotice
	case clientui.MessageTypeGoal, clientui.MessageTypeWorkflowMode:
		return StyleRoleNoticePrimary
	case clientui.MessageTypeBackgroundNotice:
		return StyleRoleNoticeForeground
	case clientui.MessageTypeWorktreeModeExit:
		return StyleRoleNoticeForeground
	default:
		return StyleRoleNotice
	}
}

func noticeDiagnosticHasReviewerRole(row *clientui.TranscriptNoticeRow) bool {
	if row == nil || row.Diagnostic == nil {
		return false
	}
	return transcript.IsReviewerEntryRole(strings.TrimSpace(row.Diagnostic.Code))
}

func noticeLegacyText(row *clientui.TranscriptNoticeRow) string {
	if row == nil || row.Data.LegacyText == nil {
		return ""
	}
	return *row.Data.LegacyText
}

func cacheWarningNoticeText(data *clientui.TranscriptCacheWarningData) string {
	if data == nil {
		return ""
	}
	return transcript.CacheWarningText(transcript.CacheWarning{
		Scope:           transcript.CacheWarningScope(strings.TrimSpace(data.Scope)),
		Reason:          transcript.CacheWarningReason(strings.TrimSpace(data.Reason)),
		LostInputTokens: data.LostInputTokens,
	})
}

func roleAllowsThreeLinePreview(role StyleRole) bool {
	return role == StyleRoleUser || role == StyleRoleAssistant
}

func modeUsesFullUserAssistantText(mode Mode) bool {
	return mode == ModeOngoingFull || mode == ModeOngoingStable || mode == ModeDetailExpanded
}

func modeUsesCompactTextBlock(mode Mode) bool {
	return mode == ModeOngoing || mode == ModeOngoingCollapsed || mode == ModeDetailCollapsed
}

func modeUsesOngoingContinuationPrefix(mode Mode) bool {
	return mode == ModeOngoing || mode == ModeOngoingCollapsed || mode == ModeOngoingFull || mode == ModeOngoingStable
}

func labelForRole(role StyleRole) string {
	switch role {
	case StyleRoleUser:
		return "user"
	case StyleRoleAssistant:
		return "assistant"
	case StyleRoleNotice, StyleRoleNoticeForeground, StyleRoleNoticeForegroundFaint, StyleRoleNoticePrimary, StyleRoleNoticeSecondary, StyleRoleNoticeReviewer:
		return "notice"
	case StyleRoleToolShell,
		StyleRoleToolShellSecondary:
		return "tool call"
	default:
		return "tool call"
	}
}

func firstDisplayLine(text string) string {
	text = safeTranscriptText(text)
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstNWrapped(text string, width int, n int) []string {
	wrapped := wrapLines(text, width)
	if len(wrapped) > n {
		return wrapped[:n]
	}
	return wrapped
}

func wrapLines(text string, width int) []string {
	width = max(1, width)
	text = safeTranscriptText(text)
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			out = append(out, "")
			continue
		}
		var current strings.Builder
		currentWidth := 0
		graphemes := uniseg.NewGraphemes(line)
		for graphemes.Next() {
			cluster := graphemes.Str()
			clusterWidth := uniseg.StringWidth(cluster)
			if current.Len() > 0 && currentWidth+clusterWidth > width {
				out = append(out, current.String())
				current.Reset()
				currentWidth = 0
			}
			current.WriteString(cluster)
			currentWidth += clusterWidth
		}
		if current.Len() > 0 {
			out = append(out, current.String())
		}
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

func contentWidth(role StyleRole, width int) int {
	return max(1, width-lipgloss.Width(roleSymbol(role)+" "))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(safeTranscriptText(value)); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstNonBlankPreservingWhitespace(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(safeTranscriptText(value)) != "" {
			return value
		}
	}
	return ""
}

func safeTranscriptText(text string) string {
	return TerminalSafePlainText(text)
}

func TruncateLine(line Line, width int, forceEllipsis bool) Line {
	if width <= 0 {
		return Line{}
	}
	plain := line.Plain()
	if plain == "" {
		if forceEllipsis {
			return Line{Spans: []Span{SemanticSpan("…", StyleRoleNotice)}, Background: line.Background}
		}
		return line
	}
	if !forceEllipsis && lipgloss.Width(plain) <= width {
		return line
	}
	if width == 1 {
		return Line{Spans: []Span{SemanticSpan("…", StyleRoleNotice)}, Background: line.Background}
	}
	visibleLimit := width - 1
	if forceEllipsis && lipgloss.Width(plain) < width {
		visibleLimit = lipgloss.Width(plain)
	}
	consumed := 0
	out := Line{Background: line.Background}
	type positionedSpan struct {
		span    Span
		leading bool
	}
	spans := make([]positionedSpan, 0, len(line.Spans)+1)
	if line.LeadingSymbol != nil {
		spans = append(spans, positionedSpan{span: *line.LeadingSymbol, leading: true})
	}
	for _, span := range line.Spans {
		spans = append(spans, positionedSpan{span: span})
	}
	appendText := func(span Span, text string, leading bool) {
		fragment := span
		fragment.Text = text
		if leading {
			if out.LeadingSymbol == nil {
				out.LeadingSymbol = &fragment
			} else {
				out.LeadingSymbol.Text += text
			}
			return
		}
		out.Spans = append(out.Spans, fragment)
	}
	for _, positioned := range spans {
		span := positioned.span
		graphemes := uniseg.NewGraphemes(span.Text)
		for graphemes.Next() {
			cluster := graphemes.Str()
			w := lipgloss.Width(cluster)
			if consumed+w > visibleLimit {
				appendText(span, "…", positioned.leading)
				return out
			}
			appendText(span, cluster, positioned.leading)
			consumed += w
		}
	}
	if forceEllipsis || lipgloss.Width(plain) > width {
		out.Spans = append(out.Spans, SemanticSpan("…", StyleRoleNotice))
	}
	return out
}

func max(left, right int) int {
	if left > right {
		return left
	}
	return right
}
