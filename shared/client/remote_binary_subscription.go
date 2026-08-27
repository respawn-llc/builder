package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"core/shared/protoapi"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"core/shared/rpcwire"
	"core/shared/serverapi"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const remoteBinarySubscriptionBuffer = 16

type remoteDescriptorTerminalOutcome struct {
	err error
}

type remoteBinarySubscriptionFrameKind uint8

const (
	remoteBinarySubscriptionEvent remoteBinarySubscriptionFrameKind = iota + 1
	remoteBinarySubscriptionCompletion
	remoteBinarySubscriptionFailure
)

type remoteBinarySubscriptionFrame struct {
	kind    remoteBinarySubscriptionFrameKind
	message proto.Message
	err     error
}

type remoteBinarySubscriptionAcknowledgement struct {
	response remoteBinaryResponse
	err      error
}

type remoteBinarySubscription[Event any] struct {
	conn               rpcwire.Conn
	frames             chan remoteBinarySubscriptionFrame
	done               chan struct{}
	projectEvent       func(proto.Message) (Event, error)
	classifyCompletion func(proto.Message) (remoteDescriptorTerminalOutcome, error)
	closeOnce          sync.Once
	terminalMu         sync.Mutex
	terminalConsumed   bool
}

type remoteBinarySubscriptionContract struct {
	subscribe  protoapi.Operation
	event      protoapi.Operation
	completion protoapi.Operation
}

func subscribeGeneratedBinary[
	Request proto.Message,
	StartResult proto.Message,
	EventMessage proto.Message,
	CompletionMessage proto.Message,
	Event any,
](
	c *Remote,
	ctx context.Context,
	method protoreflect.MethodDescriptor,
	request Request,
	startResult StartResult,
	newEvent func() EventMessage,
	projectEvent func(EventMessage) (Event, error),
	newCompletion func() CompletionMessage,
	classifyCompletion func(CompletionMessage) (remoteDescriptorTerminalOutcome, error),
	classifyStart func(StartResult) error,
) (*remoteBinarySubscription[Event], error) {
	if newEvent == nil || projectEvent == nil || newCompletion == nil ||
		classifyCompletion == nil || classifyStart == nil {
		return nil, fmt.Errorf("generated %s subscription is incomplete", method.FullName())
	}
	contract, err := resolveRemoteBinarySubscriptionContract(method, request, startResult, newEvent(), newCompletion())
	if err != nil {
		return nil, err
	}
	conn, cleanup, err := c.openRPCConn(ctx)
	if err != nil {
		return nil, err
	}
	correlation := contract.subscribe.Name
	callFrame, err := binarySubscriptionCallFrame(correlation, contract.subscribe, request)
	if err != nil {
		cleanup()
		return nil, err
	}
	subscription := &remoteBinarySubscription[Event]{
		conn:   conn,
		frames: make(chan remoteBinarySubscriptionFrame, remoteBinarySubscriptionBuffer),
		done:   make(chan struct{}),
		projectEvent: func(message proto.Message) (Event, error) {
			typed, ok := message.(EventMessage)
			if !ok {
				var zero Event
				return zero, fmt.Errorf("%s event type is invalid", contract.subscribe.Name)
			}
			return projectEvent(typed)
		},
		classifyCompletion: func(message proto.Message) (remoteDescriptorTerminalOutcome, error) {
			typed, ok := message.(CompletionMessage)
			if !ok {
				return remoteDescriptorTerminalOutcome{}, fmt.Errorf(
					"%s completion type is invalid",
					contract.subscribe.Name,
				)
			}
			return classifyCompletion(typed)
		},
	}
	acknowledgement := make(chan remoteBinarySubscriptionAcknowledgement, 1)
	go readRemoteBinarySubscription(
		subscription,
		contract,
		correlation,
		newEvent,
		newCompletion,
		acknowledgement,
	)
	if err := conn.Send(ctx, callFrame); err != nil {
		_ = subscription.Close()
		return nil, err
	}
	select {
	case acknowledgement := <-acknowledgement:
		if acknowledgement.err != nil {
			_ = subscription.Close()
			return nil, acknowledgement.err
		}
		response := acknowledgement.response
		if response.failure != nil {
			_ = subscription.Close()
			return nil, remoteTransportFailureError{code: response.failure.Code}
		}
		if response.result == nil {
			_ = subscription.Close()
			return nil, errors.New("binary subscription acknowledgement is required")
		}
		if err := decodeBinaryResponse(contract.subscribe, correlation, &response, startResult); err != nil {
			_ = subscription.Close()
			return nil, err
		}
		if err := classifyStart(startResult); err != nil {
			_ = subscription.Close()
			return nil, err
		}
		return subscription, nil
	case <-ctx.Done():
		_ = subscription.Close()
		return nil, ctx.Err()
	case <-subscription.done:
		return nil, io.EOF
	}
}

