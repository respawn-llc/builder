package transcriptrender

import (
	"core/shared/transcript"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

func shellSyntaxSpans(line string, meta toolMeta) []Span {
	fallback := SemanticStyle(StyleRoleToolShell, SpanAttributeFaint)
	lexer := shellSyntaxLexer(meta)
	projected := projectSyntaxSpans(lexer, line, fallback, func(tokenType chroma.TokenType) SpanStyle {
		return SemanticStyle(shellSyntaxRole(tokenType), SpanAttributeFaint)
	})
	return projected[0]
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

func shellSyntaxRole(tokenType chroma.TokenType) StyleRole {
	switch {
	case tokenType == chroma.Error:
		return StyleRoleToolShellError
	case tokenType.InCategory(chroma.Keyword),
		tokenType.InSubCategory(chroma.LiteralString),
		tokenType.InSubCategory(chroma.LiteralNumber):
		return StyleRoleToolShellPrimary
	case tokenType.InSubCategory(chroma.NameBuiltin),
		tokenType.InSubCategory(chroma.NameVariable),
		tokenType.InSubCategory(chroma.NameFunction):
		return StyleRoleToolShellSecondary
	case tokenType.InCategory(chroma.Comment):
		return StyleRoleToolShellWarning
	default:
		return StyleRoleToolShell
	}
}
