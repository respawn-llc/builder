package migrationcheck

import (
	"errors"
	"testing"

	"core/shared/apicontract"
)

type descriptorFixture []OperationDescriptor

func (fixture descriptorFixture) OperationDescriptors() []OperationDescriptor {
	return append([]OperationDescriptor(nil), fixture...)
}

func TestOperationAssociationJoinsSpecifiedLegacyFixturesExactly(t *testing.T) {
	routes := specifiedLegacyRoutes(t)
	descriptors := specifiedDescriptors()

	if err := CheckOperationAssociations(routes, descriptors, nil); err != nil {
		t.Fatal(err)
	}
}

func TestOperationAssociationRequiresExactLegacyWireName(t *testing.T) {
	routes := []apicontract.Route{syntheticRoute("workflow.create", apicontract.KindUnary)}
	descriptors := descriptorFixture{
		descriptor("workflow", "WorkflowService", "Create", "workflow.Create", apicontract.KindUnary),
	}

	assertAssociationIssue(t, routes, descriptors, nil, IssueMissingLegacyDescriptor)
}

func TestOperationAssociationRequiresExactEventAndCompletionAssociation(t *testing.T) {
	routes := worktreeSetupRoutes(t)
	descriptors := specifiedWorktreeSetupDescriptors()
	wrongEvent := ref("worktree", "SetupService", "Complete")
	descriptors[0].Event = &wrongEvent

	assertAssociationIssue(t, routes, descriptors, nil, IssueWrongEventAssociation)

	descriptors = specifiedWorktreeSetupDescriptors()
	wrongCompletion := ref("worktree", "SetupService", "Event")
	descriptors[0].Completion = &wrongCompletion
	assertAssociationIssue(t, routes, descriptors, nil, IssueWrongCompletionAssociation)
}

func TestOperationAssociationReferencesExactDeclarationDespiteNormalizedCollision(t *testing.T) {
	routes := worktreeSetupRoutes(t)
	descriptors := specifiedWorktreeSetupDescriptors()
	collidingEvent := descriptor(
		"worktree",
		"SetupService",
		"ApiStatus",
		"fixture.collidingEvent",
		apicontract.KindNotification,
	)
	descriptors = append(descriptors, collidingEvent)
	wrongEvent := ref("worktree", "SetupService", "ApiStatus")
	descriptors[0].Event = &wrongEvent
	descriptors[1].Method = "APIStatus"

	assertAssociationIssue(t, routes, descriptors, nil, IssueWrongEventAssociation)
}

func TestOperationAssociationRequiresExactKind(t *testing.T) {
	routes := []apicontract.Route{syntheticRoute("workflow.create", apicontract.KindUnary)}
	descriptors := descriptorFixture{
		descriptor("workflow", "WorkflowService", "Create", "workflow.create", apicontract.KindProgress),
	}

	assertAssociationIssue(t, routes, descriptors, nil, IssueWrongOperationKind)
}

func TestOperationAssociationReportsMissingAndDuplicateIndependently(t *testing.T) {
	workflowCreate := syntheticRoute("workflow.create", apicontract.KindUnary)
	routes := []apicontract.Route{workflowCreate}

	assertAssociationIssue(t, routes, descriptorFixture{}, nil, IssueMissingLegacyDescriptor)

	duplicate := descriptor("workflow", "WorkflowService", "CreateAgain", "workflow.create", apicontract.KindUnary)
	descriptors := descriptorFixture{
		descriptor("workflow", "WorkflowService", "Create", "workflow.create", apicontract.KindUnary),
		duplicate,
	}
	assertAssociationIssue(t, routes, descriptors, nil, IssueDuplicateLegacyDescriptor)

	assertAssociationIssue(
		t,
		[]apicontract.Route{workflowCreate, workflowCreate},
		descriptorFixture{descriptor("workflow", "WorkflowService", "Create", "workflow.create", apicontract.KindUnary)},
		nil,
		IssueDuplicateLegacyRoute,
	)
}

