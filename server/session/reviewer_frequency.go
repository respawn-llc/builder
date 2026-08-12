package session

import "strings"

func NormalizeReviewerFrequency(frequency string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(frequency)) {
	case "off":
		return "off", true
	case "all":
		return "all", true
	case "edits":
		return "edits", true
	default:
		return "", false
	}
}
