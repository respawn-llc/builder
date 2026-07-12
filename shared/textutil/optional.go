package textutil

// CloneInt returns an independent copy of value and preserves nil as absence.
func CloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// Int returns a distinct pointer for an optional integer value.
func Int(value int) *int {
	return &value
}

// EqualOptionalInt compares optional integers by presence and value.
func EqualOptionalInt(left *int, right *int) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}
