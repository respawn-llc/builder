package migrationcheck

import (
	"testing"

	"core/shared/apicontract"
)

func TestInspectExecutionTargetUsesLiveRoutesAndResolvesLockedPredecessors(t *testing.T) {
	report, err := InspectExecutionTarget()
	if err != nil {
		t.Fatal(err)
	}

	liveRoutes := apicontract.Routes()
	if len(report.Routes) != len(liveRoutes) {
		t.Fatalf("inspected routes = %d, live routes = %d", len(report.Routes), len(liveRoutes))
	}
	for i, live := range liveRoutes {
		inspected := report.Routes[i]
		if inspected != live {
			t.Fatalf("route %d inspection differs from live route:\ninspected = %+v\nlive = %+v", i, inspected, live)
		}
	}

	seen := make(map[Identity]struct{}, len(report.Predecessors))
	for _, predecessor := range report.Predecessors {
		if _, exists := seen[predecessor.Identity]; exists {
			t.Fatalf("locked predecessor identity resolved more than once: %s", predecessor.Identity)
		}
		seen[predecessor.Identity] = struct{}{}
		if predecessor.Object == nil {
			t.Fatalf("locked predecessor identity has no go/types object: %s", predecessor.Identity)
		}
	}
	want := expectedLockedPredecessorIdentities()
	if len(seen) != len(want) {
		t.Fatalf("resolved locked predecessor identities = %d, want %d", len(seen), len(want))
	}
	for identity := range want {
		if _, exists := seen[identity]; !exists {
			t.Errorf("locked predecessor identity was not resolved: %s", identity)
		}
	}
	for identity := range seen {
		if _, exists := want[identity]; !exists {
			t.Errorf("unexpected locked predecessor identity was resolved: %s", identity)
		}
	}
}

func TestInspectExecutionTargetDiscoversReachableScalarsAndValidatorDeclarations(t *testing.T) {
	report, err := InspectExecutionTarget()
	if err != nil {
		t.Fatal(err)
	}

	if len(report.NamedScalars) == 0 {
		t.Fatal("no route-reachable named scalars discovered")
	}
	for _, scalar := range report.NamedScalars {
		if scalar.Type == nil {
			t.Fatalf("scalar %s has no named go/types declaration", scalar.Identity)
		}
		for _, constant := range scalar.Constants {
			if constant == nil {
				t.Fatalf("scalar %s contains a nil typed constant", scalar.Identity)
			}
		}
	}

	if len(report.Validators) == 0 {
		t.Fatal("no route-reachable validators discovered")
	}
	for _, validator := range report.Validators {
		if validator.Function == nil {
			t.Fatalf("validator %s has no typed declaration", validator.Identity)
		}
		if validator.Fingerprint == "" {
			t.Fatalf("validator %s has no implementation fingerprint", validator.Identity)
		}
		if len(validator.Closure) == 0 || validator.Closure[0].PackagePath == "" {
			t.Fatalf("validator %s has no typed implementation closure", validator.Identity)
		}
		if validator.Function.Name() != "Validate" && validator.Function.Name() != "ValidateRPC" {
			t.Fatalf("unexpected validator method %s", validator.Identity)
		}
	}
}

func TestInspectExecutionTargetDiscoversKnownScalarAndValidatorDeclarations(t *testing.T) {
	report, err := InspectExecutionTarget()
	if err != nil {
		t.Fatal(err)
	}

	wantScalar := typeIdentity("core/shared/clientui", "UserTurnResultKind")
	var scalar *NamedScalar
	for index := range report.NamedScalars {
		if report.NamedScalars[index].Identity == wantScalar {
			scalar = &report.NamedScalars[index]
			break
		}
	}
	if scalar == nil {
		t.Fatalf("named scalar %s was not discovered", wantScalar)
	}
	wantConstants := map[string]struct{}{
		"UserTurnResultKindQueued":         {},
		"UserTurnResultKindNoFinal":        {},
		"UserTurnResultKindAssistantFinal": {},
		"UserTurnResultKindSilentFinal":    {},
	}
	if len(scalar.Constants) != len(wantConstants) {
		t.Fatalf("typed constants for %s = %d, want %d", wantScalar, len(scalar.Constants), len(wantConstants))
	}
	for _, constant := range scalar.Constants {
		if _, exists := wantConstants[constant.Name()]; !exists {
			t.Fatalf("unexpected typed constant %s for %s", constant.Name(), wantScalar)
		}
	}

	assertValidatorDiscovered(
		t,
		report,
		"core/shared/serverapi",
		"TaskSearchRequest",
		"Validate",
	)
	assertValidatorDiscovered(
		t,
		report,
		"core/shared/serverapi",
		"WorkflowGraphSavePreviewRequest",
		"ValidateRPC",
	)
}

