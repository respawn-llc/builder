package promptcontrol

import (
	"errors"
	"fmt"
)

var ErrRecommendedOptionIndexInvalid = errors.New("recommended option index is invalid")

func DecodeLegacyRecommendedOptionIndex(index int, suggestionCount int) (*int, error) {
	if index == 0 {
		return nil, nil
	}
	if index < 1 || index > suggestionCount {
		return nil, fmt.Errorf(
			"%w: index %d is outside suggestions 1..%d",
			ErrRecommendedOptionIndexInvalid,
			index,
			suggestionCount,
		)
	}
	return &index, nil
}
