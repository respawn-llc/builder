package textutil

// CloneInt returns an independent copy of value and preserves nil as absence.
func CloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
