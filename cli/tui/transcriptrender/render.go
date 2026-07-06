package transcriptrender

import (
	"fmt"
	"strings"
	"unicode"

	"core/shared/clientui"
	"core/shared/transcript"
	patchformat "core/shared/transcript/patchformat"

	"github.com/charmbracelet/lipgloss"
	"github.com/rivo/uniseg"
)

func RenderCommittedRow(row clientui.TranscriptCommittedRow, width int, _ string, mode Mode) Row {
	switch row.Kind {
	case clientui.TranscriptRowUser:
		return Row{Group: clientui.TranscriptRowUser, Lines: renderTextBlock(StyleRoleUser, row.User.Text, width, mode)}
	case clientui.TranscriptRowAssistant:
		return Row{Group: clientui.TranscriptRowAssistant, Lines: renderTextBlock(StyleRoleAssistant, row.Assistant.Text, width, mode)}
	case clientui.TranscriptRowTool:
		return Row{Group: clientui.TranscriptRowTool, Lines: RenderToolRow(*row.Tool, width, mode)}
	case clientui.TranscriptRowNotice:
		role, text := noticeRoleAndText(row.Notice, row.Visibility, mode)
		return Row{Group: clientui.TranscriptRowNotice, Lines: renderTextBlock(role, text, width, mode)}
	default:
		return Row{Group: clientui.TranscriptRowNotice, Lines: renderTextBlock(StyleRoleNotice, "unknown transcript row", width, mode)}
	}
}

func RenderToolRow(row clientui.TranscriptToolRow, width int, mode Mode) []Line {
	meta := normalizeToolMeta(row.ToolName, row.ToolPresentation)
	role := toolRole(meta, row.IsError)
	display := toolDisplayText(row, meta, mode)
	if isPatchTool(meta) {
		return renderPatchTool(role, display.Text, display.InlineMeta, meta.PatchRender, width, mode)
	}
	return renderTextBlockWithInlineMeta(role, display.Text, display.InlineMeta, width, mode)
}

func RenderPendingTool(tool clientui.TranscriptToolStart, width int) Line {
	meta := normalizeToolMeta(tool.ToolName, tool.ToolPresentation)
	text := compactToolText(meta, tool.ToolName)
	lines := renderTextBlock(toolRole(meta, false), text, width, ModeOngoing)
	if len(lines) == 0 {
		return Line{}
	}
	return lines[0]
}

func RenderDivider(group clientui.TranscriptRowKind, width int) Line {
	label := "notice"
	switch group {
	case clientui.TranscriptRowUser:
		label = "user"
	case clientui.TranscriptRowAssistant:
		label = "assistant"
	case clientui.TranscriptRowTool:
		label = "tool"
	}
	text := " " + label + " "
	if width <= 0 {
		return Line{}
	}
	labelWidth := lipgloss.Width(text)
	if width <= labelWidth {
		return truncateDividerLine(text, width)
	}
	leftWidth := (width - labelWidth) / 2
	left := strings.Repeat("─", leftWidth)
	right := strings.Repeat("─", width-labelWidth-leftWidth)
	return dividerLine(left + text + right)
}

func dividerLine(text string) Line {
	return Line{Spans: []Span{{Text: text, Role: StyleRoleNotice, Faint: true}}}
}

func truncateDividerLine(text string, width int) Line {
	if width <= 0 {
		return Line{}
	}
	if width == 1 {
		return dividerLine("…")
	}
	line := dividerLine(text)
	if lipgloss.Width(text) <= width {
		return line
	}
	return TruncateLine(line, width, false)
}

type toolMeta struct {
	ToolName               string
	Presentation           clientui.ToolPresentationKind
	RenderBehavior         clientui.ToolCallRenderBehavior
	IsShell                bool
	UserInitiated          bool
	Command                string
	CompactText            string
	InlineMeta             string
	TimeoutLabel           string
	PatchSummary           string
	PatchDetail            string
	PatchRender            *patchformat.RenderedPatch
	Question               string
	Suggestions            []string
	RecommendedOptionIndex int
	RawOutputRequested     bool
	OutputTruncated        bool
}

