package migrationcheck

import (
	"testing"
	"time"

	"core/shared/protoapi"
	runtimepb "core/shared/protoapi/gen/kent/api/runtime"
	sessionpb "core/shared/protoapi/gen/kent/api/session"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"core/shared/protocol"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestInteractiveSessionReadSchemasHaveExactLegacyProvenanceAndPolicies(t *testing.T) {
	assertSchemaRoutes(t, "kent.api.session", []string{
		protocol.MethodSessionGetMainView,
		protocol.MethodSessionGetExecutionEnvironment,
	})
}

func TestRuntimeSchemasHaveExactLegacyProvenanceAndPolicies(t *testing.T) {
	assertSchemaRoutes(t, "kent.api.runtime", []string{
		protocol.MethodRuntimeSetSessionName,
		protocol.MethodRuntimeSetThinkingLevel,
		protocol.MethodRuntimeSetFastModeEnabled,
		protocol.MethodRuntimeSetReviewerEnabled,
		protocol.MethodRuntimeSetAutoCompactionEnabled,
		protocol.MethodRuntimeSetQuestionsEnabled,
		protocol.MethodRuntimeShouldCompactBeforeUserMessage,
		protocol.MethodRuntimeSubmitUserTurn,
		protocol.MethodRuntimeSubmitUserShellCommand,
		protocol.MethodRuntimeCompactContext,
		protocol.MethodRuntimeInterrupt,
		protocol.MethodRuntimeLiveSteer,
		protocol.MethodRuntimeLiveStop,
		protocol.MethodRuntimeLiveWait,
		protocol.MethodRuntimeDiscardQueuedUserMessage,
		protocol.MethodRuntimeGoalShow,
		protocol.MethodRuntimeGoalSet,
		protocol.MethodRuntimeGoalPause,
		protocol.MethodRuntimeGoalResume,
		protocol.MethodRuntimeGoalComplete,
		protocol.MethodRuntimeGoalClear,
	})
}

func TestRuntimeDedicatedUnaryPoliciesRemainExactAfterKENT345Projection(t *testing.T) {
	for _, legacyName := range []string{
		protocol.MethodRuntimeSubmitUserTurn,
		protocol.MethodRuntimeSubmitUserShellCommand,
		protocol.MethodRuntimeCompactContext,
		protocol.MethodRuntimeInterrupt,
		protocol.MethodRuntimeLiveStop,
		protocol.MethodRuntimeLiveWait,
	} {
		operation := mustOperationByLegacyName(t, legacyName)
		if operation.Options.UnaryConnection != sharedpb.UnaryConnection_UNARY_CONNECTION_DEDICATED {
			t.Errorf("%s connection = %s, want dedicated", legacyName, operation.Options.UnaryConnection)
		}
	}
}

func TestInteractiveSessionRuntimeRetainsRealIdentitiesAndCustomUnions(t *testing.T) {
	assertExactFields(t, (&sessionlaunchpb.SessionRuntimeAttachment{}).ProtoReflect().Descriptor(), "session_id", "generation")
	assertExactFields(t, (&runtimepb.ActiveStep{}).ProtoReflect().Descriptor(), "run_id", "step_id", "active_kind")
	assertExactFields(t, (&runtimepb.SubmitUserTurnQueued{}).ProtoReflect().Descriptor(), "queue_item_id", "compacted", "steered")
	assertMessageOneofFields(t, (&runtimepb.SubmitUserTurnSuccess{}).ProtoReflect().Descriptor(), "result",
		"queued", "no_final", "assistant_final", "silent_final")
	assertMessageOneofFields(t, (&runtimepb.UserTurnInput{}).ProtoReflect().Descriptor(), "input", "text", "prompt_command")
	assertMessageOneofFields(t, (&sessionpb.ExecutionWorkspaceField{}).ProtoReflect().Descriptor(), "result",
		"available", "unavailable", "failed")
	assertMessageOneofFields(t, (&runtimepb.LiveWaitSuccess{}).ProtoReflect().Descriptor(), "result",
		"assistant_final_answer", "no_final_answer")
}

func TestInteractiveSessionRuntimeClosedScalarCoverageIsExact(t *testing.T) {
	enums := map[protoreflect.EnumDescriptor][]protoreflect.Name{
		runtimepb.ConversationFreshness_CONVERSATION_FRESHNESS_UNSPECIFIED.Descriptor(): {
			"CONVERSATION_FRESHNESS_UNSPECIFIED", "CONVERSATION_FRESHNESS_FRESH", "CONVERSATION_FRESHNESS_ESTABLISHED",
		},
		runtimepb.GoalStatus_RUNTIME_GOAL_STATUS_UNSPECIFIED.Descriptor(): {
			"RUNTIME_GOAL_STATUS_UNSPECIFIED", "RUNTIME_GOAL_STATUS_ACTIVE", "RUNTIME_GOAL_STATUS_PAUSED", "RUNTIME_GOAL_STATUS_COMPLETE",
		},
		runtimepb.ActivityState_RUNTIME_ACTIVITY_STATE_UNSPECIFIED.Descriptor(): {
			"RUNTIME_ACTIVITY_STATE_UNSPECIFIED", "RUNTIME_ACTIVITY_UNAVAILABLE", "RUNTIME_ACTIVITY_REGISTERED_IDLE",
			"RUNTIME_ACTIVITY_STARTING", "RUNTIME_ACTIVITY_RUNNING", "RUNTIME_ACTIVITY_AWAITING_PROMPT",
			"RUNTIME_ACTIVITY_DRAINING", "RUNTIME_ACTIVITY_CLOSING",
		},
		runtimepb.ActivityActiveKind_RUNTIME_ACTIVITY_ACTIVE_KIND_UNSPECIFIED.Descriptor(): {
			"RUNTIME_ACTIVITY_ACTIVE_KIND_UNSPECIFIED", "RUNTIME_ACTIVITY_ACTIVE_KIND_USER_TURN",
			"RUNTIME_ACTIVITY_ACTIVE_KIND_WORKFLOW_TURN", "RUNTIME_ACTIVITY_ACTIVE_KIND_GOAL_LOOP",
			"RUNTIME_ACTIVITY_ACTIVE_KIND_COMPACTION", "RUNTIME_ACTIVITY_ACTIVE_KIND_PRE_SUBMIT_COMPACTION",
			"RUNTIME_ACTIVITY_ACTIVE_KIND_USER_SHELL", "RUNTIME_ACTIVITY_ACTIVE_KIND_BACKGROUND",
			"RUNTIME_ACTIVITY_ACTIVE_KIND_RUNTIME_MAINTENANCE",
		},
		runtimepb.LiveStopStatus_RUNTIME_LIVE_STOP_STATUS_UNSPECIFIED.Descriptor(): {
			"RUNTIME_LIVE_STOP_STATUS_UNSPECIFIED", "RUNTIME_LIVE_STOP_STATUS_STOPPED", "RUNTIME_LIVE_STOP_STATUS_IDLE",
		},
		sessionpb.ExecutionFieldErrorCode_EXECUTION_FIELD_ERROR_CODE_UNSPECIFIED.Descriptor(): {
			"EXECUTION_FIELD_ERROR_CODE_UNSPECIFIED", "EXECUTION_FIELD_ERROR_CODE_SOURCE_FAILURE",
			"EXECUTION_FIELD_ERROR_CODE_INVALID_CONFIGURATION",
		},
		sessionpb.ExecutionBranchUnavailableReason_EXECUTION_BRANCH_UNAVAILABLE_REASON_UNSPECIFIED.Descriptor(): {
			"EXECUTION_BRANCH_UNAVAILABLE_REASON_UNSPECIFIED", "EXECUTION_BRANCH_UNAVAILABLE_REASON_DETACHED_HEAD",
			"EXECUTION_BRANCH_UNAVAILABLE_REASON_NOT_GIT_REPOSITORY",
		},
		sessionpb.ExecutionWorkspaceUnavailableReason_EXECUTION_WORKSPACE_UNAVAILABLE_REASON_UNSPECIFIED.Descriptor(): {
			"EXECUTION_WORKSPACE_UNAVAILABLE_REASON_UNSPECIFIED", "EXECUTION_WORKSPACE_UNAVAILABLE_REASON_NOT_CONFIGURED",
		},
		sessionpb.ExecutionAuthUnavailableReason_EXECUTION_AUTH_UNAVAILABLE_REASON_UNSPECIFIED.Descriptor(): {
			"EXECUTION_AUTH_UNAVAILABLE_REASON_UNSPECIFIED", "EXECUTION_AUTH_UNAVAILABLE_REASON_NOT_APPLICABLE",
		},
		sessionpb.ExecutionModelUnavailableReason_EXECUTION_MODEL_UNAVAILABLE_REASON_UNSPECIFIED.Descriptor(): {
			"EXECUTION_MODEL_UNAVAILABLE_REASON_UNSPECIFIED", "EXECUTION_MODEL_UNAVAILABLE_REASON_NOT_CONFIGURED",
		},
		sessionpb.ExecutionAuthMethod_EXECUTION_AUTH_METHOD_UNSPECIFIED.Descriptor(): {
			"EXECUTION_AUTH_METHOD_UNSPECIFIED", "EXECUTION_AUTH_METHOD_NONE", "EXECUTION_AUTH_METHOD_API_KEY",
			"EXECUTION_AUTH_METHOD_O_AUTH",
		},
	}
	for descriptor, values := range enums {
		assertExactEnumValues(t, descriptor, values...)
	}
}

func TestInteractiveSessionRuntimeLegacyFieldCoverageAndReshapesAreExact(t *testing.T) {
	for _, fixture := range []struct {
		message protoreflect.MessageDescriptor
		fields  []protoreflect.Name
	}{
		{(&runtimepb.ReadModelVersion{}).ProtoReflect().Descriptor(), []protoreflect.Name{"epoch", "generation", "sequence"}},
		{(&runtimepb.ActiveStep{}).ProtoReflect().Descriptor(), []protoreflect.Name{"run_id", "step_id", "active_kind"}},
		{(&runtimepb.Activity{}).ProtoReflect().Descriptor(), []protoreflect.Name{"state", "active_step", "queue_accepting", "diagnostic_recovery"}},
		{(&runtimepb.ContextUsage{}).ProtoReflect().Descriptor(), []protoreflect.Name{"used_tokens", "window_tokens", "cache_hit_percent"}},
		{(&runtimepb.GoalView{}).ProtoReflect().Descriptor(), []protoreflect.Name{"id", "objective", "status", "created_at", "updated_at", "availability", "suspended"}},
		{(&runtimepb.WorkflowSessionStatus{}).ProtoReflect().Descriptor(), []protoreflect.Name{"task_id", "workflow_id"}},
		{(&runtimepb.Status{}).ProtoReflect().Descriptor(), []protoreflect.Name{
			"reviewer_frequency", "reviewer_enabled", "auto_compaction_enabled", "questions_enabled",
			"fast_mode_available", "fast_mode_enabled", "conversation_freshness", "previous_session_id",
			"parent_agent_session_id", "navigation_target_session_id", "last_committed_assistant_final_answer",
			"thinking_level", "compaction_mode", "context_usage", "compaction_count", "goal", "workflow_session",
		}},
		{(&runtimepb.SessionView{}).ProtoReflect().Descriptor(), []protoreflect.Name{
			"session_id", "session_name", "agent_role", "conversation_freshness", "execution_target",
		}},
		{(&runtimepb.MainView{}).ProtoReflect().Descriptor(), []protoreflect.Name{"version", "status", "session", "activity"}},
		{(&sessionpb.ExecutionFieldError{}).ProtoReflect().Descriptor(), []protoreflect.Name{"code", "message"}},
		{(&sessionpb.ExecutionWorkspace{}).ProtoReflect().Descriptor(), []protoreflect.Name{"path"}},
		{(&sessionpb.ExecutionBranch{}).ProtoReflect().Descriptor(), []protoreflect.Name{"name"}},
		{(&sessionpb.ExecutionAuth{}).ProtoReflect().Descriptor(), []protoreflect.Name{"provider", "method"}},
		{(&sessionpb.ExecutionModel{}).ProtoReflect().Descriptor(), []protoreflect.Name{"name", "provider", "locked"}},
		{(&sessionpb.ExecutionEnvironment{}).ProtoReflect().Descriptor(), []protoreflect.Name{"session_id", "workspace", "branch", "auth", "model"}},
		{(&runtimepb.SetSessionNameRequest{}).ProtoReflect().Descriptor(), []protoreflect.Name{"session_id", "name"}},
		{(&runtimepb.SetThinkingLevelRequest{}).ProtoReflect().Descriptor(), []protoreflect.Name{"session_id", "level"}},
		{(&runtimepb.ToggleRequest{}).ProtoReflect().Descriptor(), []protoreflect.Name{"session_id", "enabled"}},
		{(&runtimepb.ToggleSuccess{}).ProtoReflect().Descriptor(), []protoreflect.Name{"changed"}},
		{(&runtimepb.ReviewerToggleSuccess{}).ProtoReflect().Descriptor(), []protoreflect.Name{"changed", "mode"}},
		{(&runtimepb.EffectiveToggleSuccess{}).ProtoReflect().Descriptor(), []protoreflect.Name{"changed", "enabled"}},
		{(&runtimepb.ShouldCompactRequest{}).ProtoReflect().Descriptor(), []protoreflect.Name{"session_id", "text"}},
		{(&runtimepb.ShouldCompactSuccess{}).ProtoReflect().Descriptor(), []protoreflect.Name{"should_compact"}},
		{(&runtimepb.PromptCommandInput{}).ProtoReflect().Descriptor(), []protoreflect.Name{"name", "arguments"}},
		{(&runtimepb.SubmitUserTurnQueued{}).ProtoReflect().Descriptor(), []protoreflect.Name{"queue_item_id", "compacted", "steered"}},
		{(&runtimepb.SubmitUserTurnNoFinal{}).ProtoReflect().Descriptor(), []protoreflect.Name{"compacted"}},
		{(&runtimepb.SubmitUserTurnAssistantFinal{}).ProtoReflect().Descriptor(), []protoreflect.Name{"message", "compacted"}},
		{(&runtimepb.SubmitUserTurnSilentFinal{}).ProtoReflect().Descriptor(), []protoreflect.Name{"message", "compacted"}},
		{(&runtimepb.ShellCommandRequest{}).ProtoReflect().Descriptor(), []protoreflect.Name{"session_id", "command"}},
		{(&runtimepb.CompactContextRequest{}).ProtoReflect().Descriptor(), []protoreflect.Name{"session_id", "args"}},
		{(&runtimepb.InterruptRequest{}).ProtoReflect().Descriptor(), []protoreflect.Name{"session_id"}},
		{(&runtimepb.InterruptSuccess{}).ProtoReflect().Descriptor(), []protoreflect.Name{"version", "activity"}},
		{(&runtimepb.LiveSteerRequest{}).ProtoReflect().Descriptor(), []protoreflect.Name{"session_id", "caller_session_id", "text"}},
		{(&runtimepb.LiveSteerSuccess{}).ProtoReflect().Descriptor(), []protoreflect.Name{"queue_item_id", "text"}},
		{(&runtimepb.LiveStopRequest{}).ProtoReflect().Descriptor(), []protoreflect.Name{"session_id"}},
		{(&runtimepb.LiveStopSuccess{}).ProtoReflect().Descriptor(), []protoreflect.Name{"status"}},
		{(&runtimepb.LiveWaitRequest{}).ProtoReflect().Descriptor(), []protoreflect.Name{"session_id"}},
		{(&runtimepb.LiveWaitSuccess{}).ProtoReflect().Descriptor(), []protoreflect.Name{
			"session_id", "session_name", "duration_millis", "live_run_group_id",
			"terminal_run_id", "terminal_step_id", "terminal_status", "assistant_final_answer", "no_final_answer",
		}},
		{(&runtimepb.DiscardQueuedUserMessageRequest{}).ProtoReflect().Descriptor(), []protoreflect.Name{"session_id", "queue_item_id"}},
		{(&runtimepb.DiscardQueuedUserMessageSuccess{}).ProtoReflect().Descriptor(), []protoreflect.Name{"discarded"}},
		{(&runtimepb.Goal{}).ProtoReflect().Descriptor(), []protoreflect.Name{"id", "objective", "status", "created_at", "updated_at"}},
		{(&runtimepb.GoalShowRequest{}).ProtoReflect().Descriptor(), []protoreflect.Name{"session_id"}},
		{(&runtimepb.GoalMutationRequest{}).ProtoReflect().Descriptor(), []protoreflect.Name{"session_id", "actor", "run_id", "step_id"}},
		{(&runtimepb.GoalSetRequest{}).ProtoReflect().Descriptor(), []protoreflect.Name{"session_id", "objective", "actor", "run_id", "step_id"}},
		{(&runtimepb.GoalClearRequest{}).ProtoReflect().Descriptor(), []protoreflect.Name{"session_id", "actor"}},
		{(&runtimepb.GoalShowSuccess{}).ProtoReflect().Descriptor(), []protoreflect.Name{"goal", "availability"}},
		{(&runtimepb.GoalMutationSuccess{}).ProtoReflect().Descriptor(), []protoreflect.Name{"goal", "pending", "availability"}},
	} {
		assertExactFields(t, fixture.message, fixture.fields...)
	}
}

func TestInteractiveSessionRuntimeReviewedValidationBoundaries(t *testing.T) {
	validID := "123e4567-e89b-42d3-a456-426614174000"
	validTimestamp := timestamppb.New(time.Unix(1_700_000_000, 0))
	zeroTimestamp := timestamppb.New(time.Time{})
	for name, message := range map[string]proto.Message{
		"blank read-model epoch": &runtimepb.ReadModelVersion{
			Epoch:      " ",
			Generation: 1,
			Sequence:   1,
		},
		"zero context window":         &runtimepb.ContextUsage{},
		"noncanonical prompt command": &runtimepb.PromptCommandInput{Name: "bogus"},
		"blank user turn text": &runtimepb.UserTurnInput{
			Input: &runtimepb.UserTurnInput_Text{Text: " "},
		},
		"blank assistant final": &runtimepb.SubmitUserTurnAssistantFinal{Message: " "},
		"blank goal ID": &runtimepb.Goal{
			Id:        " ",
			Objective: "objective",
			Status:    runtimepb.GoalStatus_RUNTIME_GOAL_STATUS_ACTIVE,
		},
		"blank goal objective": &runtimepb.Goal{
			Id:        "goal",
			Objective: " ",
			Status:    runtimepb.GoalStatus_RUNTIME_GOAL_STATUS_ACTIVE,
		},
		"zero goal creation time": &runtimepb.GoalView{
			Id:        "goal",
			Objective: "objective",
			Status:    runtimepb.GoalStatus_RUNTIME_GOAL_STATUS_ACTIVE,
			CreatedAt: zeroTimestamp,
			UpdatedAt: validTimestamp,
		},
		"zero goal update time": &runtimepb.GoalView{
			Id:        "goal",
			Objective: "objective",
			Status:    runtimepb.GoalStatus_RUNTIME_GOAL_STATUS_ACTIVE,
			CreatedAt: validTimestamp,
			UpdatedAt: zeroTimestamp,
		},
	} {
		if err := protoapi.ValidateGeneratedMessage(message); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}

	for name, request := range map[string]*sessionlaunchpb.SessionRetargetWorkspaceRequest{
		"traversal session ID": {SessionId: "../x", WorkspaceRoot: "/workspace"},
		"separator session ID": {SessionId: `a\b`, WorkspaceRoot: "/workspace"},
		"blank workspace root": {SessionId: "session", WorkspaceRoot: " "},
		"blank project ID":     {SessionId: "session", WorkspaceRoot: "/workspace", ProjectId: stringPointer(" ")},
	} {
		if err := protoapi.ValidateGeneratedMessage(request); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}

	validActivity := &runtimepb.Activity{
		State: runtimepb.ActivityState_RUNTIME_ACTIVITY_RUNNING,
		ActiveStep: &runtimepb.ActiveStep{
			RunId:      validID,
			StepId:     validID,
			ActiveKind: runtimepb.ActivityActiveKind_RUNTIME_ACTIVITY_ACTIVE_KIND_USER_TURN,
		},
		QueueAccepting: true,
	}
	if err := protoapi.ValidateGeneratedMessage(validActivity); err != nil {
		t.Fatalf("valid runtime activity: %v", err)
	}
	validActivity.ActiveStep = nil
	if err := protoapi.ValidateGeneratedMessage(validActivity); err == nil {
		t.Fatal("running runtime activity without Agent Step identity was accepted")
	}

	queued := &runtimepb.SubmitUserTurnQueued{QueueItemId: validID, Steered: true}
	if err := protoapi.ValidateGeneratedMessage(queued); err != nil {
		t.Fatalf("valid queued user turn: %v", err)
	}
	queued.Steered = false
	if err := protoapi.ValidateGeneratedMessage(queued); err == nil {
		t.Fatal("queued user turn without steering acceptance was accepted")
	}

	validWorkspace := &sessionpb.ExecutionWorkspaceField{
		Result: &sessionpb.ExecutionWorkspaceField_Available{
			Available: &sessionpb.ExecutionWorkspace{Path: "/workspace"},
		},
	}
	if err := protoapi.ValidateGeneratedMessage(validWorkspace); err != nil {
		t.Fatalf("valid execution workspace field: %v", err)
	}
	validWorkspace.Result = nil
	if err := protoapi.ValidateGeneratedMessage(validWorkspace); err == nil {
		t.Fatal("execution workspace field without a result was accepted")
	}
	for name, message := range map[string]proto.Message{
		"branch": &sessionpb.ExecutionBranchField{
			Result: &sessionpb.ExecutionBranchField_Unavailable{
				Unavailable: sessionpb.ExecutionBranchUnavailableReason_EXECUTION_BRANCH_UNAVAILABLE_REASON_DETACHED_HEAD,
			},
		},
		"auth": &sessionpb.ExecutionAuthField{
			Result: &sessionpb.ExecutionAuthField_Available{
				Available: &sessionpb.ExecutionAuth{
					Provider: "openai",
					Method:   sessionpb.ExecutionAuthMethod_EXECUTION_AUTH_METHOD_O_AUTH,
				},
			},
		},
		"model": &sessionpb.ExecutionModelField{
			Result: &sessionpb.ExecutionModelField_Unavailable{
				Unavailable: sessionpb.ExecutionModelUnavailableReason_EXECUTION_MODEL_UNAVAILABLE_REASON_NOT_CONFIGURED,
			},
		},
		"failure": &sessionpb.ExecutionFieldError{
			Code:    sessionpb.ExecutionFieldErrorCode_EXECUTION_FIELD_ERROR_CODE_SOURCE_FAILURE,
			Message: "source unavailable",
		},
	} {
		if err := protoapi.ValidateGeneratedMessage(message); err != nil {
			t.Fatalf("valid execution %s field: %v", name, err)
		}
	}
	for name, message := range map[string]proto.Message{
		"workspace": &sessionpb.ExecutionWorkspace{Path: " "},
		"branch":    &sessionpb.ExecutionBranch{Name: " "},
		"auth": &sessionpb.ExecutionAuth{
			Provider: " ",
			Method:   sessionpb.ExecutionAuthMethod_EXECUTION_AUTH_METHOD_O_AUTH,
		},
		"model name":     &sessionpb.ExecutionModel{Name: " ", Provider: "openai"},
		"model provider": &sessionpb.ExecutionModel{Name: "gpt", Provider: " "},
		"failure message": &sessionpb.ExecutionFieldError{
			Code:    sessionpb.ExecutionFieldErrorCode_EXECUTION_FIELD_ERROR_CODE_SOURCE_FAILURE,
			Message: " ",
		},
	} {
		if err := protoapi.ValidateGeneratedMessage(message); err == nil {
			t.Errorf("execution %s accepted blank text", name)
		}
	}

	silent := &runtimepb.SubmitUserTurnSuccess{
		Result: &runtimepb.SubmitUserTurnSuccess_SilentFinal{
			SilentFinal: &runtimepb.SubmitUserTurnSilentFinal{Message: ""},
		},
	}
	if err := protoapi.ValidateGeneratedMessage(silent); err != nil {
		t.Fatalf("valid silent final result: %v", err)
	}
	silent.GetSilentFinal().Message = "unexpected"
	if err := protoapi.ValidateGeneratedMessage(silent); err == nil {
		t.Fatal("silent final result with non-empty message was accepted")
	}

	noActiveRun := &runtimepb.RuntimeCommandNotAcceptedDetails{
		Cause: &runtimepb.RuntimeCommandNotAcceptedDetails_NoActiveRun{NoActiveRun: &emptypb.Empty{}},
	}
	if err := protoapi.ValidateGeneratedMessage(noActiveRun); err != nil {
		t.Fatalf("valid runtime-command rejection: %v", err)
	}

	for _, actor := range []string{"user", " agent ", "\tsystem\n"} {
		if err := protoapi.ValidateGeneratedMessage(&runtimepb.GoalClearRequest{
			SessionId: "session-1",
			Actor:     actor,
		}); err != nil {
			t.Fatalf("legacy-compatible actor %q: %v", actor, err)
		}
	}
	if err := protoapi.ValidateGeneratedMessage(&runtimepb.GoalClearRequest{
		SessionId: "session-1",
		Actor:     "operator",
	}); err == nil {
		t.Fatal("unknown goal actor was accepted")
	}

	validWait := &runtimepb.LiveWaitSuccess{
		SessionId:      validID,
		SessionName:    "session",
		LiveRunGroupId: validID,
		TerminalRunId:  validID,
		TerminalStepId: validID,
		TerminalStatus: "completed",
		Result: &runtimepb.LiveWaitSuccess_AssistantFinalAnswer{
			AssistantFinalAnswer: &runtimepb.LiveWaitAssistantFinalAnswer{Result: "done"},
		},
	}
	if err := protoapi.ValidateGeneratedMessage(validWait); err != nil {
		t.Fatalf("valid live-wait result: %v", err)
	}
	for name, mutate := range map[string]func(*runtimepb.LiveWaitSuccess){
		"live_run_group_id": func(message *runtimepb.LiveWaitSuccess) {
			message.LiveRunGroupId = "123E4567-E89B-42D3-A456-426614174000"
		},
		"terminal_run_id": func(message *runtimepb.LiveWaitSuccess) {
			message.TerminalRunId = "123e4567-e89b-12d3-a456-426614174000"
		},
		"terminal_step_id": func(message *runtimepb.LiveWaitSuccess) { message.TerminalStepId = "not-a-uuid" },
	} {
		copy := proto.Clone(validWait).(*runtimepb.LiveWaitSuccess)
		mutate(copy)
		if err := protoapi.ValidateGeneratedMessage(copy); err == nil {
			t.Errorf("live-wait result accepted invalid %s", name)
		}
	}
	for name, message := range map[string]proto.Message{
		"steer request": &runtimepb.LiveSteerRequest{
			SessionId: validID,
			Text:      " ",
		},
		"steer success": &runtimepb.LiveSteerSuccess{
			QueueItemId: validID,
			Text:        " ",
		},
		"assistant final answer": &runtimepb.LiveWaitAssistantFinalAnswer{Result: " "},
		"no final answer":        &runtimepb.LiveWaitNoFinalAnswer{Reason: " "},
	} {
		if err := protoapi.ValidateGeneratedMessage(message); err == nil {
			t.Errorf("%s accepted blank text", name)
		}
	}
	for name, mutate := range map[string]func(*runtimepb.LiveWaitSuccess){
		"session_name":    func(message *runtimepb.LiveWaitSuccess) { message.SessionName = " " },
		"terminal_status": func(message *runtimepb.LiveWaitSuccess) { message.TerminalStatus = " " },
	} {
		copy := proto.Clone(validWait).(*runtimepb.LiveWaitSuccess)
		mutate(copy)
		if err := protoapi.ValidateGeneratedMessage(copy); err == nil {
			t.Errorf("live-wait result accepted blank %s", name)
		}
	}
}

func TestInteractiveSessionRuntimeReviewedValidatorSignoffsRemainMessageLocal(t *testing.T) {
	want := map[Identity]struct{}{
		typeMethodIdentity("core/shared/clientui", "ReadModelVersion", "Validate"):                              {},
		typeMethodIdentity("core/shared/clientui", "RuntimeActiveStep", "Validate"):                             {},
		typeMethodIdentity("core/shared/clientui", "RuntimeActivity", "Validate"):                               {},
		typeMethodIdentity("core/shared/clientui", "RuntimeActivityActiveKind", "Validate"):                     {},
		typeMethodIdentity("core/shared/serverapi", "RuntimeCompactContextRequest", "Validate"):                 {},
		typeMethodIdentity("core/shared/serverapi", "RuntimeDiscardQueuedUserMessageRequest", "Validate"):       {},
		typeMethodIdentity("core/shared/serverapi", "RuntimeGoalClearRequest", "Validate"):                      {},
		typeMethodIdentity("core/shared/serverapi", "RuntimeGoalSetRequest", "Validate"):                        {},
		typeMethodIdentity("core/shared/serverapi", "RuntimeGoalShowRequest", "Validate"):                       {},
		typeMethodIdentity("core/shared/serverapi", "RuntimeGoalStatusRequest", "Validate"):                     {},
		typeMethodIdentity("core/shared/serverapi", "RuntimeInterruptRequest", "Validate"):                      {},
		typeMethodIdentity("core/shared/serverapi", "RuntimeLiveSteerRequest", "Validate"):                      {},
		typeMethodIdentity("core/shared/serverapi", "RuntimeLiveSteerResponse", "Validate"):                     {},
		typeMethodIdentity("core/shared/serverapi", "RuntimeLiveStopRequest", "Validate"):                       {},
		typeMethodIdentity("core/shared/serverapi", "RuntimeLiveStopResponse", "Validate"):                      {},
		typeMethodIdentity("core/shared/serverapi", "RuntimeLiveWaitRequest", "Validate"):                       {},
		typeMethodIdentity("core/shared/serverapi", "RuntimeLiveWaitResponse", "Validate"):                      {},
		typeMethodIdentity("core/shared/serverapi", "RuntimeSetAutoCompactionEnabledRequest", "Validate"):       {},
		typeMethodIdentity("core/shared/serverapi", "RuntimeSetFastModeEnabledRequest", "Validate"):             {},
		typeMethodIdentity("core/shared/serverapi", "RuntimeSetQuestionsEnabledRequest", "Validate"):            {},
		typeMethodIdentity("core/shared/serverapi", "RuntimeSetReviewerEnabledRequest", "Validate"):             {},
		typeMethodIdentity("core/shared/serverapi", "RuntimeSetSessionNameRequest", "Validate"):                 {},
		typeMethodIdentity("core/shared/serverapi", "RuntimeSetThinkingLevelRequest", "Validate"):               {},
		typeMethodIdentity("core/shared/serverapi", "RuntimeShouldCompactBeforeUserMessageRequest", "Validate"): {},
		typeMethodIdentity("core/shared/serverapi", "RuntimeSubmitUserShellCommandRequest", "Validate"):         {},
		typeMethodIdentity("core/shared/serverapi", "RuntimeSubmitUserTurnRequest", "Validate"):                 {},
		typeMethodIdentity("core/shared/serverapi", "RuntimeSubmitUserTurnResponse", "Validate"):                {},
		typeMethodIdentity("core/shared/serverapi", "SessionExecutionEnvironment", "Validate"):                  {},
		typeMethodIdentity("core/shared/serverapi", "SessionExecutionEnvironmentRequest", "Validate"):           {},
		typeMethodIdentity("core/shared/serverapi", "SessionExecutionEnvironmentResponse", "Validate"):          {},
		typeMethodIdentity("core/shared/serverapi", "SessionMainViewRequest", "Validate"):                       {},
	}
	for _, signoff := range ExecutionTargetDomainSignoffs() {
		if signoff.Domain != "interactive_session" {
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
		t.Errorf("missing message-local interactive Session/Runtime validator sign-off %s", identity)
	}
}

func TestInteractiveSessionRuntimeContainsNoGenericRequestIdentityOrOpaqueProviderPayload(t *testing.T) {
	operations, err := protoapi.Operations()
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range operations {
		pkg := string(operation.Descriptor.ParentFile().Package())
		if pkg != "kent.api.session" && pkg != "kent.api.runtime" {
			continue
		}
		for _, message := range []protoreflect.MessageDescriptor{
			operation.Descriptor.Input(),
			operation.Descriptor.Output(),
		} {
			walkMessageFields(message, func(field protoreflect.FieldDescriptor) {
				switch field.Name() {
				case "request_id", "client_request_id", "provider_payload", "raw_json":
					t.Errorf("%s contains forbidden field %s", field.ContainingMessage().FullName(), field.Name())
				}
			})
		}
	}
}

func assertSchemaRoutes(t *testing.T, packageName protoreflect.FullName, legacyNames []string) {
	t.Helper()
	operations, err := protoapi.Operations()
	if err != nil {
		t.Fatal(err)
	}
	byLegacyName := make(map[string]protoapi.Operation)
	for _, operation := range operations {
		if operation.Descriptor.ParentFile().Package() != packageName {
			continue
		}
		if operation.LegacyWireName == nil {
			t.Fatalf("%s has no migration provenance", operation.ActiveName)
		}
		byLegacyName[*operation.LegacyWireName] = operation
	}
	if len(byLegacyName) != len(legacyNames) {
		t.Fatalf("%s descriptors = %d, want %d", packageName, len(byLegacyName), len(legacyNames))
	}
	for _, legacyName := range legacyNames {
		_, exists := byLegacyName[legacyName]
		if !exists {
			t.Errorf("%s missing descriptor provenance %q", packageName, legacyName)
			continue
		}
	}
}

func operationByLegacyName(t *testing.T, legacyName string) *protoapi.Operation {
	t.Helper()
	operations, err := protoapi.Operations()
	if err != nil {
		t.Fatal(err)
	}
	for index := range operations {
		if operations[index].LegacyWireName != nil && *operations[index].LegacyWireName == legacyName {
			return &operations[index]
		}
	}
	return nil
}

func walkMessageFields(message protoreflect.MessageDescriptor, visit func(protoreflect.FieldDescriptor)) {
	seen := make(map[protoreflect.FullName]struct{})
	var walk func(protoreflect.MessageDescriptor)
	walk = func(current protoreflect.MessageDescriptor) {
		if _, exists := seen[current.FullName()]; exists {
			return
		}
		seen[current.FullName()] = struct{}{}
		fields := current.Fields()
		for index := 0; index < fields.Len(); index++ {
			field := fields.Get(index)
			visit(field)
			if field.Message() != nil {
				walk(field.Message())
			}
		}
	}
	walk(message)
}
