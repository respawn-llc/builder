package worktreecontract

import "testing"

type testDirtyStateKind string
type testTransitionKind string

func TestValidateDirtyState(t *testing.T) {
	count := 2
	cause := "status failed"
	tests := []struct {
		name         string
		kind         testDirtyStateKind
		dirtyCount   *int
		unknownCause *string
		wantValid    bool
	}{
		{name: "clean", kind: "clean", wantValid: true},
		{name: "clean payload", kind: "clean", dirtyCount: &count, wantValid: false},
		{name: "dirty", kind: "dirty", dirtyCount: &count, wantValid: true},
		{name: "dirty missing count", kind: "dirty", wantValid: false},
		{name: "unknown", kind: "unknown", unknownCause: &cause, wantValid: true},
		{name: "unknown missing cause", kind: "unknown", wantValid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateDirtyState(test.kind, test.dirtyCount, test.unknownCause)
			if (err == nil) != test.wantValid {
				t.Fatalf("ValidateDirtyState() error = %v, want valid=%t", err, test.wantValid)
			}
		})
	}
}

func TestValidateDeleteTransitionPreconditionOwnsApplicabilityAndCleanliness(t *testing.T) {
	count := 1
	tests := []struct {
		name       string
		transition testTransitionKind
		kind       testDirtyStateKind
		count      *int
		wantValid  bool
	}{
		{name: "delete dirty", transition: "delete", kind: "dirty", count: &count, wantValid: true},
		{name: "non-delete dirty", transition: "leave", kind: "dirty", count: &count, wantValid: false},
		{name: "delete clean", transition: "delete", kind: "clean", wantValid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateDeleteTransitionPrecondition(test.transition, test.kind, test.count, nil)
			if (err == nil) != test.wantValid {
				t.Fatalf("ValidateDeleteTransitionPrecondition() error = %v, want valid=%t", err, test.wantValid)
			}
		})
	}
}
