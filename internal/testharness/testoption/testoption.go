// Package testoption provides optional-value fixtures for cross-package tests.
package testoption

func String(value string) *string {
	return &value
}

func EqualString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
