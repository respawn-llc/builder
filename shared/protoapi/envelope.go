package protoapi

import (
	"fmt"

	sharedpb "core/shared/protoapi/gen/kent/api/shared"
)

// MarshalEnvelope validates and serializes one inactive binary Server API
// envelope.
func MarshalEnvelope(envelope *sharedpb.Envelope) ([]byte, error) {
	if envelope == nil {
		return nil, fmt.Errorf("envelope is required")
	}
	encoded, err := EncodeGeneratedMessage(envelope)
	if err != nil {
		return nil, fmt.Errorf("encode envelope: %w", err)
	}
	return encoded, nil
}

// UnmarshalEnvelope parses and validates one inactive binary Server API
// envelope.
func UnmarshalEnvelope(encoded []byte) (*sharedpb.Envelope, error) {
	envelope := &sharedpb.Envelope{}
	if err := DecodeGeneratedMessage(encoded, envelope); err != nil {
		return nil, fmt.Errorf("decode envelope: %w", err)
	}
	return envelope, nil
}
