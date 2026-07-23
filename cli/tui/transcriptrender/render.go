package transcriptrender

import (
	"fmt"
	"path/filepath"
	"strings"

	"core/shared/clientui"
	"core/shared/transcript"

	"github.com/charmbracelet/lipgloss"
	"github.com/rivo/uniseg"
)

const (
	backgroundedShellStatus = "backgrounded"
	BackgroundedShellSuffix = "· " + backgroundedShellStatus
)

func RenderCommittedRow(row clientui.TranscriptCommittedRow, width int, themeName string, mode Mode) Row {
	return RenderCommittedRowWithLinkPresentation(
		row,
		width,
		themeName,
		mode,
		MarkdownLinkLabelOnly,
	)
}

func RenderCommittedRowWithLinkPresentation(
	row clientui.TranscriptCommittedRow,
	width int,
	themeName string,
	mode Mode,
	linkPresentation MarkdownLinkPresentation,
) Row {
	if !linkPresentation.Valid() {
		panic(fmt.Sprintf("render committed row with invalid Markdown link presentation %d", linkPresentation))
	}
	var syntax *syntaxProjector
	if row.Kind == clientui.TranscriptRowTool {
		configured := newSyntaxProjector(themeName)
		syntax = &configured
	}
	return renderCommittedRow(row, width, mode, syntax, linkPresentation)
}

