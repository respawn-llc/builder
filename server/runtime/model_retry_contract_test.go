package runtime

import (
	"context"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/tools"
)

func TestGenerateWithRetryRejectsNonRetriableModelErrorsWithoutRetry(t *testing.T) {
	tests := []struct {
		name           string
		cause          error
		assertCategory func(*testing.T, error)
	}{
		{
			name:  "authentication",
			cause: &llm.AuthError{Err: errors.New("authentication unavailable")},
			assertCategory: func(t *testing.T, err error) {
				t.Helper()
				if !llm.IsAuthenticationError(err) {
					t.Fatalf("error is not classified as authentication: %T", err)
				}
			},
		},
		{
			name:  "status-400",
			cause: &llm.APIStatusError{StatusCode: 400},
			assertCategory: func(t *testing.T, err error) {
				t.Helper()
				if !llm.HasHTTPStatus(err, 400) {
					t.Fatalf("error does not retain HTTP status 400: %T", err)
				}
			},
		},
		{
			name:  "status-401",
			cause: &llm.APIStatusError{StatusCode: 401},
			assertCategory: func(t *testing.T, err error) {
				t.Helper()
				if !llm.HasHTTPStatus(err, 401) {
					t.Fatalf("error does not retain HTTP status 401: %T", err)
				}
			},
		},
		{
			name:  "status-403",
			cause: &llm.APIStatusError{StatusCode: 403},
			assertCategory: func(t *testing.T, err error) {
				t.Helper()
				if !llm.HasHTTPStatus(err, 403) {
					t.Fatalf("error does not retain HTTP status 403: %T", err)
				}
			},
		},
		{
			name:  "status-404",
			cause: &llm.APIStatusError{StatusCode: 404},
			assertCategory: func(t *testing.T, err error) {
				t.Helper()
				if !llm.HasHTTPStatus(err, 404) {
					t.Fatalf("error does not retain HTTP status 404: %T", err)
				}
			},
		},
		{
			name: "provider-contract",
			cause: &llm.ProviderAPIError{
				Code: llm.UnifiedErrorCodeProviderContract,
			},
			assertCategory: func(t *testing.T, err error) {
				t.Helper()
				var providerErr *llm.ProviderAPIError
				if !errors.As(err, &providerErr) ||
					providerErr.Code != llm.UnifiedErrorCodeProviderContract {
					t.Fatalf("error does not retain provider-contract category: %T", err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeClient{errors: []error{test.cause}}
			engine := mustNewTestEngine(
				t,
				mustCreateTestSession(t),
				client,
				tools.NewRegistry(),
				Config{Model: "gpt-5"},
			)

			_, err := engine.generateWithRetryClient(
				context.Background(),
				"",
				client,
				llm.Request{Model: "gpt-5", ToolChoiceMode: llm.ToolChoiceModeAutomatic},
				nil,
				nil,
				nil,
			)
			if !errors.Is(err, test.cause) {
				t.Fatalf("generation error = %v, want original typed cause", err)
			}
			if !llm.IsNonRetriableModelError(err) {
				t.Fatalf("generation error is retriable: %T", err)
			}
			test.assertCategory(t, err)
			if calls := len(client.calls); calls != 1 {
				t.Fatalf("provider dispatches = %d, want one", calls)
			}
		})
	}
}
