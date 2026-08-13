package migrationcheck

import (
	"errors"
	"testing"

	"core/shared/apicontract"
	"core/shared/protoapi"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"core/shared/protocol"
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
	routes := []apicontract.Route{mustLiveRoute(t, "workflow.create")}
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
	routes := []apicontract.Route{mustLiveRoute(t, "workflow.create")}
	descriptors := descriptorFixture{
		descriptor("workflow", "WorkflowService", "Create", "workflow.create", apicontract.KindProgress),
	}

	assertAssociationIssue(t, routes, descriptors, nil, IssueWrongOperationKind)
}

func TestOperationAssociationReportsMissingAndDuplicateIndependently(t *testing.T) {
	workflowCreate := mustLiveRoute(t, "workflow.create")
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
		mustLiveRoute(t, "workflow.create"),
		mustLiveRoute(t, "session.materializeWorkspaceChat"),
	}
	descriptors := descriptorFixture{
		descriptor("workflow", "WorkflowService", "Create", "workflow.create", apicontract.KindUnary),
		descriptor("workflow", "WorkflowService", "Create", "session.materializeWorkspaceChat", apicontract.KindUnary),
	}

	assertAssociationIssue(t, routes, descriptors, nil, IssueDuplicateActiveName)
}

func TestOperationAssociationDoesNotAcceptUnapprovedAliases(t *testing.T) {
	routes := []apicontract.Route{mustLiveRoute(t, "workflow.create")}
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
	routes := []apicontract.Route{mustLiveRoute(t, "workflow.create")}
	descriptors := descriptorFixture{
		descriptor("Workflow", "WorkflowService", "Create", "workflow.create", apicontract.KindUnary),
	}

	assertAssociationIssue(t, routes, descriptors, nil, IssueInvalidPackage)
}

func TestOperationAssociationRejectsInvalidSubsequentPackageCharacter(t *testing.T) {
	routes := []apicontract.Route{mustLiveRoute(t, "workflow.create")}
	descriptors := descriptorFixture{
		descriptor("workflow-api", "WorkflowService", "Create", "workflow.create", apicontract.KindUnary),
	}

	assertAssociationIssue(t, routes, descriptors, nil, IssueInvalidPackage)
}

func TestOperationAssociationRejectsUnexpectedEventAndCompletion(t *testing.T) {
	routes := []apicontract.Route{mustLiveRoute(t, "workflow.create")}
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
		descriptor("worktree", "SetupService", "Event", protocol.MethodWorktreeSetupEvent, apicontract.KindNotification),
		descriptor("worktree", "SetupService", "Complete", protocol.MethodWorktreeSetupComplete, apicontract.KindNotification),
	}

	assertAssociationIssue(t, routes, descriptors, nil, IssueUnexpectedEventAssociation)
	assertAssociationIssue(t, routes, descriptors, nil, IssueUnexpectedCompletion)
}

func TestPascalCaseStateMachineRejectsMalformedDescriptorIdentifiers(t *testing.T) {
	routes := []apicontract.Route{mustLiveRoute(t, "workflow.create")}
	descriptors := descriptorFixture{
		descriptor("workflow", "workflowService", "Create", "workflow.create", apicontract.KindUnary),
	}
	assertAssociationIssue(t, routes, descriptors, nil, IssueInvalidPascalCaseIdentifier)
}

