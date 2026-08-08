package setcomparison

// Equal reports whether both slices contain the same unique values.
// Duplicate values are invalid set inputs and compare unequal.
func Equal[T comparable](left []T, right []T) bool {
	if len(left) != len(right) {
		return false
	}
	remaining := make(map[T]struct{}, len(left))
	for _, value := range left {
		if _, duplicate := remaining[value]; duplicate {
			return false
		}
		remaining[value] = struct{}{}
	}
	for _, value := range right {
		if _, exists := remaining[value]; !exists {
			return false
		}
		delete(remaining, value)
	}
	return len(remaining) == 0
}
