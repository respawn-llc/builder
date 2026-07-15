package transcriptrender

import (
	"fmt"
	"net/url"
	"strings"

	"charm.land/glamour/v2"
	glamourstyles "charm.land/glamour/v2/styles"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/cellbuf"
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
	return RenderMarkdownLinesWithLinkPresentation(role, sourceText, width, MarkdownLinkLabelOnly)
}

func RenderMarkdownLinesWithLinkPresentation(
	role StyleRole,
	sourceText string,
	width int,
	linkPresentation MarkdownLinkPresentation,
) []Line {
	return renderMarkdown(role, sourceText, width, true, true, linkPresentation)
}

// RenderMarkdownStableLines leaves ordinary Markdown wrapping to the terminal.
// Constructs with cross-row horizontal layout, such as tables, are formatted
// to the supplied width by the Markdown renderer.
func RenderMarkdownStableLines(role StyleRole, sourceText string, width int) []Line {
	return RenderMarkdownStableLinesWithLinkPresentation(role, sourceText, width, MarkdownLinkLabelOnly)
}

func RenderMarkdownStableLinesWithLinkPresentation(
	role StyleRole,
	sourceText string,
	width int,
	linkPresentation MarkdownLinkPresentation,
) []Line {
	return renderMarkdown(role, sourceText, width, false, false, linkPresentation)
}

type renderedMarkdownBlock struct {
	lines          []Line
	widthFormatted bool
}

func renderMarkdown(
	role StyleRole,
	sourceText string,
	width int,
	preserveSoftBreaks bool,
	wrapFlow bool,
	linkPresentation MarkdownLinkPresentation,
) []Line {
	if !linkPresentation.Valid() {
		panic(fmt.Sprintf("render markdown with invalid link presentation %d", linkPresentation))
	}
	sourceText = TerminalSafePlainText(sourceText)
	source := []byte(sourceText)
	document := goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,
			extension.DefinitionList,
		),
	).Parser().Parse(text.NewReader(source))
	if document.FirstChild() == nil {
		return []Line{{Spans: []Span{SemanticSpan("", role)}}}
	}
	var out []Line
	for node := document.FirstChild(); node != nil; node = node.NextSibling() {
		block := renderMarkdownBlock(node, source, role, width, preserveSoftBreaks, linkPresentation)
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

func renderMarkdownBlock(
	node ast.Node,
	source []byte,
	role StyleRole,
	width int,
	preserveSoftBreaks bool,
	linkPresentation MarkdownLinkPresentation,
) renderedMarkdownBlock {
	if table, ok := node.(*extensionast.Table); ok {
		return renderedMarkdownBlock{
			lines:          renderMarkdownTable(table, source, role, width),
			widthFormatted: true,
		}
	}
	return renderedMarkdownBlock{
		lines: markdownBlockLines(node, source, role, width, preserveSoftBreaks, linkPresentation),
	}
}

func renderMarkdownTable(
	table *extensionast.Table,
	source []byte,
	role StyleRole,
	width int,
) []Line {
	style := glamourstyles.ASCIIStyleConfig
	zero := uint(0)
	centerSeparator := "┼"
	columnSeparator := "│"
	rowSeparator := "─"
	style.Document.Margin = &zero
	style.Document.BlockPrefix = ""
	style.Document.BlockSuffix = ""
	style.Table.CenterSeparator = &centerSeparator
	style.Table.ColumnSeparator = &columnSeparator
	style.Table.RowSeparator = &rowSeparator
	renderer, err := glamour.NewTermRenderer(
		glamour.WithWordWrap(max(1, width)),
		glamour.WithTableWrap(true),
		glamour.WithInlineTableLinks(true),
		glamour.WithStyles(style),
	)
	if err != nil {
		panic(fmt.Sprintf("create markdown table renderer: %v", err))
	}
	rendered, err := renderer.Render(markdownTableSource(table, source))
	if err != nil {
		panic(fmt.Sprintf("render markdown table: %v", err))
	}
	return markdownANSILines(rendered, role)
}

func markdownTableSource(table *extensionast.Table, source []byte) string {
	var rows []string
	for row := table.FirstChild(); row != nil; row = row.NextSibling() {
		var cells []string
		for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
			cells = append(cells, markdownTableCellSource(cell, source))
		}
		rows = append(rows, "| "+strings.Join(cells, " | ")+" |")
		if _, header := row.(*extensionast.TableHeader); header {
			delimiters := make([]string, len(table.Alignments))
			for index, alignment := range table.Alignments {
				switch alignment {
				case extensionast.AlignLeft:
					delimiters[index] = ":---"
				case extensionast.AlignRight:
					delimiters[index] = "---:"
				case extensionast.AlignCenter:
					delimiters[index] = ":---:"
				case extensionast.AlignNone:
					delimiters[index] = "---"
				default:
					panic(fmt.Sprintf("render Markdown table with invalid alignment %d", alignment))
				}
			}
			rows = append(rows, "| "+strings.Join(delimiters, " | ")+" |")
		}
	}
	return strings.Join(rows, "\n")
}

