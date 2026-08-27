package projectbinding

import (
	"path/filepath"
	"strings"
)

type VisibleRow struct {
	Index       int
	ShowPreview bool
	ShowGroup   bool
}

type RowText struct {
	Title     string
	Preview   string
	Timestamp string
}

type VisibleRowsRequest struct {
	Offset     int
	ItemCount  int
	LineBudget int
	HasPreview func(index int) bool
	ShowGroup  func(index int, groupRendered bool) bool
}

func ProjectIndexForRow(rowIndex int, projectCount int, allowCreate bool) (int, bool) {
	projectIndex := rowIndex
	if allowCreate {
		projectIndex--
	}
	if projectIndex < 0 || projectIndex >= projectCount {
		return 0, false
	}
	return projectIndex, true
}

func ProjectRowText(displayName string, projectID string, rootPath string, timestamp string, homeDir string) RowText {
	title := strings.TrimSpace(displayName)
	if title == "" {
		title = strings.TrimSpace(projectID)
	}
	return RowText{Title: title, Preview: PreviewPath(rootPath, homeDir), Timestamp: strings.TrimSpace(timestamp)}
}

func WorkspaceRowText(displayName string, rootPath string, homeDir string) RowText {
	title := strings.TrimSpace(displayName)
	if title == "" {
		title = strings.TrimSpace(filepath.Base(rootPath))
	}
	return RowText{Title: title, Preview: PreviewPath(rootPath, homeDir)}
}

func PreviewPath(rootPath string, homeDir string) string {
	trimmedRoot := strings.TrimSpace(rootPath)
	if trimmedRoot == "" {
		return ""
	}
	trimmedHome := strings.TrimSpace(homeDir)
	if trimmedHome == "" {
		return trimmedRoot
	}
	rel, err := filepath.Rel(trimmedHome, trimmedRoot)
	if err != nil {
		return trimmedRoot
	}
	if rel == "." {
		return "~"
	}
	if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.Join("~", rel)
	}
	return trimmedRoot
}

func MoveCursor(cursor int, delta int, itemCount int) int {
	if itemCount == 0 {
		return cursor
	}
	cursor += delta
	if cursor < 0 {
		return 0
	}
	if cursor >= itemCount {
		return itemCount - 1
	}
	return cursor
}

func EnsureCursorVisible(cursor int, offset int, req VisibleRowsRequest) int {
	if cursor < offset {
		offset = cursor
	}
	for offset < cursor && !rowVisible(visibleRowsAtOffset(req, offset), cursor) {
		offset++
	}
	if offset < 0 {
		offset = 0
	}
	for offset > 0 && rowVisible(visibleRowsAtOffset(req, offset-1), cursor) {
		offset--
	}
	maxOffset := req.ItemCount - 1
	if maxOffset < 0 {
		maxOffset = 0
	}
	if offset > maxOffset {
		offset = maxOffset
	}
	return offset
}

func visibleRowsAtOffset(req VisibleRowsRequest, offset int) []VisibleRow {
	req.Offset = offset
	return VisibleRows(req)
}

func VisibleRows(req VisibleRowsRequest) []VisibleRow {
	budget := req.LineBudget
	if budget < 1 {
		budget = 1
	}
	visible := make([]VisibleRow, 0, req.ItemCount)
	groupRendered := false
	for index := req.Offset; index < req.ItemCount; index++ {
		separator := 0
		if len(visible) > 0 {
			separator = 1
		}
		groupLines := 0
		showGroup := false
		if req.ShowGroup != nil && req.ShowGroup(index, groupRendered) {
			groupLines = 3
			showGroup = true
		}
		available := budget - separator - groupLines
		if available < 1 {
			break
		}
		showPreview := req.HasPreview != nil && req.HasPreview(index) && available >= 2
		rowLines := 1
		if showPreview {
			rowLines = 2
		}
		if rowLines > available {
			if len(visible) == 0 {
				return []VisibleRow{{Index: index, ShowPreview: false, ShowGroup: showGroup}}
			}
			break
		}
		visible = append(visible, VisibleRow{Index: index, ShowPreview: showPreview, ShowGroup: showGroup})
		budget -= separator + groupLines + rowLines
		if showGroup {
			groupRendered = true
		}
		if budget == 0 {
			break
		}
	}
	return visible
}

func rowVisible(rows []VisibleRow, index int) bool {
	for _, row := range rows {
		if row.Index == index {
			return true
		}
	}
	return false
}
