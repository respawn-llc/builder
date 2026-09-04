package transport

import (
	"context"
	"sync/atomic"
	"testing"

	"core/shared/apicontract"
	"core/shared/protoapi"
	chatpb "core/shared/protoapi/gen/kent/api/chat"
	chatsettingspb "core/shared/protoapi/gen/kent/api/chat_settings"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestGatewayChatMutationsInvokeServiceOnceForEveryTargetContext(t *testing.T) {
	fixture := newRoutePolicyFixture(t)
	projectless := ""
	for _, mutation := range chatMutationTransportCases() {
		for _, targetCase := range []struct {
			name      string
			target    *chatpb.ChatTarget
			state     *connectionState
			projectID *string
		}{
			{
				name: "projectless existing Session",
				target: &chatpb.ChatTarget{
					Target: &chatpb.ChatTarget_Session{
						Session: &chatpb.ExistingSessionTarget{SessionId: fixture.ownSessionID},
					},
				},
				state:     &connectionState{},
				projectID: &projectless,
			},
			{
				name: "attached Project existing Session",
				target: &chatpb.ChatTarget{
					Target: &chatpb.ChatTarget_Session{
						Session: &chatpb.ExistingSessionTarget{SessionId: fixture.ownSessionID},
					},
				},
				state: &connectionState{
					attachedProject:     fixture.bindingA.ProjectID,
					attachedWorkspaceID: fixture.bindingA.WorkspaceID,
				},
			},
			{
				name: "exact New Chat Project and workspace",
				target: newChatRoutePolicyTarget(
					fixture.bindingA.ProjectID,
					fixture.bindingA.WorkspaceID,
				),
				state: &connectionState{
					attachedProject:     fixture.bindingA.ProjectID,
					attachedWorkspaceID: fixture.bindingA.WorkspaceID,
				},
			},
		} {
			t.Run(string(mutation.methodName)+"/"+targetCase.name, func(t *testing.T) {
				service := &countingChatMutationService{resolvedSessionID: fixture.ownSessionID}
				deps := &chatAuthorizationGatewayDependencies{
					GatewayDependencies: fixture.appCore,
					service:             service,
					projectID:           targetCase.projectID,
				}
				gateway, err := NewGateway(deps, gatewayTestIdentity())
				if err != nil {
					t.Fatalf("NewGateway: %v", err)
				}

				result, failure := dispatchChatMutation(
					t,
					gateway,
					targetCase.state,
					mutation.methodName,
					mutation.request(targetCase.target),
				)
				if failure != nil {
					t.Fatalf("Chat mutation transport failure: %+v", failure)
				}
				classified, err := protoapi.ClassifyResult(result)
				if err != nil {
					t.Fatalf("classify Chat mutation result: %v", err)
				}
				if classified.Outcome != protoapi.OperationSuccess {
					t.Fatalf("Chat mutation result = %+v, want success", classified)
				}
				if deps.selectionCount.Load() != 1 {
					t.Fatalf("Chat service selections = %d, want 1", deps.selectionCount.Load())
				}
				if service.callCount.Load() != 1 {
					t.Fatalf("Chat service calls = %d, want 1", service.callCount.Load())
				}
			})
		}
	}
}

func TestGatewayChatTargetMismatchesRejectBeforeServiceSelection(t *testing.T) {
	fixture := newRoutePolicyFixture(t)
	projectless := ""
	missingSessionID := "6ff7ace4-e08b-43fc-b425-73242f0b3d26"
	for _, tc := range []struct {
		name      string
		target    *chatpb.ChatTarget
		state     *connectionState
		projectID *string
		wantCode  string
	}{
		{
			name: "attached Project foreign Session",
			target: &chatpb.ChatTarget{
				Target: &chatpb.ChatTarget_Session{
					Session: &chatpb.ExistingSessionTarget{SessionId: fixture.foreignSessionID},
				},
			},
			state: &connectionState{
				attachedProject:     fixture.bindingA.ProjectID,
				attachedWorkspaceID: fixture.bindingA.WorkspaceID,
			},
			wantCode: "session_not_found",
		},
		{
			name: "projectless missing Session",
			target: &chatpb.ChatTarget{
				Target: &chatpb.ChatTarget_Session{
					Session: &chatpb.ExistingSessionTarget{SessionId: missingSessionID},
				},
			},
			state:     &connectionState{},
			projectID: &projectless,
			wantCode:  "session_not_found",
		},
		{
			name: "wrong Project",
			target: newChatRoutePolicyTarget(
				fixture.bindingB.ProjectID,
				fixture.bindingB.WorkspaceID,
			),
			state: &connectionState{
				attachedProject:     fixture.bindingA.ProjectID,
				attachedWorkspaceID: fixture.bindingA.WorkspaceID,
			},
			wantCode: "workspace_not_registered",
		},
		{
			name: "wrong Workspace",
			target: newChatRoutePolicyTarget(
				fixture.bindingA.ProjectID,
				fixture.bindingB.WorkspaceID,
			),
			state: &connectionState{
				attachedProject:     fixture.bindingA.ProjectID,
				attachedWorkspaceID: fixture.bindingA.WorkspaceID,
			},
			wantCode: "workspace_not_registered",
		},
		{
			name: "Workspace bound to another Project",
			target: newChatRoutePolicyTarget(
				fixture.bindingA.ProjectID,
				fixture.bindingB.WorkspaceID,
			),
			state: &connectionState{
				attachedProject:     fixture.bindingA.ProjectID,
				attachedWorkspaceID: fixture.bindingB.WorkspaceID,
			},
			wantCode: "workspace_not_registered",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &countingChatMutationService{resolvedSessionID: fixture.ownSessionID}
			deps := &chatAuthorizationGatewayDependencies{
				GatewayDependencies: fixture.appCore,
				service:             service,
				projectID:           tc.projectID,
			}
			gateway, err := NewGateway(deps, gatewayTestIdentity())
			if err != nil {
				t.Fatalf("NewGateway: %v", err)
			}

			result, failure := dispatchChatMutation(
				t,
				gateway,
				tc.state,
				"Steer",
				chatSteerRequest(tc.target),
			)
			if failure != nil {
				t.Fatalf("Chat mutation transport failure: %+v", failure)
			}
			classified, err := protoapi.ClassifyResult(result)
			if err != nil {
				t.Fatalf("classify Chat mutation result: %v", err)
			}
			if classified.Outcome != protoapi.OperationKnownFailure ||
				classified.Failure == nil ||
				classified.Failure.Code != tc.wantCode {
				t.Fatalf("Chat mutation result = %+v, want %q failure", classified, tc.wantCode)
			}
			if deps.selectionCount.Load() != 0 {
				t.Fatalf("Chat service selections = %d, want 0", deps.selectionCount.Load())
			}
			if service.callCount.Load() != 0 {
				t.Fatalf("Chat service calls = %d, want 0", service.callCount.Load())
			}
		})
	}
}

