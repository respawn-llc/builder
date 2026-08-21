import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { type Envelope, EnvelopeSchema } from "./gen/kent/api/shared/foundation_pb.js";
import { decode, decodeUnvalidated, encode } from "./message.js";

export function decodeEnvelope(bytes: Uint8Array): Envelope {
  return decode(EnvelopeSchema, bytes);
}

export function decodeEnvelopeCorrelation(bytes: Uint8Array): string | undefined {
  let envelope: Envelope;
  try {
    envelope = decodeUnvalidated(EnvelopeSchema, bytes);
  } catch {
    return undefined;
  }
  const correlation =
    envelope.frame.case === "call" ||
    envelope.frame.case === "result" ||
    envelope.frame.case === "transportFailure"
      ? envelope.frame.value.correlation
      : undefined;
  return correlation === undefined || correlation.trim().length === 0 ? undefined : correlation;
}

export function encodeEnvelope(envelope: MessageInitShape<typeof EnvelopeSchema>): Uint8Array {
  return encode(EnvelopeSchema, create(EnvelopeSchema, envelope));
}