func renderCommittedRow(
	row clientui.TranscriptCommittedRow,
	width int,
	mode Mode,
	syntax *syntaxProjector,
	linkPresentation MarkdownLinkPresentation,
) Row {
	switch row.Kind {
	case clientui.TranscriptRowUser:
		return Row{
			Group: clientui.TranscriptRowUser,
			Lines: renderUserAssistantTextBlock(
				StyleRoleUser,
				userAssistantDisplayText(row.User.Text, optionalString(row.User.CondensedText), mode),
				width,
				mode,
				linkPresentation,
			),
		}
	case clientui.TranscriptRowAssistant:
		return Row{
			Group: clientui.TranscriptRowAssistant,
			Lines: renderUserAssistantTextBlock(
				StyleRoleAssistant,
				userAssistantDisplayText(row.Assistant.Text, optionalString(row.Assistant.CondensedText), mode),
				width,
				mode,
				linkPresentation,
			),
		}
	case clientui.TranscriptRowTool:
		return Row{
			Group: clientui.TranscriptRowTool,
			Lines: renderToolRowWithLinkPresentation(
				*row.Tool,
				width,
				mode,
				syntax,
				linkPresentation,
			),
		}
	case clientui.TranscriptRowNotice:
		role, text := noticeRoleAndText(row.Notice, row.Visibility, mode)
		meta := toolMeta{}
		group := clientui.TranscriptRowNotice
		if row.Notice != nil && row.Notice.MessageType != nil && *row.Notice.MessageType == clientui.TranscriptMessageBackgroundNotice {
			symbolRole := StyleRoleNoticePrimary
			meta.SymbolStyleRole = &symbolRole
			group = clientui.TranscriptRowTool
		}
		options := textBlockOptions{}
		if isReviewerNotice(row.Notice) {
			options.compactEllipsis = compactEllipsisNever
		}
		if noticeUsesMarkdown(row.Notice) {
			return Row{
				Group: group,
				Lines: renderMarkdownTextBlock(
					role,
					text,
					width,
					mode,
					meta,
					options,
					linkPresentation,
				),
			}
		}
		return Row{
			Group: group,
			Lines: renderTextBlockWithOptions(role, text, "", width, mode, meta, options),
		}
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

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func renderTextBlock(role StyleRole, text string, width int, mode Mode) []Line {
	return renderTextBlockWithInlineMeta(role, text, "", width, mode, toolMeta{})
}

func renderUserAssistantTextBlock(
	role StyleRole,
	text string,
	width int,
	mode Mode,
	linkPresentation MarkdownLinkPresentation,
) []Line {
	text = safeTranscriptText(text)
	text = strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if text == "" {
		text = labelForRole(role)
	}
	if mode == ModeOngoingStable {
		return attachPrefixWithMeta(
			role,
			RenderMarkdownStableLinesWithLinkPresentation(
				role,
				text,
				contentWidth(role, width),
				linkPresentation,
			),
			width,
			false,
			mode,
			toolMeta{},
		)
	}
	if modeUsesCompactTextBlock(mode) {
		if mode == ModeDetailCollapsed && roleAllowsThreeLinePreview(role) {
			lines := RenderMarkdownLinesWithLinkPresentation(
				role,
				text,
				contentWidth(role, width),
				linkPresentation,
			)
			if len(lines) > 3 {
				lines = lines[:3]
			}
			return attachPrefixWithMeta(role, lines, width, false, mode, toolMeta{})
		}
		return attachPrefixWithMeta(
			role,
			RenderMarkdownLinesWithLinkPresentation(
				role,
				firstDisplayLine(text),
				contentWidth(role, width),
				linkPresentation,
			),
			width,
			len(strings.Split(text, "\n")) > 1,
			mode,
			toolMeta{},
		)
	}
	return attachPrefixWithMeta(
		role,
		RenderMarkdownLinesWithLinkPresentation(
			role,
			text,
			contentWidth(role, width),
			linkPresentation,
		),
		width,
		false,
		mode,
		toolMeta{},
	)
}

func renderTextBlockWithInlineMeta(role StyleRole, text string, inlineMeta string, width int, mode Mode, meta toolMeta) []Line {
	return renderTextBlockWithOptions(role, text, inlineMeta, width, mode, meta, textBlockOptions{})
}

type textBlockOptions struct {
	compactEllipsis compactEllipsisPolicy
}

type compactEllipsisPolicy uint8

const (
	compactEllipsisWhenMultiline compactEllipsisPolicy = iota
	compactEllipsisNever
)

type textBlockFormat uint8

const (
	textBlockPlain textBlockFormat = iota
	textBlockMarkdown
)

type textBlockLayout uint8

const (
	textBlockLayoutCompact textBlockLayout = iota
	textBlockLayoutFull
)

func renderTextBlockWithOptions(
	role StyleRole,
	text string,
	inlineMeta string,
	width int,
	mode Mode,
	meta toolMeta,
	options textBlockOptions,
) []Line {
	return renderFormattedTextBlock(
		role,
		text,
		inlineMeta,
		width,
		mode,
		meta,
		options,
		MarkdownLinkLabelOnly,
		textBlockPlain,
	)
}

func renderMarkdownTextBlock(
	role StyleRole,
	text string,
	width int,
	mode Mode,
	meta toolMeta,
	options textBlockOptions,
	linkPresentation MarkdownLinkPresentation,
) []Line {
	return renderFormattedTextBlock(role, text, "", width, mode, meta, options, linkPresentation, textBlockMarkdown)
}

func renderFormattedTextBlock(
	role StyleRole,
	text string,
	inlineMeta string,
	width int,
	mode Mode,
	meta toolMeta,
	options textBlockOptions,
	linkPresentation MarkdownLinkPresentation,
	format textBlockFormat,
) []Line {
	text = safeTranscriptText(text)
	inlineMeta = strings.TrimSpace(safeTranscriptText(inlineMeta))
	text = strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if text == "" {
		text = labelForRole(role)
	}
	if modeUsesCompactTextBlock(mode) {
		first := firstDisplayLine(text)
		return attachPrefixWithFirstLineMeta(
			role,
			formattedTextLines(role, first, width, mode, meta, format, textBlockLayoutCompact, linkPresentation),
			width,
			options.compactEllipsis == compactEllipsisWhenMultiline && len(strings.Split(text, "\n")) > 1,
			inlineMeta,
			mode,
			meta,
		)
	}
	return attachPrefixWithMeta(
		role,
		formattedTextLines(role, text, width, mode, meta, format, textBlockLayoutFull, linkPresentation),
		width,
		false,
		mode,
		meta,
	)
}

func formattedTextLines(
	role StyleRole,
	text string,
	width int,
	mode Mode,
	meta toolMeta,
	format textBlockFormat,
	layout textBlockLayout,
	linkPresentation MarkdownLinkPresentation,
) []Line {
	switch format {
	case textBlockMarkdown:
		return RenderMarkdownLinesWithLinkPresentation(
			role,
			text,
			contentWidth(role, width),
			linkPresentation,
		)
	case textBlockPlain:
		switch layout {
		case textBlockLayoutCompact:
			return textLines(role, []string{text}, meta, mode)
		case textBlockLayoutFull:
			return textLines(role, wrapLines(text, contentWidth(role, width)), meta, mode)
		default:
			panic(fmt.Sprintf("render plain text block with invalid layout %d", layout))
		}
	default:
		panic(fmt.Sprintf("render text block with invalid format %d", format))
	}
}

func renderBackgroundedShell(command string, width int, mode Mode) Line {
	if width <= 0 {
		return Line{}
	}
	command = safeTranscriptText(command)
	inlineMeta := backgroundedShellStatus
	if modeShowsShellContinuationMetadata(mode) {
		if continuation, ok := shellCommandContinuationMetadata(command); ok {
			inlineMeta = joinToolInlineMetadata(continuation, inlineMeta)
		}
	}
	command = strings.TrimSpace(firstDisplayLine(command))
	symbol := SemanticSpan("$", StyleRoleToolShellSecondary)
	fixed := []Span{
		SemanticSpan("  ", StyleRoleToolShell, SpanAttributeFaint),
		SemanticSpan("· "+inlineMeta, StyleRoleNoticeForegroundFaint, SpanAttributeFaint),
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
	spans = append(spans, fixed...)
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
			if (mode != ModeDetailExpanded || roleAlwaysFaint(symbolRole)) && roleSymbolFaint(role, symbolRole) {
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
		if mode == ModeOngoingStable {
			return nil
		}
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
		StyleRoleToolQuestionAnswer,
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
	if roleDefaultFaint(role) && (mode != ModeDetailExpanded || roleAlwaysFaint(role)) {
		span.Style = span.Style.With(SpanAttributeFaint)
	}
	return span
}

func roleAlwaysFaint(role StyleRole) bool {
	return role == StyleRoleNoticeForegroundFaint
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
	if text, ok := worktreeNoticeText(row); ok {
		return noticeStyleRole(row), text
	}
	cacheWarningText := cacheWarningNoticeText(row.CacheWarning)
	typedCompactText := firstNonEmpty(optionalString(row.CompactLabel), optionalString(row.CondensedText), noticeLegacyText(row), cacheWarningText, optionalString(row.SourcePath))
	compactText := firstNonEmpty(typedCompactText, string(row.Reason), "notice")
	text := compactText
	if mode == ModeDetailExpanded {
		text = firstNonBlankPreservingWhitespace(noticeLegacyText(row), optionalString(row.CondensedText), optionalString(row.CompactLabel), cacheWarningText, optionalString(row.SourcePath))
		if strings.TrimSpace(text) == "" {
			text = firstNonEmpty(string(row.Reason), "notice")
		}
	}
	if row.Diagnostic != nil && (mode == ModeDetailExpanded || typedCompactText == "") {
		text = firstNonEmpty(row.Diagnostic.Detail, string(row.Diagnostic.Code), text)
	}
	if isReasoningNotice(row) {
		text = stripReasoningBoldDelimiters(text)
	}
	return noticeStyleRoleForMode(row, mode), text
}

func worktreeNoticeText(row *clientui.TranscriptNoticeRow) (string, bool) {
	context := row.Worktree
	if context == nil {
		return "", false
	}
	effectiveCWD := strings.TrimSpace(context.EffectiveCwd)
	if effectiveCWD == "" {
		effectiveCWD = strings.TrimSpace(context.WorktreePath)
	}
	if row.MessageType == nil {
		return "", false
	}
	switch *row.MessageType {
	case clientui.TranscriptMessageWorktreeMode:
		name := ""
		if context.Branch != nil {
			name = strings.TrimSpace(*context.Branch)
		}
		if name == "" {
			name = strings.TrimSpace(filepath.Base(strings.TrimSpace(context.WorktreePath)))
		}
		if name == "" || name == "." || name == string(filepath.Separator) {
			name = "worktree"
		}
		if effectiveCWD == "" {
			return "Switched worktree to " + name, true
		}
		return "Switched worktree to " + name + ": " + effectiveCWD, true
	case clientui.TranscriptMessageWorktreeModeExit:
		if effectiveCWD == "" {
			return "Switched worktree to main workspace", true
		}
		return "Switched worktree to main workspace: " + effectiveCWD, true
	default:
		return "", false
	}
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
	if isReasoningNotice(row) {
		return StyleRoleNoticeForegroundFaint
	}
	if isReviewerNotice(row) {
		return StyleRoleNoticeReviewer
	}
	if row.MessageType == nil {
		return StyleRoleNotice
	}
	switch *row.MessageType {
	case clientui.TranscriptMessageInterruption, clientui.TranscriptMessageErrorFeedback:
		return StyleRoleError
	case clientui.TranscriptMessageCompactionSoonReminder:
		return StyleRoleWarning
	case clientui.TranscriptMessageCompactionSummary,
		clientui.TranscriptMessageManualCompactionCarryover:
		return StyleRoleNoticeSecondary
	case clientui.TranscriptMessageHandoffFutureMessage,
		clientui.TranscriptMessageWorktreeMode,
		clientui.TranscriptMessageSubagents:
		return StyleRoleNotice
	case clientui.TranscriptMessageGoal, clientui.TranscriptMessageWorkflowMode:
		return StyleRoleNoticePrimary
	case clientui.TranscriptMessageBackgroundNotice:
		return StyleRoleNoticeForeground
	case clientui.TranscriptMessageWorktreeModeExit:
		return StyleRoleNoticeForeground
	default:
		return StyleRoleNotice
	}
}

func noticeStyleRoleForMode(row *clientui.TranscriptNoticeRow, mode Mode) StyleRole {
	if mode == ModeDetailExpanded && isExpandedCompactionNotice(row) {
		return StyleRoleNotice
	}
	return noticeStyleRole(row)
}

func isExpandedCompactionNotice(row *clientui.TranscriptNoticeRow) bool {
	if row == nil || row.MessageType == nil {
		return false
	}
	switch *row.MessageType {
	case clientui.TranscriptMessageCompactionSummary,
		clientui.TranscriptMessageCompactionSoonReminder:
		return true
	default:
		return false
	}
}

func noticeDiagnosticHasReviewerRole(row *clientui.TranscriptNoticeRow) bool {
	if row == nil || row.Diagnostic == nil {
		return false
	}
	return transcript.IsReviewerEntryRole(strings.TrimSpace(string(row.Diagnostic.Code)))
}

func isReviewerNotice(row *clientui.TranscriptNoticeRow) bool {
	return row != nil &&
		((row.MessageType != nil && *row.MessageType == clientui.TranscriptMessageReviewerFeedback) ||
			noticeDiagnosticHasReviewerRole(row))
}

func isReasoningNotice(row *clientui.TranscriptNoticeRow) bool {
	return row != nil &&
		row.Diagnostic != nil &&
		transcript.EntryRole(row.Diagnostic.Code) == transcript.EntryRoleReasoning
}

func noticeUsesMarkdown(row *clientui.TranscriptNoticeRow) bool {
	if row == nil || row.MessageType == nil {
		return false
	}
	switch *row.MessageType {
	case clientui.TranscriptMessageAgentsMD,
		clientui.TranscriptMessageSkills,
		clientui.TranscriptMessageSubagents,
		clientui.TranscriptMessageEnvironment,
		clientui.TranscriptMessageCompactionSummary,
		clientui.TranscriptMessageHeadlessMode,
		clientui.TranscriptMessageHeadlessModeExit,
		clientui.TranscriptMessageWorkflowMode,
		clientui.TranscriptMessageActiveGoalContinuation:
		return true
	default:
		return false
	}
}

func stripReasoningBoldDelimiters(text string) string {
	// Codex/GPT reasoning traces are provider-formatted plaintext, not Markdown.
	// Remove only their outer `**` delimiters; interior asterisks are literal.
	// Do not remove or change this behavior without explicit user approval.
	text = strings.TrimPrefix(text, "**")
	return strings.TrimSuffix(text, "**")
}

func noticeLegacyText(row *clientui.TranscriptNoticeRow) string {
	if row == nil || row.LegacyText == nil {
		return ""
	}
	return *row.LegacyText
}

func cacheWarningNoticeText(data *clientui.TranscriptCacheWarning) string {
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
	return max(1, width-RolePrefixWidth(role))
}

// RolePrefixWidth returns the terminal cells reserved for a row's symbol and gap.
func RolePrefixWidth(role StyleRole) int {
	return lipgloss.Width(roleSymbol(role) + " ")
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
