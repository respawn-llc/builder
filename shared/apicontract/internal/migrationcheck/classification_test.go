package migrationcheck

import (
	"errors"
	"reflect"
	"testing"

	"core/shared/apicontract/internal/migrationcheck/testfixture"
)

func TestStructuredDiscoveryClassifiesRepresentativeScalarDomains(t *testing.T) {
	report := inspectClassificationFixture(t)
	signoff := currentClassificationSignoff(t, report)

	if err := CheckDeclarationClassifications(report, signoff); err != nil {
		t.Fatal(err)
	}

	assertScalarClassification(t, signoff, "OpenText", ScalarOpenValidatedString)
	assertScalarClassification(t, signoff, "EntityID", ScalarIdentifier)
	assertScalarClassification(t, signoff, "DeliveryMode", ScalarClosedStringEnum)
}

func TestStructuredDiscoveryFindsLegacyOnlyTypedConstant(t *testing.T) {
	report := inspectClassificationFixture(t)
	signoff := staleEnumBaselineSignoff(t, report)

	assertOnlyClassificationIssue(
		t,
		CheckDeclarationClassifications(report, signoff),
		IssueClosedEnumSetMismatch,
	)
}

func TestStructuredDiscoveryRecordsIntentionalEnumRename(t *testing.T) {
	report := inspectClassificationFixture(t)
	signoff := currentClassificationSignoff(t, report)
	deliveryMode := findScalarClassification(t, signoff, "DeliveryMode")

	var deferred *EnumMemberClassification
	for index := range deliveryMode.EnumMembers {
		if deliveryMode.EnumMembers[index].GoConstant == "DeliveryModeDeferred" {
			deferred = &deliveryMode.EnumMembers[index]
			break
		}
	}
	if deferred == nil {
		t.Fatal("DeliveryModeDeferred was not discovered")
	}
	if deferred.DescriptorName != "DELIVERY_MODE_LATER" || !deferred.IntentionalRename {
		t.Fatalf("intentional enum rename = %+v", *deferred)
	}
}

func TestStructuredDiscoveryFindsLegacyOnlyValidatorBranch(t *testing.T) {
	report, err := InspectDeclarations(reflect.TypeFor[testfixture.MessageLocalRequest]())
	if err != nil {
		t.Fatal(err)
	}
	signoff := messageLocalClassificationSignoff(t, report)
	// Reviewed before MessageLocalRequest added the deferred-delivery branch.
	signoff.Validators[0].Fingerprint = "8d539922517764b295a9757abe40bd9125c9e6b2636f8beb19ad19cf026946cb"

	assertOnlyClassificationIssue(
		t,
		CheckDeclarationClassifications(report, signoff),
		IssueValidatorFingerprintMismatch,
	)
}

func TestStructuredDiscoveryFindsLegacyOnlyDirectReceiverHelperCall(t *testing.T) {
	report := inspectClassificationFixture(t)
	validator := findValidator(t, report, "Request", "Validate")
	if !containsIdentityMember(validator.Closure, "validateDeliveryChoice") ||
		!containsIdentityMember(validator.Closure, "validateAccountOwnership") {
		t.Fatalf("validator closure = %+v", validator.Closure)
	}
	if !containsIdentityTypeMember(validator.Closure, "Request", "validateDeliveryChoice") {
		t.Fatalf("validator closure omits direct package-local receiver helper: %+v", validator.Closure)
	}

	signoff := currentClassificationSignoff(t, report)
	// Reviewed after the deferred-delivery branch, but before Request.Validate
	// delegated delivery choice validation to its package-local receiver helper.
	signoff.Validators[0].Fingerprint = "e365886825b8941434f1d67b8b37b3edcebac83a8f063fd84f5b0caa3f3604f"
	assertOnlyClassificationIssue(
		t,
		CheckDeclarationClassifications(report, signoff),
		IssueValidatorFingerprintMismatch,
	)
}

func TestStructuredDiscoveryReportsEnumAndFingerprintDriftIndependently(t *testing.T) {
	report := inspectClassificationFixture(t)

	enumOnly := staleEnumBaselineSignoff(t, report)
	assertOnlyClassificationIssue(
		t,
		CheckDeclarationClassifications(report, enumOnly),
		IssueClosedEnumSetMismatch,
	)

	fingerprintOnly := staleValidatorBaselineSignoff(t, report)
	assertOnlyClassificationIssue(
		t,
		CheckDeclarationClassifications(report, fingerprintOnly),
		IssueValidatorFingerprintMismatch,
	)
}

