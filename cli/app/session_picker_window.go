package app

type sessionPickerVisibleRow struct {
	index       int
	showPreview bool
}

func (m *sessionPickerModel) ensureSelectedVisible(tab *sessionPickerTab) {
	selected := tab.selectedIndex()
	if selected == nil {
		tab.offset = 0
		return
	}
	if *selected < tab.offset {
		tab.offset = *selected
	}
	for tab.offset < *selected && !m.rowVisibleFromOffset(tab, tab.offset, *selected) {
		tab.offset++
	}
	if tab.offset < 0 {
		tab.offset = 0
	}
	for tab.offset > 0 && m.rowVisibleFromOffset(tab, tab.offset-1, *selected) {
		tab.offset--
	}
	maxOffset := tab.itemCount() - 1
	if maxOffset < 0 {
		maxOffset = 0
	}
	if tab.offset > maxOffset {
		tab.offset = maxOffset
	}
}

func (m *sessionPickerModel) visibleRowsFromOffset(tab *sessionPickerTab, offset int) []sessionPickerVisibleRow {
	budget := m.visibleLineBudget()
	if budget <= 0 {
		return nil
	}
	visible := make([]sessionPickerVisibleRow, 0, tab.itemCount())
	for index := offset; index < tab.itemCount(); index++ {
		separator := 0
		if len(visible) > 0 {
			separator = 1
		}
		available := budget - separator
		if available < 1 {
			break
		}
		showPreview := m.hasPreview(tab, index) && available >= 2
		rowLines := 1
		if showPreview {
			rowLines = 2
		}
		if rowLines > available {
			if len(visible) == 0 {
				return []sessionPickerVisibleRow{{index: index}}
			}
			break
		}
		visible = append(visible, sessionPickerVisibleRow{index: index, showPreview: showPreview})
		budget -= separator + rowLines
		if budget == 0 {
			break
		}
	}
	return visible
}

func (m *sessionPickerModel) rowVisibleFromOffset(tab *sessionPickerTab, offset, index int) bool {
	for _, row := range m.visibleRowsFromOffset(tab, offset) {
		if row.index == index {
			return true
		}
	}
	return false
}
