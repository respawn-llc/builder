package tui

// Deprecated emergency path: this file survives tui-redesign demolition only
// as the temporary non-native transcript entry renderer for this branch. Do not extend.

import (
	"core/shared/theme"
	"core/shared/transcript"
	"strings"

	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
)

func (m Model) entryPrefix(role RenderIntent, symbolOverride string) string {
	if symbolOverride != "" {
		return symbolOverride
	}
	symbol := m.roleSymbol(role)
	if symbol == "" {
		return ""
	}
	if role.IsToolHeadline() && m.toolSymbolGap > 1 {
		return symbol + strings.Repeat(" ", m.toolSymbolGap)
	}
	return symbol + " "
}

func (m Model) detailExpansionSymbolPrefix(role RenderIntent, expanded bool) string {
	symbol := "▶"
	if expanded {
		symbol = "▼"
	}
	return renderRoleSymbol(symbol, m.detailExpansionSymbolStyle(role)) + " "
}

func (m Model) detailExpansionSymbolStyle(role RenderIntent) roleSymbolColorStyle {
	p := m.palette()
	switch transcriptMessageStyleForIntent(role) {
	case transcriptMessageStyleSuccess:
		return roleSymbolColorStyle{color: p.successColor}
	case transcriptMessageStyleWarning:
		return roleSymbolColorStyle{color: p.warningColor}
	case transcriptMessageStyleError:
		return roleSymbolColorStyle{color: p.errorColor}
	}
	switch role {
	case RenderIntentToolSuccess, RenderIntentToolShellSuccess, RenderIntentToolPatchSuccess, RenderIntentToolWebSearchSuccess:
		return roleSymbolColorStyle{color: p.toolSuccessColor}
	case RenderIntentToolError, RenderIntentToolShellError, RenderIntentToolPatchError, RenderIntentToolWebSearchError, RenderIntentToolQuestionError, RenderIntentDeveloperFeedback, RenderIntentInterruption:
		return roleSymbolColorStyle{color: p.toolErrorColor}
	default:
		return roleSymbolColorStyle{color: p.primaryColor}
	}
}

func (m Model) entryRenderWidth(role RenderIntent, symbolOverride string) int {
	railWidth := 0
	if m.compactDetail && m.mode == ModeDetail {
		railWidth = max(lipgloss.Width(theme.SelectionRailBlank), lipgloss.Width(theme.SelectionRailGlyph))
	}
	renderWidth := m.viewportWidth - lipgloss.Width(m.entryPrefix(role, symbolOverride)) - railWidth
	if renderWidth < 1 {
		return 1
	}
	return renderWidth
}

func (m Model) flattenEntryWithMetaAndSymbol(role RenderIntent, text string, muteText bool, toolMeta *transcript.ToolCallMeta, symbolOverride string) []string {
	text = transcriptDisplayText(role, text)
	renderWidth := m.entryRenderWidth(role, symbolOverride)
	if role.IsThinking() {
		return m.flattenThinkingEntry(role, text, renderWidth)
	}
	content := m.renderEntryContentStage(role, text, renderWidth, toolMeta, muteText)
	return m.flattenEntryContent(role, content, renderWidth, muteText, isPatchToolBlock(role, toolMeta), symbolOverride)
}

func (m Model) flattenPlainEntryWithIntents(role RenderIntent, text string, intents StyleIntent, symbolOverride string) []string {
	text = transcriptDisplayText(role, text)
	renderWidth := m.entryRenderWidth(role, symbolOverride)
	content := transcriptRenderContent{
		Lines:    []transcriptRenderLine{{Text: text, Intents: intents}},
		WrapMode: transcriptRenderWrapModeViewport,
	}
	return m.flattenEntryContent(role, content, renderWidth, false, false, symbolOverride)
}

func (m Model) flattenEntryContent(role RenderIntent, content transcriptRenderContent, renderWidth int, muteText bool, isPatchBlock bool, symbolOverride string) []string {
	content = m.applyEntrySemanticTransformStage(content)
	if muteText && role.IsShellPreview() {
		return m.flattenSingleLineShellPreview(role, content, renderWidth, symbolOverride)
	}
	content = m.wrapEntryContentStage(content, renderWidth)
	laidOut := m.layoutEntryContentStage(role, content, symbolOverride)
	decorated := m.decorateEntryLayoutBodyStage(role, laidOut, renderWidth, muteText, isPatchBlock)
	decorated = m.applyDeferredDecoratedLayoutTransformStage(decorated)
	return m.attachRoleSymbolStage(role, decorated, symbolOverride)
}

