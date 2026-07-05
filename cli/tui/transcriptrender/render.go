package transcriptrender

import (
	"fmt"
	"strings"

	"core/shared/clientui"
	"core/shared/theme"
	patchformat "core/shared/transcript/patchformat"

	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
)

func RenderCommittedRow(row clientui.TranscriptCommittedRow, width int, themeName string, mode Mode) Row {
	switch row.Kind {
	case clientui.TranscriptRowUser:
		return Row{Group: clientui.TranscriptRowUser, Lines: renderTextBlock(StyleRoleUser, row.User.Text, width, themeName, mode)}
	case clientui.TranscriptRowAssistant:
		return Row{Group: clientui.TranscriptRowAssistant, Lines: renderTextBlock(StyleRoleAssistant, row.Assistant.Text, width, themeName, mode)}
	case clientui.TranscriptRowTool:
		return Row{Group: clientui.TranscriptRowTool, Lines: RenderToolRow(*row.Tool, width, themeName, mode)}
	case clientui.TranscriptRowNotice:
		role, text := noticeRoleAndText(row.Notice)
		return Row{Group: clientui.TranscriptRowNotice, Lines: renderTextBlock(role, text, width, themeName, mode)}
	default:
		return Row{Group: clientui.TranscriptRowNotice, Lines: renderTextBlock(StyleRoleNotice, "unknown transcript row", width, themeName, mode)}
	}
}

func RenderToolRow(row clientui.TranscriptToolRow, width int, themeName string, mode Mode) []string {
	meta := normalizeToolMeta(row.ToolName, row.ToolPresentation)
	role := toolRole(meta, row.IsError)
	text := toolDisplayText(row, meta, mode)
	if isPatchTool(meta) {
		return renderPatchTool(role, text, meta.PatchRender, width, themeName, mode)
	}
	return renderTextBlock(role, text, width, themeName, mode)
}

func RenderPendingTool(tool clientui.TranscriptToolStart, width int, themeName string) string {
	meta := normalizeToolMeta(tool.ToolName, tool.ToolPresentation)
	text := compactToolText(meta, tool.ToolName)
	lines := renderTextBlock(toolRole(meta, false), text, width, themeName, ModeOngoing)
	if len(lines) == 0 {
		return ""
	}
	return lines[0]
}

func RenderDivider(group clientui.TranscriptRowKind, width int, themeName string) string {
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
	return applyColor(left+text+right, palette(themeName).mutedColor, true)
}

func RoleHasStyledANSI(line string) bool {
	return strings.Contains(line, "\x1b[")
}

func StripANSI(line string) string {
	return xansi.Strip(line)
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
	if strings.Contains(text, "\n") {
		for _, line := range strings.Split(text, "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				return patchformat.StripEditedLabel(trimmed)
			}
		}
	}
	return patchformat.StripEditedLabel(strings.TrimSpace(text))
}

func renderPatchTool(role StyleRole, text string, rendered *patchformat.RenderedPatch, width int, themeName string, mode Mode) []string {
	if rendered == nil || len(rendered.SummaryLines) == 0 || mode == ModeDetailExpanded {
		return renderTextBlock(role, text, width, themeName, mode)
	}
	p := palette(themeName)
	lines := make([]string, 0, len(rendered.Files))
	for _, file := range rendered.Files {
		path := firstNonEmpty(file.RelPath, file.AbsPath)
		if path == "" {
			continue
		}
		parts := []string{path}
		if file.Removed > 0 {
			parts = append(parts, applyColor(fmt.Sprintf("-%d", file.Removed), p.toolErrorColor, false))
		}
		if file.Added > 0 {
			parts = append(parts, applyColor(fmt.Sprintf("+%d", file.Added), p.toolSuccessColor, false))
		}
		lines = append(lines, strings.Join(parts, " "))
	}
	if len(lines) == 0 {
		lines = []string{text}
	}
	return attachPrefix(role, lines, width, themeName, true)
}

func renderTextBlock(role StyleRole, text string, width int, themeName string, mode Mode) []string {
	text = safeTranscriptText(text)
	text = strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if text == "" {
		text = labelForRole(role)
	}
	if mode == ModeOngoing && roleAllowsThreeLinePreview(role) {
		return attachPrefix(role, wrapLines(text, contentWidth(role, width)), width, themeName, false)
	}
	if mode == ModeOngoing || mode == ModeDetailCollapsed {
		first := firstDisplayLine(text)
		if mode == ModeDetailCollapsed && roleAllowsThreeLinePreview(role) {
			return attachPrefix(role, firstNWrapped(text, contentWidth(role, width), 3), width, themeName, false)
		}
		return attachPrefix(role, []string{first}, width, themeName, strings.Contains(text, "\n"))
	}
	return attachPrefix(role, wrapLines(text, contentWidth(role, width)), width, themeName, false)
}