func TestLiveOperationDescriptorsCoverLegacyRoutes(t *testing.T) {
	operations, descriptors, err := completedSchemaSliceDescriptors()
	if err != nil {
		t.Fatal(err)
	}
	routes := routesForDescriptorProvenance(descriptors)
	if err := CheckOperationAssociations(
		routes,
		descriptors,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	assertCompletedSchemaSliceMetadata(t, routes, operations)
}

func completedSchemaSliceRoutes() []apicontract.Route {
	names := []string{
		protocol.MethodHandshake,
		protocol.MethodServerReadinessGet,
		protocol.MethodServerUpdateStatusGet,
		protocol.MethodAuthGetBootstrapStatus,
		protocol.MethodAuthCompleteBootstrap,
		protocol.MethodAuthAcknowledgeNoAuth,
		protocol.MethodAuthGetStatus,
		protocol.MethodCapabilityFactsGet,
		protocol.MethodPromptCommandCatalogGet,
		protocol.MethodOnboardingFinalize,
		protocol.MethodAttachProject,
		protocol.MethodAttachSession,
		protocol.MethodProjectList,
		protocol.MethodProjectHomeList,
		protocol.MethodProjectResolvePath,
		protocol.MethodProjectPlanWorkspaceBinding,
		protocol.MethodProjectCreate,
		protocol.MethodProjectEditGet,
		protocol.MethodProjectUpdate,
		protocol.MethodProjectSetDefaultWorkspace,
		protocol.MethodProjectWorkspaceList,
		protocol.MethodProjectUnlinkWorkspace,
		protocol.MethodProjectDelete,
		protocol.MethodProjectAttachWorkspace,
		protocol.MethodProjectRebindWorkspace,
		protocol.MethodProjectGetOverview,
		protocol.MethodSessionPage,
		protocol.MethodSessionPlan,
		protocol.MethodSessionWorkspaceChatDraft,
		protocol.MethodSessionWorkspaceChatMaterialize,
		protocol.MethodSessionGetInitialInput,
		protocol.MethodSessionPersistInputDraft,
		protocol.MethodSessionRetargetWorkspace,
		protocol.MethodSessionResolveTransition,
		protocol.MethodSessionRuntimeActivate,
		protocol.MethodSessionRuntimeRelease,
	}
	routes := make([]apicontract.Route, 0, len(names))
	for _, name := range names {
		route, exists := apicontract.RouteByMethod(name)
		if !exists {
			panic("completed schema slice route missing: " + name)
		}
		routes = append(routes, route)
	}
	return routes
}

func completedSchemaSliceDescriptors() ([]protoapi.Operation, descriptorFixture, error) {
	routeNames := make(map[string]struct{})
	for _, route := range completedSchemaSliceRoutes() {
		routeNames[route.Method] = struct{}{}
	}
	operations, err := protoapi.Operations()
	if err != nil {
		return nil, nil, err
	}
	var matchedOperations []protoapi.Operation
	descriptors := make(descriptorFixture, 0, len(routeNames))
	for _, operation := range operations {
		if operation.LegacyWireName == nil {
			continue
		}
		_, priorCompletedSlice := routeNames[*operation.LegacyWireName]
		worktreeSlice := operation.Descriptor.ParentFile().Package() == "kent.api.worktree"
		if !priorCompletedSlice && !worktreeSlice {
			continue
		}
		kind, err := legacyOperationKind(operation.Options.Kind)
		if err != nil {
			return nil, nil, err
		}
		matchedOperations = append(matchedOperations, operation)
		method := operation.Descriptor
		descriptors = append(descriptors, OperationDescriptor{
			Package:        string(method.ParentFile().Package()),
			Service:        string(method.Parent().Name()),
			Method:         string(method.Name()),
			LegacyWireName: operation.LegacyWireName,
			Kind:           kind,
			Event:          operationAssociationRef(operation.Event),
			Completion:     operationAssociationRef(operation.Completion),
		})
	}
	return matchedOperations, descriptors, nil
}

func routesForDescriptorProvenance(descriptors descriptorFixture) []apicontract.Route {
	byMethod := make(map[string]apicontract.Route)
	for _, route := range apicontract.Routes() {
		byMethod[route.Method] = route
	}
	routes := make([]apicontract.Route, 0, len(descriptors))
	for _, descriptor := range descriptors {
		if descriptor.LegacyWireName == nil {
			panic("completed schema descriptor has no legacy provenance")
		}
		route, exists := byMethod[*descriptor.LegacyWireName]
		if !exists {
			panic("completed schema descriptor provenance has no live route: " + *descriptor.LegacyWireName)
		}
		routes = append(routes, route)
	}
	return routes
}

func assertCompletedSchemaSliceMetadata(t *testing.T, routes []apicontract.Route, operations []protoapi.Operation) {
	t.Helper()
	byLegacyName := make(map[string]protoapi.Operation, len(operations))
	for _, operation := range operations {
		if operation.LegacyWireName == nil {
			t.Fatalf("%s has no legacy wire name", operation.ActiveName)
		}
		byLegacyName[*operation.LegacyWireName] = operation
	}
	for _, route := range routes {
		operation, exists := byLegacyName[route.Method]
		if !exists {
			t.Fatalf("%s has no generated operation", route.Method)
		}
		options := operation.Options
		if got := options.AuthenticationStage; got != generatedAuthenticationStage(route.Auth) {
			t.Errorf("%s authentication = %v, want %v", route.Method, got, route.Auth)
		}
		if got := options.ScopePolicy; got != generatedScopePolicy(route.Scope) {
			t.Errorf("%s scope = %v, want %v", route.Method, got, route.Scope)
		}
		wantDirection := sharedpb.Direction_DIRECTION_CLIENT_TO_SERVER
		if route.Kind == apicontract.KindNotification {
			wantDirection = sharedpb.Direction_DIRECTION_SERVER_TO_CLIENT
		}
		if options.Direction != wantDirection {
			t.Errorf("%s direction = %v, want %v", route.Method, options.Direction, wantDirection)
		}
		if route.Kind == apicontract.KindUnary {
			if got := options.UnaryConnection; got != generatedUnaryConnection(route.Connection) {
				t.Errorf("%s unary connection = %v, want %v", route.Method, got, route.Connection)
			}
		} else if options.UnaryConnection != sharedpb.UnaryConnection_UNARY_CONNECTION_UNSPECIFIED {
			t.Errorf("%s non-unary connection = %v", route.Method, options.UnaryConnection)
		}
		switch route.Kind {
		case apicontract.KindSubscription:
			if operation.Event == nil || operation.Completion == nil {
				t.Errorf("%s subscription associations = event:%v completion:%v", route.Method, operation.Event, operation.Completion)
			}
		default:
			if operation.Event != nil || operation.Completion != nil {
				t.Errorf("%s unexpectedly declares event/completion metadata", route.Method)
			}
		}
	}
}

func specifiedLegacyRoutes(t *testing.T) []apicontract.Route {
	t.Helper()
	routes := []apicontract.Route{
		mustLiveRoute(t, "workflow.create"),
		mustLiveRoute(t, "session.materializeWorkspaceChat"),
		mustLiveRoute(t, "worktree.create_target.resolve"),
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
		mustLiveRoute(t, protocol.MethodWorktreeSetupSubscribe),
		mustLiveRoute(t, protocol.MethodWorktreeSetupEvent),
		mustLiveRoute(t, protocol.MethodWorktreeSetupComplete),
	}
}

func specifiedWorktreeSetupDescriptors() descriptorFixture {
	event := ref("worktree", "SetupService", "Event")
	completion := ref("worktree", "SetupService", "Complete")
	subscribe := descriptor(
		"worktree",
		"SetupService",
		"Subscribe",
		protocol.MethodWorktreeSetupSubscribe,
		apicontract.KindSubscription,
	)
	subscribe.Event = &event
	subscribe.Completion = &completion
	return descriptorFixture{
		subscribe,
		descriptor("worktree", "SetupService", "Event", protocol.MethodWorktreeSetupEvent, apicontract.KindNotification),
		descriptor("worktree", "SetupService", "Complete", protocol.MethodWorktreeSetupComplete, apicontract.KindNotification),
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

func mustLiveRoute(t *testing.T, method string) apicontract.Route {
	t.Helper()
	route, exists := apicontract.RouteByMethod(method)
	if !exists {
		t.Fatalf("live route %q not found", method)
	}
	return route
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
