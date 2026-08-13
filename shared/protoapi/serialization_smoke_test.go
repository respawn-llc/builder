package protoapi_test

import (
	"testing"

	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	workflowtaskpb "core/shared/protoapi/gen/kent/api/workflow_task"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	serializationSmokeSizeCeiling       = 16 * 1024
	serializationSmokeAllocationCeiling = 32
)

func TestGeneratedMessageSerializationStaysWithinRegressionCeilings(t *testing.T) {
	messages := []struct {
		name    string
		message proto.Message
	}{
		{
			name: "small",
			message: &sharedpb.InternalFailureDetails{
				Operation: stringPointer("load session"),
				Cause:     stringPointer("storage unavailable"),
			},
		},
		{
			name:    "nested",
			message: representativeBoard(),
		},
		{
			name: "event",
			message: &workflowtaskpb.AttentionNotificationEvent{
				Sequence: 1,
				Source:   workflowtaskpb.AttentionNotificationSource_ATTENTION_NOTIFICATION_SOURCE_SNAPSHOT,
				Type:     workflowtaskpb.AttentionNotificationEventType_ATTENTION_NOTIFICATION_EVENT_SNAPSHOT_COMPLETE,
				Payload: &workflowtaskpb.AttentionNotificationEvent_SnapshotComplete{
					SnapshotComplete: &workflowtaskpb.AttentionSnapshotComplete{SessionId: "session-1"},
				},
			},
		},
		{
			name: "error",
			message: &workflowtaskpb.SearchError{
				Code: "internal_failure",
				Detail: &workflowtaskpb.SearchError_InternalFailure{
					InternalFailure: &sharedpb.InternalFailureDetails{
						Operation: stringPointer("search tasks"),
						Cause:     stringPointer("index unavailable"),
					},
				},
			},
		},
	}

	marshal := proto.MarshalOptions{Deterministic: true}
	for _, test := range messages {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := marshal.Marshal(test.message)
			if err != nil {
				t.Fatalf("marshal representative message: %v", err)
			}
			if len(encoded) > serializationSmokeSizeCeiling {
				t.Fatalf("encoded size = %d bytes, ceiling = %d", len(encoded), serializationSmokeSizeCeiling)
			}

			allocations := testing.AllocsPerRun(100, func() {
				if _, err := marshal.Marshal(test.message); err != nil {
					panic(err)
				}
			})
			if allocations > serializationSmokeAllocationCeiling {
				t.Fatalf("allocations = %.0f, ceiling = %d", allocations, serializationSmokeAllocationCeiling)
			}
		})
	}
}

func TestSerializationSmokeSizeCeilingDetectsDuplicatedPayload(t *testing.T) {
	board := representativeBoard()
	column := &workflowtaskpb.BoardColumn{
		Node: &workflowtaskpb.BoardNodeSummary{
			NodeId:      "agent",
			Key:         "implementation",
			DisplayName: "Implementation",
		},
	}
	for range 1_000 {
		board.Board.Columns = append(board.Board.Columns, column)
	}

	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(board)
	if err != nil {
		t.Fatalf("marshal intentionally duplicated payload: %v", err)
	}
	if len(encoded) <= serializationSmokeSizeCeiling {
		t.Fatalf("duplicated payload encoded size = %d bytes, want above ceiling %d", len(encoded), serializationSmokeSizeCeiling)
	}
}

func representativeBoard() *workflowtaskpb.BoardGetSuccess {
	return &workflowtaskpb.BoardGetSuccess{
		Board: &workflowtaskpb.Board{
			ProjectId: "project-1",
			Project: &workflowtaskpb.BoardProject{
				ProjectKey:             "KENT",
				DisplayName:            "Kent",
				DefaultWorkspaceId:     "workspace-1",
				AttachedWorkspaceCount: 1,
			},
			GeneratedAt: &timestamppb.Timestamp{Seconds: 1_700_000_000},
		},
	}
}

func stringPointer(value string) *string {
	return &value
}
