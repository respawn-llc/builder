package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"

	sharedtheme "core/shared/theme"

	"charm.land/glamour/v2"
	glamouransi "charm.land/glamour/v2/ansi"
	glamourstyles "charm.land/glamour/v2/styles"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/cellbuf"
)

func RenderAskQuestionMarkdownLines(question string, themeName string, width int) []string {
	width = max(1, width)
	renderer, err := glamour.NewTermRenderer(
		glamour.WithWordWrap(width),
		glamour.WithStyles(askQuestionMarkdownStyle(themeName)),
	)
	if err != nil {
		return resolveAskQuestionMarkdownLines(question, width, askQuestionMarkdownRenderOutcome{err: err})
	}
	rendered, err := renderer.Render(question)
	return resolveAskQuestionMarkdownLines(question, width, askQuestionMarkdownRenderOutcome{rendered: rendered, err: err})
}

type askQuestionMarkdownRenderOutcome struct {
	rendered string
	err      error
}

func resolveAskQuestionMarkdownLines(question string, width int, outcome askQuestionMarkdownRenderOutcome) []string {
	if outcome.err == nil {
		lines := trimAskQuestionMarkdownEdgeLines(
			strings.Split(hardWrapAskQuestionMarkdown(outcome.rendered, width), "\n"),
		)
		if len(lines) > 0 {
			return lines
		}
	}
	return renderPlainAskQuestionMarkdownSource(question, width)
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
		out = append(out, wrapAskQuestionMarkdownLine(line, width)...)
	}
	return strings.Join(out, "\n")
}

type askQuestionHyperlink = cellbuf.Link

func wrapAskQuestionMarkdownLine(line string, width int) []string {
	if width < 1 {
		width = 1
	}
	parser := xansi.GetParser()
	defer xansi.PutParser(parser)

	var (
		active *askQuestionHyperlink
		out    []string
		row    strings.Builder
		state  byte
		used   int
		input  = line
	)
	for len(input) > 0 {
		sequence, sequenceWidth, consumed, nextState := xansi.GraphemeWidth.DecodeSequenceInString(input, state, parser)
		if consumed <= 0 {
			break
		}
		state = nextState
		input = input[consumed:]

		if sequenceWidth == 0 {
			row.WriteString(sequence)
			if hyperlink, isHyperlink := askQuestionHyperlinkFromParser(parser); isHyperlink {
				if hyperlink.Empty() {
					active = nil
				} else {
					active = &hyperlink
				}
			}
			continue
		}
		if used > 0 && used+sequenceWidth > width {
			if active != nil {
				row.WriteString(xansi.ResetHyperlink())
			}
			out = append(out, row.String())
			row.Reset()
			used = 0
			if active != nil {
				row.WriteString(xansi.SetHyperlink(active.URL, active.Params))
			}
		}
		row.WriteString(sequence)
		used += sequenceWidth
	}
	if active != nil {
		row.WriteString(xansi.ResetHyperlink())
	}
	out = append(out, row.String())
	return out
}

func askQuestionHyperlinkFromParser(parser *xansi.Parser) (askQuestionHyperlink, bool) {
	if parser.Command() != 8 {
		return askQuestionHyperlink{}, false
	}
	var hyperlink askQuestionHyperlink
	cellbuf.ReadLink(parser.Data(), &hyperlink)
	return hyperlink, true
}

func trimAskQuestionMarkdownEdgeLines(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(xansi.Strip(lines[0])) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(xansi.Strip(lines[len(lines)-1])) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func renderPlainAskQuestionMarkdownSource(question string, width int) []string {
	width = max(1, width)
	sourceLines := strings.Split(plainAskQuestionMarkdownSource(question), "\n")
	out := make([]string, 0, len(sourceLines))
	for _, sourceLine := range sourceLines {
		out = append(out, wrapAskQuestionMarkdownLine(sourceLine, width)...)
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

func plainAskQuestionMarkdownSource(question string) string {
	source := xansi.Strip(question)
	var out strings.Builder
	for len(source) > 0 {
		r, size := utf8.DecodeRuneInString(source)
		if r == '\n' {
			out.WriteByte('\n')
			source = source[size:]
			continue
		}
		if unicode.IsControl(r) || (r == utf8.RuneError && size == 1) {
			source = source[size:]
			continue
		}
		grapheme, _ := xansi.FirstGraphemeCluster(source, xansi.GraphemeWidth)
		if len(grapheme) == 0 {
			break
		}
		if isPlainAskQuestionGrapheme(grapheme) {
			out.WriteString(grapheme)
		}
		source = source[len(grapheme):]
	}
	return out.String()
}

func isPlainAskQuestionGrapheme(grapheme string) bool {
	hasPrintableRune := false
	for _, r := range grapheme {
		if unicode.IsControl(r) {
			return false
		}
		if unicode.IsPrint(r) {
			hasPrintableRune = true
		}
	}
	return hasPrintableRune
}
