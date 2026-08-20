package protoapi

import (
	"fmt"

	sharedpb "core/shared/protoapi/gen/kent/api/shared"
)

func DecodeEnvelope(encoded []byte) (*sharedpb.Envelope, error) {
	envelope := &sharedpb.Envelope{}
	if err := Decode(encoded, envelope); err != nil {
		return nil, fmt.Errorf("decode envelope: %w", err)
	}
	return envelope, nil
}

func EncodeEnvelope(envelope *sharedpb.Envelope) ([]byte, error) {
	encoded, err := Encode(envelope)
	if err != nil {
		return nil, fmt.Errorf("encode envelope: %w", err)
	}
	return encoded, nil
}
