package transcriptrender

import (
	"fmt"
	"strings"

	"charm.land/glamour/v2"
	glamourstyles "charm.land/glamour/v2/styles"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extensionast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// RenderMarkdownLines projects Markdown source into transcript-owned semantic
// spans without adding row symbols or continuation guides.
func RenderMarkdownLines(role StyleRole, sourceText string, width int) []Line {
	return renderMarkdown(role, sourceText, width, true, true)
}

// RenderMarkdownStableLines leaves ordinary Markdown wrapping to the terminal.
// Constructs with cross-row horizontal layout, such as tables, are formatted
// to the supplied width by the Markdown renderer.
func RenderMarkdownStableLines(role StyleRole, sourceText string, width int) []Line {
	return renderMarkdown(role, sourceText, width, false, false)
}

type renderedMarkdownBlock struct {
	lines          []Line
	widthFormatted bool
}

func renderMarkdown(role StyleRole, sourceText string, width int, preserveSoftBreaks bool, wrapFlow bool) []Line {
	source := []byte(sourceText)
	document := goldmark.New(goldmark.WithExtensions(extension.Table)).Parser().Parse(text.NewReader(source))
	if document.FirstChild() == nil {
		return []Line{{Spans: []Span{SemanticSpan("", role)}}}
	}
	var out []Line
	for node := document.FirstChild(); node != nil; node = node.NextSibling() {
		block := renderMarkdownBlock(node, source, role, width, preserveSoftBreaks)
		if len(block.lines) == 0 {
			continue
		}
		if len(out) > 0 {
			out = append(out, Line{Spans: []Span{SemanticSpan("", role)}})
		}
		if wrapFlow && !block.widthFormatted {
			for _, line := range block.lines {
				out = append(out, wrapStyledLine(line.Spans, width)...)
			}
			continue
		}
		out = append(out, block.lines...)
	}
	if len(out) == 0 {
		return []Line{{Spans: []Span{SemanticSpan("", role)}}}
	}
	return out
}

func renderMarkdownBlock(node ast.Node, source []byte, role StyleRole, width int, preserveSoftBreaks bool) renderedMarkdownBlock {
	if markdownNodeContainsTable(node) {
		return renderedMarkdownBlock{
			lines:          renderMarkdownTable(markdownTopLevelNodeSource(node, source), role, width),
			widthFormatted: true,
		}
	}
	return renderedMarkdownBlock{
		lines: markdownBlockLines(node, source, role, preserveSoftBreaks),
	}
}

func markdownNodeContainsTable(node ast.Node) bool {
	found := false
	err := ast.Walk(node, func(current ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering && current.Kind() == extensionast.KindTable {
			found = true
			return ast.WalkStop, nil
		}
		return ast.WalkContinue, nil
	})
	if err != nil {
		panic(fmt.Sprintf("walk markdown table node: %v", err))
	}
	return found
}

func markdownTopLevelNodeSource(node ast.Node, source []byte) string {
	start := markdownNodeSourceStart(node, source)
	end := len(source)
	if next := node.NextSibling(); next != nil {
		if nextStart := markdownNodeSourceStart(next, source); nextStart > start {
			end = nextStart
		}
	}
	return strings.Trim(string(source[start:end]), "\r\n")
}

func markdownNodeSourceStart(node ast.Node, source []byte) int {
	start := len(source)
	err := ast.Walk(node, func(current ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if current.Type() == ast.TypeBlock {
			if lines := current.Lines(); lines != nil {
				for index := 0; index < lines.Len(); index++ {
					start = min(start, lines.At(index).Start)
				}
			}
		}
		if textNode, ok := current.(*ast.Text); ok {
			start = min(start, textNode.Segment.Start)
		}
		return ast.WalkContinue, nil
	})
	if err != nil {
		panic(fmt.Sprintf("find markdown source start: %v", err))
	}
	if start == len(source) {
		start = max(0, node.Pos())
	}
	for start > 0 && source[start-1] != '\n' {
		start--
	}
	return start
}

func renderMarkdownTable(source string, role StyleRole, width int) []Line {
	style := glamourstyles.ASCIIStyleConfig
	zero := uint(0)
	style.Document.Margin = &zero
	style.Document.BlockPrefix = ""
	style.Document.BlockSuffix = ""
	renderer, err := glamour.NewTermRenderer(
		glamour.WithWordWrap(max(1, width)),
		glamour.WithTableWrap(true),
		glamour.WithInlineTableLinks(true),
		glamour.WithStyles(style),
	)
	if err != nil {
		panic(fmt.Sprintf("create markdown table renderer: %v", err))
	}
	rendered, err := renderer.Render(source)
	if err != nil {
		panic(fmt.Sprintf("render markdown table: %v", err))
	}
	plainLines := strings.Split(xansi.Strip(rendered), "\n")
	for len(plainLines) > 0 && strings.TrimSpace(plainLines[0]) == "" {
		plainLines = plainLines[1:]
	}
	for len(plainLines) > 0 && strings.TrimSpace(plainLines[len(plainLines)-1]) == "" {
		plainLines = plainLines[:len(plainLines)-1]
	}
	lines := make([]Line, 0, len(plainLines))
	for _, line := range plainLines {
		lines = append(lines, Line{Spans: []Span{SemanticSpan(line, role)}})
	}
	return lines
}

func markdownBlockLines(node ast.Node, source []byte, role StyleRole, preserveSoftBreaks bool) []Line {
	switch typed := node.(type) {
	case *ast.Heading:
		return markdownInlineLines(typed, source, role, markdownInlineStyle{Bold: true}, preserveSoftBreaks)
	case *ast.Paragraph:
		return restoreMarkdownParagraphIndent(node, source, markdownInlineLines(node, source, role, markdownInlineStyle{}, preserveSoftBreaks))
	case *ast.TextBlock:
		return markdownInlineLines(node, source, role, markdownInlineStyle{}, preserveSoftBreaks)
	case *ast.FencedCodeBlock:
		return markdownCodeLines(string(typed.Text(source)))
	case *ast.CodeBlock:
		return markdownCodeLines(markdownBlockSource(node, source))
	case *ast.List:
		return markdownListLines(typed, source, role, preserveSoftBreaks)
	case *ast.Blockquote:
		return prefixMarkdownLines(markdownChildBlockLines(node, source, role, preserveSoftBreaks), "> ", role, true)
	case *ast.ThematicBreak:
		return []Line{{Spans: []Span{SemanticSpan("───", role, SpanAttributeFaint)}}}
	default:
		return markdownChildBlockLines(node, source, role, preserveSoftBreaks)
	}
}

type markdownInlineStyle struct {
	Bold      bool
	Italic    bool
	Faint     bool
	Underline bool
	Role      *StyleRole
}

type markdownLineBuilder struct {
	role  StyleRole
	lines []Line
	spans []Span
}

func (b *markdownLineBuilder) append(text string, style markdownInlineStyle) {
	if text == "" {
		return
	}
	role := b.role
	if style.Role != nil {
		role = *style.Role
	}
	attributes := SpanAttribute(0)
	if style.Faint {
		attributes |= SpanAttributeFaint
	}
	if style.Bold {
		attributes |= SpanAttributeBold
	}
	if style.Italic {
		attributes |= SpanAttributeItalic
	}
	if style.Underline {
		attributes |= SpanAttributeUnderline
	}
	next := Span{Text: text, Style: SemanticStyle(role)}
	next.Style.Attributes = attributes
	last := len(b.spans) - 1
	if last >= 0 && sameSpanStyle(b.spans[last], next) {
		b.spans[last].Text += text
		return
	}
	b.spans = append(b.spans, next)
}

func (b *markdownLineBuilder) breakLine() {
	b.lines = append(b.lines, Line{Spans: append([]Span(nil), b.spans...)})
	b.spans = nil
}

func (b *markdownLineBuilder) finish() []Line {
	if len(b.spans) > 0 || len(b.lines) == 0 {
		b.breakLine()
	}
	return b.lines
}

func markdownInlineLines(node ast.Node, source []byte, role StyleRole, style markdownInlineStyle, preserveSoftBreaks bool) []Line {
	builder := markdownLineBuilder{role: role}
	renderMarkdownInlineChildren(&builder, node, source, style, preserveSoftBreaks)
	return builder.finish()
}

func renderMarkdownInlineChildren(builder *markdownLineBuilder, node ast.Node, source []byte, style markdownInlineStyle, preserveSoftBreaks bool) {
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		switch typed := child.(type) {
		case *ast.Text:
			builder.append(string(typed.Text(source)), style)
			if typed.HardLineBreak() || (preserveSoftBreaks && typed.SoftLineBreak()) {
				builder.breakLine()
			} else if typed.SoftLineBreak() {
				builder.append(" ", style)
			}
		case *ast.String:
			builder.append(string(typed.Value), style)
		case *ast.Emphasis:
			next := style
			if typed.Level >= 2 {
				next.Bold = true
			} else {
				next.Italic = true
			}
			renderMarkdownInlineChildren(builder, typed, source, next, preserveSoftBreaks)
		case *ast.CodeSpan:
			next := style
			codeRole := StyleRoleMarkdownCode
			next.Role = &codeRole
			next.Faint = false
			renderMarkdownInlineChildren(builder, typed, source, next, preserveSoftBreaks)
		case *ast.Link:
			next := style
			next.Underline = true
			renderMarkdownInlineChildren(builder, typed, source, next, preserveSoftBreaks)
		case *ast.AutoLink:
			next := style
			next.Underline = true
			builder.append(string(typed.Label(source)), next)
		default:
			if child.HasChildren() {
				renderMarkdownInlineChildren(builder, child, source, style, preserveSoftBreaks)
			}
		}
	}
}

