package textutil

import (
	"cmp"
	"strings"
)

// OptionalTrimmedString returns nil for blank text and a pointer to the
// normalized value otherwise.
func OptionalTrimmedString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

// OptionalExactString returns nil for blank text while preserving every byte
// of a present value.
func OptionalExactString(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func Pointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

// OptionalValue returns an optional value with its presence fact.
func OptionalValue[T any](value *T) (T, bool) {
	if value == nil {
		var zero T
		return zero, false
	}
	return *value, true
}

// OptionalExact returns an optional string-like value without normalization.
func OptionalExact[T ~string](value *T) (string, bool) {
	if value == nil {
		return "", false
	}
	return string(*value), true
}

// OptionalTrimmed returns an optional string-like value after trimming.
func OptionalTrimmed[T ~string](value *T) (string, bool) {
	if value == nil {
		return "", false
	}
	trimmed := strings.TrimSpace(string(*value))
	return trimmed, trimmed != ""
}

// FirstOptionalTrimmed returns the first present non-blank normalized value.
func FirstOptionalTrimmed[T ~string](values ...*T) (string, bool) {
	for _, value := range values {
		if trimmed, present := OptionalTrimmed(value); present {
			return trimmed, true
		}
	}
	return "", false
}

// Value returns a distinct pointer for a present value.
func Value[T any](value T) *T {
	return &value
}

// EqualOptional compares optional comparable values by presence and value.
func EqualOptional[T comparable](left *T, right *T) bool {
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
