package serverapi_test

import (
	"errors"
	"testing"

	"core/shared/serverapi"
	"core/shared/toolspec"
)

func TestOnboardingFinalizeRequestRejectsEmptyMainProviderValues(t *testing.T) {
	empty := ""
	req := serverapi.OnboardingFinalizeRequest{
		MainProvider: &serverapi.OnboardingProviderChoice{
			ProviderOverride: &empty,
			OpenAIBaseURL:    &empty,
		},
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

func TestOnboardingFinalizeRequestRejectsInvalidToolOverrides(t *testing.T) {
	req := serverapi.OnboardingFinalizeRequest{ToolOverrides: []serverapi.OnboardingToolOverride{
		{ID: toolspec.ID("shell")},
		{ID: toolspec.ToolAskQuestion},
		{ID: toolspec.ToolPatch, Enabled: true},
		{ID: toolspec.ToolPatch},
	}}
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
