package protoapi_test

import (
	"testing"

	"core/shared/protoapi"
	chatpb "core/shared/protoapi/gen/kent/api/chat"
	chatsettingspb "core/shared/protoapi/gen/kent/api/chat_settings"
	runtimepb "core/shared/protoapi/gen/kent/api/runtime"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestChatTargetRequiresOneTarget(t *testing.T) {
	valid := &chatpb.ChatTarget{
		Target: &chatpb.ChatTarget_Session{
			Session: &chatpb.ExistingSessionTarget{
				SessionId: "8b0a92d4-18f8-4b5f-9b66-b8ac0f3f987e",
			},
		},
	}
	if err := protoapi.Validate(valid); err != nil {
		t.Fatalf("valid existing-Session target: %v", err)
	}
	if err := protoapi.Validate(&chatpb.ChatTarget{}); err == nil {
		t.Fatal("target without a selected variant validated")
	}
	if err := protoapi.Validate(&chatpb.ChatTarget{
		Target: &chatpb.ChatTarget_Session{},
	}); err == nil {
		t.Fatal("target with a malformed selected variant validated")
	}

	var multiplySelected chatpb.ChatTarget
	err := protojson.Unmarshal([]byte(`{
		"session":{"sessionId":"8b0a92d4-18f8-4b5f-9b66-b8ac0f3f987e"},
		"newChat":{
			"projectId":"project-1",
			"workspaceId":"workspace-1",
			"initialSettings":{
				"agentRole":"default",
				"supervisor":"SUPERVISOR_VALUE_OFF",
				"questionsEnabled":false,
				"autoCompactionEnabled":true
			}
		}
	}`), &multiplySelected)
	if err == nil {
		t.Fatal("target with multiple selected variants decoded")
	}
}

func TestNewChatTargetRequiresCompleteInitialSettings(t *testing.T) {
	fast := false
	questions := false
	autoCompaction := true
	target := &chatpb.ChatTarget{
		Target: &chatpb.ChatTarget_NewChat{
			NewChat: &chatpb.NewChatTarget{
				ProjectId:   "project-1",
				WorkspaceId: "workspace-1",
				InitialSettings: &chatpb.InitialChatSettings{
					AgentRole:             "default",
					Supervisor:            chatsettingspb.SupervisorValue_SUPERVISOR_VALUE_OFF,
					Thinking:              stringPointer("medium"),
					Fast:                  &fast,
					QuestionsEnabled:      &questions,
					AutoCompactionEnabled: &autoCompaction,
				},
			},
		},
	}
	if err := protoapi.Validate(target); err != nil {
		t.Fatalf("complete New Chat settings: %v", err)
	}

	target.GetNewChat().InitialSettings.Thinking = nil
	target.GetNewChat().InitialSettings.Fast = nil
	if err := protoapi.Validate(target); err != nil {
		t.Fatalf("New Chat settings without unavailable optional capabilities: %v", err)
	}

	for name, mutate := range map[string]func(*chatpb.NewChatTarget){
		"initial settings": func(target *chatpb.NewChatTarget) {
			target.InitialSettings = nil
		},
		"Agent": func(target *chatpb.NewChatTarget) {
			target.InitialSettings.AgentRole = ""
		},
		"Supervisor": func(target *chatpb.NewChatTarget) {
			target.InitialSettings.Supervisor = chatsettingspb.SupervisorValue_SUPERVISOR_VALUE_UNSPECIFIED
		},
		"Questions": func(target *chatpb.NewChatTarget) {
			target.InitialSettings.QuestionsEnabled = nil
		},
		"Auto-compaction": func(target *chatpb.NewChatTarget) {
			target.InitialSettings.AutoCompactionEnabled = nil
		},
	} {
		t.Run("missing "+name, func(t *testing.T) {
			invalid := proto.Clone(target).(*chatpb.ChatTarget)
			mutate(invalid.GetNewChat())
			if err := protoapi.Validate(invalid); err == nil {
				t.Fatal("incomplete New Chat settings validated")
			}
		})
	}
}

func TestChatActivationPreservesExactTextAndCommandLexemes(t *testing.T) {
	text := "  preserve exact ordinary text  "
	ordinary := &chatpb.Activation{
		Input: &chatpb.Activation_Text{Text: text},
	}
	if err := protoapi.Validate(ordinary); err != nil {
		t.Fatalf("exact ordinary text: %v", err)
	}
	if ordinary.GetText() != text {
		t.Fatalf("ordinary text = %q, want %q", ordinary.GetText(), text)
	}

	command := &chatpb.CommandInvocation{
		CatalogIdentity:     "prompt:review",
		Token:               "/review",
		SeparatorWhitespace: "\t ",
		Arguments:           "keep  exact\narguments",
	}
	activation := &chatpb.Activation{
		Input: &chatpb.Activation_Command{Command: command},
	}
	if err := protoapi.Validate(activation); err != nil {
		t.Fatalf("exact command invocation: %v", err)
	}
	if reconstructed := command.Token + command.SeparatorWhitespace + command.Arguments; reconstructed != "/review\t keep  exact\narguments" {
		t.Fatalf("reconstructed command = %q", reconstructed)
	}

	for name, invalid := range map[string]*chatpb.Activation{
		"missing variant": {},
		"blank text": {
			Input: &chatpb.Activation_Text{Text: " \t "},
		},
		"noncanonical catalog identity": {
			Input: &chatpb.Activation_Command{Command: &chatpb.CommandInvocation{
				CatalogIdentity: "prompt:Review",
				Token:           "/review",
			}},
		},
		"token containing whitespace": {
			Input: &chatpb.Activation_Command{Command: &chatpb.CommandInvocation{
				CatalogIdentity: "prompt:review",
				Token:           "/review now",
			}},
		},
		"non-whitespace separator": {
			Input: &chatpb.Activation_Command{Command: &chatpb.CommandInvocation{
				CatalogIdentity:     "prompt:review",
				Token:               "/review",
				SeparatorWhitespace: "x",
			}},
		},
		"arguments without separator": {
			Input: &chatpb.Activation_Command{Command: &chatpb.CommandInvocation{
				CatalogIdentity: "prompt:review",
				Token:           "/review",
				Arguments:       "now",
			}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := protoapi.Validate(invalid); err == nil {
				t.Fatal("malformed activation validated")
			}
		})
	}
}

func TestManualCompactionInvocationPreservesExactLexemes(t *testing.T) {
	invocation := &chatpb.CompactionInvocation{
		Token:               "/compact",
		SeparatorWhitespace: " \t",
		RawGuidance:         "preserve  exact\nguidance ",
	}
	if err := protoapi.Validate(invocation); err != nil {
		t.Fatalf("exact compaction invocation: %v", err)
	}
	if reconstructed := invocation.Token + invocation.SeparatorWhitespace + invocation.RawGuidance; reconstructed != "/compact \tpreserve  exact\nguidance " {
		t.Fatalf("reconstructed compaction invocation = %q", reconstructed)
	}
	if err := protoapi.Validate(&chatpb.CompactionInvocation{Token: "/compact"}); err != nil {
		t.Fatalf("bare compaction invocation: %v", err)
	}

	for name, invalid := range map[string]*chatpb.CompactionInvocation{
		"wrong token": {
			Token: "/Compact",
		},
		"non-whitespace separator": {
			Token:               "/compact",
			SeparatorWhitespace: "-",
		},
		"guidance without separator": {
			Token:       "/compact",
			RawGuidance: "now",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := protoapi.Validate(invalid); err == nil {
				t.Fatal("malformed compaction invocation validated")
			}
		})
	}
}

