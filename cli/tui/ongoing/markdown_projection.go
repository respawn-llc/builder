package ongoing

import (
	"strings"

	"github.com/charmbracelet/glamour"
	glamouransi "github.com/charmbracelet/glamour/ansi"
	glamourstyles "github.com/charmbracelet/glamour/styles"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

type markdownProjectionInput struct {
	Source           string
	Width            int
	PromotedBoundary int
}

type markdownProjectionResult struct {
	PromotedRows      []string
	VolatileRows      []string
	PromotedBoundary  int
	ProjectionFailure *markdownProjectionFailure
}

type markdownProjectionFailure struct {
	SourceBoundary    int
	Width             int
	CandidateBoundary int
	RowIndex          int
}

type markdownRenderer interface {
	Render(source string, width int) []string
}

type markdownProjector struct {
	renderer markdownRenderer
}

func newMarkdownProjector(renderer markdownRenderer) markdownProjector {
	if renderer == nil {
		renderer = terminalMarkdownRenderer{}
	}
	return markdownProjector{renderer: renderer}
}

func (p markdownProjector) Project(input markdownProjectionInput) markdownProjectionResult {
	source := input.Source
	if input.PromotedBoundary < 0 || input.PromotedBoundary > len(source) {
		panicOngoingDeveloperError("markdown_projection", "promoted boundary outside source", map[string]any{
			"source_len":        len(source),
			"promoted_boundary": input.PromotedBoundary,
			"width":             input.Width,
		})
	}
	width := input.Width
	if width <= 0 {
		width = 80
	}
	suffix := source[input.PromotedBoundary:]
	fullRows := p.renderer.Render(suffix, width)
	candidateBoundary := longestSafeCandidateBoundary(suffix)
	if candidateBoundary == 0 {
		return markdownProjectionResult{VolatileRows: fullRows, PromotedBoundary: input.PromotedBoundary}
	}
	candidateRows := p.renderer.Render(suffix[:candidateBoundary], width)
	if !rowsPrefixEqual(fullRows, candidateRows) {
		return markdownProjectionResult{
			VolatileRows:     fullRows,
			PromotedBoundary: input.PromotedBoundary,
			ProjectionFailure: &markdownProjectionFailure{
				SourceBoundary:    input.PromotedBoundary,
				Width:             width,
				CandidateBoundary: input.PromotedBoundary + candidateBoundary,
				RowIndex:          firstDifferentRow(fullRows, candidateRows),
			},
		}
	}
	return markdownProjectionResult{
		PromotedRows:     candidateRows,
		VolatileRows:     append([]string(nil), fullRows[len(candidateRows):]...),
		PromotedBoundary: input.PromotedBoundary + candidateBoundary,
	}
}

type terminalMarkdownRenderer struct{}

func (terminalMarkdownRenderer) Render(source string, width int) []string {
	source = terminalSafeMarkdownSource(source)
	if strings.TrimSpace(source) == "" {
		return nil
	}
	renderer, err := glamour.NewTermRenderer(
		glamour.WithWordWrap(width),
		glamour.WithStyles(ongoingMarkdownStyle()),
	)
	if err != nil {
		panicOngoingDeveloperError("markdown_render", "create terminal markdown renderer", map[string]any{
			"width": width,
			"err":   err.Error(),
		})
	}
	rendered, err := renderer.Render(source)
	if err != nil {
		panicOngoingDeveloperError("markdown_render", "render terminal markdown", map[string]any{
			"width":      width,
			"source_len": len(source),
			"err":        err.Error(),
		})
	}
	return splitRenderedMarkdownRows(rendered)
}

func ongoingMarkdownStyle() glamouransi.StyleConfig {
	style := glamourstyles.DarkStyleConfig
	zero := uint(0)
	style.Document.Margin = &zero
	return style
}

func splitRenderedMarkdownRows(rendered string) []string {
	rendered = strings.TrimRight(rendered, "\n")
	if rendered == "" {
		return nil
	}
	return strings.Split(rendered, "\n")
}

func longestSafeCandidateBoundary(suffix string) int {
	if suffix == "" {
		return 0
	}
	root := parseMarkdownSuffix(suffix)
	boundary := 0
	for node := root.FirstChild(); node != nil; node = node.NextSibling() {
		end := blockSourceEnd(node)
		if end <= boundary || end > len(suffix) {
			continue
		}
		if hasClosedBlockBoundary(suffix, end) {
			boundary = end
		}
	}
	return boundary
}

func rowsPrefixEqual(fullRows, candidateRows []string) bool {
	if len(candidateRows) > len(fullRows) {
		return false
	}
	for index, candidate := range candidateRows {
		if fullRows[index] != candidate {
			return false
		}
	}
	return true
}

func firstDifferentRow(fullRows, candidateRows []string) int {
	limit := min(len(fullRows), len(candidateRows))
	for index := 0; index < limit; index++ {
		if fullRows[index] != candidateRows[index] {
			return index
		}
	}
	return limit
}

func parseMarkdownSuffix(source string) ast.Node {
	reader := text.NewReader([]byte(source))
	return goldmark.New().Parser().Parse(reader)
}

func blockSourceEnd(node ast.Node) int {
	lines := node.Lines()
	if lines == nil || lines.Len() == 0 {
		return node.Pos()
	}
	end := 0
	for index := 0; index < lines.Len(); index++ {
		segment := lines.At(index)
		if segment.Stop > end {
			end = segment.Stop
		}
	}
	return end
}

func hasClosedBlockBoundary(source string, boundary int) bool {
	if boundary <= 0 || boundary > len(source) {
		return false
	}
	if boundary == len(source) {
		return hasTrailingClosedBlockBoundary(source)
	}
	index := boundary
	sawNewline := false
	for index < len(source) {
		switch source[index] {
		case '\r':
			index++
		case '\n':
			if sawNewline {
				return true
			}
			sawNewline = true
			index++
		case ' ', '\t':
			index++
		default:
			return false
		}
	}
	return false
}

func hasTrailingClosedBlockBoundary(source string) bool {
	newlines := 0
	for index := len(source) - 1; index >= 0; index-- {
		switch source[index] {
		case ' ', '\t':
			continue
		case '\n':
			newlines++
			if newlines >= 2 {
				return true
			}
			if index > 0 && source[index-1] == '\r' {
				index--
			}
		case '\r':
			continue
		default:
			return false
		}
	}
	return false
}