func normalizeToolMeta(toolName string, in *clientui.ToolCallMeta) toolMeta {
	meta := toolMeta{ToolName: strings.TrimSpace(toolName)}
	if in != nil {
		meta = toolMeta{
			ToolName:               firstNonEmpty(in.ToolName, toolName),
			Presentation:           in.Presentation,
			RenderBehavior:         in.RenderBehavior,
			IsShell:                in.IsShell,
			UserInitiated:          in.UserInitiated,
			Command:                strings.TrimSpace(in.Command),
			CompactText:            strings.TrimSpace(in.CompactText),
			InlineMeta:             strings.TrimSpace(in.InlineMeta),
			TimeoutLabel:           strings.TrimSpace(in.TimeoutLabel),
			PatchSummary:           strings.TrimSpace(in.PatchSummary),
			PatchDetail:            strings.TrimSpace(in.PatchDetail),
			PatchRender:            in.PatchRender,
			Question:               strings.TrimSpace(in.Question),
			Suggestions:            append([]string(nil), in.Suggestions...),
			RecommendedOptionIndex: in.RecommendedOptionIndex,
			RawOutputRequested:     in.RawOutputRequested,
			OutputTruncated:        in.OutputTruncated,
		}
	}
	if meta.Presentation == "" {
		switch {
		case meta.RenderBehavior == clientui.ToolCallRenderBehaviorShell || meta.IsShell || isShellTool(meta.ToolName):
			meta.Presentation = clientui.ToolPresentationShell
		case meta.RenderBehavior == clientui.ToolCallRenderBehaviorAskQuestion || meta.Question != "" || len(meta.Suggestions) > 0:
			meta.Presentation = clientui.ToolPresentationAskQuestion
		default:
			meta.Presentation = clientui.ToolPresentationDefault
		}
	}
	if meta.Presentation == clientui.ToolPresentationShell || meta.RenderBehavior == clientui.ToolCallRenderBehaviorShell {
		meta.IsShell = true
	}
	if meta.InlineMeta == "" {
		meta.InlineMeta = meta.TimeoutLabel
	}
	if meta.TimeoutLabel == "" {
		meta.TimeoutLabel = meta.InlineMeta
	}
	if meta.PatchRender != nil {
		if meta.PatchSummary == "" {
			meta.PatchSummary = strings.TrimSpace(meta.PatchRender.SummaryText())
		}
		if meta.PatchDetail == "" {
			meta.PatchDetail = strings.TrimSpace(meta.PatchRender.DetailText())
		}
	}
	if meta.Command == "" {
		meta.Command = meta.PatchDetail
	}
	if meta.CompactText == "" {
		meta.CompactText = firstNonEmpty(meta.PatchSummary, meta.Command)
	}
	meta.ToolName = strings.TrimSpace(meta.ToolName)
	return meta
}

func toolRole(meta toolMeta, isError bool) StyleRole {
	if isError {
		return StyleRoleToolError
	}
	if isPatchTool(meta) {
		return StyleRoleToolPatch
	}
	if meta.Presentation == clientui.ToolPresentationAskQuestion {
		return StyleRoleToolQuestion
	}
	if isWebSearchTool(meta.ToolName) {
		return StyleRoleToolWebSearch
	}
	if meta.IsShell {
		return StyleRoleToolShell
	}
	return StyleRoleTool
}

type toolDisplay struct {
	Text       string
	InlineMeta string
}

func toolDisplayText(row clientui.TranscriptToolRow, meta toolMeta, mode Mode) toolDisplay {
	if mode == ModeOngoing || mode == ModeDetailCollapsed {
		text := firstNonEmpty(row.CondensedText, compactToolText(meta, row.Text))
		return toolDisplay{Text: text, InlineMeta: firstNonEmpty(row.ResultSummary, meta.InlineMeta)}
	}
	text := firstNonEmpty(meta.PatchDetail, row.Text, meta.Command, meta.CompactText, meta.ToolName)
	if summary := strings.TrimSpace(row.ResultSummary); summary != "" {
		text = text + "\n" + summary
	}
	return toolDisplay{Text: text}
}

func compactToolText(meta toolMeta, fallback string) string {
	text := firstNonEmpty(meta.CompactText, meta.PatchSummary, meta.Command, fallback, meta.ToolName, "tool call")
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return patchformat.StripEditedLabel(trimmed)
		}
	}
	return patchformat.StripEditedLabel(strings.TrimSpace(text))
}

