package migrationcheck

import (
	"reflect"
	"testing"
)

type exceptionalFingerprintFixture struct {
	Value string
}

func TestExceptionalWireFingerprintUsesImmutableReviewedBaseline(t *testing.T) {
	reviewed := []WireException{{
		LegacyType:            reflect.TypeFor[exceptionalFingerprintFixture](),
		Message:               "kent.api.fixture.Exceptional",
		LegacyFingerprint:     "reviewed-legacy",
		DescriptorFingerprint: "reviewed-descriptor",
	}}
	const reviewedFingerprint = "cc6bb7759d885a4f05720ddb7e6e8a48da3ad15668edbc804149baca5dea0aa6"

	issues := make([]CoverageIssue, 0)
	checkAggregateFingerprint(
		"exceptional wire coverage",
		reviewedFingerprint,
		fingerprintWireExceptions(reviewed),
		&issues,
	)
	if len(issues) != 0 {
		t.Fatalf("reviewed exceptional fingerprint issues = %+v", issues)
	}

	mutated := append([]WireException(nil), reviewed...)
	mutated[0].DescriptorFingerprint = "changed-descriptor"
	issues = issues[:0]
	checkAggregateFingerprint(
		"exceptional wire coverage",
		reviewedFingerprint,
		fingerprintWireExceptions(mutated),
		&issues,
	)
	if len(issues) != 1 {
		t.Fatalf("mutated exceptional fingerprint issues = %+v, want one", issues)
	}
}
