package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"core/shared/protoapi"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"core/shared/protocol"
	"core/shared/rpcwire"
	"core/shared/serverapi"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type remoteBinarySubscriptionFrame[Event proto.Message] struct {
	event      Event
	completion proto.Message
	err        error
}

type remoteBinarySubscription[Event proto.Message] struct {
	conn      rpcwire.Conn
	frames    chan remoteBinarySubscriptionFrame[Event]
	done      chan struct{}
	closeOnce sync.Once
}

func subscribeGeneratedBinary[
	Request proto.Message,
	StartResult proto.Message,
	Event proto.Message,
	Completion proto.Message,
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
	if err := requireBinaryMessageType(operations.Subscribe.Name, "request", request, method.Input()); err != nil {
		return nil, err
	}
	if err := requireBinaryMessageType(operations.Subscribe.Name, "start result", startResult, method.Output()); err != nil {
		return nil, err
	}
	event := newEvent()
	if err := requireBinaryMessageType(
		operations.Event.Name,
		"event",
		event,
		operations.Event.Descriptor.Input(),
	); err != nil {
		return nil, err
	}
	completion := newCompletion()
	if err := requireBinaryMessageType(
		operations.Completion.Name,
		"completion",
		completion,
		operations.Completion.Descriptor.Input(),
	); err != nil {
		return nil, err
	}
	payload, err := protoapi.Encode(request)
	if err != nil {
		return nil, fmt.Errorf("encode %s request: %w", operations.Subscribe.Name, err)
	}
	correlation := operations.Subscribe.Name
	envelope, err := protoapi.EncodeEnvelope(&sharedpb.Envelope{
		Frame: &sharedpb.Envelope_Call{Call: &sharedpb.Call{
			Operation:   operations.Subscribe.Name,
			Correlation: &correlation,
			Payload:     payload,
		}},
	})
	if err != nil {
		return nil, fmt.Errorf("encode %s call: %w", operations.Subscribe.Name, err)
	}
	conn, cleanup, err := c.openRPCConn(ctx)
	if err != nil {
		return nil, err
	}
	subscription := &remoteBinarySubscription[Event]{
		conn:   conn,
		frames: make(chan remoteBinarySubscriptionFrame[Event]),
		done:   make(chan struct{}),
	}
	start := make(chan error, 1)
	go readRemoteBinarySubscription(
		subscription,
		operations,
		correlation,
		startResult,
		newEvent,
		newCompletion,
		classifyStart,
		start,
	)
	if err := conn.Send(ctx, rpcwire.Frame{Kind: rpcwire.FrameBinary, Payload: envelope}); err != nil {
		_ = subscription.Close()
		return nil, err
	}
	select {
	case err := <-start:
		if err != nil {
			_ = subscription.Close()
			return nil, err
		}
		return subscription, nil
	case <-ctx.Done():
		_ = subscription.Close()
		return nil, ctx.Err()
	case <-subscription.done:
		cleanup()
		return nil, io.EOF
	}
}

