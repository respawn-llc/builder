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
	lines := renderTextBlockWithInlineMeta(role, text, "", width, ModeOngoing, meta)
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
	ToolName               string
	IsError                bool
	Presentation           clientui.ToolPresentationKind
	RenderBehavior         clientui.ToolCallRenderBehavior
	ShellDialect           clientui.ToolShellDialect
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
	BackgroundExitCode     *int
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
		if in.RenderHint != nil {
			meta.ShellDialect = in.RenderHint.ShellDialect
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

func toolRole(meta toolMeta) StyleRole {
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
	return transcript.CompactToolCallText(transcriptToolMeta(meta), fallback)
}

func detailedToolText(meta toolMeta, fallback string) string {
	return transcript.DetailedToolCallText(transcriptToolMeta(meta), fallback)
}

func transcriptToolMeta(meta toolMeta) *transcript.ToolCallMeta {
	return &transcript.ToolCallMeta{
		ToolName:               meta.ToolName,
		Presentation:           transcript.ToolPresentationKind(meta.Presentation),
		RenderBehavior:         transcript.ToolCallRenderBehavior(meta.RenderBehavior),
		IsShell:                meta.IsShell,
		UserInitiated:          meta.UserInitiated,
		Command:                meta.Command,
		CompactText:            meta.CompactText,
		InlineMeta:             meta.InlineMeta,
		TimeoutLabel:           meta.TimeoutLabel,
		PatchSummary:           meta.PatchSummary,
		PatchDetail:            meta.PatchDetail,
		PatchRender:            meta.PatchRender,
		Question:               meta.Question,
		Suggestions:            append([]string(nil), meta.Suggestions...),
		RecommendedOptionIndex: meta.RecommendedOptionIndex,
		RawOutputRequested:     meta.RawOutputRequested,
		OutputTruncated:        meta.OutputTruncated,
	}
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
		meta.PatchSummary != "" ||
		meta.PatchDetail != ""
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
