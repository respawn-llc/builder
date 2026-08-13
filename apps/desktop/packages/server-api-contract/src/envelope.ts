import { isFieldSet } from "@bufbuild/protobuf";
import {
  CallSchema,
  Direction,
  type Envelope,
  EnvelopeSchema,
  NotificationEventSchema,
  OperationKind,
  ResultSchema,
} from "./gen/kent/api/shared/foundation_pb.js";
import {
  decodeGeneratedMessage,
  encodeGeneratedMessage,
} from "./message.js";
import { operationByName } from "./policy.js";
import { classifyOperationResult } from "./result.js";

export function marshalEnvelope(envelope: Envelope): Uint8Array {
  validateEnvelopePayload(envelope);
  return encodeGeneratedMessage(EnvelopeSchema, envelope);
}

export function unmarshalEnvelope(bytes: Uint8Array): Envelope {
  const envelope = decodeGeneratedMessage(EnvelopeSchema, bytes);
  validateEnvelopePayload(envelope);
  return envelope;
}

function validateEnvelopePayload(envelope: Envelope): void {
  const frame = envelope.frame;
  if (frame.case === "transportFailure" || frame.case === undefined) {
    return;
  }
  const operation = operationByName(frame.value.operation);
  if (operation === undefined) {
    throw new Error(`envelope operation ${frame.value.operation} is unknown`);
  }
  const descriptor = envelopePayloadDescriptor(frame.case, operation);
  const payloadDescriptor = foundationPayloadField(frame.case);
  const payload = frame.value.payload;
  if (payload === undefined || !isFieldSet(frame.value, payloadDescriptor)) {
    throw new Error(`${frame.case} payload is required`);
  }
  const message = decodeGeneratedMessage(descriptor, payload);
  if (frame.case === "result") {
    classifyOperationResult(descriptor, message);
  }
}

function envelopePayloadDescriptor(
  frame: "call" | "result" | "notificationEvent",
  operation: NonNullable<ReturnType<typeof operationByName>>,
) {
  switch (frame) {
    case "call":
      requireCallableOperation(frame, operation.options.kind);
      if (operation.options.direction !== Direction.CLIENT_TO_SERVER) {
        throw new Error(
          `call has wrong sender direction ${String(operation.options.direction)}`,
        );
      }
      return operation.descriptor.input;
    case "result":
      requireCallableOperation(frame, operation.options.kind);
      if (operation.options.direction !== Direction.CLIENT_TO_SERVER) {
        throw new Error(
          `result has wrong operation direction ${String(operation.options.direction)}`,
        );
      }
      return operation.descriptor.output;
    case "notificationEvent":
      if (operation.options.kind !== OperationKind.NOTIFICATION) {
        throw new Error(
          `notification/event cannot carry operation kind ${String(operation.options.kind)}`,
        );
      }
      if (operation.options.direction !== Direction.SERVER_TO_CLIENT) {
        throw new Error(
          `notification/event has wrong sender direction ${String(operation.options.direction)}`,
        );
      }
      return operation.descriptor.input;
  }
}

function requireCallableOperation(
  frame: "call" | "result",
  kind: OperationKind,
): void {
  switch (kind) {
    case OperationKind.UNARY:
    case OperationKind.SUBSCRIPTION:
    case OperationKind.PROGRESS:
      return;
    case OperationKind.UNSPECIFIED:
    case OperationKind.NOTIFICATION:
      throw new Error(`${frame} cannot carry operation kind ${String(kind)}`);
  }
}

function foundationPayloadField(frame: "call" | "result" | "notificationEvent") {
  switch (frame) {
    case "call":
      return CallSchema.field.payload;
    case "result":
      return ResultSchema.field.payload;
    case "notificationEvent":
      return NotificationEventSchema.field.payload;
  }
}
