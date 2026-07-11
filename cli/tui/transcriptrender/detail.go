package transcriptrender

import (
	"fmt"
	"slices"

	"core/shared/clientui"
	"core/shared/theme"
	"core/shared/transcript"
)

type DetailPresentation struct {
	Collapsed  []Line
	Expanded   []Line
	Expandable bool
}

type DetailCompiler struct {
	width     int
	themeName string
	syntax    syntaxProjector
}

func NewDetailCompiler(width int, themeName string) DetailCompiler {
	resolvedTheme := theme.Resolve(themeName)
	return DetailCompiler{
		width:     max(0, width),
		themeName: resolvedTheme,
		syntax:    newSyntaxProjector(resolvedTheme),
	}
}

func (c DetailCompiler) Matches(width int, themeName string) bool {
	return c.width == max(0, width) && c.themeName == theme.Resolve(themeName)
}

func (c DetailCompiler) Compile(row clientui.TranscriptCommittedRow) DetailPresentation {
	return renderDetailPresentation(row, max(1, c.width), c.syntax)
}

func RenderDetailPresentation(row clientui.TranscriptCommittedRow, width int, themeName string) DetailPresentation {
	return NewDetailCompiler(width, themeName).Compile(row)
}

func renderDetailPresentation(
	row clientui.TranscriptCommittedRow,
	width int,
	syntax syntaxProjector,
) DetailPresentation {
	if !row.Integrity.Valid() {
		panic(fmt.Sprintf("render detail presentation with invalid integrity classification: %d", row.Integrity))
	}
	collapsed := renderCommittedRow(row, width, ModeDetailCollapsed, &syntax).Lines
	expanded := renderCommittedRow(row, width, ModeDetailExpanded, &syntax).Lines
	expandable := !detailLinesEqual(collapsed, expanded)
	if row.Integrity == transcript.RowIntegrityRecoverableMalformed {
		expandable = true
	}
	if row.Integrity == transcript.RowIntegrityUnrecoverableMalformed {
		expandable = false
	}
	return DetailPresentation{
		Collapsed:  collapsed,
		Expanded:   expanded,
		Expandable: expandable,
	}
}

func detailLinesEqual(left, right []Line) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !detailLineEqual(left[index], right[index]) {
			return false
		}
	}
	return true
}

func detailLineEqual(left, right Line) bool {
	if left.Background != right.Background {
		return false
	}
	if (left.LeadingSymbol == nil) != (right.LeadingSymbol == nil) {
		return false
	}
	if left.LeadingSymbol != nil && *left.LeadingSymbol != *right.LeadingSymbol {
		return false
	}
	return slices.Equal(left.Spans, right.Spans)
}
