package transcriptrender

import (
	"fmt"
	"strings"

	"core/shared/clientui"
	"core/shared/config"
	"core/shared/toolspec"
	"core/shared/transcript"
	patchformat "core/shared/transcript/patchformat"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

func renderToolRowWithLinkPresentation(
	row clientui.TranscriptToolRow,
	width int,
	mode Mode,
	syntax *syntaxProjector,
	linkPresentation MarkdownLinkPresentation,
) []Line {
	meta := normalizeToolMeta(row.ToolName, row.Presentation)
	meta.syntax = syntax
	meta.IsError = row.IsError || shellExitFailed(meta)
	role := toolRole(meta)
	if lines, ok := renderAnsweredQuestion(row, meta, width, mode, linkPresentation); ok {
		return lines
	}
	display := toolDisplayText(row, meta, mode)
	if role == StyleRoleToolShell &&
		meta.MovedToBackground &&
		!meta.IsError &&
		mode != ModeDetailExpanded {
		return []Line{renderBackgroundedShell(firstNonEmpty(meta.Command, display.Text), width, mode)}
	}
	if isPatchTool(meta) {
		result := optionalString(row.ResultSummary)
		if mode == ModeDetailExpanded {
			result = detailedToolResultText(row)
		}
		return renderPatchTool(
			role,
			meta.PatchPresentation,
			display.InlineMeta,
			result,
			width,
			mode,
			meta,
			syntax,
		)
	}
	if mode == ModeDetailExpanded {
		input := detailedToolText(meta, row.Text)
		if display.kind == toolDisplaySourceResult {
			return renderDetailedToolWithOutputLines(
				role,
				input,
				sourceResultLines(display.Text, contentWidth(role, width), meta, mode),
				width,
				meta,
			)
		}
		return renderDetailedToolTextBlock(
			role,
			input,
			detailedToolResultText(row),
			width,
			meta,
		)
	}
	return renderTextBlockWithInlineMeta(role, display.Text, display.InlineMeta, width, mode, meta)
}

func renderAnsweredQuestion(
	row clientui.TranscriptToolRow,
	meta toolMeta,
	width int,
	mode Mode,
	linkPresentation MarkdownLinkPresentation,
) ([]Line, bool) {
	if meta.Presentation != transcript.ToolPresentationAskQuestion ||
		meta.IsError ||
		(mode != ModeOngoing && mode != ModeOngoingCollapsed) {
		return nil, false
	}
	answer := strings.TrimSpace(safeTranscriptText(optionalString(row.CondensedText)))
	if answer == "" {
		return nil, false
	}

	question := strings.TrimSpace(safeTranscriptText(firstNonEmpty(meta.Question, meta.Command, meta.CompactText, "ask question")))
	bodyWidth := contentWidth(StyleRoleToolQuestion, width)
	lines := RenderMarkdownLinesWithLinkPresentation(
		StyleRoleUser,
		question,
		bodyWidth,
		linkPresentation,
	)
	lines = append(lines, textLines(StyleRoleToolQuestionAnswer, wrapLines(answer, bodyWidth), meta, mode)...)
	return attachPrefixWithTree(StyleRoleToolQuestion, lines, width, mode, meta), true
}

func RenderPendingTool(tool clientui.TranscriptToolStart, width int, themeName string, spinner string) Line {
	meta := normalizeToolMeta(tool.ToolName, tool.Presentation)
	syntax := newSyntaxProjector(themeName)
	meta.syntax = &syntax
	role := toolRole(meta)
	text := compactToolText(meta, tool.ToolName)
	inlineMeta := ""
	if meta.IsShell {
		if continuation, ok := shellCommandContinuationMetadata(meta.Command); ok {
			inlineMeta = continuation
		}
	}
	var lines []Line
	if isPatchTool(meta) {
		lines = renderPatchTool(
			role,
			meta.PatchPresentation,
			"",
			"",
			width,
			ModeOngoing,
			meta,
			nil,
		)
	} else {
		lines = renderTextBlockWithInlineMeta(role, text, inlineMeta, width, ModeOngoing, meta)
	}
	if len(lines) == 0 {
		return Line{}
	}
	line := lines[0]
	if spinner == "" {
		return line
	}
	return line.WithLeadingSymbolText(spinner)
}

type toolMeta struct {
	transcript.ToolCallMeta
	IsError         bool
	SymbolStyleRole *StyleRole
	syntax          *syntaxProjector
}

func normalizeToolMeta(toolName string, in *transcript.ToolCallMeta) toolMeta {
	adapted := transcript.ToolCallMeta{ToolName: strings.TrimSpace(toolName)}
	if in != nil {
		adapted = *in
		adapted.ToolName = firstNonEmpty(in.ToolName, toolName)
		adapted.Suggestions = append([]string(nil), in.Suggestions...)
		if in.RenderHint != nil {
			adapted.RenderHint = &transcript.ToolRenderHint{
				Kind:         in.RenderHint.Kind,
				Path:         in.RenderHint.Path,
				ResultOnly:   in.RenderHint.ResultOnly,
				ShellDialect: in.RenderHint.ShellDialect,
			}
		}
	}
	return toolMeta{ToolCallMeta: transcript.NormalizeToolCallMeta(adapted)}
}

func toolRole(meta toolMeta) StyleRole {
	if isPatchTool(meta) {
		return StyleRoleToolPatch
	}
	if meta.Presentation == transcript.ToolPresentationAskQuestion {
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

type toolDisplayKind uint8

const (
	toolDisplayDefault toolDisplayKind = iota
	toolDisplaySourceResult
)

const viewImageDisplayPrefix = "Viewed image at "

const webSearchDisplayPrefix = "Searched the web for "

type toolDisplay struct {
	Text       string
	InlineMeta string
	kind       toolDisplayKind
}

func toolDisplayText(row clientui.TranscriptToolRow, meta toolMeta, mode Mode) toolDisplay {
	if mode == ModeOngoing || mode == ModeOngoingCollapsed || mode == ModeDetailCollapsed {
		text := compactToolText(meta, firstNonEmpty(optionalString(row.CondensedText), row.Text))
		status := ""
		if !isWebSearchTool(meta.ToolName) {
			resultSummary := optionalString(row.ResultSummary)
			if isPatchTool(meta) && !meta.IsError {
				return toolDisplay{Text: text}
			}
			if meta.IsError && (mode == ModeOngoing || mode == ModeOngoingCollapsed) && !isPatchTool(meta) {
				resultSummary = ""
			}
			status = firstNonEmpty(shellExitStatus(meta), resultSummary, meta.InlineMeta)
			if meta.IsShell && modeShowsShellContinuationMetadata(mode) {
				if continuation, ok := shellCommandContinuationMetadata(meta.Command); ok {
					status = joinToolInlineMetadata(continuation, status)
				}
			}
		}
		return toolDisplay{Text: text, InlineMeta: status}
	}
	if !meta.IsError &&
		meta.RenderHint != nil &&
		meta.RenderHint.Kind == transcript.ToolRenderKindSource &&
		meta.RenderHint.ResultOnly {
		return toolDisplay{Text: row.Text, kind: toolDisplaySourceResult}
	}
	return toolDisplay{Text: detailedToolText(meta, row.Text)}
}

func shellExitFailed(meta toolMeta) bool {
	return meta.IsShell && meta.ShellExitCode != nil && *meta.ShellExitCode != 0
}

func shellExitStatus(meta toolMeta) string {
	if !shellExitFailed(meta) {
		return ""
	}
	return fmt.Sprintf("exit %d", *meta.ShellExitCode)
}

func shellCommandContinuationMetadata(command string) (string, bool) {
	hiddenLineCount := explicitCommandLineBreakCount(command)
	switch hiddenLineCount {
	case 0:
		return "", false
	case 1:
		return "1 more line", true
	default:
		return fmt.Sprintf("%d more lines", hiddenLineCount), true
	}
}

func modeShowsShellContinuationMetadata(mode Mode) bool {
	return mode == ModeOngoing ||
		mode == ModeOngoingCollapsed ||
		mode == ModeDetailCollapsed
}

func explicitCommandLineBreakCount(command string) int {
	count := 0
	for index := 0; index < len(command); index++ {
		switch command[index] {
		case '\n':
			count++
		case '\r':
			if index+1 >= len(command) || command[index+1] != '\n' {
				count++
			}
		}
	}
	return count
}

func joinToolInlineMetadata(items ...string) string {
	visible := make([]string, 0, len(items))
	for _, item := range items {
		if item = strings.TrimSpace(item); item != "" {
			visible = append(visible, item)
		}
	}
	return strings.Join(visible, " · ")
}

func compactToolText(meta toolMeta, fallback string) string {
	if text, ok := viewImageDisplayText(meta); ok {
		return text
	}
	if text, ok := webSearchDisplayText(meta); ok {
		return text
	}
	return transcript.CompactToolCallText(&meta.ToolCallMeta, fallback)
}

func detailedToolText(meta toolMeta, fallback string) string {
	if text, ok := viewImageDisplayText(meta); ok {
		return text
	}
	return transcript.DetailedToolCallText(&meta.ToolCallMeta, fallback)
}

func viewImageDisplayText(meta toolMeta) (string, bool) {
	toolID, ok := toolspec.ParseID(meta.ToolName)
	if !ok || toolID != toolspec.ToolViewImage ||
		meta.RenderHint == nil ||
		meta.RenderHint.Kind != transcript.ToolRenderKindPlain {
		return "", false
	}
	imagePath := strings.TrimSpace(meta.RenderHint.Path)
	if imagePath == "" {
		return "", false
	}
	return viewImageDisplayPrefix + imagePath, true
}

func webSearchDisplayText(meta toolMeta) (string, bool) {
	toolID, ok := toolspec.ParseID(meta.ToolName)
	if !ok || toolID != toolspec.ToolWebSearch {
		return "", false
	}
	query := strings.TrimSpace(meta.Command)
	if query == "" {
		return "", false
	}
	return webSearchDisplayPrefix + `"` + query + `"`, true
}

func detailedToolResultText(row clientui.TranscriptToolRow) string {
	output := strings.TrimSpace(safeTranscriptText(row.Text))
	summary := strings.TrimSpace(safeTranscriptText(optionalString(row.ResultSummary)))
	if output == summary {
		return output
	}
	if output == "" {
		return summary
	}
	if summary == "" {
		return output
	}
	return output + "\n" + summary
}

func renderDetailedToolTextBlock(
	role StyleRole,
	input string,
	output string,
	width int,
	meta toolMeta,
) []Line {
	return renderDetailedToolWithOutputLines(
		role,
		input,
		detailedToolOutputLines(role, output, contentWidth(role, width)),
		width,
		meta,
	)
}

func renderDetailedToolWithOutputLines(
	role StyleRole,
	input string,
	outputLines []Line,
	width int,
	meta toolMeta,
) []Line {
	inputLines := textLines(role, wrapLines(input, contentWidth(role, width)), meta, ModeDetailExpanded)
	if len(outputLines) > 0 {
		inputLines = append(inputLines, Line{Spans: []Span{contentRoleSpan("", role, ModeDetailExpanded)}})
		inputLines = append(inputLines, outputLines...)
	}
	return attachPrefixWithMeta(role, inputLines, width, false, ModeDetailExpanded, meta)
}

func detailedToolOutputLines(role StyleRole, output string, width int) []Line {
	if strings.TrimSpace(output) == "" {
		return nil
	}
	lines := wrapLines(output, width)
	out := make([]Line, 0, len(lines))
	for _, line := range lines {
		out = append(out, Line{Spans: []Span{contentRoleSpan(line, role, ModeDetailExpanded)}})
	}
	return out
}

func renderPatchTool(
	role StyleRole,
	presentation *patchformat.Presentation,
	inlineMeta string,
	result string,
	width int,
	mode Mode,
	meta toolMeta,
	syntax *syntaxProjector,
) []Line {
	if presentation == nil || !presentation.Valid() {
		panic("render Patch/Edit tool without valid presentation")
	}
	switch presentation.Variant {
	case patchformat.PresentationVariantChanges:
		if mode == ModeDetailExpanded {
			lines := renderPatchChangesDetail(
				presentation.Changes,
				contentWidth(role, width),
				syntax,
			)
			if meta.IsError && result != "" {
				lines = append(lines, Line{Spans: []Span{contentRoleSpan("", role, mode)}})
				lines = append(lines, detailedToolOutputLines(role, result, contentWidth(role, width))...)
			}
			return attachPrefixWithMeta(role, lines, width, false, mode, meta)
		}
		return renderPatchChangesCompact(
			role,
			presentation.Changes,
			inlineMeta,
			width,
			mode,
			meta,
		)
	case patchformat.PresentationVariantInvalidInput:
		label := invalidPatchInputLabel(meta.ToolName)
		if mode != ModeDetailExpanded {
			return renderTextBlockWithInlineMeta(role, label, inlineMeta, width, mode, meta)
		}
		if !meta.IsError {
			result = ""
		}
		return renderInvalidPatchInputDetail(
			role,
			presentation.InvalidInput.InputDetail,
			result,
			width,
			meta,
		)
	default:
		panic(fmt.Sprintf("render unsupported Patch/Edit presentation variant %q", presentation.Variant))
	}
}

func renderPatchChangesCompact(
	role StyleRole,
	changes *patchformat.Changes,
	inlineMeta string,
	width int,
	mode Mode,
	meta toolMeta,
) []Line {
	lines := make([]Line, 0, len(changes.Files))
	for _, file := range changes.Files {
		var spans []Span
		spans = append(spans, patchPathSpan(
			safeTranscriptText(file.Path.Relative),
			file.Path.Absolute,
			role,
		))
		if file.Removed != nil &&
			(*file.Removed > 0 || fileHasOnlyWholeFileDeletion(file)) {
			spans = append(spans, roleSpan(" ", role))
			spans = append(spans, SemanticSpan(fmt.Sprintf("-%d", *file.Removed), StyleRoleToolError))
		}
		if file.Added > 0 {
			spans = append(spans, roleSpan(" ", role))
			spans = append(spans, SemanticSpan(fmt.Sprintf("+%d", file.Added), StyleRoleToolSuccess))
		}
		lines = append(lines, Line{Spans: spans})
	}
	return attachPrefixWithFirstLineMeta(role, lines, width, false, inlineMeta, mode, meta)
}

const (
	patchSyntaxBatchMaxLines = 400
	patchSyntaxBatchMaxBytes = 64 * 1024
)

type patchSourceLine struct {
	kind patchformat.ChangedLineKind
	text string
}

func renderPatchChangesDetail(
	changes *patchformat.Changes,
	width int,
	syntax *syntaxProjector,
) []Line {
	if syntax == nil {
		panic("render Patch/Edit changes without syntax projector")
	}
	width = max(1, width)
	out := make([]Line, 0, len(changes.Files))
	for _, file := range changes.Files {
		out = append(out, wrapPatchFileLine(file, width)...)
		out = append(out, renderPatchFileSource(file, width, syntax)...)
	}
	return out
}

func wrapPatchFileLine(file patchformat.FileChange, width int) []Line {
	spans := []Span{patchPathSpan(
		safeTranscriptText(file.Path.Absolute),
		file.Path.Absolute,
		StyleRoleToolPatch,
	)}
	if fileHasOnlyWholeFileDeletion(file) && file.Removed != nil {
		spans = append(spans, roleSpan(" ", StyleRoleToolPatch))
		spans = append(spans, SemanticSpan(
			fmt.Sprintf("-%d", *file.Removed),
			StyleRoleToolError,
		))
	}
	return wrapStyledLine(spans, width)
}

func fileHasOnlyWholeFileDeletion(file patchformat.FileChange) bool {
	if len(file.Operations) == 0 {
		return false
	}
	for _, operation := range file.Operations {
		if operation.Kind != patchformat.FileOperationDelete {
			return false
		}
	}
	return true
}

func renderPatchFileSource(
	file patchformat.FileChange,
	width int,
	syntax *syntaxProjector,
) []Line {
	out := make([]Line, 0)
	var inferredLexer chroma.Lexer
	inferredLexerResolved := false
	lexer := lexers.Match(strings.TrimSpace(file.Path.Absolute))
	pending := make([]patchSourceLine, 0, 16)
	pendingBytes := 0
	flushPending := func() {
		if len(pending) == 0 {
			return
		}
		sourceLines := make([]string, 0, len(pending))
		for _, line := range pending {
			sourceLines = append(sourceLines, line.text)
		}
		source := strings.Join(sourceLines, "\n")
		selectedLexer := lexer
		if selectedLexer == nil {
			if !inferredLexerResolved {
				inferredLexer = lexers.Analyse(source)
				inferredLexerResolved = true
			}
			selectedLexer = inferredLexer
		}
		highlighted := syntax.highlight(selectedLexer, source)
		for index, line := range pending {
			out = append(out, wrapPatchSourceLine(line.kind, highlighted[index], width)...)
		}
		pending = pending[:0]
		pendingBytes = 0
	}
	for _, operation := range file.Operations {
		for _, group := range operation.Groups {
			for _, line := range group.Lines {
				text := safeTranscriptText(line.Content)
				pending = append(pending, patchSourceLine{kind: line.Kind, text: text})
				pendingBytes += len(text) + 1
				if len(pending) >= patchSyntaxBatchMaxLines ||
					pendingBytes >= patchSyntaxBatchMaxBytes {
					flushPending()
				}
			}
		}
	}
	flushPending()
	return out
}

func invalidPatchInputLabel(toolName string) string {
	if toolID, ok := toolspec.ParseID(toolName); ok && toolID == toolspec.ToolEdit {
		return "Edit failed"
	}
	return "Patch failed"
}

func renderInvalidPatchInputDetail(
	role StyleRole,
	input string,
	result string,
	width int,
	meta toolMeta,
) []Line {
	bodyWidth := contentWidth(role, width)
	lines := make([]Line, 0)
	if input != "" {
		lines = append(lines, textLines(
			role,
			wrapLines(safeTranscriptText(input), bodyWidth),
			meta,
			ModeDetailExpanded,
		)...)
	}
	if result != "" {
		if len(lines) > 0 {
			lines = append(lines, Line{Spans: []Span{contentRoleSpan("", role, ModeDetailExpanded)}})
		}
		lines = append(lines, detailedToolOutputLines(role, result, bodyWidth)...)
	}
	return attachPrefixWithMeta(role, lines, width, false, ModeDetailExpanded, meta)
}

func patchPathSpan(text, path string, role StyleRole) Span {
	span := roleSpan(text, role)
	if uri, ok := config.LocalFileURL(path); ok {
		span.Hyperlink = &Hyperlink{URL: uri.String()}
	}
	return span
}

func wrapPatchSourceLine(kind patchformat.ChangedLineKind, source []Span, width int) []Line {
	var marker string
	var markerRole StyleRole
	var background LineBackground
	switch kind {
	case patchformat.ChangedLineAdded:
		marker = "+"
		markerRole = StyleRoleToolSuccess
		background = LineBackgroundDiffAdded
	case patchformat.ChangedLineRemoved:
		marker = "-"
		markerRole = StyleRoleToolError
		background = LineBackgroundDiffRemoved
	default:
		panic(fmt.Sprintf("render unsupported Patch/Edit changed-line kind %q", kind))
	}
	wrapped := wrapStyledLine(source, max(1, width-1))
	out := make([]Line, 0, len(wrapped))
	for index, line := range wrapped {
		prefix := " "
		role := StyleRoleToolPatch
		if index == 0 {
			prefix = marker
			role = markerRole
		}
		out = append(out, Line{
			Spans:      append([]Span{SemanticSpan(prefix, role)}, line.Spans...),
			Background: background,
		})
	}
	return out
}

func isPatchTool(meta toolMeta) bool {
	return transcript.IsPatchFamilyToolName(meta.ToolName)
}

func isWebSearchTool(toolName string) bool {
	toolID, ok := toolspec.ParseID(toolName)
	return ok && toolID == toolspec.ToolWebSearch
}
