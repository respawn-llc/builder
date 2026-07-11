package runtime

import (
	"strings"

	"core/shared/transcript"
)

func normalizeRuntimeEntryVisibility(visibility transcript.EntryVisibility) transcript.EntryVisibility {
	switch strings.ToLower(strings.TrimSpace(string(visibility))) {
	case "all":
		return transcript.EntryVisibilityOngoing
	case "verbose":
		return transcript.EntryVisibilityDetail
	default:
		return transcript.NormalizeEntryVisibility(visibility)
	}
}
