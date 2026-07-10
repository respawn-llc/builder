package tui

import (
	"strings"

	"core/cli/tui/transcriptrender"
	sharedtheme "core/shared/theme"

	"github.com/charmbracelet/glamour"
	glamouransi "github.com/charmbracelet/glamour/ansi"
	glamourstyles "github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
)

func RenderAskQuestionMarkdownLines(question string, themeName string, width int) []string {
	width = max(1, width)
	renderer, err := glamour.NewTermRenderer(
		glamour.WithWordWrap(width),
		glamour.WithStyles(askQuestionMarkdownStyle(themeName)),
	)
	if err == nil {
		rendered, renderErr := renderer.Render(question)
		if renderErr == nil {
			lines := trimAskQuestionMarkdownEdgeLines(
				strings.Split(hardWrapAskQuestionMarkdown(rendered, width), "\n"),
			)
			if len(lines) > 0 {
				return lines
			}
		}
	}
	return renderAskQuestionMarkdownFallback(question, themeName, width)
}

func askQuestionMarkdownStyle(themeName string) glamouransi.StyleConfig {
	resolvedTheme := sharedtheme.Resolve(themeName)
	config := glamourstyles.DarkStyleConfig
	if resolvedTheme == "light" {
		config = glamourstyles.LightStyleConfig
	}
	if config.CodeBlock.Chroma != nil {
		chromaConfig := *config.CodeBlock.Chroma
		config.CodeBlock.Chroma = &chromaConfig
	}

	foreground := sharedtheme.ResolvePalette(resolvedTheme).Transcript.Foreground.TrueColor
	zero := uint(0)
	config.Document.Margin = &zero
	config.Document.BlockPrefix = ""
	config.Document.BlockSuffix = ""
	config.Document.Color = &foreground
	config.Text.Color = &foreground
	config.CodeBlock.StylePrimitive.Color = &foreground
	if config.CodeBlock.Chroma != nil {
		config.CodeBlock.Chroma.Text.Color = &foreground
		config.CodeBlock.Chroma.Name.Color = &foreground
	}
	clearAskQuestionMarkdownBackgrounds(&config)
	return config
}

func clearAskQuestionMarkdownBackgrounds(config *glamouransi.StyleConfig) {
	if config == nil {
		return
	}
	primitives := []*glamouransi.StylePrimitive{
		&config.Document.StylePrimitive,
		&config.BlockQuote.StylePrimitive,
		&config.Paragraph.StylePrimitive,
		&config.List.StyleBlock.StylePrimitive,
		&config.Heading.StylePrimitive,
		&config.H1.StylePrimitive,
		&config.H2.StylePrimitive,
		&config.H3.StylePrimitive,
		&config.H4.StylePrimitive,
		&config.H5.StylePrimitive,
		&config.H6.StylePrimitive,
		&config.Text,
		&config.Strikethrough,
		&config.Emph,
		&config.Strong,
		&config.HorizontalRule,
		&config.Item,
		&config.Enumeration,
		&config.Task.StylePrimitive,
		&config.Link,
		&config.LinkText,
		&config.Image,
		&config.ImageText,
		&config.Code.StylePrimitive,
		&config.CodeBlock.StyleBlock.StylePrimitive,
		&config.Table.StyleBlock.StylePrimitive,
		&config.DefinitionList.StylePrimitive,
		&config.DefinitionTerm,
		&config.DefinitionDescription,
		&config.HTMLBlock.StylePrimitive,
		&config.HTMLSpan.StylePrimitive,
	}
	if config.CodeBlock.Chroma != nil {
		primitives = append(primitives,
			&config.CodeBlock.Chroma.Text,
			&config.CodeBlock.Chroma.Error,
			&config.CodeBlock.Chroma.Comment,
			&config.CodeBlock.Chroma.CommentPreproc,
			&config.CodeBlock.Chroma.Keyword,
			&config.CodeBlock.Chroma.KeywordReserved,
			&config.CodeBlock.Chroma.KeywordNamespace,
			&config.CodeBlock.Chroma.KeywordType,
			&config.CodeBlock.Chroma.Operator,
			&config.CodeBlock.Chroma.Punctuation,
			&config.CodeBlock.Chroma.Name,
			&config.CodeBlock.Chroma.NameBuiltin,
			&config.CodeBlock.Chroma.NameTag,
			&config.CodeBlock.Chroma.NameAttribute,
			&config.CodeBlock.Chroma.NameClass,
			&config.CodeBlock.Chroma.NameConstant,
			&config.CodeBlock.Chroma.NameDecorator,
			&config.CodeBlock.Chroma.NameException,
			&config.CodeBlock.Chroma.NameFunction,
			&config.CodeBlock.Chroma.NameOther,
			&config.CodeBlock.Chroma.Literal,
			&config.CodeBlock.Chroma.LiteralNumber,
			&config.CodeBlock.Chroma.LiteralDate,
			&config.CodeBlock.Chroma.LiteralString,
			&config.CodeBlock.Chroma.LiteralStringEscape,
			&config.CodeBlock.Chroma.GenericDeleted,
			&config.CodeBlock.Chroma.GenericEmph,
			&config.CodeBlock.Chroma.GenericInserted,
			&config.CodeBlock.Chroma.GenericStrong,
			&config.CodeBlock.Chroma.GenericSubheading,
			&config.CodeBlock.Chroma.Background,
		)
	}
	for _, primitive := range primitives {
		primitive.BackgroundColor = nil
	}
}

func hardWrapAskQuestionMarkdown(rendered string, width int) string {
	rendered = strings.TrimRight(rendered, "\n")
	if rendered == "" {
		return ""
	}
	lines := strings.Split(rendered, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if lipgloss.Width(line) <= width {
			out = append(out, line)
			continue
		}
		out = append(out, strings.Split(xansi.Hardwrap(line, width, true), "\n")...)
	}
	return strings.Join(out, "\n")
}

func trimAskQuestionMarkdownEdgeLines(lines []string) []string {
	for len(lines) > 0 && lipgloss.Width(lines[0]) == 0 {
		lines = lines[1:]
	}
	for len(lines) > 0 && lipgloss.Width(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func renderAskQuestionMarkdownFallback(question string, themeName string, width int) []string {
	lines := transcriptrender.RenderMarkdownLines(
		transcriptrender.StyleRoleToolQuestion,
		question,
		width,
	)
	if len(lines) == 0 {
		return []string{""}
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		var rendered strings.Builder
		if line.LeadingSymbol != nil {
			rendered.WriteString(renderTranscriptSpan(*line.LeadingSymbol, themeName))
		}
		for _, span := range line.Spans {
			rendered.WriteString(renderTranscriptSpan(span, themeName))
		}
		out = append(out, rendered.String())
	}
	return out
}

func renderTranscriptSpan(span transcriptrender.Span, themeName string) string {
	if span.Text == "" {
		return ""
	}
	return transcriptSpanStyle(span, themeName).Render(span.Text)
}
