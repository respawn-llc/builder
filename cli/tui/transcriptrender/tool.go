package transcriptrender

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"core/shared/clientui"
	"core/shared/transcript"
	patchformat "core/shared/transcript/patchformat"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

func RenderToolRow(row clientui.TranscriptToolRow, width int, mode Mode) []Line {
	var syntax *syntaxProjector
	if mode == ModeDetailExpanded {
		configured := newSyntaxProjector("")
		syntax = &configured
	}
	return renderToolRow(row, width, mode, syntax)
}

func renderToolRow(
	row clientui.TranscriptToolRow,
	width int,
	mode Mode,
	syntax *syntaxProjector,
) []Line {
	meta := normalizeToolMeta(row.ToolName, row.ToolPresentation)
	meta.syntax = syntax
	meta.IsError = row.IsError
	role := toolRole(meta)
	display := toolDisplayText(row, meta, mode)
	if role == StyleRoleToolShell && meta.MovedToBackground && !meta.IsError {
		return []Line{RenderBackgroundedShell(display.Text, width)}
	}
	if isPatchTool(meta) {
		input := display.Text
		result := row.ResultSummary
		if mode == ModeDetailExpanded {
			input = detailedToolText(meta, row.Text)
			result = detailedToolResultText(row)
		}
		return renderPatchTool(role, input, display.InlineMeta, result, meta.PatchRender, width, mode, meta, syntax)
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

func RenderPendingTool(tool clientui.TranscriptToolStart, width int, themeName string, spinner string) Line {
	meta := normalizeToolMeta(tool.ToolName, tool.ToolPresentation)
	syntax := newSyntaxProjector(themeName)
	meta.syntax = &syntax
	role := toolRole(meta)
	text := compactToolText(meta, tool.ToolName)
	var lines []Line
	if isPatchTool(meta) {
		lines = renderPatchTool(role, text, "", "", meta.PatchRender, width, ModeOngoing, meta, nil)
	} else {
		lines = renderTextBlockWithInlineMeta(role, text, "", width, ModeOngoing, meta)
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

func normalizeToolMeta(toolName string, in *clientui.ToolCallMeta) toolMeta {
	adapted := transcript.ToolCallMeta{ToolName: strings.TrimSpace(toolName)}
	if in != nil {
		adapted = transcript.ToolCallMeta{
			ToolName:               firstNonEmpty(in.ToolName, toolName),
			Presentation:           transcript.ToolPresentationKind(in.Presentation),
			RenderBehavior:         transcript.ToolCallRenderBehavior(in.RenderBehavior),
			IsShell:                in.IsShell,
			UserInitiated:          in.UserInitiated,
			Command:                in.Command,
			CompactText:            in.CompactText,
			InlineMeta:             in.InlineMeta,
			TimeoutLabel:           in.TimeoutLabel,
			PatchSummary:           in.PatchSummary,
			PatchDetail:            in.PatchDetail,
			PatchRender:            in.PatchRender,
			Question:               in.Question,
			Suggestions:            append([]string(nil), in.Suggestions...),
			RecommendedOptionIndex: in.RecommendedOptionIndex,
			OmitSuccessfulResult:   in.OmitSuccessfulResult,
			RawOutputRequested:     in.RawOutputRequested,
			OutputTruncated:        in.OutputTruncated,
			MovedToBackground:      in.MovedToBackground,
		}
		if in.RenderHint != nil {
			adapted.RenderHint = &transcript.ToolRenderHint{
				Kind:         transcript.ToolRenderKind(in.RenderHint.Kind),
				Path:         in.RenderHint.Path,
				ResultOnly:   in.RenderHint.ResultOnly,
				ShellDialect: transcript.ToolShellDialect(in.RenderHint.ShellDialect),
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

type toolDisplay struct {
	Text       string
	InlineMeta string
	kind       toolDisplayKind
}

func toolDisplayText(row clientui.TranscriptToolRow, meta toolMeta, mode Mode) toolDisplay {
	if mode == ModeOngoing || mode == ModeOngoingCollapsed || mode == ModeDetailCollapsed {
		text := compactToolText(meta, firstNonEmpty(row.CondensedText, row.Text))
		resultSummary := row.ResultSummary
		if meta.IsError && (mode == ModeOngoing || mode == ModeOngoingCollapsed) {
			resultSummary = ""
		}
		return toolDisplay{Text: text, InlineMeta: firstNonEmpty(resultSummary, meta.InlineMeta)}
	}
	if !meta.IsError &&
		meta.RenderHint != nil &&
		meta.RenderHint.Kind == transcript.ToolRenderKindSource &&
		meta.RenderHint.ResultOnly {
		return toolDisplay{Text: row.Text, kind: toolDisplaySourceResult}
	}
	return toolDisplay{Text: detailedToolText(meta, row.Text)}
}

func compactToolText(meta toolMeta, fallback string) string {
	return transcript.CompactToolCallText(&meta.ToolCallMeta, fallback)
}

func detailedToolText(meta toolMeta, fallback string) string {
	return transcript.DetailedToolCallText(&meta.ToolCallMeta, fallback)
}

func detailedToolResultText(row clientui.TranscriptToolRow) string {
	output := strings.TrimSpace(safeTranscriptText(row.Text))
	summary := strings.TrimSpace(safeTranscriptText(row.ResultSummary))
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
	text string,
	inlineMeta string,
	result string,
	rendered *patchformat.RenderedPatch,
	width int,
	mode Mode,
	meta toolMeta,
	syntax *syntaxProjector,
) []Line {
	if mode == ModeDetailExpanded {
		if lines, ok := renderStructuredPatch(rendered, contentWidth(role, width), syntax); ok {
			if result != "" {
				lines = append(lines, Line{Spans: []Span{contentRoleSpan("", role, ModeDetailExpanded)}})
				lines = append(lines, detailedToolOutputLines(role, result, contentWidth(role, width))...)
			}
			return attachPrefixWithMeta(role, lines, width, false, mode, meta)
		}
	}
	if rendered == nil || len(rendered.SummaryLines) == 0 || mode == ModeDetailExpanded {
		if mode == ModeDetailExpanded {
			return renderDetailedToolTextBlock(role, text, result, width, meta)
		}
		return renderTextBlockWithInlineMeta(role, text, inlineMeta, width, mode, meta)
	}
	lines := make([]Line, 0, len(rendered.Files))
	for _, file := range rendered.Files {
		path := firstNonEmpty(file.RelPath, file.AbsPath)
		if path == "" {
			continue
		}
		var spans []Span
		spans = append(spans, roleSpan(path, role))
		if file.Removed > 0 {
			spans = append(spans, roleSpan(" ", role))
			spans = append(spans, SemanticSpan(fmt.Sprintf("-%d", file.Removed), StyleRoleToolError))
		}
		if file.Added > 0 {
			spans = append(spans, roleSpan(" ", role))
			spans = append(spans, SemanticSpan(fmt.Sprintf("+%d", file.Added), StyleRoleToolSuccess))
		}
		lines = append(lines, Line{Spans: spans})
	}
	if len(lines) == 0 {
		lines = []Line{{Spans: []Span{roleSpan(text, role)}}}
	}
	return attachPrefixWithFirstLineMeta(role, lines, width, false, inlineMeta, mode, meta)
}

const (
	patchSyntaxBatchMaxLines = 400
	patchSyntaxBatchMaxBytes = 64 * 1024
)

type patchSourceKind uint8

const (
	patchSourceContext patchSourceKind = iota
	patchSourceAdded
	patchSourceRemoved
)

type patchSourceLine struct {
	kind patchSourceKind
	text string
}

func renderStructuredPatch(
	rendered *patchformat.RenderedPatch,
	width int,
	syntax *syntaxProjector,
) ([]Line, bool) {
	if !hasStructuredPatchDetail(rendered) {
		return nil, false
	}
	if syntax == nil {
		panic("render structured detail patch without syntax projector")
	}
	width = max(1, width)
	out := make([]Line, 0, len(rendered.DetailLines))
	var currentLexer chroma.Lexer
	var inferredLexer chroma.Lexer
	inferredLexerResolved := false
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
		lexer := currentLexer
		if lexer == nil {
			if !inferredLexerResolved {
				inferredLexer = lexers.Analyse(source)
				inferredLexerResolved = true
			}
			lexer = inferredLexer
		}
		highlighted := syntax.highlight(lexer, source)
		for index, sourceLine := range pending {
			out = append(out, wrapPatchSourceLine(
				sourceLine.kind,
				highlighted[index],
				width,
			)...)
		}
		pending = pending[:0]
		pendingBytes = 0
	}
	appendPending := func(kind patchSourceKind, text string) {
		pending = append(pending, patchSourceLine{kind: kind, text: text})
		pendingBytes += len(text) + 1
		if len(pending) >= patchSyntaxBatchMaxLines || pendingBytes >= patchSyntaxBatchMaxBytes {
			flushPending()
		}
	}
	for _, renderedLine := range rendered.DetailLines {
		renderedLine.Text = safeTranscriptText(renderedLine.Text)
		renderedLine.Path = safeTranscriptText(renderedLine.Path)
		if renderedLine.Kind == patchformat.RenderedLineKindFile {
			flushPending()
			currentLexer = lexers.Match(strings.TrimSpace(renderedLine.Path))
			inferredLexer = nil
			inferredLexerResolved = false
			out = append(out, wrapPatchMetadataLine(renderedLine.Text, width)...)
			continue
		}
		kind, text, source := classifyPatchDetailLine(renderedLine)
		if source {
			appendPending(kind, text)
			continue
		}
		flushPending()
		out = append(out, wrapPatchMetadataLine(renderedLine.Text, width)...)
	}
	flushPending()
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

func hasStructuredPatchDetail(rendered *patchformat.RenderedPatch) bool {
	if rendered == nil || len(rendered.DetailLines) == 0 {
		return false
	}
	for _, line := range rendered.DetailLines {
		if line.Kind == patchformat.RenderedLineKindFile {
			return true
		}
	}
	return false
}

func classifyPatchDetailLine(line patchformat.RenderedLine) (patchSourceKind, string, bool) {
	if line.Kind != patchformat.RenderedLineKindDiff {
		return 0, "", false
	}
	if line.Text == "" {
		return patchSourceContext, "", true
	}
	marker, markerWidth := utf8.DecodeRuneInString(line.Text)
	switch marker {
	case '+':
		return patchSourceAdded, line.Text[markerWidth:], true
	case '-':
		return patchSourceRemoved, line.Text[markerWidth:], true
	case ' ':
		return patchSourceContext, line.Text[markerWidth:], true
	default:
		return 0, "", false
	}
}

func wrapPatchMetadataLine(text string, width int) []Line {
	return wrapStyledLine([]Span{SemanticSpan(text, StyleRoleToolPatch)}, width)
}

func wrapPatchSourceLine(kind patchSourceKind, source []Span, width int) []Line {
	marker := " "
	markerRole := StyleRoleToolPatch
	background := LineBackgroundDefault
	switch kind {
	case patchSourceAdded:
		marker = "+"
		markerRole = StyleRoleToolSuccess
		background = LineBackgroundDiffAdded
	case patchSourceRemoved:
		marker = "-"
		markerRole = StyleRoleToolError
		background = LineBackgroundDiffRemoved
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
	return transcript.IsPatchFamilyToolName(meta.ToolName) ||
		meta.PatchRender != nil ||
		meta.HasPatchSummary() ||
		meta.HasPatchDetail()
}

func isWebSearchTool(toolName string) bool {
	return strings.TrimSpace(toolName) == "web_search"
}
