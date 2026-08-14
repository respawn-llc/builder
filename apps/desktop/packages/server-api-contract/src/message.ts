import {
  createRegistry,
  fromBinary,
  toBinary,
  type DescMessage,
  type MessageShape,
} from "@bufbuild/protobuf";
import { createValidator } from "@bufbuild/protovalidate";
import { canonical_uuid_v4 } from "./gen/kent/api/shared/validation_pb.js";

const validator = createValidator({
  registry: createRegistry(canonical_uuid_v4),
});

export function decodeGeneratedMessage<Descriptor extends DescMessage>(
  descriptor: Descriptor,
  bytes: Uint8Array,
): MessageShape<Descriptor> {
  const message = fromBinary(descriptor, bytes, {
    readUnknownFields: true,
  });
  validateGeneratedMessage(descriptor, message);
  return message;
}

export function encodeGeneratedMessage<Descriptor extends DescMessage>(
  descriptor: Descriptor,
  message: MessageShape<Descriptor>,
): Uint8Array {
  validateGeneratedMessage(descriptor, message);
  return toBinary(descriptor, message, {
    writeUnknownFields: true,
  });
}

export function validateGeneratedMessage<Descriptor extends DescMessage>(
  descriptor: Descriptor,
  message: MessageShape<Descriptor>,
): void {
  const result = validator.validate(descriptor, message);
  if (result.kind !== "valid") {
    throw result.error;
  }
}