func TestChatMutationIdentitiesRequireCanonicalUUIDv4(t *testing.T) {
	valid := "8b0a92d4-18f8-4b5f-9b66-b8ac0f3f987e"
	for name, identity := range map[string]proto.Message{
		"Queue Item":         &chatpb.QueueItemIdentity{Id: valid},
		"Compaction Request": &chatpb.CompactionRequestIdentity{Id: valid},
	} {
		t.Run(name, func(t *testing.T) {
			if err := protoapi.Validate(identity); err != nil {
				t.Fatalf("valid identity: %v", err)
			}
		})
	}

	for name, identity := range map[string]proto.Message{
		"Queue Item":         &chatpb.QueueItemIdentity{Id: "not-a-uuid"},
		"Compaction Request": &chatpb.CompactionRequestIdentity{Id: "8B0A92D4-18F8-4B5F-9B66-B8AC0F3F987E"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := protoapi.Validate(identity); err == nil {
				t.Fatal("invalid identity validated")
			}
		})
	}
}

func TestChatInputMutationResultsCarryResolvedSessionAndAcceptance(t *testing.T) {
	sessionID := "8b0a92d4-18f8-4b5f-9b66-b8ac0f3f987e"
	queueItemID := "424e4b78-6516-4a31-89fb-7847cb2c9454"
	accepted := &chatpb.SteerResult{
		Outcome: &chatpb.SteerResult_Success{
			Success: &chatpb.InputMutationSuccess{
				Session: &chatpb.ExistingSessionTarget{SessionId: sessionID},
				Outcome: &chatpb.InputMutationSuccess_Accepted{
					Accepted: &chatpb.InputAccepted{
						QueueItem: &chatpb.QueueItemIdentity{Id: queueItemID},
						Diagnostic: &chatpb.AcceptedDiagnostic{
							Detail: &chatpb.AcceptedDiagnostic_PromptHistoryFailure{
								PromptHistoryFailure: &sharedpb.InternalFailureDetails{Operation: stringPointer("record_prompt_history")},
							},
						},
					},
				},
			},
		},
	}
	classified, err := protoapi.ClassifyResult(accepted)
	if err != nil {
		t.Fatalf("accepted Steer result: %v", err)
	}
	if classified.Outcome != protoapi.OperationSuccess {
		t.Fatalf("accepted Steer result outcome = %v", classified.Outcome)
	}

	notAccepted := &chatpb.QueueResult{
		Outcome: &chatpb.QueueResult_Success{
			Success: &chatpb.InputMutationSuccess{
				Session: &chatpb.ExistingSessionTarget{SessionId: sessionID},
				Outcome: &chatpb.InputMutationSuccess_NotAccepted{
					NotAccepted: &chatpb.InputNotAccepted{
						Reason: &chatpb.InputNotAccepted_PendingWorkCapacity{
							PendingWorkCapacity: &chatpb.PendingWorkCapacityDetails{},
						},
					},
				},
			},
		},
	}
	classified, err = protoapi.ClassifyResult(notAccepted)
	if err != nil {
		t.Fatalf("not-accepted Queue result: %v", err)
	}
	if classified.Outcome != protoapi.OperationSuccess {
		t.Fatalf("not-accepted Queue result outcome = %v", classified.Outcome)
	}

	for name, malformed := range map[string]proto.Message{
		"missing top-level outcome": &chatpb.SteerResult{},
		"missing resolved Session": &chatpb.SteerResult{
			Outcome: &chatpb.SteerResult_Success{
				Success: &chatpb.InputMutationSuccess{
					Outcome: &chatpb.InputMutationSuccess_NotAccepted{
						NotAccepted: &chatpb.InputNotAccepted{
							Reason: &chatpb.InputNotAccepted_PendingWorkCapacity{
								PendingWorkCapacity: &chatpb.PendingWorkCapacityDetails{},
							},
						},
					},
				},
			},
		},
		"accepted without Queue Item identity": &chatpb.SteerResult{
			Outcome: &chatpb.SteerResult_Success{
				Success: &chatpb.InputMutationSuccess{
					Session: &chatpb.ExistingSessionTarget{SessionId: sessionID},
					Outcome: &chatpb.InputMutationSuccess_Accepted{
						Accepted: &chatpb.InputAccepted{},
					},
				},
			},
		},
		"accepted with malformed diagnostic": &chatpb.SteerResult{
			Outcome: &chatpb.SteerResult_Success{
				Success: &chatpb.InputMutationSuccess{
					Session: &chatpb.ExistingSessionTarget{SessionId: sessionID},
					Outcome: &chatpb.InputMutationSuccess_Accepted{
						Accepted: &chatpb.InputAccepted{
							QueueItem:  &chatpb.QueueItemIdentity{Id: queueItemID},
							Diagnostic: &chatpb.AcceptedDiagnostic{},
						},
					},
				},
			},
		},
		"not accepted without reason": &chatpb.QueueResult{
			Outcome: &chatpb.QueueResult_Success{
				Success: &chatpb.InputMutationSuccess{
					Session: &chatpb.ExistingSessionTarget{SessionId: sessionID},
					Outcome: &chatpb.InputMutationSuccess_NotAccepted{
						NotAccepted: &chatpb.InputNotAccepted{},
					},
				},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := protoapi.ClassifyResult(malformed); err == nil {
				t.Fatal("malformed Chat input result classified")
			}
		})
	}
}

