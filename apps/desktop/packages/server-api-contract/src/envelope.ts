import { isFieldSet } from "@bufbuild/protobuf";
import { EmptySchema } from "@bufbuild/protobuf/wkt";
import {
  CallSchema,
  type Envelope,
  EnvelopeSchema,
  NotificationEventSchema,
  ResultSchema,
} from "./gen/kent/api/shared/foundation_pb.js";
import {
  decodeGeneratedMessage,
  encodeGeneratedMessage,
} from "./message.js";
import { operationByName } from "./policy.js";

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
  const descriptor = frame.case === "result"
    ? operation.descriptor.output
    : operation.descriptor.input;
  const payloadDescriptor = foundationPayloadField(frame.case);
  const payload = frame.value.payload;
  if (payload === undefined || !isFieldSet(frame.value, payloadDescriptor)) {
    throw new Error(`${frame.case} payload is required`);
  }
  if (
    payload.length === 0 &&
    descriptor.typeName !== EmptySchema.typeName
  ) {
    throw new Error(
      `zero-byte payload is invalid for ${descriptor.typeName} operation ${frame.value.operation}`,
    );
  }
  decodeGeneratedMessage(descriptor, payload);
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
