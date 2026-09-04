package transport

import (
	"context"
	"testing"

	"core/shared/apicontract"
	"core/shared/protoapi"
	chatpb "core/shared/protoapi/gen/kent/api/chat"
	chatsettingspb "core/shared/protoapi/gen/kent/api/chat_settings"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"

	"google.golang.org/protobuf/proto"
)

func TestGatewayAuthorizesChatTargetModes(t *testing.T) {
	fixture := newRoutePolicyFixture(t)
	projectless := ""
	for _, test := range []struct {
		name      string
		target    *chatpb.ChatTarget
		state     *connectionState
		projectID *string
		wantCode  string
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
			name:   "exact New Chat binding",
			target: newChatRoutePolicyTarget(fixture.bindingA.ProjectID, fixture.bindingA.WorkspaceID),
			state: &connectionState{
				attachedProject:     fixture.bindingA.ProjectID,
				attachedWorkspaceID: fixture.bindingA.WorkspaceID,
			},
		},
		{
			name: "foreign existing Session",
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
			name:   "mismatched New Chat binding",
			target: newChatRoutePolicyTarget(fixture.bindingB.ProjectID, fixture.bindingB.WorkspaceID),
			state: &connectionState{
				attachedProject:     fixture.bindingA.ProjectID,
				attachedWorkspaceID: fixture.bindingA.WorkspaceID,
			},
			wantCode: "workspace_not_registered",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			gateway, err := NewGateway(
				&chatAuthorizationGatewayDependencies{
					GatewayDependencies: fixture.appCore,
					service:             chatAuthorizationService{sessionID: fixture.ownSessionID},
					projectID:           test.projectID,
				},
				gatewayTestIdentity(),
			)
			if err != nil {
				t.Fatalf("NewGateway: %v", err)
			}

			result, failure := dispatchChatSteer(
				t,
				gateway,
				test.state,
				chatSteerRequest(test.target),
			)
			if failure != nil {
				t.Fatalf("Chat mutation transport failure: %+v", failure)
			}
			classified, err := protoapi.ClassifyResult(result)
			if err != nil {
				t.Fatalf("classify Chat mutation result: %v", err)
			}
			if test.wantCode == "" {
				if classified.Outcome != protoapi.OperationSuccess {
					t.Fatalf("outcome = %+v, want success", classified)
				}
				return
			}
			if classified.Failure == nil || classified.Failure.Code != test.wantCode {
				t.Fatalf("failure = %+v, want code %q", classified.Failure, test.wantCode)
			}
		})
	}
}

type chatAuthorizationGatewayDependencies struct {
	GatewayDependencies
	service   apicontract.ChatMutationService
	projectID *string
}

func (d *chatAuthorizationGatewayDependencies) ChatMutationClient() apicontract.ChatMutationService {
	return d.service
}

func (d *chatAuthorizationGatewayDependencies) ProjectID() string {
	if d.projectID != nil {
		return *d.projectID
	}
	return d.GatewayDependencies.ProjectID()
}

type chatAuthorizationService struct {
	sessionID string
}

func (s chatAuthorizationService) Steer(context.Context, *chatpb.SteerRequest) (*chatpb.InputMutationSuccess, error) {
	return acceptedChatInputMutation(s.sessionID), nil
}

func (s chatAuthorizationService) Queue(context.Context, *chatpb.QueueRequest) (*chatpb.InputMutationSuccess, error) {
	return acceptedChatInputMutation(s.sessionID), nil
}

func (s chatAuthorizationService) Compact(context.Context, *chatpb.CompactRequest) (*chatpb.CompactionMutationSuccess, error) {
	panic("unexpected Compact call")
}

func acceptedChatInputMutation(sessionID string) *chatpb.InputMutationSuccess {
	return &chatpb.InputMutationSuccess{
		Session: &chatpb.ExistingSessionTarget{SessionId: sessionID},
		Outcome: &chatpb.InputMutationSuccess_Accepted{
			Accepted: &chatpb.InputAccepted{
				QueueItem: &chatpb.QueueItemIdentity{Id: "424e4b78-6516-4a31-89fb-7847cb2c9454"},
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

func chatSteerRequest(target *chatpb.ChatTarget) *chatpb.SteerRequest {
	return &chatpb.SteerRequest{
		Target: target,
		Activation: &chatpb.Activation{
			Input: &chatpb.Activation_Text{Text: "exact text"},
		},
	}
}

func dispatchChatSteer(
	t *testing.T,
	gateway *Gateway,
	state *connectionState,
	request *chatpb.SteerRequest,
) (proto.Message, *sharedpb.TransportFailure) {
	t.Helper()
	method := chatpb.File_kent_api_chat_chat_proto.Services().
		ByName("ChatService").
		Methods().
		ByName("Steer")
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
