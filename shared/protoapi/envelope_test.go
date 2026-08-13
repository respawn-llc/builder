package protoapi_test

import (
	"bytes"
	"testing"

	"core/shared/protoapi"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"google.golang.org/protobuf/proto"
)

func TestEnvelopeRoundTripsEveryVariant(t *testing.T) {
	correlation := "connection-call-1"
	tests := []struct {
		name     string
		envelope *sharedpb.Envelope
	}{
		{
			name: "call with correlation",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_Call{Call: &sharedpb.Call{
				Operation:   "fixture.naming_service.api_status",
				Correlation: &correlation,
				Payload:     []byte{0x08, 0x01},
			}}},
		},
		{
			name: "call without correlation",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_Call{Call: &sharedpb.Call{
				Operation: "fixture.naming_service.http2_server",
				Payload:   []byte{0x08, 0x02},
			}}},
		},
		{
			name: "result with correlation",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_Result{Result: &sharedpb.Result{
				Operation:   "fixture.naming_service.api_status",
				Correlation: &correlation,
				Payload:     []byte{0x08, 0x03},
			}}},
		},
		{
			name: "result without correlation",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_Result{Result: &sharedpb.Result{
				Operation: "fixture.naming_service.http2_server",
				Payload:   []byte{0x08, 0x04},
			}}},
		},
		{
			name: "notification or event",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_NotificationEvent{NotificationEvent: &sharedpb.NotificationEvent{
				Operation: "fixture.naming_service.watch_event",
				Payload:   []byte{0x08, 0x05},
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
				Operation: "fixture.naming_service.api_status",
			}}},
		},
		{
			name: "call empty correlation",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_Call{Call: &sharedpb.Call{
				Operation:   "fixture.naming_service.api_status",
				Correlation: new(string),
				Payload:     []byte{1},
			}}},
		},
		{
			name: "result missing payload",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_Result{Result: &sharedpb.Result{
				Operation:   "fixture.naming_service.api_status",
				Correlation: &correlation,
			}}},
		},
		{
			name: "notification missing payload",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_NotificationEvent{NotificationEvent: &sharedpb.NotificationEvent{
				Operation: "fixture.naming_service.watch_event",
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
				Operation: "fixture.naming_service.api_status",
			}}},
		},
		{
			name: "call empty correlation",
			envelope: &sharedpb.Envelope{Frame: &sharedpb.Envelope_Call{Call: &sharedpb.Call{
				Operation:   "fixture.naming_service.api_status",
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
				Operation: "fixture.naming_service.api_status",
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
				Operation: "fixture.naming_service.watch_event",
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