func (m Model) flattenSingleLineShellPreview(role RenderIntent, content transcriptRenderContent, renderWidth int, symbolOverride string) []string {
	first, forceEllipsis := firstShellPreviewRenderLine(content)
	laidOut := m.layoutEntryContentStage(role, transcriptRenderContent{WrapMode: transcriptRenderWrapModePreserved, Lines: []transcriptRenderLine{first}}, symbolOverride)
	decorated := m.decorateEntryLayoutBodyStage(role, laidOut, renderWidth, true, false)
	decorated = m.applyDeferredDecoratedLayoutTransformStage(decorated)
	out := m.attachRoleSymbolStage(role, decorated, symbolOverride)
	if len(out) == 0 {
		return []string{truncateRenderedLineToWidthWithEllipsis("", max(1, m.viewportWidth), true)}
	}
	targetWidth := m.viewportWidth
	if targetWidth < 1 {
		targetWidth = 1
	}
	return []string{truncateRenderedLineToWidthWithEllipsis(out[0], targetWidth, forceEllipsis)}
}

func (m Model) flattenToolErrorText(role RenderIntent, text string, symbolOverride string) []string {
	renderWidth := m.entryRenderWidth(role, symbolOverride)
	content := transcriptRenderContent{
		Lines:    []transcriptRenderLine{{Text: text, Intents: ErrorForeground}},
		WrapMode: transcriptRenderWrapModeViewport,
	}
	content = m.wrapEntryContentStage(content, renderWidth)
	laidOut := m.layoutEntryContentStage(role, content, symbolOverride)
	decorated := m.decorateEntryLayoutBodyStage(role, laidOut, renderWidth, false, false)
	return m.attachRoleSymbolStage(role, decorated, symbolOverride)
}

func firstShellPreviewRenderLine(content transcriptRenderContent) (transcriptRenderLine, bool) {
	if len(content.Lines) == 0 {
		return transcriptRenderLine{}, true
	}
	first := content.Lines[0]
	parts := splitLines(first.Text)
	if len(parts) == 0 {
		return first, len(content.Lines) > 1
	}
	first.Text = parts[0]
	return first, len(parts) > 1 || len(content.Lines) > 1
}

func truncateRenderedLineToWidthWithEllipsis(line string, width int, forceEllipsis bool) string {
	if width < 1 {
		width = 1
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

	visibleWidth := lipgloss.Width(line)
	visibleLimit := width - 1
	if forceEllipsis && visibleWidth < width {
		visibleLimit = visibleWidth
	}
	if visibleLimit < 0 {
		visibleLimit = 0
	}

	var out strings.Builder
	hasANSI := strings.Contains(line, "\x1b[")
	state := byte(0)
	input := line
	consumedWidth := 0
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
		if consumedWidth+seqWidth > visibleLimit {
			break
		}
		out.WriteString(seq)
		consumedWidth += seqWidth
		input = input[n:]
	}
	out.WriteString("…")
	if hasANSI {
		out.WriteString("\x1b[0m")
	}
	return out.String()
}

func (m Model) renderEntryContentStage(role RenderIntent, text string, width int, toolMeta *transcript.ToolCallMeta, muteText bool) transcriptRenderContent {
	_ = text
	if isPatchToolBlock(role, toolMeta) {
		if content, ok := m.renderPatchSummaryContent(toolMeta); ok {
			return content
		}
	}
	if !muteText {
		if diffLines, ok := m.renderDiffToolLines(text, width, toolMeta); ok {
			return transcriptRenderContent{Lines: diffLines, WrapMode: transcriptRenderWrapModePreserved}
		}
	}
	rendered, intents, wrapMode := m.renderEntryTextStage(role, text, width, toolMeta, muteText)
	return transcriptRenderContent{Lines: []transcriptRenderLine{{Text: rendered, Intents: intents}}, WrapMode: wrapMode}
}

