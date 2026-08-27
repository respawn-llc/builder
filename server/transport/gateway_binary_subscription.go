package transport

import (
	"context"
	"fmt"

	"core/shared/protoapi"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"core/shared/rpcwire"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type gatewayBinarySubscriptionBinding struct {
	operation  protoapi.Operation
	associated protoapi.SubscriptionOperations
	policy     gatewayBinaryExecutionPolicy
	request    func() proto.Message
	scope      func(proto.Message) (routeScopeParams, error)
	subscribe  func(*Gateway, context.Context, *connectionState, proto.Message) (gatewayBinarySubscriber, error)
	failure    func(*Gateway, *connectionState, proto.Message, error) proto.Message
	start      func() (proto.Message, error)
	event      func() proto.Message
	completion func() proto.Message
	complete   func(error) proto.Message
}

type gatewayBinarySubscriptionRequest struct {
	binding gatewayBinarySubscriptionBinding
	call    *sharedpb.Call
}

type gatewayBinarySubscriber interface {
	Next(context.Context) (proto.Message, error)
	Close() error
}

type gatewayBinarySubscriberAdapter[Event proto.Message, Sub gatewaySubscription[Event]] struct {
	subscription Sub
}

func (a gatewayBinarySubscriberAdapter[Event, Sub]) Next(ctx context.Context) (proto.Message, error) {
	return a.subscription.Next(ctx)
}

func (a gatewayBinarySubscriberAdapter[Event, Sub]) Close() error {
	return a.subscription.Close()
}

func registerGatewayBinarySubscription[
	Request proto.Message,
	Start proto.Message,
	Event proto.Message,
	Completion proto.Message,
	Subscription gatewaySubscription[Event],
](
	bindings map[string]gatewayBinarySubscriptionBinding,
	service protoreflect.ServiceDescriptor,
	methodName protoreflect.Name,
	policy gatewayBinaryExecutionPolicy,
	newRequest func() Request,
	scope func(Request) (routeScopeParams, error),
	subscribe func(*Gateway, context.Context, *connectionState, Request) (Subscription, error),
	failureDetail func(*Gateway, *connectionState, Request, error) proto.Message,
	startSuccess func() Start,
	newEvent func() Event,
	newCompletion func() Completion,
	complete func(error) Completion,
) error {
	if service == nil {
		return fmt.Errorf("generated service descriptor is required")
	}
	if newRequest == nil || subscribe == nil || failureDetail == nil ||
		startSuccess == nil || newEvent == nil || newCompletion == nil || complete == nil {
		return fmt.Errorf("generated %s.%s subscription binding is incomplete", service.Name(), methodName)
	}
	method := service.Methods().ByName(methodName)
	if method == nil {
		return fmt.Errorf("generated %s.%s descriptor is required", service.Name(), methodName)
	}
	associated, err := protoapi.ResolveSubscriptionOperations(method)
	if err != nil {
		return err
	}
	binding := gatewayBinarySubscriptionBinding{
		operation:  associated.Subscribe,
		associated: associated,
		policy:     policy,
		request: func() proto.Message {
			return newRequest()
		},
		subscribe: func(
			g *Gateway,
			ctx context.Context,
			state *connectionState,
			message proto.Message,
		) (gatewayBinarySubscriber, error) {
			request, ok := message.(Request)
			if !ok {
				return nil, fmt.Errorf("%s request type is invalid", associated.Subscribe.Name)
			}
			subscription, err := subscribe(g, ctx, state, request)
			if err != nil {
				return nil, err
			}
			return gatewayBinarySubscriberAdapter[Event, Subscription]{subscription: subscription}, nil
		},
		failure: func(g *Gateway, state *connectionState, message proto.Message, err error) proto.Message {
			if details, ok := binaryServerNotReadyDetails(err); ok {
				return gatewayBinaryFailureResult(method, details)
			}
			var request Request
			if message != nil {
				typed, ok := message.(Request)
				if !ok {
					return gatewayBinaryFailureResult(
						method,
						failureDetail(g, state, request, fmt.Errorf("%s request type is invalid: %w", associated.Subscribe.Name, err)),
					)
				}
				request = typed
			}
			return gatewayBinaryFailureResult(method, failureDetail(g, state, request, err))
		},
		start: func() (proto.Message, error) {
			return protoapi.SuccessResult(method, startSuccess())
		},
		event: func() proto.Message {
			return newEvent()
		},
		completion: func() proto.Message {
			return newCompletion()
		},
		complete: func(err error) proto.Message {
			return complete(err)
		},
	}
	if scope != nil {
		binding.scope = func(message proto.Message) (routeScopeParams, error) {
			request, ok := message.(Request)
			if !ok {
				return routeScopeParams{}, fmt.Errorf("%s request type is invalid", associated.Subscribe.Name)
			}
			return scope(request)
		}
	}
	bindings[associated.Subscribe.Name] = binding
	return nil
}

func (g *Gateway) serveBinarySubscriptionRequest(
	conn rpcwire.Conn,
	ctx context.Context,
	state *connectionState,
	request gatewayBinarySubscriptionRequest,
) {
	subscription, result, transportFailure := g.startBinarySubscription(ctx, state, request)
	if transportFailure != nil {
		_ = sendTransportFailure(ctx, conn, transportFailure)
		return
	}
	if !sendBinaryResult(ctx, conn, request.binding.operation, request.call.Correlation, result) {
		if subscription != nil {
			_ = subscription.Close()
		}
		return
	}
	if subscription == nil {
		return
	}
	defer func() { _ = subscription.Close() }()
	for {
		event, err := subscription.Next(ctx)
		if err != nil {
			_ = sendBinaryNotification(
				ctx,
				conn,
				request.binding.associated.Completion,
				request.binding.complete(err),
			)
			return
		}
		if err := sendBinaryNotification(ctx, conn, request.binding.associated.Event, event); err != nil {
			_ = sendBinaryNotification(
				ctx,
				conn,
				request.binding.associated.Completion,
				request.binding.complete(err),
			)
			return
		}
	}
}

func (g *Gateway) startBinarySubscription(
	ctx context.Context,
	state *connectionState,
	request gatewayBinarySubscriptionRequest,
) (gatewayBinarySubscriber, proto.Message, *sharedpb.TransportFailure) {
	binding := request.binding
	var decoded proto.Message
	fail := func(err error) (gatewayBinarySubscriber, proto.Message, *sharedpb.TransportFailure) {
		return nil, binding.failure(g, state, decoded, err), nil
	}
	switch binding.policy {
	case gatewayBinaryCoreActiveOrdinary, gatewayBinaryCoreActiveExclusive:
		if err := g.requireCoreActive(); err != nil {
			return fail(err)
		}
	}
	if err := newRoutePolicyExecutor(g).requireAuthenticationStage(
		ctx,
		state,
		binding.operation.Options.AuthenticationStage,
	); err != nil {
		return fail(err)
	}
	payloadField := request.call.ProtoReflect().Descriptor().Fields().ByName("payload")
	if !request.call.ProtoReflect().Has(payloadField) {
		return nil, nil, invalidPayloadFailure(request.call.Correlation)
	}
	message := binding.request()
	if err := protoapi.Unmarshal(request.call.Payload, message); err != nil {
		return nil, nil, invalidPayloadFailure(request.call.Correlation)
	}
	decoded = message
	if err := protoapi.Validate(message); err != nil {
		return nil, nil, invalidPayloadFailure(request.call.Correlation)
	}
	var scopeFacts routeScopeParams
	if binding.scope != nil {
		facts, err := binding.scope(message)
		if err != nil {
			return fail(err)
		}
		scopeFacts = facts
	}
	if err := newRoutePolicyExecutor(g).authorizeScopeFacts(
		ctx,
		state,
		routeScopePolicy(binding.operation.Options.ScopePolicy),
		binding.operation.Name,
		scopeFacts,
	); err != nil {
		return fail(err)
	}
	subscription, err := binding.subscribe(g, ctx, state, message)
	if err != nil {
		return fail(err)
	}
	result, err := binding.start()
	if err != nil {
		_ = subscription.Close()
		return fail(err)
	}
	return subscription, result, nil
}

func sendBinaryResult(
	ctx context.Context,
	conn rpcwire.Conn,
	operation protoapi.Operation,
	correlation *string,
	result proto.Message,
) bool {
	frame, err := binaryPayloadEnvelope(operation, correlation, result, true)
	if err != nil {
		return false
	}
	return conn.Send(ctx, frame) == nil
}

func sendBinaryNotification(
	ctx context.Context,
	conn rpcwire.Conn,
	operation protoapi.Operation,
	payload proto.Message,
) error {
	frame, err := binaryPayloadEnvelope(operation, nil, payload, false)
	if err != nil {
		return err
	}
	return conn.Send(ctx, frame)
}

func binaryPayloadEnvelope(
	operation protoapi.Operation,
	correlation *string,
	payload proto.Message,
	result bool,
) (rpcwire.Frame, error) {
	if payload == nil {
		return rpcwire.Frame{}, fmt.Errorf("%s payload is required", operation.Name)
	}
	expected := operation.Descriptor.Input()
	if result {
		expected = operation.Descriptor.Output()
	}
	if payload.ProtoReflect().Descriptor().FullName() != expected.FullName() {
		return rpcwire.Frame{}, fmt.Errorf(
			"%s payload type %s does not match %s",
			operation.Name,
			payload.ProtoReflect().Descriptor().FullName(),
			expected.FullName(),
		)
	}
	encodedPayload, err := protoapi.Encode(payload)
	if err != nil {
		return rpcwire.Frame{}, fmt.Errorf("encode %s payload: %w", operation.Name, err)
	}
	var envelope *sharedpb.Envelope
	if result {
		envelope = &sharedpb.Envelope{Frame: &sharedpb.Envelope_Result{Result: &sharedpb.Result{
			Operation: operation.Name, Correlation: correlation, Payload: encodedPayload,
		}}}
	} else {
		envelope = &sharedpb.Envelope{
			Frame: &sharedpb.Envelope_NotificationEvent{NotificationEvent: &sharedpb.NotificationEvent{
				Operation: operation.Name, Payload: encodedPayload,
			}},
		}
	}
	encoded, err := protoapi.EncodeEnvelope(envelope)
	if err != nil {
		return rpcwire.Frame{}, err
	}
	return rpcwire.Frame{Kind: rpcwire.FrameBinary, Payload: encoded}, nil
}
