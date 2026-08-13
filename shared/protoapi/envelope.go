package protoapi

import (
	"fmt"

	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/emptypb"
)

// MarshalEnvelope validates and serializes one inactive binary Server API
// envelope.
func MarshalEnvelope(envelope *sharedpb.Envelope) ([]byte, error) {
	if envelope == nil {
		return nil, fmt.Errorf("envelope is required")
	}
	if err := validateEnvelopePayload(envelope); err != nil {
		return nil, err
	}
	encoded, err := EncodeGeneratedMessage(envelope)
	if err != nil {
		return nil, fmt.Errorf("encode envelope: %w", err)
	}
	return encoded, nil
}

// UnmarshalEnvelope parses and validates one inactive binary Server API
// envelope.
func UnmarshalEnvelope(encoded []byte) (*sharedpb.Envelope, error) {
	envelope := &sharedpb.Envelope{}
	if err := DecodeGeneratedMessage(encoded, envelope); err != nil {
		return nil, fmt.Errorf("decode envelope: %w", err)
	}
	if err := validateEnvelopePayload(envelope); err != nil {
		return nil, err
	}
	return envelope, nil
}

func validateEnvelopePayload(envelope *sharedpb.Envelope) error {
	var operationName string
	var payload []byte
	var payloadField protoreflect.FieldDescriptor
	var frameKind envelopeFrameKind

	switch frame := envelope.GetFrame().(type) {
	case *sharedpb.Envelope_Call:
		operationName = frame.Call.GetOperation()
		payload = frame.Call.GetPayload()
		payloadField = frame.Call.ProtoReflect().Descriptor().Fields().ByName("payload")
		if !frame.Call.ProtoReflect().Has(payloadField) {
			return fmt.Errorf("call payload is required")
		}
		frameKind = envelopeFrameCall
	case *sharedpb.Envelope_Result:
		operationName = frame.Result.GetOperation()
		payload = frame.Result.GetPayload()
		payloadField = frame.Result.ProtoReflect().Descriptor().Fields().ByName("payload")
		if !frame.Result.ProtoReflect().Has(payloadField) {
			return fmt.Errorf("result payload is required")
		}
		frameKind = envelopeFrameResult
	case *sharedpb.Envelope_NotificationEvent:
		operationName = frame.NotificationEvent.GetOperation()
		payload = frame.NotificationEvent.GetPayload()
		payloadField = frame.NotificationEvent.ProtoReflect().Descriptor().Fields().ByName("payload")
		if !frame.NotificationEvent.ProtoReflect().Has(payloadField) {
			return fmt.Errorf("notification/event payload is required")
		}
		frameKind = envelopeFrameNotification
	default:
		return nil
	}

	operation, exists, err := OperationByName(operationName)
	if err != nil {
		return fmt.Errorf("resolve envelope operation %q: %w", operationName, err)
	}
	if !exists {
		return fmt.Errorf("envelope operation %q is unknown", operationName)
	}
	descriptor, err := envelopePayloadDescriptor(frameKind, operation)
	if err != nil {
		return fmt.Errorf("operation %q: %w", operationName, err)
	}
	if len(payload) == 0 && descriptor.FullName() != (&emptypb.Empty{}).ProtoReflect().Descriptor().FullName() {
		return fmt.Errorf("zero-byte payload is invalid for %s operation %q", descriptor.FullName(), operationName)
	}
	message := dynamicpb.NewMessage(descriptor)
	if err := DecodeGeneratedMessage(payload, message); err != nil {
		return fmt.Errorf("decode %s payload for operation %q: %w", descriptor.FullName(), operationName, err)
	}
	return nil
}

type envelopeFrameKind uint8

const (
	envelopeFrameCall envelopeFrameKind = iota + 1
	envelopeFrameResult
	envelopeFrameNotification
)

func envelopePayloadDescriptor(
	frame envelopeFrameKind,
	operation Operation,
) (protoreflect.MessageDescriptor, error) {
	options := operation.Options
	switch frame {
	case envelopeFrameCall:
		if options.Direction != sharedpb.Direction_DIRECTION_CLIENT_TO_SERVER {
			return nil, fmt.Errorf("call has wrong sender direction %s", options.Direction)
		}
		if !isCallableOperationKind(options.Kind) {
			return nil, fmt.Errorf("call cannot carry operation kind %s", options.Kind)
		}
		return operation.Descriptor.Input(), nil
	case envelopeFrameResult:
		if options.Direction != sharedpb.Direction_DIRECTION_CLIENT_TO_SERVER {
			return nil, fmt.Errorf("result has wrong operation direction %s", options.Direction)
		}
		if !isCallableOperationKind(options.Kind) {
			return nil, fmt.Errorf("result cannot carry operation kind %s", options.Kind)
		}
		return operation.Descriptor.Output(), nil
	case envelopeFrameNotification:
		if options.Direction != sharedpb.Direction_DIRECTION_SERVER_TO_CLIENT {
			return nil, fmt.Errorf("notification/event has wrong sender direction %s", options.Direction)
		}
		if options.Kind != sharedpb.OperationKind_OPERATION_KIND_NOTIFICATION {
			return nil, fmt.Errorf("notification/event cannot carry operation kind %s", options.Kind)
		}
		return operation.Descriptor.Input(), nil
	default:
		return nil, fmt.Errorf("envelope frame kind %d is invalid", frame)
	}
}

func isCallableOperationKind(kind sharedpb.OperationKind) bool {
	switch kind {
	case sharedpb.OperationKind_OPERATION_KIND_UNARY,
		sharedpb.OperationKind_OPERATION_KIND_SUBSCRIPTION,
		sharedpb.OperationKind_OPERATION_KIND_PROGRESS:
		return true
	default:
		return false
	}
}
