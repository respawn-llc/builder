package protocol

import (
	"strings"
	"testing"
)

func TestNewSentinelErrorRejectsMissingSentinelAndMessage(t *testing.T) {
	err := NewSentinelError(nil, " \t")
	if err == nil || strings.TrimSpace(err.Error()) == "" {
		t.Fatalf("error = %v, want a non-empty validation error", err)
	}
}
