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
