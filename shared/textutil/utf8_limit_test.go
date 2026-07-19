package textutil

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLimitUTF8BytesPreservesRuneBoundaries(t *testing.T) {
	limited, truncated, err := LimitUTF8Bytes(strings.Repeat("界", 3), 8)
	if err != nil {
		t.Fatalf("limit UTF-8 bytes: %v", err)
	}
	if !truncated || limited != "界界" || !utf8.ValidString(limited) {
		t.Fatalf("limited value = %q truncated=%t", limited, truncated)
	}
}

func TestValidateUTF8ByteLimitRejectsInvalidOrOversizedValues(t *testing.T) {
	if err := ValidateUTF8ByteLimit(string([]byte{0xff}), 8); err == nil {
		t.Fatal("invalid UTF-8 value was accepted")
	}
	if err := ValidateUTF8ByteLimit("oversized", 4); err == nil {
		t.Fatal("oversized value was accepted")
	}
}