func TestChatCompactionResultsCarryResolvedSessionAndRequestIdentity(t *testing.T) {
	sessionID := "8b0a92d4-18f8-4b5f-9b66-b8ac0f3f987e"
	requestID := "424e4b78-6516-4a31-89fb-7847cb2c9454"
	accepted := &chatpb.CompactResult{
		Outcome: &chatpb.CompactResult_Success{
			Success: &chatpb.CompactionMutationSuccess{
				Session: &chatpb.ExistingSessionTarget{SessionId: sessionID},
				Outcome: &chatpb.CompactionMutationSuccess_Accepted{
					Accepted: &chatpb.CompactionAccepted{
						Request: &chatpb.CompactionRequestIdentity{Id: requestID},
					},
				},
			},
		},
	}
	if classified, err := protoapi.ClassifyResult(accepted); err != nil {
		t.Fatalf("accepted compaction result: %v", err)
	} else if classified.Outcome != protoapi.OperationSuccess {
		t.Fatalf("accepted compaction result outcome = %v", classified.Outcome)
	}

	tooSoon := &chatpb.CompactResult{
		Outcome: &chatpb.CompactResult_Success{
			Success: &chatpb.CompactionMutationSuccess{
				Session: &chatpb.ExistingSessionTarget{SessionId: sessionID},
				Outcome: &chatpb.CompactionMutationSuccess_NotAccepted{
					NotAccepted: &chatpb.CompactionNotAccepted{
						Reason: &chatpb.CompactionNotAccepted_TooSoon{
							TooSoon: &chatpb.ManualCompactionTooSoonDetails{},
						},
					},
				},
			},
		},
	}
	if classified, err := protoapi.ClassifyResult(tooSoon); err != nil {
		t.Fatalf("not-accepted compaction result: %v", err)
	} else if classified.Outcome != protoapi.OperationSuccess {
		t.Fatalf("not-accepted compaction result outcome = %v", classified.Outcome)
	}

	for name, malformed := range map[string]proto.Message{
		"accepted without Compaction Request identity": &chatpb.CompactResult{
			Outcome: &chatpb.CompactResult_Success{
				Success: &chatpb.CompactionMutationSuccess{
					Session: &chatpb.ExistingSessionTarget{SessionId: sessionID},
					Outcome: &chatpb.CompactionMutationSuccess_Accepted{
						Accepted: &chatpb.CompactionAccepted{},
					},
				},
			},
		},
		"not accepted without reason": &chatpb.CompactResult{
			Outcome: &chatpb.CompactResult_Success{
				Success: &chatpb.CompactionMutationSuccess{
					Session: &chatpb.ExistingSessionTarget{SessionId: sessionID},
					Outcome: &chatpb.CompactionMutationSuccess_NotAccepted{
						NotAccepted: &chatpb.CompactionNotAccepted{},
					},
				},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := protoapi.ClassifyResult(malformed); err == nil {
				t.Fatal("malformed Chat compaction result classified")
			}
		})
	}
}

