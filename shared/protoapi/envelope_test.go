package protoapi_test

import (
	"bytes"
	"testing"

	"core/shared/protoapi"
	attentionpb "core/shared/protoapi/gen/kent/api/attention"
	serverpb "core/shared/protoapi/gen/kent/api/server"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"google.golang.org/protobuf/proto"
)

func TestEnvelopeRoundTripsEveryVariant(t *testing.T) {
	correlation := "connection-call-1"
	readinessResultPayload, err := proto.Marshal(&serverpb.GetReadinessResult{
		Outcome: &serverpb.GetReadinessResult_Success{
			Success: &serverpb.GetReadinessSuccess{Readiness: &serverpb.Readiness{
				Ready:           true,
				ServerId:        "server-1",
				ServerVersion:   "1.0.0",
				ServerBuild:     "build-1",
				ProtocolVersion: "1",
			}},
		},
	})
	if err != nil {
		t.Fatalf("marshal result payload: %v", err)
	}
	notificationPayload, err := proto.Marshal(&attentionpb.NotificationEvent{
		Sequence: 1,
		Payload: &attentionpb.NotificationEvent_SnapshotComplete{
			SnapshotComplete: &attentionpb.SnapshotComplete{SessionId: "session-1"},
		},
	})
	if err != nil {
		t.Fatalf("marshal event payload: %v", err)
	}
	tests := []struct {
		name     string
		envelope *sharedpb.Envelope
	}{
		{
			name: "call with correlation",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_Call{Call: &sharedpb.Call{
				Operation:   "kent.api.server.server_service.get_readiness",
				Correlation: &correlation,
				Payload:     []byte{},
			}}},
		},
		{
			name: "result with correlation",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_Result{Result: &sharedpb.Result{
				Operation:   "kent.api.server.server_service.get_readiness",
				Correlation: &correlation,
				Payload:     readinessResultPayload,
			}}},
		},
		{
			name: "notification or event",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_NotificationEvent{NotificationEvent: &sharedpb.NotificationEvent{
				Operation: "kent.api.attention.session_service.event",
				Payload:   notificationPayload,
			}}},
		},
		{
			name: "transport failure with correlation",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_TransportFailure{TransportFailure: &sharedpb.TransportFailure{
				Code:        sharedpb.TransportFailureCode_TRANSPORT_FAILURE_CODE_MALFORMED_ENVELOPE,
				Correlation: &correlation,
			}}},
		},
		{
			name: "transport failure without correlation",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_TransportFailure{TransportFailure: &sharedpb.TransportFailure{
				Code: sharedpb.TransportFailureCode_TRANSPORT_FAILURE_CODE_UNKNOWN_OPERATION,
			}}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := protoapi.MarshalEnvelope(test.envelope)
			if err != nil {
				t.Fatalf("marshal envelope: %v", err)
			}
			decoded, err := protoapi.UnmarshalEnvelope(encoded)
			if err != nil {
				t.Fatalf("unmarshal envelope: %v", err)
			}
			reencoded, err := protoapi.MarshalEnvelope(decoded)
			if err != nil {
				t.Fatalf("re-marshal envelope: %v", err)
			}
			if !bytes.Equal(reencoded, encoded) {
				t.Fatalf("round-trip bytes = %v, want %v", reencoded, encoded)
			}
		})
	}
}

func TestEnvelopeRejectsOperationFrameAndDirectionMismatches(t *testing.T) {
	notificationPayload, err := proto.Marshal(&attentionpb.NotificationEvent{
		Sequence: 1,
		Payload: &attentionpb.NotificationEvent_SnapshotComplete{
			SnapshotComplete: &attentionpb.SnapshotComplete{SessionId: "session-1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		envelope *sharedpb.Envelope
	}{
		{
			name: "server notification as call",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_Call{Call: &sharedpb.Call{
				Operation: "kent.api.attention.session_service.event",
				Payload:   notificationPayload,
			}}},
		},
		{
			name: "server notification as result",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_Result{Result: &sharedpb.Result{
				Operation: "kent.api.attention.session_service.event",
				Payload:   []byte{},
			}}},
		},
		{
			name: "client call as notification",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_NotificationEvent{NotificationEvent: &sharedpb.NotificationEvent{
				Operation: "kent.api.server.server_service.get_readiness",
				Payload:   []byte{},
			}}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := protoapi.MarshalEnvelope(test.envelope); err == nil {
				t.Fatal("operation/frame mismatch unexpectedly marshaled")
			}
			raw, err := proto.Marshal(test.envelope)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := protoapi.UnmarshalEnvelope(raw); err == nil {
				t.Fatal("operation/frame mismatch unexpectedly unmarshaled")
			}
		})
	}
}