func TestValidationBehaviorParityDetectsOmittedScalarBound(t *testing.T) {
	validInline := testfixture.OpenText("inline")
	cases := []ValidationBehaviorCase[testfixture.MessageLocalRequest]{
		{
			Name: "label at bound",
			Value: testfixture.MessageLocalRequest{
				Label:  testfixture.OpenText("123456789012345678901234"),
				Inline: &validInline,
			},
		},
		{
			Name: "label beyond bound",
			Value: testfixture.MessageLocalRequest{
				Label:  testfixture.OpenText("1234567890123456789012345"),
				Inline: &validInline,
			},
		},
	}
	descriptor := fixtureValidationDescriptor{
		requireExactlyOneDelivery: true,
		requireDeferredBatch:      true,
	}

	assertBehaviorIssue(
		t,
		CheckValidationBehaviorParity(cases, validateLegacyFixture, descriptor.Validate),
		"label beyond bound",
	)
}

func TestValidationBehaviorParityDetectsWeakenedCrossFieldAndOneofRule(t *testing.T) {
	inline := testfixture.OpenText("inline")
	queued := testfixture.OpenText("queued")
	cases := []ValidationBehaviorCase[testfixture.MessageLocalRequest]{
		{
			Name: "one delivery choice",
			Value: testfixture.MessageLocalRequest{
				Inline: &inline,
			},
		},
		{
			Name: "both delivery choices",
			Value: testfixture.MessageLocalRequest{
				Inline: &inline,
				Queued: &queued,
			},
		},
	}
	maxLabelBytes := 24
	descriptor := fixtureValidationDescriptor{
		maxLabelBytes:        &maxLabelBytes,
		requireDeferredBatch: true,
	}

	assertBehaviorIssue(
		t,
		CheckValidationBehaviorParity(cases, validateLegacyFixture, descriptor.Validate),
		"both delivery choices",
	)
}

func TestValidationBehaviorParityCoversScalarBoundCrossFieldAndOneof(t *testing.T) {
	inline := testfixture.OpenText("inline")
	queued := testfixture.OpenText("queued")
	maxLabelBytes := 24
	descriptor := fixtureValidationDescriptor{
		maxLabelBytes:             &maxLabelBytes,
		requireExactlyOneDelivery: true,
		requireDeferredBatch:      true,
	}
	cases := []ValidationBehaviorCase[testfixture.MessageLocalRequest]{
		{Name: "valid inline", Value: testfixture.MessageLocalRequest{Inline: &inline}},
		{Name: "omitted choice", Value: testfixture.MessageLocalRequest{}},
		{Name: "both choices", Value: testfixture.MessageLocalRequest{Inline: &inline, Queued: &queued}},
		{
			Name: "deferred batch too small",
			Value: testfixture.MessageLocalRequest{
				DeliveryMode: testfixture.DeliveryModeDeferred,
				BatchSize:    1,
				Inline:       &inline,
			},
		},
	}

	if err := CheckValidationBehaviorParity(cases, validateLegacyFixture, descriptor.Validate); err != nil {
		t.Fatal(err)
	}
}

func TestStatefulValidatorRequiresOwningSpecAndServerOwnerClassification(t *testing.T) {
	report := inspectClassificationFixture(t)
	signoff := currentClassificationSignoff(t, report)

	signoff.Validators[0].Owner = nil
	assertClassificationIssue(
		t,
		CheckDeclarationClassifications(report, signoff),
		IssueMissingValidatorOwner,
	)

	signoff.Validators[0].Owner = &ValidatorOwner{
		Spec: ServerAPISpec("docs/dev/specs/server-api-contract.md"),
	}
	assertClassificationIssue(
		t,
		CheckDeclarationClassifications(report, signoff),
		IssueMissingValidatorOwner,
	)

	signoff.Validators[0].Owner.Server = ServerOwner("server/accountservice")
	if err := CheckDeclarationClassifications(report, signoff); err != nil {
		t.Fatal(err)
	}
}

