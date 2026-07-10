package transcriptrender

import (
	"fmt"
	"slices"

	"core/shared/clientui"
)

type DetailIntegrity uint8

const (
	DetailIntegrityValid DetailIntegrity = iota
	DetailIntegrityRecoverableMalformed
	DetailIntegrityUnrecoverableMalformed
)

type DetailPresentation struct {
	Collapsed  []Line
	Expanded   []Line
	Expandable bool
}

func RenderDetailPresentation(row clientui.TranscriptCommittedRow, width int, themeName string, integrity DetailIntegrity) DetailPresentation {
	if !integrity.valid() {
		panic(fmt.Sprintf("render detail presentation with invalid integrity classification: %d", integrity))
	}
	collapsed := RenderCommittedRow(row, width, themeName, ModeDetailCollapsed).Lines
	expanded := RenderCommittedRow(row, width, themeName, ModeDetailExpanded).Lines
	expandable := !detailLinesEqual(collapsed, expanded)
	if integrity == DetailIntegrityRecoverableMalformed {
		expandable = true
	}
	if integrity == DetailIntegrityUnrecoverableMalformed {
		expandable = false
	}
	return DetailPresentation{
		Collapsed:  collapsed,
		Expanded:   expanded,
		Expandable: expandable,
	}
}

func (integrity DetailIntegrity) valid() bool {
	return integrity == DetailIntegrityValid ||
		integrity == DetailIntegrityRecoverableMalformed ||
		integrity == DetailIntegrityUnrecoverableMalformed
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
	if (left.LeadingSymbol == nil) != (right.LeadingSymbol == nil) {
		return false
	}
	if left.LeadingSymbol != nil && *left.LeadingSymbol != *right.LeadingSymbol {
		return false
	}
	return slices.Equal(left.Spans, right.Spans)
}
