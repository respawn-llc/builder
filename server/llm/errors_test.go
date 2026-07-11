package llm

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"core/server/auth"
)

func TestIsAuthenticationError(t *testing.T) {
	if !IsAuthenticationError(&ProviderAPIError{ProviderID: "openai", StatusCode: 401, Code: UnifiedErrorCodeAuthentication}) {
		t.Fatal("expected provider authentication code to be auth error")
	}
	if !IsAuthenticationError(&APIStatusError{StatusCode: 401, Body: "unauthorized"}) {
		t.Fatal("expected 401 to be auth error")
	}
	if !IsAuthenticationError(&APIStatusError{StatusCode: 403, Body: "forbidden"}) {
		t.Fatal("expected 403 to be auth error")
	}
	if IsAuthenticationError(&APIStatusError{StatusCode: 429, Body: "rate limit"}) {
		t.Fatal("did not expect 429 to be auth error")
	}
	if !IsAuthenticationError(&AuthError{Err: errors.New("token refresh failed")}) {
		t.Fatal("expected AuthError to be auth error")
	}
}

func TestIsNonRetriableModelError(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404} {
		if !IsNonRetriableModelError(&APIStatusError{StatusCode: status, Body: "x"}) {
			t.Fatalf("expected %d to be non-retriable", status)
		}
	}
	for _, status := range []int{408, 409, 429, 500} {
		if IsNonRetriableModelError(&APIStatusError{StatusCode: status, Body: "x"}) {
			t.Fatalf("did not expect %d to be non-retriable", status)
		}
	}
	if !IsNonRetriableModelError(&ProviderAPIError{ProviderID: "openai", StatusCode: 0, Code: UnifiedErrorCodeProviderContract, Message: "unknown provider contract"}) {
		t.Fatal("expected provider contract error to be non-retriable")
	}
	if !IsNonRetriableModelError(&ProviderAPIError{ProviderID: "openai", StatusCode: http.StatusOK, Code: UnifiedErrorCodeUnknown, ProviderType: "response.incomplete", ProviderCode: "max_output_tokens"}) {
		t.Fatal("expected response.incomplete terminal error to be non-retriable")
	}
	if !IsNonRetriableModelError(&AuthError{Err: errors.New("token refresh failed")}) {
		t.Fatal("expected AuthError to be non-retriable")
	}
	if !IsNonRetriableModelError(&ProviderSelectionError{Model: "my-model", Err: ErrUnsupportedProvider}) {
		t.Fatal("expected provider selection error to be non-retriable")
	}
}

func TestIsContextLengthOverflowError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "provider unified overflow code",
			err:  &ProviderAPIError{ProviderID: "openai", StatusCode: 400, Code: UnifiedErrorCodeContextLengthOverflow, ProviderCode: "context_length_exceeded"},
			want: true,
		},
		{
			name: "413 overflow code",
			err:  &ProviderAPIError{ProviderID: "openai", StatusCode: 413, Code: UnifiedErrorCodeContextLengthOverflow, ProviderCode: "context_length_exceeded"},
			want: true,
		},
		{
			name: "wrapped overflow error",
			err:  fmt.Errorf("compact failed: %w", &ProviderAPIError{ProviderID: "openai", StatusCode: 422, Code: UnifiedErrorCodeContextLengthOverflow, ProviderCode: "input_too_long"}),
			want: true,
		},
		{
			name: "provider unknown code",
			err:  &ProviderAPIError{ProviderID: "openai", StatusCode: 400, Code: UnifiedErrorCodeUnknown, ProviderCode: "invalid_tool_arguments"},
			want: false,
		},
		{
			name: "legacy api status error is not overflow typed",
			err: &APIStatusError{
				StatusCode: 400,
				Body:       `{"error":{"code":"context_length_exceeded"}}`,
			},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsContextLengthOverflowError(tc.err); got != tc.want {
				t.Fatalf("IsContextLengthOverflowError() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUserFacingError(t *testing.T) {
	if got := UserFacingError(&ProviderSelectionError{Model: "my-model", Err: ErrUnsupportedProvider}); got == "" || !containsAll(got, []string{"provider/auth path", "provider_override", "openai_base_url"}) {
		t.Fatalf("expected provider selection warning, got %q", got)
	}
	if got := UserFacingError(&AuthError{Err: auth.ErrAuthNotConfigured}); got != "Not authenticated, run /login to sign in with your provider" {
		t.Fatalf("expected unauthenticated warning, got %q", got)
	}
	if got := UserFacingError(&ProviderAPIError{ProviderID: "openai-compatible", StatusCode: 401, Code: UnifiedErrorCodeAuthentication}); got == "" || !containsAll(got, []string{"401", "/login", "OPENAI_API_KEY"}) {
		t.Fatalf("expected authentication failure warning, got %q", got)
	}
}

func containsAll(text string, parts []string) bool {
	for _, part := range parts {
		if !strings.Contains(text, part) {
			return false
		}
	}
	return true
}
