package testsetup

func PreparedPublicationStage[T any, Delta any](
	buildDelta func(T) (Delta, error),
) func(T) (Delta, func(error), error) {
	if buildDelta == nil {
		panic("prepared publication delta builder is required")
	}
	return func(value T) (Delta, func(error), error) {
		delta, err := buildDelta(value)
		return delta, nil, err
	}
}