func TestOperationAssociationRejectsDescriptorWithoutProvenanceOrRoute(t *testing.T) {
	workflowCreate := descriptor("workflow", "WorkflowService", "Create", "workflow.create", apicontract.KindUnary)
	workflowCreate.LegacyWireName = nil
	assertAssociationIssue(
		t,
		nil,
		descriptorFixture{workflowCreate},
		nil,
		IssueDescriptorWithoutLegacyName,
	)

	assertAssociationIssue(
		t,
		nil,
		descriptorFixture{
			descriptor("workflow", "WorkflowService", "Create", "workflow.create", apicontract.KindUnary),
		},
		nil,
		IssueDescriptorWithoutLegacyRoute,
	)
}

func TestOperationAssociationRejectsDuplicateStrictActiveNames(t *testing.T) {
	routes := []apicontract.Route{
		syntheticRoute("workflow.create", apicontract.KindUnary),
		syntheticRoute("session.materializeWorkspaceChat", apicontract.KindUnary),
	}
	descriptors := descriptorFixture{
		descriptor("workflow", "WorkflowService", "Create", "workflow.create", apicontract.KindUnary),
		descriptor("workflow", "WorkflowService", "Create", "session.materializeWorkspaceChat", apicontract.KindUnary),
	}

	assertAssociationIssue(t, routes, descriptors, nil, IssueDuplicateActiveName)
}

func TestOperationAssociationDoesNotAcceptUnapprovedAliases(t *testing.T) {
	routes := []apicontract.Route{syntheticRoute("workflow.create", apicontract.KindUnary)}
	descriptors := descriptorFixture{
		descriptor("workflow", "WorkflowService", "Create", "workflow.create", apicontract.KindUnary),
	}

	assertAssociationIssue(
		t,
		routes,
		descriptors,
		[]string{"workflow.workflow_service.create"},
		IssueActiveNameIsUnapprovedAlias,
	)
}

func TestOperationAssociationRejectsInvalidFirstPackageCharacter(t *testing.T) {
	routes := []apicontract.Route{syntheticRoute("workflow.create", apicontract.KindUnary)}
	descriptors := descriptorFixture{
		descriptor("Workflow", "WorkflowService", "Create", "workflow.create", apicontract.KindUnary),
	}

	assertAssociationIssue(t, routes, descriptors, nil, IssueInvalidPackage)
}

func TestOperationAssociationRejectsInvalidSubsequentPackageCharacter(t *testing.T) {
	routes := []apicontract.Route{syntheticRoute("workflow.create", apicontract.KindUnary)}
	descriptors := descriptorFixture{
		descriptor("workflow-api", "WorkflowService", "Create", "workflow.create", apicontract.KindUnary),
	}

	assertAssociationIssue(t, routes, descriptors, nil, IssueInvalidPackage)
}

func TestOperationAssociationRejectsUnexpectedEventAndCompletion(t *testing.T) {
	routes := []apicontract.Route{syntheticRoute("workflow.create", apicontract.KindUnary)}
	event := ref("worktree", "SetupService", "Event")
	completion := ref("worktree", "SetupService", "Complete")
	workflowCreate := descriptor(
		"workflow",
		"WorkflowService",
		"Create",
		"workflow.create",
		apicontract.KindUnary,
	)
	workflowCreate.Event = &event
	workflowCreate.Completion = &completion
	descriptors := descriptorFixture{
		workflowCreate,
		descriptor("worktree", "SetupService", "Event", "worktree.setup.event", apicontract.KindNotification),
		descriptor("worktree", "SetupService", "Complete", "worktree.setup.complete", apicontract.KindNotification),
	}

	assertAssociationIssue(t, routes, descriptors, nil, IssueUnexpectedEventAssociation)
	assertAssociationIssue(t, routes, descriptors, nil, IssueUnexpectedCompletion)
}