func markdownCodeLines(code string) []Line {
	code = strings.TrimRight(strings.ReplaceAll(code, "\r\n", "\n"), "\n")
	if code == "" {
		return []Line{{Spans: []Span{SemanticSpan("", StyleRoleMarkdownCode)}}}
	}
	lines := strings.Split(code, "\n")
	out := make([]Line, 0, len(lines))
	for _, line := range lines {
		out = append(out, Line{Spans: []Span{SemanticSpan(line, StyleRoleMarkdownCode)}})
	}
	return out
}

func markdownBlockSource(node ast.Node, source []byte) string {
	segments := node.Lines()
	if segments == nil {
		return ""
	}
	var out strings.Builder
	for index := 0; index < segments.Len(); index++ {
		segment := segments.At(index)
		lineStart := segment.Start
		for lineStart > 0 && source[lineStart-1] != '\n' {
			lineStart--
		}
		prefix := source[lineStart:segment.Start]
		if bytesOnlyWhitespace(prefix) {
			out.Write(prefix)
		}
		out.Write(segment.Value(source))
	}
	return out.String()
}

func restoreMarkdownParagraphIndent(node ast.Node, source []byte, lines []Line) []Line {
	if _, nestedInList := node.Parent().(*ast.ListItem); nestedInList {
		return lines
	}
	segments := node.Lines()
	if segments == nil {
		return lines
	}
	out := append([]Line(nil), lines...)
	limit := min(len(out), segments.Len())
	for index := 0; index < limit; index++ {
		segment := segments.At(index)
		lineStart := segment.Start
		for lineStart > 0 && source[lineStart-1] != '\n' {
			lineStart--
		}
		prefix := source[lineStart:segment.Start]
		if len(prefix) == 0 || !bytesOnlyWhitespace(prefix) {
			continue
		}
		out[index].Spans = append([]Span{SemanticSpan(string(prefix), firstSpanRole(out[index].Spans))}, out[index].Spans...)
	}
	return out
}

