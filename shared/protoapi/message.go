package protoapi

import (
	"fmt"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/proto"
)

// DecodeGeneratedMessage parses and validates one inactive generated Server API
// message. Unknown fields remain attached to the generated message.
func DecodeGeneratedMessage(encoded []byte, message proto.Message) error {
	if message == nil {
		return fmt.Errorf("generated message is required")
	}
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(encoded, message); err != nil {
		return fmt.Errorf("unmarshal generated message: %w", err)
	}
	return ValidateGeneratedMessage(message)
}

// EncodeGeneratedMessage validates and serializes one inactive generated
// Server API message, including retained unknown fields.
func EncodeGeneratedMessage(message proto.Message) ([]byte, error) {
	if err := ValidateGeneratedMessage(message); err != nil {
		return nil, err
	}
	encoded, err := (proto.MarshalOptions{}).Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("marshal generated message: %w", err)
	}
	return encoded, nil
}

// ValidateGeneratedMessage executes the generated message's Protovalidate
// constraints.
func ValidateGeneratedMessage(message proto.Message) error {
	if message == nil {
		return fmt.Errorf("generated message is required")
	}
	if err := protovalidate.Validate(message); err != nil {
		return fmt.Errorf("validate generated message: %w", err)
	}
	return nil
}
