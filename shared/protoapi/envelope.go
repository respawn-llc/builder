package protoapi

import (
	"fmt"
	"strings"

	sharedpb "core/shared/protoapi/gen/kent/api/shared"
)

func DecodeEnvelope(encoded []byte) (*sharedpb.Envelope, error) {
	envelope := &sharedpb.Envelope{}
	if err := Decode(encoded, envelope); err != nil {
		return nil, fmt.Errorf("decode envelope: %w", err)
	}
	return envelope, nil
}

// DecodeEnvelopeCorrelation recovers only a syntactically decodable correlation
// so callers can isolate an invalid frame without accepting the envelope.
func DecodeEnvelopeCorrelation(encoded []byte) string {
	envelope := &sharedpb.Envelope{}
	if err := unmarshalGenerated(encoded, envelope); err != nil {
		return ""
	}
	var correlation *string
	switch frame := envelope.GetFrame().(type) {
	case *sharedpb.Envelope_Call:
		correlation = frame.Call.Correlation
	case *sharedpb.Envelope_Result:
		correlation = frame.Result.Correlation
	case *sharedpb.Envelope_TransportFailure:
		correlation = frame.TransportFailure.Correlation
	}
	if correlation == nil || strings.TrimSpace(*correlation) == "" {
		return ""
	}
	return *correlation
}

func EncodeEnvelope(envelope *sharedpb.Envelope) ([]byte, error) {
	encoded, err := Encode(envelope)
	if err != nil {
		return nil, fmt.Errorf("encode envelope: %w", err)
	}
	return encoded, nil
}
