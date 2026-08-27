package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"core/shared/protoapi"
	"core/shared/protocol"
	"core/shared/rpcwire"
	"core/shared/serverapi"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type binarySubscriptionCompletion interface {
	proto.Message
	GetCode() int32
	GetDiagnostic() string
}

func subscribeGeneratedBinary[
	Request proto.Message,
	StartResult proto.Message,
	Event proto.Message,
	Completion binarySubscriptionCompletion,
](
	c *Remote,
	ctx context.Context,
	method protoreflect.MethodDescriptor,
	request Request,
	startResult StartResult,
	newEvent func() Event,
	newCompletion func() Completion,
	classifyStart func(StartResult) error,
) (*remoteSubscription[Event], error) {
	if newEvent == nil || newCompletion == nil || classifyStart == nil {
		return nil, fmt.Errorf("generated %s subscription is incomplete", method.FullName())
	}
	operations, err := protoapi.ResolveSubscriptionOperations(method)
	if err != nil {
		return nil, err
	}
	if err := requireBinaryMessageType(operations.Event.Name, "event", newEvent(), operations.Event.Descriptor.Input()); err != nil {
		return nil, err
	}
	completion := newCompletion()
	if err := requireBinaryMessageType(operations.Completion.Name, "completion", completion, operations.Completion.Descriptor.Input()); err != nil {
		return nil, err
	}
	conn, cleanup, err := c.openRPCConn(ctx)
	if err != nil {
		return nil, err
	}
	if err := callBinaryRPC(ctx, conn, operations.Subscribe.Name, method, request, startResult); err != nil {
		cleanup()
		return nil, errors.Join(serverapi.ErrStreamFailed, err)
	}
	if err := classifyStart(startResult); err != nil {
		cleanup()
		return nil, err
	}
	return &remoteSubscription[Event]{
		conn: conn,
		next: func(ctx context.Context, conn rpcwire.Conn) (Event, error) {
			return nextGeneratedSubscriptionEvent(
				ctx,
				conn,
				operations,
				newEvent,
				func() binarySubscriptionCompletion { return newCompletion() },
			)
		},
	}, nil
}

func nextGeneratedSubscriptionEvent[Event proto.Message](
	ctx context.Context,
	conn rpcwire.Conn,
	operations protoapi.SubscriptionOperations,
	newEvent func() Event,
	newCompletion func() binarySubscriptionCompletion,
) (Event, error) {
	var zero Event
	frame, err := receiveFrame(ctx, conn)
	if err != nil {
		return zero, serverapi.NormalizeStreamError(err)
	}
	if frame.Kind != rpcwire.FrameBinary {
		return zero, errors.Join(serverapi.ErrStreamFailed, fmt.Errorf(
			"operation %s received a JSON frame",
			operations.Subscribe.Name,
		))
	}
	envelope, err := protoapi.DecodeEnvelope(frame.Payload)
	if err != nil {
		return zero, errors.Join(serverapi.ErrStreamFailed, err)
	}
	notification := envelope.GetNotificationEvent()
	if notification == nil || notification.Payload == nil {
		return zero, errors.Join(serverapi.ErrStreamFailed, fmt.Errorf(
			"operation %s received an unexpected envelope",
			operations.Subscribe.Name,
		))
	}
	switch notification.Operation {
	case operations.Event.Name:
		event := newEvent()
		if err := protoapi.Decode(notification.Payload, event); err != nil {
			return zero, errors.Join(serverapi.ErrStreamFailed, err)
		}
		return event, nil
	case operations.Completion.Name:
		completion := newCompletion()
		if err := protoapi.Decode(notification.Payload, completion); err != nil {
			return zero, errors.Join(serverapi.ErrStreamFailed, err)
		}
		_ = conn.Close()
		if completion.GetCode() == 0 && strings.TrimSpace(completion.GetDiagnostic()) == "" {
			return zero, io.EOF
		}
		return zero, protocolError(&protocol.ResponseError{
			Code:    int(completion.GetCode()),
			Message: completion.GetDiagnostic(),
		})
	default:
		return zero, errors.Join(serverapi.ErrStreamFailed, fmt.Errorf(
			"operation %s received unexpected notification %s",
			operations.Subscribe.Name,
			notification.Operation,
		))
	}
}
