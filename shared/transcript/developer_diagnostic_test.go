package transcript

import (
	"reflect"
	"testing"

	patchformat "core/shared/transcript/patchformat"
)

func TestDeletionFactMismatchDeveloperDiagnosticOwnsRequiredTypedContext(t *testing.T) {
	mismatch := &patchformat.WholeFileDeletionFactMismatchError{
		Kind: patchformat.WholeFileDeletionFactMismatchMissing,
		ID:   patchformat.WholeFileDeletionOperationID{HunkOrdinal: 2},
	}
	diagnostic := NewDeletionFactMismatchDeveloperDiagnostic("call-1", *mismatch)

	if err := diagnostic.Validate(); err != nil {
		t.Fatalf("validate diagnostic: %v", err)
	}
	kind, present := diagnostic.Kind()
	if !present || kind != DeveloperDiagnosticDeletionFactMismatch {
		t.Fatalf("diagnostic kind = %q present=%t", kind, present)
	}
	context := diagnostic.DeletionFactMismatch
	if context == nil ||
		context.CallID != "call-1" ||
		context.OperationID != mismatch.ID ||
		context.MismatchKind != mismatch.Kind {
		t.Fatalf("diagnostic context = %+v, want mismatch %+v", context, mismatch)
	}
	if DeveloperDiagnosticText(diagnostic) == "" {
		t.Fatal("typed diagnostic did not derive display text")
	}
}

func TestDeveloperDiagnosticKindReportsMissingVariantExplicitly(t *testing.T) {
	kind, present := (DeveloperDiagnostic{}).Kind()
	if present {
		t.Fatalf("missing diagnostic variant reported kind %q", kind)
	}
}

func TestDeveloperDiagnosticRejectsMissingOrInvalidVariantContext(t *testing.T) {
	tests := []DeveloperDiagnostic{
		{},
		{DeletionFactMismatch: &DeletionFactMismatchDeveloperDiagnostic{}},
		{DeletionFactMismatch: &DeletionFactMismatchDeveloperDiagnostic{
			CallID:       "call-1",
			OperationID:  patchformat.WholeFileDeletionOperationID{HunkOrdinal: -1},
			MismatchKind: patchformat.WholeFileDeletionFactMismatchMissing,
		}},
		{DeletionFactMismatch: &DeletionFactMismatchDeveloperDiagnostic{
			CallID:       "call-1",
			OperationID:  patchformat.WholeFileDeletionOperationID{HunkOrdinal: 0},
			MismatchKind: patchformat.WholeFileDeletionFactMismatchKind("invalid"),
		}},
	}
	for index, diagnostic := range tests {
		if err := diagnostic.Validate(); err == nil {
			t.Fatalf("diagnostic %d unexpectedly validated: %+v", index, diagnostic)
		}
	}
}

func TestDeveloperDiagnosticDoesNotDuplicateTypedFactsAsParallelFields(t *testing.T) {
	typ := reflect.TypeFor[DeveloperDiagnostic]()
	for _, field := range []string{"CallID", "OperationID", "MismatchKind", "Detail"} {
		if _, exists := typ.FieldByName(field); exists {
			t.Fatalf("developer diagnostic duplicates variant fact in field %q", field)
		}
	}
}

func TestDeveloperDiagnosticEqualityUsesVariantValues(t *testing.T) {
	mismatch := &patchformat.WholeFileDeletionFactMismatchError{
		Kind: patchformat.WholeFileDeletionFactMismatchMissing,
		ID:   patchformat.WholeFileDeletionOperationID{HunkOrdinal: 2},
	}
	left := NewDeletionFactMismatchDeveloperDiagnostic("call-1", *mismatch)
	right := NewDeletionFactMismatchDeveloperDiagnostic("call-1", *mismatch)
	if !DeveloperDiagnosticEqual(&left, &right) {
		t.Fatal("equal diagnostic variants were not equal")
	}
	right.DeletionFactMismatch.CallID = "call-2"
	if DeveloperDiagnosticEqual(&left, &right) {
		t.Fatal("different diagnostic variants were equal")
	}
}