func renderPatchTool(role StyleRole, text string, inlineMeta string, rendered *patchformat.RenderedPatch, width int, mode Mode) []Line {
	if rendered == nil || len(rendered.SummaryLines) == 0 || mode == ModeDetailExpanded {
		return renderTextBlockWithInlineMeta(role, text, inlineMeta, width, mode)
	}
	lines := make([]Line, 0, len(rendered.Files))
	for _, file := range rendered.Files {
		path := firstNonEmpty(file.RelPath, file.AbsPath)
		if path == "" {
			continue
		}
		var spans []Span
		spans = append(spans, Span{Text: path, Role: role})
		if file.Removed > 0 {
			spans = append(spans, Span{Text: " ", Role: role})
			spans = append(spans, Span{Text: fmt.Sprintf("-%d", file.Removed), Role: StyleRoleToolError})
		}
		if file.Added > 0 {
			spans = append(spans, Span{Text: " ", Role: role})
			spans = append(spans, Span{Text: fmt.Sprintf("+%d", file.Added), Role: StyleRoleToolSuccess})
		}
		lines = append(lines, Line{Spans: spans})
	}
	if len(lines) == 0 {
		lines = []Line{{Spans: []Span{{Text: text, Role: role}}}}
	}
	return attachPrefixWithFirstLineMeta(role, lines, width, false, inlineMeta, mode)
}

func renderTextBlock(role StyleRole, text string, width int, mode Mode) []Line {
	return renderTextBlockWithInlineMeta(role, text, "", width, mode)
}

func renderTextBlockWithInlineMeta(role StyleRole, text string, inlineMeta string, width int, mode Mode) []Line {
	text = safeTranscriptText(text)
	inlineMeta = strings.TrimSpace(safeTranscriptText(inlineMeta))
	text = strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if text == "" {
		text = labelForRole(role)
	}
	if mode == ModeOngoing && roleAllowsThreeLinePreview(role) {
		return attachPrefix(role, textLines(role, wrapLines(text, contentWidth(role, width))), width, false, mode)
	}
	if mode == ModeOngoing || mode == ModeDetailCollapsed {
		first := firstDisplayLine(text)
		if mode == ModeDetailCollapsed && roleAllowsThreeLinePreview(role) {
			return attachPrefix(role, textLines(role, firstNWrapped(text, contentWidth(role, width), 3)), width, false, mode)
		}
		return attachPrefixWithFirstLineMeta(role, textLines(role, []string{first}), width, len(strings.Split(text, "\n")) > 1, inlineMeta, mode)
	}
	return attachPrefix(role, textLines(role, wrapLines(text, contentWidth(role, width))), width, false, mode)
}

func textLines(role StyleRole, lines []string) []Line {
	if len(lines) == 0 {
		return []Line{{Spans: []Span{{Role: role}}}}
	}
	out := make([]Line, 0, len(lines))
	for _, line := range lines {
		out = append(out, Line{Spans: []Span{{Text: line, Role: role}}})
	}
	return out
}

func attachPrefix(role StyleRole, lines []Line, width int, forceEllipsis bool, mode Mode) []Line {
	return attachPrefixWithFirstLineMeta(role, lines, width, forceEllipsis, "", mode)
}

func attachPrefixWithFirstLineMeta(role StyleRole, lines []Line, width int, forceEllipsis bool, firstLineMeta string, mode Mode) []Line {
	if len(lines) == 0 {
		lines = []Line{{Spans: []Span{{Role: role}}}}
	}
	firstLineMeta = strings.TrimSpace(safeTranscriptText(firstLineMeta))
	prefixWidth := lipgloss.Width(roleSymbol(role) + " ")
	bodyWidth := max(1, width-prefixWidth)
	out := make([]Line, 0, len(lines))
	for idx, line := range lines {
		command := strings.TrimSpace(line.Plain())
		meta := ""
		spans := line.Spans
		if idx == 0 && firstLineMeta != "" {
			meta = firstLineMeta
			spans = inlineMetaCommandSpans(spans, role)
		}
		if meta != "" {
			gap := bodyWidth - lipgloss.Width(command) - lipgloss.Width(meta)
			if gap < 1 {
				gap = 1
			}
			spans = append(spans, Span{Text: strings.Repeat(" ", gap), Role: role})
			spans = append(spans, Span{Text: meta, Role: StyleRoleNotice, Faint: true})
		}
		if idx == 0 {
			spans = append([]Span{{Text: roleSymbol(role), Role: role}, {Text: " ", Role: role}}, spans...)
		} else {
			spans = append(continuationPrefix(mode, prefixWidth), spans...)
		}
		line = Line{Spans: spans}
		if forceEllipsis || lipgloss.Width(line.Plain()) > max(1, width) {
			line = TruncateLine(line, max(1, width), forceEllipsis)
		}
		out = append(out, line)
	}
	return out
}

