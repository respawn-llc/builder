package protoapi

import (
	"fmt"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/proto"
)

func Decode(encoded []byte, message proto.Message) error {
	if err := unmarshalGenerated(encoded, message); err != nil {
		return err
	}
	return Validate(message)
}

func unmarshalGenerated(encoded []byte, message proto.Message) error {
	if message == nil {
		return fmt.Errorf("generated message is required")
	}
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(encoded, message); err != nil {
		return fmt.Errorf("unmarshal generated message: %w", err)
	}
	return nil
}

func Encode(message proto.Message) ([]byte, error) {
	if err := Validate(message); err != nil {
		return nil, err
	}
	encoded, err := proto.Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("marshal generated message: %w", err)
	}
	return encoded, nil
}

func Validate(message proto.Message) error {
	if message == nil {
		return fmt.Errorf("generated message is required")
	}
	if err := protovalidate.Validate(message); err != nil {
		return fmt.Errorf("validate generated message: %w", err)
	}
	return nil
}