func (m Model) applyEntrySemanticTransformStage(content transcriptRenderContent) transcriptRenderContent {
	palette := m.ansiIntentPalette()
	out := transcriptRenderContent{WrapMode: content.WrapMode, Lines: make([]transcriptRenderLine, 0, len(content.Lines))}
	for _, line := range content.Lines {
		if shouldDeferEntrySemanticTransform(line.Intents) {
			out.Lines = append(out.Lines, line)
			continue
		}
		if line.Intents.Has(SyntaxHighlighted) || strings.Contains(line.Text, "\x1b[") {
			line.Text = applyANSIStyleIntents(line.Text, palette, line.Intents)
		}
		out.Lines = append(out.Lines, line)
	}
	if len(out.Lines) == 0 {
		out.Lines = []transcriptRenderLine{{}}
	}
	return out
}

func (m Model) wrapEntryContentStage(content transcriptRenderContent, width int) transcriptRenderContent {
	if width < 1 {
		width = 1
	}
	if content.WrapMode == transcriptRenderWrapModePreserved {
		if len(content.Lines) == 0 {
			content.Lines = []transcriptRenderLine{{}}
		}
		return content
	}
	out := transcriptRenderContent{WrapMode: transcriptRenderWrapModePreserved, Lines: make([]transcriptRenderLine, 0, len(content.Lines))}
	for _, line := range content.Lines {
		wrapped := splitLines(wrapTextForViewport(line.Text, width))
		if len(wrapped) == 0 {
			wrapped = []string{""}
		}
		for _, chunk := range wrapped {
			out.Lines = append(out.Lines, transcriptRenderLine{Text: chunk, Intents: line.Intents})
		}
	}
	if len(out.Lines) == 0 {
		out.Lines = []transcriptRenderLine{{}}
	}
	return out
}

func (m Model) layoutEntryContentStage(role RenderIntent, content transcriptRenderContent, symbolOverride string) []transcriptLayoutLine {
	hasRoleSymbol := rolePrefix(role) != ""
	continuationPrefix := strings.Repeat(" ", max(0, lipgloss.Width(m.entryPrefix(role, symbolOverride))))
	out := make([]transcriptLayoutLine, 0, len(content.Lines))
	for idx, line := range content.Lines {
		layoutLine := transcriptLayoutLine{Text: line.Text, Intents: line.Intents, PatchSummary: line.PatchSummary}
		if idx == 0 {
			layoutLine.ShowRoleSymbol = hasRoleSymbol
		} else {
			layoutLine.Prefix = continuationPrefix
		}
		out = append(out, layoutLine)
	}
	if len(out) == 0 {
		out = []transcriptLayoutLine{{}}
	}
	return out
}

func (m Model) applyDeferredDecoratedLayoutTransformStage(lines []transcriptLayoutLine) []transcriptLayoutLine {
	if len(lines) == 0 {
		return lines
	}
	palette := m.ansiIntentPalette()
	out := append([]transcriptLayoutLine(nil), lines...)
	for start := 0; start < len(out); {
		if !shouldDeferEntrySemanticTransform(out[start].Intents) {
			start++
			continue
		}
		intents := out[start].Intents
		end := start + 1
		for end < len(out) && out[end].Intents == intents && shouldDeferEntrySemanticTransform(out[end].Intents) {
			end++
		}
		joined := make([]string, 0, end-start)
		for idx := start; idx < end; idx++ {
			joined = append(joined, out[idx].Text)
		}
		transformed := applyANSIStyleIntents(strings.Join(joined, "\n"), palette, intents)
		transformedLines := splitLines(transformed)
		if len(transformedLines) != end-start {
			start = end
			continue
		}
		for idx := start; idx < end; idx++ {
			out[idx].Text = transformedLines[idx-start]
		}
		start = end
	}
	return out
}