func continuationPrefix(mode Mode, prefixWidth int) []Span {
	if mode == ModeOngoing {
		return []Span{{Text: strings.Repeat(" ", max(0, prefixWidth)), Role: StyleRoleNotice, Faint: true}}
	}
	return []Span{
		{Text: "│", Role: StyleRoleNotice, Faint: true},
		{Text: strings.Repeat(" ", max(0, prefixWidth-1)), Role: StyleRoleNotice, Faint: true},
	}
}

func inlineMetaCommandSpans(spans []Span, role StyleRole) []Span {
	if role != StyleRoleToolShell {
		return spans
	}
	out := append([]Span(nil), spans...)
	for idx := range out {
		if out[idx].Role == role {
			out[idx].Faint = true
		}
	}
	return out
}

func roleSymbol(role StyleRole) string {
	symbol := "•"
	switch role {
	case StyleRoleUser:
		symbol = "❯"
	case StyleRoleAssistant:
		symbol = "❮"
	case StyleRoleToolShell:
		symbol = "$"
	case StyleRoleToolPatch:
		symbol = "⇄"
	case StyleRoleToolQuestion:
		symbol = "?"
	case StyleRoleToolWebSearch:
		symbol = "@"
	case StyleRoleNotice:
		symbol = "ℹ"
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
	switch row.Severity {
	case clientui.TranscriptNoticeWarning:
		return StyleRoleWarning, text
	case clientui.TranscriptNoticeError:
		return StyleRoleError, text
	default:
		return StyleRoleNotice, text
	}
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

func isPatchTool(meta toolMeta) bool {
	switch strings.TrimSpace(meta.ToolName) {
	case "patch", "edit":
		return true
	default:
		return meta.PatchRender != nil || meta.PatchSummary != "" || meta.PatchDetail != ""
	}
}

func isShellTool(toolName string) bool {
	switch strings.TrimSpace(toolName) {
	case "exec_command", "write_stdin", "shell":
		return true
	default:
		return false
	}
}

func isWebSearchTool(toolName string) bool {
	return strings.TrimSpace(toolName) == "web_search"
}

func roleAllowsThreeLinePreview(role StyleRole) bool {
	return role == StyleRoleUser || role == StyleRoleAssistant
}

func labelForRole(role StyleRole) string {
	switch role {
	case StyleRoleUser:
		return "user"
	case StyleRoleAssistant:
		return "assistant"
	case StyleRoleNotice:
		return "notice"
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
	var out strings.Builder
	for _, r := range text {
		switch {
		case r == '\n':
			out.WriteRune('\n')
		case r == '\t':
			out.WriteRune(' ')
		case unicode.IsControl(r):
			continue
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

func TruncateLine(line Line, width int, forceEllipsis bool) Line {
	if width <= 0 {
		return Line{}
	}
	plain := line.Plain()
	if plain == "" {
		if forceEllipsis {
			return Line{Spans: []Span{{Text: "…", Role: StyleRoleNotice}}}
		}
		return line
	}
	if !forceEllipsis && lipgloss.Width(plain) <= width {
		return line
	}
	if width == 1 {
		return Line{Spans: []Span{{Text: "…", Role: StyleRoleNotice}}}
	}
	visibleLimit := width - 1
	if forceEllipsis && lipgloss.Width(plain) < width {
		visibleLimit = lipgloss.Width(plain)
	}
	consumed := 0
	out := Line{}
	for _, span := range line.Spans {
		graphemes := uniseg.NewGraphemes(span.Text)
		for graphemes.Next() {
			cluster := graphemes.Str()
			w := lipgloss.Width(cluster)
			if consumed+w > visibleLimit {
				out.Spans = append(out.Spans, Span{Text: "…", Role: span.Role, Faint: span.Faint})
				return out
			}
			out.Spans = append(out.Spans, Span{Text: cluster, Role: span.Role, Faint: span.Faint})
			consumed += w
		}
	}
	if forceEllipsis || lipgloss.Width(plain) > width {
		out.Spans = append(out.Spans, Span{Text: "…", Role: StyleRoleNotice})
	}
	return out
}

func max(left, right int) int {
	if left > right {
		return left
	}
	return right
}
