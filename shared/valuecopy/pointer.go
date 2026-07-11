package valuecopy

import "cmp"

func Pointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
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
