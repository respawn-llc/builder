package textutil

import "cmp"

func Pointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
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

// CompareOptional orders absent values before present values.
func CompareOptional[T cmp.Ordered](left *T, right *T) int {
	switch {
	case left == nil && right == nil:
		return 0
	case left == nil:
		return -1
	case right == nil:
		return 1
	default:
		return cmp.Compare(*left, *right)
	}
}
