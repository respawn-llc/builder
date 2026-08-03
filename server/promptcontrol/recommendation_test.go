package promptcontrol

import (
	"errors"
	"testing"
)

func TestDecodeLegacyRecommendedOptionIndex(t *testing.T) {
	if decoded, err := DecodeLegacyRecommendedOptionIndex(0, 2); err != nil || decoded != nil {
		t.Fatalf("decode absent recommendation = %v, %v; want nil, nil", decoded, err)
	}
	decoded, err := DecodeLegacyRecommendedOptionIndex(2, 2)
	if err != nil || decoded == nil || *decoded != 2 {
		t.Fatalf("decode present recommendation = %v, %v; want 2, nil", decoded, err)
	}
	for _, invalid := range []int{-1, 3} {
		decoded, err := DecodeLegacyRecommendedOptionIndex(invalid, 2)
		if decoded != nil || !errors.Is(err, ErrRecommendedOptionIndexInvalid) {
			t.Fatalf(
				"decode invalid recommendation %d = %v, %v; want nil, typed error",
				invalid,
				decoded,
				err,
			)
		}
	}
}
