package runtimeids

import (
	"errors"
	"testing"
)

func TestParseProjectKeyOwnsCanonicalGrammar(t *testing.T) {
	for raw, want := range map[string]string{
		" kent ":   "KENT",
		"K2":       "K2",
		"AB123456": "AB123456",
	} {
		t.Run(raw, func(t *testing.T) {
			key, err := ParseProjectKey(raw)
			if err != nil {
				t.Fatalf("ParseProjectKey(%q): %v", raw, err)
			}
			if got := key.String(); got != want {
				t.Fatalf("ParseProjectKey(%q) = %q, want %q", raw, got, want)
			}
		})
	}

	for _, raw := range []string{"", "A", "1A", "BAD-KEY", "ABCDEFGHI"} {
		t.Run("rejects_"+raw, func(t *testing.T) {
			if _, err := ParseProjectKey(raw); !errors.Is(err, ErrInvalidProjectKey) {
				t.Fatalf("ParseProjectKey(%q) error = %v, want ErrInvalidProjectKey", raw, err)
			}
		})
	}
}
