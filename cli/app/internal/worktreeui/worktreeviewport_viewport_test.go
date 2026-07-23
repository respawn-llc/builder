package worktreeui

import "testing"

func TestRowsPerPageUsesAtLeastOneRow(t *testing.T) {
	if got := RowsPerPage(3, 3, 1, 3); got != 1 {
		t.Fatalf("rows = %d, want 1", got)
	}
}

func TestRowsPerPageHandlesNonPositiveRowLines(t *testing.T) {
	if got := RowsPerPage(20, 3, 1, 0); got != 1 {
		t.Fatalf("rows = %d, want 1", got)
	}
}

func TestRowsPerPageCountsAvailableRows(t *testing.T) {
	if got := RowsPerPage(20, 3, 1, 3); got != 5 {
		t.Fatalf("rows = %d, want 5", got)
	}
}

func TestOverlayStartRowKeepsSelectionVisible(t *testing.T) {
	if got := OverlayStartRow(4, 8, 9, 3); got != 6 {
		t.Fatalf("start = %d, want 6", got)
	}
}

func TestOverlayStartRowClampsToLastRow(t *testing.T) {
	if got := OverlayStartRow(99, 3, 3, 3); got != 6 {
		t.Fatalf("start = %d, want last row offset", got)
	}
}

func TestOverlayStartRowHandlesInvalidInputs(t *testing.T) {
	cases := []struct {
		name          string
		selection     int
		rowCount      int
		contentHeight int
		rowLines      int
	}{
		{name: "negative selection", selection: -1, rowCount: 3, contentHeight: 3, rowLines: 3},
		{name: "zero rows", selection: 1, rowCount: 0, contentHeight: 3, rowLines: 3},
		{name: "zero content height", selection: 1, rowCount: 3, contentHeight: 0, rowLines: 3},
		{name: "zero row lines", selection: 1, rowCount: 3, contentHeight: 3, rowLines: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := OverlayStartRow(tc.selection, tc.rowCount, tc.contentHeight, tc.rowLines); got != 0 {
				t.Fatalf("start = %d, want 0", got)
			}
		})
	}
}

func TestDialogVisibleStartKeepsFocusedSectionInView(t *testing.T) {
	if got := DialogVisibleStart(20, 5, 8, 9); got != 8 {
		t.Fatalf("start = %d, want focused start", got)
	}
}

func TestDialogVisibleStartHandlesInvalidViewport(t *testing.T) {
	if got := DialogVisibleStart(20, 0, 8, 9); got != 0 {
		t.Fatalf("start = %d, want 0", got)
	}
}

func TestDialogVisibleStartHandlesNegativeFocusStart(t *testing.T) {
	if got := DialogVisibleStart(20, 5, -5, -1); got != 0 {
		t.Fatalf("start = %d, want 0", got)
	}
}

func TestDialogVisibleStartClampsToViewportEnd(t *testing.T) {
	if got := DialogVisibleStart(20, 5, 18, 19); got != 15 {
		t.Fatalf("start = %d, want max start", got)
	}
}

func TestDialogVisibleStartHandlesOversizedFocusedSection(t *testing.T) {
	if got := DialogVisibleStart(20, 5, 8, 20); got != 8 {
		t.Fatalf("start = %d, want oversized focused start", got)
	}
}
