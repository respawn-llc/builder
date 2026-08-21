package client

import (
	"context"
	"fmt"

	"core/shared/protoapi"
	"core/shared/rpcwire"

	serverpb "core/shared/protoapi/gen/kent/api/server"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type remoteBinaryResponse struct {
	result  *sharedpb.Result
	failure *sharedpb.TransportFailure
}

type remoteTransportFailureError struct {
	code sharedpb.TransportFailureCode
}

type comparableProtoMessage interface {
	proto.Message
	comparable
}

type generatedUnaryResult[Success any, Failure comparableProtoMessage] interface {
	proto.Message
	GetSuccess() Success
	GetError() Failure
}

type generatedServerNotReadyFailure interface {
	GetServerNotReady() *serverpb.ServerNotReadyDetails
}

func (e remoteTransportFailureError) Error() string {
	return fmt.Sprintf("binary transport failure: %s", e.code)
}

func callGeneratedBinary[
	Request proto.Message,
	Success any,
	Failure comparableProtoMessage,
	Result generatedUnaryResult[Success, Failure],
](
	c *Remote,
	ctx context.Context,
	method protoreflect.MethodDescriptor,
	request Request,
	result Result,
	decodeFailure func(Failure) error,
) (Success, error) {
	var zeroSuccess Success
	if err := c.callBinary(ctx, method, request, result); err != nil {
		return zeroSuccess, err
	}
	classified, err := protoapi.ClassifyResult(result)
	if err != nil {
		return zeroSuccess, fmt.Errorf("classify %s result: %w", method.FullName(), err)
	}
	if classified.Outcome == protoapi.OperationSuccess {
		return result.GetSuccess(), nil
	}
	if classified.Outcome == protoapi.OperationGenericFailure {
		return zeroSuccess, generatedOperationFailure(classified.Failure.Code)
	}
	var zeroFailure Failure
	failure := result.GetError()
	if failure == zeroFailure {
		return zeroSuccess, fmt.Errorf("%s classified a failure without an error value", method.FullName())
	}
	if typed, ok := any(failure).(generatedServerNotReadyFailure); ok {
		if details := typed.GetServerNotReady(); details != nil {
			return zeroSuccess, protoapi.ServerNotReadyFromProto(details)
		}
	}
	return zeroSuccess, decodeFailure(failure)
}

func (c *Remote) callBinaryControl(
	ctx context.Context,
	method protoreflect.MethodDescriptor,
	request proto.Message,
	result proto.Message,
) error {
	control, err := c.ensureControl(ctx)
	if err != nil {
		return err
	}
	return control.callBinary(ctx, method, request, result)
}

func (c *Remote) callBinary(
	ctx context.Context,
	method protoreflect.MethodDescriptor,
	request proto.Message,
	result proto.Message,
) error {
	operation, err := protoapi.OperationFromDescriptor(method)
	if err != nil {
		return err
	}
	switch operation.Options.UnaryConnection {
	case sharedpb.UnaryConnection_UNARY_CONNECTION_MULTIPLEXED:
		return c.callBinaryControl(ctx, method, request, result)
	case sharedpb.UnaryConnection_UNARY_CONNECTION_DEDICATED:
		return c.callBinaryDedicated(ctx, operation.Name, method, request, result)
	default:
		return fmt.Errorf("operation %s has no unary connection policy", operation.Name)
	}
}

func (c *Remote) callBinaryDedicated(
	ctx context.Context,
	requestID string,
	method protoreflect.MethodDescriptor,
	request proto.Message,
	result proto.Message,
) error {
	conn, cleanup, err := c.openRPCConn(ctx)
	if err != nil {
		return err
	}
	defer cleanup()
	return callBinaryRPC(ctx, conn, requestID, method, request, result)
}

func (c *remoteControlConn) callBinary(
	ctx context.Context,
	method protoreflect.MethodDescriptor,
	request proto.Message,
	result proto.Message,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	id := fmt.Sprintf("rpc-%d", c.requestID.Add(1))
	frame, operation, err := binaryCallFrame(id, method, request, result)
	if err != nil {
		return err
	}
	if operation.Options.UnaryConnection != sharedpb.UnaryConnection_UNARY_CONNECTION_MULTIPLEXED {
		return fmt.Errorf("operation %s is not multiplexed", operation.Name)
	}
	responseCh := make(chan remoteControlResponse, 1)
	if err := c.registerPending(id, responseCh); err != nil {
		return err
	}
	if err := c.conn.Send(ctx, frame); err != nil {
		c.removePending(id)
		return err
	}
	select {
	case response := <-responseCh:
		if response.err != nil {
			return response.err
		}
		if response.binary == nil {
			return fmt.Errorf("operation %s received a JSON response", operation.Name)
		}
		return decodeBinaryResponse(operation, id, response.binary, result)
	case <-ctx.Done():
		c.removePending(id)
		return ctx.Err()
	case <-c.done:
		c.removePending(id)
		return c.currentErr()
	}
}

func callBinaryRPC(
	ctx context.Context,
	conn rpcwire.Conn,
	requestID string,
	method protoreflect.MethodDescriptor,
	request proto.Message,
	result proto.Message,
) error {
	frame, operation, err := binaryCallFrame(requestID, method, request, result)
	if err != nil {
		return err
	}
	if err := conn.Send(ctx, frame); err != nil {
		return err
	}
	for {
		received, err := receiveFrame(ctx, conn)
		if err != nil {
			return err
		}
		if received.Kind != rpcwire.FrameBinary {
			return fmt.Errorf("operation %s received a JSON response", operation.Name)
		}
		response, correlation, err := decodeBinaryEnvelope(received.Payload)
		if err != nil {
			return err
		}
		if correlation != requestID {
			continue
		}
		if err := decodeBinaryResponse(operation, requestID, response, result); err != nil {
			return err
		}
		if _, err := protoapi.ClassifyResult(result); err != nil {
			return fmt.Errorf("classify %s result: %w", operation.Name, err)
		}
		return nil
	}
}

func binaryCallFrame(
	correlation string,
	method protoreflect.MethodDescriptor,
	request proto.Message,
	result proto.Message,
) (rpcwire.Frame, protoapi.Operation, error) {
	operation, err := protoapi.OperationFromDescriptor(method)
	if err != nil {
		return rpcwire.Frame{}, protoapi.Operation{}, err
	}
	if operation.Options.Kind != sharedpb.OperationKind_OPERATION_KIND_UNARY {
		return rpcwire.Frame{}, protoapi.Operation{}, fmt.Errorf("operation %s is not unary", operation.Name)
	}
	if operation.Options.Direction != sharedpb.Direction_DIRECTION_CLIENT_TO_SERVER {
		return rpcwire.Frame{}, protoapi.Operation{}, fmt.Errorf("operation %s is not client-to-server", operation.Name)
	}
	if err := requireBinaryMessageType(operation.Name, "request", request, method.Input()); err != nil {
		return rpcwire.Frame{}, protoapi.Operation{}, err
	}
	if err := requireBinaryMessageType(operation.Name, "result", result, method.Output()); err != nil {
		return rpcwire.Frame{}, protoapi.Operation{}, err
	}
	payload, err := protoapi.Encode(request)
	if err != nil {
		return rpcwire.Frame{}, protoapi.Operation{}, fmt.Errorf("encode %s request: %w", operation.Name, err)
	}
	encoded, err := protoapi.EncodeEnvelope(&sharedpb.Envelope{
		Frame: &sharedpb.Envelope_Call{Call: &sharedpb.Call{
			Operation:   operation.Name,
			Correlation: &correlation,
			Payload:     payload,
		}},
	})
	if err != nil {
		return rpcwire.Frame{}, protoapi.Operation{}, fmt.Errorf("encode %s call: %w", operation.Name, err)
	}
	return rpcwire.Frame{Kind: rpcwire.FrameBinary, Payload: encoded}, operation, nil
}

func requireBinaryMessageType(
	operation string,
	role string,
	message proto.Message,
	expected protoreflect.MessageDescriptor,
) error {
	if message == nil || !message.ProtoReflect().IsValid() {
		return fmt.Errorf("%s %s is required", operation, role)
	}
	actual := message.ProtoReflect().Descriptor()
	if actual.FullName() != expected.FullName() {
		return fmt.Errorf(
			"%s %s type %s does not match %s",
			operation,
			role,
			actual.FullName(),
			expected.FullName(),
		)
	}
	return nil
}

func decodeBinaryEnvelope(encoded []byte) (*remoteBinaryResponse, string, error) {
	envelope, err := protoapi.DecodeEnvelope(encoded)
	if err != nil {
		return nil, protoapi.DecodeEnvelopeCorrelation(encoded), err
	}
	switch frame := envelope.GetFrame().(type) {
	case *sharedpb.Envelope_Result:
		if frame.Result.Correlation == nil {
			return nil, "", fmt.Errorf("binary result correlation is required")
		}
		return &remoteBinaryResponse{result: frame.Result}, frame.Result.GetCorrelation(), nil
	case *sharedpb.Envelope_TransportFailure:
		if frame.TransportFailure.Correlation == nil {
			return nil, "", fmt.Errorf("binary transport failure correlation is required")
		}
		return &remoteBinaryResponse{failure: frame.TransportFailure}, frame.TransportFailure.GetCorrelation(), nil
	default:
		return nil, protoapi.DecodeEnvelopeCorrelation(encoded), fmt.Errorf("binary response envelope has unexpected frame type")
	}
}

func decodeBinaryResponse(
	operation protoapi.Operation,
	correlation string,
	response *remoteBinaryResponse,
	result proto.Message,
) error {
	if response == nil {
		return fmt.Errorf("operation %s response is required", operation.Name)
	}
	if response.failure != nil {
		return remoteTransportFailureError{code: response.failure.Code}
	}
	if response.result == nil {
		return fmt.Errorf("operation %s result is required", operation.Name)
	}
	if response.result.GetCorrelation() != correlation {
		return fmt.Errorf("operation %s result correlation does not match request", operation.Name)
	}
	if response.result.Operation != operation.Name {
		return fmt.Errorf(
			"operation %s received result for %s",
			operation.Name,
			response.result.Operation,
		)
	}
	payloadField := response.result.ProtoReflect().Descriptor().Fields().ByName("payload")
	if !response.result.ProtoReflect().Has(payloadField) {
		return fmt.Errorf("operation %s result payload is required", operation.Name)
	}
	if err := protoapi.Decode(response.result.Payload, result); err != nil {
		return fmt.Errorf("decode %s result: %w", operation.Name, err)
	}
	return nil
}
