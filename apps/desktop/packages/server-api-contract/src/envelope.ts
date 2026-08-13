import {
  type Envelope,
  EnvelopeSchema,
} from "./gen/kent/api/shared/foundation_pb.js";
import {
  decodeGeneratedMessage,
  encodeGeneratedMessage,
} from "./message.js";

export function marshalEnvelope(envelope: Envelope): Uint8Array {
  return encodeGeneratedMessage(EnvelopeSchema, envelope);
}

export function unmarshalEnvelope(bytes: Uint8Array): Envelope {
  return decodeGeneratedMessage(EnvelopeSchema, bytes);
}
