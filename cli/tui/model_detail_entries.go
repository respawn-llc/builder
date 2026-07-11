package tui

import (
	"fmt"

	"core/cli/tui/transcriptrender"
	"core/shared/clientui"
)

type detailEntry struct {
	rowData          clientui.TranscriptCommittedRow
	presentationData transcriptrender.DetailPresentation
}

type detailProjection struct {
	entries  []detailEntry
	lines    []detailProjectedLine
	ranges   []detailLineRange
	compiler transcriptrender.DetailCompiler
}

func newDetailProjection(contentWidth int, themeName string) detailProjection {
	return detailProjection{
		compiler: transcriptrender.NewDetailCompiler(contentWidth, themeName),
	}
}

func detailEntryFromCommittedRow(
	row clientui.TranscriptCommittedRow,
	compiler transcriptrender.DetailCompiler,
) (detailEntry, bool) {
	if !detailCommittedRowVisible(row) {
		return detailEntry{}, false
	}
	return newDetailEntry(row, compiler.Compile(row)), true
}

func detailCommittedRowVisible(row clientui.TranscriptCommittedRow) bool {
	switch row.Visibility {
	case clientui.EntryVisibilityHidden:
		return false
	case clientui.EntryVisibilityOngoing,
		clientui.EntryVisibilityOngoingCollapsed,
		clientui.EntryVisibilityDetail:
	default:
		panic(fmt.Sprintf("detail received committed row with unresolved visibility %q", row.Visibility))
	}
	if !row.Integrity.Valid() {
		panic("detail entry has invalid server-projected integrity")
	}
	return true
}

func newDetailEntry(
	row clientui.TranscriptCommittedRow,
	presentation transcriptrender.DetailPresentation,
) detailEntry {
	return detailEntry{
		rowData:          row,
		presentationData: presentation,
	}
}

func (entry detailEntry) row() clientui.TranscriptCommittedRow {
	return entry.rowData
}

func (entry detailEntry) presentation() transcriptrender.DetailPresentation {
	return entry.presentationData
}

func (p detailProjection) indexOfRow(row clientui.TranscriptCommittedRow) (int, bool) {
	match := 0
	found := false
	for index, entry := range p.entries {
		if !TranscriptCommittedRowEqual(entry.row(), row) {
			continue
		}
		if found {
			return 0, false
		}
		match = index
		found = true
	}
	return match, found
}

func sameDetailGroup(left, right detailEntry) bool {
	return left.rowData.Kind == right.rowData.Kind
}

func (p *detailProjection) replaceSnapshot(
	rows []clientui.TranscriptCommittedRow,
	contentWidth int,
	themeName string,
	expanded map[int]struct{},
) {
	if p == nil {
		return
	}
	compiler := p.compiler
	if !compiler.Matches(contentWidth, themeName) {
		compiler = transcriptrender.NewDetailCompiler(contentWidth, themeName)
	}
	entries := make([]detailEntry, 0, len(rows))
	for _, row := range rows {
		if entry, ok := detailEntryFromCommittedRow(row, compiler); ok {
			entries = append(entries, entry)
		}
	}
	p.compiler = compiler
	p.entries = entries
	p.rebuildLines(contentWidth, expanded)
}

func (p *detailProjection) recompile(
	contentWidth int,
	themeName string,
	expanded map[int]struct{},
) {
	if p == nil {
		return
	}
	if p.compiler.Matches(contentWidth, themeName) {
		return
	}
	compiler := transcriptrender.NewDetailCompiler(contentWidth, themeName)
	entries := make([]detailEntry, 0, len(p.entries))
	for _, entry := range p.entries {
		entries = append(entries, newDetailEntry(entry.row(), compiler.Compile(entry.row())))
	}
	p.compiler = compiler
	p.entries = entries
	p.rebuildLines(contentWidth, expanded)
}

func (p *detailProjection) rebuildLines(contentWidth int, expanded map[int]struct{}) {
	if p == nil {
		return
	}
	targetWidth := maxInt(1, contentWidth+1)
	contentWidth = maxInt(0, contentWidth)
	renderWidth := maxInt(1, contentWidth)
	lines := make([]detailProjectedLine, 0, (len(p.entries)+1)*2)
	ranges := make([]detailLineRange, len(p.entries))
	for entryIndex, entry := range p.entries {
		if entryIndex > 0 && !sameDetailGroup(p.entries[entryIndex-1], entry) {
			lines = append(lines, detailProjectedLine{
				Kind:         detailLineGroupSpacer,
				Rail:         detailRailBlank,
				TargetWidth:  targetWidth,
				ContentWidth: contentWidth,
			})
		}
		presentation := entry.presentation()
		entryLines := presentation.Collapsed
		if _, isExpanded := expanded[entryIndex]; isExpanded && presentation.Expandable {
			entryLines = presentation.Expanded
		}
		first := len(lines)
		for _, line := range entryLines {
			lines = append(lines, detailProjectedLine{
				Content:      transcriptrender.TruncateLine(line, renderWidth, false),
				EntryIndex:   entryIndex,
				Kind:         detailLineContent,
				Rail:         detailRailBlank,
				TargetWidth:  targetWidth,
				ContentWidth: contentWidth,
			})
		}
		ranges[entryIndex] = detailLineRange{first: first, last: len(lines) - 1}
	}
	p.lines = lines
	p.ranges = ranges
}

func (p *detailProjection) clear(contentWidth int, themeName string) {
	if p == nil {
		return
	}
	*p = newDetailProjection(contentWidth, themeName)
}
