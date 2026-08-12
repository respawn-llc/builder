package transcript

import "fmt"

const (
	MinCommittedAtUnixMs int64 = -8_640_000_000_000_000
	MaxCommittedAtUnixMs int64 = 8_640_000_000_000_000
)

// ValidateCommittedAtUnixMs validates an optional committed timestamp against
// the range representable by a JavaScript Date.
func ValidateCommittedAtUnixMs(value *int64) error {
	if value == nil {
		return nil
	}
	if *value < MinCommittedAtUnixMs || *value > MaxCommittedAtUnixMs {
		return fmt.Errorf(
			"committed time must be between %d and %d Unix milliseconds, got %d",
			MinCommittedAtUnixMs,
			MaxCommittedAtUnixMs,
			*value,
		)
	}
	return nil
}
