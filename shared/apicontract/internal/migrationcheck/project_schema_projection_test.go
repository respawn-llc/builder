package migrationcheck

import (
	"testing"

	projectpb "core/shared/protoapi/gen/kent/api/project"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestProjectSchemaProjectionOmitsOnlyServerAuthoredBlockerWording(t *testing.T) {
	fixture := projectSchemaProjectionFixture()
	if err := CheckProjectSchemaFiniteProjection(
		fixture.legacy,
		fixture.descriptor,
		ProjectSchemaProjectionIdentities(),
	); err != nil {
		t.Fatal(err)
	}
}

func TestProjectSchemaProjectionRejectsOmittingStableBlockerFacts(t *testing.T) {
	fixture := projectSchemaProjectionFixture()
	for _, retained := range fixture.descriptor {
		t.Run(retained.String(), func(t *testing.T) {
			assertProjectionIssue(
				t,
				CheckProjectSchemaFiniteProjection(
					fixture.legacy,
					removeFixtureIdentity(fixture.descriptor, retained),
					ProjectSchemaProjectionIdentities(),
				),
				IssueUnapprovedProjection,
			)
		})
	}
}

func TestProjectSchemaProjectionRejectsExpandingClientOwnedWordingDeletion(t *testing.T) {
	fixture := projectSchemaProjectionFixture()
	expanded := append(
		ProjectSchemaProjectionIdentities(),
		fieldIdentity("core/shared/serverapi", "ProjectDeleteBlocker", "Code"),
	)
	assertProjectionIssue(
		t,
		CheckProjectSchemaFiniteProjection(fixture.legacy, fixture.descriptor, expanded),
		IssueUnexpectedProjectionIdentity,
	)
}

func TestProjectSchemaProjectionResolvesEveryLockedLegacyIdentity(t *testing.T) {
	report, err := InspectExecutionTarget()
	if err != nil {
		t.Fatal(err)
	}
	resolved := make(map[Identity]struct{}, len(report.Predecessors))
	for _, predecessor := range report.Predecessors {
		resolved[predecessor.Identity] = struct{}{}
	}
	for _, identity := range ProjectSchemaProjectionIdentities() {
		if _, exists := resolved[identity]; !exists {
			t.Errorf("Project schema projection identity was not resolved from the execution target: %s", identity)
		}
	}
}

func TestProjectWorkspaceOperationsOwnDistinctErrorUnions(t *testing.T) {
	for _, fixture := range []struct {
		message protoreflect.MessageDescriptor
		details []protoreflect.Name
	}{
		{(&projectpb.ListProjectWorkspacesError{}).ProtoReflect().Descriptor(), []protoreflect.Name{"project_not_found", "internal_failure"}},
		{(&projectpb.GetProjectWorkspaceError{}).ProtoReflect().Descriptor(), []protoreflect.Name{"project_not_found", "internal_failure"}},
	} {
		if fixture.message.Fields().Get(0).Name() != "code" {
			t.Errorf("%s first field is not code", fixture.message.FullName())
		}
		assertMessageOneofFields(t, fixture.message, "detail", fixture.details...)
	}
	if (&projectpb.ListProjectWorkspacesError{}).ProtoReflect().Descriptor().FullName() ==
		(&projectpb.GetProjectWorkspaceError{}).ProtoReflect().Descriptor().FullName() {
		t.Fatal("project workspace list and get operations share an error union")
	}
}

type projectSchemaProjection struct {
	legacy     []Identity
	descriptor []Identity
}

func projectSchemaProjectionFixture() projectSchemaProjection {
	retained := []Identity{
		fieldIdentity("core/shared/serverapi", "ProjectWorkspaceUnlinkBlocker", "Code"),
		fieldIdentity("core/shared/serverapi", "ProjectWorkspaceUnlinkBlocker", "Count"),
		fieldIdentity("core/shared/serverapi", "ProjectDeleteBlocker", "Code"),
		fieldIdentity("core/shared/serverapi", "ProjectDeleteBlocker", "Count"),
	}
	legacy := append([]Identity(nil), ProjectSchemaProjectionIdentities()...)
	legacy = append(legacy, retained...)
	return projectSchemaProjection{
		legacy:     legacy,
		descriptor: retained,
	}
}
