package protoapi

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func dereference[T any](value *T) T {
	if value == nil {
		var zero T
		return zero
	}
	return *value
}