func resolveRemoteBinarySubscriptionContract(
	method protoreflect.MethodDescriptor,
	request proto.Message,
	startResult proto.Message,
	event proto.Message,
	completion proto.Message,
) (remoteBinarySubscriptionContract, error) {
	subscribe, err := protoapi.OperationFromDescriptor(method)
	if err != nil {
		return remoteBinarySubscriptionContract{}, err
	}
	if subscribe.Options.Kind != sharedpb.OperationKind_OPERATION_KIND_SUBSCRIPTION ||
		subscribe.Options.Direction != sharedpb.Direction_DIRECTION_CLIENT_TO_SERVER {
		return remoteBinarySubscriptionContract{}, fmt.Errorf("operation %s is not a client subscription", subscribe.Name)
	}
	if err := requireBinaryMessageType(subscribe.Name, "request", request, method.Input()); err != nil {
		return remoteBinarySubscriptionContract{}, err
	}
	if err := requireBinaryMessageType(subscribe.Name, "start result", startResult, method.Output()); err != nil {
		return remoteBinarySubscriptionContract{}, err
	}
	operations, err := protoapi.Operations()
	if err != nil {
		return remoteBinarySubscriptionContract{}, err
	}
	byName := make(map[string]protoapi.Operation, len(operations))
	for _, operation := range operations {
		byName[operation.Name] = operation
	}
	resolve := func(
		role string,
		association *sharedpb.OperationAssociation,
		message proto.Message,
	) (protoapi.Operation, error) {
		if association == nil {
			return protoapi.Operation{}, fmt.Errorf("operation %s has no %s association", subscribe.Name, role)
		}
		name, nameErr := protoapi.ActiveOperationName(
			association.Package,
			association.Service,
			association.Method,
		)
		if nameErr != nil {
			return protoapi.Operation{}, nameErr
		}
		operation, exists := byName[name]
		if !exists {
			return protoapi.Operation{}, fmt.Errorf("operation %s %s association %s is missing", subscribe.Name, role, name)
		}
		if operation.Options.Kind != sharedpb.OperationKind_OPERATION_KIND_NOTIFICATION ||
			operation.Options.Direction != sharedpb.Direction_DIRECTION_SERVER_TO_CLIENT {
			return protoapi.Operation{}, fmt.Errorf("operation %s %s association %s is not a server notification", subscribe.Name, role, name)
		}
		if err := requireBinaryMessageType(name, "payload", message, operation.Descriptor.Input()); err != nil {
			return protoapi.Operation{}, err
		}
		return operation, nil
	}
	eventOperation, err := resolve("event", subscribe.Options.Event, event)
	if err != nil {
		return remoteBinarySubscriptionContract{}, err
	}
	completionOperation, err := resolve("completion", subscribe.Options.Completion, completion)
	if err != nil {
		return remoteBinarySubscriptionContract{}, err
	}
	return remoteBinarySubscriptionContract{
		subscribe: subscribe, event: eventOperation, completion: completionOperation,
	}, nil
}

func binarySubscriptionCallFrame(
	correlation string,
	operation protoapi.Operation,
	request proto.Message,
) (rpcwire.Frame, error) {
	payload, err := protoapi.Encode(request)
	if err != nil {
		return rpcwire.Frame{}, fmt.Errorf("encode %s request: %w", operation.Name, err)
	}
	encoded, err := protoapi.EncodeEnvelope(&sharedpb.Envelope{
		Frame: &sharedpb.Envelope_Call{Call: &sharedpb.Call{
			Operation: operation.Name, Correlation: &correlation, Payload: payload,
		}},
	})
	if err != nil {
		return rpcwire.Frame{}, fmt.Errorf("encode %s call: %w", operation.Name, err)
	}
	return rpcwire.Frame{Kind: rpcwire.FrameBinary, Payload: encoded}, nil
}

