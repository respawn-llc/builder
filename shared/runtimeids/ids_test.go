package runtimeids

import "testing"

func TestParseCanonicalUUIDv4RejectsNonRFCVariant(t *testing.T) {
	if _, err := ParseCanonicalUUIDv4("00000000-0000-4000-0000-000000000000", "id"); err == nil {
		t.Fatal("ParseCanonicalUUIDv4 accepted a non-RFC UUID variant")
	}
}
