package metadata

// SessionIDSetsEqual reports whether both inputs contain the same distinct
// Session IDs. Duplicate IDs are invalid and compare unequal.
func SessionIDSetsEqual(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	remaining := make(map[string]struct{}, len(left))
	for _, id := range left {
		if _, exists := remaining[id]; exists {
			return false
		}
		remaining[id] = struct{}{}
	}
	for _, id := range right {
		if _, exists := remaining[id]; !exists {
			return false
		}
		delete(remaining, id)
	}
	return len(remaining) == 0
}
