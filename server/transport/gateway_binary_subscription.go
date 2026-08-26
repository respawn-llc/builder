package transport

import (
	"context"
	"fmt"

	"core/shared/invariant"
	"core/shared/protoapi"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"core/shared/rpcwire"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type gatewayBinarySubscription interface {
	Close() error
}

type gatewayBinarySubscriptionBinding struct {
	operation  protoapi.Operation
	policy     gatewayBinaryExecutionPolicy
	request    func() proto.Message
	scope      func(proto.Message) (routeScopeParams, error)
	start      func() (proto.Message, error)
	subscribe  func(*Gateway, context.Context, *connectionState, proto.Message) (gatewayBinarySubscription, error)
	failure    func(*Gateway, *connectionState, proto.Message, error) proto.Message
	event      func() proto.Message
	next       func(context.Context, gatewayBinarySubscription) (proto.Message, error)
	completion func() proto.Message
	complete   func(error) (proto.Message, error)
}

type gatewayBinarySubscriptionRequest struct {
	binding gatewayBinarySubscriptionBinding
	call    *sharedpb.Call
}

func registerGatewayBinarySubscription[
	Request proto.Message,
	StartSuccess proto.Message,
	Event any,
	EventMessage proto.Message,
	CompletionMessage proto.Message,
	Subscription gatewaySubscription[Event],
](
	bindings map[string]gatewayBinarySubscriptionBinding,
	service protoreflect.ServiceDescriptor,
	methodName protoreflect.Name,
	policy gatewayBinaryExecutionPolicy,
	newRequest func() Request,
	scope func(Request) (routeScopeParams, error),
	newStartSuccess func() StartSuccess,
	subscribe func(*Gateway, context.Context, *connectionState, Request) (Subscription, error),
	newEvent func() EventMessage,
	encodeEvent func(Event) (EventMessage, error),
	newCompletion func() CompletionMessage,
	encodeCompletion func(error) (CompletionMessage, error),
	failureDetail func(*Gateway, *connectionState, Request, error) proto.Message,
) error {
	if service == nil {
		return fmt.Errorf("generated service descriptor is required")
	}
	if newRequest == nil ||
		newStartSuccess == nil ||
		subscribe == nil ||
		newEvent == nil ||
		encodeEvent == nil ||
		newCompletion == nil ||
		encodeCompletion == nil ||
		failureDetail == nil {
		return fmt.Errorf("generated %s.%s subscription binding is incomplete", service.Name(), methodName)
	}
	method := service.Methods().ByName(methodName)
	if method == nil {
		return fmt.Errorf("generated %s.%s descriptor is required", service.Name(), methodName)
	}
	operation, err := protoapi.OperationFromDescriptor(method)
	if err != nil {
		return err
	}
	bindings[operation.Name] = gatewayBinarySubscriptionBinding{
		operation: operation,
		policy:    policy,
		request: func() proto.Message {
			return newRequest()
		},
		start: func() (proto.Message, error) {
			return protoapi.SuccessResult(method, newStartSuccess())
		},
		subscribe: func(
			g *Gateway,
			ctx context.Context,
			state *connectionState,
			message proto.Message,
		) (gatewayBinarySubscription, error) {
			request, ok := message.(Request)
			if !ok {
				return nil, fmt.Errorf("%s request type is invalid", operation.Name)
			}
			return subscribe(g, ctx, state, request)
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
						failureDetail(g, state, request, fmt.Errorf("%s request type is invalid: %w", operation.Name, err)),
					)
				}
				request = typed
			}
			return gatewayBinaryFailureResult(method, failureDetail(g, state, request, err))
		},
		event: func() proto.Message {
			return newEvent()
		},
		next: func(ctx context.Context, subscription gatewayBinarySubscription) (proto.Message, error) {
			typed, ok := subscription.(Subscription)
			if !ok {
				return nil, fmt.Errorf("%s subscription type is invalid", operation.Name)
			}
			event, err := typed.Next(ctx)
			if err != nil {
				return nil, err
			}
			return encodeEvent(event)
		},
		completion: func() proto.Message {
			return newCompletion()
		},
		complete: func(err error) (proto.Message, error) {
			return encodeCompletion(err)
		},
	}
	if scope != nil {
		binding := bindings[operation.Name]
		binding.scope = func(message proto.Message) (routeScopeParams, error) {
			request, ok := message.(Request)
			if !ok {
				return routeScopeParams{}, fmt.Errorf("%s request type is invalid", operation.Name)
			}
			return scope(request)
		}
		bindings[operation.Name] = binding
	}
	return nil
}

