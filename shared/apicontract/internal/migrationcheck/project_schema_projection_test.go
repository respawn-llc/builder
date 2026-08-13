package migrationcheck

import "testing"

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
