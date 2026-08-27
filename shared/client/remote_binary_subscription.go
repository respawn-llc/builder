package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"core/shared/protoapi"
	"core/shared/protocol"
	"core/shared/rpcwire"
	"core/shared/serverapi"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type remoteBinarySubscription[Event proto.Message] struct {
	conn          rpcwire.Conn
	operations    protoapi.SubscriptionOperations
	newEvent      func() Event
	newCompletion func() binarySubscriptionCompletion
	closeOnce     sync.Once
}

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
) (*remoteBinarySubscription[Event], error) {
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
	return &remoteBinarySubscription[Event]{
		conn:          conn,
		operations:    operations,
		newEvent:      newEvent,
		newCompletion: func() binarySubscriptionCompletion { return newCompletion() },
	}, nil
}

func (s *remoteBinarySubscription[Event]) Next(ctx context.Context) (Event, error) {
	var zero Event
	frame, err := receiveFrame(ctx, s.conn)
	if err != nil {
		return zero, serverapi.NormalizeStreamError(err)
	}
	if frame.Kind != rpcwire.FrameBinary {
		return zero, errors.Join(serverapi.ErrStreamFailed, fmt.Errorf(
			"operation %s received a JSON frame",
			s.operations.Subscribe.Name,
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
			s.operations.Subscribe.Name,
		))
	}
	switch notification.Operation {
	case s.operations.Event.Name:
		event := s.newEvent()
		if err := protoapi.Decode(notification.Payload, event); err != nil {
			return zero, errors.Join(serverapi.ErrStreamFailed, err)
		}
		return event, nil
	case s.operations.Completion.Name:
		completion := s.newCompletion()
		if err := protoapi.Decode(notification.Payload, completion); err != nil {
			return zero, errors.Join(serverapi.ErrStreamFailed, err)
		}
		_ = s.Close()
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
			s.operations.Subscribe.Name,
			notification.Operation,
		))
	}
}

func (s *remoteBinarySubscription[Event]) Close() error {
	if s == nil {
		return nil
	}
	var closeErr error
	s.closeOnce.Do(func() {
		if s.conn != nil {
			closeErr = s.conn.Close()
		}
	})
	return closeErr
}
