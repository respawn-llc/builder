package transcriptrender

import (
	"fmt"
	"strings"

	"github.com/rivo/uniseg"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// RenderMarkdownLines projects Markdown source into transcript-owned semantic
// spans without adding row symbols or continuation guides.
func RenderMarkdownLines(role StyleRole, sourceText string, width int) []Line {
	source := []byte(sourceText)
	document := goldmark.New().Parser().Parse(text.NewReader(source))
	if document.FirstChild() == nil {
		return []Line{{Spans: []Span{SemanticSpan("", role)}}}
	}
	var logical []Line
	for node := document.FirstChild(); node != nil; node = node.NextSibling() {
		block := markdownBlockLines(node, source, role)
		if len(block) == 0 {
			continue
		}
		if len(logical) > 0 {
			logical = append(logical, Line{Spans: []Span{SemanticSpan("", role)}})
		}
		logical = append(logical, block...)
	}
	out := make([]Line, 0, len(logical))
	for _, line := range logical {
		out = append(out, wrapStyledLine(line.Spans, width)...)
	}
	if len(out) == 0 {
		return []Line{{Spans: []Span{SemanticSpan("", role)}}}
	}
	return out
}

func markdownBlockLines(node ast.Node, source []byte, role StyleRole) []Line {
	switch typed := node.(type) {
	case *ast.Heading:
		return markdownInlineLines(typed, source, role, markdownInlineStyle{Bold: true})
	case *ast.Paragraph:
		return restoreMarkdownParagraphIndent(node, source, markdownInlineLines(node, source, role, markdownInlineStyle{}))
	case *ast.TextBlock:
		return markdownInlineLines(node, source, role, markdownInlineStyle{})
	case *ast.FencedCodeBlock:
		return markdownCodeLines(string(typed.Text(source)))
	case *ast.CodeBlock:
		return markdownCodeLines(markdownBlockSource(node, source))
	case *ast.List:
		return markdownListLines(typed, source, role)
	case *ast.Blockquote:
		return prefixMarkdownLines(markdownChildBlockLines(node, source, role), "> ", role, true)
	case *ast.ThematicBreak:
		return []Line{{Spans: []Span{SemanticSpan("───", role, SpanAttributeFaint)}}}
	default:
		return markdownChildBlockLines(node, source, role)
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

func markdownInlineLines(node ast.Node, source []byte, role StyleRole, style markdownInlineStyle) []Line {
	builder := markdownLineBuilder{role: role}
	renderMarkdownInlineChildren(&builder, node, source, style)
	return builder.finish()
}

func renderMarkdownInlineChildren(builder *markdownLineBuilder, node ast.Node, source []byte, style markdownInlineStyle) {
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		switch typed := child.(type) {
		case *ast.Text:
			builder.append(string(typed.Text(source)), style)
			if typed.SoftLineBreak() || typed.HardLineBreak() {
				builder.breakLine()
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
			renderMarkdownInlineChildren(builder, typed, source, next)
		case *ast.CodeSpan:
			next := style
			codeRole := StyleRoleMarkdownCode
			next.Role = &codeRole
			next.Faint = false
			renderMarkdownInlineChildren(builder, typed, source, next)
		case *ast.Link:
			next := style
			next.Underline = true
			renderMarkdownInlineChildren(builder, typed, source, next)
		case *ast.AutoLink:
			next := style
			next.Underline = true
			builder.append(string(typed.Label(source)), next)
		default:
			if child.HasChildren() {
				renderMarkdownInlineChildren(builder, child, source, style)
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

func markdownChildBlockLines(node ast.Node, source []byte, role StyleRole) []Line {
	var out []Line
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		out = append(out, markdownBlockLines(child, source, role)...)
	}
	return out
}

func markdownListLines(list *ast.List, source []byte, role StyleRole) []Line {
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
		out = append(out, prefixMarkdownLines(markdownChildBlockLines(item, source, role), prefix, role, false)...)
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
