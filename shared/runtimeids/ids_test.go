package runtimeids

import (
	"testing"

	"github.com/google/uuid"
)

func TestParseCanonicalUUIDv4RejectsNonRFCVariant(t *testing.T) {
	if _, err := ParseCanonicalUUIDv4("00000000-0000-4000-0000-000000000000", "id"); err == nil {
		t.Fatal("ParseCanonicalUUIDv4 accepted a non-RFC UUID variant")
	}
}

func TestParseSessionIDAcceptsCanonicalUUIDv4AndSupportedLegacyIDs(t *testing.T) {
	for _, raw := range []string{
		"7fd3bc93-f11c-4814-87d0-b60f10e6dd5c",
		"session-1",
		"legacy_session.2024",
	} {
		t.Run(raw, func(t *testing.T) {
			id, err := ParseSessionID(raw)
			if err != nil {
				t.Fatalf("ParseSessionID(%q): %v", raw, err)
			}
			if got := id.String(); got != raw {
				t.Fatalf("ParseSessionID(%q).String() = %q", raw, got)
			}
		})
	}
}

func TestParseSessionIDRejectsEmptyPaddedAndPathEscapingValues(t *testing.T) {
	for _, raw := range []string{
		"",
		" ",
		" session-1",
		"session-1 ",
		".",
		"..",
		"../session-1",
		"nested/session-1",
		`nested\session-1`,
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := ParseSessionID(raw); err == nil {
				t.Fatalf("ParseSessionID(%q) succeeded", raw)
			}
		})
	}
}

func TestNewSessionIDIsCanonicalUUIDv4(t *testing.T) {
	id := NewSessionID()
	parsed, err := uuid.Parse(id.String())
	if err != nil {
		t.Fatalf("parse generated session ID: %v", err)
	}
	if parsed.String() != id.String() || parsed.Version() != 4 || parsed.Variant() != uuid.RFC4122 {
		t.Fatalf("generated session ID = %q, want canonical RFC4122 UUIDv4", id.String())
	}
}

func TestSessionIDTracksCanonicalUUIDv4WithoutRejectingLegacyIDs(t *testing.T) {
	canonical, err := ParseSessionID("7fd3bc93-f11c-4814-87d0-b60f10e6dd5c")
	if err != nil {
		t.Fatalf("ParseSessionID canonical: %v", err)
	}
	if !canonical.IsCanonicalUUIDv4() {
		t.Fatal("canonical UUIDv4 session ID was not marked canonical")
	}
	legacy, err := ParseSessionID("session-1")
	if err != nil {
		t.Fatalf("ParseSessionID legacy: %v", err)
	}
	if legacy.IsCanonicalUUIDv4() {
		t.Fatal("legacy session ID was marked canonical UUIDv4")
	}
}
