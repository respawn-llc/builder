package core

import (
	"errors"
	"testing"

	"core/shared/serverapi"
)

func TestLegacyOptionalRecommendedOptionIndex(t *testing.T) {
	t.Run("legacy zero is absent", func(t *testing.T) {
		value, err := legacyOptionalRecommendedOptionIndex(0)
		if err != nil {
			t.Fatalf("legacyOptionalRecommendedOptionIndex(0) error = %v", err)
		}
		if value != nil {
			t.Fatalf("legacyOptionalRecommendedOptionIndex(0) = %d, want nil", *value)
		}
	})

	t.Run("negative value returns a typed error", func(t *testing.T) {
		value, err := legacyOptionalRecommendedOptionIndex(-1)
		if value != nil {
			t.Fatalf("legacyOptionalRecommendedOptionIndex(-1) = %d, want nil", *value)
		}
		var validationErr serverapi.WorkflowRequestValidationError
		if !errors.As(err, &validationErr) {
			t.Fatalf("legacyOptionalRecommendedOptionIndex(-1) error = %T %v, want WorkflowRequestValidationError", err, err)
		}
		if validationErr.Code != serverapi.WorkflowRequestErrorInvalidValue || validationErr.Field != "recommended_option_index" {
			t.Fatalf("validation error = %+v, want invalid recommended option", validationErr)
		}
	})
}
