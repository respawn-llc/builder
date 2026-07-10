package transcriptrender

import (
	"core/shared/transcript"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

func shellInputSpans(line string, meta toolMeta) []Span {
	if meta.RenderHint != nil && meta.RenderHint.Kind == transcript.ToolRenderKindPlain {
		return []Span{SemanticSpan(line, StyleRoleToolShell, SpanAttributeFaint)}
	}
	if meta.syntax == nil {
		panic("render highlighted shell input without syntax projector")
	}
	return meta.syntax.highlight(shellSyntaxLexer(meta), line, SpanAttributeFaint)[0]
}

func sourceResultLines(source string, width int, meta toolMeta) []Line {
	if meta.syntax == nil || meta.RenderHint == nil {
		panic("render highlighted source result without syntax metadata")
	}
	highlighted := meta.syntax.highlight(
		sourceSyntaxLexer(meta.RenderHint.Path, source),
		source,
		SpanAttributeFaint,
	)
	lines := make([]Line, 0, len(highlighted))
	for _, spans := range highlighted {
		lines = append(lines, wrapStyledLine(spans, width)...)
	}
	return lines
}

func shellSyntaxLexer(meta toolMeta) chroma.Lexer {
	dialect := transcript.ToolShellDialectPosix
	if meta.RenderHint != nil && meta.RenderHint.ShellDialect != "" {
		dialect = meta.RenderHint.ShellDialect
	}
	switch dialect {
	case transcript.ToolShellDialectPowerShell:
		return firstAvailableLexer("powershell", "posh", "shell")
	case transcript.ToolShellDialectWindowsCommand:
		return firstAvailableLexer("batch", "bat", "shell")
	case transcript.ToolShellDialectPosix:
		return firstAvailableLexer("bash", "shell")
	default:
		return firstAvailableLexer("bash", "shell")
	}
}

func firstAvailableLexer(names ...string) chroma.Lexer {
	for _, name := range names {
		lexer := lexers.Get(name)
		if lexer != nil {
			return lexer
		}
	}
	return nil
}