func TestEnvelopeRejectsMalformedVariants(t *testing.T) {
	correlation := "connection-call-1"
	tests := []struct {
		name     string
		envelope *sharedpb.Envelope
	}{
		{name: "missing frame", envelope: &sharedpb.Envelope{}},
		{
			name: "call missing operation",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_Call{Call: &sharedpb.Call{
				Payload: []byte{1},
			}}},
		},
		{
			name: "call missing payload",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_Call{Call: &sharedpb.Call{
				Operation: "kent.api.server.server_service.get_readiness",
			}}},
		},
		{
			name: "call empty correlation",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_Call{Call: &sharedpb.Call{
				Operation:   "kent.api.server.server_service.get_readiness",
				Correlation: new(string),
				Payload:     []byte{1},
			}}},
		},
		{
			name: "result missing payload",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_Result{Result: &sharedpb.Result{
				Operation:   "kent.api.server.server_service.get_readiness",
				Correlation: &correlation,
			}}},
		},
		{
			name: "notification missing payload",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_NotificationEvent{NotificationEvent: &sharedpb.NotificationEvent{
				Operation: "kent.api.attention.session_service.event",
			}}},
		},
		{
			name:     "transport failure missing code",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_TransportFailure{TransportFailure: &sharedpb.TransportFailure{}}},
		},
		{
			name: "transport failure unknown code",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_TransportFailure{TransportFailure: &sharedpb.TransportFailure{
				Code: sharedpb.TransportFailureCode(99),
			}}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := protoapi.MarshalEnvelope(test.envelope); err == nil {
				t.Fatal("malformed envelope unexpectedly marshaled")
			}
		})
	}

	if _, err := protoapi.UnmarshalEnvelope([]byte{0xff}); err == nil {
		t.Fatal("malformed protobuf unexpectedly decoded")
	}
}

func TestEnvelopeRejectsSemanticallyMalformedWireBytes(t *testing.T) {
	tests := []struct {
		name     string
		envelope *sharedpb.Envelope
	}{
		{name: "missing frame", envelope: &sharedpb.Envelope{}},
		{
			name: "call missing operation",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_Call{Call: &sharedpb.Call{
				Payload: []byte{1},
			}}},
		},
		{
			name: "call missing payload",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_Call{Call: &sharedpb.Call{
				Operation: "kent.api.server.server_service.get_readiness",
			}}},
		},
		{
			name: "call empty correlation",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_Call{Call: &sharedpb.Call{
				Operation:   "kent.api.server.server_service.get_readiness",
				Correlation: new(string),
				Payload:     []byte{1},
			}}},
		},
		{
			name: "result missing operation",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_Result{Result: &sharedpb.Result{
				Payload: []byte{1},
			}}},
		},
		{
			name: "result missing payload",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_Result{Result: &sharedpb.Result{
				Operation: "kent.api.server.server_service.get_readiness",
			}}},
		},
		{
			name: "notification missing operation",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_NotificationEvent{NotificationEvent: &sharedpb.NotificationEvent{
				Payload: []byte{1},
			}}},
		},
		{
			name: "notification missing payload",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_NotificationEvent{NotificationEvent: &sharedpb.NotificationEvent{
				Operation: "kent.api.attention.session_service.event",
			}}},
		},
		{
			name: "transport failure empty correlation",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_TransportFailure{TransportFailure: &sharedpb.TransportFailure{
				Code:        sharedpb.TransportFailureCode_TRANSPORT_FAILURE_CODE_UNKNOWN_OPERATION,
				Correlation: new(string),
			}}},
		},
		{
			name: "transport failure invalid code",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_TransportFailure{TransportFailure: &sharedpb.TransportFailure{
				Code: sharedpb.TransportFailureCode(99),
			}}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := proto.Marshal(test.envelope)
			if err != nil {
				t.Fatalf("encode malformed fixture: %v", err)
			}
			if _, err := protoapi.UnmarshalEnvelope(encoded); err == nil {
				t.Fatal("semantically malformed wire bytes unexpectedly decoded")
			}
		})
	}
}

func TestEnvelopeAllowsPresentZeroByteEmptyPayloadAndRejectsOtherZeroBytePayloads(t *testing.T) {
	emptyPayloadFrames := []struct {
		name     string
		envelope *sharedpb.Envelope
	}{
		{
			name: "call input",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_Call{Call: &sharedpb.Call{
				Operation: "kent.api.server.server_service.get_readiness",
				Payload:   []byte{},
			}}},
		},
	}
	for _, test := range emptyPayloadFrames {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := protoapi.MarshalEnvelope(test.envelope)
			if err != nil {
				t.Fatalf("marshal Empty payload envelope: %v", err)
			}
			if _, err := protoapi.UnmarshalEnvelope(encoded); err != nil {
				t.Fatalf("unmarshal marshaled Empty payload envelope: %v", err)
			}

			rawEncoded, err := proto.Marshal(test.envelope)
			if err != nil {
				t.Fatalf("marshal raw Empty payload envelope: %v", err)
			}
			if _, err := protoapi.UnmarshalEnvelope(rawEncoded); err != nil {
				t.Fatalf("unmarshal raw Empty payload envelope: %v", err)
			}
		})
	}

	t.Run("absent Empty payload", func(t *testing.T) {
		envelope := &sharedpb.Envelope{Frame: &sharedpb.Envelope_Call{Call: &sharedpb.Call{
			Operation: "kent.api.server.server_service.get_readiness",
		}}}
		if _, err := protoapi.MarshalEnvelope(envelope); err == nil {
			t.Fatal("absent Empty payload unexpectedly marshaled")
		}
	})

	t.Run("zero-byte non-Empty payload", func(t *testing.T) {
		envelope := &sharedpb.Envelope{Frame: &sharedpb.Envelope_NotificationEvent{NotificationEvent: &sharedpb.NotificationEvent{
			Operation: "kent.api.attention.session_service.event",
			Payload:   []byte{},
		}}}
		if _, err := protoapi.MarshalEnvelope(envelope); err == nil {
			t.Fatal("zero-byte WatchEvent payload unexpectedly marshaled")
		}
		rawEncoded, err := proto.Marshal(envelope)
		if err != nil {
			t.Fatalf("marshal raw zero-byte WatchEvent envelope: %v", err)
		}
		if _, err := protoapi.UnmarshalEnvelope(rawEncoded); err == nil {
			t.Fatal("raw zero-byte WatchEvent payload unexpectedly unmarshaled")
		}
	})
}
