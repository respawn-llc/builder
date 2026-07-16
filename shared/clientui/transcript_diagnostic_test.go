package clientui

import (
	"encoding/json"
	"testing"

	"core/shared/transcript"
	patchformat "core/shared/transcript/patchformat"
)

func TestTranscriptDiagnosticJSONUsesNullableInactiveVariantFields(t *testing.T) {
	legacyJSON, err := json.Marshal(NewLegacyTranscriptDiagnostic("legacy_code", "legacy detail"))
	if err != nil {
		t.Fatalf("marshal legacy diagnostic: %v", err)
	}
	if string(legacyJSON) != `{"Code":"legacy_code","Detail":"legacy detail","Developer":null}` {
		t.Fatalf("legacy diagnostic JSON = %s", legacyJSON)
	}

	developer := transcript.NewDeletionFactMismatchDeveloperDiagnostic(
		"call-1",
		patchformat.WholeFileDeletionFactMismatchError{
			Kind: patchformat.WholeFileDeletionFactMismatchMissing,
			ID:   patchformat.WholeFileDeletionOperationID{HunkOrdinal: 0},
		},
	)
	developerJSON, err := json.Marshal(NewDeveloperTranscriptDiagnostic(developer))
	if err != nil {
		t.Fatalf("marshal developer diagnostic: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(developerJSON, &wire); err != nil {
		t.Fatalf("decode developer wire shape: %v", err)
	}
	if string(wire["Code"]) != "null" || string(wire["Detail"]) != "null" {
		t.Fatalf("developer diagnostic leaked legacy fields: %s", developerJSON)
	}
}

func TestTranscriptDiagnosticJSONRejectsMissingPartialBlankAndMixedVariants(t *testing.T) {
	developer := transcript.NewDeletionFactMismatchDeveloperDiagnostic(
		"call-1",
		patchformat.WholeFileDeletionFactMismatchError{
			Kind: patchformat.WholeFileDeletionFactMismatchMissing,
			ID:   patchformat.WholeFileDeletionOperationID{HunkOrdinal: 0},
		},
	)
	for _, payload := range []string{
		`{"Code":null,"Detail":null,"Developer":null}`,
		`{"Code":"legacy","Detail":null,"Developer":null}`,
		`{"Code":null,"Detail":"detail","Developer":null}`,
		`{"Code":"","Detail":"","Developer":null}`,
		`{"Code":"legacy","Detail":"detail","Developer":{"deletion_fact_mismatch":{"call_id":"call-1","operation_id":{"HunkOrdinal":0},"mismatch_kind":"missing"}}}`,
	} {
		var diagnostic TranscriptDiagnostic
		if err := json.Unmarshal([]byte(payload), &diagnostic); err == nil {
			t.Fatalf("decoded invalid diagnostic payload: %s", payload)
		}
	}

	blank := NewLegacyTranscriptDiagnostic(" ", "detail")
	if _, err := json.Marshal(blank); err == nil {
		t.Fatal("encoded blank legacy diagnostic")
	}
	mixed := NewDeveloperTranscriptDiagnostic(developer)
	code := TranscriptDiagnosticCode("legacy")
	detail := "detail"
	mixed.Code = &code
	mixed.Detail = &detail
	if _, err := json.Marshal(mixed); err == nil {
		t.Fatal("encoded mixed legacy and developer diagnostic")
	}
}