func TestGatewayMalformedChatTargetRejectsBeforeServiceSelection(t *testing.T) {
	fixture := newRoutePolicyFixture(t)
	for _, target := range []*chatpb.ChatTarget{
		nil,
		{Target: &chatpb.ChatTarget_Session{}},
	} {
		service := &countingChatMutationService{resolvedSessionID: fixture.ownSessionID}
		deps := &chatAuthorizationGatewayDependencies{
			GatewayDependencies: fixture.appCore,
			service:             service,
		}
		gateway, err := NewGateway(deps, gatewayTestIdentity())
		if err != nil {
			t.Fatalf("NewGateway: %v", err)
		}

		result, failure := dispatchChatMutation(
			t,
			gateway,
			&connectionState{
				attachedProject:     fixture.bindingA.ProjectID,
				attachedWorkspaceID: fixture.bindingA.WorkspaceID,
			},
			"Steer",
			chatSteerRequest(target),
		)
		if result != nil {
			t.Fatalf("malformed Chat target result = %v, want no result", result)
		}
		if failure == nil ||
			failure.Code != sharedpb.TransportFailureCode_TRANSPORT_FAILURE_CODE_INVALID_PAYLOAD {
			t.Fatalf("malformed Chat target failure = %+v, want invalid payload", failure)
		}
		if deps.selectionCount.Load() != 0 {
			t.Fatalf("Chat service selections = %d, want 0", deps.selectionCount.Load())
		}
		if service.callCount.Load() != 0 {
			t.Fatalf("Chat service calls = %d, want 0", service.callCount.Load())
		}
	}
}

type chatAuthorizationGatewayDependencies struct {
	GatewayDependencies
	service        apicontract.ChatMutationService
	projectID      *string
	selectionCount atomic.Int32
}

func (d *chatAuthorizationGatewayDependencies) ChatMutationClient() apicontract.ChatMutationService {
	d.selectionCount.Add(1)
	return d.service
}

func (d *chatAuthorizationGatewayDependencies) ProjectID() string {
	if d.projectID != nil {
		return *d.projectID
	}
	return d.GatewayDependencies.ProjectID()
}

type countingChatMutationService struct {
	resolvedSessionID string
	callCount         atomic.Int32
}