func markdownTableCellSource(node ast.Node, source []byte) string {
	segments := node.Lines()
	if segments == nil || segments.Len() == 0 {
		return ""
	}
	var out strings.Builder
	for index := 0; index < segments.Len(); index++ {
		segment := segments.At(index)
		out.Write(segment.Value(source))
	}
	return out.String()
}

func markdownANSILines(rendered string, role StyleRole) []Line {
	var (
		lines  []Line
		spans  []Span
		active *Hyperlink
	)
	appendText := func(text string) {
		if text == "" {
			return
		}
		next := SemanticSpan(text, role)
		next.Hyperlink = active
		appendCoalescedSpan(&spans, next)
	}
	breakLine := func() {
		lines = append(lines, Line{Spans: append([]Span(nil), spans...)})
		spans = nil
	}

	parser := xansi.NewParser()
	parser.SetHandler(xansi.Handler{
		Print: func(character rune) {
			appendText(string(character))
		},
		Execute: func(control byte) {
			switch control {
			case '\n':
				breakLine()
			case '\t':
				appendText("\t")
			case '\r':
			}
		},
		HandleOsc: func(command int, data []byte) {
			if command != 8 {
				return
			}
			var link cellbuf.Link
			cellbuf.ReadLink(data, &link)
			if link.Empty() {
				active = nil
				return
			}
			active = &Hyperlink{URL: link.URL}
		},
	})
	parser.Parse([]byte(rendered))
	if active != nil {
		panic(fmt.Sprintf("render Markdown table with unclosed hyperlink %q", active.URL))
	}
	if len(spans) > 0 {
		breakLine()
	}
	for len(lines) > 0 && strings.TrimSpace(lines[0].Plain()) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1].Plain()) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func markdownBlockLines(
	node ast.Node,
	source []byte,
	role StyleRole,
	width int,
	preserveSoftBreaks bool,
	linkPresentation MarkdownLinkPresentation,
) []Line {
	switch typed := node.(type) {
	case *ast.Heading:
		return markdownInlineLines(typed, source, role, markdownInlineStyle{Bold: true}, preserveSoftBreaks, linkPresentation)
	case *ast.Paragraph:
		return restoreMarkdownParagraphIndent(
			node,
			source,
			markdownInlineLines(node, source, role, markdownInlineStyle{}, preserveSoftBreaks, linkPresentation),
		)
	case *ast.TextBlock:
		return markdownInlineLines(node, source, role, markdownInlineStyle{}, preserveSoftBreaks, linkPresentation)
	case *ast.FencedCodeBlock:
		return markdownCodeLines(string(typed.Text(source)))
	case *ast.CodeBlock:
		return markdownCodeLines(markdownBlockSource(node, source))
	case *extensionast.Table:
		return renderMarkdownTable(typed, source, role, width)
	case *ast.List:
		return markdownListLines(typed, source, role, width, preserveSoftBreaks, linkPresentation)
	case *ast.Blockquote:
		const prefix = "> "
		return prefixMarkdownLines(
			markdownChildBlockLines(
				node,
				source,
				role,
				max(1, width-uniseg.StringWidth(prefix)),
				preserveSoftBreaks,
				linkPresentation,
			),
			prefix,
			role,
			true,
		)
	case *extensionast.DefinitionTerm:
		return markdownInlineLines(typed, source, role, markdownInlineStyle{}, preserveSoftBreaks, linkPresentation)
	case *extensionast.DefinitionDescription:
		const prefix = "  "
		return prefixMarkdownLines(
			markdownChildBlockLines(
				node,
				source,
				role,
				max(1, width-uniseg.StringWidth(prefix)),
				preserveSoftBreaks,
				linkPresentation,
			),
			prefix,
			role,
			false,
		)
	case *ast.ThematicBreak:
		return []Line{{Spans: []Span{SemanticSpan("───", role, SpanAttributeFaint)}}}
	default:
		return markdownChildBlockLines(node, source, role, width, preserveSoftBreaks, linkPresentation)
	}
}