func (g *Gateway) serveBinarySubscriptionRequest(
	conn rpcwire.Conn,
	ctx context.Context,
	state *connectionState,
	request gatewayBinarySubscriptionRequest,
) {
	subscription, result, transportFailure := g.dispatchBinarySubscriptionStart(ctx, state, request)
	if transportFailure != nil {
		_ = sendTransportFailure(ctx, conn, transportFailure)
		return
	}
	if subscription != nil {
		defer func() { _ = subscription.Close() }()
	}
	encoded, err := encodeBinarySubscriptionStartResult(request, result)
	if err != nil {
		invariant.NewPolicy().Check(false, invariant.FailureDiagnostic(
			invariant.ScopeServerAPIContract,
			request.binding.operation.Name,
			err,
		))
		result = request.binding.failure(g, state, nil, fmt.Errorf("encode subscription start result: %w", err))
		encoded, err = encodeBinarySubscriptionStartResult(request, result)
		if err != nil {
			return
		}
	}
	if err := conn.Send(ctx, rpcwire.Frame{Kind: rpcwire.FrameBinary, Payload: encoded}); err != nil ||
		subscription == nil {
		return
	}
	associations, err := g.registration.binarySubscriptionAssociations(request.binding)
	if err != nil {
		invariant.NewPolicy().Check(false, invariant.FailureDiagnostic(
			invariant.ScopeServerAPIContract,
			request.binding.operation.Name,
			err,
		))
		return
	}
	for {
		event, nextErr := request.binding.next(ctx, subscription)
		if nextErr != nil {
			completion, completionErr := request.binding.complete(nextErr)
			if completionErr != nil {
				invariant.NewPolicy().Check(false, invariant.FailureDiagnostic(
					invariant.ScopeServerAPIContract,
					associations.completion.Name,
					completionErr,
				))
				return
			}
			_ = sendBinaryNotification(ctx, conn, associations.completion, completion)
			return
		}
		if !sendBinaryNotification(ctx, conn, associations.event, event) {
			return
		}
	}
}

func (g *Gateway) dispatchBinarySubscriptionStart(
	ctx context.Context,
	state *connectionState,
	request gatewayBinarySubscriptionRequest,
) (gatewayBinarySubscription, proto.Message, *sharedpb.TransportFailure) {
	binding := request.binding
	var decoded proto.Message
	fail := func(err error) (gatewayBinarySubscription, proto.Message, *sharedpb.TransportFailure) {
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
	if err := protoapi.Decode(request.call.Payload, message); err != nil {
		return nil, nil, invalidPayloadFailure(request.call.Correlation)
	}
	decoded = message
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

func encodeBinarySubscriptionStartResult(
	request gatewayBinarySubscriptionRequest,
	result proto.Message,
) ([]byte, error) {
	if result == nil {
		return nil, fmt.Errorf("subscription start result is required")
	}
	if result.ProtoReflect().Descriptor().FullName() != request.binding.operation.Descriptor.Output().FullName() {
		return nil, fmt.Errorf(
			"subscription start result type %s does not match %s",
			result.ProtoReflect().Descriptor().FullName(),
			request.binding.operation.Descriptor.Output().FullName(),
		)
	}
	payload, err := protoapi.Encode(result)
	if err != nil {
		return nil, err
	}
	presentPayload := make([]byte, len(payload))
	copy(presentPayload, payload)
	return protoapi.EncodeEnvelope(&sharedpb.Envelope{
		Frame: &sharedpb.Envelope_Result{Result: &sharedpb.Result{
			Operation:   request.binding.operation.Name,
			Correlation: request.call.Correlation,
			Payload:     presentPayload,
		}},
	})
}

func sendBinaryNotification(
	ctx context.Context,
	conn rpcwire.Conn,
	operation protoapi.Operation,
	message proto.Message,
) bool {
	if message == nil ||
		message.ProtoReflect().Descriptor().FullName() != operation.Descriptor.Input().FullName() {
		return false
	}
	payload, err := protoapi.Encode(message)
	if err != nil {
		return false
	}
	presentPayload := make([]byte, len(payload))
	copy(presentPayload, payload)
	encoded, err := protoapi.EncodeEnvelope(&sharedpb.Envelope{
		Frame: &sharedpb.Envelope_NotificationEvent{NotificationEvent: &sharedpb.NotificationEvent{
			Operation: operation.Name,
			Payload:   presentPayload,
		}},
	})
	if err != nil {
		return false
	}
	return conn.Send(ctx, rpcwire.Frame{Kind: rpcwire.FrameBinary, Payload: encoded}) == nil
}