func TestChatMutationRequestsRequireTypedTargetAndInput(t *testing.T) {
	target := &chatpb.ChatTarget{
		Target: &chatpb.ChatTarget_Session{
			Session: &chatpb.ExistingSessionTarget{
				SessionId: "8b0a92d4-18f8-4b5f-9b66-b8ac0f3f987e",
			},
		},
	}
	activation := &chatpb.Activation{
		Input: &chatpb.Activation_Text{Text: "exact text"},
	}
	invocation := &chatpb.CompactionInvocation{Token: "/compact"}

	for name, request := range map[string]proto.Message{
		"Steer":   &chatpb.SteerRequest{Target: target, Activation: activation},
		"Queue":   &chatpb.QueueRequest{Target: target, Activation: activation},
		"Compact": &chatpb.CompactRequest{Target: target, Invocation: invocation},
	} {
		t.Run(name, func(t *testing.T) {
			if err := protoapi.Validate(request); err != nil {
				t.Fatalf("valid request: %v", err)
			}
		})
	}

	for name, request := range map[string]proto.Message{
		"Steer without target": &chatpb.SteerRequest{Activation: activation},
		"Steer without activation": &chatpb.SteerRequest{
			Target: target,
		},
		"Queue without target": &chatpb.QueueRequest{Activation: activation},
		"Queue without activation": &chatpb.QueueRequest{
			Target: target,
		},
		"Compact without target": &chatpb.CompactRequest{Invocation: invocation},
		"Compact without invocation": &chatpb.CompactRequest{
			Target: target,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := protoapi.Validate(request); err == nil {
				t.Fatal("incomplete request validated")
			}
		})
	}
}

