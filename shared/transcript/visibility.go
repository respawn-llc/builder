package transcript

import "strings"

type EntryVisibility string

const (
	EntryVisibilityAuto             EntryVisibility = ""
	EntryVisibilityOngoing          EntryVisibility = "O"
	EntryVisibilityOngoingCollapsed EntryVisibility = "OC"
	EntryVisibilityDetail           EntryVisibility = "D"
	EntryVisibilityHidden           EntryVisibility = "X"
)

func NormalizeEntryVisibility(visibility EntryVisibility) EntryVisibility {
	switch strings.ToLower(strings.TrimSpace(string(visibility))) {
	case "", "auto":
		return EntryVisibilityAuto
	case "o", "ongoing":
		return EntryVisibilityOngoing
	case "oc", "ongoing_collapsed":
		return EntryVisibilityOngoingCollapsed
	case "d", "detail":
		return EntryVisibilityDetail
	case "x", "hidden":
		return EntryVisibilityHidden
	default:
		return EntryVisibility(strings.TrimSpace(string(visibility)))
	}
}