func (m Model) decorateEntryLayoutBodyStage(role RenderIntent, lines []transcriptLayoutLine, renderWidth int, muteText bool, isPatchBlock bool) []transcriptLayoutLine {
	out := make([]transcriptLayoutLine, 0, len(lines))
	for idx, line := range lines {
		display := line.Text
		if isPatchBlock && line.Intents.Has(Subdued) {
			line.Intents &^= Subdued
			line.Intents |= ThemeForeground
		}
		if role.IsToolHeadline() {
			if idx == 0 {
				display = m.renderToolHeadline(display, renderWidth)
			}
			if line.PatchSummary != nil {
				display = m.renderPatchSummaryLine(*line.PatchSummary)
			} else {
				display = m.styleToolLine(display)
			}
		}
		if !strings.Contains(display, "\x1b[") {
			display = applyANSIStyleIntents(display, m.ansiIntentPalette(), line.Intents)
		}
		if muteText && strings.TrimSpace(display) != "" && !isPatchBlock && !line.Intents.Has(Subdued) && !line.Intents.Has(Faint) {
			display = m.palette().preview.Faint(true).Render(display)
		} else if isStyledMetaRole(role) {
			display = styleForRole(role, m.palette()).Render(display)
		}
		formatted := display
		if idx > 0 && strings.TrimSpace(display) == "" {
			formatted = ""
		} else if idx > 0 {
			formatted = line.Prefix + display
		}
		if line.Intents.Has(DiffAdded) {
			formatted = m.tintToolDiffLine(formatted, "add")
		}
		if line.Intents.Has(DiffRemoved) {
			formatted = m.tintToolDiffLine(formatted, "remove")
		}
		line.Text = formatted
		out = append(out, line)
	}
	return out
}

func (m Model) attachRoleSymbolStage(role RenderIntent, lines []transcriptLayoutLine, symbolOverride string) []string {
	out := make([]string, 0, len(lines))
	prefix := m.entryPrefix(role, symbolOverride)
	for idx, line := range lines {
		formatted := line.Text
		if idx == 0 && line.ShowRoleSymbol {
			formatted = prefix + formatted
		}
		out = append(out, formatted)
	}
	return out
}

func (m Model) flattenThinkingEntry(role RenderIntent, text string, renderWidth int) []string {
	if renderWidth < 1 {
		renderWidth = 1
	}
	chunks := splitLines(wrapTextForViewport(text, renderWidth))
	if len(chunks) == 0 {
		chunks = []string{""}
	}
	style := styleForRole(role, m.palette())
	out := make([]string, 0, len(chunks))
	for i, chunk := range chunks {
		display := style.Render(chunk)
		if i == 0 {
			out = append(out, display)
			continue
		}
		if strings.TrimSpace(chunk) == "" {
			out = append(out, "")
			continue
		}
		out = append(out, "  "+display)
	}
	return out
}

func (m Model) renderDiffToolLines(text string, width int, toolMeta *transcript.ToolCallMeta) ([]transcriptRenderLine, bool) {
	_ = text
	if toolMeta == nil || !toolMeta.HasRenderHint() || m.code == nil {
		return nil, false
	}
	hint := toolMeta.RenderHint
	if hint == nil || hint.Kind != transcript.ToolRenderKindDiff {
		return nil, false
	}
	if toolMeta.PatchRender == nil {
		return nil, false
	}
	lines, ok := m.code.renderDiffLines(toolMeta.PatchRender, width)
	if !ok {
		return nil, false
	}
	out := make([]transcriptRenderLine, 0, len(lines))
	for _, line := range lines {
		intents := ThemeForeground
		switch line.Kind {
		case diffRenderAdd:
			intents |= SyntaxHighlighted | DiffAdded
		case diffRenderRemove:
			intents |= SyntaxHighlighted | DiffRemoved
		case diffRenderContext:
			intents |= SyntaxHighlighted
		}
		out = append(out, transcriptRenderLine{Text: line.Text, Intents: intents})
	}
	return out, true
}