func readRemoteBinarySubscription[
	StartResult proto.Message,
	Event proto.Message,
	Completion proto.Message,
](
	subscription *remoteBinarySubscription[Event],
	operations protoapi.SubscriptionOperations,
	correlation string,
	startResult StartResult,
	newEvent func() Event,
	newCompletion func() Completion,
	classifyStart func(StartResult) error,
	start chan<- error,
) {
	started := false
	fail := func(err error) {
		if !started {
			start <- errors.Join(serverapi.ErrStreamFailed, err)
			return
		}
		subscription.send(remoteBinarySubscriptionFrame[Event]{err: err})
	}
	for {
		frame, err := receiveFrame(context.Background(), subscription.conn)
		if err != nil {
			fail(err)
			return
		}
		if frame.Kind != rpcwire.FrameBinary {
			fail(fmt.Errorf("operation %s received a JSON frame", operations.Subscribe.Name))
			return
		}
		envelope, err := protoapi.DecodeEnvelope(frame.Payload)
		if err != nil {
			fail(err)
			return
		}
		switch value := envelope.GetFrame().(type) {
		case *sharedpb.Envelope_Result:
			if started || value.Result.GetCorrelation() != correlation {
				fail(fmt.Errorf("operation %s received an unexpected result", operations.Subscribe.Name))
				return
			}
			started = true
			if err := decodeBinaryResponse(
				operations.Subscribe,
				correlation,
				&remoteBinaryResponse{result: value.Result},
				startResult,
			); err != nil {
				start <- err
				return
			}
			if err := classifyStart(startResult); err != nil {
				start <- err
				return
			}
			start <- nil
		case *sharedpb.Envelope_TransportFailure:
			if started || value.TransportFailure.GetCorrelation() != correlation {
				fail(fmt.Errorf("operation %s received an unexpected transport failure", operations.Subscribe.Name))
				return
			}
			started = true
			start <- remoteTransportFailureError{code: value.TransportFailure.Code}
			return
		case *sharedpb.Envelope_NotificationEvent:
			if !started {
				fail(fmt.Errorf("operation %s received a notification before its start result", operations.Subscribe.Name))
				return
			}
			notification := value.NotificationEvent
			if notification.Payload == nil {
				fail(fmt.Errorf("operation %s notification payload is required", notification.Operation))
				return
			}
			switch notification.Operation {
			case operations.Event.Name:
				event := newEvent()
				if err := protoapi.Decode(notification.Payload, event); err != nil {
					fail(err)
					return
				}
				if !subscription.send(remoteBinarySubscriptionFrame[Event]{event: event}) {
					return
				}
			case operations.Completion.Name:
				completion := newCompletion()
				if err := protoapi.Decode(notification.Payload, completion); err != nil {
					fail(err)
					return
				}
				subscription.send(remoteBinarySubscriptionFrame[Event]{completion: completion})
				return
			default:
				fail(fmt.Errorf(
					"operation %s received unexpected notification %s",
					operations.Subscribe.Name,
					notification.Operation,
				))
				return
			}
		default:
			fail(fmt.Errorf("operation %s received an unexpected envelope", operations.Subscribe.Name))
			return
		}
	}
}

func (s *remoteBinarySubscription[Event]) send(frame remoteBinarySubscriptionFrame[Event]) bool {
	select {
	case s.frames <- frame:
		return true
	case <-s.done:
		return false
	}
}

func (s *remoteBinarySubscription[Event]) Next(ctx context.Context) (Event, error) {
	select {
	case frame := <-s.frames:
		switch {
		case frame.err != nil:
			_ = s.Close()
			var zero Event
			return zero, serverapi.NormalizeStreamError(frame.err)
		case frame.completion != nil:
			_ = s.Close()
			var zero Event
			completion, ok := frame.completion.(interface {
				GetCode() int32
				GetDiagnostic() string
			})
			if !ok {
				return zero, errors.Join(serverapi.ErrStreamFailed, errors.New("binary completion type is invalid"))
			}
			if completion.GetCode() == 0 && completion.GetDiagnostic() == "" {
				return zero, io.EOF
			}
			return zero, protocolError(&protocol.ResponseError{
				Code:    int(completion.GetCode()),
				Message: completion.GetDiagnostic(),
			})
		default:
			return frame.event, nil
		}
	case <-ctx.Done():
		var zero Event
		return zero, serverapi.NormalizeStreamError(ctx.Err())
	case <-s.done:
		var zero Event
		return zero, io.EOF
	}
}

func (s *remoteBinarySubscription[Event]) Close() error {
	if s == nil {
		return nil
	}
	var closeErr error
	s.closeOnce.Do(func() {
		close(s.done)
		if s.conn != nil {
			closeErr = s.conn.Close()
		}
	})
	return closeErr
}
