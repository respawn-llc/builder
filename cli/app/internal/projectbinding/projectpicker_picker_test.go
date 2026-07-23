package projectbinding

import (
	"path/filepath"
	"testing"
)

func TestVisibleRowsBudgetPreviewAndGroup(t *testing.T) {
	rows := VisibleRows(VisibleRowsRequest{
		Offset:     0,
		ItemCount:  4,
		LineBudget: 6,
		HasPreview: func(index int) bool { return index > 0 },
		ShowGroup:  func(index int, rendered bool) bool { return !rendered && index == 1 },
	})
	want := []VisibleRow{{Index: 0}, {Index: 1, ShowPreview: false, ShowGroup: true}}
	if len(rows) != len(want) {
		t.Fatalf("rows = %+v, want %+v", rows, want)
	}
	for index := range want {
		if rows[index] != want[index] {
			t.Fatalf("rows = %+v, want %+v", rows, want)
		}
	}
}

func TestEnsureCursorVisibleScrollsAndCompactsOffset(t *testing.T) {
	req := VisibleRowsRequest{ItemCount: 5, LineBudget: 3}
	if got := EnsureCursorVisible(4, 0, req); got != 3 {
		t.Fatalf("offset for cursor 4 = %d, want 3", got)
	}
	if got := EnsureCursorVisible(2, 4, req); got != 1 {
		t.Fatalf("offset for cursor 2 = %d, want 1", got)
	}
	if got := EnsureCursorVisible(1, 3, req); got != 0 {
		t.Fatalf("compacted offset = %d, want 0", got)
	}
}

func TestProjectRowMappingWithCreateRow(t *testing.T) {
	if index, ok := ProjectIndexForRow(0, 2, true); ok || index != 0 {
		t.Fatalf("create row should not map to project, got index=%d ok=%v", index, ok)
	}
	if index, ok := ProjectIndexForRow(2, 2, true); !ok || index != 1 {
		t.Fatalf("row 2 maps to index=%d ok=%v, want 1 true", index, ok)
	}
	if got := MoveCursor(2, 1, 3); got != 2 {
		t.Fatalf("cursor past end = %d, want 2", got)
	}
}

func TestEmptyProjectListWithAndWithoutCreateRow(t *testing.T) {
	if got := MoveCursor(3, 1, 0); got != 3 {
		t.Fatalf("empty cursor = %d, want unchanged", got)
	}
}

func TestVisibleRowsClampZeroAndNegativeBudgets(t *testing.T) {
	for _, budget := range []int{0, -4} {
		rows := VisibleRows(VisibleRowsRequest{ItemCount: 2, LineBudget: budget})
		if len(rows) != 1 || rows[0].Index != 0 {
			t.Fatalf("budget %d rows = %+v, want first row only", budget, rows)
		}
	}
}

func TestEnsureCursorVisibleClampsOffsetWhenSelectedRowDisappears(t *testing.T) {
	if got := EnsureCursorVisible(4, 4, VisibleRowsRequest{ItemCount: 2, LineBudget: 1}); got != 1 {
		t.Fatalf("clamped offset = %d, want 1", got)
	}
}

func TestRowTextAndPreviewPath(t *testing.T) {
	project := ProjectRowText("", "project-1", "/home/me/work/app", "now", "/home/me")
	if project.Title != "project-1" || project.Preview != filepath.Join("~", "work", "app") || project.Timestamp != "now" {
		t.Fatalf("project row text = %+v", project)
	}
	workspace := WorkspaceRowText("", "/outside/app", "", "/home/me")
	if workspace.Title != "app" || workspace.Preview != "/outside/app" {
		t.Fatalf("workspace row text = %+v", workspace)
	}
}