func (m Model) flattenPatchToolBlockWithSymbol(role RenderIntent, toolMeta *transcript.ToolCallMeta, resultText string, symbolOverride string) []string {
	if toolMeta == nil || toolMeta.PatchRender == nil {
		return m.flattenEntryWithMetaAndSymbol(role, resultText, false, toolMeta, symbolOverride)
	}
	renderWidth := m.entryRenderWidth(role, symbolOverride)
	content := transcriptRenderContent{WrapMode: transcriptRenderWrapModePreserved}
	if diffLines, ok := m.renderDiffToolLines(toolMeta.PatchDetail, renderWidth, toolMeta); ok {
		content.Lines = append(content.Lines, diffLines...)
	}
	trimmedResult := strings.TrimSpace(resultText)
	if trimmedResult != "" {
		if len(content.Lines) > 0 {
			content.Lines = append(content.Lines, transcriptRenderLine{})
		}
		intents := ThemeForeground
		if role == RenderIntentToolError || role == RenderIntentToolPatchError {
			intents = ErrorForeground
		}
		for _, chunk := range splitLines(wrapTextForViewport(trimmedResult, max(1, renderWidth))) {
			content.Lines = append(content.Lines, transcriptRenderLine{Text: chunk, Intents: intents})
		}
	}
	if len(content.Lines) == 0 {
		return m.flattenEntryWithMetaAndSymbol(role, toolMeta.PatchDetail, false, toolMeta, symbolOverride)
	}
	return m.flattenEntryContent(role, content, renderWidth, false, true, symbolOverride)
}

func (m Model) flattenEntryPlain(role RenderIntent, text string) []string {
	text = transcriptDisplayText(role, text)
	renderWidth := m.entryRenderWidth(role, "")
	chunks := splitLines(wrapTextForViewport(text, renderWidth))
	if len(chunks) == 0 {
		chunks = []string{""}
	}
	prefix := m.entryPrefix(role, "")
	continuationPrefix := strings.Repeat(" ", max(0, lipgloss.Width(m.entryPrefix(role, ""))))
	out := make([]string, 0, len(chunks))
	for i, chunk := range chunks {
		if i == 0 {
			if prefix == "" {
				out = append(out, chunk)
				continue
			}
			out = append(out, prefix+chunk)
			continue
		}
		if strings.TrimSpace(chunk) == "" {
			out = append(out, "")
			continue
		}
		out = append(out, continuationPrefix+chunk)
	}
	return out
}

func (m Model) maybeSelectedUserBlock(entryIndex int, role RenderIntent, lines []string) []string {
	if role != RenderIntentUser {
		return lines
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, m.maybeHighlightSelectedTranscriptLine(line, entryIndex))
	}
	return out
}

func (m Model) selectedUserTranscriptEntry() (int, bool) {
	if !m.selectedTranscriptActive {
		return -1, false
	}
	localIndex, ok := m.localTranscriptIndex(m.selectedTranscriptEntry)
	if !ok {
		return -1, false
	}
	if TranscriptRoleFromWire(string(m.transcriptInput.Entries[localIndex].Role)) != TranscriptRoleUser {
		return -1, false
	}
	return m.selectedTranscriptEntry, true
}

func (m Model) selectedTranscriptEntryMatches(entryIndex int) bool {
	selectedEntry, ok := m.selectedUserTranscriptEntry()
	return ok && selectedEntry == entryIndex
}

func (m Model) resolveDetailSelection() (int, bool) {
	if selectedEntry, ok := m.selectedUserTranscriptEntry(); ok {
		return selectedEntry, true
	}
	if m.compactDetail && m.detailSelectedActive {
		return m.detailSelectedEntry, true
	}
	return -1, false
}

func (m Model) maybeHighlightSelectedTranscriptLine(line string, entryIndex int) string {
	selectedEntry, ok := m.selectedUserTranscriptEntry()
	if !ok || entryIndex != selectedEntry {
		return line
	}
	return m.renderSelectedTranscriptLine(line)
}

func (m Model) renderSelectedTranscriptLine(line string) string {
	padded := padRenderedLineToWidth(line, m.viewportWidth)
	if !m.compactDetail {
		palette := m.palette()
		return applyANSIStyleTransform(padded, ansiStyleTransform{DefaultForeground: &palette.selectionForegroundColor, DefaultBackground: &palette.selectionBackgroundColor})
	}
	background := rgbColorFromHex(theme.ResolvePalette(m.theme).App.ModeBg.TrueColor)
	return applyANSIStyleTransform(padded, ansiStyleTransform{DefaultBackground: &background, PreserveBackground: true})
}