func expectedLockedPredecessorIdentities() map[Identity]struct{} {
	identities := []Identity{
		fieldIdentity("core/shared/protocol", "HandshakeRequest", "ClientCapabilities"),
		typeIdentity("core/shared/protocol", "ClientCapabilities"),
		fieldIdentity("core/shared/protocol", "ClientCapabilities", "TranscriptLiveRunFinished"),
		fieldIdentity("core/shared/protocol", "ServerIdentity", "Capabilities"),
		typeIdentity("core/shared/protocol", "CapabilityFlags"),

		fieldIdentity("core/shared/serverapi", "ProcessKillRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RunPromptRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "SessionPlanRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "SessionPersistInputDraftRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "SessionRetargetWorkspaceRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "SessionResolveTransitionRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "SessionRuntimeActivateRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "SessionRuntimeReleaseRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeSetSessionNameRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeSetThinkingLevelRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeSetFastModeEnabledRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeSetReviewerEnabledRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeSetAutoCompactionEnabledRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeSetQuestionsEnabledRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeAppendCommittedEntryRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeSubmitUserTurnRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeSubmitUserShellCommandRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeCompactContextRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeInterruptRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeLiveSteerRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeLiveSteerResponse", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeLiveStopRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeDiscardQueuedUserMessageRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeRecordPromptHistoryRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeGoalSetRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeGoalStatusRequest", "ClientRequestID"),
		fieldIdentity("core/shared/serverapi", "RuntimeGoalClearRequest", "ClientRequestID"),

		fieldIdentity("core/shared/clientui", "QueuedUserMessage", "ClientRequestID"),
		fieldIdentity("core/shared/clientui", "RuntimeSubmitRequest", "ClientRequestID"),
		fieldIdentity("core/shared/clientui", "TranscriptQueuedMessageState", "ClientRequestID"),
		fieldIdentity("core/shared/clientui", "TranscriptUserMessageFlushed", "Messages"),
		typeIdentity("core/shared/clientui", "QueuedUserMessageIdentity"),

		fieldIdentity("core/shared/serverapi", "ProjectWorkspaceUnlinkBlocker", "Message"),
		fieldIdentity("core/shared/serverapi", "ProjectDeleteBlocker", "Message"),

		typeIdentity("core/shared/runtimeids", "RuntimeClientRequestID"),
		variableIdentity("core/shared/serverapi", "ErrClientRequestIDRequired"),
		functionIdentity("core/shared/serverapi", "validateClientRequestID"),
	}
	for _, fieldName := range []string{
		"JSONRPCWebSocket",
		"AuthBootstrap",
		"ProjectAttach",
		"SessionAttach",
		"HealthEndpoint",
		"ReadinessEndpoint",
		"RunPrompt",
		"SessionPlan",
		"SessionLifecycle",
		"SessionTranscript",
		"SessionRuntime",
		"RuntimeControl",
		"RuntimeLiveControl",
		"PromptControl",
		"ProcessOutput",
		"AttentionNotifications",
		"OnboardingFinalize",
		"PromptCommands",
	} {
		identities = append(identities, fieldIdentity("core/shared/protocol", "CapabilityFlags", fieldName))
	}
	for _, fieldName := range []string{"ClientRequestID", "QueueItemID"} {
		identities = append(identities, fieldIdentity("core/shared/clientui", "QueuedUserMessageIdentity", fieldName))
	}
	result := make(map[Identity]struct{}, len(identities))
	for _, identity := range identities {
		result[identity] = struct{}{}
	}
	return result
}

func assertValidatorDiscovered(t *testing.T, report Report, packagePath string, typeName string, methodName string) {
	t.Helper()
	want := Identity{
		PackagePath: packagePath,
		TypeName:    typeName,
		MemberName:  methodName,
		Kind:        IdentityFunction,
	}
	for _, validator := range report.Validators {
		if validator.Identity == want {
			return
		}
	}
	t.Fatalf("validator %s was not discovered", want)
}
