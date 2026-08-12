package serverapi_test

import (
	"errors"
	"testing"

	"core/shared/jsoncontract"
	"core/shared/serverapi"
	"core/shared/serverjsoncontract"
)

func TestOnboardingFinalizeRequestContractValidatesStructuralShapes(t *testing.T) {
	contract, err := serverjsoncontract.PrepareOnboardingFinalizeRequest(jsoncontract.NewPreparer(false))
	if err != nil {
		t.Fatalf("prepare onboarding request contract: %v", err)
	}
	for _, raw := range []string{
		`{}`,
		`{"theme":null}`,
		`{"skills_import":null}`,
		`{"skills_import":{"mode":"none","provider_uuid":null}}`,
	} {
		if _, err := contract.Decode([]byte(raw)); err != nil {
			t.Fatalf("onboarding request contract rejected %s: %v", raw, err)
		}
	}
	for _, raw := range []string{
		`null`,
		`{"skills_import":{}}`,
		`{"skills_import":{"mode":"none","provider_uuid":7}}`,
		`{"skills_import":{"mode":"none","extra":true}}`,
		`{"unknown":true}`,
		`{"theme":"auto"} {}`,
	} {
		if _, err := contract.Decode([]byte(raw)); err == nil {
			t.Fatalf("onboarding request contract accepted %s", raw)
		}
	}
}

func TestOnboardingFinalizeRequestDomainValidationOwnsMalformedProviderUUID(t *testing.T) {
	contract, err := serverjsoncontract.PrepareOnboardingFinalizeRequest(jsoncontract.NewPreparer(false))
	if err != nil {
		t.Fatalf("prepare onboarding request contract: %v", err)
	}
	req, err := contract.Decode([]byte(`{"skills_import":{"mode":"symlink_source","provider_uuid":"not-a-uuid"}}`))
	if err != nil {
		t.Fatalf("decode request: %v", err)
	}
	err = serverapi.ValidateOnboardingFinalizeRequest(req)
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
	contract, err := serverjsoncontract.PrepareOnboardingFinalizeRequest(jsoncontract.NewPreparer(false))
	if err != nil {
		t.Fatalf("prepare onboarding request contract: %v", err)
	}
	if _, err := contract.Decode([]byte(`{"compaction_mode":"bogus"}`)); err == nil {
		t.Fatal("onboarding request contract accepted an unknown field")
	}
}

func TestOnboardingFinalizeRequestRejectsEmptyMainProviderValues(t *testing.T) {
	req := decodeOnboardingFinalizeRequest(t, `{"main_provider":{"provider_override":"","openai_base_url":""}}`)
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

func TestOnboardingFinalizeRequestRejectsInvalidToolOverrides(t *testing.T) {
	req := decodeOnboardingFinalizeRequest(t, `{"tool_overrides":[{"id":"shell","enabled":false},{"id":"ask_question","enabled":false},{"id":"patch","enabled":true},{"id":"patch","enabled":false}]}`)
	err := serverapi.ValidateOnboardingFinalizeRequest(req)
	var finalizeErr *serverapi.OnboardingFinalizeError
	if !errors.As(err, &finalizeErr) {
		t.Fatalf("validation error = %T %v, want OnboardingFinalizeError", err, err)
	}
	details := finalizeErr.Details.(serverapi.OnboardingInvalidRequestDetails)
	if len(details.FieldErrors) != 3 ||
		details.FieldErrors[0].Field != "tool_overrides.0.id" || details.FieldErrors[0].Code != "unsupported_value" ||
		details.FieldErrors[1].Field != "tool_overrides.1.id" || details.FieldErrors[1].Code != "forbidden" ||
		details.FieldErrors[2].Field != "tool_overrides.3.id" || details.FieldErrors[2].Code != "duplicate" {
		t.Fatalf("field errors = %+v", details.FieldErrors)
	}
}

func decodeOnboardingFinalizeRequest(t *testing.T, raw string) serverapi.OnboardingFinalizeRequest {
	t.Helper()
	contract, err := serverjsoncontract.PrepareOnboardingFinalizeRequest(jsoncontract.NewPreparer(false))
	if err != nil {
		t.Fatalf("prepare onboarding request contract: %v", err)
	}
	request, err := contract.Decode([]byte(raw))
	if err != nil {
		t.Fatalf("decode onboarding request: %v", err)
	}
	return request
}
