package transport

import (
	"context"
	"fmt"

	"core/shared/protoapi"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"core/shared/rpcwire"

	"google.golang.org/protobuf/proto"
)

type gatewayBinarySubscriber interface {
	Next(context.Context) (proto.Message, error)
	Close() error
}

func sendBinaryNotification(
	ctx context.Context,
	conn rpcwire.Conn,
	operation protoapi.Operation,
	payload proto.Message,
) error {
	if payload == nil {
		return fmt.Errorf("%s payload is required", operation.Name)
	}
	expected := operation.Descriptor.Input()
	if payload.ProtoReflect().Descriptor().FullName() != expected.FullName() {
		return fmt.Errorf(
			"%s payload type %s does not match %s",
			operation.Name,
			payload.ProtoReflect().Descriptor().FullName(),
			expected.FullName(),
		)
	}
	encodedPayload, err := protoapi.Encode(payload)
	if err != nil {
		return fmt.Errorf("encode %s payload: %w", operation.Name, err)
	}
	envelope := &sharedpb.Envelope{
		Frame: &sharedpb.Envelope_NotificationEvent{NotificationEvent: &sharedpb.NotificationEvent{
			Operation: operation.Name, Payload: encodedPayload,
		}},
	}
	encoded, err := protoapi.EncodeEnvelope(envelope)
	if err != nil {
		return err
	}
	return conn.Send(ctx, rpcwire.Frame{Kind: rpcwire.FrameBinary, Payload: encoded})
}