func attachPrefix(role StyleRole, lines []string, width int, themeName string, forceEllipsis bool) []string {
	if len(lines) == 0 {
		lines = []string{""}
	}
	prefix := roleSymbol(role, themeName) + " "
	bodyWidth := max(1, width-lipgloss.Width(prefix))
	p := palette(themeName)
	out := make([]string, 0, len(lines))
	for idx, line := range lines {
		command, meta := splitInlineMeta(line)
		styled := styleBody(role, command, p)
		if meta != "" {
			metaText := applyColor(meta, p.mutedColor, true)
			gap := bodyWidth - lipgloss.Width(styled) - lipgloss.Width(metaText)
			if gap < 1 {
				gap = 1
			}
			styled += strings.Repeat(" ", gap) + metaText
		}
		if idx == 0 {
			styled = prefix + styled
		} else {
			styled = applyColor("└", p.mutedColor, true) + strings.Repeat(" ", max(0, lipgloss.Width(prefix)-1)) + styled
		}
		if forceEllipsis || lipgloss.Width(styled) > max(1, width) {
			styled = TruncateANSI(styled, max(1, width), forceEllipsis)
		}
		out = append(out, styled)
	}
	return out
}

func styleBody(role StyleRole, text string, p renderPalette) string {
	switch {
	case strings.HasPrefix(text, "+") && !strings.HasPrefix(text, "+++"):
		return applyColor("+", p.toolSuccessColor, false) + text[1:]
	case strings.HasPrefix(text, "-") && !strings.HasPrefix(text, "---"):
		return applyColor("-", p.toolErrorColor, false) + text[1:]
	}
	return applyColor(text, colorForRole(role, p), role == StyleRoleToolShell)
}

func roleSymbol(role StyleRole, themeName string) string {
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
	p := palette(themeName)
	return applyColor(symbol, colorForRole(role, p), false)
}

func noticeRoleAndText(row *clientui.TranscriptNoticeRow) (StyleRole, string) {
	if row == nil {
		return StyleRoleNotice, "notice"
	}
	text := firstNonEmpty(row.Data.CompactLabel, row.Data.CondensedText, noticeLegacyText(row), row.Data.SourcePath, string(row.Reason), "notice")
	if row.Diagnostic != nil {
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

func colorForRole(role StyleRole, p renderPalette) string {
	switch role {
	case StyleRoleUser:
		return p.userColor
	case StyleRoleAssistant:
		return p.assistantColor
	case StyleRoleToolSuccess:
		return p.toolSuccessColor
	case StyleRoleToolError:
		return p.toolErrorColor
	case StyleRoleToolShell:
		return p.toolColor
	case StyleRoleToolPatch:
		return p.toolColor
	case StyleRoleToolQuestion:
		return p.userColor
	case StyleRoleToolWebSearch:
		return p.toolColor
	case StyleRoleWarning:
		return p.warningColor
	case StyleRoleError:
		return p.errorColor
	case StyleRoleNotice:
		return p.primaryColor
	default:
		return p.toolColor
	}
}

type renderPalette struct {
	primaryColor     string
	mutedColor       string
	userColor        string
	assistantColor   string
	toolColor        string
	toolSuccessColor string
	toolErrorColor   string
	warningColor     string
	errorColor       string
}

func palette(themeName string) renderPalette {
	tokens := theme.ResolvePalette(themeName)
	return renderPalette{
		primaryColor:     tokens.App.Primary.TrueColor,
		mutedColor:       tokens.Transcript.Subdued.TrueColor,
		userColor:        tokens.Transcript.User.TrueColor,
		assistantColor:   tokens.Transcript.Assistant.TrueColor,
		toolColor:        tokens.Transcript.Tool.TrueColor,
		toolSuccessColor: tokens.Transcript.ToolSuccess.TrueColor,
		toolErrorColor:   tokens.Transcript.ToolError.TrueColor,
		warningColor:     tokens.Transcript.Warning.TrueColor,
		errorColor:       tokens.Transcript.Error.TrueColor,
	}
}

func applyColor(text string, hex string, faint bool) string {
	if text == "" {
		return text
	}
	r, g, b, ok := parseHexColor(hex)
	if !ok {
		if faint {
			return "\x1b[2m" + text + "\x1b[0m"
		}
		return text
	}
	prefix := fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b)
	if faint {
		prefix += "\x1b[2m"
	}
	return prefix + text + "\x1b[0m"
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
	return max(1, width-lipgloss.Width(roleSymbol(role, "")+" "))
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

func TruncateANSI(line string, width int, forceEllipsis bool) string {
	if width <= 0 {
		return ""
	}
	if line == "" {
		if forceEllipsis {
			return "…"
		}
		return ""
	}
	if !forceEllipsis && lipgloss.Width(line) <= width {
		return line
	}
	if width == 1 {
		return "…"
	}
	parser := xansi.GetParser()
	defer xansi.PutParser(parser)
	visibleLimit := width - 1
	if forceEllipsis && lipgloss.Width(line) < width {
		visibleLimit = lipgloss.Width(line)
	}
	hasANSI := strings.Contains(line, "\x1b[")
	state := byte(0)
	input := line
	consumed := 0
	var out strings.Builder
	for len(input) > 0 {
		seq, seqWidth, n, newState := xansi.GraphemeWidth.DecodeSequenceInString(input, state, parser)
		if n <= 0 {
			break
		}
		state = newState
		if seqWidth == 0 {
			out.WriteString(seq)
			input = input[n:]
			continue
		}
		if consumed+seqWidth > visibleLimit {
			break
		}
		out.WriteString(seq)
		consumed += seqWidth
		input = input[n:]
	}
	if forceEllipsis || lipgloss.Width(line) > width {
		out.WriteString("…")
	}
	if hasANSI {
		out.WriteString("\x1b[0m")
	}
	return out.String()
}

func max(left, right int) int {
	if left > right {
		return left
	}
	return right
}

const inlineMetaSeparator = "\x1f"
