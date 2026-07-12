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

func TestOnboardingFinalizeRequestSortsUnknownTopLevelConfigKeys(t *testing.T) {
	var req serverapi.OnboardingFinalizeRequest
	if err := json.Unmarshal([]byte(`{"z_unknown":true,"a_unknown":true}`), &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	err := serverapi.ValidateOnboardingFinalizeRequest(req)
	var finalizeErr *serverapi.OnboardingFinalizeError
	if !errors.As(err, &finalizeErr) {
		t.Fatalf("validation error = %T %v, want OnboardingFinalizeError", err, err)
	}
	details := finalizeErr.Details.(serverapi.OnboardingInvalidRequestDetails)
	if len(details.FieldErrors) != 2 {
		t.Fatalf("field errors = %+v, want 2", details.FieldErrors)
	}
	if details.FieldErrors[0].Field != "a_unknown" || details.FieldErrors[1].Field != "z_unknown" {
		t.Fatalf("field errors = %+v, want sorted unknown fields", details.FieldErrors)
	}
}

func TestOnboardingFinalizeRequestRejectsEmptyMainProviderValues(t *testing.T) {
	var req serverapi.OnboardingFinalizeRequest
	if err := json.Unmarshal([]byte(`{"main_provider":{"provider_override":"","openai_base_url":""}}`), &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	err := serverapi.ValidateOnboardingFinalizeRequest(req)
	var finalizeErr *serverapi.OnboardingFinalizeError
	if !errors.As(err, &finalizeErr) {
		t.Fatalf("validation error = %T %v, want OnboardingFinalizeError", err, err)
	}
	details := finalizeErr.Details.(serverapi.OnboardingInvalidRequestDetails)
	if len(details.FieldErrors) != 2 || details.FieldErrors[0].Field != "main_provider.provider_override" || details.FieldErrors[1].Field != "main_provider.openai_base_url" {
		t.Fatalf("field errors = %+v", details.FieldErrors)
	}
}
