package transcriptrender

import (
	"fmt"
	"strings"

	"core/shared/clientui"
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
	text := toolDisplayText(row, meta, mode)
	if isPatchTool(meta) {
		return renderPatchTool(role, text, meta.PatchRender, width, mode)
	}
	return renderTextBlock(role, text, width, mode)
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
	if width < lipgloss.Width(text) {
		width = lipgloss.Width(text)
	}
	left := strings.Repeat("─", max(1, (width-lipgloss.Width(text))/2))
	right := strings.Repeat("─", max(1, width-lipgloss.Width(text)-lipgloss.Width(left)))
	return Line{Spans: []Span{{Text: left + text + right, Role: StyleRoleNotice, Faint: true}}}
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

func toolDisplayText(row clientui.TranscriptToolRow, meta toolMeta, mode Mode) string {
	if mode == ModeOngoing || mode == ModeDetailCollapsed {
		text := firstNonEmpty(row.CondensedText, compactToolText(meta, row.Text))
		if summary := strings.TrimSpace(row.ResultSummary); summary != "" {
			text = text + inlineMetaSeparator + summary
		}
		return text
	}
	text := firstNonEmpty(meta.PatchDetail, row.Text, meta.Command, meta.CompactText, meta.ToolName)
	if summary := strings.TrimSpace(row.ResultSummary); summary != "" {
		text = text + "\n" + summary
	}
	return text
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

func renderPatchTool(role StyleRole, text string, rendered *patchformat.RenderedPatch, width int, mode Mode) []Line {
	if rendered == nil || len(rendered.SummaryLines) == 0 || mode == ModeDetailExpanded {
		return renderTextBlock(role, text, width, mode)
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
	return attachPrefix(role, lines, width, false)
}

func renderTextBlock(role StyleRole, text string, width int, mode Mode) []Line {
	text = safeTranscriptText(text)
	text = strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if text == "" {
		text = labelForRole(role)
	}
	if mode == ModeOngoing && roleAllowsThreeLinePreview(role) {
		return attachPrefix(role, textLines(role, wrapLines(text, contentWidth(role, width))), width, false)
	}
	if mode == ModeOngoing || mode == ModeDetailCollapsed {
		first := firstDisplayLine(text)
		if mode == ModeDetailCollapsed && roleAllowsThreeLinePreview(role) {
			return attachPrefix(role, textLines(role, firstNWrapped(text, contentWidth(role, width), 3)), width, false)
		}
		return attachPrefix(role, textLines(role, []string{first}), width, len(strings.Split(text, "\n")) > 1)
	}
	return attachPrefix(role, textLines(role, wrapLines(text, contentWidth(role, width))), width, false)
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

func attachPrefix(role StyleRole, lines []Line, width int, forceEllipsis bool) []Line {
	if len(lines) == 0 {
		lines = []Line{{Spans: []Span{{Role: role}}}}
	}
	prefixWidth := lipgloss.Width(roleSymbol(role) + " ")
	bodyWidth := max(1, width-prefixWidth)
	out := make([]Line, 0, len(lines))
	for idx, line := range lines {
		command, meta := splitInlineMeta(line.Plain())
		spans := line.Spans
		if meta != "" {
			spans = []Span{{Text: command, Role: role, Faint: role == StyleRoleToolShell}}
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
			spans = append([]Span{{Text: "└", Role: StyleRoleNotice, Faint: true}, {Text: strings.Repeat(" ", max(0, prefixWidth-1)), Role: StyleRoleNotice, Faint: true}}, spans...)
		}
		line = Line{Spans: spans}
		if forceEllipsis || lipgloss.Width(line.Plain()) > max(1, width) {
			line = TruncateLine(line, max(1, width), forceEllipsis)
		}
		out = append(out, line)
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
	compactText := firstNonEmpty(row.Data.CompactLabel, row.Data.CondensedText, noticeLegacyText(row), row.Data.SourcePath, string(row.Reason), "notice")
	text := compactText
	if row.Diagnostic != nil && (mode == ModeDetailExpanded || visibility != clientui.EntryVisibilityOngoingCollapsed || compactText == "") {
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
	return strings.TrimSpace(*row.Data.LegacyText)
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
		words := strings.Fields(strings.TrimRight(line, " "))
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		current := ""
		for _, word := range words {
			if current == "" {
				current = word
				continue
			}
			candidate := current + " " + word
			if lipgloss.Width(candidate) > width {
				out = append(out, current)
				current = word
			} else {
				current = candidate
			}
		}
		if current != "" {
			out = append(out, current)
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

func splitInlineMeta(line string) (string, string) {
	parts := strings.SplitN(line, inlineMetaSeparator, 2)
	if len(parts) == 1 {
		return strings.TrimSpace(line), ""
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(safeTranscriptText(value)); trimmed != "" {
			return trimmed
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
		case r < 0x20 || r == 0x7f:
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

const inlineMetaSeparator = "\x1f"