func bytesOnlyWhitespace(value []byte) bool {
	for _, character := range value {
		switch character {
		case ' ', '\t', '\r':
		default:
			return false
		}
	}
	return true
}

func markdownChildBlockLines(node ast.Node, source []byte, role StyleRole, preserveSoftBreaks bool) []Line {
	var out []Line
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		out = append(out, markdownBlockLines(child, source, role, preserveSoftBreaks)...)
	}
	return out
}

func markdownListLines(list *ast.List, source []byte, role StyleRole, preserveSoftBreaks bool) []Line {
	var out []Line
	ordinal := list.Start
	for child := list.FirstChild(); child != nil; child = child.NextSibling() {
		item, ok := child.(*ast.ListItem)
		if !ok {
			continue
		}
		prefix := "• "
		if list.IsOrdered() {
			prefix = fmt.Sprintf("%d. ", ordinal)
			ordinal++
		}
		out = append(out, prefixMarkdownLines(markdownChildBlockLines(item, source, role, preserveSoftBreaks), prefix, role, false)...)
	}
	return out
}

func prefixMarkdownLines(lines []Line, firstPrefix string, role StyleRole, faint bool) []Line {
	if len(lines) == 0 {
		return nil
	}
	continuation := strings.Repeat(" ", uniseg.StringWidth(firstPrefix))
	out := make([]Line, 0, len(lines))
	for index, line := range lines {
		prefix := continuation
		if index == 0 {
			prefix = firstPrefix
		}
		prefixSpan := SemanticSpan(prefix, role)
		if faint {
			prefixSpan.Style = prefixSpan.Style.With(SpanAttributeFaint)
		}
		spans := append([]Span{prefixSpan}, line.Spans...)
		out = append(out, Line{Spans: spans})
	}
	return out
}

func wrapStyledLine(spans []Span, width int) []Line {
	width = max(1, width)
	var lines []Line
	var current []Span
	currentWidth := 0
	appendSpan := func(span Span) {
		if span.Text == "" {
			return
		}
		last := len(current) - 1
		if last >= 0 && sameSpanStyle(current[last], span) {
			current[last].Text += span.Text
			return
		}
		current = append(current, span)
	}
	flushLine := func() {
		lines = append(lines, Line{Spans: append([]Span(nil), current...)})
		current = nil
		currentWidth = 0
	}
	for _, span := range spans {
		graphemes := uniseg.NewGraphemes(span.Text)
		for graphemes.Next() {
			cluster := graphemes.Str()
			clusterWidth := uniseg.StringWidth(cluster)
			if currentWidth > 0 && currentWidth+clusterWidth > width {
				flushLine()
			}
			next := span
			next.Text = cluster
			appendSpan(next)
			currentWidth += clusterWidth
		}
	}
	if len(current) > 0 {
		flushLine()
	}
	if len(lines) == 0 {
		return []Line{{Spans: []Span{SemanticSpan("", firstSpanRole(spans))}}}
	}
	return lines
}

func sameSpanStyle(left, right Span) bool {
	return left.Style == right.Style
}

func firstSpanRole(spans []Span) StyleRole {
	for _, span := range spans {
		if role, ok := span.Style.Role(); ok {
			return role
		}
	}
	return StyleRoleNotice
}
