package migrationcheck

import (
	"errors"
	"strings"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestKENT554ProjectionOmitsOnlyNegotiationIdentities(t *testing.T) {
	fixture := kent554ProjectionFixture(t)

	if err := CheckFiniteProjection(fixture.Legacy, fixture.Descriptor, KENT554ProjectionIdentities()); err != nil {
		t.Fatal(err)
	}
}

func TestKENT554ProjectionRejectsExtraFieldAndTypeOmissions(t *testing.T) {
	fixture := kent554ProjectionFixture(t)

	withoutProviderFact := removeFixtureIdentity(
		fixture.Descriptor,
		fieldIdentity("core/shared/serverapi", "CapabilityFactsResponse", "Providers"),
	)
	assertProjectionIssue(
		t,
		CheckFiniteProjection(fixture.Legacy, withoutProviderFact, KENT554ProjectionIdentities()),
		IssueUnapprovedProjection,
	)

	withoutImportFacts := removeFixtureIdentity(
		fixture.Descriptor,
		typeIdentity("core/shared/serverapi", "ImportCapabilityFacts"),
	)
	assertProjectionIssue(
		t,
		CheckFiniteProjection(fixture.Legacy, withoutImportFacts, KENT554ProjectionIdentities()),
		IssueUnapprovedProjection,
	)
}

func TestKENT554ProjectionRejectsExpandingApprovedProjection(t *testing.T) {
	fixture := kent554ProjectionFixture(t)
	expanded := append(
		KENT554ProjectionIdentities(),
		fieldIdentity("core/shared/serverapi", "ModelCapabilityFact", "SupportsThinking"),
	)

	assertProjectionIssue(
		t,
		CheckFiniteProjection(fixture.Legacy, fixture.Descriptor, expanded),
		IssueUnexpectedProjectionIdentity,
	)
}

func TestKENT554ProjectedIdentitiesNeverAppearInDescriptorFixture(t *testing.T) {
	fixture := kent554ProjectionFixture(t)
	withProjectedField := append(
		append([]Identity(nil), fixture.Descriptor...),
		fieldIdentity("core/shared/protocol", "HandshakeRequest", "ClientCapabilities"),
	)

	assertProjectionIssue(
		t,
		CheckFiniteProjection(fixture.Legacy, withProjectedField, KENT554ProjectionIdentities()),
		IssueProjectedIdentityAuthored,
	)
}

func TestKENT554PostProjectionHandshakeValidationOmitsCapabilityNegotiation(t *testing.T) {
	assertFocusedProjectionFixture(t, FocusedKENT554NegotiationValidation)
	valid := postKENT554Handshake{ProtocolVersion: protocol.Version}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid post-projection handshake: %v", err)
	}
	if err := (postKENT554Handshake{}).Validate(); err == nil {
		t.Fatal("post-projection handshake accepted an absent protocol version")
	}

	legacyExplicitEmpty := protocol.HandshakeRequest{
		ProtocolVersion:    protocol.Version,
		ClientCapabilities: &protocol.ClientCapabilities{},
	}
	if err := legacyExplicitEmpty.Validate(); err == nil {
		t.Fatal("legacy handshake fixture no longer exercises capability negotiation")
	}
}

func TestKENT554RetainsCapabilityFactsValidationAndConstants(t *testing.T) {
	assertFocusedProjectionFixture(t, FocusedKENT554NegotiationConstants)
	assertFocusedProjectionFixture(t, FocusedKENT554RetainedCapabilityFacts)
	blank := " "
	if err := (serverapi.CapabilityFactsRequest{WorkspaceRoot: &blank}).Validate(); err == nil {
		t.Fatal("retained capability facts request accepted a blank workspace root")
	}
	if err := (serverapi.CapabilityFactsRequest{
		ExplicitLLMProviderIDs: []string{"openai", " "},
	}).Validate(); err == nil {
		t.Fatal("retained capability facts request accepted a blank provider id")
	}
	if got := protocol.MethodCapabilityFactsGet; got != "capability.facts.get" {
		t.Fatalf("retained capability facts operation constant = %q", got)
	}
	if got := serverapi.ImportErrorItemKindSkill; got != "skill" {
		t.Fatalf("retained import capability constant = %q", got)
	}
}