func readRemoteBinarySubscription[
	Event any,
	EventMessage proto.Message,
	CompletionMessage proto.Message,
](
	s *remoteBinarySubscription[Event],
	contract remoteBinarySubscriptionContract,
	correlation string,
	newEvent func() EventMessage,
	newCompletion func() CompletionMessage,
	acknowledgement chan<- remoteBinarySubscriptionAcknowledgement,
) {
	startSeen := false
	fail := func(err error) {
		if !startSeen {
			select {
			case acknowledgement <- remoteBinarySubscriptionAcknowledgement{
				err: errors.Join(serverapi.ErrStreamFailed, err),
			}:
			case <-s.done:
			}
			return
		}
		s.enqueue(remoteBinarySubscriptionFrame{kind: remoteBinarySubscriptionFailure, err: err})
	}
	for {
		frame, err := receiveFrame(context.Background(), s.conn)
		if err != nil {
			fail(err)
			return
		}
		if frame.Kind != rpcwire.FrameBinary {
			fail(fmt.Errorf("operation %s received a JSON frame", contract.subscribe.Name))
			return
		}
		envelope, err := protoapi.DecodeEnvelope(frame.Payload)
		if err != nil {
			fail(err)
			return
		}
		switch value := envelope.GetFrame().(type) {
		case *sharedpb.Envelope_Result:
			if startSeen || value.Result.GetCorrelation() != correlation {
				fail(fmt.Errorf("operation %s received an unexpected result", contract.subscribe.Name))
				return
			}
			startSeen = true
			acknowledgement <- remoteBinarySubscriptionAcknowledgement{
				response: remoteBinaryResponse{result: value.Result},
			}
		case *sharedpb.Envelope_TransportFailure:
			if startSeen || value.TransportFailure.GetCorrelation() != correlation {
				fail(fmt.Errorf("operation %s received an unexpected transport failure", contract.subscribe.Name))
				return
			}
			startSeen = true
			acknowledgement <- remoteBinarySubscriptionAcknowledgement{
				response: remoteBinaryResponse{failure: value.TransportFailure},
			}
			return
		case *sharedpb.Envelope_NotificationEvent:
			notification := value.NotificationEvent
			if notification.Payload == nil {
				fail(fmt.Errorf("operation %s notification payload is required", notification.Operation))
				return
			}
			switch notification.Operation {
			case contract.event.Name:
				message := newEvent()
				if err := protoapi.Decode(notification.Payload, message); err != nil {
					fail(err)
					return
				}
				if !s.enqueue(remoteBinarySubscriptionFrame{
					kind: remoteBinarySubscriptionEvent, message: message,
				}) {
					return
				}
			case contract.completion.Name:
				message := newCompletion()
				if err := protoapi.Decode(notification.Payload, message); err != nil {
					fail(err)
					return
				}
				s.enqueue(remoteBinarySubscriptionFrame{
					kind: remoteBinarySubscriptionCompletion, message: message,
				})
				return
			default:
				fail(fmt.Errorf("operation %s received unexpected notification %s", contract.subscribe.Name, notification.Operation))
				return
			}
		default:
			fail(fmt.Errorf("operation %s received an unexpected envelope", contract.subscribe.Name))
			return
		}
	}
}

func (s *remoteBinarySubscription[Event]) enqueue(frame remoteBinarySubscriptionFrame) bool {
	select {
	case s.frames <- frame:
		return true
	case <-s.done:
		return false
	}
}

func (s *remoteBinarySubscription[Event]) Next(ctx context.Context) (Event, error) {
	s.terminalMu.Lock()
	terminalConsumed := s.terminalConsumed
	s.terminalMu.Unlock()
	if terminalConsumed {
		_, err := receiveFrame(ctx, s.conn)
		var zero Event
		return zero, serverapi.NormalizeStreamError(err)
	}
	select {
	case frame := <-s.frames:
		switch frame.kind {
		case remoteBinarySubscriptionEvent:
			event, err := s.projectEvent(frame.message)
			if err != nil {
				s.consumeTerminal()
				var zero Event
				return zero, errors.Join(serverapi.ErrStreamFailed, err)
			}
			return event, nil
		case remoteBinarySubscriptionCompletion:
			outcome, err := s.classifyCompletion(frame.message)
			s.consumeTerminal()
			var zero Event
			if err != nil {
				return zero, errors.Join(serverapi.ErrStreamFailed, err)
			}
			if outcome.err == nil {
				return zero, io.EOF
			}
			return zero, outcome.err
		case remoteBinarySubscriptionFailure:
			s.consumeTerminal()
			var zero Event
			return zero, serverapi.NormalizeStreamError(frame.err)
		default:
			s.consumeTerminal()
			var zero Event
			return zero, errors.Join(serverapi.ErrStreamFailed, errors.New("binary subscription frame kind is invalid"))
		}
	case <-ctx.Done():
		var zero Event
		return zero, serverapi.NormalizeStreamError(ctx.Err())
	case <-s.done:
		var zero Event
		return zero, io.EOF
	}
}

func (s *remoteBinarySubscription[Event]) consumeTerminal() {
	s.terminalMu.Lock()
	s.terminalConsumed = true
	s.terminalMu.Unlock()
	_ = s.Close()
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
