package protoapi_test

import (
	"testing"

	"core/shared/protoapi"
	chatpb "core/shared/protoapi/gen/kent/api/chat"
	chatsettingspb "core/shared/protoapi/gen/kent/api/chat_settings"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
)

const (
	chatContractSessionID = "8b0a92d4-18f8-4b5f-9b66-b8ac0f3f987e"
	chatContractQueueID   = "424e4b78-6516-4a31-89fb-7847cb2c9454"
)

func TestChatTargetSemanticUnion(t *testing.T) {
	questions := false
	autoCompaction := true
	for _, test := range []struct {
		name    string
		target  *chatpb.ChatTarget
		wantErr bool
	}{
		{
			name: "existing Session",
			target: &chatpb.ChatTarget{
				Target: &chatpb.ChatTarget_Session{
					Session: &chatpb.ExistingSessionTarget{SessionId: chatContractSessionID},
				},
			},
		},
		{
			name: "New Chat",
			target: &chatpb.ChatTarget{
				Target: &chatpb.ChatTarget_NewChat{
					NewChat: &chatpb.NewChatTarget{
						ProjectId:   "project-1",
						WorkspaceId: "workspace-1",
						InitialSettings: &chatpb.InitialChatSettings{
							AgentRole:             "default",
							Supervisor:            chatsettingspb.SupervisorValue_SUPERVISOR_VALUE_OFF,
							QuestionsEnabled:      &questions,
							AutoCompactionEnabled: &autoCompaction,
						},
					},
				},
			},
		},
		{name: "missing target", target: &chatpb.ChatTarget{}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := protoapi.Validate(test.target)
			if (err != nil) != test.wantErr {
				t.Fatalf("Validate() error = %v, want error %v", err, test.wantErr)
			}
		})
	}
}

func TestChatResultClassification(t *testing.T) {
	for _, test := range []struct {
		name        string
		result      *chatpb.SteerResult
		wantOutcome protoapi.OperationOutcome
		wantCode    string
		wantErr     bool
	}{
		{
			name: "accepted",
			result: &chatpb.SteerResult{
				Outcome: &chatpb.SteerResult_Success{
					Success: &chatpb.InputMutationSuccess{
						Session: &chatpb.ExistingSessionTarget{SessionId: chatContractSessionID},
						Outcome: &chatpb.InputMutationSuccess_Accepted{
							Accepted: &chatpb.InputAccepted{
								QueueItem: &chatpb.QueueItemIdentity{Id: chatContractQueueID},
							},
						},
					},
				},
			},
			wantOutcome: protoapi.OperationSuccess,
		},
		{
			name: "not accepted",
			result: &chatpb.SteerResult{
				Outcome: &chatpb.SteerResult_Success{
					Success: &chatpb.InputMutationSuccess{
						Session: &chatpb.ExistingSessionTarget{SessionId: chatContractSessionID},
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
			wantOutcome: protoapi.OperationSuccess,
		},
		{
			name: "typed failure",
			result: &chatpb.SteerResult{
				Outcome: &chatpb.SteerResult_Error{
					Error: &chatpb.ChatOperationError{
						Code: "session_not_found",
						Detail: &chatpb.ChatOperationError_SessionNotFound{
							SessionNotFound: &chatpb.SessionNotFoundDetails{SessionId: chatContractSessionID},
						},
					},
				},
			},
			wantOutcome: protoapi.OperationKnownFailure,
			wantCode:    "session_not_found",
		},
		{
			name: "malformed success",
			result: &chatpb.SteerResult{
				Outcome: &chatpb.SteerResult_Success{
					Success: &chatpb.InputMutationSuccess{
						Outcome: &chatpb.InputMutationSuccess_Accepted{
							Accepted: &chatpb.InputAccepted{
								QueueItem: &chatpb.QueueItemIdentity{Id: chatContractQueueID},
							},
						},
					},
				},
			},
			wantErr: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			classified, err := protoapi.ClassifyResult(test.result)
			if (err != nil) != test.wantErr {
				t.Fatalf("ClassifyResult() error = %v, want error %v", err, test.wantErr)
			}
			if test.wantErr {
				return
			}
			if classified.Outcome != test.wantOutcome {
				t.Fatalf("outcome = %v, want %v", classified.Outcome, test.wantOutcome)
			}
			if test.wantCode != "" && (classified.Failure == nil || classified.Failure.Code != test.wantCode) {
				t.Fatalf("failure = %+v, want code %q", classified.Failure, test.wantCode)
			}
		})
	}

	internal := &chatpb.SteerResult{
		Outcome: &chatpb.SteerResult_Error{
			Error: &chatpb.ChatOperationError{
				Code: "internal_failure",
				Detail: &chatpb.ChatOperationError_InternalFailure{
					InternalFailure: &sharedpb.InternalFailureDetails{},
				},
			},
		},
	}
	if _, err := protoapi.ClassifyResult(internal); err != nil {
		t.Fatalf("typed internal failure: %v", err)
	}
}
