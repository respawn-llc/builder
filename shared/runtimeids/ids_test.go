package runtimeids

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func TestParseCanonicalUUIDv4RejectsNonRFCVariant(t *testing.T) {
	if _, err := ParseCanonicalUUIDv4("00000000-0000-4000-0000-000000000000", "id"); err == nil {
		t.Fatal("ParseCanonicalUUIDv4 accepted a non-RFC UUID variant")
	}
}

func TestParseCanonicalPrefixedUUIDv4RequiresExactPrefixAndCanonicalValue(t *testing.T) {
	const canonical = "7e8d24d2-8a98-4dcf-a197-6214db1cb3c0"
	parsed, err := ParseCanonicalPrefixedUUIDv4("workflow-"+canonical, "workflow-", "workflow id")
	if err != nil {
		t.Fatalf("ParseCanonicalPrefixedUUIDv4 valid id: %v", err)
	}
	if parsed.String() != canonical {
		t.Fatalf("parsed id = %q, want %q", parsed, canonical)
	}
	for _, raw := range []string{
		canonical,
		"workflowx" + canonical,
		"workflow--" + canonical,
		"workflow-" + canonical + "-extra",
		"workflow-7E8D24D2-8A98-4DCF-A197-6214DB1CB3C0",
	} {
		if _, err := ParseCanonicalPrefixedUUIDv4(raw, "workflow-", "workflow id"); err == nil {
			t.Fatalf("ParseCanonicalPrefixedUUIDv4(%q) succeeded", raw)
		}
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

func TestRuntimeUUIDIDsRoundTripAsJSONString(t *testing.T) {
	runID, err := ParseRunID("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatalf("ParseRunID: %v", err)
	}
	stepID, err := ParseStepID("22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatalf("ParseStepID: %v", err)
	}
	streamID, err := ParseAssistantStreamID("33333333-3333-4333-8333-333333333333")
	if err != nil {
		t.Fatalf("ParseAssistantStreamID: %v", err)
	}
	activityID, err := ParseBackgroundActivityID("44444444-4444-4444-8444-444444444444")
	if err != nil {
		t.Fatalf("ParseBackgroundActivityID: %v", err)
	}

	assertRuntimeUUIDJSONRoundTrip(t, NewRuntimeClientRequestID(), new(RuntimeClientRequestID))
	assertRuntimeUUIDJSONRoundTrip(t, NewQueueItemID(), new(QueueItemID))
	assertRuntimeUUIDJSONRoundTrip(t, NewLiveRunGroupID(), new(LiveRunGroupID))
	assertRuntimeUUIDJSONRoundTrip(t, runID, new(RunID))
	assertRuntimeUUIDJSONRoundTrip(t, stepID, new(StepID))
	assertRuntimeUUIDJSONRoundTrip(t, streamID, new(AssistantStreamID))
	assertRuntimeUUIDJSONRoundTrip(t, activityID, new(BackgroundActivityID))
}

func TestZeroRuntimeUUIDIDCannotMarshal(t *testing.T) {
	if _, err := json.Marshal(RuntimeClientRequestID{}); err == nil {
		t.Fatal("zero RuntimeClientRequestID marshaled successfully")
	}
}

func assertRuntimeUUIDJSONRoundTrip[T interface{ String() string }](t *testing.T, id T, decoded *T) {
	t.Helper()
	encoded, err := json.Marshal(id)
	if err != nil {
		t.Fatalf("marshal %T: %v", id, err)
	}
	if got, want := string(encoded), `"`+id.String()+`"`; got != want {
		t.Fatalf("marshal %T = %s, want %s", id, got, want)
	}
	if err := json.Unmarshal(encoded, decoded); err != nil {
		t.Fatalf("unmarshal %T: %v", id, err)
	}
	if got := (*decoded).String(); got != id.String() {
		t.Fatalf("round-trip %T = %q, want %q", id, got, id.String())
	}
}
