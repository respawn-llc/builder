package transcriptrender

import (
	"strings"

	"core/shared/theme"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

const (
	lightSyntaxStyle = "catppuccin-latte"
	darkSyntaxStyle  = "onedark"
)

type syntaxProjector struct {
	style           *chroma.Style
	baseForeground  chroma.Colour
	themeForeground chroma.Colour
}

func newSyntaxProjector(themeName string) syntaxProjector {
	resolvedTheme := theme.Resolve(themeName)
	style := chromaStyleForTheme(resolvedTheme)
	return syntaxProjector{
		style:           style,
		baseForeground:  style.Get(chroma.Text).Colour,
		themeForeground: chroma.MustParseColour(theme.ResolvePalette(resolvedTheme).Transcript.Foreground.TrueColor),
	}
}

func chromaStyleForTheme(themeName string) *chroma.Style {
	styleName := darkSyntaxStyle
	if themeName == theme.Light {
		styleName = lightSyntaxStyle
	}
	style := styles.Get(styleName)
	if style == nil {
		panic("required Chroma style is not registered: " + styleName)
	}
	return style
}

func (p syntaxProjector) explicitStyle(tokenType chroma.TokenType) SpanStyle {
	entry := p.style.Get(tokenType)
	foreground := entry.Colour
	if !foreground.IsSet() || foreground == p.baseForeground {
		foreground = p.themeForeground
	}
	attributes := make([]SpanAttribute, 0, 3)
	if entry.Bold == chroma.Yes {
		attributes = append(attributes, SpanAttributeBold)
	}
	if entry.Italic == chroma.Yes {
		attributes = append(attributes, SpanAttributeItalic)
	}
	if entry.Underline == chroma.Yes {
		attributes = append(attributes, SpanAttributeUnderline)
	}
	return ExplicitRGBStyle(RGBColor{
		Red:   foreground.Red(),
		Green: foreground.Green(),
		Blue:  foreground.Blue(),
	}, attributes...)
}

func (p syntaxProjector) highlight(
	lexer chroma.Lexer,
	source string,
	attributes ...SpanAttribute,
) [][]Span {
	fallback := p.explicitStyle(chroma.Text)
	fallback.Attributes |= combineSpanAttributes(attributes)
	return projectSyntaxSpans(lexer, source, fallback, func(tokenType chroma.TokenType) SpanStyle {
		style := p.explicitStyle(tokenType)
		style.Attributes |= combineSpanAttributes(attributes)
		return style
	})
}

func sourceSyntaxLexer(path string, source string) chroma.Lexer {
	if lexer := lexers.Match(strings.TrimSpace(path)); lexer != nil {
		return lexer
	}
	return lexers.Analyse(source)
}

type syntaxStyleResolver func(chroma.TokenType) SpanStyle

func projectSyntaxSpans(
	lexer chroma.Lexer,
	source string,
	fallback SpanStyle,
	resolve syntaxStyleResolver,
) [][]Span {
	sourceLines := strings.Split(source, "\n")
	if lexer == nil || source == "" {
		return fallbackSyntaxSpans(sourceLines, fallback)
	}
	iterator, err := chroma.Coalesce(lexer).Tokenise(nil, source)
	if err != nil {
		return fallbackSyntaxSpans(sourceLines, fallback)
	}
	lines := make([][]Span, 1, len(sourceLines))
	for token := iterator(); token != chroma.EOF; token = iterator() {
		fragments := strings.Split(strings.ReplaceAll(token.Value, "\r\n", "\n"), "\n")
		for index, fragment := range fragments {
			appendStyledText(&lines[len(lines)-1], fragment, resolve(token.Type))
			if index < len(fragments)-1 {
				lines = append(lines, nil)
			}
		}
	}
	if len(lines) != len(sourceLines) {
		return fallbackSyntaxSpans(sourceLines, fallback)
	}
	return lines
}

func fallbackSyntaxSpans(lines []string, style SpanStyle) [][]Span {
	out := make([][]Span, 0, len(lines))
	for _, line := range lines {
		out = append(out, []Span{{Text: line, Style: style}})
	}
	return out
}

func appendStyledText(spans *[]Span, text string, style SpanStyle) {
	if text == "" {
		return
	}
	last := len(*spans) - 1
	if last >= 0 && (*spans)[last].Style == style {
		(*spans)[last].Text += text
		return
	}
	*spans = append(*spans, Span{Text: text, Style: style})
}