type postKENT554Handshake struct {
	ProtocolVersion string
}

func (r postKENT554Handshake) Validate() error {
	if strings.TrimSpace(r.ProtocolVersion) == "" {
		return errors.New("protocol version is required")
	}
	return nil
}

type kent554Fixture struct {
	Legacy     []Identity
	Descriptor []Identity
}

func kent554ProjectionFixture(t *testing.T) kent554Fixture {
	t.Helper()
	legacy := append([]Identity(nil), KENT554ProjectionIdentities()...)
	legacy = append(legacy, retainedKENT554CapabilityIdentities()...)
	descriptor := retainedKENT554CapabilityIdentities()
	return kent554Fixture{Legacy: legacy, Descriptor: descriptor}
}

func retainedKENT554CapabilityIdentities() []Identity {
	return []Identity{
		fieldIdentity("core/shared/protocol", "HandshakeRequest", "ProtocolVersion"),
		fieldIdentity("core/shared/protocol", "ServerIdentity", "ProtocolVersion"),
		fieldIdentity("core/shared/protocol", "ServerIdentity", "ServerID"),
		fieldIdentity("core/shared/protocol", "ServerIdentity", "PID"),
		fieldIdentity("core/shared/protocol", "ServerIdentity", "PersistenceRootID"),
		typeIdentity("core/shared/serverapi", "CapabilityFactsRequest"),
		fieldIdentity("core/shared/serverapi", "CapabilityFactsRequest", "WorkspaceRoot"),
		fieldIdentity("core/shared/serverapi", "CapabilityFactsRequest", "ExplicitLLMProviderIDs"),
		typeIdentity("core/shared/serverapi", "CapabilityFactsResponse"),
		fieldIdentity("core/shared/serverapi", "CapabilityFactsResponse", "Models"),
		fieldIdentity("core/shared/serverapi", "CapabilityFactsResponse", "Providers"),
		fieldIdentity("core/shared/serverapi", "CapabilityFactsResponse", "Imports"),
		fieldIdentity("core/shared/serverapi", "CapabilityFactsResponse", "Defaults"),
		fieldIdentity("core/shared/serverapi", "CapabilityFactsResponse", "Recommendations"),
		typeIdentity("core/shared/serverapi", "ModelCapabilityFacts"),
		typeIdentity("core/shared/serverapi", "ModelCapabilityFact"),
		fieldIdentity("core/shared/serverapi", "ModelCapabilityFact", "SupportsThinking"),
		fieldIdentity("core/shared/serverapi", "ModelCapabilityFact", "SupportsVisionInputs"),
		typeIdentity("core/shared/serverapi", "ProviderCapabilityFacts"),
		typeIdentity("core/shared/serverapi", "LLMProviderCapabilityFact"),
		fieldIdentity("core/shared/serverapi", "LLMProviderCapabilityFact", "SupportsResponsesAPI"),
		fieldIdentity("core/shared/serverapi", "LLMProviderCapabilityFact", "SupportsNativeWebSearch"),
		typeIdentity("core/shared/serverapi", "ImportCapabilityFacts"),
		fieldIdentity("core/shared/serverapi", "ImportCapabilityFacts", "Skills"),
		fieldIdentity("core/shared/serverapi", "ImportCapabilityFacts", "Commands"),
		fieldIdentity("core/shared/serverapi", "ImportCapabilityFacts", "SkillEnablement"),
		fieldIdentity("core/shared/serverapi", "ImportCapabilityFacts", "Errors"),
	}
}

func removeFixtureIdentity(identities []Identity, removed Identity) []Identity {
	result := make([]Identity, 0, len(identities))
	for _, identity := range identities {
		if identity != removed {
			result = append(result, identity)
		}
	}
	return result
}

func assertProjectionIssue(t *testing.T, err error, code ProjectionIssueCode) {
	t.Helper()
	var projectionErr *ProjectionError
	if !errors.As(err, &projectionErr) {
		t.Fatalf("error = %v, want ProjectionError", err)
	}
	for _, issue := range projectionErr.Issues {
		if issue.Code == code {
			return
		}
	}
	t.Fatalf("projection issues = %+v, want %s", projectionErr.Issues, code)
}