type markdownInlineStyle struct {
	Bold          bool
	Italic        bool
	Faint         bool
	Underline     bool
	Strikethrough bool
	Role          *StyleRole
	Hyperlink     *Hyperlink
}

type markdownLineBuilder struct {
	role             StyleRole
	linkPresentation MarkdownLinkPresentation
	lines            []Line
	spans            []Span
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
	if style.Strikethrough {
		attributes |= SpanAttributeStrikethrough
	}
	next := Span{Text: text, Style: SemanticStyle(role), Hyperlink: style.Hyperlink}
	next.Style.Attributes = attributes
	appendCoalescedSpan(&b.spans, next)
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

func markdownInlineLines(
	node ast.Node,
	source []byte,
	role StyleRole,
	style markdownInlineStyle,
	preserveSoftBreaks bool,
	linkPresentation MarkdownLinkPresentation,
) []Line {
	builder := markdownLineBuilder{role: role, linkPresentation: linkPresentation}
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
		case *extensionast.Strikethrough:
			next := style
			next.Strikethrough = true
			renderMarkdownInlineChildren(builder, typed, source, next, preserveSoftBreaks)
		case *extensionast.TaskCheckBox:
			if typed.IsChecked {
				builder.append("[✓] ", style)
			} else {
				builder.append("[ ] ", style)
			}
		case *ast.Link:
			next := style
			next.Underline = true
			next.Hyperlink = markdownHyperlink(string(typed.Destination))
			renderMarkdownInlineChildren(builder, typed, source, next, preserveSoftBreaks)
			if next.Hyperlink != nil && builder.linkPresentation == MarkdownLinkLabelAndDestination {
				builder.append(" ", style)
				builder.append(next.Hyperlink.URL, next)
			}
		case *ast.AutoLink:
			next := style
			next.Underline = true
			next.Hyperlink = markdownHyperlink(string(typed.URL(source)))
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

func markdownChildBlockLines(
	node ast.Node,
	source []byte,
	role StyleRole,
	width int,
	preserveSoftBreaks bool,
	linkPresentation MarkdownLinkPresentation,
) []Line {
	var out []Line
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		out = append(out, markdownBlockLines(child, source, role, width, preserveSoftBreaks, linkPresentation)...)
	}
	return out
}

func markdownListLines(
	list *ast.List,
	source []byte,
	role StyleRole,
	width int,
	preserveSoftBreaks bool,
	linkPresentation MarkdownLinkPresentation,
) []Line {
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
		taskItem := markdownListItemIsTask(item)
		if taskItem {
			prefix = ""
		}
		itemLines := markdownChildBlockLines(
			item,
			source,
			role,
			max(1, width-uniseg.StringWidth(prefix)),
			preserveSoftBreaks,
			linkPresentation,
		)
		if taskItem {
			out = append(out, itemLines...)
			continue
		}
		out = append(
			out,
			prefixMarkdownLines(
				itemLines,
				prefix,
				role,
				false,
			)...,
		)
	}
	return out
}

func markdownListItemIsTask(item *ast.ListItem) bool {
	firstBlock := item.FirstChild()
	return firstBlock != nil && firstBlock.FirstChild() != nil &&
		firstBlock.FirstChild().Kind() == extensionast.KindTaskCheckBox
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
			appendCoalescedSpan(&current, next)
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

func appendCoalescedSpan(spans *[]Span, next Span) {
	if next.Text == "" {
		return
	}
	last := len(*spans) - 1
	if last >= 0 && sameSpanStyle((*spans)[last], next) {
		(*spans)[last].Text += next.Text
		return
	}
	*spans = append(*spans, next)
}

func sameSpanStyle(left, right Span) bool {
	if left.Style != right.Style {
		return false
	}
	if left.Hyperlink == nil || right.Hyperlink == nil {
		return left.Hyperlink == nil && right.Hyperlink == nil
	}
	return left.Hyperlink.URL == right.Hyperlink.URL
}

func markdownHyperlink(raw string) *Hyperlink {
	target := strings.TrimSpace(raw)
	if target == "" {
		return nil
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return nil
	}
	if parsed.Scheme == "" &&
		parsed.Host == "" &&
		parsed.Path == "" &&
		parsed.RawQuery == "" &&
		parsed.Fragment != "" {
		return nil
	}
	return &Hyperlink{URL: target}
}

func firstSpanRole(spans []Span) StyleRole {
	for _, span := range spans {
		if role, ok := span.Style.Role(); ok {
			return role
		}
	}
	return StyleRoleNotice
}
