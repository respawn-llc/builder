package worktreecontract

import "testing"

func TestValidateDirtyState(t *testing.T) {
	count := 2
	cause := "status failed"
	tests := []struct {
		name         string
		kind         DirtyStateKind
		dirtyCount   *int
		unknownCause *string
		wantValid    bool
	}{
		{name: "clean", kind: DirtyStateClean, wantValid: true},
		{name: "clean payload", kind: DirtyStateClean, dirtyCount: &count, wantValid: false},
		{name: "dirty", kind: DirtyStateDirty, dirtyCount: &count, wantValid: true},
		{name: "dirty missing count", kind: DirtyStateDirty, wantValid: false},
		{name: "unknown", kind: DirtyStateUnknown, unknownCause: &cause, wantValid: true},
		{name: "unknown missing cause", kind: DirtyStateUnknown, wantValid: false},
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
		transition TransitionKind
		kind       DirtyStateKind
		count      *int
		wantValid  bool
	}{
		{name: "delete dirty", transition: TransitionDelete, kind: DirtyStateDirty, count: &count, wantValid: true},
		{name: "non-delete dirty", transition: TransitionKind("leave"), kind: DirtyStateDirty, count: &count, wantValid: false},
		{name: "delete clean", transition: TransitionDelete, kind: DirtyStateClean, wantValid: false},
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
