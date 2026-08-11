package runtimeids

import (
	"bytes"
	"testing"

	"github.com/google/uuid"
)

const testGraphEntityID = "12345678-1234-4234-9234-123456789abc"

func TestNewGraphEntityIDCreatesCanonicalUUIDv4Text(t *testing.T) {
	id := NewGraphEntityID()
	if _, err := ParseCanonicalUUIDv4(id, "graph entity ID"); err != nil {
		t.Fatalf("NewGraphEntityID() = %q: %v", id, err)
	}
}

func TestGraphEntityIDTextAndBytesRoundTripExactly(t *testing.T) {
	blob, err := GraphEntityIDBlob(testGraphEntityID)
	if err != nil {
		t.Fatalf("GraphEntityIDBlob: %v", err)
	}
	expected := uuid.MustParse(testGraphEntityID)
	if !bytes.Equal(blob, expected[:]) {
		t.Fatalf("GraphEntityIDBlob() = %x, want %x", blob, expected[:])
	}

	text, err := GraphEntityIDText(blob)
	if err != nil {
		t.Fatalf("GraphEntityIDText: %v", err)
	}
	if text != testGraphEntityID {
		t.Fatalf("GraphEntityIDText() = %q, want %q", text, testGraphEntityID)
	}
}

func TestGraphEntityIDBlobRejectsInvalidText(t *testing.T) {
	for name, raw := range map[string]string{
		"blank":         "",
		"padded":        " " + testGraphEntityID,
		"noncanonical":  "12345678-1234-4234-9234-123456789ABC",
		"non-v4":        "12345678-1234-1234-9234-123456789abc",
		"wrong variant": "12345678-1234-4234-1234-123456789abc",
		"zero":          "00000000-0000-0000-0000-000000000000",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := GraphEntityIDBlob(raw); err == nil {
				t.Fatalf("GraphEntityIDBlob(%q) succeeded", raw)
			}
		})
	}
}

func TestGraphEntityIDTextRejectsMalformedBlobs(t *testing.T) {
	for name, raw := range map[string][]byte{
		"nil":           nil,
		"short":         {1, 2, 3},
		"text bytes":    []byte(testGraphEntityID),
		"non-v4":        graphUUIDBytes("12345678-1234-1234-9234-123456789abc"),
		"wrong variant": graphUUIDBytes("12345678-1234-4234-1234-123456789abc"),
		"zero":          make([]byte, 16),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := GraphEntityIDText(raw); err == nil {
				t.Fatalf("GraphEntityIDText(%x) succeeded", raw)
			}
		})
	}
}

func TestMigrateGraphEntityIDBlobPreservesCanonicalAndRemapsLegacyText(t *testing.T) {
	canonical, err := MigrateGraphEntityIDBlob(testGraphEntityID)
	if err != nil {
		t.Fatalf("MigrateGraphEntityIDBlob canonical: %v", err)
	}
	wantCanonical, err := GraphEntityIDBlob(testGraphEntityID)
	if err != nil {
		t.Fatalf("GraphEntityIDBlob canonical: %v", err)
	}
	if !bytes.Equal(canonical, wantCanonical) {
		t.Fatalf("canonical migration = %x, want %x", canonical, wantCanonical)
	}

	legacy, err := MigrateGraphEntityIDBlob("node-legacy")
	if err != nil {
		t.Fatalf("MigrateGraphEntityIDBlob legacy: %v", err)
	}
	if _, err := GraphEntityIDText(legacy); err != nil {
		t.Fatalf("legacy migration = %x: %v", legacy, err)
	}
}

func TestMigrateGraphEntityIDBlobRejectsBlankAndZeroText(t *testing.T) {
	for name, raw := range map[string]string{
		"blank":             "",
		"whitespace":        " ",
		"canonical zero":    "00000000-0000-0000-0000-000000000000",
		"noncanonical zero": "00000000000000000000000000000000",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := MigrateGraphEntityIDBlob(raw); err == nil {
				t.Fatalf("MigrateGraphEntityIDBlob(%q) succeeded", raw)
			}
		})
	}
}

func graphUUIDBytes(raw string) []byte {
	value := uuid.MustParse(raw)
	return value[:]
}