func TestPascalCaseStateMachineRejectsMalformedDescriptorIdentifiers(t *testing.T) {
	routes := []apicontract.Route{syntheticRoute("workflow.create", apicontract.KindUnary)}
	descriptors := descriptorFixture{
		descriptor("workflow", "workflowService", "Create", "workflow.create", apicontract.KindUnary),
	}
	assertAssociationIssue(t, routes, descriptors, nil, IssueInvalidPascalCaseIdentifier)
}

func specifiedLegacyRoutes(t *testing.T) []apicontract.Route {
	t.Helper()
	routes := []apicontract.Route{
		syntheticRoute("workflow.create", apicontract.KindUnary),
		syntheticRoute("session.materializeWorkspaceChat", apicontract.KindUnary),
		syntheticRoute("worktree.create_target.resolve", apicontract.KindUnary),
	}
	routes = append(routes, worktreeSetupRoutes(t)...)
	return routes
}

func specifiedDescriptors() descriptorFixture {
	return descriptorFixture{
		descriptor("workflow", "WorkflowService", "Create", "workflow.create", apicontract.KindUnary),
		descriptor("session", "WorkspaceChatService", "Materialize", "session.materializeWorkspaceChat", apicontract.KindUnary),
		descriptor("worktree", "CreateTargetService", "Resolve", "worktree.create_target.resolve", apicontract.KindUnary),
	}.with(specifiedWorktreeSetupDescriptors()...)
}

func worktreeSetupRoutes(t *testing.T) []apicontract.Route {
	t.Helper()
	return []apicontract.Route{
		{
			Method:         "worktree.setup.subscribe",
			Kind:           apicontract.KindSubscription,
			EventMethod:    "worktree.setup.event",
			CompleteMethod: "worktree.setup.complete",
		},
		syntheticRoute("worktree.setup.event", apicontract.KindNotification),
		syntheticRoute("worktree.setup.complete", apicontract.KindNotification),
	}
}

func specifiedWorktreeSetupDescriptors() descriptorFixture {
	event := ref("worktree", "SetupService", "Event")
	completion := ref("worktree", "SetupService", "Complete")
	subscribe := descriptor(
		"worktree",
		"SetupService",
		"Subscribe",
		"worktree.setup.subscribe",
		apicontract.KindSubscription,
	)
	subscribe.Event = &event
	subscribe.Completion = &completion
	return descriptorFixture{
		subscribe,
		descriptor("worktree", "SetupService", "Event", "worktree.setup.event", apicontract.KindNotification),
		descriptor("worktree", "SetupService", "Complete", "worktree.setup.complete", apicontract.KindNotification),
	}
}

func (fixture descriptorFixture) with(descriptors ...OperationDescriptor) descriptorFixture {
	return append(fixture, descriptors...)
}

func descriptor(
	packageName string,
	service string,
	method string,
	legacyWireName string,
	kind apicontract.Kind,
) OperationDescriptor {
	return OperationDescriptor{
		Package:        packageName,
		Service:        service,
		Method:         method,
		LegacyWireName: &legacyWireName,
		Kind:           kind,
	}
}

func ref(packageName string, service string, method string) OperationReference {
	return OperationReference{Package: packageName, Service: service, Method: method}
}

func syntheticRoute(method string, kind apicontract.Kind) apicontract.Route {
	return apicontract.Route{Method: method, Kind: kind}
}

func assertAssociationIssue(
	t *testing.T,
	routes []apicontract.Route,
	descriptors descriptorFixture,
	aliases []string,
	want AssociationIssueCode,
) {
	t.Helper()
	associationError := requireAssociationError(
		t,
		CheckOperationAssociations(routes, descriptors, aliases),
	)
	for _, issue := range associationError.Issues {
		if issue.Code == want {
			return
		}
	}
	t.Fatalf("association issues = %+v, want code %q", associationError.Issues, want)
}

func requireAssociationError(t *testing.T, err error) *AssociationError {
	t.Helper()
	if err == nil {
		t.Fatal("operation association unexpectedly succeeded")
	}
	var associationError *AssociationError
	if !errors.As(err, &associationError) {
		t.Fatalf("error type = %T, want *AssociationError", err)
	}
	return associationError
}
