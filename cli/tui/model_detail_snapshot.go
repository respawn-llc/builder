package tui

import (
	"maps"

	"core/shared/valuecopy"
)

type DetailPresentationSnapshot struct {
	loaded   bool
	expanded map[int]struct{}
	selected *int
	scroll   int
}

func (m Model) DetailPresentationSnapshot() DetailPresentationSnapshot {
	return DetailPresentationSnapshot{
		loaded:   m.detailPageLoaded,
		expanded: maps.Clone(m.expanded),
		selected: valuecopy.Pointer(m.selected),
		scroll:   m.detailScroll,
	}
}

func (m *Model) restoreDetailPresentation(snapshot DetailPresentationSnapshot) {
	if m == nil {
		return
	}
	if !snapshot.loaded {
		m.resetDetailTranscript()
		return
	}
	if !m.detailPageLoaded {
		panic("restore detail presentation requires a loaded transcript projection")
	}
	m.expanded = maps.Clone(snapshot.expanded)
	m.detailProjection.rebuildLines(m.detailContentWidth(), m.expanded)
	if snapshot.selected == nil || *snapshot.selected < 0 || *snapshot.selected >= len(m.detailProjection.entries) {
		m.clearSelectedDetailIndex()
	} else {
		m.setSelectedDetailIndex(*snapshot.selected)
	}
	m.detailScroll = clampInt(snapshot.scroll, 0, m.maxDetailScroll())
}