func (m Model) renderDetailViewportLine(line string, selected bool) string {
	if !m.compactDetail {
		if selected {
			return m.renderSelectedTranscriptLine(line)
		}
		return line
	}
	originalWidth := lipgloss.Width(line)
	rail := theme.SelectionRailBlank
	if selected {
		rail = renderRoleSymbol(theme.SelectionRailGlyph, roleSymbolColorStyle{color: m.palette().primaryColor})
	}
	line = rail + line
	if originalWidth <= m.viewportWidth {
		for overflow := lipgloss.Width(line) - m.viewportWidth; overflow > 0; overflow = lipgloss.Width(line) - m.viewportWidth {
			trimmed := removeExtraSpacesFromLongestRunLongerThan(line, overflow, 2)
			if trimmed == line {
				break
			}
			line = trimmed
		}
		line = truncateRenderedLineToWidthWithEllipsis(line, max(1, m.viewportWidth), false)
	}
	if !selected {
		return line
	}
	background := rgbColorFromHex(theme.ResolvePalette(m.theme).App.ModeBg.TrueColor)
	return applyANSIStyleTransform(padRenderedLineToWidth(line, m.viewportWidth), ansiStyleTransform{DefaultBackground: &background, PreserveBackground: true})
}

func (m Model) renderDetailSelectionSpacerLine() string {
	if !m.compactDetail {
		return m.renderSelectedTranscriptLine("")
	}
	line := renderRoleSymbol(theme.SelectionRailGlyph, roleSymbolColorStyle{color: m.palette().primaryColor})
	background := rgbColorFromHex(theme.ResolvePalette(m.theme).App.ModeBg.TrueColor)
	return applyANSIStyleTransform(padRenderedLineToWidth(line, m.viewportWidth), ansiStyleTransform{DefaultBackground: &background, PreserveBackground: true})
}

func padRenderedLineToWidth(line string, width int) string {
	if width <= 0 {
		return line
	}
	current := lipgloss.Width(line)
	if current >= width {
		return line
	}
	return line + strings.Repeat(" ", width-current)
}

func (m Model) renderEntryTextStage(role RenderIntent, text string, width int, toolMeta *transcript.ToolCallMeta, muteText bool) (string, StyleIntent, transcriptRenderWrapMode) {
	if strings.TrimSpace(text) == "" {
		return text, 0, transcriptRenderWrapModeViewport
	}
	if role.IsThinking() {
		return text, 0, transcriptRenderWrapModeViewport
	}
	if rendered, intents, ok := m.renderToolTextWithHighlight(role, text, toolMeta, muteText); ok {
		return rendered, intents, transcriptRenderWrapModeViewport
	}
	if shouldUseMutedToolForeground(role, toolMeta, muteText) {
		return text, ThemeForeground | Faint, transcriptRenderWrapModeViewport
	}
	if !isMarkdownRole(role) {
		intents := m.defaultEntryStyleIntents(role, muteText)
		if role.IsToolHeadline() && strings.Contains(text, "\n") {
			intents &^= ThemeForeground
		}
		return text, intents, transcriptRenderWrapModeViewport
	}
	if m.md == nil {
		return text, m.defaultEntryStyleIntents(role, muteText), transcriptRenderWrapModeViewport
	}
	rendered, err := m.md.renderWithRenderer(role, text, width, "plain", m.md.getRenderer)
	if err != nil {
		return text, m.defaultEntryStyleIntents(role, muteText), transcriptRenderWrapModeViewport
	}
	return rendered, ThemeForeground, transcriptRenderWrapModeViewport
}

func shouldUseMutedToolForeground(role RenderIntent, toolMeta *transcript.ToolCallMeta, muteText bool) bool {
	return muteText &&
		role.IsShellPreview() &&
		toolMeta != nil &&
		toolMeta.HasRenderHint() &&
		toolMeta.RenderHint.Kind == transcript.ToolRenderKindPlain
}