func (s *countingChatMutationService) Steer(
	context.Context,
	*chatpb.SteerRequest,
) (*chatpb.InputMutationSuccess, error) {
	s.callCount.Add(1)
	return acceptedChatInputMutation(s.resolvedSessionID), nil
}

func (s *countingChatMutationService) Queue(
	context.Context,
	*chatpb.QueueRequest,
) (*chatpb.InputMutationSuccess, error) {
	s.callCount.Add(1)
	return acceptedChatInputMutation(s.resolvedSessionID), nil
}

func (s *countingChatMutationService) Compact(
	context.Context,
	*chatpb.CompactRequest,
) (*chatpb.CompactionMutationSuccess, error) {
	s.callCount.Add(1)
	return &chatpb.CompactionMutationSuccess{
		Session: &chatpb.ExistingSessionTarget{SessionId: s.resolvedSessionID},
		Outcome: &chatpb.CompactionMutationSuccess_Accepted{
			Accepted: &chatpb.CompactionAccepted{
				Request: &chatpb.CompactionRequestIdentity{
					Id: "8b0a92d4-18f8-4b5f-9b66-b8ac0f3f987e",
				},
			},
		},
	}, nil
}

func acceptedChatInputMutation(sessionID string) *chatpb.InputMutationSuccess {
	return &chatpb.InputMutationSuccess{
		Session: &chatpb.ExistingSessionTarget{SessionId: sessionID},
		Outcome: &chatpb.InputMutationSuccess_Accepted{
			Accepted: &chatpb.InputAccepted{
				QueueItem: &chatpb.QueueItemIdentity{
					Id: "424e4b78-6516-4a31-89fb-7847cb2c9454",
				},
			},
		},
	}
}

func newChatRoutePolicyTarget(projectID string, workspaceID string) *chatpb.ChatTarget {
	questions := false
	autoCompaction := true
	return &chatpb.ChatTarget{
		Target: &chatpb.ChatTarget_NewChat{
			NewChat: &chatpb.NewChatTarget{
				ProjectId:   projectID,
				WorkspaceId: workspaceID,
				InitialSettings: &chatpb.InitialChatSettings{
					AgentRole:             "default",
					Supervisor:            chatsettingspb.SupervisorValue_SUPERVISOR_VALUE_OFF,
					QuestionsEnabled:      &questions,
					AutoCompactionEnabled: &autoCompaction,
				},
			},
		},
	}
}

type chatMutationTransportCase struct {
	methodName protoreflect.Name
	request    func(*chatpb.ChatTarget) proto.Message
}

func chatMutationTransportCases() []chatMutationTransportCase {
	return []chatMutationTransportCase{
		{methodName: "Steer", request: func(target *chatpb.ChatTarget) proto.Message {
			return chatSteerRequest(target)
		}},
		{methodName: "Queue", request: func(target *chatpb.ChatTarget) proto.Message {
			return &chatpb.QueueRequest{
				Target: target,
				Activation: &chatpb.Activation{
					Input: &chatpb.Activation_Text{Text: "exact text"},
				},
			}
		}},
		{methodName: "Compact", request: func(target *chatpb.ChatTarget) proto.Message {
			return &chatpb.CompactRequest{
				Target:     target,
				Invocation: &chatpb.CompactionInvocation{Token: "/compact"},
			}
		}},
	}
}

func chatSteerRequest(target *chatpb.ChatTarget) *chatpb.SteerRequest {
	return &chatpb.SteerRequest{
		Target: target,
		Activation: &chatpb.Activation{
			Input: &chatpb.Activation_Text{Text: "exact text"},
		},
	}
}

func dispatchChatMutation(
	t *testing.T,
	gateway *Gateway,
	state *connectionState,
	methodName protoreflect.Name,
	request proto.Message,
) (proto.Message, *sharedpb.TransportFailure) {
	t.Helper()
	service := chatpb.File_kent_api_chat_chat_proto.Services().ByName("ChatService")
	method := service.Methods().ByName(methodName)
	operation, err := protoapi.OperationFromDescriptor(method)
	if err != nil {
		t.Fatalf("resolve Chat operation: %v", err)
	}
	binding, exists := gateway.registration.BinaryBinding(operation.Name)
	if !exists {
		t.Fatalf("Chat operation %q has no binary binding", operation.Name)
	}
	payload, err := protoapi.Marshal(request)
	if err != nil {
		t.Fatalf("marshal Chat request: %v", err)
	}
	correlation := "chat-authorization"
	_, result, failure := gateway.dispatchBinary(
		context.Background(),
		state,
		gatewayBinaryRequest{
			binding: binding,
			call: &sharedpb.Call{
				Operation:   operation.Name,
				Correlation: &correlation,
				Payload:     payload,
			},
		},
	)
	return result, failure
}
