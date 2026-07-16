package clientui

import (
	"encoding/json"
	"testing"

	"core/shared/transcript"
	patchformat "core/shared/transcript/patchformat"
)

func TestTranscriptDiagnosticJSONUsesNullableInactiveVariantFields(t *testing.T) {
	legacy := legacyTranscriptDiagnosticForTest("legacy_code", "legacy detail")
	legacyJSON, err := json.Marshal(legacy)
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
	developerJSON, err := json.Marshal(developerTranscriptDiagnosticForTest(developer))
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
		if err := json.Unmarshal([]byte(payload), &diagnostic); err != nil {
			t.Fatalf("decode diagnostic payload for validation: %v", err)
		}
		if err := diagnostic.Validate(); err == nil {
			t.Fatalf("validated invalid diagnostic payload: %s", payload)
		}
	}

	blank := legacyTranscriptDiagnosticForTest(" ", "detail")
	if err := blank.Validate(); err == nil {
		t.Fatal("validated blank legacy diagnostic")
	}
	mixed := developerTranscriptDiagnosticForTest(developer)
	code := TranscriptDiagnosticCode("legacy")
	detail := "detail"
	mixed.Code = &code
	mixed.Detail = &detail
	if err := mixed.Validate(); err == nil {
		t.Fatal("validated mixed legacy and developer diagnostic")
	}
}

func legacyTranscriptDiagnosticForTest(code TranscriptDiagnosticCode, detail string) *TranscriptDiagnostic {
	return &TranscriptDiagnostic{Code: &code, Detail: &detail}
}

func developerTranscriptDiagnosticForTest(diagnostic transcript.DeveloperDiagnostic) *TranscriptDiagnostic {
	return &TranscriptDiagnostic{Developer: transcript.CloneDeveloperDiagnostic(&diagnostic)}
}
