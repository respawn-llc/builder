import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { type Envelope, EnvelopeSchema } from "./gen/kent/api/shared/foundation_pb.js";
import { decode, encode } from "./message.js";

export function decodeEnvelope(bytes: Uint8Array): Envelope {
  return decode(EnvelopeSchema, bytes);
}

export function encodeEnvelope(envelope: MessageInitShape<typeof EnvelopeSchema>): Uint8Array {
  return encode(EnvelopeSchema, create(EnvelopeSchema, envelope));
}
