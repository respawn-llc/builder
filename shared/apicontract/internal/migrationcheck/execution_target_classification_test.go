package migrationcheck

import (
	"errors"
	"testing"
)

func TestExecutionTargetDeclarationsMatchExplicitDomainSignoffs(t *testing.T) {
	report, err := InspectExecutionTarget()
	if err != nil {
		t.Fatal(err)
	}
	signoffs := ExecutionTargetDomainSignoffs()
	classification, err := MergeDomainDeclarationSignoffs(signoffs)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckDeclarationClassifications(
		DeclarationReport{
			NamedScalars: report.NamedScalars,
			Validators:   report.Validators,
		},
		classification,
	); err != nil {
		t.Fatal(err)
	}
	assertDurationSignoff(t, signoffs)
}

func TestExecutionTargetDomainSignoffsAssignEachIdentityOnce(t *testing.T) {
	_, err := MergeDomainDeclarationSignoffs([]DomainDeclarationSignoff{
		{
			Domain: "first",
			Classification: DeclarationClassification{
				Scalars: []ScalarClassification{{
					Identity: typeIdentity("fixture", "Scalar"),
					Kind:     ScalarIdentifier,
				}},
			},
		},
		{
			Domain: "second",
			Classification: DeclarationClassification{
				Scalars: []ScalarClassification{{
					Identity: typeIdentity("fixture", "Scalar"),
					Kind:     ScalarIdentifier,
				}},
			},
		},
	})
	if err == nil {
		t.Fatal("duplicate cross-domain classification unexpectedly succeeded")
	}
	var mergeError *DomainSignoffError
	if !errors.As(err, &mergeError) {
		t.Fatalf("error type = %T, want *DomainSignoffError", err)
	}
}

func TestExecutionTargetDomainSignoffsAreDeterministic(t *testing.T) {
	first, err := MergeDomainDeclarationSignoffs(ExecutionTargetDomainSignoffs())
	if err != nil {
		t.Fatal(err)
	}
	second, err := MergeDomainDeclarationSignoffs(ExecutionTargetDomainSignoffs())
	if err != nil {
		t.Fatal(err)
	}
	if declarationClassificationFingerprint(first) != declarationClassificationFingerprint(second) {
		t.Fatal("explicit execution-target classifications are nondeterministic")
	}
}

func assertDurationSignoff(t *testing.T, signoffs []DomainDeclarationSignoff) {
	t.Helper()
	wantIdentity := typeIdentity("time", "Duration")
	var matches int
	var scalarMatches int
	for _, signoff := range signoffs {
		for _, scalar := range signoff.Classification.Scalars {
			if scalar.Identity != wantIdentity {
				continue
			}
			scalarMatches++
			if scalar.Kind != ScalarProtobufDuration {
				t.Fatalf("time.Duration classification = %q", scalar.Kind)
			}
			if len(scalar.EnumMembers) != 0 {
				t.Fatalf("time.Duration enum members = %+v", scalar.EnumMembers)
			}
		}
		for _, wellKnown := range signoff.WellKnownScalars {
			if wellKnown.Identity != wantIdentity {
				continue
			}
			matches++
			if wellKnown.Mapping != WellKnownProtobufDuration {
				t.Fatalf("time.Duration mapping = %q", wellKnown.Mapping)
			}
		}
	}
	if matches != 1 {
		t.Fatalf("time.Duration well-known sign-offs = %d, want 1", matches)
	}
	if scalarMatches != 1 {
		t.Fatalf("time.Duration scalar classifications = %d, want 1", scalarMatches)
	}
}
