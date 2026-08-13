package llmerrors

import (
	"errors"
	"fmt"
	"testing"
)

func TestHasHTTPStatus(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		want   bool
	}{
		{name: "nil", err: nil, status: 400, want: false},
		{name: "provider match", err: &ProviderAPIError{StatusCode: 400}, status: 400, want: true},
		{name: "provider mismatch", err: &ProviderAPIError{StatusCode: 429}, status: 400, want: false},
		{name: "api status match", err: &APIStatusError{StatusCode: 400}, status: 400, want: true},
		{name: "api status mismatch", err: &APIStatusError{StatusCode: 500}, status: 400, want: false},
		{name: "wrapped provider", err: fmt.Errorf("send: %w", &ProviderAPIError{StatusCode: 400}), status: 400, want: true},
		{name: "joined api status", err: errors.Join(errors.New("x"), &APIStatusError{StatusCode: 400}), status: 400, want: true},
		{name: "unrelated", err: errors.New("boom"), status: 400, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasHTTPStatus(tc.err, tc.status); got != tc.want {
				t.Fatalf("HasHTTPStatus(%v, %d) = %v, want %v", tc.err, tc.status, got, tc.want)
			}
		})
	}
}

func TestUserFacingAuthenticationErrorIncludesProviderDiagnosticsWhenPresent(t *testing.T) {
	tests := []struct {
		name       string
		requestID  *string
		diagnostic *string
		suffix     string
	}{
		{name: "neither"},
		{name: "diagnostic only", diagnostic: stringPointer("token rejected"), suffix: providerAuthorizationDiagnosticPrefix + "token rejected."},
		{name: "request ID only", requestID: stringPointer("request-1"), suffix: providerRequestIDPrefix + "request-1."},
		{name: "both", requestID: stringPointer("request-1"), diagnostic: stringPointer("token rejected"), suffix: providerAuthorizationDiagnosticPrefix + "token rejected." + providerRequestIDPrefix + "request-1."},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			providerErr := &ProviderAPIError{
				ProviderID:              "chatgpt-codex",
				StatusCode:              401,
				Code:                    UnifiedErrorCodeAuthentication,
				ProviderRequestID:       test.requestID,
				AuthorizationDiagnostic: test.diagnostic,
			}
			if !IsAuthenticationError(providerErr) || !IsNonRetriableModelError(providerErr) {
				t.Fatal("provider diagnostics changed authentication or retry classification")
			}
			got := UserFacingError(providerErr)
			want := authenticationFailedWarning("chatgpt-codex", 401) + test.suffix
			if got != want {
				t.Fatalf("user-facing error = %q, want %q", got, want)
			}
		})
	}
}

func stringPointer(value string) *string {
	return &value
}
