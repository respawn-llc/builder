package rpcwire

type FieldResultKind uint8

const (
	FieldResultAvailable FieldResultKind = iota + 1
	FieldResultUnavailable
	FieldResultFailed
)

type FieldResult[T any, R any, E any] interface {
	isFieldResult()
}

type fieldAvailable[T any] struct {
	value T
}

type fieldUnavailable[R any] struct {
	reason R
}

type fieldFailed[E any] struct {
	failure E
}

func (fieldAvailable[T]) isFieldResult()   {}
func (fieldUnavailable[R]) isFieldResult() {}
func (fieldFailed[E]) isFieldResult()      {}

func AvailableField[T any, R any, E any](value T) FieldResult[T, R, E] {
	return fieldAvailable[T]{value: value}
}

func UnavailableField[T any, R any, E any](reason R) FieldResult[T, R, E] {
	return fieldUnavailable[R]{reason: reason}
}

func FailedField[T any, R any, E any](failure E) FieldResult[T, R, E] {
	return fieldFailed[E]{failure: failure}
}

func FieldResultKindOf[T any, R any, E any](result FieldResult[T, R, E]) (FieldResultKind, bool) {
	switch result.(type) {
	case fieldAvailable[T]:
		return FieldResultAvailable, true
	case fieldUnavailable[R]:
		return FieldResultUnavailable, true
	case fieldFailed[E]:
		return FieldResultFailed, true
	default:
		return 0, false
	}
}

func FieldValue[T any, R any, E any](result FieldResult[T, R, E]) (T, bool) {
	value, ok := result.(fieldAvailable[T])
	if !ok {
		var zero T
		return zero, false
	}
	return value.value, true
}

func FieldUnavailableReason[T any, R any, E any](result FieldResult[T, R, E]) (R, bool) {
	unavailable, ok := result.(fieldUnavailable[R])
	if !ok {
		var zero R
		return zero, false
	}
	return unavailable.reason, true
}

func FieldFailure[T any, R any, E any](result FieldResult[T, R, E]) (E, bool) {
	failed, ok := result.(fieldFailed[E])
	if !ok {
		var zero E
		return zero, false
	}
	return failed.failure, true
}
