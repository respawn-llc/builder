// Package optional contains immutable helpers for typed optional values.
package optional

// CloneInt returns an independent copy of value and preserves nil as absence.
func CloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