func TestMessageLocalValidatorClassificationNeedsNoServerOwner(t *testing.T) {
	report, err := InspectDeclarations(reflect.TypeFor[testfixture.MessageLocalRequest]())
	if err != nil {
		t.Fatal(err)
	}
	signoff := messageLocalClassificationSignoff(t, report)

	if err := CheckDeclarationClassifications(report, signoff); err != nil {
		t.Fatal(err)
	}
}

func messageLocalClassificationSignoff(t *testing.T, report DeclarationReport) DeclarationClassification {
	t.Helper()
	validator := findValidator(t, report, "MessageLocalRequest", "Validate")
	return DeclarationClassification{
		Scalars: []ScalarClassification{
			{Identity: findScalar(t, report, "OpenText").Identity, Kind: ScalarOpenValidatedString},
			{
				Identity: findScalar(t, report, "DeliveryMode").Identity,
				Kind:     ScalarClosedStringEnum,
				EnumMembers: []EnumMemberClassification{
					{GoConstant: "DeliveryModeImmediate", DescriptorName: "DELIVERY_MODE_IMMEDIATE"},
					{GoConstant: "DeliveryModeScheduled", DescriptorName: "DELIVERY_MODE_SCHEDULED"},
					{
						GoConstant:        "DeliveryModeDeferred",
						DescriptorName:    "DELIVERY_MODE_LATER",
						IntentionalRename: true,
					},
				},
			},
		},
		Validators: []ValidatorClassification{{
			Identity:    validator.Identity,
			Fingerprint: validator.Fingerprint,
			Kind:        ValidatorMessageLocal,
		}},
	}
}

