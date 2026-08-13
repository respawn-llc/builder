package migrationcheck

import (
	"testing"

	"core/shared/protoapi"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestWorktreeSetupStartIsSeparateFromEventsAndCompletion(t *testing.T) {
	operation := mustOperationByLegacyName(t, "worktree.setup.subscribe")
	assertMessageOneofFields(t, operation.Descriptor.Output(), "outcome", "success", "error")
	if operation.Event == nil || operation.Completion == nil {
		t.Fatal("setup subscription is missing event/completion association")
	}
	assertMessageOneofFields(t, operation.Event.Descriptor.Input(), "phase", "started", "completed", "not_required", "failed")
	if operation.Completion.Descriptor.Input().Fields().Len() != 2 {
		t.Fatalf("setup completion fields = %d, want code and diagnostic", operation.Completion.Descriptor.Input().Fields().Len())
	}
}

func TestWorktreeCustomUnionsAndClosedDomainsAreExhaustive(t *testing.T) {
	enums := map[protoreflect.EnumDescriptor][]protoreflect.Name{
		worktreepb.TopologyVariant_WORKTREE_TOPOLOGY_VARIANT_UNSPECIFIED.Descriptor(): {
			"WORKTREE_TOPOLOGY_VARIANT_UNSPECIFIED", "WORKTREE_TOPOLOGY_VARIANT_REGISTERED",
			"WORKTREE_TOPOLOGY_VARIANT_EXTERNAL", "WORKTREE_TOPOLOGY_VARIANT_MISSING",
		},
		worktreepb.SwitchOperationKind_WORKTREE_SWITCH_OPERATION_UNSPECIFIED.Descriptor(): {
			"WORKTREE_SWITCH_OPERATION_UNSPECIFIED", "WORKTREE_SWITCH_OPERATION_ENTER", "WORKTREE_SWITCH_OPERATION_LEAVE_MAIN",
		},
		worktreepb.StatusProblemKind_WORKTREE_STATUS_PROBLEM_UNSPECIFIED.Descriptor(): {
			"WORKTREE_STATUS_PROBLEM_UNSPECIFIED", "WORKTREE_STATUS_PROBLEM_ROOT_MISSING",
			"WORKTREE_STATUS_PROBLEM_ROOT_INACCESSIBLE", "WORKTREE_STATUS_PROBLEM_GIT_BINDING_MISSING",
			"WORKTREE_STATUS_PROBLEM_GIT_BINDING_MISMATCHED", "WORKTREE_STATUS_PROBLEM_RECORDED_REF_MISSING",
		},
		worktreepb.DirtyStateKind_DIRTY_STATE_UNSPECIFIED.Descriptor(): {
			"DIRTY_STATE_UNSPECIFIED", "DIRTY_STATE_CLEAN", "DIRTY_STATE_DIRTY", "DIRTY_STATE_UNKNOWN",
		},
		worktreepb.BranchCleanupMode_WORKTREE_BRANCH_CLEANUP_MODE_UNSPECIFIED.Descriptor(): {
			"WORKTREE_BRANCH_CLEANUP_MODE_UNSPECIFIED", "WORKTREE_BRANCH_CLEANUP_MODE_RETAIN",
			"WORKTREE_BRANCH_CLEANUP_MODE_AUTO_IF_KENT_CREATED", "WORKTREE_BRANCH_CLEANUP_MODE_DELETE_SAFE",
			"WORKTREE_BRANCH_CLEANUP_MODE_DELETE_FORCE",
		},
		worktreepb.BranchCleanupOutcomeKind_WORKTREE_BRANCH_CLEANUP_OUTCOME_UNSPECIFIED.Descriptor(): {
			"WORKTREE_BRANCH_CLEANUP_OUTCOME_UNSPECIFIED", "WORKTREE_BRANCH_CLEANUP_OUTCOME_NOT_REQUESTED",
			"WORKTREE_BRANCH_CLEANUP_OUTCOME_NOT_APPLICABLE", "WORKTREE_BRANCH_CLEANUP_OUTCOME_DELETED",
			"WORKTREE_BRANCH_CLEANUP_OUTCOME_RETAINED",
		},
		worktreepb.CreateTargetResolutionKind_WORKTREE_CREATE_TARGET_RESOLUTION_KIND_UNSPECIFIED.Descriptor(): {
			"WORKTREE_CREATE_TARGET_RESOLUTION_KIND_UNSPECIFIED", "WORKTREE_CREATE_TARGET_RESOLUTION_KIND_NEW_BRANCH",
			"WORKTREE_CREATE_TARGET_RESOLUTION_KIND_EXISTING_BRANCH", "WORKTREE_CREATE_TARGET_RESOLUTION_KIND_DETACHED_REF",
		},
		worktreepb.SelectorErrorKind_WORKTREE_SELECTOR_ERROR_KIND_UNSPECIFIED.Descriptor(): {
			"WORKTREE_SELECTOR_ERROR_KIND_UNSPECIFIED", "WORKTREE_SELECTOR_ERROR_KIND_NOT_FOUND",
			"WORKTREE_SELECTOR_ERROR_KIND_AMBIGUOUS", "WORKTREE_SELECTOR_ERROR_KIND_UNAVAILABLE",
		},
		worktreepb.ImmediateTransitionErrorKind_WORKTREE_IMMEDIATE_TRANSITION_UNSPECIFIED.Descriptor(): {
			"WORKTREE_IMMEDIATE_TRANSITION_UNSPECIFIED", "WORKTREE_IMMEDIATE_TRANSITION_ORIGIN_INACTIVE",
			"WORKTREE_IMMEDIATE_TRANSITION_APPLY_FAILED",
		},
		worktreepb.CreateErrorOwner_WORKTREE_CREATE_ERROR_OWNER_UNSPECIFIED.Descriptor(): {
			"WORKTREE_CREATE_ERROR_OWNER_UNSPECIFIED", "WORKTREE_CREATE_ERROR_OWNER_BASE_REF", "WORKTREE_CREATE_ERROR_OWNER_FORM",
		},
		worktreepb.SetupNotRequiredReason_WORKTREE_SETUP_NOT_REQUIRED_REASON_UNSPECIFIED.Descriptor(): {
			"WORKTREE_SETUP_NOT_REQUIRED_REASON_UNSPECIFIED", "WORKTREE_SETUP_NOT_REQUIRED_REASON_NO_TARGET_PREPARATION",
			"WORKTREE_SETUP_NOT_REQUIRED_REASON_NO_CONFIGURED_SCRIPT",
		},
		worktreepb.SetupRetryReadiness_WORKTREE_SETUP_RETRY_READINESS_UNSPECIFIED.Descriptor(): {
			"WORKTREE_SETUP_RETRY_READINESS_UNSPECIFIED", "WORKTREE_SETUP_RETRY_READY", "WORKTREE_SETUP_NON_RETRYABLE",
		},
	}
	for descriptor, names := range enums {
		assertExactEnumValues(t, descriptor, names...)
	}
	assertMessageOneofFields(t, (&worktreepb.TopologyEntry{}).ProtoReflect().Descriptor(), "topology", "registered", "external", "missing")
	assertMessageOneofFields(t, (&worktreepb.SetupFailureCause{}).ProtoReflect().Descriptor(), "cause",
		"process_exit",
		"timeout",
		"target_preparation",
		"interruption_persistence",
		"canceled",
		"controller_shutdown",
		"operational",
	)
	assertMessageOneofFields(t, (&worktreepb.DeleteSuccess{}).ProtoReflect().Descriptor(), "result", "completed", "scheduled")
}

func TestWorktreeOperationErrorUnionsAreExact(t *testing.T) {
	for _, fixture := range []struct {
		message protoreflect.MessageDescriptor
		details []protoreflect.Name
	}{
		{(&worktreepb.StatusError{}).ProtoReflect().Descriptor(), []protoreflect.Name{"internal_failure"}},
		{(&worktreepb.ListError{}).ProtoReflect().Descriptor(), []protoreflect.Name{"internal_failure"}},
		{(&worktreepb.WorkspaceListError{}).ProtoReflect().Descriptor(), []protoreflect.Name{"internal_failure"}},
		{(&worktreepb.SelectorResolveError{}).ProtoReflect().Descriptor(), []protoreflect.Name{"selector_error", "internal_failure"}},
		{(&worktreepb.DeletePreviewError{}).ProtoReflect().Descriptor(), []protoreflect.Name{"selector_error", "worktree_blocked", "internal_failure"}},
		{(&worktreepb.CreateTargetResolveError{}).ProtoReflect().Descriptor(), []protoreflect.Name{"internal_failure"}},
		{(&worktreepb.CreateError{}).ProtoReflect().Descriptor(), []protoreflect.Name{"create_failed", "setup_retained", "internal_failure"}},
		{(&worktreepb.EnterError{}).ProtoReflect().Descriptor(), []protoreflect.Name{"selector_error", "transition_pending", "immediate_transition", "internal_failure"}},
		{(&worktreepb.LeaveError{}).ProtoReflect().Descriptor(), []protoreflect.Name{"transition_pending", "immediate_transition", "internal_failure"}},
		{(&worktreepb.DeleteError{}).ProtoReflect().Descriptor(), []protoreflect.Name{"selector_error", "worktree_blocked", "delete_precondition", "transition_pending", "immediate_transition", "internal_failure"}},
		{(&worktreepb.SetupStartError{}).ProtoReflect().Descriptor(), []protoreflect.Name{"internal_failure"}},
	} {
		if fixture.message.Fields().Get(0).Name() != "code" {
			t.Errorf("%s first field is not code", fixture.message.FullName())
		}
		assertMessageOneofFields(t, fixture.message, "detail", fixture.details...)
	}
}

func TestWorktreeLegacyFieldCoverageAndIntentionalReshapesAreExact(t *testing.T) {
	for _, fixture := range []struct {
		message protoreflect.MessageDescriptor
		fields  []protoreflect.Name
	}{
		{(&worktreepb.GitFacts{}).ProtoReflect().Descriptor(), []protoreflect.Name{
			"canonical_root", "head_object", "branch_ref", "branch_name", "detached", "bare",
			"locked_reason", "prunable_reason", "is_main", "path_available",
		}},
		{(&worktreepb.KentFacts{}).ProtoReflect().Descriptor(), []protoreflect.Name{
			"worktree_id", "canonical_root", "display_name", "managed", "created_branch", "origin_session_id",
		}},
		{(&worktreepb.ListProjection{}).ProtoReflect().Descriptor(), []protoreflect.Name{
			"selector", "is_current", "switch", "delete_preview", "fallback_identity",
		}},
		{(&worktreepb.DeletePreviewOperation{}).ProtoReflect().Descriptor(), []protoreflect.Name{"selector"}},
		{(&worktreepb.SessionExecutionTarget{}).ProtoReflect().Descriptor(), []protoreflect.Name{
			"workspace_id", "workspace_name", "workspace_root", "workspace_availability", "worktree", "cwd_relpath", "effective_workdir",
		}},
		{(&worktreepb.SessionExecutionWorktreeTarget{}).ProtoReflect().Descriptor(), []protoreflect.Name{"id", "name", "root", "availability"}},
		{(&worktreepb.StatusProblem{}).ProtoReflect().Descriptor(), []protoreflect.Name{"kind", "root", "ref"}},
		{(&worktreepb.StatusTarget{}).ProtoReflect().Descriptor(), []protoreflect.Name{
			"recorded_root", "observed_root", "display_name", "recorded_branch_ref", "observed_branch_ref",
		}},
		{(&worktreepb.DirtyState{}).ProtoReflect().Descriptor(), []protoreflect.Name{"kind", "dirty_file_count", "unknown_cause"}},
		{(&worktreepb.BranchCleanupOutcome{}).ProtoReflect().Descriptor(), []protoreflect.Name{"kind", "branch_name", "diagnostic"}},
		{(&worktreepb.RuntimeStepOrigin{}).ProtoReflect().Descriptor(), []protoreflect.Name{"run_id", "step_id"}},
		{(&worktreepb.TransitionHeader{}).ProtoReflect().Descriptor(), []protoreflect.Name{"operation_id", "session_id", "origin"}},
		{(&worktreepb.SelectorCandidate{}).ProtoReflect().Descriptor(), []protoreflect.Name{
			"variant", "selector", "branch_name", "display_name", "fallback_identity",
		}},
		{(&worktreepb.SelectorErrorDetails{}).ProtoReflect().Descriptor(), []protoreflect.Name{"kind", "input", "candidates"}},
		{(&worktreepb.TransitionPendingDetails{}).ProtoReflect().Descriptor(), []protoreflect.Name{"session_id", "pending_operation_id"}},
		{(&worktreepb.ImmediateTransitionDetails{}).ProtoReflect().Descriptor(), []protoreflect.Name{"kind"}},
		{(&worktreepb.SetupRetainedDetails{}).ProtoReflect().Descriptor(), []protoreflect.Name{
			"worktree", "script_path", "diagnostic", "retained_previous_worktree",
		}},
		{(&worktreepb.DeletePreconditionDetails{}).ProtoReflect().Descriptor(), []protoreflect.Name{"dirty_state"}},
		{(&worktreepb.CreateFailureDetails{}).ProtoReflect().Descriptor(), []protoreflect.Name{"owner", "diagnostic"}},
		{(&worktreepb.StatusRequest{}).ProtoReflect().Descriptor(), []protoreflect.Name{"session_id"}},
		{(&worktreepb.StatusSuccess{}).ProtoReflect().Descriptor(), []protoreflect.Name{"target", "worktree", "problems"}},
		{(&worktreepb.ListRequest{}).ProtoReflect().Descriptor(), []protoreflect.Name{"session_id"}},
		{(&worktreepb.ListSuccess{}).ProtoReflect().Descriptor(), []protoreflect.Name{"target", "worktrees"}},
		{(&worktreepb.WorkspaceListRequest{}).ProtoReflect().Descriptor(), []protoreflect.Name{"project_id", "workspace_id"}},
		{(&worktreepb.WorkspaceListSuccess{}).ProtoReflect().Descriptor(), []protoreflect.Name{"workspace_id", "worktrees"}},
		{(&worktreepb.SelectorResolveRequest{}).ProtoReflect().Descriptor(), []protoreflect.Name{"session_id", "selector"}},
		{(&worktreepb.SelectorResolveSuccess{}).ProtoReflect().Descriptor(), []protoreflect.Name{"worktree"}},
		{(&worktreepb.DeletePreviewRequest{}).ProtoReflect().Descriptor(), []protoreflect.Name{"session_id", "selector"}},
		{(&worktreepb.DeletePreviewSuccess{}).ProtoReflect().Descriptor(), []protoreflect.Name{"worktree", "deletion_selector", "cleanliness"}},
		{(&worktreepb.CreateTargetResolveRequest{}).ProtoReflect().Descriptor(), []protoreflect.Name{"session_id", "target"}},
		{(&worktreepb.CreateTargetResolution{}).ProtoReflect().Descriptor(), []protoreflect.Name{"input", "kind", "resolved_ref"}},
		{(&worktreepb.CreateTargetResolveSuccess{}).ProtoReflect().Descriptor(), []protoreflect.Name{"resolution"}},
		{(&worktreepb.CreateRequest{}).ProtoReflect().Descriptor(), []protoreflect.Name{
			"setup_operation_id", "session_id", "base_ref", "create_branch", "branch_name", "root_path",
		}},
		{(&worktreepb.CreateSuccess{}).ProtoReflect().Descriptor(), []protoreflect.Name{"target", "worktree"}},
		{(&worktreepb.EnterRequest{}).ProtoReflect().Descriptor(), []protoreflect.Name{"transition", "selector"}},
		{(&worktreepb.LeaveRequest{}).ProtoReflect().Descriptor(), []protoreflect.Name{"transition"}},
		{(&worktreepb.DeleteRequest{}).ProtoReflect().Descriptor(), []protoreflect.Name{
			"transition", "selector", "force_folder_removal", "branch_cleanup_policy",
		}},
		{(&worktreepb.DeleteCompleted{}).ProtoReflect().Descriptor(), []protoreflect.Name{"cleanup", "leftover_root"}},
		{(&worktreepb.RetainedPreviousWorktree{}).ProtoReflect().Descriptor(), []protoreflect.Name{"worktree"}},
		{(&worktreepb.SetupExecutionTargetSelection{}).ProtoReflect().Descriptor(), []protoreflect.Name{"mode", "custom_ref"}},
		{(&worktreepb.SetupStarted{}).ProtoReflect().Descriptor(), []protoreflect.Name{"source_workspace_root", "worktree_root", "script_path"}},
		{(&worktreepb.SetupCompleted{}).ProtoReflect().Descriptor(), []protoreflect.Name{"retained_previous_worktree"}},
		{(&worktreepb.SetupNotRequired{}).ProtoReflect().Descriptor(), []protoreflect.Name{"reason", "retained_previous_worktree"}},
		{(&worktreepb.SetupProcessExit{}).ProtoReflect().Descriptor(), []protoreflect.Name{"exit_code", "stdout", "stderr"}},
		{(&worktreepb.SetupTimeout{}).ProtoReflect().Descriptor(), []protoreflect.Name{"stdout", "stderr"}},
		{(&worktreepb.SetupFailed{}).ProtoReflect().Descriptor(), []protoreflect.Name{
			"retry_readiness", "cause", "diagnostic", "script_path", "execution_target", "retained_worktree", "retained_previous_worktree",
		}},
		{(&worktreepb.SetupEvent{}).ProtoReflect().Descriptor(), []protoreflect.Name{
			"setup_operation_id", "started", "completed", "not_required", "failed",
		}},
		{(&worktreepb.SetupSubscribeRequest{}).ProtoReflect().Descriptor(), []protoreflect.Name{"setup_operation_id"}},
		{(&worktreepb.SetupCompletion{}).ProtoReflect().Descriptor(), []protoreflect.Name{"code", "diagnostic"}},
	} {
		assertExactFields(t, fixture.message, fixture.fields...)
	}

	// Legacy discriminator-plus-payload structs become exhaustive oneofs.
	assertMessageOneofFields(t, (&worktreepb.TopologyEntry{}).ProtoReflect().Descriptor(), "topology", "registered", "external", "missing")
	assertMessageOneofFields(t, (&worktreepb.DeleteSuccess{}).ProtoReflect().Descriptor(), "result", "completed", "scheduled")
	assertMessageOneofFields(t, (&worktreepb.SetupFailureCause{}).ProtoReflect().Descriptor(), "cause",
		"process_exit", "timeout", "target_preparation", "interruption_persistence", "canceled", "controller_shutdown", "operational",
	)
}

func TestWorktreeGitFactsRejectBlankPresentOptionalValues(t *testing.T) {
	valid := &worktreepb.GitFacts{
		CanonicalRoot: "/repo",
		HeadObject:    "abc123",
	}
	for _, field := range []struct {
		name string
		set  func(*worktreepb.GitFacts, *string)
	}{
		{name: "branch ref", set: func(facts *worktreepb.GitFacts, value *string) { facts.BranchRef = value }},
		{name: "branch name", set: func(facts *worktreepb.GitFacts, value *string) { facts.BranchName = value }},
		{name: "locked reason", set: func(facts *worktreepb.GitFacts, value *string) { facts.LockedReason = value }},
		{name: "prunable reason", set: func(facts *worktreepb.GitFacts, value *string) { facts.PrunableReason = value }},
	} {
		t.Run(field.name, func(t *testing.T) {
			facts := proto.Clone(valid).(*worktreepb.GitFacts)
			blank := " "
			field.set(facts, &blank)
			if err := protoapi.ValidateGeneratedMessage(facts); err == nil {
				t.Fatal("blank present Git fact accepted")
			}
		})
	}
}

func TestWorktreeRequestsRejectBlankSelectors(t *testing.T) {
	for name, message := range map[string]proto.Message{
		"selector resolve": &worktreepb.SelectorResolveRequest{SessionId: "session-1", Selector: " "},
		"delete preview":   &worktreepb.DeletePreviewRequest{SessionId: "session-1", Selector: " "},
		"enter":            &worktreepb.EnterRequest{Transition: validTransitionHeader(), Selector: " "},
		"delete": &worktreepb.DeleteRequest{
			Transition:          validTransitionHeader(),
			Selector:            " ",
			BranchCleanupPolicy: worktreepb.BranchCleanupMode_WORKTREE_BRANCH_CLEANUP_MODE_RETAIN,
		},
	} {
		if err := protoapi.ValidateGeneratedMessage(message); err == nil {
			t.Errorf("%s accepted blank selector", name)
		}
	}
}

func validTransitionHeader() *worktreepb.TransitionHeader {
	return &worktreepb.TransitionHeader{
		OperationId: "11111111-1111-4111-8111-111111111111",
		SessionId:   "22222222-2222-4222-8222-222222222222",
	}
}

func TestWorktreeSetupCompletionReshapeIsExact(t *testing.T) {
	completion := (&worktreepb.SetupCompletion{}).ProtoReflect().Descriptor()
	assertExactFields(t, completion, "code", "diagnostic")
	if completion.Fields().ByName("message") != nil {
		t.Fatal("setup completion retained legacy message instead of diagnostic reshape")
	}
	if completion.Fields().ByName("transcript_close_reason") != nil {
		t.Fatal("setup completion retained transcript-only close reason")
	}
}

func TestWorktreeSetupPreAcknowledgementFailureAndEventValidation(t *testing.T) {
	validID := "550e8400-e29b-41d4-a716-446655440000"
	startFailure := &worktreepb.SetupStartResult{
		Outcome: &worktreepb.SetupStartResult_Error{
			Error: &worktreepb.SetupStartError{
				Code: "internal_failure",
				Detail: &worktreepb.SetupStartError_InternalFailure{
					InternalFailure: &sharedpb.InternalFailureDetails{Operation: stringPointer("worktree.setup.subscribe")},
				},
			},
		},
	}
	if _, err := protoapi.ClassifyOperationResult(startFailure); err != nil {
		t.Fatalf("valid pre-ack failure: %v", err)
	}

	event := &worktreepb.SetupEvent{
		SetupOperationId: validID,
		Phase: &worktreepb.SetupEvent_Failed{
			Failed: &worktreepb.SetupFailed{
				RetryReadiness: worktreepb.SetupRetryReadiness_WORKTREE_SETUP_NON_RETRYABLE,
				Cause: &worktreepb.SetupFailureCause{
					Cause: &worktreepb.SetupFailureCause_Canceled{Canceled: &emptypb.Empty{}},
				},
				Diagnostic: "canceled",
			},
		},
	}
	if err := protoapi.ValidateGeneratedMessage(event); err != nil {
		t.Fatalf("valid setup event: %v", err)
	}
	event.GetFailed().Cause.Cause = &worktreepb.SetupFailureCause_Timeout{Timeout: &worktreepb.SetupTimeout{}}
	if err := protoapi.ValidateGeneratedMessage(event); err == nil {
		t.Fatal("setup event accepted mismatched cause kind and payload")
	}
}

func TestWorktreeSchemasContainNoGenericRequestID(t *testing.T) {
	operations, err := protoapi.Operations()
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range operations {
		if operation.Descriptor.ParentFile().Package() != "kent.api.worktree" {
			continue
		}
		for _, message := range []protoreflect.MessageDescriptor{
			operation.Descriptor.Input(),
			operation.Descriptor.Output(),
		} {
			if field := message.Fields().ByName("request_id"); field != nil {
				t.Errorf("%s unexpectedly contains generic request_id", message.FullName())
			}
		}
	}
}

func TestWorktreeProjectionValidatorsRemainMessageLocal(t *testing.T) {
	signoffs := ExecutionTargetDomainSignoffs()
	want := map[Identity]struct{}{
		typeMethodIdentity("core/shared/serverapi", "WorktreeCreateResponse", "Validate"):          {},
		typeMethodIdentity("core/shared/serverapi", "WorktreeListResponse", "Validate"):            {},
		typeMethodIdentity("core/shared/serverapi", "WorktreeSelectorPreviewResponse", "Validate"): {},
		typeMethodIdentity("core/shared/serverapi", "WorktreeWorkspaceListResponse", "Validate"):   {},
	}
	for _, signoff := range signoffs {
		if signoff.Domain != "worktree" {
			continue
		}
		for _, validator := range signoff.Classification.Validators {
			if _, expected := want[validator.Identity]; !expected {
				continue
			}
			if validator.Kind != ValidatorMessageLocal || validator.Owner != nil {
				t.Errorf("%s owner = %+v kind=%s", validator.Identity, validator.Owner, validator.Kind)
			}
			delete(want, validator.Identity)
		}
	}
	for identity := range want {
		t.Errorf("missing message-local Worktree validator sign-off %s", identity)
	}
}

func mustOperationByLegacyName(t *testing.T, legacyName string) protoapi.Operation {
	t.Helper()
	operations, err := protoapi.Operations()
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range operations {
		if operation.LegacyWireName != nil && *operation.LegacyWireName == legacyName {
			return operation
		}
	}
	t.Fatalf("missing descriptor provenance %q", legacyName)
	return protoapi.Operation{}
}

func assertMessageOneofFields(t *testing.T, message protoreflect.MessageDescriptor, oneofName protoreflect.Name, names ...protoreflect.Name) {
	t.Helper()
	oneof := message.Oneofs().ByName(oneofName)
	if oneof == nil {
		t.Fatalf("%s missing oneof %s", message.FullName(), oneofName)
	}
	if oneof.Fields().Len() != len(names) {
		t.Fatalf("%s.%s field count = %d, want %d", message.FullName(), oneofName, oneof.Fields().Len(), len(names))
	}
	for index, name := range names {
		if got := oneof.Fields().Get(index).Name(); got != name {
			t.Fatalf("%s.%s field %d = %s, want %s", message.FullName(), oneofName, index, got, name)
		}
	}
}

func stringPointer(value string) *string {
	return &value
}

func typeMethodIdentity(packagePath, typeName, methodName string) Identity {
	return Identity{
		PackagePath: packagePath,
		TypeName:    typeName,
		MemberName:  methodName,
		Kind:        IdentityFunction,
	}
}

var _ proto.Message = (*worktreepb.SetupEvent)(nil)
