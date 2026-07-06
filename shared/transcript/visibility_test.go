package transcript

import "testing"

func TestNormalizeEntryVisibility(t *testing.T) {
	tests := []struct {
		name       string
		visibility EntryVisibility
		want       EntryVisibility
	}{
		{name: "blank defaults to auto", visibility: "", want: EntryVisibilityAuto},
		{name: "auto normalizes to auto", visibility: "auto", want: EntryVisibilityAuto},
		{name: "auto is case-insensitive", visibility: " AUTO ", want: EntryVisibilityAuto},
		{name: "ongoing canonical", visibility: EntryVisibilityOngoing, want: EntryVisibilityOngoing},
		{name: "ongoing legacy lowercase", visibility: " o ", want: EntryVisibilityOngoing},
		{name: "ongoing legacy name", visibility: "ongoing", want: EntryVisibilityOngoing},
		{name: "collapsed canonical", visibility: EntryVisibilityOngoingCollapsed, want: EntryVisibilityOngoingCollapsed},
		{name: "collapsed legacy lowercase", visibility: " oc ", want: EntryVisibilityOngoingCollapsed},
		{name: "collapsed legacy name", visibility: "ongoing_collapsed", want: EntryVisibilityOngoingCollapsed},
		{name: "detail canonical", visibility: EntryVisibilityDetail, want: EntryVisibilityDetail},
		{name: "detail legacy lowercase", visibility: " d ", want: EntryVisibilityDetail},
		{name: "detail legacy name", visibility: "detail", want: EntryVisibilityDetail},
		{name: "hidden canonical", visibility: EntryVisibilityHidden, want: EntryVisibilityHidden},
		{name: "hidden legacy lowercase", visibility: " x ", want: EntryVisibilityHidden},
		{name: "hidden legacy name", visibility: "hidden", want: EntryVisibilityHidden},
		{name: "unknown trimmed", visibility: "  custom  ", want: EntryVisibility("custom")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeEntryVisibility(tt.visibility); got != tt.want {
				t.Fatalf("NormalizeEntryVisibility(%q) = %q, want %q", tt.visibility, got, tt.want)
			}
		})
	}
}