func inspectClassificationFixture(t *testing.T) DeclarationReport {
	t.Helper()
	report, err := InspectDeclarations(reflect.TypeFor[testfixture.Request]())
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func currentClassificationSignoff(t *testing.T, report DeclarationReport) DeclarationClassification {
	t.Helper()
	validator := findValidator(t, report, "Request", "Validate")
	return DeclarationClassification{
		Scalars: []ScalarClassification{
			{
				Identity: findScalar(t, report, "OpenText").Identity,
				Kind:     ScalarOpenValidatedString,
			},
			{
				Identity: findScalar(t, report, "EntityID").Identity,
				Kind:     ScalarIdentifier,
			},
			{
				Identity: findScalar(t, report, "DeliveryMode").Identity,
				Kind:     ScalarClosedStringEnum,
				EnumMembers: []EnumMemberClassification{
					{GoConstant: "DeliveryModeImmediate", DescriptorName: "DELIVERY_MODE_IMMEDIATE"},
					{GoConstant: "DeliveryModeScheduled", DescriptorName: "DELIVERY_MODE_SCHEDULED"},
					{
						GoConstant:        "DeliveryModeDeferred",
						DescriptorName:    "DELIVERY_MODE_LATER",
						IntentionalRename: true,
					},
				},
			},
		},
		Validators: []ValidatorClassification{
			{
				Identity:    validator.Identity,
				Fingerprint: validator.Fingerprint,
				Kind:        ValidatorStatefulOrSharedOwner,
				Owner: &ValidatorOwner{
					Spec:   ServerAPISpec("docs/dev/specs/server-api-contract.md"),
					Server: ServerOwner("server/accountservice"),
				},
			},
		},
	}
}

func staleEnumBaselineSignoff(t *testing.T, report DeclarationReport) DeclarationClassification {
	t.Helper()
	signoff := currentClassificationSignoff(t, report)
	deliveryModeIndex := findScalarClassificationIndex(t, signoff, "DeliveryMode")
	signoff.Scalars[deliveryModeIndex].EnumMembers = []EnumMemberClassification{
		{GoConstant: "DeliveryModeImmediate", DescriptorName: "DELIVERY_MODE_IMMEDIATE"},
		{GoConstant: "DeliveryModeScheduled", DescriptorName: "DELIVERY_MODE_SCHEDULED"},
	}
	return signoff
}

func staleValidatorBaselineSignoff(t *testing.T, report DeclarationReport) DeclarationClassification {
	t.Helper()
	signoff := currentClassificationSignoff(t, report)
	// This reviewed baseline intentionally predates only the direct
	// package-local receiver helper call in the legacy fixture.
	signoff.Validators[0].Fingerprint = "e365886825b8941434f1d67b8b37b3edcebac83a8f063fd84f5b0caa3f3604f"
	return signoff
}

type fixtureValidationDescriptor struct {
	maxLabelBytes             *int
	requireExactlyOneDelivery bool
	requireDeferredBatch      bool
}

func (d fixtureValidationDescriptor) Validate(request testfixture.MessageLocalRequest) error {
	if d.maxLabelBytes != nil && len(request.Label) > *d.maxLabelBytes {
		return errors.New("descriptor label bound")
	}
	if d.requireExactlyOneDelivery && (request.Inline == nil) == (request.Queued == nil) {
		return errors.New("descriptor delivery oneof")
	}
	if d.requireDeferredBatch &&
		request.DeliveryMode == testfixture.DeliveryModeDeferred &&
		request.BatchSize < 2 {
		return errors.New("descriptor deferred batch")
	}
	return nil
}

func validateLegacyFixture(request testfixture.MessageLocalRequest) error {
	return request.Validate()
}

func findScalar(t *testing.T, report DeclarationReport, typeName string) NamedScalar {
	t.Helper()
	for _, scalar := range report.NamedScalars {
		if scalar.Identity.TypeName == typeName {
			return scalar
		}
	}
	t.Fatalf("scalar %s was not discovered", typeName)
	return NamedScalar{}
}

func findValidator(t *testing.T, report DeclarationReport, typeName string, methodName string) Validator {
	t.Helper()
	for _, validator := range report.Validators {
		if validator.Identity.TypeName == typeName && validator.Identity.MemberName == methodName {
			return validator
		}
	}
	t.Fatalf("validator %s.%s was not discovered", typeName, methodName)
	return Validator{}
}

func findScalarClassification(t *testing.T, signoff DeclarationClassification, typeName string) ScalarClassification {
	t.Helper()
	return signoff.Scalars[findScalarClassificationIndex(t, signoff, typeName)]
}

func findScalarClassificationIndex(t *testing.T, signoff DeclarationClassification, typeName string) int {
	t.Helper()
	for index, scalar := range signoff.Scalars {
		if scalar.Identity.TypeName == typeName {
			return index
		}
	}
	t.Fatalf("scalar classification %s was not found", typeName)
	return 0
}

func assertScalarClassification(
	t *testing.T,
	signoff DeclarationClassification,
	typeName string,
	want ScalarClassificationKind,
) {
	t.Helper()
	if got := findScalarClassification(t, signoff, typeName).Kind; got != want {
		t.Fatalf("%s classification = %q, want %q", typeName, got, want)
	}
}

func containsIdentityMember(identities []Identity, member string) bool {
	for _, identity := range identities {
		if identity.MemberName == member {
			return true
		}
	}
	return false
}

func containsIdentityTypeMember(identities []Identity, typeName string, member string) bool {
	for _, identity := range identities {
		if identity.TypeName == typeName && identity.MemberName == member {
			return true
		}
	}
	return false
}

func assertClassificationIssue(t *testing.T, err error, want ClassificationIssueCode) {
	t.Helper()
	classificationError := requireClassificationError(t, err)
	for _, issue := range classificationError.Issues {
		if issue.Code == want {
			return
		}
	}
	t.Fatalf("classification issues = %+v, want %q", classificationError.Issues, want)
}

func assertOnlyClassificationIssue(t *testing.T, err error, want ClassificationIssueCode) {
	t.Helper()
	classificationError := requireClassificationError(t, err)
	if len(classificationError.Issues) != 1 || classificationError.Issues[0].Code != want {
		t.Fatalf("classification issues = %+v, want only %q", classificationError.Issues, want)
	}
}

func requireClassificationError(t *testing.T, err error) *ClassificationError {
	t.Helper()
	if err == nil {
		t.Fatal("declaration classification unexpectedly succeeded")
	}
	var classificationError *ClassificationError
	if !errors.As(err, &classificationError) {
		t.Fatalf("error type = %T, want *ClassificationError", err)
	}
	return classificationError
}

func assertBehaviorIssue(t *testing.T, err error, caseName string) {
	t.Helper()
	if err == nil {
		t.Fatal("validation behavior parity unexpectedly succeeded")
	}
	var parityError *ValidationBehaviorError
	if !errors.As(err, &parityError) {
		t.Fatalf("error type = %T, want *ValidationBehaviorError", err)
	}
	for _, issue := range parityError.Issues {
		if issue.CaseName == caseName {
			return
		}
	}
	t.Fatalf("behavior issues = %+v, want case %q", parityError.Issues, caseName)
}