func TestChatTargetFromRequestReturnsTheSelectedTarget(t *testing.T) {
	target := &chatpb.ChatTarget{
		Target: &chatpb.ChatTarget_Session{
			Session: &chatpb.ExistingSessionTarget{
				SessionId: "8b0a92d4-18f8-4b5f-9b66-b8ac0f3f987e",
			},
		},
	}
	activation := &chatpb.Activation{
		Input: &chatpb.Activation_Text{Text: "exact text"},
	}
	for name, request := range map[string]protoapi.ChatTargetRequest{
		"Steer": &chatpb.SteerRequest{
			Target:     target,
			Activation: activation,
		},
		"Queue": &chatpb.QueueRequest{
			Target:     target,
			Activation: activation,
		},
		"Compact": &chatpb.CompactRequest{
			Target:     target,
			Invocation: &chatpb.CompactionInvocation{Token: "/compact"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			selected, err := protoapi.ChatTargetFromRequest(request)
			if err != nil {
				t.Fatalf("Chat target from request: %v", err)
			}
			if !proto.Equal(selected, target) {
				t.Fatalf("selected target = %v, want %v", selected, target)
			}
		})
	}
}

func TestChatOperationErrorsRequireMatchingTypedDetails(t *testing.T) {
	sessionID := "8b0a92d4-18f8-4b5f-9b66-b8ac0f3f987e"
	valid := &chatpb.SteerResult{
		Outcome: &chatpb.SteerResult_Error{
			Error: &chatpb.ChatOperationError{
				Code: "session_not_found",
				Detail: &chatpb.ChatOperationError_SessionNotFound{
					SessionNotFound: &chatpb.SessionNotFoundDetails{SessionId: sessionID},
				},
			},
		},
	}
	classified, err := protoapi.ClassifyResult(valid)
	if err != nil {
		t.Fatalf("typed operation error: %v", err)
	}
	if classified.Outcome != protoapi.OperationKnownFailure ||
		classified.Failure == nil ||
		classified.Failure.Code != "session_not_found" {
		t.Fatalf("classified operation error = %+v", classified)
	}

	unknown := &chatpb.QueueResult{
		Outcome: &chatpb.QueueResult_Error{
			Error: &chatpb.ChatOperationError{Code: "future_failure"},
		},
	}
	classified, err = protoapi.ClassifyResult(unknown)
	if err != nil {
		t.Fatalf("unknown operation error: %v", err)
	}
	if classified.Outcome != protoapi.OperationGenericFailure ||
		classified.Failure == nil ||
		classified.Failure.Code != "future_failure" {
		t.Fatalf("classified unknown operation error = %+v", classified)
	}

	for name, malformed := range map[string]proto.Message{
		"empty code": &chatpb.SteerResult{
			Outcome: &chatpb.SteerResult_Error{
				Error: &chatpb.ChatOperationError{
					Detail: &chatpb.ChatOperationError_SessionNotFound{
						SessionNotFound: &chatpb.SessionNotFoundDetails{SessionId: sessionID},
					},
				},
			},
		},
		"known code without detail": &chatpb.QueueResult{
			Outcome: &chatpb.QueueResult_Error{
				Error: &chatpb.ChatOperationError{Code: "session_not_found"},
			},
		},
		"known code with wrong detail": &chatpb.CompactResult{
			Outcome: &chatpb.CompactResult_Error{
				Error: &chatpb.ChatOperationError{
					Code: "session_not_found",
					Detail: &chatpb.ChatOperationError_InternalFailure{
						InternalFailure: &sharedpb.InternalFailureDetails{},
					},
				},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := protoapi.ClassifyResult(malformed); err == nil {
				t.Fatal("malformed operation error classified")
			}
		})
	}
}

