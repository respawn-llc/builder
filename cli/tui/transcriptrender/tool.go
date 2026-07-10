package transcriptrender

import (
	"fmt"
	"strings"

	"core/shared/clientui"
	"core/shared/transcript"
	patchformat "core/shared/transcript/patchformat"
)

func RenderToolRow(row clientui.TranscriptToolRow, width int, mode Mode) []Line {
	meta := normalizeToolMeta(row.ToolName, row.ToolPresentation)
	meta.IsError = row.IsError
	role := toolRole(meta)
	display := toolDisplayText(row, meta, mode)
	if isPatchTool(meta) {
		return renderPatchTool(role, display.Text, display.InlineMeta, meta.PatchRender, width, mode, meta)
	}
	return renderTextBlockWithInlineMeta(role, display.Text, display.InlineMeta, width, mode, meta)
}

func RenderPendingTool(tool clientui.TranscriptToolStart, width int, spinner string) Line {
	meta := normalizeToolMeta(tool.ToolName, tool.ToolPresentation)
	role := toolRole(meta)
	text := compactToolText(meta, tool.ToolName)
	var lines []Line
	if isPatchTool(meta) {
		lines = renderPatchTool(role, text, "", meta.PatchRender, width, ModeOngoing, meta)
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
	return replaceLeadingRoleSymbol(line, role, spinner)
}

type toolMeta struct {
	transcript.ToolCallMeta
	IsError            bool
	BackgroundExitCode *int
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

type toolDisplay struct {
	Text       string
	InlineMeta string
}

func toolDisplayText(row clientui.TranscriptToolRow, meta toolMeta, mode Mode) toolDisplay {
	if mode == ModeOngoing || mode == ModeOngoingCollapsed || mode == ModeDetailCollapsed {
		text := compactToolText(meta, firstNonEmpty(row.CondensedText, row.Text))
		return toolDisplay{Text: text, InlineMeta: firstNonEmpty(row.ResultSummary, meta.InlineMeta)}
	}
	text := detailedToolText(meta, row.Text)
	if summary := strings.TrimSpace(row.ResultSummary); summary != "" {
		text = text + "\n" + summary
	}
	return toolDisplay{Text: text}
}

func compactToolText(meta toolMeta, fallback string) string {
	return transcript.CompactToolCallText(&meta.ToolCallMeta, fallback)
}

func detailedToolText(meta toolMeta, fallback string) string {
	return transcript.DetailedToolCallText(&meta.ToolCallMeta, fallback)
}

func renderPatchTool(role StyleRole, text string, inlineMeta string, rendered *patchformat.RenderedPatch, width int, mode Mode, meta toolMeta) []Line {
	if rendered == nil || len(rendered.SummaryLines) == 0 || mode == ModeDetailExpanded {
		return renderTextBlockWithInlineMeta(role, text, inlineMeta, width, mode, meta)
	}
	lines := make([]Line, 0, len(rendered.Files))
	for _, file := range rendered.Files {
		path := firstNonEmpty(file.RelPath, file.AbsPath)
		if path == "" {
			continue
		}
		var spans []Span
		spans = append(spans, Span{Text: path, Role: role, Faint: roleDefaultFaint(role)})
		if file.Removed > 0 {
			spans = append(spans, Span{Text: " ", Role: role, Faint: roleDefaultFaint(role)})
			spans = append(spans, Span{Text: fmt.Sprintf("-%d", file.Removed), Role: StyleRoleToolError})
		}
		if file.Added > 0 {
			spans = append(spans, Span{Text: " ", Role: role, Faint: roleDefaultFaint(role)})
			spans = append(spans, Span{Text: fmt.Sprintf("+%d", file.Added), Role: StyleRoleToolSuccess})
		}
		lines = append(lines, Line{Spans: spans})
	}
	if len(lines) == 0 {
		lines = []Line{{Spans: []Span{{Text: text, Role: role, Faint: roleDefaultFaint(role)}}}}
	}
	return attachPrefixWithFirstLineMeta(role, lines, width, false, inlineMeta, mode, meta)
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
