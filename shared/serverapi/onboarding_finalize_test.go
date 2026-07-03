package serverapi_test

import (
	"encoding/json"
	"errors"
	"testing"

	"core/shared/serverapi"
)

func TestOnboardingFinalizeRequestDomainValidationOwnsMalformedProviderUUID(t *testing.T) {
	var req serverapi.OnboardingFinalizeRequest
	if err := json.Unmarshal([]byte(`{"skills_import":{"mode":"symlink_source","provider_uuid":"not-a-uuid"}}`), &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	err := serverapi.ValidateOnboardingFinalizeRequest(req)
	var finalizeErr *serverapi.OnboardingFinalizeError
	if !errors.As(err, &finalizeErr) {
		t.Fatalf("validation error = %T %v, want OnboardingFinalizeError", err, err)
	}
	if finalizeErr.Code != serverapi.OnboardingFinalizeInvalidRequest {
		t.Fatalf("code = %q, want invalid_request", finalizeErr.Code)
	}
	details := finalizeErr.Details.(serverapi.OnboardingInvalidRequestDetails)
	if len(details.FieldErrors) != 1 || details.FieldErrors[0].Field != "skills_import.provider_uuid" || details.FieldErrors[0].Code != "uuid_v4_required" {
		t.Fatalf("field errors = %+v", details.FieldErrors)
	}
}

func TestOnboardingFinalizeRequestRejectsUnknownTopLevelConfigKeys(t *testing.T) {
	var req serverapi.OnboardingFinalizeRequest
	if err := json.Unmarshal([]byte(`{"compaction_mode":"bogus"}`), &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	err := serverapi.ValidateOnboardingFinalizeRequest(req)
	var finalizeErr *serverapi.OnboardingFinalizeError
	if !errors.As(err, &finalizeErr) {
		t.Fatalf("validation error = %T %v, want OnboardingFinalizeError", err, err)
	}
	details := finalizeErr.Details.(serverapi.OnboardingInvalidRequestDetails)
	if len(details.FieldErrors) != 1 || details.FieldErrors[0].Field != "compaction_mode" || details.FieldErrors[0].Code != "unknown_field" {
		t.Fatalf("field errors = %+v", details.FieldErrors)
	}
}