func TestChatMutationDescriptorsUseDedicatedChatTargetScope(t *testing.T) {
	service := chatpb.File_kent_api_chat_chat_proto.Services().ByName("ChatService")
	if service == nil {
		t.Fatal("generated ChatService descriptor is missing")
	}
	descriptorOperations, err := protoapi.Operations()
	if err != nil {
		t.Fatalf("load generated operation descriptors: %v", err)
	}
	operationsByName := make(map[string]protoapi.Operation, len(descriptorOperations))
	for _, operation := range descriptorOperations {
		operationsByName[operation.Name] = operation
	}
	for _, methodName := range []string{"Steer", "Queue", "Compact"} {
		t.Run(methodName, func(t *testing.T) {
			method := service.Methods().ByName(protoreflect.Name(methodName))
			if method == nil {
				t.Fatalf("generated ChatService.%s descriptor is missing", methodName)
			}
			operation, err := protoapi.OperationFromDescriptor(method)
			if err != nil {
				t.Fatalf("resolve generated Chat operation: %v", err)
			}
			if operation.Options.ScopePolicy != sharedpb.ScopePolicy_SCOPE_POLICY_CHAT_TARGET {
				t.Fatalf("scope = %s, want chat_target", operation.Options.ScopePolicy)
			}
			if operation.Options.UnaryConnection != sharedpb.UnaryConnection_UNARY_CONNECTION_DEDICATED {
				t.Fatalf("unary connection = %s, want dedicated", operation.Options.UnaryConnection)
			}
			if operation.LegacyWireName != nil {
				t.Fatalf("generated Chat operation has legacy alias %q", *operation.LegacyWireName)
			}
			embedded, ok := operationsByName[operation.Name]
			if !ok {
				t.Fatalf("generated descriptor set is missing %q", operation.Name)
			}
			if embedded.Options.ScopePolicy != sharedpb.ScopePolicy_SCOPE_POLICY_CHAT_TARGET {
				t.Fatalf("embedded scope = %s, want chat_target", embedded.Options.ScopePolicy)
			}
		})
	}

	submit := runtimepb.File_kent_api_runtime_runtime_proto.Services().
		ByName("TurnService").Methods().ByName("SubmitUserTurn")
	submitOperation, err := protoapi.OperationFromDescriptor(submit)
	if err != nil {
		t.Fatalf("resolve ordinary Submit User Turn operation: %v", err)
	}
	if submitOperation.Options.ScopePolicy != sharedpb.ScopePolicy_SCOPE_POLICY_SESSION_ACTIVE_PROJECT ||
		submitOperation.Options.UnaryConnection != sharedpb.UnaryConnection_UNARY_CONNECTION_DEDICATED ||
		submitOperation.LegacyWireName == nil ||
		*submitOperation.LegacyWireName != "runtime.submitUserTurn" {
		t.Fatalf("ordinary Submit User Turn descriptor changed: %+v", submitOperation)
	}
}

func stringPointer(value string) *string {
	return &value
}