func (m Model) defaultEntryStyleIntents(role RenderIntent, muteText bool) StyleIntent {
	if muteText {
		return Subdued
	}
	switch transcriptMessageStyleForIntent(role) {
	case transcriptMessageStyleSuccess:
		return SuccessForeground
	case transcriptMessageStyleWarning:
		return WarningForeground
	case transcriptMessageStyleError:
		return ErrorForeground
	}
	switch role {
	case RenderIntentDeveloperFeedback:
		return WarningForeground
	case RenderIntentInterruption:
		return ErrorForeground
	default:
		if TranscriptRole(role).IsCompaction() {
			return 0
		}
		return ThemeForeground
	}
}

func shouldDeferEntrySemanticTransform(intents StyleIntent) bool {
	return intents.Has(Subdued) && intents.Has(SyntaxHighlighted)
}

func shouldUseLowLevelMutedShellStyle(role RenderIntent, text string, toolMeta *transcript.ToolCallMeta) bool {
	if !role.IsShellPreview() || toolMeta == nil || !toolMeta.HasRenderHint() {
		return false
	}
	hint := toolMeta.RenderHint
	if hint == nil {
		return false
	}
	if hint.Kind == transcript.ToolRenderKindShell {
		return true
	}
	return shouldFallbackToShellPreviewHint(role, text, toolMeta, hint)
}

func (m Model) renderToolTextWithHighlight(role RenderIntent, text string, toolMeta *transcript.ToolCallMeta, muteText bool) (string, StyleIntent, bool) {
	hint, ok := resolveToolRenderHint(role, text, toolMeta)
	if !ok || m.code == nil {
		return "", 0, false
	}
	if muteText && !shouldUseLowLevelMutedShellStyle(role, text, toolMeta) {
		return "", 0, false
	}
	if hint.Kind == transcript.ToolRenderKindDiff {
		return "", 0, false
	}
	highlightTarget := text
	prefix := ""
	if hint.ResultOnly {
		parts := strings.SplitN(text, "\n", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
			return "", 0, false
		}
		prefix = parts[0]
		highlightTarget = parts[1]
	}
	rendered, ok := m.code.render(hint, highlightTarget)
	if !ok {
		return "", 0, false
	}
	if prefix != "" {
		rendered = prefix + "\n" + rendered
	}
	intents := ThemeForeground | SyntaxHighlighted
	if role.IsShellPreview() {
		intents |= ShellPreview
		if muteText && shouldUseLowLevelMutedShellStyle(role, text, toolMeta) {
			intents |= Subdued
		}
	}
	return rendered, intents, true
}

func resolveToolRenderHint(role RenderIntent, text string, toolMeta *transcript.ToolCallMeta) (*transcript.ToolRenderHint, bool) {
	if !role.IsToolHeadline() || toolMeta == nil || !toolMeta.HasRenderHint() {
		return nil, false
	}
	hint := toolMeta.RenderHint
	if hint == nil {
		return nil, false
	}
	if hint.Kind == transcript.ToolRenderKindPlain {
		return nil, false
	}
	if shouldFallbackToShellPreviewHint(role, text, toolMeta, hint) {
		return &transcript.ToolRenderHint{Kind: transcript.ToolRenderKindShell, ShellDialect: hint.ShellDialect}, true
	}
	return hint, true
}

func shouldFallbackToShellPreviewHint(role RenderIntent, text string, toolMeta *transcript.ToolCallMeta, hint *transcript.ToolRenderHint) bool {
	if hint == nil || !hint.ResultOnly || !role.IsShellPreview() || toolMeta == nil || !toolMeta.UsesShellRendering() {
		return false
	}
	parts := strings.SplitN(text, "\n", 2)
	return len(parts) < 2 || strings.TrimSpace(parts[1]) == ""
}

func wrapTextForViewport(text string, width int) string {
	if width < 1 {
		width = 1
	}
	wrapped := xansi.Wordwrap(text, width, " ,.;-+|")
	wrapped = hardWrapOverflowingRenderedLines(wrapped, width)
	return strings.TrimRight(wrapped, "\n")
}

func hardWrapOverflowingRenderedLines(text string, width int) string {
	if width < 1 || text == "" {
		return text
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if lipgloss.Width(line) <= width {
			out = append(out, line)
			continue
		}
		out = append(out, splitLines(xansi.Hardwrap(line, width, true))...)
	}
	return strings.Join(out, "\n")
}
